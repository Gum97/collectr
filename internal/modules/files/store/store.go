// Package store persists attachment metadata. The bytes never live here.
package store

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/collectr/collectr/internal/modules/files/domain"
	"github.com/collectr/collectr/internal/platform/postgres"
)

// Store reads and writes the files schema.
type Store struct {
	db *postgres.DB
}

// New returns a Store.
func New(db *postgres.DB) *Store { return &Store{db: db} }

// File is one attachment's metadata.
type File struct {
	ID            uuid.UUID
	TenantID      uuid.UUID
	ProjectID     uuid.UUID
	FormVersionID uuid.UUID
	FieldID       string
	StorageKey    string
	OriginalName  string
	ContentType   string
	SizeBytes     int64
	Checksum      []byte
	DEKWrapped    []byte
	Status        string
	SubmissionID  *uuid.UUID
	CreatedAt     time.Time
}

// Insert records an uploaded file.
func (s *Store) Insert(ctx context.Context, f File) error {
	const q = `
		INSERT INTO files.files
			(id, tenant_id, project_id, form_version_id, field_id, storage_key,
			 original_name, content_type, size_bytes, checksum, encrypted, dek_wrapped, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, true, $11, $12)`

	err := s.db.InTenantTx(ctx, f.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, q, f.ID, f.TenantID, f.ProjectID, f.FormVersionID, f.FieldID,
			f.StorageKey, f.OriginalName, f.ContentType, f.SizeBytes, f.Checksum,
			f.DEKWrapped, f.Status)
		return err
	})
	if err != nil {
		return fmt.Errorf("inserting file: %w", err)
	}
	return nil
}

// ResolvePublic loads a file by id without a tenant scope.
//
// Upload and download both know a file id and nothing else: the respondent
// filling in a public form has not identified themselves to any organisation.
func (s *Store) ResolvePublic(ctx context.Context, id uuid.UUID) (File, error) {
	const q = `
		SELECT id, tenant_id, storage_key, original_name, content_type,
		       size_bytes, dek_wrapped, status, submission_id
		FROM files.resolve_public($1)`

	var f File
	err := s.db.QueryRow(ctx, q, id).Scan(
		&f.ID, &f.TenantID, &f.StorageKey, &f.OriginalName, &f.ContentType,
		&f.SizeBytes, &f.DEKWrapped, &f.Status, &f.SubmissionID)
	if postgres.IsNoRows(err) {
		return File{}, domain.ErrNotFound
	}
	if err != nil {
		return File{}, fmt.Errorf("resolving file: %w", err)
	}

	// resolve_public deliberately returns a narrow projection; the validation
	// fields come from a second, tenant-scoped read.
	err = s.db.InTenantTx(ctx, f.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT form_version_id, coalesce(field_id, ''), project_id FROM files.files WHERE id = $1`,
			id).Scan(&f.FormVersionID, &f.FieldID, &f.ProjectID)
	})
	if err != nil {
		return File{}, fmt.Errorf("loading file context: %w", err)
	}
	return f, nil
}

// GetInTenant loads one file's metadata within a tenant.
//
// Separate from ResolvePublic, which answers "which file is this id" for a
// respondent who has not identified themselves to anybody. This is the operator
// path, and an operator always belongs to exactly one tenant, so scoping is
// available here and must be used.
//
// tenant_id appears in the WHERE clause as well as in InTenantTx. Row-level
// security already restricts the rows, but the export path once shipped a
// cross-tenant read for precisely this reason: it passed the tenant to the
// transaction and not to the query, and the worker's role bypasses RLS. A query
// that is correct on its own does not depend on which role runs it.
func (s *Store) GetInTenant(ctx context.Context, tenantID, id uuid.UUID) (File, error) {
	const q = `
		SELECT id, tenant_id, project_id, form_version_id, coalesce(field_id, ''),
		       storage_key, original_name, content_type, size_bytes, dek_wrapped,
		       status, submission_id, created_at
		FROM files.files
		WHERE id = $1 AND tenant_id = $2 AND status <> 'erased'`

	var f File
	err := s.db.InTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, q, id, tenantID).Scan(
			&f.ID, &f.TenantID, &f.ProjectID, &f.FormVersionID, &f.FieldID,
			&f.StorageKey, &f.OriginalName, &f.ContentType, &f.SizeBytes,
			&f.DEKWrapped, &f.Status, &f.SubmissionID, &f.CreatedAt)
	})
	if postgres.IsNoRows(err) {
		return File{}, domain.ErrNotFound
	}
	if err != nil {
		return File{}, fmt.Errorf("loading file: %w", err)
	}
	return f, nil
}

// Bind attaches files to a submission inside the caller's transaction.
//
// The update is conditional on the file still being unattached, so the same
// upload cannot be claimed by two submissions racing each other.
func (s *Store) Bind(ctx context.Context, tx pgx.Tx, tenantID, submissionID uuid.UUID, fileIDs []uuid.UUID) error {
	if len(fileIDs) == 0 {
		return nil
	}
	const q = `
		UPDATE files.files
		SET submission_id = $2, status = 'bound'
		WHERE id = ANY($3) AND tenant_id = $1
		  AND submission_id IS NULL AND status = 'pending'`

	tag, err := tx.Exec(ctx, q, tenantID, submissionID, fileIDs)
	if err != nil {
		return fmt.Errorf("binding files: %w", err)
	}
	if int(tag.RowsAffected()) != len(fileIDs) {
		return fmt.Errorf("%w: %d of %d attachments could not be attached",
			domain.ErrAlreadyBound, len(fileIDs)-int(tag.RowsAffected()), len(fileIDs))
	}
	return nil
}

// Orphan identifies a file to be swept.
type Orphan struct {
	ID         uuid.UUID
	TenantID   uuid.UUID
	StorageKey string
}

// ListOrphans returns uploads that were never attached to a submission.
func (s *Store) ListOrphans(ctx context.Context, olderThan time.Duration, limit int) ([]Orphan, error) {
	const q = `
		SELECT id, tenant_id, storage_key
		FROM files.files
		WHERE status = 'pending' AND submission_id IS NULL AND created_at < $1
		ORDER BY created_at
		LIMIT $2`

	rows, err := s.db.Query(ctx, q, time.Now().UTC().Add(-olderThan), limit)
	if err != nil {
		return nil, fmt.Errorf("listing orphaned files: %w", err)
	}
	defer rows.Close()

	var out []Orphan
	for rows.Next() {
		var o Orphan
		if err := rows.Scan(&o.ID, &o.TenantID, &o.StorageKey); err != nil {
			return nil, fmt.Errorf("scanning orphan: %w", err)
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// ListErasedWithObjects returns files whose keys are gone but whose bytes remain.
func (s *Store) ListErasedWithObjects(ctx context.Context, limit int) ([]Orphan, error) {
	const q = `
		SELECT id, tenant_id, storage_key
		FROM files.files
		WHERE dek_wrapped IS NULL AND storage_key <> ''
		LIMIT $1`

	rows, err := s.db.Query(ctx, q, limit)
	if err != nil {
		return nil, fmt.Errorf("listing erased files: %w", err)
	}
	defer rows.Close()

	var out []Orphan
	for rows.Next() {
		var o Orphan
		if err := rows.Scan(&o.ID, &o.TenantID, &o.StorageKey); err != nil {
			return nil, fmt.Errorf("scanning erased file: %w", err)
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// Delete removes a file row.
func (s *Store) Delete(ctx context.Context, tenantID, id uuid.UUID) error {
	err := s.db.InTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM files.files WHERE id = $1`, id)
		return err
	})
	if err != nil {
		return fmt.Errorf("deleting file: %w", err)
	}
	return nil
}

// ClearStorageKey marks an erased file's bytes as gone.
func (s *Store) ClearStorageKey(ctx context.Context, tenantID, id uuid.UUID) error {
	err := s.db.InTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`UPDATE files.files SET storage_key = '', status = 'erased' WHERE id = $1`, id)
		return err
	})
	if err != nil {
		return fmt.Errorf("clearing storage key: %w", err)
	}
	return nil
}

// ShredForSubject destroys the keys of every attachment belonging to a data
// subject, in the caller's transaction.
//
// Called from the erasure path. Deleting the key is what makes the bytes
// unreadable everywhere, backups included; the sweeper reclaims the space later.
func (s *Store) ShredForSubject(ctx context.Context, tx pgx.Tx, subjectID uuid.UUID) (int64, error) {
	const q = `
		UPDATE files.files f
		SET dek_wrapped = NULL, status = 'erased'
		FROM forms.submissions s
		WHERE f.submission_id = s.id AND s.data_subject_id = $1`

	tag, err := tx.Exec(ctx, q, subjectID)
	if err != nil {
		return 0, fmt.Errorf("shredding subject attachments: %w", err)
	}
	return tag.RowsAffected(), nil
}
