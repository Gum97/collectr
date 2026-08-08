// Package store persists data subject tokens and requests.
package store

import (
	"context"
	"crypto/sha256"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/collectr/collectr/internal/modules/dsr/domain"
	"github.com/collectr/collectr/internal/platform/postgres"
)

// Store reads and writes the DSR tables.
type Store struct {
	db *postgres.DB
}

// New returns a Store.
func New(db *postgres.DB) *Store { return &Store{db: db} }

// Token is an issued magic-link token.
type Token struct {
	ID           uuid.UUID
	SubjectID    uuid.UUID
	Scope        string
	SubmissionID *uuid.UUID
	ExpiresAt    time.Time
}

// Hash reduces a raw token to what is stored.
//
// SHA-256 without a work factor is right here: the token is 256 bits from a
// CSPRNG, so there is nothing to guess, and the check runs on a path that must
// stay fast enough not to leak timing.
func Hash(raw string) []byte {
	sum := sha256.Sum256([]byte(raw))
	return sum[:]
}

// IssueToken stores a token for a subject.
func (s *Store) IssueToken(ctx context.Context, tenantID, subjectID uuid.UUID, raw, scope string, submissionID *uuid.UUID, ttl time.Duration) (Token, error) {
	t := Token{
		ID: uuid.New(), SubjectID: subjectID, Scope: scope,
		SubmissionID: submissionID, ExpiresAt: time.Now().UTC().Add(ttl),
	}
	err := s.db.InTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO consent.dsr_tokens
				(id, tenant_id, token_hash, data_subject_id, scope, submission_id, expires_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			t.ID, tenantID, Hash(raw), subjectID, scope, submissionID, t.ExpiresAt)
		return err
	})
	if err != nil {
		return Token{}, fmt.Errorf("issuing dsr token: %w", err)
	}
	return t, nil
}

// ConsumeToken validates a token and marks it used.
//
// The update is conditional on the token still being unused, so two clicks on the
// same link -- an email client prefetching, say -- cannot both succeed. Checking
// then updating would leave exactly that gap.
func (s *Store) ConsumeToken(ctx context.Context, tenantID uuid.UUID, raw string) (Token, error) {
	const q = `
		UPDATE consent.dsr_tokens
		SET used_at = now()
		WHERE token_hash = $1
		  AND used_at IS NULL
		  AND expires_at > now()
		RETURNING id, data_subject_id, scope, submission_id, expires_at`

	var t Token
	err := s.db.InTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, q, Hash(raw)).Scan(
			&t.ID, &t.SubjectID, &t.Scope, &t.SubmissionID, &t.ExpiresAt)
	})
	if postgres.IsNoRows(err) {
		return Token{}, domain.ErrTokenInvalid
	}
	if err != nil {
		return Token{}, fmt.Errorf("consuming dsr token: %w", err)
	}
	return t, nil
}

// RegisterAttempt counts an identify attempt and reports whether it is allowed.
//
// Rate limiting lives in the database rather than in Redis for this one endpoint:
// failing open when the cache is down would hand an attacker an oracle for which
// phone numbers a company holds.
func (s *Store) RegisterAttempt(ctx context.Context, tenantID uuid.UUID, identifierHash []byte, limit int, window time.Duration) (bool, error) {
	windowStart := time.Now().UTC().Truncate(window)

	const q = `
		INSERT INTO consent.dsr_attempts (tenant_id, identifier_hash, window_start, attempts)
		VALUES ($1, $2, $3, 1)
		ON CONFLICT (tenant_id, identifier_hash, window_start) DO UPDATE
			SET attempts = consent.dsr_attempts.attempts + 1
		RETURNING attempts`

	var attempts int
	err := s.db.InTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, q, tenantID, identifierHash, windowStart).Scan(&attempts)
	})
	if err != nil {
		return false, fmt.Errorf("registering identify attempt: %w", err)
	}
	return attempts <= limit, nil
}

// PurgeExpired removes used and expired tokens and old attempt counters.
func (s *Store) PurgeExpired(ctx context.Context, olderThan time.Duration) (int64, error) {
	cutoff := time.Now().UTC().Add(-olderThan)

	tag, err := s.db.Exec(ctx,
		`DELETE FROM consent.dsr_tokens WHERE expires_at < $1 OR used_at < $1`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("purging dsr tokens: %w", err)
	}
	if _, err := s.db.Exec(ctx,
		`DELETE FROM consent.dsr_attempts WHERE window_start < $1`, cutoff); err != nil {
		return 0, fmt.Errorf("purging identify attempts: %w", err)
	}
	return tag.RowsAffected(), nil
}

// CreateRequest records an exercise of a right and its statutory deadline.
func (s *Store) CreateRequest(ctx context.Context, tx pgx.Tx, tenantID, subjectID uuid.UUID, reqType, verification string, sla time.Duration) (domain.Request, error) {
	now := time.Now().UTC()
	r := domain.Request{
		ID: uuid.NewString(), Type: reqType, Status: domain.StatusVerified,
		ReceivedAt: now,
		// Stored, not computed on read: changing the configured SLA later must not
		// silently move a deadline that is already running.
		DueAt: now.Add(sla),
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO consent.dsr_requests
			(id, tenant_id, data_subject_id, type, status, verification_method, received_at, due_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		r.ID, tenantID, subjectID, reqType, r.Status, verification, r.ReceivedAt, r.DueAt,
	); err != nil {
		return domain.Request{}, fmt.Errorf("creating dsr request: %w", err)
	}
	return r, nil
}

// ListRequests returns a subject's requests, newest first.
func (s *Store) ListRequests(ctx context.Context, tenantID, subjectID uuid.UUID) ([]domain.Request, error) {
	const q = `
		SELECT id, type, status, received_at, due_at, fulfilled_at
		FROM consent.dsr_requests
		WHERE data_subject_id = $1
		ORDER BY received_at DESC
		LIMIT 50`

	var out []domain.Request
	err := s.db.InTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, q, subjectID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r domain.Request
			if err := rows.Scan(&r.ID, &r.Type, &r.Status, &r.ReceivedAt, &r.DueAt, &r.FulfilledAt); err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("listing dsr requests: %w", err)
	}
	return out, nil
}

// PendingRequest is one request awaiting action, with the tenant it belongs to.
type PendingRequest struct {
	ID        uuid.UUID
	TenantID  uuid.UUID
	SubjectID uuid.UUID
	Type      string
	DueAt     time.Time
}

// ClaimPending locks a batch of unfulfilled requests for processing.
//
// SKIP LOCKED lets several workers drain the queue without coordinating and
// without any of them waiting on the others.
func (s *Store) ClaimPending(ctx context.Context, tx pgx.Tx, limit int) ([]PendingRequest, error) {
	const q = `
		SELECT id, tenant_id, data_subject_id, type, due_at
		FROM consent.dsr_requests
		WHERE status IN ('received', 'verified')
		ORDER BY due_at
		LIMIT $1
		FOR UPDATE SKIP LOCKED`

	rows, err := tx.Query(ctx, q, limit)
	if err != nil {
		return nil, fmt.Errorf("claiming dsr requests: %w", err)
	}
	defer rows.Close()

	var out []PendingRequest
	for rows.Next() {
		var r PendingRequest
		if err := rows.Scan(&r.ID, &r.TenantID, &r.SubjectID, &r.Type, &r.DueAt); err != nil {
			return nil, fmt.Errorf("scanning dsr request: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// MarkFulfilled closes a request.
func (s *Store) MarkFulfilled(ctx context.Context, tx pgx.Tx, id uuid.UUID, note string) error {
	if _, err := tx.Exec(ctx, `
		UPDATE consent.dsr_requests
		SET status = 'fulfilled', fulfilled_at = now(), resolution_note = $2
		WHERE id = $1`, id, note); err != nil {
		return fmt.Errorf("marking dsr request fulfilled: %w", err)
	}
	return nil
}

// OverdueCount reports how many requests have passed their deadline.
//
// This is the alert that matters most in the whole system: it is the only metric
// whose breach is a breach of the law rather than of a service level.
func (s *Store) OverdueCount(ctx context.Context) (int64, error) {
	var n int64
	err := s.db.QueryRow(ctx, `
		SELECT count(*) FROM consent.dsr_requests
		WHERE status NOT IN ('fulfilled', 'rejected') AND due_at < now()`).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("counting overdue dsr requests: %w", err)
	}
	return n, nil
}

// EraseSubject destroys a subject's data and returns how many rows went.
func (s *Store) EraseSubject(ctx context.Context, tx pgx.Tx, subjectID uuid.UUID) (int, error) {
	var submissions, files int
	if err := tx.QueryRow(ctx,
		`SELECT submissions_deleted, files_deleted FROM consent.erase_subject($1)`, subjectID,
	).Scan(&submissions, &files); err != nil {
		return 0, fmt.Errorf("erasing subject: %w", err)
	}
	return submissions, nil
}

// SubjectSubmission is one record shown in the portal.
type SubjectSubmission struct {
	ID          uuid.UUID
	FormTitle   string
	VersionNo   int
	Answers     map[string]any
	Visible     []string
	SubmittedAt time.Time
	Status      string
}

// SubjectSubmissions returns everything one subject has submitted to a tenant.
func (s *Store) SubjectSubmissions(ctx context.Context, tenantID, subjectID uuid.UUID, onlyID *uuid.UUID) ([]SubjectSubmission, error) {
	const q = `
		SELECT s.id, f.title, v.version_no, s.answers, s.visible_fields, s.submitted_at, s.status
		FROM forms.submissions s
		JOIN forms.forms f ON f.id = s.form_id
		JOIN forms.form_versions v ON v.id = s.form_version_id
		WHERE s.data_subject_id = $1
		  AND s.status <> 'erased'
		  AND ($2::uuid IS NULL OR s.id = $2)
		ORDER BY s.submitted_at DESC
		LIMIT 200`

	var out []SubjectSubmission
	err := s.db.InTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, q, subjectID, onlyID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var sub SubjectSubmission
			if err := rows.Scan(&sub.ID, &sub.FormTitle, &sub.VersionNo, &sub.Answers,
				&sub.Visible, &sub.SubmittedAt, &sub.Status); err != nil {
				return err
			}
			out = append(out, sub)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("listing subject submissions: %w", err)
	}
	return out, nil
}

// RectifySubmission replaces answers, keeping the previous version on record.
//
// The old values are kept, not overwritten: a correction is a fact about the
// record, and losing what it said before would remove the ability to explain why
// something downstream was different.
func (s *Store) RectifySubmission(ctx context.Context, tenantID, subjectID, submissionID uuid.UUID, answers map[string]any) error {
	return s.db.InTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		var before map[string]any
		err := tx.QueryRow(ctx,
			`SELECT answers FROM forms.submissions
			 WHERE id = $1 AND data_subject_id = $2 AND status = 'active'
			 FOR UPDATE`, submissionID, subjectID).Scan(&before)
		if postgres.IsNoRows(err) {
			// Ownership is checked in the query itself. A separate lookup would
			// invite the classic mistake of trusting an id from the request.
			return domain.ErrForbidden
		}
		if err != nil {
			return fmt.Errorf("loading submission for rectification: %w", err)
		}

		if _, err := tx.Exec(ctx, `
			INSERT INTO forms.submission_revisions
				(id, tenant_id, submission_id, answers_before, changed_by, change_source)
			VALUES ($1, $2, $3, $4, $5, 'dsr_self_service')`,
			uuid.New(), tenantID, submissionID, before, "subject:"+subjectID.String(),
		); err != nil {
			return fmt.Errorf("recording revision: %w", err)
		}
		if _, err := tx.Exec(ctx,
			`UPDATE forms.submissions SET answers = $2 WHERE id = $1`, submissionID, answers,
		); err != nil {
			return fmt.Errorf("updating answers: %w", err)
		}
		return nil
	})
}

// PurgeExpiredSubmissions deletes submissions past their retention date.
func (s *Store) PurgeExpiredSubmissions(ctx context.Context, limit int) (int64, error) {
	const q = `
		DELETE FROM forms.submissions
		WHERE id IN (
			SELECT id FROM forms.submissions
			WHERE status = 'active' AND purge_at IS NOT NULL AND purge_at < now()
			ORDER BY purge_at
			LIMIT $1
		)`

	tag, err := s.db.Exec(ctx, q, limit)
	if err != nil {
		return 0, fmt.Errorf("purging expired submissions: %w", err)
	}
	return tag.RowsAffected(), nil
}

// AdminRequest is one request as an administrator sees it.
type AdminRequest struct {
	ID          uuid.UUID
	SubjectID   uuid.UUID
	Type        string
	Status      string
	ReceivedAt  time.Time
	DueAt       time.Time
	FulfilledAt *time.Time
	HandledBy   *uuid.UUID
	Note        string
}

// Overdue reports whether the statutory deadline has passed.
func (r AdminRequest) Overdue(now time.Time) bool {
	return r.Status != domain.StatusFulfilled && r.Status != domain.StatusRejected && now.After(r.DueAt)
}

// ListForAdmin returns an organisation's requests, soonest deadline first.
//
// Ordered by due_at rather than by arrival: the queue exists to stop anything
// going past its deadline, so the most urgent must be at the top even if it
// arrived last.
func (s *Store) ListForAdmin(ctx context.Context, tenantID uuid.UUID, openOnly, overdueOnly bool, limit int) ([]AdminRequest, error) {
	const q = `
		SELECT id, data_subject_id, type, status, received_at, due_at,
		       fulfilled_at, handled_by, coalesce(resolution_note, '')
		FROM consent.dsr_requests
		WHERE tenant_id = $1
		  AND ($2::bool IS NOT TRUE OR status NOT IN ('fulfilled', 'rejected'))
		  AND ($3::bool IS NOT TRUE OR (due_at < now() AND status NOT IN ('fulfilled', 'rejected')))
		ORDER BY due_at
		LIMIT $4`

	var out []AdminRequest
	err := s.db.InTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, q, tenantID, openOnly, overdueOnly, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r AdminRequest
			if err := rows.Scan(&r.ID, &r.SubjectID, &r.Type, &r.Status, &r.ReceivedAt,
				&r.DueAt, &r.FulfilledAt, &r.HandledBy, &r.Note); err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("listing dsr requests for admin: %w", err)
	}
	return out, nil
}

// Resolve closes a request that needed human judgement.
//
// Conditional on the request still being open, so two administrators acting at
// once cannot both record a decision on the same case.
func (s *Store) Resolve(ctx context.Context, tx pgx.Tx, tenantID, id, handledBy uuid.UUID, status, note string) (AdminRequest, error) {
	const q = `
		UPDATE consent.dsr_requests
		SET status = $3, handled_by = $4, resolution_note = $5,
		    fulfilled_at = CASE WHEN $3 = 'fulfilled' THEN now() ELSE fulfilled_at END
		WHERE id = $1 AND tenant_id = $2 AND status NOT IN ('fulfilled', 'rejected')
		RETURNING id, data_subject_id, type, status, received_at, due_at,
		          fulfilled_at, handled_by, coalesce(resolution_note, '')`

	var r AdminRequest
	err := tx.QueryRow(ctx, q, id, tenantID, status, handledBy, note).Scan(
		&r.ID, &r.SubjectID, &r.Type, &r.Status, &r.ReceivedAt, &r.DueAt,
		&r.FulfilledAt, &r.HandledBy, &r.Note)
	if postgres.IsNoRows(err) {
		return AdminRequest{}, domain.ErrNotFound
	}
	if err != nil {
		return AdminRequest{}, fmt.Errorf("resolving dsr request: %w", err)
	}
	return r, nil
}

// Restrict marks a subject's submissions as not to be processed.
//
// Restriction is not deletion: the data stays, and stops being used. Every read
// path already filters on status, so flipping it here is what makes the
// instruction take effect.
func (s *Store) Restrict(ctx context.Context, tx pgx.Tx, subjectID uuid.UUID) (int64, error) {
	tag, err := tx.Exec(ctx, `
		UPDATE forms.submissions SET status = 'restricted'
		WHERE data_subject_id = $1 AND status = 'active'`, subjectID)
	if err != nil {
		return 0, fmt.Errorf("restricting submissions: %w", err)
	}
	return tag.RowsAffected(), nil
}
