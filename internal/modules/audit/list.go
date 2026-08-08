package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Entry is one line of the trail as it goes out over the API.
type Entry struct {
	Seq        int64           `json:"seq"`
	Action     string          `json:"action"`
	ActorType  string          `json:"actor_type"`
	ActorID    string          `json:"actor_id"`
	ActorEmail string          `json:"actor_email,omitempty"`
	ActorRole  string          `json:"actor_role,omitempty"`
	IPPrefix   string          `json:"ip_prefix,omitempty"`
	Target     json.RawMessage `json:"target"`
	Payload    json.RawMessage `json:"payload,omitempty"`
	OccurredAt time.Time       `json:"occurred_at"`
	Hash       string          `json:"hash"`
}

// ListFilter narrows the trail.
type ListFilter struct {
	// Before is the seq to page back from; zero starts at the newest.
	Before int64
	Actor  string
	Action string
	From   time.Time
	To     time.Time
	Limit  int
}

// List returns entries newest first.
//
// The actor is projected into named fields rather than passed through as stored.
// The column holds whatever shape the writing struct serialised to at the time,
// and that has already changed once; a response built from the raw column would
// change its key names with it. Reading both spellings here keeps one stable
// contract over a corpus written by more than one version.
func (w *Writer) List(ctx context.Context, tenantID uuid.UUID, f ListFilter) ([]Entry, error) {
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 50
	}

	const q = `
		SELECT e.seq, e.action,
		       coalesce(e.actor->>'Type', e.actor->>'type', ''),
		       coalesce(e.actor->>'ID', e.actor->>'id', ''),
		       coalesce(e.actor->>'IPPrefix', e.actor->>'ip_prefix', ''),
		       e.target::text, coalesce(e.payload::text, ''),
		       e.occurred_at, encode(e.hash, 'hex'),
		       coalesce(u.email, ''), coalesce(m.role, '')
		FROM audit.entries e
		LEFT JOIN iam.users u
		       ON u.id = nullif(coalesce(e.actor->>'ID', e.actor->>'id'), '')::uuid
		LEFT JOIN iam.org_members m ON m.user_id = u.id AND m.tenant_id = e.tenant_id
		WHERE e.tenant_id = $1
		  AND ($2::bigint = 0 OR e.seq < $2)
		  AND ($3 = '' OR coalesce(e.actor->>'ID', e.actor->>'id') = $3)
		  AND ($4 = '' OR e.action = $4)
		  AND ($5::timestamptz IS NULL OR e.occurred_at >= $5)
		  AND ($6::timestamptz IS NULL OR e.occurred_at < $6)
		ORDER BY e.seq DESC
		LIMIT $7`

	var from, to *time.Time
	if !f.From.IsZero() {
		from = &f.From
	}
	if !f.To.IsZero() {
		to = &f.To
	}

	var out []Entry
	err := w.db.InTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, q, tenantID, f.Before, f.Actor, f.Action, from, to, f.Limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var (
				e               Entry
				target, payload string
			)
			if err := rows.Scan(&e.Seq, &e.Action, &e.ActorType, &e.ActorID, &e.IPPrefix,
				&target, &payload, &e.OccurredAt, &e.Hash, &e.ActorEmail, &e.ActorRole); err != nil {
				return err
			}
			e.Target = json.RawMessage(target)
			if payload != "" {
				e.Payload = json.RawMessage(payload)
			}
			out = append(out, e)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("listing audit entries: %w", err)
	}
	return out, nil
}

// Actions returns the distinct action names present, for a filter control that
// offers what actually happened rather than a hardcoded list that drifts.
func (w *Writer) Actions(ctx context.Context, tenantID uuid.UUID) ([]string, error) {
	var out []string
	err := w.db.InTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT DISTINCT action FROM audit.entries WHERE tenant_id = $1 ORDER BY action`, tenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var a string
			if err := rows.Scan(&a); err != nil {
				return err
			}
			out = append(out, a)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("listing audit actions: %w", err)
	}
	return out, nil
}
