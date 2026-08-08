// Package store persists forms, their immutable versions, and submissions.
package store

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/collectr/collectr/internal/modules/forms/domain"
	"github.com/collectr/collectr/internal/platform/postgres"
)

// Store reads and writes the forms schema.
type Store struct {
	db *postgres.DB
}

// New returns a Store backed by db.
func New(db *postgres.DB) *Store { return &Store{db: db} }

// Form is a form's metadata, without any schema.
type Form struct {
	ID              uuid.UUID
	TenantID        uuid.UUID
	ProjectID       uuid.UUID
	PublicID        string
	Title           string
	LiveVersionID   *uuid.UUID
	Status          string
	RetentionDays   *int
	RetentionAction string
	CreatedBy       uuid.UUID
	CreatedAt       time.Time
}

// Version is one published, immutable schema version.
type Version struct {
	ID          uuid.UUID
	FormID      uuid.UUID
	VersionNo   int
	Schema      domain.Schema
	SchemaHash  []byte
	PublishedAt time.Time
	PublishedBy uuid.UUID
	RetiredAt   *time.Time
}

// PublicForm is what the renderer needs to draw a form for a respondent.
type PublicForm struct {
	FormID    uuid.UUID
	TenantID  uuid.UUID
	Title     string
	VersionID uuid.UUID
	VersionNo int
	Schema    domain.Schema
}

// Submission is one stored response.
type Submission struct {
	ID            uuid.UUID
	FormID        uuid.UUID
	FormVersionID uuid.UUID
	VersionNo     int
	Answers       map[string]any
	VisibleFields []string
	Status        string
	SubmittedAt   time.Time
}

// CreateForm inserts a form with its initial draft.
func (s *Store) CreateForm(ctx context.Context, f Form, draft domain.Schema) error {
	const q = `
		INSERT INTO forms.forms
			(id, tenant_id, project_id, public_id, title, draft_schema, status,
			 retention_days, retention_action, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, 'draft', $7, $8, $9)`

	err := s.db.InTenantTx(ctx, f.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, q, f.ID, f.TenantID, f.ProjectID, f.PublicID, f.Title,
			draft, f.RetentionDays, f.RetentionAction, f.CreatedBy)
		return err
	})
	if err != nil {
		return fmt.Errorf("creating form: %w", err)
	}
	return nil
}

// GetForm returns a form's metadata.
func (s *Store) GetForm(ctx context.Context, tenantID, formID uuid.UUID) (Form, error) {
	// The tenant is in the predicate, not only in the transaction setting.
	//
	// InTenantTx sets app.tenant_id, which row-level security enforces -- but
	// only for the API role. The worker connects as the database owner, which is
	// exempt, and the worker is what produces exports. Relying on RLS alone meant
	// a form id from another organisation returned that organisation's form.
	const q = `
		SELECT id, tenant_id, project_id, public_id, title, live_version_id, status,
		       retention_days, retention_action, created_by, created_at
		FROM forms.forms WHERE id = $1 AND tenant_id = $2`

	var f Form
	err := s.db.InTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, q, formID, tenantID).Scan(
			&f.ID, &f.TenantID, &f.ProjectID, &f.PublicID, &f.Title, &f.LiveVersionID,
			&f.Status, &f.RetentionDays, &f.RetentionAction, &f.CreatedBy, &f.CreatedAt)
	})
	if postgres.IsNoRows(err) {
		return Form{}, domain.ErrFormNotFound
	}
	if err != nil {
		return Form{}, fmt.Errorf("getting form: %w", err)
	}
	return f, nil
}

// GetDraft returns the working copy of a form's schema.
func (s *Store) GetDraft(ctx context.Context, tenantID, formID uuid.UUID) (domain.Schema, error) {
	const q = `SELECT draft_schema FROM forms.forms WHERE id = $1`

	var raw []byte
	err := s.db.InTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, q, formID).Scan(&raw)
	})
	if postgres.IsNoRows(err) {
		return domain.Schema{}, domain.ErrFormNotFound
	}
	if err != nil {
		return domain.Schema{}, fmt.Errorf("getting draft: %w", err)
	}
	if len(raw) == 0 {
		return domain.Schema{}, domain.ErrNoDraft
	}
	var schema domain.Schema
	if err := json.Unmarshal(raw, &schema); err != nil {
		return domain.Schema{}, fmt.Errorf("decoding draft schema: %w", err)
	}
	return schema, nil
}

// SaveDraft overwrites the working copy. Drafts are mutable by design; only
// published versions are frozen.
func (s *Store) SaveDraft(ctx context.Context, tenantID, formID uuid.UUID, schema domain.Schema) error {
	const q = `UPDATE forms.forms SET draft_schema = $2, updated_at = now() WHERE id = $1`

	err := s.db.InTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, q, formID, schema)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return domain.ErrFormNotFound
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("saving draft: %w", err)
	}
	return nil
}

// Publish freezes the draft as a new version and points the form at it.
//
// Version numbering is derived inside the transaction from the current maximum,
// so two concurrent publishes cannot mint the same number: the unique constraint
// on (form_id, version_no) is what ultimately decides.
func (s *Store) Publish(ctx context.Context, tenantID, formID, publishedBy uuid.UUID, schema domain.Schema) (Version, error) {
	payload, err := json.Marshal(schema)
	if err != nil {
		return Version{}, fmt.Errorf("encoding schema: %w", err)
	}
	hash := sha256.Sum256(payload)

	var v Version
	err = s.db.InTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		var next int
		if err := tx.QueryRow(ctx,
			`SELECT coalesce(max(version_no), 0) + 1 FROM forms.form_versions WHERE form_id = $1`,
			formID,
		).Scan(&next); err != nil {
			return err
		}

		v = Version{
			ID: uuid.New(), FormID: formID, VersionNo: next,
			Schema: schema, SchemaHash: hash[:], PublishedBy: publishedBy,
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO forms.form_versions
				(id, tenant_id, form_id, version_no, schema, schema_hash, published_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			RETURNING published_at`,
			v.ID, tenantID, formID, next, schema, hash[:], publishedBy,
		).Scan(&v.PublishedAt); err != nil {
			return err
		}

		// The draft is cleared on publish so the builder cannot keep showing
		// edits that are already live under a different number.
		tag, err := tx.Exec(ctx, `
			UPDATE forms.forms
			SET live_version_id = $2, status = 'live', draft_schema = NULL, updated_at = now()
			WHERE id = $1`, formID, v.ID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return domain.ErrFormNotFound
		}
		return nil
	})
	if err != nil {
		return Version{}, fmt.Errorf("publishing form: %w", err)
	}
	return v, nil
}

// GetVersion returns one published version.
func (s *Store) GetVersion(ctx context.Context, tenantID, versionID uuid.UUID) (Version, error) {
	const q = `
		SELECT id, form_id, version_no, schema, schema_hash, published_at, published_by, retired_at
		FROM forms.form_versions WHERE id = $1`

	var (
		v   Version
		raw []byte
	)
	err := s.db.InTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, q, versionID).Scan(
			&v.ID, &v.FormID, &v.VersionNo, &raw, &v.SchemaHash,
			&v.PublishedAt, &v.PublishedBy, &v.RetiredAt)
	})
	if postgres.IsNoRows(err) {
		return Version{}, domain.ErrVersionNotFound
	}
	if err != nil {
		return Version{}, fmt.Errorf("getting version: %w", err)
	}
	if err := json.Unmarshal(raw, &v.Schema); err != nil {
		return Version{}, fmt.Errorf("decoding version schema: %w", err)
	}
	return v, nil
}

// ListVersions returns every published version of a form, oldest first.
//
// The submission grid needs all of them: a column exists if any version ever
// asked the question.
func (s *Store) ListVersions(ctx context.Context, tenantID, formID uuid.UUID) ([]Version, error) {
	const q = `
		SELECT id, form_id, version_no, schema, schema_hash, published_at, published_by, retired_at
		FROM forms.form_versions
		WHERE form_id = $1
		ORDER BY version_no`

	var out []Version
	err := s.db.InTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, q, formID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var (
				v   Version
				raw []byte
			)
			if err := rows.Scan(&v.ID, &v.FormID, &v.VersionNo, &raw, &v.SchemaHash,
				&v.PublishedAt, &v.PublishedBy, &v.RetiredAt); err != nil {
				return err
			}
			if err := json.Unmarshal(raw, &v.Schema); err != nil {
				return fmt.Errorf("decoding schema of version %d: %w", v.VersionNo, err)
			}
			out = append(out, v)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("listing versions: %w", err)
	}
	return out, nil
}

// ResolvePublic returns the live version of a form by its public id.
func (s *Store) ResolvePublic(ctx context.Context, publicID string) (PublicForm, error) {
	const q = `
		SELECT form_id, tenant_id, title, status, version_id, version_no, schema, retired_at
		FROM forms.resolve_public($1)`

	var (
		pf        PublicForm
		status    string
		raw       []byte
		retiredAt *time.Time
	)
	err := s.db.QueryRow(ctx, q, publicID).Scan(
		&pf.FormID, &pf.TenantID, &pf.Title, &status,
		&pf.VersionID, &pf.VersionNo, &raw, &retiredAt)
	if postgres.IsNoRows(err) {
		return PublicForm{}, domain.ErrFormNotFound
	}
	if err != nil {
		return PublicForm{}, fmt.Errorf("resolving public form: %w", err)
	}
	if retiredAt != nil {
		return PublicForm{}, domain.ErrVersionRetired
	}
	if err := json.Unmarshal(raw, &pf.Schema); err != nil {
		return PublicForm{}, fmt.Errorf("decoding public schema: %w", err)
	}
	return pf, nil
}

// InsertSubmission stores one response.
//
// It takes a transaction rather than opening its own: from v0.3 the submission
// and its consent records must commit together or not at all.
func (s *Store) InsertSubmission(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, sub Submission, purgeAt *time.Time) error {
	const q = `
		INSERT INTO forms.submissions
			(id, tenant_id, form_id, form_version_id, answers, visible_fields, purge_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`

	if _, err := tx.Exec(ctx, q, sub.ID, tenantID, sub.FormID, sub.FormVersionID,
		sub.Answers, sub.VisibleFields, purgeAt); err != nil {
		return fmt.Errorf("inserting submission: %w", err)
	}
	return nil
}

// ListSubmissions returns a page of responses, newest first.
func (s *Store) ListSubmissions(ctx context.Context, tenantID, formID uuid.UUID, before time.Time, limit int) ([]Submission, error) {
	const q = `
		SELECT s.id, s.form_id, s.form_version_id, v.version_no, s.answers,
		       s.visible_fields, s.status, s.submitted_at
		FROM forms.submissions s
		JOIN forms.form_versions v ON v.id = s.form_version_id
		WHERE s.form_id = $1 AND s.status <> 'erased' AND s.submitted_at < $2
		ORDER BY s.submitted_at DESC
		LIMIT $3`

	var out []Submission
	err := s.db.InTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, q, formID, before, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var sub Submission
			if err := rows.Scan(&sub.ID, &sub.FormID, &sub.FormVersionID, &sub.VersionNo,
				&sub.Answers, &sub.VisibleFields, &sub.Status, &sub.SubmittedAt); err != nil {
				return err
			}
			out = append(out, sub)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("listing submissions: %w", err)
	}
	return out, nil
}

// DB exposes the pool so callers can compose a submission with other modules'
// writes inside one transaction.
func (s *Store) DB() *postgres.DB { return s.db }
