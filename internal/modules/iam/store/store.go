// Package store persists users, sessions and memberships.
package store

import (
	"context"
	"crypto/sha256"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/collectr/collectr/internal/modules/iam/domain"
	"github.com/collectr/collectr/internal/platform/postgres"
)

// Store reads and writes the iam schema.
type Store struct {
	db *postgres.DB
}

// New returns a Store.
func New(db *postgres.DB) *Store { return &Store{db: db} }

// DB exposes the pool for composing writes with other modules.
func (s *Store) DB() *postgres.DB { return s.db }

// Hash reduces a session or invitation token to what is stored.
func Hash(raw string) []byte {
	sum := sha256.Sum256([]byte(raw))
	return sum[:]
}

// User is an account.
type User struct {
	ID           uuid.UUID
	Email        string
	Name         string
	PasswordHash string
	MFASecret    []byte
	MFAEnabled   bool
	Status       string
	// CreatedAt anchors the window a privileged account has before it must
	// enrol a second factor.
	CreatedAt time.Time
}

// Membership is what a user holds inside one organisation.
type Membership struct {
	TenantID   uuid.UUID
	TenantName string
	OrgRole    string
	// ProjectRoles is the role held in each project, keyed by project.
	//
	// Keyed, not flattened. It used to be a bare list, which meant the capability
	// set could be resolved but not the question that matters at every write:
	// *which* project this person may act in. Losing the key made every
	// InProject check pass.
	ProjectRoles map[uuid.UUID]string
}

// ProjectIDs returns the projects an explicit grant covers.
func (m Membership) ProjectIDs() []uuid.UUID {
	out := make([]uuid.UUID, 0, len(m.ProjectRoles))
	for id := range m.ProjectRoles {
		out = append(out, id)
	}
	return out
}

// RoleList returns the project roles alone, for capability resolution.
func (m Membership) RoleList() []string {
	out := make([]string, 0, len(m.ProjectRoles))
	for _, r := range m.ProjectRoles {
		out = append(out, r)
	}
	return out
}

// Session is a live sign-in.
type Session struct {
	ID        uuid.UUID
	UserID    uuid.UUID
	TenantID  uuid.UUID
	ExpiresAt time.Time
}

// HasAnyUser reports whether the deployment has been set up.
//
// A fresh install has no way in, so the bootstrap endpoint stays open until the
// first account exists and then closes itself permanently.
func (s *Store) HasAnyUser(ctx context.Context) (bool, error) {
	var exists bool
	if err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM iam.users)`).Scan(&exists); err != nil {
		return false, fmt.Errorf("checking for existing users: %w", err)
	}
	return exists, nil
}

// FindUserByEmail loads an account for sign-in.
//
// A missing account is not an error: the caller must behave identically either
// way, or the login form becomes a way to test which addresses have accounts.
func (s *Store) FindUserByEmail(ctx context.Context, email string) (User, bool, error) {
	const q = `
		SELECT id, email, name, coalesce(password_hash, ''), mfa_secret_enc, mfa_enabled, status, created_at
		FROM iam.users WHERE email = $1`

	var u User
	err := s.db.QueryRow(ctx, q, email).Scan(
		&u.ID, &u.Email, &u.Name, &u.PasswordHash, &u.MFASecret, &u.MFAEnabled, &u.Status, &u.CreatedAt)
	if postgres.IsNoRows(err) {
		return User{}, false, nil
	}
	if err != nil {
		return User{}, false, fmt.Errorf("finding user: %w", err)
	}
	return u, true, nil
}

// FindUserByID loads an account by id.
func (s *Store) FindUserByID(ctx context.Context, id uuid.UUID) (User, error) {
	const q = `
		SELECT id, email, name, coalesce(password_hash, ''), mfa_secret_enc, mfa_enabled, status, created_at
		FROM iam.users WHERE id = $1`

	var u User
	err := s.db.QueryRow(ctx, q, id).Scan(
		&u.ID, &u.Email, &u.Name, &u.PasswordHash, &u.MFASecret, &u.MFAEnabled, &u.Status, &u.CreatedAt)
	if postgres.IsNoRows(err) {
		return User{}, domain.ErrNotFound
	}
	if err != nil {
		return User{}, fmt.Errorf("finding user: %w", err)
	}
	return u, nil
}

// RegisterLoginAttempt counts a sign-in attempt and reports whether it is allowed.
func (s *Store) RegisterLoginAttempt(ctx context.Context, email string, limit int, window time.Duration) (bool, error) {
	windowStart := time.Now().UTC().Truncate(window)

	const q = `
		INSERT INTO iam.login_attempts (email, window_start, attempts)
		VALUES ($1, $2, 1)
		ON CONFLICT (email, window_start) DO UPDATE
			SET attempts = iam.login_attempts.attempts + 1
		RETURNING attempts`

	var attempts int
	if err := s.db.QueryRow(ctx, q, email, windowStart).Scan(&attempts); err != nil {
		return false, fmt.Errorf("registering login attempt: %w", err)
	}
	return attempts <= limit, nil
}

// ClearLoginAttempts resets the counter after a successful sign-in.
func (s *Store) ClearLoginAttempts(ctx context.Context, email string) error {
	if _, err := s.db.Exec(ctx, `DELETE FROM iam.login_attempts WHERE email = $1`, email); err != nil {
		return fmt.Errorf("clearing login attempts: %w", err)
	}
	return nil
}

// Memberships returns every organisation a user belongs to.
func (s *Store) Memberships(ctx context.Context, userID uuid.UUID) ([]Membership, error) {
	const q = `
		SELECT m.tenant_id, t.name, m.role,
		       coalesce(
		         jsonb_object_agg(pm.project_id, pm.role) FILTER (WHERE pm.role IS NOT NULL),
		         '{}'::jsonb
		       ) AS project_roles
		FROM iam.org_members m
		JOIN iam.tenants t ON t.id = m.tenant_id
		LEFT JOIN iam.project_members pm
			ON pm.user_id = m.user_id AND pm.tenant_id = m.tenant_id
		WHERE m.user_id = $1
		GROUP BY m.tenant_id, t.name, m.role
		ORDER BY t.name`

	rows, err := s.db.Query(ctx, q, userID)
	if err != nil {
		return nil, fmt.Errorf("loading memberships: %w", err)
	}
	defer rows.Close()

	var out []Membership
	for rows.Next() {
		var m Membership
		var roles map[string]string
		if err := rows.Scan(&m.TenantID, &m.TenantName, &m.OrgRole, &roles); err != nil {
			return nil, fmt.Errorf("scanning membership: %w", err)
		}
		m.ProjectRoles = make(map[uuid.UUID]string, len(roles))
		for raw, role := range roles {
			id, err := uuid.Parse(raw)
			if err != nil {
				return nil, fmt.Errorf("scanning project role key %q: %w", raw, err)
			}
			m.ProjectRoles[id] = role
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// Membership returns one user's standing in one organisation.
func (s *Store) Membership(ctx context.Context, userID, tenantID uuid.UUID) (Membership, error) {
	all, err := s.Memberships(ctx, userID)
	if err != nil {
		return Membership{}, err
	}
	for _, m := range all {
		if m.TenantID == tenantID {
			return m, nil
		}
	}
	return Membership{}, domain.ErrNotFound
}

// TenantName returns an organisation's display name.
func (s *Store) TenantName(ctx context.Context, tenantID uuid.UUID) (string, error) {
	var name string
	err := s.db.QueryRow(ctx, `SELECT name FROM iam.tenants WHERE id = $1`, tenantID).Scan(&name)
	if postgres.IsNoRows(err) {
		return "", domain.ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("loading organisation name: %w", err)
	}
	return name, nil
}

// CreateSession records a sign-in.
func (s *Store) CreateSession(ctx context.Context, userID, tenantID uuid.UUID, raw, ipPrefix, userAgent string, ttl time.Duration) (Session, error) {
	sess := Session{
		ID: uuid.New(), UserID: userID, TenantID: tenantID,
		ExpiresAt: time.Now().UTC().Add(ttl),
	}
	const q = `
		INSERT INTO iam.sessions
			(id, user_id, token_hash, tenant_id, ip_prefix, user_agent, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`

	if _, err := s.db.Exec(ctx, q, sess.ID, userID, Hash(raw), tenantID,
		nullable(ipPrefix), nullable(truncate(userAgent, 512)), sess.ExpiresAt); err != nil {
		return Session{}, fmt.Errorf("creating session: %w", err)
	}
	return sess, nil
}

// ResolveSession loads a live session by its raw token.
//
// Revocation is checked here, on every request. That is the whole reason sessions
// are rows rather than signed cookies: withdrawing access has to take effect when
// the decision is made, not when the token happens to expire.
func (s *Store) ResolveSession(ctx context.Context, raw string) (Session, User, error) {
	const q = `
		SELECT s.id, s.user_id, s.tenant_id, s.expires_at,
		       u.email, u.name, u.status, u.mfa_enabled, u.created_at
		FROM iam.sessions s
		JOIN iam.users u ON u.id = s.user_id
		WHERE s.token_hash = $1
		  AND s.revoked_at IS NULL
		  AND s.expires_at > now()`

	var (
		sess Session
		u    User
	)
	err := s.db.QueryRow(ctx, q, Hash(raw)).Scan(
		&sess.ID, &sess.UserID, &sess.TenantID, &sess.ExpiresAt,
		&u.Email, &u.Name, &u.Status, &u.MFAEnabled, &u.CreatedAt)
	if postgres.IsNoRows(err) {
		return Session{}, User{}, domain.ErrSessionInvalid
	}
	if err != nil {
		return Session{}, User{}, fmt.Errorf("resolving session: %w", err)
	}
	if u.Status != "active" {
		return Session{}, User{}, domain.ErrSessionInvalid
	}
	u.ID = sess.UserID
	return sess, u, nil
}

// TouchSession records that a session was used.
func (s *Store) TouchSession(ctx context.Context, id uuid.UUID) error {
	// Once a minute is enough for "last seen"; writing on every request would
	// turn a read-mostly path into a write on every call.
	if _, err := s.db.Exec(ctx,
		`UPDATE iam.sessions SET last_seen_at = now()
		 WHERE id = $1 AND last_seen_at < now() - interval '1 minute'`, id); err != nil {
		return fmt.Errorf("touching session: %w", err)
	}
	return nil
}

// RevokeSession ends one session.
func (s *Store) RevokeSession(ctx context.Context, id uuid.UUID, reason string) error {
	if _, err := s.db.Exec(ctx,
		`UPDATE iam.sessions SET revoked_at = now(), revoked_reason = $2
		 WHERE id = $1 AND revoked_at IS NULL`, id, reason); err != nil {
		return fmt.Errorf("revoking session: %w", err)
	}
	return nil
}

// RevokeUserSessions ends every session a user holds.
func (s *Store) RevokeUserSessions(ctx context.Context, userID uuid.UUID, reason string) (int, error) {
	var n int
	if err := s.db.QueryRow(ctx,
		`SELECT iam.revoke_user_sessions($1, $2)`, userID, reason).Scan(&n); err != nil {
		return 0, fmt.Errorf("revoking user sessions: %w", err)
	}
	return n, nil
}

// PurgeExpiredSessions removes sessions that are long finished.
func (s *Store) PurgeExpiredSessions(ctx context.Context, olderThan time.Duration) (int64, error) {
	tag, err := s.db.Exec(ctx,
		`DELETE FROM iam.sessions WHERE expires_at < $1 OR revoked_at < $1`,
		time.Now().UTC().Add(-olderThan))
	if err != nil {
		return 0, fmt.Errorf("purging sessions: %w", err)
	}
	return tag.RowsAffected(), nil
}

// Bootstrap creates the first organisation, owner and project in one transaction.
func (s *Store) Bootstrap(ctx context.Context, orgName, orgSlug, email, name, passwordHash, linkHost string) (uuid.UUID, uuid.UUID, error) {
	var (
		tenantID = uuid.New()
		userID   = uuid.New()
	)
	err := s.db.InTx(ctx, func(tx pgx.Tx) error {
		// Re-checked inside the transaction: two simultaneous setup requests must
		// not both succeed and leave two owners nobody expected.
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM iam.users)`).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return domain.ErrAlreadyMember
		}

		if _, err := tx.Exec(ctx,
			`INSERT INTO iam.tenants (id, name, slug) VALUES ($1, $2, $3)`,
			tenantID, orgName, orgSlug); err != nil {
			return err
		}
		// Set here rather than when the transaction opened, because until the
		// line above there was no tenant to name. Everything below this point
		// writes to a table under row-level security, which without the setting
		// rejects the insert -- so setup fails on a fresh install while working
		// on any deployment created before those policies were tightened.
		if _, err := tx.Exec(ctx,
			`SELECT set_config('app.tenant_id', $1, true)`, tenantID.String()); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO iam.users (id, email, name, password_hash) VALUES ($1, $2, $3, $4)`,
			userID, email, name, passwordHash); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO iam.org_members (tenant_id, user_id, role) VALUES ($1, $2, 'owner')`,
			tenantID, userID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO iam.projects (id, tenant_id, name, slug, created_by)
			 VALUES ($1, $2, 'Mặc định', 'default', $3)`,
			uuid.New(), tenantID, userID); err != nil {
			return err
		}
		// The first link domain, without which no short code can be issued at
		// all. Created here rather than left to the operator because there is no
		// useful deployment without one, and a setup that completes but cannot
		// create a link reports success for a system that does not work.
		_, err := tx.Exec(ctx,
			`INSERT INTO links.domains (id, tenant_id, host, is_default)
			 VALUES ($1, $2, $3, true)`,
			uuid.New(), tenantID, linkHost)
		return err
	})
	if err != nil {
		return uuid.Nil, uuid.Nil, fmt.Errorf("bootstrapping deployment: %w", err)
	}
	return tenantID, userID, nil
}

// SetMFA stores a user's TOTP secret.
func (s *Store) SetMFA(ctx context.Context, userID uuid.UUID, secret []byte, enabled bool) error {
	if _, err := s.db.Exec(ctx,
		`UPDATE iam.users SET mfa_secret_enc = $2, mfa_enabled = $3 WHERE id = $1`,
		userID, secret, enabled); err != nil {
		return fmt.Errorf("storing mfa secret: %w", err)
	}
	return nil
}

// UpdatePasswordHash replaces a user's password.
func (s *Store) UpdatePasswordHash(ctx context.Context, userID uuid.UUID, hash string) error {
	if _, err := s.db.Exec(ctx,
		`UPDATE iam.users SET password_hash = $2 WHERE id = $1`, userID, hash); err != nil {
		return fmt.Errorf("updating password: %w", err)
	}
	return nil
}

// TouchLogin records a successful sign-in.
func (s *Store) TouchLogin(ctx context.Context, userID uuid.UUID) error {
	if _, err := s.db.Exec(ctx,
		`UPDATE iam.users SET last_login_at = now() WHERE id = $1`, userID); err != nil {
		return fmt.Errorf("recording login: %w", err)
	}
	return nil
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
