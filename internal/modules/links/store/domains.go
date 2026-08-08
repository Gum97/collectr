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

// DomainsHostConstraint is the unique index on links.domains (host).
const DomainsHostConstraint = "domains_host_key"

// ListDomains returns every domain the tenant can issue short codes on.
func (s *Store) ListDomains(ctx context.Context, tenantID uuid.UUID) ([]domain.Domain, error) {
	const q = `
		SELECT d.id, d.host, d.is_default, d.created_at,
		       count(l.id) FILTER (WHERE l.status <> 'deleted')
		FROM links.domains d
		LEFT JOIN links.links l ON l.domain_id = d.id
		WHERE d.tenant_id = $1
		GROUP BY d.id
		ORDER BY d.is_default DESC, d.created_at`

	var out []domain.Domain
	err := s.db.InTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, q, tenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var d domain.Domain
			if err := rows.Scan(&d.ID, &d.Host, &d.IsDefault, &d.CreatedAt, &d.LinkCount); err != nil {
				return err
			}
			out = append(out, d)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("listing domains: %w", err)
	}
	return out, nil
}

// InsertDomain adds a host.
//
// It returns domain.ErrHostTaken on a unique violation. The constraint is global
// rather than per-tenant on purpose: a hostname resolves to exactly one tenant,
// because the redirect knows only the Host header and has no other way to decide
// whose code a path belongs to.
func (s *Store) InsertDomain(ctx context.Context, tenantID uuid.UUID, host string, makeDefault bool) (domain.Domain, error) {
	d := domain.Domain{
		ID: uuid.New(), Host: host, IsDefault: makeDefault, CreatedAt: time.Now().UTC(),
	}

	err := s.db.InTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		// The first domain a tenant adds is always the default, whatever was
		// asked for: a tenant with domains but no default cannot create a link.
		if !makeDefault {
			var n int
			if err := tx.QueryRow(ctx,
				`SELECT count(*) FROM links.domains WHERE tenant_id = $1`, tenantID).Scan(&n); err != nil {
				return err
			}
			d.IsDefault = n == 0
		}
		if d.IsDefault {
			if _, err := tx.Exec(ctx,
				`UPDATE links.domains SET is_default = false WHERE tenant_id = $1`, tenantID); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx,
			`INSERT INTO links.domains (id, tenant_id, host, is_default, created_at)
			 VALUES ($1, $2, $3, $4, $5)`,
			d.ID, tenantID, d.Host, d.IsDefault, d.CreatedAt)
		return err
	})
	if postgres.IsUniqueViolation(err, DomainsHostConstraint) {
		return domain.Domain{}, domain.ErrHostTaken
	}
	if err != nil {
		return domain.Domain{}, fmt.Errorf("inserting domain: %w", err)
	}
	return d, nil
}

// SetDefaultDomain moves the default marker. New links get this host; existing
// links keep the host they were created on, because their codes are already in
// the world on printed material.
func (s *Store) SetDefaultDomain(ctx context.Context, tenantID, id uuid.UUID) error {
	err := s.db.InTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`UPDATE links.domains SET is_default = (id = $2) WHERE tenant_id = $1`, tenantID, id)
		if err != nil {
			return err
		}
		// Every row of the tenant is rewritten, so a zero count means the tenant
		// has no domains at all; check the target separately.
		var exists bool
		if err := tx.QueryRow(ctx,
			`SELECT exists(SELECT 1 FROM links.domains WHERE tenant_id = $1 AND id = $2)`,
			tenantID, id).Scan(&exists); err != nil {
			return err
		}
		if !exists || tag.RowsAffected() == 0 {
			return domain.ErrNotFound
		}
		return nil
	})
	if err != nil {
		if err == domain.ErrNotFound {
			return err
		}
		return fmt.Errorf("setting default domain: %w", err)
	}
	return nil
}

// DeleteDomain removes a host that has no links on it.
//
// A domain carrying links is refused rather than cascaded: deleting it would
// break every short code already printed, scanned or shared under that host, and
// nothing about the request says the operator meant that.
func (s *Store) DeleteDomain(ctx context.Context, tenantID, id uuid.UUID) error {
	err := s.db.InTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		var linkCount int
		var isDefault bool
		err := tx.QueryRow(ctx,
			`SELECT d.is_default, count(l.id) FILTER (WHERE l.status <> 'deleted')
			 FROM links.domains d
			 LEFT JOIN links.links l ON l.domain_id = d.id
			 WHERE d.tenant_id = $1 AND d.id = $2
			 GROUP BY d.id`, tenantID, id).Scan(&isDefault, &linkCount)
		if postgres.IsNoRows(err) {
			return domain.ErrNotFound
		}
		if err != nil {
			return err
		}
		if linkCount > 0 {
			return domain.ErrDomainInUse
		}
		if _, err := tx.Exec(ctx,
			`DELETE FROM links.domains WHERE tenant_id = $1 AND id = $2`, tenantID, id); err != nil {
			return err
		}
		if !isDefault {
			return nil
		}
		// Promote another host, so removing the default never leaves the tenant
		// unable to create links.
		_, err = tx.Exec(ctx,
			`UPDATE links.domains SET is_default = true
			 WHERE id = (SELECT id FROM links.domains WHERE tenant_id = $1
			             ORDER BY created_at LIMIT 1)`, tenantID)
		return err
	})
	switch err {
	case nil:
		return nil
	case domain.ErrNotFound, domain.ErrDomainInUse:
		return err
	default:
		return fmt.Errorf("deleting domain: %w", err)
	}
}
