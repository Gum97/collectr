// Package store persists consent documents, subjects and records.
package store

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/collectr/collectr/internal/contracts"
	"github.com/collectr/collectr/internal/platform/crypto"
	"github.com/collectr/collectr/internal/platform/postgres"
)

// Errors returned by the consent store.
var (
	ErrDocumentNotFound = errors.New("consent document not found")
	ErrPurposeNotFound  = errors.New("consent purpose not found")
	ErrSubjectErased    = errors.New("data subject has been erased")
)

// Store reads and writes the consent schema.
type Store struct {
	db     *postgres.DB
	env    *crypto.Envelope
	pepper []byte
}

// New returns a Store. The pepper keys the subject identifier hash, so the same
// email address produces different hashes on different deployments.
func New(db *postgres.DB, env *crypto.Envelope, pepper []byte) *Store {
	return &Store{db: db, env: env, pepper: pepper}
}

// UpsertSubject finds or creates the data subject for an identifying value.
//
// Only a keyed hash is stored. The table can still answer "is this the same
// person as last time" without being a readable directory of everyone's contact
// details -- which matters because this table, unlike the submissions, spans
// every form a tenant runs.
func (s *Store) UpsertSubject(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, kind, value string) (contracts.Subject, error) {
	hash := s.identifierHash(kind, value)

	const q = `
		INSERT INTO consent.data_subjects (id, tenant_id, identifier_hash, identifier_kind)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (tenant_id, identifier_kind, identifier_hash) DO UPDATE
			SET identifier_kind = EXCLUDED.identifier_kind
		RETURNING id, erased_at IS NOT NULL`

	var sub contracts.Subject
	sub.TenantID, sub.Kind = tenantID, kind
	sub.IdentifierHash = hash
	if err := tx.QueryRow(ctx, q, uuid.New(), tenantID, hash, kind).Scan(&sub.ID, &sub.Erased); err != nil {
		return contracts.Subject{}, fmt.Errorf("upserting data subject: %w", err)
	}
	return sub, nil
}

// FindSubject looks up a subject without creating one.
//
// An unknown identifier is not an error: it is the normal case for the portal's
// identify endpoint, which must behave identically whether or not the person is
// known.
func (s *Store) FindSubject(ctx context.Context, tenantID uuid.UUID, kind, value string) (contracts.Subject, error) {
	hash := s.identifierHash(kind, value)

	const q = `
		SELECT id, erased_at IS NOT NULL
		FROM consent.data_subjects
		WHERE tenant_id = $1 AND identifier_kind = $2 AND identifier_hash = $3`

	sub := contracts.Subject{TenantID: tenantID, Kind: kind, IdentifierHash: hash}
	err := s.db.InTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, q, tenantID, kind, hash).Scan(&sub.ID, &sub.Erased)
	})
	if postgres.IsNoRows(err) {
		return contracts.Subject{IdentifierHash: hash}, nil
	}
	if err != nil {
		return contracts.Subject{}, fmt.Errorf("finding data subject: %w", err)
	}
	return sub, nil
}

// identifierHash normalises then keys the identifying value.
//
// Normalisation matters as much as the hash: "A@Example.VN " and "a@example.vn"
// are the same person, and a subject-access request that misses half of
// someone's records because of a capital letter is a failure to answer it.
func (s *Store) identifierHash(kind, value string) []byte {
	v := strings.TrimSpace(strings.ToLower(value))
	if kind == "phone" {
		var digits strings.Builder
		for _, r := range v {
			if r >= '0' && r <= '9' {
				digits.WriteRune(r)
			}
		}
		v = digits.String()
		// Vietnamese numbers appear as 0901..., +84901... and 84901...; all three
		// are one person.
		v = strings.TrimPrefix(v, "84")
		v = strings.TrimPrefix(v, "0")
	}
	mac := hmac.New(sha256.New, s.pepper)
	mac.Write([]byte(kind + ":" + v))
	return mac.Sum(nil)
}

// DataKey returns the subject's data key, creating and wrapping one on first use.
func (s *Store) DataKey(ctx context.Context, tx pgx.Tx, subjectID uuid.UUID) ([]byte, error) {
	var (
		wrapped []byte
		erased  bool
	)
	err := tx.QueryRow(ctx,
		`SELECT dek_wrapped, erased_at IS NOT NULL FROM consent.data_subjects WHERE id = $1 FOR UPDATE`,
		subjectID,
	).Scan(&wrapped, &erased)
	if postgres.IsNoRows(err) {
		return nil, fmt.Errorf("data subject %s not found", subjectID)
	}
	if err != nil {
		return nil, fmt.Errorf("reading data key: %w", err)
	}
	if erased {
		return nil, ErrSubjectErased
	}

	if len(wrapped) > 0 {
		dek, err := s.env.Unwrap(wrapped)
		if err != nil {
			return nil, fmt.Errorf("unwrapping data key for subject %s: %w", subjectID, err)
		}
		return dek, nil
	}

	dek, err := crypto.NewDataKey()
	if err != nil {
		return nil, err
	}
	wrapped, err = s.env.Wrap(dek)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE consent.data_subjects SET dek_wrapped = $2 WHERE id = $1`, subjectID, wrapped,
	); err != nil {
		return nil, fmt.Errorf("storing wrapped data key: %w", err)
	}
	return dek, nil
}

// Shred destroys a subject's data key, rendering their sensitive data
// permanently unreadable everywhere it exists, backups included.
func (s *Store) Shred(ctx context.Context, tx pgx.Tx, subjectID uuid.UUID) error {
	if _, err := tx.Exec(ctx,
		`UPDATE consent.data_subjects
		 SET dek_wrapped = NULL, identifier_hash = sha256(id::text::bytea), erased_at = now()
		 WHERE id = $1`, subjectID,
	); err != nil {
		return fmt.Errorf("shredding data key: %w", err)
	}
	return nil
}

// ActiveDocument returns the newest published document of a kind.
func (s *Store) ActiveDocument(ctx context.Context, tenantID uuid.UUID, kind string) (contracts.DocumentRef, error) {
	const q = `
		SELECT id, content_hash, version_no
		FROM consent.documents
		WHERE tenant_id = $1 AND kind = $2 AND effective_from <= now()
		ORDER BY version_no DESC
		LIMIT 1`

	var ref contracts.DocumentRef
	err := s.db.InTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, q, tenantID, kind).Scan(&ref.ID, &ref.ContentHash, &ref.VersionNo)
	})
	if postgres.IsNoRows(err) {
		return contracts.DocumentRef{}, ErrDocumentNotFound
	}
	if err != nil {
		return contracts.DocumentRef{}, fmt.Errorf("getting active consent document: %w", err)
	}
	return ref, nil
}

// PublicDocument returns a document by id for the permalink.
type PublicDocument struct {
	ID        uuid.UUID
	Kind      string
	VersionNo int
	BodyHTML  string
	Hash      []byte
}

// PublicDocument reads one immutable document without a tenant scope.
func (s *Store) PublicDocument(ctx context.Context, id uuid.UUID) (PublicDocument, error) {
	const q = `
		SELECT id, kind, version_no, body_html, content_hash
		FROM consent.public_document($1)`

	var d PublicDocument
	err := s.db.QueryRow(ctx, q, id).Scan(&d.ID, &d.Kind, &d.VersionNo, &d.BodyHTML, &d.Hash)
	if postgres.IsNoRows(err) {
		return PublicDocument{}, ErrDocumentNotFound
	}
	if err != nil {
		return PublicDocument{}, fmt.Errorf("getting public document: %w", err)
	}
	return d, nil
}

// CreateDocument publishes a new immutable version of a consent text.
func (s *Store) CreateDocument(ctx context.Context, tenantID, createdBy uuid.UUID, kind, body string) (contracts.DocumentRef, error) {
	sum := sha256.Sum256([]byte(body))

	var ref contracts.DocumentRef
	err := s.db.InTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		var next int
		if err := tx.QueryRow(ctx,
			`SELECT coalesce(max(version_no), 0) + 1 FROM consent.documents WHERE tenant_id = $1 AND kind = $2`,
			tenantID, kind,
		).Scan(&next); err != nil {
			return err
		}
		ref = contracts.DocumentRef{ID: uuid.New(), ContentHash: sum[:], VersionNo: next}
		_, err := tx.Exec(ctx, `
			INSERT INTO consent.documents
				(id, tenant_id, kind, version_no, body_html, content_hash, created_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			ref.ID, tenantID, kind, next, body, sum[:], createdBy)
		return err
	})
	if err != nil {
		return contracts.DocumentRef{}, fmt.Errorf("creating consent document: %w", err)
	}
	return ref, nil
}

// PurposeIDs resolves purpose codes to ids.
func (s *Store) PurposeIDs(ctx context.Context, tenantID uuid.UUID, codes []string) (map[string]uuid.UUID, error) {
	const q = `SELECT code, id FROM consent.purposes WHERE tenant_id = $1 AND code = ANY($2)`

	out := make(map[string]uuid.UUID, len(codes))
	err := s.db.InTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, q, tenantID, codes)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var (
				code string
				id   uuid.UUID
			)
			if err := rows.Scan(&code, &id); err != nil {
				return err
			}
			out[code] = id
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("resolving purposes: %w", err)
	}
	return out, nil
}

// Record appends consent decisions and refreshes the derived current state.
//
// It runs inside the caller's transaction so that the decisions and the data
// they authorise commit together.
func (s *Store) Record(ctx context.Context, tx pgx.Tx, in contracts.RecordConsentInput) error {
	if len(in.Grants) == 0 {
		return nil
	}

	codes := make([]string, 0, len(in.Grants))
	for _, g := range in.Grants {
		codes = append(codes, g.PurposeCode)
	}

	purposeIDs := make(map[string]uuid.UUID, len(codes))
	rows, err := tx.Query(ctx,
		`SELECT code, id FROM consent.purposes WHERE tenant_id = $1 AND code = ANY($2)`,
		in.TenantID, codes)
	if err != nil {
		return fmt.Errorf("resolving purposes: %w", err)
	}
	for rows.Next() {
		var (
			code string
			id   uuid.UUID
		)
		if err := rows.Scan(&code, &id); err != nil {
			rows.Close()
			return fmt.Errorf("scanning purpose: %w", err)
		}
		purposeIDs[code] = id
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterating purposes: %w", err)
	}

	evidence := map[string]any{
		"rendered_hash": fmt.Sprintf("%x", in.Evidence.RenderedHash),
		"method":        in.Evidence.Method,
		"ip_prefix":     in.Evidence.IPPrefix,
		"user_agent":    in.Evidence.UserAgent,
		"locale":        in.Evidence.Locale,
		"ts_client":     in.Evidence.ClientTime,
	}

	for _, g := range in.Grants {
		purposeID, ok := purposeIDs[g.PurposeCode]
		if !ok {
			return fmt.Errorf("%w: %s", ErrPurposeNotFound, g.PurposeCode)
		}
		action := contracts.ConsentWithdrawn
		if g.Granted {
			action = contracts.ConsentGranted
		}

		recordID := uuid.New()
		if _, err := tx.Exec(ctx, `
			INSERT INTO consent.records
				(id, tenant_id, data_subject_id, purpose_id, submission_id, form_version_id,
				 action, document_id, evidence)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
			recordID, in.TenantID, in.SubjectID, purposeID, in.SubmissionID, in.FormVersionID,
			action, in.DocumentID, evidence,
		); err != nil {
			return fmt.Errorf("appending consent record: %w", err)
		}

		if _, err := tx.Exec(ctx, `
			INSERT INTO consent.current_consents
				(tenant_id, data_subject_id, purpose_id, granted, last_record_id, updated_at)
			VALUES ($1, $2, $3, $4, $5, now())
			ON CONFLICT (tenant_id, data_subject_id, purpose_id) DO UPDATE
				SET granted = EXCLUDED.granted,
				    last_record_id = EXCLUDED.last_record_id,
				    updated_at = EXCLUDED.updated_at`,
			in.TenantID, in.SubjectID, purposeID, g.Granted, recordID,
		); err != nil {
			return fmt.Errorf("updating current consent: %w", err)
		}
	}
	return nil
}

// HasActive reports whether a purpose is currently agreed to.
func (s *Store) HasActive(ctx context.Context, tenantID, subjectID uuid.UUID, purposeCode string) (bool, error) {
	const q = `
		SELECT c.granted
		FROM consent.current_consents c
		JOIN consent.purposes p ON p.id = c.purpose_id
		WHERE c.tenant_id = $1 AND c.data_subject_id = $2 AND p.code = $3`

	var granted bool
	err := s.db.InTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, q, tenantID, subjectID, purposeCode).Scan(&granted)
	})
	if postgres.IsNoRows(err) {
		// No record means no consent. Absence is never agreement.
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("checking consent: %w", err)
	}
	return granted, nil
}

// CreatePurpose declares a processing purpose.
func (s *Store) CreatePurpose(ctx context.Context, tenantID uuid.UUID, code, name, description, legalBasis string, required bool, retentionDays *int) (uuid.UUID, error) {
	id := uuid.New()
	err := s.db.InTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO consent.purposes
				(id, tenant_id, code, name, description, legal_basis, is_required, retention_days)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
			id, tenantID, code, name, description, legalBasis, required, retentionDays)
		return err
	})
	if err != nil {
		return uuid.Nil, fmt.Errorf("creating purpose: %w", err)
	}
	return id, nil
}

// OpenSensitive decrypts the sealed answers of one submission.
//
// The context binds the ciphertext to its submission, so a blob lifted from one
// record cannot be opened against another even by someone with write access to
// the database.
func (s *Store) OpenSensitive(ctx context.Context, tenantID, subjectID, submissionID uuid.UUID, blob []byte) (map[string]any, error) {
	if len(blob) == 0 {
		return nil, nil
	}

	var wrapped []byte
	err := s.db.InTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT dek_wrapped FROM consent.data_subjects WHERE id = $1`, subjectID).Scan(&wrapped)
	})
	if postgres.IsNoRows(err) {
		return nil, ErrSubjectErased
	}
	if err != nil {
		return nil, fmt.Errorf("reading data key: %w", err)
	}

	dek, err := s.env.Unwrap(wrapped)
	if errors.Is(err, crypto.ErrShredded) {
		// The key was destroyed by an erasure. The ciphertext may still exist and
		// is permanently unreadable, which is the intended outcome.
		return nil, ErrSubjectErased
	}
	if err != nil {
		return nil, fmt.Errorf("unwrapping data key: %w", err)
	}

	aad := fmt.Appendf(nil, "%s|%s", tenantID, submissionID)
	plaintext, err := crypto.OpenWith(dek, blob, aad)
	if err != nil {
		return nil, fmt.Errorf("decrypting sensitive answers: %w", err)
	}

	var answers map[string]any
	if err := json.Unmarshal(plaintext, &answers); err != nil {
		return nil, fmt.Errorf("decoding sensitive answers: %w", err)
	}
	return answers, nil
}

// ActiveDocumentBody returns the current document of a kind with its text.
func (s *Store) ActiveDocumentBody(ctx context.Context, tenantID uuid.UUID, kind string) (contracts.DocumentBody, error) {
	const q = `
		SELECT id, version_no, body_html, content_hash
		FROM consent.documents
		WHERE tenant_id = $1 AND kind = $2
		ORDER BY version_no DESC
		LIMIT 1`

	var d contracts.DocumentBody
	err := s.db.InTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, q, tenantID, kind).
			Scan(&d.ID, &d.VersionNo, &d.BodyHTML, &d.ContentHash)
	})
	if postgres.IsNoRows(err) {
		return contracts.DocumentBody{}, ErrDocumentNotFound
	}
	if err != nil {
		return contracts.DocumentBody{}, fmt.Errorf("reading active document body: %w", err)
	}
	return d, nil
}

// WithdrawalCount counts consent withdrawals in a window, by purpose.
//
// Counted from consent.records rather than from current_consents: the current
// table holds one row per subject and purpose and is overwritten, so a subject
// who withdrew and later granted again leaves nothing behind in it. The record
// table is append-only, which is what makes "how many people withdrew this
// month" answerable at all -- and that question is the one that says whether a
// purpose is being pushed too hard.
func (s *Store) WithdrawalCount(ctx context.Context, tenantID uuid.UUID, since time.Time) (int, map[string]int, error) {
	const q = `
		SELECT p.code, count(*)
		FROM consent.records r
		JOIN consent.purposes p ON p.id = r.purpose_id
		WHERE r.tenant_id = $1 AND r.action = $2 AND r.occurred_at >= $3
		GROUP BY p.code
		ORDER BY 2 DESC`

	byPurpose := map[string]int{}
	total := 0
	err := s.db.InTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, q, tenantID, contracts.ConsentWithdrawn, since)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var code string
			var n int
			if err := rows.Scan(&code, &n); err != nil {
				return err
			}
			byPurpose[code] = n
			total += n
		}
		return rows.Err()
	})
	if err != nil {
		return 0, nil, fmt.Errorf("counting withdrawals: %w", err)
	}
	return total, byPurpose, nil
}
