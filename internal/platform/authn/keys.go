package authn

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// APIKey is one issued credential as it appears in a listing.
//
// The secret is absent by construction: only its prefix and hash are stored, so
// there is nothing here to leak by listing.
type APIKey struct {
	ID         uuid.UUID  `json:"id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	ProjectID  *uuid.UUID `json:"project_id"`
	Scopes     []string   `json:"scopes"`
	ExpiresAt  *time.Time `json:"expires_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	RevokedAt  *time.Time `json:"revoked_at"`
	CreatedAt  time.Time  `json:"created_at"`
}

// ListAPIKeys returns a tenant's keys, live ones first.
func (a *Authenticator) ListAPIKeys(ctx context.Context, tenantID uuid.UUID) ([]APIKey, error) {
	const q = `
		SELECT id, name, prefix, project_id, scopes, expires_at, last_used_at,
		       revoked_at, created_at
		FROM iam.api_keys
		WHERE tenant_id = $1
		ORDER BY revoked_at NULLS FIRST, created_at DESC`

	// Empty rather than nil: a JSON null makes every client that calls .length
	// on the result throw, and an empty list is the honest answer.
	out := []APIKey{}
	err := a.db.InTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, q, tenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var k APIKey
			if err := rows.Scan(&k.ID, &k.Name, &k.Prefix, &k.ProjectID, &k.Scopes,
				&k.ExpiresAt, &k.LastUsedAt, &k.RevokedAt, &k.CreatedAt); err != nil {
				return err
			}
			out = append(out, k)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("listing api keys: %w", err)
	}
	return out, nil
}

// RevokeAPIKey stops a key working, keeping the row.
//
// Revoked rather than deleted: the audit trail refers to it by id, and a
// credential that used to reach personal data should stay answerable for what
// it did rather than vanish from the record along with the evidence.
func (a *Authenticator) RevokeAPIKey(ctx context.Context, tenantID, id uuid.UUID) (bool, error) {
	var revoked bool
	err := a.db.InTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`UPDATE iam.api_keys SET revoked_at = now()
			 WHERE tenant_id = $1 AND id = $2 AND revoked_at IS NULL`, tenantID, id)
		if err != nil {
			return err
		}
		revoked = tag.RowsAffected() == 1
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("revoking api key: %w", err)
	}
	return revoked, nil
}

// ForbiddenAPIKeyScopes lists the capabilities a key may never hold.
//
// Exported so the interface can show them refused with a reason instead of
// omitting them, which would look like an oversight.
func ForbiddenAPIKeyScopes() []string {
	out := make([]string, 0, len(apiKeyForbidden))
	for c := range apiKeyForbidden {
		out = append(out, c)
	}
	return out
}
