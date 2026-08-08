// Package api exposes sign-in and account management.
package api

import (
	"encoding/base64"
	"errors"
	"net/http"
	"time"

	"github.com/collectr/collectr/internal/contracts"
	"github.com/collectr/collectr/internal/modules/iam/app"
	"github.com/collectr/collectr/internal/modules/iam/domain"
	qrcode "github.com/skip2/go-qrcode"

	"github.com/collectr/collectr/internal/platform/authn"
	"github.com/collectr/collectr/internal/platform/httpx"
	"github.com/collectr/collectr/internal/platform/password"
	"github.com/collectr/collectr/internal/platform/postgres"
)

// sessionCookie carries the signed-in session.
const sessionCookie = "collectr_session"

// Handler serves the authentication routes.
type Handler struct {
	mfaGrace time.Duration
	svc      *app.Service
	db       *postgres.DB
	audit    contracts.AuditWriter
	secure   bool
}

// New returns a Handler.
func New(svc *app.Service, db *postgres.DB, audit contracts.AuditWriter, secure bool, mfaGrace time.Duration) *Handler {
	return &Handler{svc: svc, db: db, audit: audit, secure: secure, mfaGrace: mfaGrace}
}

// RegisterPublic mounts the unauthenticated routes.
func (h *Handler) RegisterPublic(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/auth/setup", h.setup)
	mux.HandleFunc("GET /api/auth/setup", h.setupStatus)
	mux.HandleFunc("POST /api/auth/login", h.login)
}

// RegisterAdmin mounts the routes that require a session.
func (h *Handler) RegisterAdmin(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/auth/logout", h.logout)
	mux.HandleFunc("GET /api/v1/auth/me", h.me)
	mux.HandleFunc("POST /api/v1/auth/mfa/begin", h.beginMFA)
	mux.HandleFunc("POST /api/v1/auth/mfa/confirm", h.confirmMFA)
	mux.HandleFunc("DELETE /api/v1/auth/mfa", h.disableMFA)
}

type setupRequest struct {
	OrgName  string `json:"org_name"`
	Email    string `json:"email"`
	Name     string `json:"name"`
	Password string `json:"password"`
}

// setupStatus reports whether the deployment still needs its first account.
func (h *Handler) setupStatus(w http.ResponseWriter, r *http.Request) {
	done, err := h.svc.SetupComplete(r.Context())
	if err != nil {
		httpx.Logger(r.Context()).Error("checking setup status", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "Internal server error")
		return
	}
	httpx.JSON(w, r, http.StatusOK, map[string]bool{"setup_complete": done})
}

// setup creates the first organisation and owner.
//
// It is reachable exactly once. A fresh deployment has no way in at all, and
// leaving the endpoint open afterwards would let anyone who reached the host
// append themselves as a second owner.
func (h *Handler) setup(w http.ResponseWriter, r *http.Request) {
	var req setupRequest
	if err := httpx.DecodeJSON(w, r, &req, 8<<10); err != nil {
		httpx.Error(w, r, http.StatusBadRequest, "invalid_body", "Request body is not valid JSON")
		return
	}
	if req.OrgName == "" || req.Email == "" {
		httpx.ErrorWithFields(w, r, http.StatusUnprocessableEntity, "validation_failed",
			"Invalid request", map[string]any{"org_name": "organisation name and email are required"})
		return
	}

	userID, err := h.svc.Bootstrap(r.Context(), app.BootstrapInput{
		OrgName: req.OrgName, Email: req.Email, Name: req.Name, Password: req.Password,
	})
	switch {
	case errors.Is(err, domain.ErrAlreadyMember):
		httpx.Error(w, r, http.StatusConflict, "already_setup",
			"This deployment has already been set up")
		return
	case errors.Is(err, password.ErrTooShort), errors.Is(err, password.ErrTooLong):
		httpx.ErrorWithFields(w, r, http.StatusUnprocessableEntity, "validation_failed",
			"Invalid request", map[string]any{"password": err.Error()})
		return
	case err != nil:
		httpx.Logger(r.Context()).Error("bootstrapping deployment", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "Internal server error")
		return
	}

	httpx.JSON(w, r, http.StatusCreated, map[string]any{
		"user_id": userID,
		"message": "Đã tạo tổ chức và tài khoản chủ sở hữu. Hãy đăng nhập.",
	})
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	MFACode  string `json:"mfa_code"`
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := httpx.DecodeJSON(w, r, &req, 8<<10); err != nil {
		httpx.Error(w, r, http.StatusBadRequest, "invalid_body", "Request body is not valid JSON")
		return
	}

	res, err := h.svc.Login(r.Context(), req.Email, req.Password, req.MFACode,
		httpx.IPPrefix(r), r.UserAgent())
	switch {
	case errors.Is(err, domain.ErrMFARequired):
		// A distinct answer, and safe: the caller already proved the password, so
		// this reveals nothing they did not know.
		//
		// Sent through the same problem+json envelope as every other error. It
		// used to be a bare {error, message} object, which meant the client read
		// its code as "unknown", treated the response as a failed login, and told
		// the person their password was wrong -- while their password was right
		// and the only thing missing was the second factor.
		httpx.Error(w, r, http.StatusUnauthorized, "mfa_required",
			"Nhập mã xác thực từ ứng dụng của bạn.")
		return
	case errors.Is(err, domain.ErrMFAInvalid):
		httpx.Error(w, r, http.StatusUnauthorized, "mfa_invalid", "Mã xác thực không đúng")
		return
	case errors.Is(err, domain.ErrRateLimited):
		httpx.Error(w, r, http.StatusTooManyRequests, "rate_limited",
			"Quá nhiều lần thử. Vui lòng đợi ít phút.")
		return
	case errors.Is(err, domain.ErrInvalidCredentials):
		// One answer for unknown address, wrong password and disabled account.
		httpx.Error(w, r, http.StatusUnauthorized, "invalid_credentials",
			"Email hoặc mật khẩu không đúng")
		return
	case err != nil:
		httpx.Logger(r.Context()).Error("signing in", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "Internal server error")
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    res.Token,
		Path:     "/",
		HttpOnly: true,
		Secure:   h.secure,
		SameSite: http.SameSiteLaxMode,
		Expires:  res.ExpiresAt,
	})

	orgs := make([]map[string]any, 0, len(res.Memberships))
	for _, m := range res.Memberships {
		orgs = append(orgs, map[string]any{"id": m.TenantID, "name": m.TenantName, "role": m.OrgRole})
	}
	httpx.JSON(w, r, http.StatusOK, map[string]any{
		"tenant_id":          res.TenantID,
		"organisations":      orgs,
		"expires_at":         res.ExpiresAt,
		"mfa_setup_required": res.MFASetupRequired,
	})
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	if sessionID, ok := SessionFrom(r.Context()); ok {
		if err := h.svc.Logout(r.Context(), sessionID); err != nil {
			httpx.Logger(r.Context()).Error("signing out", "error", err)
		}
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/",
		HttpOnly: true, Secure: h.secure, SameSite: http.SameSiteLaxMode,
		MaxAge: -1,
	})
	w.WriteHeader(http.StatusNoContent)
}

// me returns the caller's identity and effective permissions.
//
// Returning the capability list lets a UI hide what the caller cannot do. It is
// a convenience, never the enforcement: every handler checks for itself.
func (h *Handler) me(w http.ResponseWriter, r *http.Request) {
	actor, ok := authn.ActorFrom(r.Context())
	if !ok {
		httpx.Error(w, r, http.StatusUnauthorized, "unauthenticated", "Authentication required")
		return
	}
	out := map[string]any{
		"user_id":      actor.UserID,
		"tenant_id":    actor.TenantID,
		"kind":         actor.Kind,
		"capabilities": actor.Capabilities(),
	}

	// An API key has no person behind it, so there is no profile to read and no
	// name to show. Failing the whole call over that would break every key-driven
	// integration that checks its own identity.
	if actor.Kind == authn.KindUser {
		profile, err := h.svc.Profile(r.Context(), actor.TenantID, actor.UserID)
		if err != nil {
			httpx.Logger(r.Context()).Error("reading profile", "error", err)
			httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "Internal server error")
			return
		}
		out["email"] = profile.Email
		out["name"] = profile.Name
		out["org_role"] = profile.OrgRole
		out["org_name"] = profile.OrgName
		out["mfa_enabled"] = profile.MFAEnabled
		// Whether this role must have one, so the interface can tell "you hold no
		// permissions" apart from "your permissions are suspended until you
		// enrol". Without the distinction every screen just says access denied,
		// which is true and useless.
		out["mfa_required"] = domain.RequiresMFA(profile.OrgRole)
		// When the window closes. The interface counts down rather than locking
		// the door on day one; after this moment the resolver strips the
		// capabilities and the countdown becomes a gate.
		if domain.RequiresMFA(profile.OrgRole) && !profile.MFAEnabled {
			out["mfa_grace_ends_at"] = profile.CreatedAt.Add(h.mfaGrace)
		}
		out["recovery_codes_left"] = profile.RecoveryLeft
	}

	httpx.JSON(w, r, http.StatusOK, out)
}

func (h *Handler) beginMFA(w http.ResponseWriter, r *http.Request) {
	actor, ok := authn.ActorFrom(r.Context())
	if !ok || actor.Kind != authn.KindUser {
		httpx.Error(w, r, http.StatusUnauthorized, "unauthenticated", "Authentication required")
		return
	}

	enrol, err := h.svc.BeginMFA(r.Context(), actor.UserID)
	if err != nil {
		httpx.Logger(r.Context()).Error("beginning mfa enrolment", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "Internal server error")
		return
	}
	// The QR is rendered here rather than left to the browser.
	//
	// Without it the enrolment page can only offer the base32 secret to be typed
	// by hand, and a second factor that is inconvenient to turn on is one people
	// do not turn on. The image carries the same secret already in this response,
	// so it discloses nothing new -- but it must not outlive the response, hence
	// no-store below.
	out := map[string]any{"secret": enrol.Secret, "uri": enrol.URI}
	if png, err := qrcode.Encode(enrol.URI, qrcode.Medium, 320); err == nil {
		out["qr_data_uri"] = "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
	} else {
		// The page falls back to the secret and the otpauth link, so a failure
		// here is a worse experience rather than a broken one.
		httpx.Logger(r.Context()).Warn("rendering mfa qr", "error", err)
	}

	// The secret is shown once, at enrolment. It is not retrievable afterwards,
	// and no cache along the way may keep a copy.
	w.Header().Set("Cache-Control", "no-store")
	httpx.JSON(w, r, http.StatusOK, out)
}

func (h *Handler) confirmMFA(w http.ResponseWriter, r *http.Request) {
	actor, ok := authn.ActorFrom(r.Context())
	if !ok || actor.Kind != authn.KindUser {
		httpx.Error(w, r, http.StatusUnauthorized, "unauthenticated", "Authentication required")
		return
	}

	var body struct {
		Code string `json:"code"`
	}
	if err := httpx.DecodeJSON(w, r, &body, 4<<10); err != nil {
		httpx.Error(w, r, http.StatusBadRequest, "invalid_body", "Request body is not valid JSON")
		return
	}

	codes, err := h.svc.ConfirmMFA(r.Context(), actor.UserID, body.Code)
	switch {
	case errors.Is(err, domain.ErrMFAInvalid):
		httpx.Error(w, r, http.StatusUnprocessableEntity, "mfa_invalid",
			"Mã không đúng. Kiểm tra lại đồng hồ trên điện thoại.")
		return
	case err != nil:
		httpx.Logger(r.Context()).Error("confirming mfa", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "Internal server error")
		return
	}

	// Every other session was revoked, so the caller has to sign in again.
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/",
		HttpOnly: true, Secure: h.secure, SameSite: http.SameSiteLaxMode, MaxAge: -1,
	})
	// The recovery codes are handed over here, at enrolment -- not offered later,
	// because "later" is after the phone is already lost.
	httpx.JSON(w, r, http.StatusOK, map[string]any{
		"mfa_enabled":    true,
		"recovery_codes": codes,
		"message": "Đã bật xác thực hai lớp. Lưu các mã dự phòng dưới đây ở nơi an toàn — " +
			"chúng chỉ hiển thị một lần. Vui lòng đăng nhập lại.",
	})
}

// disableMFA turns off a user's own second factor.
//
// Own account only: there is no path here to disable it for somebody else. An
// administrator who could would be able to strip the protection off the account
// they most want to reach.
func (h *Handler) disableMFA(w http.ResponseWriter, r *http.Request) {
	actor, ok := authn.ActorFrom(r.Context())
	if !ok || actor.Kind != authn.KindUser {
		httpx.Error(w, r, http.StatusUnauthorized, "unauthenticated", "Authentication required")
		return
	}

	var body struct {
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	if err := httpx.DecodeJSON(w, r, &body, 4<<10); err != nil {
		httpx.Error(w, r, http.StatusBadRequest, "invalid_body", "Request body is not valid JSON")
		return
	}

	profile, err := h.svc.Profile(r.Context(), actor.TenantID, actor.UserID)
	if err != nil {
		httpx.Logger(r.Context()).Error("reading profile", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "Internal server error")
		return
	}

	err = h.svc.DisableMFA(r.Context(), actor.UserID, profile.OrgRole, body.Password, body.Code)
	switch {
	case errors.Is(err, domain.ErrInvalidInput):
		// Named, not hidden: the person is being told their role is the reason,
		// which is something they can act on by changing the role first.
		httpx.ErrorWithFields(w, r, http.StatusConflict, "mfa_required_for_role",
			"Không thể tắt xác thực hai lớp", map[string]any{"org_role": err.Error()})
		return
	case errors.Is(err, domain.ErrInvalidCredentials):
		httpx.ErrorWithFields(w, r, http.StatusUnauthorized, "invalid_credentials",
			"Mật khẩu không đúng", map[string]any{"password": "mật khẩu hiện tại không đúng"})
		return
	case errors.Is(err, domain.ErrMFAInvalid):
		httpx.ErrorWithFields(w, r, http.StatusUnauthorized, "mfa_invalid",
			"Mã xác thực không đúng", map[string]any{"code": "mã hoặc mã khôi phục không đúng"})
		return
	case err != nil:
		httpx.Logger(r.Context()).Error("disabling mfa", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "Internal server error")
		return
	}

	h.writeAudit(r, actor, "auth.mfa_disabled", map[string]any{"user_id": actor.UserID}, nil)
	httpx.JSON(w, r, http.StatusOK, map[string]any{
		"mfa_enabled": false,
		"message": "Đã tắt xác thực hai lớp. Mọi phiên đăng nhập đã bị thu hồi — " +
			"bạn cần đăng nhập lại.",
	})
}
