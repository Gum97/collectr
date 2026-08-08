// Package store persists export jobs.
package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/collectr/collectr/internal/platform/postgres"
)

// Errors returned by the export store.
var (
	ErrNotFound = errors.New("export not found")
	ErrNotReady = errors.New("export is not ready yet")
	ErrExpired  = errors.New("export has expired")
)

// Store reads and writes export jobs.
type Store struct{ db *postgres.DB }

// New returns a Store.
func New(db *postgres.DB) *Store { return &Store{db: db} }

// Job is one queued or finished export.
type Job struct {
	ID               uuid.UUID
	TenantID         uuid.UUID
	ProjectID        uuid.UUID
	Kind             string
	TargetID         uuid.UUID
	Params           map[string]any
	RequestedBy      uuid.UUID
	IncludeSensitive bool
	Status           string
	RowCount         *int
	StorageKey       string
	Filename         string
	Error            string
	ExpiresAt        *time.Time
	CreatedAt        time.Time
}

// Enqueue records a requested export.
func (s *Store) Enqueue(ctx context.Context, j Job) error {
	const q = `
		INSERT INTO core.exports
			(id, tenant_id, project_id, kind, target_id, params, requested_by, include_sensitive)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`

	err := s.db.InTenantTx(ctx, j.TenantID, func(tx pgx.Tx) error {
		// A zero UUID is a valid value, not an absent one: written as-is it
		// violates the foreign key. An export that is not scoped to a project
		// stores NULL.
		_, err := tx.Exec(ctx, q, j.ID, j.TenantID, nullUUID(j.ProjectID), j.Kind,
			j.TargetID, j.Params, j.RequestedBy, j.IncludeSensitive)
		return err
	})
	if err != nil {
		return fmt.Errorf("enqueueing export: %w", err)
	}
	return nil
}

// Claim takes the oldest queued job for processing.
//
// SKIP LOCKED so several workers can drain the queue without coordinating and
// without any of them waiting on the others.
func (s *Store) Claim(ctx context.Context) (Job, bool, error) {
	const q = `
		UPDATE core.exports
		SET status = 'running', started_at = now()
		WHERE id = (
			SELECT id FROM core.exports
			WHERE status = 'queued'
			ORDER BY created_at
			LIMIT 1
			FOR UPDATE SKIP LOCKED
		)
		RETURNING id, tenant_id,
		          coalesce(project_id, '00000000-0000-0000-0000-000000000000'::uuid),
		          kind, target_id, params, requested_by, include_sensitive, created_at`

	var j Job
	err := s.db.QueryRow(ctx, q).Scan(&j.ID, &j.TenantID, &j.ProjectID, &j.Kind,
		&j.TargetID, &j.Params, &j.RequestedBy, &j.IncludeSensitive, &j.CreatedAt)
	if postgres.IsNoRows(err) {
		return Job{}, false, nil
	}
	if err != nil {
		return Job{}, false, fmt.Errorf("claiming export: %w", err)
	}
	return j, true, nil
}

// Complete marks a job finished.
func (s *Store) Complete(ctx context.Context, j Job, key, name string, rows int, expiresAt time.Time) error {
	const q = `
		UPDATE core.exports
		SET status = 'ready', storage_key = $2, filename = $3, row_count = $4,
		    expires_at = $5, ready_at = now()
		WHERE id = $1`

	if _, err := s.db.Exec(ctx, q, j.ID, key, name, rows, expiresAt); err != nil {
		return fmt.Errorf("completing export: %w", err)
	}
	return nil
}

// Fail records why a job could not be produced.
func (s *Store) Fail(ctx context.Context, j Job, reason string) error {
	if len(reason) > 500 {
		reason = reason[:500]
	}
	if _, err := s.db.Exec(ctx,
		`UPDATE core.exports SET status = 'failed', error = $2 WHERE id = $1`, j.ID, reason); err != nil {
		return fmt.Errorf("failing export: %w", err)
	}
	return nil
}

// Get returns one job.
func (s *Store) Get(ctx context.Context, tenantID, id uuid.UUID) (Job, error) {
	const q = `
		SELECT id, tenant_id, coalesce(project_id, '00000000-0000-0000-0000-000000000000'::uuid),
		       kind, target_id, params, requested_by,
		       include_sensitive, status, row_count, coalesce(storage_key, ''),
		       coalesce(filename, ''), coalesce(error, ''), expires_at, created_at
		FROM core.exports WHERE id = $1`

	var j Job
	err := s.db.InTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, q, id).Scan(&j.ID, &j.TenantID, &j.ProjectID, &j.Kind,
			&j.TargetID, &j.Params, &j.RequestedBy, &j.IncludeSensitive, &j.Status,
			&j.RowCount, &j.StorageKey, &j.Filename, &j.Error, &j.ExpiresAt, &j.CreatedAt)
	})
	if postgres.IsNoRows(err) {
		return Job{}, ErrNotFound
	}
	if err != nil {
		return Job{}, fmt.Errorf("getting export: %w", err)
	}
	return j, nil
}

// ListExpired returns ready jobs past their lifetime.
func (s *Store) ListExpired(ctx context.Context, limit int) ([]Job, error) {
	const q = `
		SELECT id, tenant_id, coalesce(storage_key, '')
		FROM core.exports
		WHERE status = 'ready' AND expires_at < now()
		LIMIT $1`

	rows, err := s.db.Query(ctx, q, limit)
	if err != nil {
		return nil, fmt.Errorf("listing expired exports: %w", err)
	}
	defer rows.Close()

	var out []Job
	for rows.Next() {
		var j Job
		if err := rows.Scan(&j.ID, &j.TenantID, &j.StorageKey); err != nil {
			return nil, fmt.Errorf("scanning expired export: %w", err)
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// MarkExpired records that an artefact has been removed.
func (s *Store) MarkExpired(ctx context.Context, tenantID, id uuid.UUID) error {
	if _, err := s.db.Exec(ctx,
		`UPDATE core.exports SET status = 'expired', storage_key = NULL WHERE id = $1`, id); err != nil {
		return fmt.Errorf("marking export expired: %w", err)
	}
	return nil
}

// DB exposes the pool so the caller can compose the audit write.
func (s *Store) DB() *postgres.DB { return s.db }

func nullUUID(id uuid.UUID) *uuid.UUID {
	if id == uuid.Nil {
		return nil
	}
	return &id
}
