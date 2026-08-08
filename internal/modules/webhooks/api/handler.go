// Package api exposes webhook configuration.
package api

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/collectr/collectr/internal/modules/webhooks/domain"
	"github.com/collectr/collectr/internal/modules/webhooks/store"
	"github.com/collectr/collectr/internal/platform/authn"
	"github.com/collectr/collectr/internal/platform/crypto"
	"github.com/collectr/collectr/internal/platform/httpx"
)

// Handler serves the webhook routes.
type Handler struct {
	store     *store.Store
	env       *crypto.Envelope
	allowHTTP bool
}

// New returns a Handler. allowHTTP is for development only.
func New(s *store.Store, env *crypto.Envelope, allowHTTP bool) *Handler {
	return &Handler{store: s, env: env, allowHTTP: allowHTTP}
}

// RegisterAdmin mounts the routes.
func (h *Handler) RegisterAdmin(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/webhooks", h.create)
	mux.HandleFunc("GET /api/v1/webhooks", h.list)
	mux.HandleFunc("DELETE /api/v1/webhooks/{id}", h.delete)
	mux.HandleFunc("GET /api/v1/webhooks/{id}/deliveries", h.deliveries)
	mux.HandleFunc("POST /api/v1/webhooks/{id}/deliveries/{delivery_id}/replay", h.replay)
}

func (h *Handler) actor(w http.ResponseWriter, r *http.Request) (authn.Actor, bool) {
	a, ok := authn.ActorFrom(r.Context())
	if !ok {
		httpx.Error(w, r, http.StatusUnauthorized, "unauthenticated", "Authentication required")
		return authn.Actor{}, false
	}
	if !a.Can(authn.CapWebhookManage) {
		httpx.Error(w, r, http.StatusForbidden, "forbidden", "Insufficient permissions")
		return authn.Actor{}, false
	}
	return a, true
}

type createBody struct {
	ProjectID      string   `json:"project_id"`
	URL            string   `json:"url"`
	Events         []string `json:"events"`
	IncludeAnswers bool     `json:"include_answers"`
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.actor(w, r)
	if !ok {
		return
	}

	var body createBody
	if err := httpx.DecodeJSON(w, r, &body, 16<<10); err != nil {
		httpx.Error(w, r, http.StatusBadRequest, "invalid_body", "Request body is not valid JSON")
		return
	}
	projectID, err := uuid.Parse(body.ProjectID)
	if err != nil || !actor.InProject(projectID) {
		httpx.ErrorWithFields(w, r, http.StatusUnprocessableEntity, "validation_failed",
			"Invalid request", map[string]any{"project_id": "must be a project you administer"})
		return
	}
	if _, err := domain.ValidateURL(body.URL, h.allowHTTP); err != nil {
		// The reason is returned: this is an administrator configuring their own
		// integration, and "invalid" without saying why wastes their afternoon.
		httpx.ErrorWithFields(w, r, http.StatusUnprocessableEntity, "validation_failed",
			"Invalid request", map[string]any{"url": err.Error()})
		return
	}
	if len(body.Events) == 0 {
		httpx.ErrorWithFields(w, r, http.StatusUnprocessableEntity, "validation_failed",
			"Invalid request", map[string]any{"events": "at least one event is required"})
		return
	}
	for _, e := range body.Events {
		if !domain.ValidEvent(e) {
			httpx.ErrorWithFields(w, r, http.StatusUnprocessableEntity, "validation_failed",
				"Invalid request", map[string]any{"events": "unknown event " + e})
			return
		}
	}

	secret, err := newSecret()
	if err != nil {
		httpx.Logger(r.Context()).Error("generating webhook secret", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "Internal server error")
		return
	}
	sealed, err := h.env.SealBytes([]byte(secret))
	if err != nil {
		httpx.Logger(r.Context()).Error("sealing webhook secret", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "Internal server error")
		return
	}

	hook := store.Webhook{
		ID: uuid.New(), TenantID: actor.TenantID, ProjectID: projectID,
		URL: body.URL, Events: body.Events, SecretEnc: sealed,
		IncludeAnswers: body.IncludeAnswers, CreatedAt: time.Now().UTC(),
	}
	hook.CreatedBy = actor.UserID
	hook.Active = true
	if err := h.store.Create(r.Context(), hook); err != nil {
		httpx.Logger(r.Context()).Error("creating webhook", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "Internal server error")
		return
	}

	response := map[string]any{
		"id": hook.ID, "url": hook.URL, "events": hook.Events,
		"include_answers": hook.IncludeAnswers,
		// Shown once. Only the sealed copy is kept, so it cannot be retrieved
		// again -- which is also what stops a compromised admin session from
		// reading every receiver's verification key.
		"secret": secret,
	}
	if hook.IncludeAnswers {
		response["warning"] = "Webhook này sẽ gửi câu trả lời (dữ liệu cá nhân) tới bên thứ ba. " +
			"Hãy ghi nhận bên nhận vào hồ sơ xử lý dữ liệu của bạn."
	}
	httpx.JSON(w, r, http.StatusCreated, response)
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	projectID, _ := uuid.Parse(r.URL.Query().Get("project_id"))

	hooks, err := h.store.List(r.Context(), actor.TenantID, projectID)
	if err != nil {
		httpx.Logger(r.Context()).Error("listing webhooks", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "Internal server error")
		return
	}

	out := make([]map[string]any, 0, len(hooks))
	for _, hook := range hooks {
		// The secret is never returned, not even to the person who created it.
		out = append(out, map[string]any{
			"id": hook.ID, "url": hook.URL, "events": hook.Events,
			"active": hook.Active, "include_answers": hook.IncludeAnswers,
			"consecutive_failures": hook.Failures,
			"disabled_at":          hook.DisabledAt,
			"disabled_reason":      hook.DisabledReason,
		})
	}
	httpx.JSON(w, r, http.StatusOK, map[string]any{"data": out})
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.Error(w, r, http.StatusNotFound, "not_found", "Webhook not found")
		return
	}
	if err := h.store.Delete(r.Context(), actor.TenantID, id); err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			httpx.Error(w, r, http.StatusNotFound, "not_found", "Webhook not found")
			return
		}
		httpx.Logger(r.Context()).Error("deleting webhook", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "Internal server error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) deliveries(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.Error(w, r, http.StatusNotFound, "not_found", "Webhook not found")
		return
	}

	list, err := h.store.ListDeliveries(r.Context(), actor.TenantID, id, 100)
	if err != nil {
		httpx.Logger(r.Context()).Error("listing deliveries", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "Internal server error")
		return
	}

	out := make([]map[string]any, 0, len(list))
	for _, d := range list {
		// The payload is not echoed back: it is a copy of personal data, and a
		// delivery log is not a second place to read it from.
		out = append(out, map[string]any{
			"id": d.ID, "event_type": d.EventType,
			"attempt": d.Attempt, "status": d.Status,
		})
	}
	httpx.JSON(w, r, http.StatusOK, map[string]any{"data": out})
}

func (h *Handler) replay(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	deliveryID, err := uuid.Parse(r.PathValue("delivery_id"))
	if err != nil {
		httpx.Error(w, r, http.StatusNotFound, "not_found", "Delivery not found")
		return
	}

	if err := h.store.Replay(r.Context(), actor.TenantID, deliveryID); err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			httpx.Error(w, r, http.StatusNotFound, "not_found",
				"Không tìm thấy lần gửi này, hoặc nó chưa thất bại")
			return
		}
		httpx.Logger(r.Context()).Error("replaying delivery", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "Internal server error")
		return
	}
	httpx.JSON(w, r, http.StatusAccepted, map[string]string{"status": "queued"})
}

// newSecret returns 256 bits of shared secret for signing deliveries.
func newSecret() (string, error) {
	buf := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, buf); err != nil {
		return "", err
	}
	return "whsec_" + base64.RawURLEncoding.EncodeToString(buf), nil
}
