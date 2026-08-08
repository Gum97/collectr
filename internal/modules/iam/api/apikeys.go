package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/collectr/collectr/internal/platform/authn"
	"github.com/collectr/collectr/internal/platform/httpx"
)

// RegisterAPIKeys mounts key management.
//
// Separate from the rest of IAM because the thing being managed is a credential
// that acts without a person behind it, and the rules differ accordingly: some
// capabilities are refused outright, and the secret is shown once.
func (h *Handler) RegisterAPIKeys(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/api-keys", h.listAPIKeys)
	mux.HandleFunc("POST /api/v1/api-keys", h.createAPIKey)
	mux.HandleFunc("DELETE /api/v1/api-keys/{id}", h.revokeAPIKey)
}

// SetAuthenticator attaches the credential issuer.
func (h *Handler) SetAuthenticator(a *authn.Authenticator) { h.auth = a }

func (h *Handler) apiKeyActor(w http.ResponseWriter, r *http.Request) (authn.Actor, bool) {
	actor, ok := authn.ActorFrom(r.Context())
	if !ok {
		httpx.Error(w, r, http.StatusUnauthorized, "unauthenticated", "Authentication required")
		return authn.Actor{}, false
	}
	// apikey.manage is on the forbidden list, so a key cannot mint another key.
	// Without that, one leaked credential becomes a permanent supply of them.
	if actor.Kind != authn.KindUser || !actor.Can(authn.CapAPIKeyManage) {
		httpx.Error(w, r, http.StatusForbidden, "forbidden", "Insufficient permissions")
		return authn.Actor{}, false
	}
	if h.auth == nil {
		httpx.Error(w, r, http.StatusServiceUnavailable, "unavailable", "Chưa sẵn sàng")
		return authn.Actor{}, false
	}
	return actor, true
}

func (h *Handler) listAPIKeys(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.apiKeyActor(w, r)
	if !ok {
		return
	}
	keys, err := h.auth.ListAPIKeys(r.Context(), actor.TenantID)
	if err != nil {
		httpx.Logger(r.Context()).Error("listing api keys", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "Internal server error")
		return
	}
	httpx.JSON(w, r, http.StatusOK, map[string]any{
		"data": keys,
		// Sent with the list so the interface can show these refused, with a
		// reason, rather than leaving them out and looking incomplete.
		"forbidden_scopes": authn.ForbiddenAPIKeyScopes(),
	})
}

type createKeyRequest struct {
	Name      string   `json:"name"`
	Scopes    []string `json:"scopes"`
	ProjectID string   `json:"project_id"`
	TTLDays   int      `json:"ttl_days"`
}

// createAPIKey issues a credential and shows it once.
func (h *Handler) createAPIKey(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.apiKeyActor(w, r)
	if !ok {
		return
	}

	var req createKeyRequest
	if err := httpx.DecodeJSON(w, r, &req, 8<<10); err != nil {
		httpx.Error(w, r, http.StatusBadRequest, "invalid_body", "Request body is not valid JSON")
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		httpx.ErrorWithFields(w, r, http.StatusUnprocessableEntity, "validation_failed",
			"Invalid request", map[string]any{"name": "phải đặt tên để biết key này dùng cho việc gì"})
		return
	}
	if len(req.Scopes) == 0 {
		httpx.ErrorWithFields(w, r, http.StatusUnprocessableEntity, "validation_failed",
			"Invalid request", map[string]any{"scopes": "phải chọn ít nhất một quyền"})
		return
	}

	// Refused rather than silently dropped. A key created with a scope that was
	// quietly removed looks like it works until the day it is needed.
	forbidden := map[string]struct{}{}
	for _, c := range authn.ForbiddenAPIKeyScopes() {
		forbidden[c] = struct{}{}
	}
	for _, s := range req.Scopes {
		if _, bad := forbidden[s]; bad {
			httpx.ErrorWithFields(w, r, http.StatusUnprocessableEntity, "scope_forbidden",
				"Quyền này không cấp cho API key", map[string]any{
					"scopes": s + " cần một người chịu trách nhiệm đứng sau, " +
						"không cấp cho một chuỗi nằm trong cấu hình CI",
				})
			return
		}
	}

	var projectID *uuid.UUID
	if req.ProjectID != "" {
		id, err := uuid.Parse(req.ProjectID)
		if err != nil {
			httpx.ErrorWithFields(w, r, http.StatusUnprocessableEntity, "validation_failed",
				"Invalid request", map[string]any{"project_id": "must be a uuid"})
			return
		}
		if !actor.InProject(id) {
			httpx.Error(w, r, http.StatusForbidden, "forbidden", "Insufficient permissions")
			return
		}
		projectID = &id
	}

	// A key that never expires is a key nobody revisits. Ninety days by default,
	// two years at most.
	ttl := 90 * 24 * time.Hour
	if req.TTLDays > 0 {
		if req.TTLDays > 730 {
			httpx.ErrorWithFields(w, r, http.StatusUnprocessableEntity, "validation_failed",
				"Invalid request", map[string]any{"ttl_days": "tối đa 730 ngày"})
			return
		}
		ttl = time.Duration(req.TTLDays) * 24 * time.Hour
	}

	raw, id, err := h.auth.IssueAPIKey(r.Context(), actor.TenantID, projectID,
		actor.UserID, strings.TrimSpace(req.Name), req.Scopes, ttl)
	if err != nil {
		httpx.Logger(r.Context()).Error("issuing api key", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "Internal server error")
		return
	}

	h.writeAudit(r, actor, "apikey.created",
		map[string]any{"api_key_id": id},
		map[string]any{"name": req.Name, "scopes": req.Scopes, "ttl_days": int(ttl.Hours() / 24)})

	// Only stored hashed, so this is the one moment it can be read.
	w.Header().Set("Cache-Control", "no-store")
	httpx.JSON(w, r, http.StatusCreated, map[string]any{
		"id": id, "key": raw,
		"message": "Sao chép ngay. Key chỉ hiện một lần và không lấy lại được — " +
			"hệ thống chỉ lưu bản băm.",
	})
}

func (h *Handler) revokeAPIKey(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.apiKeyActor(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.Error(w, r, http.StatusNotFound, "not_found", "API key not found")
		return
	}
	revoked, err := h.auth.RevokeAPIKey(r.Context(), actor.TenantID, id)
	if err != nil {
		httpx.Logger(r.Context()).Error("revoking api key", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "Internal server error")
		return
	}
	if !revoked {
		// Already revoked, or never existed: one answer, because distinguishing
		// them tells a caller which ids are real.
		httpx.Error(w, r, http.StatusNotFound, "not_found", "API key not found")
		return
	}
	h.writeAudit(r, actor, "apikey.revoked", map[string]any{"api_key_id": id}, nil)
	w.WriteHeader(http.StatusNoContent)
}

var _ = errors.Is
