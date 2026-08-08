package api

import (
	"context"
	"net/http"
	"strconv"

	"github.com/google/uuid"

	"github.com/collectr/collectr/internal/modules/files/store"
	"github.com/collectr/collectr/internal/platform/authn"
	"github.com/collectr/collectr/internal/platform/httpx"
)

// AttachmentLister reads what a form has received.
type AttachmentLister interface {
	ListByForm(ctx context.Context, tenantID, formID uuid.UUID, limit int) ([]store.Attachment, error)
}

// RegisterAdmin mounts the attachment listing.
func (h *Handler) RegisterAdmin(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/forms/{id}/files", h.listFiles)
}

// SetLister attaches the read side.
func (h *Handler) SetLister(l AttachmentLister) { h.lister = l }

// listFiles reports the attachments a form has received.
//
// Requires submission.read, not form.read: an attachment is an answer. Whoever
// may not read the responses may not read what was uploaded alongside them,
// and file names alone often say enough -- "giay_kham_suc_khoe.pdf" is a health
// record before anybody opens it.
func (h *Handler) listFiles(w http.ResponseWriter, r *http.Request) {
	actor, ok := authn.ActorFrom(r.Context())
	if !ok {
		httpx.Error(w, r, http.StatusUnauthorized, "unauthenticated", "Authentication required")
		return
	}
	if !actor.Can(authn.CapSubmissionRead) {
		httpx.Error(w, r, http.StatusForbidden, "forbidden", "Insufficient permissions")
		return
	}
	formID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.Error(w, r, http.StatusBadRequest, "invalid_id", "Form id must be a uuid")
		return
	}
	if h.lister == nil {
		httpx.Error(w, r, http.StatusServiceUnavailable, "unavailable", "Chưa sẵn sàng")
		return
	}

	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			httpx.Error(w, r, http.StatusBadRequest, "invalid_limit", "Limit must be a number")
			return
		}
		limit = n
	}

	files, err := h.lister.ListByForm(r.Context(), actor.TenantID, formID, limit)
	if err != nil {
		httpx.Logger(r.Context()).Error("listing attachments", "error", err, "form_id", formID)
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "Internal server error")
		return
	}
	httpx.JSON(w, r, http.StatusOK, map[string]any{
		"data": files,
		// Said with the data rather than left to the interface: an attachment is
		// personal data on the same footing as an answer, and it is erased with
		// the subject rather than swept separately.
		"note": "Tệp đính kèm là dữ liệu cá nhân: bị xóa cùng chủ thể khi có yêu " +
			"cầu xóa, và theo cùng chính sách lưu trữ với bản ghi chứa nó.",
	})
}
