// Package audit appends to a tamper-evident log.
//
// Each entry carries the hash of the one before it, so removing or altering any
// entry breaks every hash after it. This is tamper-*evident*, not tamper-proof:
// anyone with the database owner's credentials could rebuild the whole chain.
// What it defeats is quiet edits -- and combined with the application role
// holding only INSERT and SELECT, the process handling personal data cannot
// rewrite its own record of what it did.
package audit

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/collectr/collectr/internal/contracts"
	"github.com/collectr/collectr/internal/platform/postgres"
)

// Actions worth recording. Reads are here as well as writes: bulk access to
// personal data is exactly what an investigation needs to reconstruct.
const (
	ActionSubmissionCreated = "submission.created"
	ActionSubmissionUpdated = "submission.updated"
	ActionSubmissionErased  = "submission.erased"
	ActionSubmissionReadAll = "submission.read_bulk"
	ActionConsentGranted    = "consent.granted"
	ActionConsentWithdrawn  = "consent.withdrawn"
	ActionDSRReceived       = "dsr.received"
	ActionDSRFulfilled      = "dsr.fulfilled"
	ActionFormPublished     = "form.published"
	ActionRetentionPurge    = "retention.purge"
	ActionSensitiveRevealed = "submission.sensitive_revealed"
)

// Writer appends entries to the chain.
type Writer struct {
	db *postgres.DB
}

// NewWriter returns a Writer.
func NewWriter(db *postgres.DB) *Writer { return &Writer{db: db} }

// genesisHash starts each tenant's chain. Any fixed value works; what matters is
// that it never changes, since every later hash depends on it.
var genesisHash = sha256.Sum256([]byte("collectr.audit.genesis.v1"))

// Write appends one entry inside the caller's transaction.
//
// The JSON columns are `json`, not `jsonb`, and the values are bound as text.
// jsonb would reorder keys and renormalise numbers on the way in, so the bytes
// verification reads back would differ from the bytes that were hashed -- and a
// chain that reports tampering when nothing was tampered with is worse than no
// chain at all.
//
// The audit line and the action it describes commit together: an entry for work
// that rolled back would be a lie, and work with no entry would be invisible.
func (w *Writer) Write(ctx context.Context, tx pgx.Tx, e contracts.AuditEntry) error {
	// Serialise appends per tenant. Two concurrent writers reading the same
	// previous hash would fork the chain, and a forked chain fails verification
	// exactly as tampering does -- indistinguishable, and far more common.
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", lockKey(e.TenantID)); err != nil {
		return fmt.Errorf("locking audit chain: %w", err)
	}

	var (
		seq      int64
		prevHash []byte
	)
	err := tx.QueryRow(ctx, `
		SELECT seq, hash FROM audit.entries
		WHERE tenant_id = $1
		ORDER BY seq DESC
		LIMIT 1`, e.TenantID).Scan(&seq, &prevHash)
	switch {
	case postgres.IsNoRows(err):
		seq, prevHash = 0, genesisHash[:]
	case err != nil:
		return fmt.Errorf("reading audit head: %w", err)
	}
	seq++

	actor, err := json.Marshal(e.Actor)
	if err != nil {
		return fmt.Errorf("encoding audit actor: %w", err)
	}
	target, err := json.Marshal(orEmpty(e.Target))
	if err != nil {
		return fmt.Errorf("encoding audit target: %w", err)
	}
	payload, err := json.Marshal(orEmpty(e.Payload))
	if err != nil {
		return fmt.Errorf("encoding audit payload: %w", err)
	}

	hash := chainHash(prevHash, e.TenantID, seq, e.Action, actor, target, payload)

	if _, err := tx.Exec(ctx, `
		INSERT INTO audit.entries
			(tenant_id, seq, actor, action, target, payload, prev_hash, hash)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		e.TenantID, seq, string(actor), e.Action, string(target), string(payload), prevHash, hash,
	); err != nil {
		return fmt.Errorf("appending audit entry: %w", err)
	}
	return nil
}

// chainHash computes an entry's hash from its predecessor and its own content.
//
// Fields are length-prefixed so that no two different entries can serialise to
// the same bytes: without it, moving a character between two adjacent fields
// would leave the hash unchanged.
func chainHash(prev []byte, tenantID uuid.UUID, seq int64, action string, actor, target, payload []byte) []byte {
	h := sha256.New()
	h.Write(prev)
	writeField(h, tenantID[:])
	writeField(h, fmt.Appendf(nil, "%d", seq))
	writeField(h, []byte(action))
	writeField(h, actor)
	writeField(h, target)
	writeField(h, payload)
	return h.Sum(nil)
}

func writeField(h io.Writer, b []byte) {
	// hash.Hash never returns an error, which is why these are unchecked.
	_, _ = h.Write(fmt.Appendf(nil, "%d:", len(b)))
	_, _ = h.Write(b)
}

// VerifyResult reports the state of one tenant's chain.
type VerifyResult struct {
	TenantID uuid.UUID `json:"tenant_id"`
	Entries  int64     `json:"entries"`
	Valid    bool      `json:"valid"`
	// BrokenAt is the sequence number where verification first failed.
	BrokenAt int64  `json:"broken_at,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

// Verify walks a tenant's chain and recomputes every hash.
func (w *Writer) Verify(ctx context.Context, tenantID uuid.UUID) (VerifyResult, error) {
	rows, err := w.db.Query(ctx, `
		SELECT seq, actor, action, target, payload, prev_hash, hash
		FROM audit.entries
		WHERE tenant_id = $1
		ORDER BY seq`, tenantID)
	if err != nil {
		return VerifyResult{}, fmt.Errorf("reading audit chain: %w", err)
	}
	defer rows.Close()

	res := VerifyResult{TenantID: tenantID, Valid: true}
	expectedPrev := genesisHash[:]
	var expectedSeq int64 = 1

	for rows.Next() {
		var (
			seq                    int64
			actor, target, payload string
			storedPrev, storedHash []byte
			action                 string
		)
		if err := rows.Scan(&seq, &actor, &action, &target, &payload, &storedPrev, &storedHash); err != nil {
			return VerifyResult{}, fmt.Errorf("scanning audit entry: %w", err)
		}
		res.Entries++

		// A gap means an entry was deleted; the remaining hashes could still be
		// self-consistent, so the sequence has to be checked separately.
		if seq != expectedSeq {
			return brokenAt(res, seq, fmt.Sprintf("sequence jumps from %d to %d: an entry was removed", expectedSeq-1, seq)), nil
		}
		if !bytes.Equal(storedPrev, expectedPrev) {
			return brokenAt(res, seq, "recorded previous hash does not match the actual previous entry"), nil
		}
		want := chainHash(storedPrev, tenantID, seq, action, []byte(actor), []byte(target), []byte(payload))
		if !bytes.Equal(want, storedHash) {
			return brokenAt(res, seq, "entry content does not match its hash: the row was altered"), nil
		}

		expectedPrev = storedHash
		expectedSeq++
	}
	if err := rows.Err(); err != nil {
		return VerifyResult{}, fmt.Errorf("iterating audit chain: %w", err)
	}
	return res, nil
}

func brokenAt(res VerifyResult, seq int64, reason string) VerifyResult {
	res.Valid = false
	res.BrokenAt = seq
	res.Reason = reason
	return res
}

// lockKey maps a tenant id onto the advisory lock space.
func lockKey(tenantID uuid.UUID) int64 {
	h := fnv.New64a()
	_, _ = h.Write(tenantID[:])
	return int64(h.Sum64() >> 1) // keep it positive
}

func orEmpty(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}
