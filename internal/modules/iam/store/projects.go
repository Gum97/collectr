package store

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/collectr/collectr/internal/modules/iam/domain"
	"github.com/collectr/collectr/internal/platform/postgres"
)

// Project groups the forms and links one team works on.
type Project struct {
	ID                   uuid.UUID
	TenantID             uuid.UUID
	Name                 string
	Slug                 string
	DefaultRetentionDays *int
	ArchivedAt           *time.Time
	CreatedBy            uuid.UUID
	CreatedAt            time.Time
	MemberCount          int
	// MyRole is the caller's role in this project, empty when they hold none.
	//
	// Returned alongside the project rather than fetched per row by the client:
	// the navigation tree shows every project in the organisation, including the
	// ones the reader cannot open, and it needs to say which is which without a
	// request per project.
	MyRole string
}

// CreateProject adds a project.
func (s *Store) CreateProject(ctx context.Context, p Project) (Project, error) {
	p.ID = uuid.New()
	const q = `
		INSERT INTO iam.projects (id, tenant_id, name, slug, default_retention_days, created_by)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING created_at`

	err := s.db.InTenantTx(ctx, p.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, q, p.ID, p.TenantID, p.Name, p.Slug,
			p.DefaultRetentionDays, p.CreatedBy).Scan(&p.CreatedAt)
	})
	if postgres.IsUniqueViolation(err, "") {
		return Project{}, domain.ErrAlreadyMember
	}
	if err != nil {
		return Project{}, fmt.Errorf("creating project: %w", err)
	}
	return p, nil
}

// ListProjects returns an organisation's projects.
func (s *Store) ListProjects(ctx context.Context, tenantID, userID uuid.UUID, includeArchived bool) ([]Project, error) {
	const q = `
		SELECT p.id, p.name, p.slug, p.default_retention_days, p.archived_at,
		       p.created_by, p.created_at,
		       (SELECT count(*) FROM iam.project_members m WHERE m.project_id = p.id),
		       coalesce(mine.role, '')
		FROM iam.projects p
		LEFT JOIN iam.project_members mine
		       ON mine.project_id = p.id AND mine.user_id = $3
		WHERE p.tenant_id = $1 AND ($2::bool OR p.archived_at IS NULL)
		ORDER BY p.created_at`

	var out []Project
	err := s.db.InTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, q, tenantID, includeArchived, userID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			p := Project{TenantID: tenantID}
			if err := rows.Scan(&p.ID, &p.Name, &p.Slug, &p.DefaultRetentionDays,
				&p.ArchivedAt, &p.CreatedBy, &p.CreatedAt, &p.MemberCount, &p.MyRole); err != nil {
				return err
			}
			out = append(out, p)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("listing projects: %w", err)
	}
	return out, nil
}

// UpdateProject changes a project's name or retention default.
func (s *Store) UpdateProject(ctx context.Context, tenantID, id uuid.UUID, name string, retentionDays *int) error {
	const q = `
		UPDATE iam.projects
		SET name = coalesce(nullif($3, ''), name),
		    default_retention_days = $4
		WHERE id = $2 AND tenant_id = $1`

	err := s.db.InTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, q, tenantID, id, name, retentionDays)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return domain.ErrNotFound
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("updating project: %w", err)
	}
	return nil
}

// ArchiveProject retires a project without deleting anything.
//
// Archived rather than deleted: the forms inside it hold personal data under a
// retention policy, and dropping the project would either orphan that data or
// delete it ahead of its schedule. Neither is the administrator's to decide by
// clicking "remove project".
func (s *Store) ArchiveProject(ctx context.Context, tenantID, id uuid.UUID) error {
	err := s.db.InTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`UPDATE iam.projects SET archived_at = now()
			 WHERE id = $2 AND tenant_id = $1 AND archived_at IS NULL`, tenantID, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return domain.ErrNotFound
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("archiving project: %w", err)
	}
	return nil
}

// ProjectMember is one person's role inside a project.
type ProjectMember struct {
	UserID    uuid.UUID
	Email     string
	Name      string
	Role      string
	GrantedAt time.Time
}

// ListProjectMembers returns who has access to a project.
func (s *Store) ListProjectMembers(ctx context.Context, tenantID, projectID uuid.UUID) ([]ProjectMember, error) {
	const q = `
		SELECT u.id, u.email, u.name, m.role, m.granted_at
		FROM iam.project_members m
		JOIN iam.users u ON u.id = m.user_id
		WHERE m.tenant_id = $1 AND m.project_id = $2
		ORDER BY m.granted_at`

	var out []ProjectMember
	err := s.db.InTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, q, tenantID, projectID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var m ProjectMember
			if err := rows.Scan(&m.UserID, &m.Email, &m.Name, &m.Role, &m.GrantedAt); err != nil {
				return err
			}
			out = append(out, m)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("listing project members: %w", err)
	}
	return out, nil
}

// GrantProjectRole adds or changes someone's role in a project.
func (s *Store) GrantProjectRole(ctx context.Context, tenantID, projectID, userID, grantedBy uuid.UUID, role string) error {
	err := s.db.InTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		// Membership of the organisation is the prerequisite: a project role
		// handed to somebody outside it would grant access with no way to sign in
		// and no record of them on the member list.
		var member bool
		if err := tx.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM iam.org_members WHERE tenant_id = $1 AND user_id = $2)`,
			tenantID, userID).Scan(&member); err != nil {
			return err
		}
		if !member {
			return domain.ErrNotFound
		}

		_, err := tx.Exec(ctx, `
			INSERT INTO iam.project_members (tenant_id, project_id, user_id, role, granted_by)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (project_id, user_id) DO UPDATE SET role = EXCLUDED.role`,
			tenantID, projectID, userID, role, grantedBy)
		return err
	})
	if err != nil {
		return fmt.Errorf("granting project role: %w", err)
	}
	return nil
}

// RevokeProjectRole removes someone from a project.
func (s *Store) RevokeProjectRole(ctx context.Context, tenantID, projectID, userID uuid.UUID) error {
	err := s.db.InTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`DELETE FROM iam.project_members
			 WHERE tenant_id = $1 AND project_id = $2 AND user_id = $3`,
			tenantID, projectID, userID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return domain.ErrNotFound
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("revoking project role: %w", err)
	}
	return nil
}

// ProjectName returns a project's display name, for report headers.
func (s *Store) ProjectName(ctx context.Context, tenantID, projectID uuid.UUID) (string, error) {
	var name string
	err := s.db.InTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT name FROM iam.projects WHERE tenant_id = $1 AND id = $2`,
			tenantID, projectID).Scan(&name)
	})
	if postgres.IsNoRows(err) {
		return "", domain.ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("reading project name: %w", err)
	}
	return name, nil
}

// UserEmail returns a member's email, for report provenance.
func (s *Store) UserEmail(ctx context.Context, tenantID, userID uuid.UUID) (string, error) {
	var email string
	err := s.db.InTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT u.email FROM iam.users u
			 JOIN iam.org_members m ON m.user_id = u.id
			 WHERE m.tenant_id = $1 AND u.id = $2`, tenantID, userID).Scan(&email)
	})
	if postgres.IsNoRows(err) {
		return "", domain.ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("reading user email: %w", err)
	}
	return email, nil
}

// Profile is who the signed-in person is, for the parts of the interface that
// name them.
type Profile struct {
	Email      string
	Name       string
	OrgRole    string
	MFAEnabled bool
	// CreatedAt anchors the window a privileged account has before it must
	// enrol a second factor.
	CreatedAt    time.Time
	OrgName      string
	RecoveryLeft int
}

// UserProfile reads the signed-in user's identity and organisation role.
func (s *Store) UserProfile(ctx context.Context, tenantID, userID uuid.UUID) (Profile, error) {
	const q = `
		SELECT u.email, u.name, m.role, u.mfa_enabled, u.created_at, t.name,
		       (SELECT count(*) FROM iam.mfa_recovery_codes c
		         WHERE c.user_id = u.id AND c.used_at IS NULL)
		FROM iam.users u
		JOIN iam.org_members m ON m.user_id = u.id AND m.tenant_id = $1
		JOIN iam.tenants t ON t.id = m.tenant_id
		WHERE u.id = $2`

	var p Profile
	err := s.db.InTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, q, tenantID, userID).
			Scan(&p.Email, &p.Name, &p.OrgRole, &p.MFAEnabled, &p.CreatedAt, &p.OrgName, &p.RecoveryLeft)
	})
	if postgres.IsNoRows(err) {
		return Profile{}, domain.ErrNotFound
	}
	if err != nil {
		return Profile{}, fmt.Errorf("reading user profile: %w", err)
	}
	return p, nil
}
