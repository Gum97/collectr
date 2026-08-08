package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/collectr/collectr/internal/modules/iam/domain"
	"github.com/collectr/collectr/internal/platform/authn"
	"github.com/collectr/collectr/internal/platform/httpx"
	"github.com/collectr/collectr/internal/platform/password"
)

// RegisterRecoveryPublic mounts the routes someone locked out can reach.
func (h *Handler) RegisterRecoveryPublic(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/auth/password/forgot", h.forgotPassword)
	mux.HandleFunc("GET /api/auth/password/reset/{token}", h.previewReset)
	mux.HandleFunc("POST /api/auth/password/reset", h.completeReset)
}

// RegisterRecoveryAdmin mounts the routes that need a session.
func (h *Handler) RegisterRecoveryAdmin(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/auth/password", h.changePassword)
	mux.HandleFunc("POST /api/v1/auth/mfa/recovery-codes", h.regenerateRecoveryCodes)
	mux.HandleFunc("GET /api/v1/auth/mfa/recovery-codes", h.countRecoveryCodes)
}

type forgotRequest struct {
	Email string `json:"email"`
}

// forgotPassword always answers the same way.
//
// Unknown address, rate limited, mail sent: one response. Distinguishing them
// would make this a way to test which addresses have accounts, which is the same
// disclosure the login form is careful not to make.
func (h *Handler) forgotPassword(w http.ResponseWriter, r *http.Request) {
	var req forgotRequest
	if err := httpx.DecodeJSON(w, r, &req, 4<<10); err != nil {
		httpx.JSON(w, r, http.StatusAccepted, resetAcceptedBody())
		return
	}

	// Detached from the request: the work must finish after the response, and how
	// long it takes must not be observable.
	ipPrefix := httpx.IPPrefix(r)
	go func() {
		ctx, cancel := contextWithTimeout(15 * time.Second)
		defer cancel()
		h.svc.RequestPasswordReset(ctx, req.Email, ipPrefix)
	}()

	httpx.JSON(w, r, http.StatusAccepted, resetAcceptedBody())
}

func resetAcceptedBody() map[string]string {
	return map[string]string{
		"status": "accepted",
		"message": "Nếu địa chỉ này có tài khoản, chúng tôi đã gửi liên kết đặt lại mật khẩu. " +
			"Liên kết có hiệu lực trong 1 giờ.",
	}
}

// previewReset tells the form whether it must also ask for a second factor.
func (h *Handler) previewReset(w http.ResponseWriter, r *http.Request) {
	preview, err := h.svc.PreviewReset(r.Context(), r.PathValue("token"))
	if err != nil {
		httpx.Error(w, r, http.StatusNotFound, "invalid_token",
			"Liên kết này không còn hiệu lực. Hãy yêu cầu liên kết mới.")
		return
	}
	httpx.JSON(w, r, http.StatusOK, preview)
}

type resetRequest struct {
	Token    string `json:"token"`
	Password string `json:"password"`
	MFACode  string `json:"mfa_code"`
}

func (h *Handler) completeReset(w http.ResponseWriter, r *http.Request) {
	var req resetRequest
	if err := httpx.DecodeJSON(w, r, &req, 8<<10); err != nil {
		httpx.Error(w, r, http.StatusBadRequest, "invalid_body", "Request body is not valid JSON")
		return
	}

	err := h.svc.CompletePasswordReset(r.Context(), req.Token, req.Password, req.MFACode)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		httpx.Error(w, r, http.StatusNotFound, "invalid_token",
			"Liên kết này không còn hiệu lực. Hãy yêu cầu liên kết mới.")
		return
	case errors.Is(err, domain.ErrMFARequired):
		// Safe to say: the caller already holds a valid reset link, so this tells
		// them nothing they could not already see from the preview endpoint.
		httpx.JSON(w, r, http.StatusUnauthorized, map[string]any{
			"error":   "mfa_required",
			"message": "Tài khoản có xác thực hai lớp. Nhập mã từ ứng dụng hoặc một mã dự phòng.",
		})
		return
	case errors.Is(err, domain.ErrMFAInvalid):
		httpx.Error(w, r, http.StatusUnauthorized, "mfa_invalid", "Mã xác thực không đúng")
		return
	case errors.Is(err, password.ErrTooShort), errors.Is(err, password.ErrTooLong):
		httpx.ErrorWithFields(w, r, http.StatusUnprocessableEntity, "validation_failed",
			"Invalid request", map[string]any{"password": err.Error()})
		return
	case err != nil:
		httpx.Logger(r.Context()).Error("completing password reset", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "Internal server error")
		return
	}

	// Every session was closed, including whoever else may have held one.
	httpx.JSON(w, r, http.StatusOK, map[string]any{
		"message": "Đã đặt lại mật khẩu. Mọi phiên đăng nhập đã bị thu hồi. Hãy đăng nhập lại.",
	})
}

type changePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

func (h *Handler) changePassword(w http.ResponseWriter, r *http.Request) {
	actor, ok := authn.ActorFrom(r.Context())
	if !ok || actor.Kind != authn.KindUser {
		httpx.Error(w, r, http.StatusUnauthorized, "unauthenticated", "Authentication required")
		return
	}
	sessionID, ok := SessionFrom(r.Context())
	if !ok {
		httpx.Error(w, r, http.StatusUnauthorized, "unauthenticated", "Authentication required")
		return
	}

	var req changePasswordRequest
	if err := httpx.DecodeJSON(w, r, &req, 8<<10); err != nil {
		httpx.Error(w, r, http.StatusBadRequest, "invalid_body", "Request body is not valid JSON")
		return
	}

	err := h.svc.ChangePassword(r.Context(), actor.UserID, sessionID, req.CurrentPassword, req.NewPassword)
	switch {
	case errors.Is(err, domain.ErrInvalidCredentials):
		httpx.ErrorWithFields(w, r, http.StatusUnprocessableEntity, "validation_failed",
			"Invalid request", map[string]any{"current_password": "mật khẩu hiện tại không đúng"})
		return
	case errors.Is(err, password.ErrTooShort), errors.Is(err, password.ErrTooLong):
		httpx.ErrorWithFields(w, r, http.StatusUnprocessableEntity, "validation_failed",
			"Invalid request", map[string]any{"new_password": err.Error()})
		return
	case err != nil:
		httpx.Logger(r.Context()).Error("changing password", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "Internal server error")
		return
	}

	h.writeAudit(r, actor, "user.password_changed", map[string]any{"user_id": actor.UserID}, nil)
	httpx.JSON(w, r, http.StatusOK, map[string]any{
		"message": "Đã đổi mật khẩu. Các phiên đăng nhập khác đã bị thu hồi.",
	})
}

func (h *Handler) regenerateRecoveryCodes(w http.ResponseWriter, r *http.Request) {
	actor, ok := authn.ActorFrom(r.Context())
	if !ok || actor.Kind != authn.KindUser {
		httpx.Error(w, r, http.StatusUnauthorized, "unauthenticated", "Authentication required")
		return
	}

	codes, err := h.svc.GenerateRecoveryCodes(r.Context(), actor.UserID)
	if err != nil {
		httpx.Logger(r.Context()).Error("generating recovery codes", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "Internal server error")
		return
	}

	h.writeAudit(r, actor, "user.recovery_codes_regenerated",
		map[string]any{"user_id": actor.UserID}, nil)

	// Shown once. Only hashes are kept, so there is no way to retrieve them again.
	httpx.JSON(w, r, http.StatusOK, map[string]any{
		"codes":   codes,
		"message": "Lưu các mã này ở nơi an toàn. Chúng chỉ hiển thị một lần và mỗi mã dùng được một lần.",
	})
}

func (h *Handler) countRecoveryCodes(w http.ResponseWriter, r *http.Request) {
	actor, ok := authn.ActorFrom(r.Context())
	if !ok || actor.Kind != authn.KindUser {
		httpx.Error(w, r, http.StatusUnauthorized, "unauthenticated", "Authentication required")
		return
	}
	n, err := h.svc.RemainingRecoveryCodes(r.Context(), actor.UserID)
	if err != nil {
		httpx.Logger(r.Context()).Error("counting recovery codes", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "Internal server error")
		return
	}
	httpx.JSON(w, r, http.StatusOK, map[string]any{"remaining": n})
}
