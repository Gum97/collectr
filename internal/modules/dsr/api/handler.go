// Package api exposes the data subject portal.
//
// Every endpoint here answers to someone who has proved control of an email
// address or phone number, and to nobody else. Two rules shape all of it: never
// reveal whether an identifier is known, and never trust an id in a request.
package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/collectr/collectr/internal/modules/dsr/app"
	"github.com/collectr/collectr/internal/modules/dsr/domain"
	"github.com/collectr/collectr/internal/platform/httpx"
	"github.com/collectr/collectr/internal/platform/signing"
)

// sessionCookie carries the verified portal session.
const sessionCookie = "collectr_dsr"

// Handler serves the portal.
type Handler struct {
	svc      *app.Service
	sessions *signing.SubjectSigner
	secure   bool
}

// New returns a Handler. secure marks the session cookie for HTTPS-only use.
func New(svc *app.Service, sessions *signing.SubjectSigner, secure bool) *Handler {
	return &Handler{svc: svc, sessions: sessions, secure: secure}
}

// Register mounts the portal routes.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/dsr/identify", h.identify)
	mux.HandleFunc("POST /api/dsr/session", h.exchange)
	mux.HandleFunc("GET /api/dsr/me/submissions", h.submissions)
	mux.HandleFunc("PATCH /api/dsr/me/submissions/{id}", h.rectify)
	mux.HandleFunc("GET /api/dsr/me/requests", h.requests)
	mux.HandleFunc("POST /api/dsr/me/requests", h.raise)
}

type identifyRequest struct {
	Tenant     string `json:"tenant"`
	Kind       string `json:"kind"`
	Identifier string `json:"identifier"`
}

// identify sends a magic link, and says nothing about whether it found anyone.
//
// The response is 202 in every case: unknown identifier, rate limited, or link
// sent. Anything else turns this into a way to test whether a company holds data
// on a given person, which is a disclosure in itself.
func (h *Handler) identify(w http.ResponseWriter, r *http.Request) {
	var req identifyRequest
	if err := httpx.DecodeJSON(w, r, &req, 8<<10); err != nil {
		// Even a malformed body gets the same answer.
		httpx.JSON(w, r, http.StatusAccepted, acceptedBody())
		return
	}

	tenantID, err := uuid.Parse(req.Tenant)
	kind := strings.ToLower(strings.TrimSpace(req.Kind))
	if err != nil || (kind != "email" && kind != "phone") || strings.TrimSpace(req.Identifier) == "" {
		httpx.JSON(w, r, http.StatusAccepted, acceptedBody())
		return
	}

	// Detached from the request context on purpose: the work must finish even
	// though the response has already gone, and its duration must not be
	// observable from the outside.
	go func() {
		ctx, cancel := contextWithTimeout(15 * time.Second)
		defer cancel()
		h.svc.Identify(ctx, tenantID, kind, req.Identifier)
	}()

	httpx.JSON(w, r, http.StatusAccepted, acceptedBody())
}

func acceptedBody() map[string]string {
	return map[string]string{
		"status": "accepted",
		"message": "Nếu thông tin này có trong hệ thống, chúng tôi đã gửi một liên kết truy cập. " +
			"Liên kết có hiệu lực trong 15 phút.",
	}
}

type exchangeRequest struct {
	Tenant string `json:"tenant"`
	Token  string `json:"token"`
}

func (h *Handler) exchange(w http.ResponseWriter, r *http.Request) {
	var req exchangeRequest
	if err := httpx.DecodeJSON(w, r, &req, 8<<10); err != nil {
		httpx.Error(w, r, http.StatusBadRequest, "invalid_body", "Request body is not valid JSON")
		return
	}
	tenantID, err := uuid.Parse(req.Tenant)
	if err != nil {
		httpx.Error(w, r, http.StatusUnauthorized, "invalid_token", "This link is not valid")
		return
	}

	sess, err := h.svc.Exchange(r.Context(), tenantID, req.Token)
	if err != nil {
		if !errors.Is(err, domain.ErrTokenInvalid) {
			httpx.Logger(r.Context()).Error("exchanging dsr token", "error", err)
		}
		// Expired, already used and never existed are one answer. Distinguishing
		// them tells an attacker which guesses were close.
		httpx.Error(w, r, http.StatusUnauthorized, "invalid_token",
			"This link is no longer valid. Please request a new one.")
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    h.sessions.Mint(sess.TenantID, sess.SubjectID, sess.Scope, sess.SubmissionID, time.Now()),
		Path:     "/api/dsr",
		HttpOnly: true,
		Secure:   h.secure,
		// Lax, not None: the portal is never embedded in another site, and this
		// blocks a cross-site page from acting on the session.
		SameSite: http.SameSiteLaxMode,
		Expires:  sess.ExpiresAt,
	})

	httpx.JSON(w, r, http.StatusOK, map[string]any{
		"scope":      sess.Scope,
		"expires_at": sess.ExpiresAt,
	})
}

// session recovers the verified session, or writes a 401.
func (h *Handler) session(w http.ResponseWriter, r *http.Request) (app.Session, bool) {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		httpx.Error(w, r, http.StatusUnauthorized, "unauthenticated", "Please open your access link again")
		return app.Session{}, false
	}
	s, err := h.sessions.Verify(c.Value, time.Now())
	if err != nil {
		httpx.Error(w, r, http.StatusUnauthorized, "unauthenticated", "Your session has expired")
		return app.Session{}, false
	}
	return app.Session{
		TenantID: s.TenantID, SubjectID: s.SubjectID,
		Scope: s.Scope, SubmissionID: s.SubmissionID, ExpiresAt: s.Expires,
	}, true
}

func (h *Handler) submissions(w http.ResponseWriter, r *http.Request) {
	sess, ok := h.session(w, r)
	if !ok {
		return
	}

	subs, err := h.svc.MySubmissions(r.Context(), sess)
	if err != nil {
		httpx.Logger(r.Context()).Error("listing subject submissions", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "Internal server error")
		return
	}

	out := make([]map[string]any, 0, len(subs))
	for _, s := range subs {
		out = append(out, map[string]any{
			"id":           s.ID,
			"form":         s.FormTitle,
			"form_version": s.VersionNo,
			"answers":      s.Answers,
			"submitted_at": s.SubmittedAt,
			"status":       s.Status,
		})
	}
	httpx.JSON(w, r, http.StatusOK, map[string]any{"data": out})
}

func (h *Handler) rectify(w http.ResponseWriter, r *http.Request) {
	sess, ok := h.session(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.Error(w, r, http.StatusNotFound, "not_found", "Record not found")
		return
	}

	var body struct {
		Answers map[string]any `json:"answers"`
	}
	if err := httpx.DecodeJSON(w, r, &body, 256<<10); err != nil {
		httpx.Error(w, r, http.StatusBadRequest, "invalid_body", "Request body is not valid JSON")
		return
	}

	err = h.svc.Rectify(r.Context(), sess, id, body.Answers)
	switch {
	case errors.Is(err, domain.ErrForbidden):
		// 404, not 403: confirming the record exists but belongs to someone else
		// is itself a disclosure.
		httpx.Error(w, r, http.StatusNotFound, "not_found", "Record not found")
		return
	case err != nil:
		httpx.Logger(r.Context()).Error("rectifying submission", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "Internal server error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) requests(w http.ResponseWriter, r *http.Request) {
	sess, ok := h.session(w, r)
	if !ok {
		return
	}
	reqs, err := h.svc.MyRequests(r.Context(), sess)
	if err != nil {
		httpx.Logger(r.Context()).Error("listing dsr requests", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "Internal server error")
		return
	}
	httpx.JSON(w, r, http.StatusOK, map[string]any{"data": reqs})
}

func (h *Handler) raise(w http.ResponseWriter, r *http.Request) {
	sess, ok := h.session(w, r)
	if !ok {
		return
	}

	var body struct {
		Type string `json:"type"`
	}
	if err := httpx.DecodeJSON(w, r, &body, 4<<10); err != nil {
		httpx.Error(w, r, http.StatusBadRequest, "invalid_body", "Request body is not valid JSON")
		return
	}

	req, err := h.svc.Raise(r.Context(), sess, body.Type)
	switch {
	case errors.Is(err, domain.ErrForbidden):
		httpx.Error(w, r, http.StatusForbidden, "forbidden",
			"This link only covers a single record. Request full access to do that.")
		return
	case err != nil && strings.Contains(err.Error(), "unknown request type"):
		httpx.ErrorWithFields(w, r, http.StatusUnprocessableEntity, "validation_failed",
			"Invalid request", map[string]any{"type": "is not a recognised right"})
		return
	case err != nil:
		httpx.Logger(r.Context()).Error("raising dsr request", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "Internal server error")
		return
	}

	// The deadline is returned so the person can hold the controller to it.
	httpx.JSON(w, r, http.StatusAccepted, map[string]any{
		"request_id": req.ID,
		"type":       req.Type,
		"due_at":     req.DueAt,
	})
}

// contextWithTimeout detaches background work from the request, so that how long
// the work takes cannot be measured from the response.
func contextWithTimeout(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}
