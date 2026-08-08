package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/collectr/collectr/internal/modules/iam/domain"
	"github.com/collectr/collectr/internal/platform/postgres"
)

// ProjectGrant is one project role handed out with an invitation.
type ProjectGrant struct {
	ProjectID uuid.UUID `json:"project_id"`
	Role      string    `json:"role"`
}

// Invitation is a pending offer of membership.
type Invitation struct {
	ID        uuid.UUID
	TenantID  uuid.UUID
	Email     string
	OrgRole   string
	Grants    []ProjectGrant
	ExpiresAt time.Time
	InvitedBy uuid.UUID
	CreatedAt time.Time
}

// CreateInvitation records an offer and returns it.
//
// Any earlier pending invitation for the same address is superseded: leaving
// several live at once would mean revoking one and quietly leaving another open.
func (s *Store) CreateInvitation(ctx context.Context, inv Invitation, rawToken string) (Invitation, error) {
	grants, err := json.Marshal(inv.Grants)
	if err != nil {
		return Invitation{}, fmt.Errorf("encoding project grants: %w", err)
	}

	inv.ID = uuid.New()
	err = s.db.InTenantTx(ctx, inv.TenantID, func(tx pgx.Tx) error {
		var alreadyMember bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM iam.org_members m
				JOIN iam.users u ON u.id = m.user_id
				WHERE m.tenant_id = $1 AND u.email = $2
			)`, inv.TenantID, inv.Email).Scan(&alreadyMember); err != nil {
			return err
		}
		if alreadyMember {
			return domain.ErrAlreadyMember
		}

		if _, err := tx.Exec(ctx, `
			DELETE FROM iam.invitations
			WHERE tenant_id = $1 AND email = $2 AND accepted_at IS NULL`,
			inv.TenantID, inv.Email); err != nil {
			return err
		}

		_, err := tx.Exec(ctx, `
			INSERT INTO iam.invitations
				(id, tenant_id, email, org_role, project_grants, token_hash, expires_at, invited_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
			inv.ID, inv.TenantID, inv.Email, inv.OrgRole, grants,
			Hash(rawToken), inv.ExpiresAt, inv.InvitedBy)
		return err
	})
	if err != nil {
		return Invitation{}, err
	}
	return inv, nil
}

// FindInvitation resolves a raw invitation token.
func (s *Store) FindInvitation(ctx context.Context, rawToken string) (Invitation, error) {
	const q = `
		SELECT id, tenant_id, email, org_role, project_grants, expires_at, invited_by, created_at
		FROM iam.invitations
		WHERE token_hash = $1 AND accepted_at IS NULL AND expires_at > now()`

	var (
		inv    Invitation
		grants []byte
	)
	// Deliberately not tenant-scoped: whoever follows the link has no way to know
	// which organisation invited them, and the token itself is the proof.
	err := s.db.QueryRow(ctx, q, Hash(rawToken)).Scan(
		&inv.ID, &inv.TenantID, &inv.Email, &inv.OrgRole, &grants,
		&inv.ExpiresAt, &inv.InvitedBy, &inv.CreatedAt)
	if postgres.IsNoRows(err) {
		return Invitation{}, domain.ErrNotFound
	}
	if err != nil {
		return Invitation{}, fmt.Errorf("finding invitation: %w", err)
	}
	if err := json.Unmarshal(grants, &inv.Grants); err != nil {
		return Invitation{}, fmt.Errorf("decoding project grants: %w", err)
	}
	return inv, nil
}

// AcceptInvitation joins the invited person to the organisation.
//
// Creating the account, the membership and the project roles in one transaction
// keeps a half-joined user from existing: someone with a login and no access, or
// access with no way to sign in.
func (s *Store) AcceptInvitation(ctx context.Context, inv Invitation, name, passwordHash string) (uuid.UUID, error) {
	var userID uuid.UUID

	err := s.db.InTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT set_config('app.tenant_id', $1, true)", inv.TenantID.String()); err != nil {
			return err
		}

		// Re-check inside the transaction: the invitation may have been revoked
		// between the page loading and the form being submitted.
		var stillOpen bool
		if err := tx.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM iam.invitations
			 WHERE id = $1 AND accepted_at IS NULL AND expires_at > now())`,
			inv.ID).Scan(&stillOpen); err != nil {
			return err
		}
		if !stillOpen {
			return domain.ErrNotFound
		}

		// An existing account joining a second organisation keeps its password;
		// only a brand-new account sets one.
		err := tx.QueryRow(ctx, `SELECT id FROM iam.users WHERE email = $1`, inv.Email).Scan(&userID)
		switch {
		case postgres.IsNoRows(err):
			if passwordHash == "" {
				return domain.ErrInvalidCredentials
			}
			userID = uuid.New()
			if _, err := tx.Exec(ctx,
				`INSERT INTO iam.users (id, email, name, password_hash) VALUES ($1, $2, $3, $4)`,
				userID, inv.Email, name, passwordHash); err != nil {
				return err
			}
		case err != nil:
			return err
		}

		if _, err := tx.Exec(ctx, `
			INSERT INTO iam.org_members (tenant_id, user_id, role)
			VALUES ($1, $2, $3)
			ON CONFLICT (tenant_id, user_id) DO UPDATE SET role = EXCLUDED.role`,
			inv.TenantID, userID, inv.OrgRole); err != nil {
			return err
		}

		for _, g := range inv.Grants {
			if _, err := tx.Exec(ctx, `
				INSERT INTO iam.project_members (tenant_id, project_id, user_id, role, granted_by)
				VALUES ($1, $2, $3, $4, $5)
				ON CONFLICT (project_id, user_id) DO UPDATE SET role = EXCLUDED.role`,
				inv.TenantID, g.ProjectID, userID, g.Role, inv.InvitedBy); err != nil {
				return err
			}
		}

		_, err = tx.Exec(ctx,
			`UPDATE iam.invitations SET accepted_at = now() WHERE id = $1`, inv.ID)
		return err
	})
	if err != nil {
		return uuid.Nil, err
	}
	return userID, nil
}

// ListInvitations returns the pending offers for an organisation.
func (s *Store) ListInvitations(ctx context.Context, tenantID uuid.UUID) ([]Invitation, error) {
	const q = `
		SELECT id, tenant_id, email, org_role, project_grants, expires_at, invited_by, created_at
		FROM iam.invitations
		WHERE tenant_id = $1 AND accepted_at IS NULL AND expires_at > now()
		ORDER BY created_at DESC`

	var out []Invitation
	err := s.db.InTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, q, tenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var (
				inv    Invitation
				grants []byte
			)
			if err := rows.Scan(&inv.ID, &inv.TenantID, &inv.Email, &inv.OrgRole, &grants,
				&inv.ExpiresAt, &inv.InvitedBy, &inv.CreatedAt); err != nil {
				return err
			}
			if err := json.Unmarshal(grants, &inv.Grants); err != nil {
				return err
			}
			out = append(out, inv)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("listing invitations: %w", err)
	}
	return out, nil
}

// RevokeInvitation withdraws a pending offer.
func (s *Store) RevokeInvitation(ctx context.Context, tenantID, id uuid.UUID) error {
	err := s.db.InTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`DELETE FROM iam.invitations WHERE id = $1 AND accepted_at IS NULL`, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return domain.ErrNotFound
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("revoking invitation: %w", err)
	}
	return nil
}

// PurgeExpiredInvitations removes offers nobody took up.
func (s *Store) PurgeExpiredInvitations(ctx context.Context) (int64, error) {
	tag, err := s.db.Exec(ctx,
		`DELETE FROM iam.invitations WHERE accepted_at IS NULL AND expires_at < now()`)
	if err != nil {
		return 0, fmt.Errorf("purging invitations: %w", err)
	}
	return tag.RowsAffected(), nil
}

// Member is one person's standing in an organisation.
type Member struct {
	UserID   uuid.UUID
	Email    string
	Name     string
	OrgRole  string
	JoinedAt time.Time
}

// ListMembers returns everyone in an organisation.
func (s *Store) ListMembers(ctx context.Context, tenantID uuid.UUID) ([]Member, error) {
	const q = `
		SELECT u.id, u.email, u.name, m.role, m.joined_at
		FROM iam.org_members m
		JOIN iam.users u ON u.id = m.user_id
		WHERE m.tenant_id = $1
		ORDER BY m.joined_at`

	var out []Member
	err := s.db.InTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, q, tenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var m Member
			if err := rows.Scan(&m.UserID, &m.Email, &m.Name, &m.OrgRole, &m.JoinedAt); err != nil {
				return err
			}
			out = append(out, m)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("listing members: %w", err)
	}
	return out, nil
}

// RemoveMember takes someone out of an organisation.
//
// The last owner cannot be removed: an organisation with nobody able to manage
// it is unrecoverable without database access.
func (s *Store) RemoveMember(ctx context.Context, tenantID, userID uuid.UUID) error {
	err := s.db.InTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		var role string
		err := tx.QueryRow(ctx,
			`SELECT role FROM iam.org_members WHERE tenant_id = $1 AND user_id = $2 FOR UPDATE`,
			tenantID, userID).Scan(&role)
		if postgres.IsNoRows(err) {
			return domain.ErrNotFound
		}
		if err != nil {
			return err
		}

		if role == domain.RoleOwner {
			var owners int
			if err := tx.QueryRow(ctx,
				`SELECT count(*) FROM iam.org_members WHERE tenant_id = $1 AND role = 'owner'`,
				tenantID).Scan(&owners); err != nil {
				return err
			}
			if owners <= 1 {
				return domain.ErrLastOwner
			}
		}

		if _, err := tx.Exec(ctx,
			`DELETE FROM iam.project_members WHERE tenant_id = $1 AND user_id = $2`,
			tenantID, userID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`DELETE FROM iam.org_members WHERE tenant_id = $1 AND user_id = $2`,
			tenantID, userID); err != nil {
			return err
		}

		// Sessions and API keys go in the same transaction. "No longer has
		// access" has to be true immediately, not once a token expires.
		if _, err := tx.Exec(ctx,
			`UPDATE iam.sessions SET revoked_at = now(), revoked_reason = 'removed from organisation'
			 WHERE user_id = $1 AND tenant_id = $2 AND revoked_at IS NULL`,
			userID, tenantID); err != nil {
			return err
		}
		_, err = tx.Exec(ctx,
			`UPDATE iam.api_keys SET revoked_at = now()
			 WHERE tenant_id = $1 AND created_by = $2 AND revoked_at IS NULL`,
			tenantID, userID)
		return err
	})
	if err != nil {
		return fmt.Errorf("removing member: %w", err)
	}
	return nil
}
