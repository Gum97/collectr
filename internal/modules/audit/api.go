package audit

import (
	"net/http"
	"strconv"
	"time"

	"github.com/collectr/collectr/internal/platform/authn"
	"github.com/collectr/collectr/internal/platform/httpx"
)

// Handler exposes chain verification.
type Handler struct {
	writer *Writer
}

// NewHandler returns a Handler.
func NewHandler(w *Writer) *Handler { return &Handler{writer: w} }

// RegisterAdmin mounts the audit routes.
func (h *Handler) RegisterAdmin(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/audit", h.list)
	mux.HandleFunc("GET /api/v1/audit/actions", h.actions)
	mux.HandleFunc("POST /api/v1/audit/verify", h.verify)
}

// verify recomputes the tenant's chain and reports the first break.
//
// Reading the audit trail is its own capability: the people it records should
// not be the ones deciding who may inspect it.
func (h *Handler) verify(w http.ResponseWriter, r *http.Request) {
	actor, ok := authn.ActorFrom(r.Context())
	if !ok || !actor.Can(authn.CapAuditRead) {
		httpx.Error(w, r, http.StatusForbidden, "forbidden", "Insufficient permissions")
		return
	}

	res, err := h.writer.Verify(r.Context(), actor.TenantID)
	if err != nil {
		httpx.Logger(r.Context()).Error("verifying audit chain", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "Internal server error")
		return
	}
	if !res.Valid {
		// Not an error response: the endpoint worked exactly as intended and is
		// reporting what it found.
		httpx.Logger(r.Context()).Error("audit chain verification failed",
			"tenant_id", actor.TenantID, "broken_at", res.BrokenAt, "reason", res.Reason)
	}
	httpx.JSON(w, r, http.StatusOK, res)
}

// list returns the trail, newest first.
//
// Reading it is its own capability for the same reason writing it is not
// optional: the people the trail records must not be the ones who decide who
// may inspect it.
func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	actor, ok := authn.ActorFrom(r.Context())
	if !ok || !actor.Can(authn.CapAuditRead) {
		httpx.Error(w, r, http.StatusForbidden, "forbidden", "Insufficient permissions")
		return
	}

	q := r.URL.Query()
	filter := ListFilter{Actor: q.Get("actor"), Action: q.Get("action")}
	if raw := q.Get("cursor"); raw != "" {
		seq, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			httpx.Error(w, r, http.StatusBadRequest, "invalid_cursor", "Cursor must be a sequence number")
			return
		}
		filter.Before = seq
	}
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			httpx.Error(w, r, http.StatusBadRequest, "invalid_limit", "Limit must be a number")
			return
		}
		filter.Limit = n
	}
	for _, bound := range []struct {
		name string
		into *time.Time
	}{{"from", &filter.From}, {"to", &filter.To}} {
		raw := q.Get(bound.name)
		if raw == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			httpx.ErrorWithFields(w, r, http.StatusUnprocessableEntity, "validation_failed",
				"Invalid request", map[string]any{bound.name: "phải theo định dạng RFC3339"})
			return
		}
		*bound.into = t
	}

	entries, err := h.writer.List(r.Context(), actor.TenantID, filter)
	if err != nil {
		httpx.Logger(r.Context()).Error("listing audit entries", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "Internal server error")
		return
	}

	out := map[string]any{"data": entries}
	// The cursor is the last seq returned, not an opaque token: seq is already a
	// dense, monotonic, per-tenant key, and inventing an encoding over it would
	// only hide that.
	if len(entries) > 0 {
		out["next_cursor"] = entries[len(entries)-1].Seq
	}
	httpx.JSON(w, r, http.StatusOK, out)
}

// actions lists the action names actually present, so a filter offers what
// happened rather than a hardcoded list that drifts from the code emitting it.
func (h *Handler) actions(w http.ResponseWriter, r *http.Request) {
	actor, ok := authn.ActorFrom(r.Context())
	if !ok || !actor.Can(authn.CapAuditRead) {
		httpx.Error(w, r, http.StatusForbidden, "forbidden", "Insufficient permissions")
		return
	}
	names, err := h.writer.Actions(r.Context(), actor.TenantID)
	if err != nil {
		httpx.Logger(r.Context()).Error("listing audit actions", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "Internal server error")
		return
	}
	httpx.JSON(w, r, http.StatusOK, map[string]any{"data": names})
}
