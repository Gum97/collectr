// Package store persists links and resolves them for the redirect path.
package store

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/collectr/collectr/internal/modules/links/domain"
	"github.com/collectr/collectr/internal/platform/postgres"
)

// LinksLookupConstraint is the unique index guarding (domain, code). It is the
// final arbiter of code uniqueness; nothing checks first and inserts after.
const LinksLookupConstraint = "links_lookup"

// Store reads and writes the links schema.
type Store struct {
	db *postgres.DB
}

// New returns a Store backed by db.
func New(db *postgres.DB) *Store { return &Store{db: db} }

// Resolve looks up a link by public host and code.
//
// It calls links.resolve(), a SECURITY DEFINER function, because the redirect is
// the one path that cannot set app.tenant_id ahead of the query: it learns the
// tenant *from* the lookup. Granting the application BYPASSRLS instead would
// switch isolation off everywhere to serve this single case.
func (s *Store) Resolve(ctx context.Context, host, code string) (domain.Resolution, error) {
	const q = `
		SELECT link_id, tenant_id, project_id, target_url, form_id, form_public_id,
		       status, expires_at
		FROM links.resolve($1, $2)`

	var (
		res      domain.Resolution
		target   *string
		publicID *string
	)
	err := s.db.QueryRow(ctx, q, host, code).Scan(
		&res.LinkID, &res.TenantID, &res.ProjectID,
		&target, &res.FormID, &publicID, &res.Status, &res.ExpiresAt,
	)
	if postgres.IsNoRows(err) {
		return domain.Resolution{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Resolution{}, fmt.Errorf("resolving link: %w", err)
	}
	if publicID != nil {
		res.FormPublicID = *publicID
	}
	if target != nil {
		res.TargetURL = *target
	}
	return res, nil
}

// Insert stores a new link.
//
// It returns domain.ErrAliasTaken on a unique violation so the caller can either
// retry with a fresh random code or report the conflict for a custom alias.
func (s *Store) Insert(ctx context.Context, l domain.Link) error {
	const q = `
		INSERT INTO links.links
			(id, tenant_id, project_id, domain_id, code, target_url, form_id,
			 expires_at, status, created_by, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`

	err := s.db.InTenantTx(ctx, l.TenantID, func(tx pgx.Tx) error {
		var target *string
		if l.TargetURL != "" {
			target = &l.TargetURL
		}
		_, err := tx.Exec(ctx, q,
			l.ID, l.TenantID, l.ProjectID, l.DomainID, l.Code, target, l.FormID,
			l.ExpiresAt, l.Status, l.CreatedBy, l.CreatedAt,
		)
		return err
	})
	if postgres.IsUniqueViolation(err, LinksLookupConstraint) {
		return domain.ErrAliasTaken
	}
	if err != nil {
		return fmt.Errorf("inserting link: %w", err)
	}
	return nil
}

// Get returns one link owned by tenantID.
func (s *Store) Get(ctx context.Context, tenantID, id uuid.UUID) (domain.Link, error) {
	const q = `
		SELECT l.id, l.tenant_id, l.project_id, l.domain_id, d.host,
		       l.code, coalesce(l.target_url, ''),
		       l.form_id, l.expires_at, l.status, l.created_by, l.created_at
		FROM links.links l
		JOIN links.domains d ON d.id = l.domain_id
		WHERE l.id = $1`

	var l domain.Link
	err := s.db.InTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, q, id).Scan(
			&l.ID, &l.TenantID, &l.ProjectID, &l.DomainID, &l.Host, &l.Code,
			&l.TargetURL, &l.FormID, &l.ExpiresAt, &l.Status, &l.CreatedBy, &l.CreatedAt,
		)
	})
	if postgres.IsNoRows(err) {
		return domain.Link{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Link{}, fmt.Errorf("getting link: %w", err)
	}
	return l, nil
}

// ListByProject returns links ordered newest first, using keyset pagination.
//
// Keyset rather than OFFSET: offsets both drift when rows are inserted mid-scan
// and degrade on deep pages, and this list is the main admin screen.
func (s *Store) ListByProject(ctx context.Context, tenantID, projectID uuid.UUID, before time.Time, limit int) ([]domain.Link, error) {
	const q = `
		SELECT l.id, l.tenant_id, l.project_id, l.domain_id, d.host,
		       l.code, coalesce(l.target_url, ''),
		       l.form_id, l.expires_at, l.status, l.created_by, l.created_at
		FROM links.links l
		JOIN links.domains d ON d.id = l.domain_id
		WHERE l.project_id = $1 AND l.status <> 'deleted' AND l.created_at < $2
		ORDER BY l.created_at DESC
		LIMIT $3`

	var out []domain.Link
	err := s.db.InTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, q, projectID, before, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var l domain.Link
			if err := rows.Scan(
				&l.ID, &l.TenantID, &l.ProjectID, &l.DomainID, &l.Host, &l.Code,
				&l.TargetURL, &l.FormID, &l.ExpiresAt, &l.Status, &l.CreatedBy, &l.CreatedAt,
			); err != nil {
				return err
			}
			out = append(out, l)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("listing links: %w", err)
	}
	return out, nil
}

// UpdateStatus changes a link's status and returns the code that was affected,
// so the caller can evict the matching cache entry.
func (s *Store) UpdateStatus(ctx context.Context, tenantID, id uuid.UUID, status string) (host, code string, err error) {
	const q = `
		UPDATE links.links l
		SET status = $2
		FROM links.domains d
		WHERE l.id = $1 AND d.id = l.domain_id
		RETURNING d.host, l.code`

	err = s.db.InTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, q, id, status).Scan(&host, &code)
	})
	if postgres.IsNoRows(err) {
		return "", "", domain.ErrNotFound
	}
	if err != nil {
		return "", "", fmt.Errorf("updating link status: %w", err)
	}
	return host, code, nil
}

// DefaultDomain returns the tenant's default domain.
func (s *Store) DefaultDomain(ctx context.Context, tenantID uuid.UUID) (uuid.UUID, string, error) {
	const q = `
		SELECT id, host FROM links.domains
		WHERE tenant_id = $1
		ORDER BY is_default DESC, created_at
		LIMIT 1`

	var (
		id   uuid.UUID
		host string
	)
	err := s.db.InTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, q, tenantID).Scan(&id, &host)
	})
	if postgres.IsNoRows(err) {
		return uuid.Nil, "", domain.ErrNoDomain
	}
	if err != nil {
		return uuid.Nil, "", fmt.Errorf("getting default domain: %w", err)
	}
	return id, host, nil
}

// ExpireStale marks links whose expiry has passed, returning how many changed.
//
// Expiry is also checked lazily on each click; this sweeper exists so that the
// state in the database eventually matches reality even for links nobody visits.
func (s *Store) ExpireStale(ctx context.Context, now time.Time) (int64, error) {
	const q = `
		UPDATE links.links
		SET status = 'disabled'
		WHERE status = 'active' AND expires_at IS NOT NULL AND expires_at < $1`

	tag, err := s.db.Exec(ctx, q, now)
	if err != nil {
		return 0, fmt.Errorf("expiring links: %w", err)
	}
	return tag.RowsAffected(), nil
}
