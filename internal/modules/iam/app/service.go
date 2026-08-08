// Package app implements sign-in, sessions and capability resolution.
package app

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/collectr/collectr/internal/modules/iam/domain"
	"github.com/collectr/collectr/internal/modules/iam/store"
	"github.com/collectr/collectr/internal/platform/authn"
	"github.com/collectr/collectr/internal/platform/crypto"
	"github.com/collectr/collectr/internal/platform/notify"
	"github.com/collectr/collectr/internal/platform/password"
	"github.com/collectr/collectr/internal/platform/redisx"
	"github.com/collectr/collectr/internal/platform/slug"
	"github.com/collectr/collectr/internal/platform/totp"
)

// Sign-in limits.
const (
	loginLimit  = 10
	loginWindow = 15 * time.Minute
	// SessionTTL is how long a sign-in lasts without re-authenticating.
	SessionTTL = 12 * time.Hour
	// capCacheTTL bounds how long a stale capability set can survive if an
	// explicit invalidation is ever missed.
	capCacheTTL = 60 * time.Second
)

// Service handles authentication.
type Service struct {
	store    *store.Store
	rdb      *redisx.Client
	env      *crypto.Envelope
	notifier notify.Notifier
	log      *slog.Logger
	baseURL  string
	linkHost string
}

// Deps are the Service's collaborators.
type Deps struct {
	Store    *store.Store
	Redis    *redisx.Client
	Envelope *crypto.Envelope
	Notifier notify.Notifier
	Log      *slog.Logger
	BaseURL  string
	// LinkHost is the hostname setup registers as the first link domain.
	LinkHost string
}

// NewService returns a Service.
func NewService(d Deps) *Service {
	return &Service{
		store: d.Store, rdb: d.Redis, env: d.Envelope,
		notifier: d.Notifier, log: d.Log, baseURL: d.BaseURL,
		linkHost: d.LinkHost,
	}
}

// LoginResult is the outcome of a sign-in attempt.
type LoginResult struct {
	Token       string
	ExpiresAt   time.Time
	TenantID    uuid.UUID
	Memberships []store.Membership
	// MFASetupRequired is true when the role demands a second factor that the
	// account has not enrolled yet.
	MFASetupRequired bool
}

// Login verifies credentials and opens a session.
//
// Every failure -- unknown address, wrong password, suspended account -- returns
// the same error and takes roughly the same time. Anything else lets the form be
// used to discover which addresses have accounts.
func (s *Service) Login(ctx context.Context, email, pw, mfaCode, ipPrefix, userAgent string) (LoginResult, error) {
	email = strings.ToLower(strings.TrimSpace(email))

	allowed, err := s.store.RegisterLoginAttempt(ctx, email, loginLimit, loginWindow)
	if err != nil {
		return LoginResult{}, err
	}
	if !allowed {
		return LoginResult{}, domain.ErrRateLimited
	}

	user, found, err := s.store.FindUserByEmail(ctx, email)
	if err != nil {
		return LoginResult{}, err
	}
	if !found || user.Status != "active" || user.PasswordHash == "" {
		// Burn the same work as a real verification so the response time does not
		// betray whether the account exists.
		password.VerifyDummy(pw)
		return LoginResult{}, domain.ErrInvalidCredentials
	}

	needsRehash, err := password.Verify(pw, user.PasswordHash)
	if err != nil {
		return LoginResult{}, domain.ErrInvalidCredentials
	}

	memberships, err := s.store.Memberships(ctx, user.ID)
	if err != nil {
		return LoginResult{}, err
	}
	if len(memberships) == 0 {
		return LoginResult{}, domain.ErrInvalidCredentials
	}

	// MFA is decided by the strongest role held anywhere, not by the organisation
	// being signed into: an admin of one organisation must not be able to skip
	// the second factor by signing into another.
	mfaRequired := false
	for _, m := range memberships {
		if domain.RequiresMFA(m.OrgRole) {
			mfaRequired = true
			break
		}
	}

	switch {
	case user.MFAEnabled:
		if mfaCode == "" {
			return LoginResult{}, domain.ErrMFARequired
		}
		// The same check the reset flow uses, which also accepts a recovery code.
		//
		// Login used to call totp.Verify directly, so a recovery code worked only
		// while resetting a password -- while the sign-in form offered one. A lost
		// phone therefore meant a password reset to get back in, and the codes
		// handed out at enrolment for exactly this did nothing here.
		if err := s.verifySecondFactor(ctx, user.ID, user.MFASecret, mfaCode); err != nil {
			if errors.Is(err, domain.ErrMFAInvalid) {
				return LoginResult{}, domain.ErrMFAInvalid
			}
			return LoginResult{}, err
		}
	case mfaRequired:
		// Signed in, but the session really is only good for enrolling now.
		//
		// The comment here used to say that and nothing enforced it: Resolve
		// handed back the full capability set, and mfa_setup_required was a JSON
		// field the client was trusted to act on. A stolen password was therefore
		// enough to read sensitive fields, export in bulk and handle data-subject
		// requests, on precisely the roles that reach personal data across the
		// whole organisation.
		// Enforcement lives in Resolve, which re-checks it on every request:
		// see the capability strip there. Refusing the sign-in outright would
		// make enrolling impossible for exactly the people who must.
		s.log.Warn("privileged account without mfa", "user_id", user.ID)
	}

	raw, err := newToken()
	if err != nil {
		return LoginResult{}, err
	}
	sess, err := s.store.CreateSession(ctx, user.ID, memberships[0].TenantID, raw, ipPrefix, userAgent, SessionTTL)
	if err != nil {
		return LoginResult{}, err
	}

	if err := s.store.ClearLoginAttempts(ctx, email); err != nil {
		s.log.Warn("clearing login attempts", "error", err)
	}
	if err := s.store.TouchLogin(ctx, user.ID); err != nil {
		s.log.Warn("recording login", "error", err)
	}
	if needsRehash {
		// Silently upgrade the stored hash now that the plaintext is in hand.
		if upgraded, err := password.Hash(pw, password.DefaultParams); err == nil {
			if err := s.store.UpdatePasswordHash(ctx, user.ID, upgraded); err != nil {
				s.log.Warn("upgrading password hash", "error", err)
			}
		}
	}

	return LoginResult{
		Token: raw, ExpiresAt: sess.ExpiresAt, TenantID: sess.TenantID,
		Memberships: memberships, MFASetupRequired: mfaRequired && !user.MFAEnabled,
	}, nil
}

// Resolve turns a session token into an actor.
func (s *Service) Resolve(ctx context.Context, raw string) (authn.Actor, uuid.UUID, error) {
	sess, user, err := s.store.ResolveSession(ctx, raw)
	if err != nil {
		return authn.Actor{}, uuid.Nil, err
	}

	caps, err := s.capabilities(ctx, user.ID, sess.TenantID)
	if err != nil {
		return authn.Actor{}, uuid.Nil, err
	}

	// The project scope, which the actor previously never received.
	//
	// Organisation roles that grant anything reach every project; a plain member
	// reaches exactly what was granted. Read from the membership rather than
	// inferred from the capability set, because two roles can hold the same
	// capabilities and differ in reach.
	m, err := s.store.Membership(ctx, user.ID, sess.TenantID)
	if err != nil {
		return authn.Actor{}, uuid.Nil, err
	}
	orgWide := len(domain.OrgCapabilities(m.OrgRole)) > 0

	// A role that must use a second factor holds nothing until it has one.
	//
	// Checked here rather than at sign-in because it is a property of the account
	// right now, not of how the session began: promoting somebody to admin
	// restricts their existing sessions immediately, and enrolling lifts the
	// restriction without signing out. The enrolment endpoints need no
	// capability, so an empty set still leaves the way forward open -- and only
	// that way.
	if domain.RequiresMFA(m.OrgRole) && !user.MFAEnabled {
		caps = nil
	}

	if err := s.store.TouchSession(ctx, sess.ID); err != nil {
		s.log.Warn("touching session", "error", err)
	}
	actor := authn.NewActor(authn.KindUser, user.ID, sess.TenantID, nil, caps).
		WithProjectScope(orgWide, m.ProjectIDs())
	return actor, sess.ID, nil
}

// capabilities resolves and caches a membership's permission set.
//
// The cache is short and is dropped explicitly whenever a membership changes, so
// a revoked permission stops working immediately rather than at the end of a TTL.
// The TTL is only the backstop for an invalidation somebody forgot to wire up.
func (s *Service) capabilities(ctx context.Context, userID, tenantID uuid.UUID) ([]string, error) {
	key := capKey(userID, tenantID)

	if cached, err := s.rdb.Get(ctx, key).Result(); err == nil {
		var caps []string
		if err := json.Unmarshal([]byte(cached), &caps); err == nil {
			return caps, nil
		}
	}

	m, err := s.store.Membership(ctx, userID, tenantID)
	if err != nil {
		return nil, err
	}
	caps := domain.Capabilities(m.OrgRole, m.RoleList())

	if payload, err := json.Marshal(caps); err == nil {
		if err := s.rdb.Set(ctx, key, payload, capCacheTTL).Err(); err != nil {
			// A cache that is down costs a query, never a wrong answer.
			s.log.Warn("caching capabilities", "error", err)
		}
	}
	return caps, nil
}

// InvalidateCapabilities drops a user's cached permissions in one organisation.
// Call it on every membership or role change.
func (s *Service) InvalidateCapabilities(ctx context.Context, userID, tenantID uuid.UUID) {
	if err := s.rdb.Del(ctx, capKey(userID, tenantID)).Err(); err != nil {
		s.log.Warn("invalidating capability cache", "error", err, "user_id", userID)
	}
}

// Logout ends one session.
func (s *Service) Logout(ctx context.Context, sessionID uuid.UUID) error {
	return s.store.RevokeSession(ctx, sessionID, "signed out")
}

// RevokeAll ends every session a user holds, for example after a password change
// or when their access is withdrawn.
func (s *Service) RevokeAll(ctx context.Context, userID uuid.UUID, reason string) (int, error) {
	return s.store.RevokeUserSessions(ctx, userID, reason)
}

// BootstrapInput describes the first account.
type BootstrapInput struct {
	OrgName  string
	Email    string
	Name     string
	Password string
}

// Bootstrap creates the first organisation and owner.
//
// It works exactly once. After that the endpoint reports that setup is already
// done, so a public deployment cannot have a second owner appended to it.
func (s *Service) Bootstrap(ctx context.Context, in BootstrapInput) (uuid.UUID, error) {
	exists, err := s.store.HasAnyUser(ctx)
	if err != nil {
		return uuid.Nil, err
	}
	if exists {
		return uuid.Nil, domain.ErrAlreadyMember
	}
	if err := password.Validate(in.Password); err != nil {
		return uuid.Nil, err
	}

	hash, err := password.Hash(in.Password, password.DefaultParams)
	if err != nil {
		return uuid.Nil, err
	}

	slug := slugify(in.OrgName)
	_, userID, err := s.store.Bootstrap(ctx, in.OrgName, slug,
		strings.ToLower(strings.TrimSpace(in.Email)), in.Name, hash, s.linkHost)
	if err != nil {
		return uuid.Nil, err
	}
	return userID, nil
}

// SetupComplete reports whether the first account has been created.
func (s *Service) SetupComplete(ctx context.Context) (bool, error) {
	return s.store.HasAnyUser(ctx)
}

// MFAEnrolment is what a user needs to add an authenticator app.
type MFAEnrolment struct {
	Secret string `json:"secret"`
	URI    string `json:"uri"`
}

// BeginMFA generates a TOTP secret and stores it, still disabled.
//
// It stays disabled until a code proves the app was set up correctly. Enabling on
// generation would lock people out whenever a QR scan silently failed.
func (s *Service) BeginMFA(ctx context.Context, userID uuid.UUID) (MFAEnrolment, error) {
	user, err := s.store.FindUserByID(ctx, userID)
	if err != nil {
		return MFAEnrolment{}, err
	}
	secret, err := totp.GenerateSecret()
	if err != nil {
		return MFAEnrolment{}, err
	}
	wrapped, err := s.env.SealBytes([]byte(secret))
	if err != nil {
		return MFAEnrolment{}, err
	}
	if err := s.store.SetMFA(ctx, userID, wrapped, false); err != nil {
		return MFAEnrolment{}, err
	}
	return MFAEnrolment{Secret: secret, URI: totp.ProvisioningURI(secret, "Collectr", user.Email)}, nil
}

// ConfirmMFA enables the second factor once a valid code proves enrolment, and
// returns the recovery codes.
//
// They are issued here rather than offered later, because "later" is after the
// phone is already lost.
func (s *Service) ConfirmMFA(ctx context.Context, userID uuid.UUID, code string) ([]string, error) {
	user, err := s.store.FindUserByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	if len(user.MFASecret) == 0 {
		return nil, domain.ErrMFAInvalid
	}

	secret, err := s.env.OpenBytes(user.MFASecret)
	if err != nil {
		return nil, fmt.Errorf("unwrapping mfa secret: %w", err)
	}
	if err := totp.Verify(string(secret), code, time.Now()); err != nil {
		return nil, domain.ErrMFAInvalid
	}
	if err := s.store.SetMFA(ctx, userID, user.MFASecret, true); err != nil {
		return nil, err
	}

	codes, err := s.GenerateRecoveryCodes(ctx, userID)
	if err != nil {
		return nil, err
	}

	// Enabling a second factor invalidates every other session: if one of them
	// belonged to whoever prompted the enrolment, it must not survive it.
	if n, err := s.RevokeAll(ctx, userID, "mfa enabled"); err != nil {
		s.log.Warn("revoking sessions after mfa enrolment", "error", err)
	} else if n > 0 {
		s.log.Info("sessions revoked after enabling mfa", "user_id", userID, "count", n)
	}
	return codes, nil
}

func capKey(userID, tenantID uuid.UUID) string {
	return "cap:" + tenantID.String() + ":" + userID.String()
}

func newToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, buf); err != nil {
		return "", fmt.Errorf("generating session token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// slugify derives an identifier from a display name, falling back to a random
// one when the name has no characters a slug can use.
func slugify(name string) string {
	s := slug.Make(name)
	if s == "" {
		s = "org-" + uuid.NewString()[:8]
	}
	return s
}
