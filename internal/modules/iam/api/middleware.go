package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/collectr/collectr/internal/modules/iam/domain"
	"github.com/collectr/collectr/internal/platform/authn"
	"github.com/collectr/collectr/internal/platform/httpx"
)

type ctxKey int

const ctxKeySession ctxKey = iota

// SessionFrom returns the session id serving this request, if the caller signed
// in rather than presenting an API key.
func SessionFrom(ctx context.Context) (uuid.UUID, bool) {
	id, ok := ctx.Value(ctxKeySession).(uuid.UUID)
	return id, ok
}

// Authenticate accepts either a session cookie or an API key.
//
// A browser sends the cookie; a script sends the key. They resolve to the same
// Actor type, so no handler downstream has to care which arrived -- and the
// capability rules apply identically, including the ones an API key may never
// hold.
func (h *Handler) Authenticate(keys *authn.Authenticator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
				actor, sessionID, err := h.svc.Resolve(r.Context(), c.Value)
				if err == nil {
					ctx := authn.WithActor(r.Context(), actor)
					ctx = context.WithValue(ctx, ctxKeySession, sessionID)
					next.ServeHTTP(w, r.WithContext(ctx))
					return
				}
				if !errors.Is(err, domain.ErrSessionInvalid) && !errors.Is(err, domain.ErrNotFound) {
					httpx.Logger(r.Context()).Error("resolving session", "error", err)
				}
				// A revoked or expired cookie is cleared rather than left to keep
				// failing on every subsequent request.
				http.SetCookie(w, &http.Cookie{
					Name: sessionCookie, Value: "", Path: "/",
					HttpOnly: true, Secure: h.secure, SameSite: http.SameSiteLaxMode, MaxAge: -1,
				})
				httpx.Error(w, r, http.StatusUnauthorized, "unauthenticated", "Your session has expired")
				return
			}

			if token, ok := bearer(r); ok {
				actor, err := keys.FromAPIKey(r.Context(), token)
				if err != nil {
					if !errors.Is(err, authn.ErrUnauthenticated) {
						httpx.Logger(r.Context()).Error("authenticating api key", "error", err)
					}
					httpx.Error(w, r, http.StatusUnauthorized, "unauthenticated", "Invalid credentials")
					return
				}
				next.ServeHTTP(w, r.WithContext(authn.WithActor(r.Context(), actor)))
				return
			}

			httpx.Error(w, r, http.StatusUnauthorized, "unauthenticated", "Authentication required")
		})
	}
}

// RequireSameOrigin rejects cross-site state changes.
//
// Session cookies are SameSite=Lax, which already blocks cross-site POSTs from
// forms, but Lax has enough exceptions across browser versions to be worth a
// second, explicit check. API-key callers are exempt: they send no cookie, so
// there is nothing for another site to ride on.
func RequireSameOrigin(allowedOrigin string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodGet, http.MethodHead, http.MethodOptions:
				next.ServeHTTP(w, r)
				return
			}
			if _, usingCookie := r.Cookie(sessionCookie); usingCookie != nil {
				next.ServeHTTP(w, r)
				return
			}

			origin := r.Header.Get("Origin")
			if origin != "" && origin != allowedOrigin {
				httpx.Error(w, r, http.StatusForbidden, "cross_origin",
					"Cross-origin requests are not accepted for this endpoint")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// contextWithTimeout detaches background work from the request, so that how long
// the work takes cannot be measured from the response.
func contextWithTimeout(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}

func bearer(r *http.Request) (string, bool) {
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	if len(h) <= len(prefix) || h[:len(prefix)] != prefix {
		return "", false
	}
	return h[len(prefix):], true
}
