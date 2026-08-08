// Package authn identifies callers and carries their capabilities through the
// request context.
//
// Capabilities, not role names, are what handlers check. Roles are only a way to
// package capabilities; separating them keeps "can read submissions" independent
// from "can read the sensitive fields inside them" and from "can export them in
// bulk" -- three permissions that carry very different risk.
package authn

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/collectr/collectr/internal/platform/httpx"
	"github.com/collectr/collectr/internal/platform/postgres"
)

// Capabilities checked across the system.
const (
	CapLinkRead   = "link.read"
	CapLinkWrite  = "link.write"
	CapLinkDelete = "link.delete"

	CapFormRead    = "form.read"
	CapFormWrite   = "form.write"
	CapFormPublish = "form.publish"

	CapSubmissionRead          = "submission.read"
	CapSubmissionReadSensitive = "submission.read_sensitive"
	CapSubmissionExport        = "submission.export"

	CapAnalyticsRead = "analytics.read"
	CapConsentManage = "consent.manage"
	CapDSRHandle     = "dsr.handle"
	CapAuditRead     = "audit.read"
	CapAPIKeyManage  = "apikey.manage"
	CapWebhookManage = "webhook.manage"
	CapMemberManage  = "member.manage"
)

// apiKeyForbidden lists capabilities an API key may never hold, whatever scopes
// it was created with. Handling a data subject's request and revealing sensitive
// fields are acts that need an accountable person behind them, not a string
// sitting in a CI configuration.
var apiKeyForbidden = map[string]struct{}{
	CapSubmissionReadSensitive: {},
	CapDSRHandle:               {},
	CapAuditRead:               {},
	CapMemberManage:            {},
}

// ErrUnauthenticated means no valid credential was presented.
var ErrUnauthenticated = errors.New("unauthenticated")

// Kind distinguishes how a caller authenticated.
type Kind string

// Actor kinds.
const (
	KindUser   Kind = "user"
	KindAPIKey Kind = "api_key"
)

// SessionID is set on actors that came from a signed-in user, so that a handler
// can revoke the exact session it is serving.
type SessionID = uuid.UUID

// Actor is an authenticated caller.
type Actor struct {
	Kind      Kind
	UserID    uuid.UUID
	TenantID  uuid.UUID
	ProjectID *uuid.UUID // set when the credential is scoped to one project
	KeyID     *uuid.UUID
	caps      map[string]struct{}

	// orgWide is true when the organisation role itself grants something, which
	// is what makes owner, admin and DPO able to reach every project. A plain
	// member holds nothing at that level and reaches only what was granted.
	orgWide bool
	// projects are the projects granted explicitly to this user.
	projects map[uuid.UUID]struct{}
}

// Can reports whether the actor holds cap.
func (a Actor) Can(cap string) bool {
	_, ok := a.caps[cap]
	return ok
}

// InProject reports whether the actor may act within projectID.
//
// Holding a capability is never sufficient on its own: the object also has to
// belong to something the caller was granted. Checking only the capability is the
// broken-object-level-authorisation bug, and it is the most common serious flaw
// in APIs of this shape.
func (a Actor) InProject(projectID uuid.UUID) bool {
	// A credential pinned to one project can only ever act there.
	if a.ProjectID != nil {
		return *a.ProjectID == projectID
	}
	// An organisation-level role spans every project by design.
	if a.orgWide {
		return true
	}
	// Otherwise it is exactly the projects granted.
	//
	// This used to return true unconditionally for every signed-in user, because
	// the resolver never carried the grants this far. Every call site below was
	// therefore dead code, and a member with a viewer role on one project could
	// read the personal data in all of them.
	_, ok := a.projects[projectID]
	return ok
}

// Projects lists the projects reachable by an explicit grant. Empty for an
// organisation-wide role, which is not the same as reaching nothing.
func (a Actor) Projects() []uuid.UUID {
	out := make([]uuid.UUID, 0, len(a.projects))
	for id := range a.projects {
		out = append(out, id)
	}
	return out
}

// OrgWide reports whether the actor's reach comes from an organisation role.
func (a Actor) OrgWide() bool { return a.orgWide }

// WithProjectScope returns a copy carrying the projects a user was granted.
//
// Separate from NewActor so the API-key path, which is pinned by ProjectID, is
// not silently widened by a caller passing the wrong thing.
func (a Actor) WithProjectScope(orgWide bool, projects []uuid.UUID) Actor {
	a.orgWide = orgWide
	a.projects = make(map[uuid.UUID]struct{}, len(projects))
	for _, id := range projects {
		a.projects[id] = struct{}{}
	}
	return a
}

// NewActor builds an actor from a resolved capability set.
//
// Exported because identity now arrives two ways -- an API key, or a signed-in
// user -- and only the IAM module knows how to turn roles into capabilities.
// Keeping Actor's field unexported means nothing else can quietly widen one.
func NewActor(kind Kind, userID, tenantID uuid.UUID, projectID *uuid.UUID, caps []string) Actor {
	set := make(map[string]struct{}, len(caps))
	for _, c := range caps {
		set[c] = struct{}{}
	}
	return Actor{
		Kind: kind, UserID: userID, TenantID: tenantID,
		ProjectID: projectID, caps: set,
	}
}

// Capabilities returns the actor's capabilities, sorted.
func (a Actor) Capabilities() []string {
	out := make([]string, 0, len(a.caps))
	for c := range a.caps {
		out = append(out, c)
	}
	slices.Sort(out)
	return out
}

type ctxKey int

const ctxKeyActor ctxKey = iota

// ActorFrom returns the actor bound to ctx.
func ActorFrom(ctx context.Context) (Actor, bool) {
	a, ok := ctx.Value(ctxKeyActor).(Actor)
	return a, ok
}

// WithActor binds an actor to ctx. Exported for tests and for the session layer.
func WithActor(ctx context.Context, a Actor) context.Context {
	return context.WithValue(ctx, ctxKeyActor, a)
}

// Authenticator resolves credentials into actors.
type Authenticator struct {
	db *postgres.DB
}

// NewAuthenticator returns an Authenticator.
func NewAuthenticator(db *postgres.DB) *Authenticator { return &Authenticator{db: db} }

// Middleware authenticates the request, rejecting it if no credential resolves.
func (a *Authenticator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := bearerToken(r)
		if !ok {
			httpx.Error(w, r, http.StatusUnauthorized, "unauthenticated", "Authentication required")
			return
		}
		actor, err := a.FromAPIKey(r.Context(), token)
		if err != nil {
			if !errors.Is(err, ErrUnauthenticated) {
				httpx.Logger(r.Context()).Error("authenticating request", "error", err)
			}
			httpx.Error(w, r, http.StatusUnauthorized, "unauthenticated", "Invalid credentials")
			return
		}
		next.ServeHTTP(w, r.WithContext(WithActor(r.Context(), actor)))
	})
}

// FromAPIKey resolves a raw API key into an Actor.
//
// The key is hashed with SHA-256 rather than a password hash: it carries 256 bits
// of entropy from a CSPRNG, so there is nothing to brute-force, and a memory-hard
// hash would add tens of milliseconds to every single API call for no gain.
func (a *Authenticator) FromAPIKey(ctx context.Context, raw string) (Actor, error) {
	prefix, _, ok := splitKey(raw)
	if !ok {
		return Actor{}, ErrUnauthenticated
	}

	const q = `
		SELECT id, tenant_id, project_id, key_hash, scopes, created_by, expires_at, revoked_at
		FROM iam.api_keys
		WHERE prefix = $1`

	var (
		keyID     uuid.UUID
		tenantID  uuid.UUID
		projectID *uuid.UUID
		hash      []byte
		scopes    []string
		createdBy uuid.UUID
		expiresAt *time.Time
		revokedAt *time.Time
	)
	err := a.db.QueryRow(ctx, q, prefix).Scan(
		&keyID, &tenantID, &projectID, &hash, &scopes, &createdBy, &expiresAt, &revokedAt,
	)
	if postgres.IsNoRows(err) {
		return Actor{}, ErrUnauthenticated
	}
	if err != nil {
		return Actor{}, fmt.Errorf("loading api key: %w", err)
	}

	sum := sha256.Sum256([]byte(raw))
	if subtle.ConstantTimeCompare(sum[:], hash) != 1 {
		return Actor{}, ErrUnauthenticated
	}
	if revokedAt != nil || (expiresAt != nil && time.Now().After(*expiresAt)) {
		return Actor{}, ErrUnauthenticated
	}

	caps := make(map[string]struct{}, len(scopes))
	for _, s := range scopes {
		if _, forbidden := apiKeyForbidden[s]; forbidden {
			continue
		}
		caps[s] = struct{}{}
	}

	// Best-effort usage stamp: it feeds "this key has not been used in months"
	// hygiene reports and must never fail the request it is describing.
	go func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		_, _ = a.db.Exec(ctx, "UPDATE iam.api_keys SET last_used_at = now() WHERE id = $1", keyID)
	}()

	return Actor{
		Kind:      KindAPIKey,
		UserID:    createdBy,
		TenantID:  tenantID,
		ProjectID: projectID,
		KeyID:     &keyID,
		caps:      caps,
	}, nil
}

// RequireCap rejects requests whose actor lacks cap.
func RequireCap(cap string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			actor, ok := ActorFrom(r.Context())
			if !ok {
				httpx.Error(w, r, http.StatusUnauthorized, "unauthenticated", "Authentication required")
				return
			}
			if !actor.Can(cap) {
				// 403, not 404: the caller is known, they simply may not do this.
				httpx.Error(w, r, http.StatusForbidden, "forbidden", "Insufficient permissions")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// IssueAPIKey creates a key and returns the raw value, which is shown once and
// never stored.
func (a *Authenticator) IssueAPIKey(ctx context.Context, tenantID uuid.UUID, projectID *uuid.UUID, createdBy uuid.UUID, name string, scopes []string, ttl time.Duration) (string, uuid.UUID, error) {
	raw, prefix, err := generateKey()
	if err != nil {
		return "", uuid.Nil, err
	}
	sum := sha256.Sum256([]byte(raw))
	id := uuid.New()
	expires := time.Now().Add(ttl)

	const q = `
		INSERT INTO iam.api_keys
			(id, tenant_id, project_id, name, prefix, key_hash, scopes, expires_at, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`

	err = a.db.InTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, q, id, tenantID, projectID, name, prefix, sum[:], scopes, expires, createdBy)
		return err
	})
	if err != nil {
		return "", uuid.Nil, fmt.Errorf("issuing api key: %w", err)
	}
	return raw, id, nil
}

func bearerToken(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	scheme, token, ok := strings.Cut(h, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || token == "" {
		return "", false
	}
	return token, true
}

// splitKey extracts the lookup prefix from a raw key of the form
// clc_<env>_<prefix><secret>.
func splitKey(raw string) (prefix, secret string, ok bool) {
	parts := strings.SplitN(raw, "_", 3)
	if len(parts) != 3 || parts[0] != "clc" || len(parts[2]) < 16 {
		return "", "", false
	}
	return parts[2][:8], parts[2][8:], true
}
