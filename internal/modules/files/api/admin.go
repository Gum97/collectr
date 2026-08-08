package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/collectr/collectr/internal/contracts"
	"github.com/collectr/collectr/internal/modules/files/app"
	"github.com/collectr/collectr/internal/modules/files/domain"
	"github.com/collectr/collectr/internal/modules/files/store"
	"github.com/collectr/collectr/internal/platform/authn"
	"github.com/collectr/collectr/internal/platform/httpx"
	"github.com/collectr/collectr/internal/platform/postgres"
)

// AttachmentLister reads what a form has received.
type AttachmentLister interface {
	ListByForm(ctx context.Context, tenantID, formID uuid.UUID, limit int) ([]store.Attachment, error)
}

// RegisterAdmin mounts the attachment listing.
func (h *Handler) RegisterAdmin(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/forms/{id}/files", h.listFiles)
	mux.HandleFunc("POST /api/v1/files/{id}/download-url", h.mintDownload)
}

// SetLister attaches the read side.
func (h *Handler) SetLister(l AttachmentLister) { h.lister = l }

// SetAudit attaches the trail that download links are recorded in.
func (h *Handler) SetAudit(db *postgres.DB, w contracts.AuditWriter) {
	h.db, h.audit = db, w
}

// mintDownload issues a short-lived link to one attachment.
//
// A POST, not a GET, and a separate call rather than a field on the listing.
// The listing says what came in; this says who read it. Putting a fetchable URL
// in the list response would turn one glance at the table into a bulk read of
// every document in it, with one audit line or none.
//
// The link that comes back is a bearer credential: whoever holds it can fetch
// the file until it expires, with no further check. That is the same bargain a
// presigned S3 URL makes, and DownloadTTL is minutes for that reason.
func (h *Handler) mintDownload(w http.ResponseWriter, r *http.Request) {
	actor, ok := authn.ActorFrom(r.Context())
	if !ok {
		httpx.Error(w, r, http.StatusUnauthorized, "unauthenticated", "Authentication required")
		return
	}
	// The same capability the listing needs, for the same reason: an attachment
	// is an answer. Reading one is reading a submission.
	if !actor.Can(authn.CapSubmissionRead) {
		httpx.Error(w, r, http.StatusForbidden, "forbidden", "Insufficient permissions")
		return
	}
	fileID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.Error(w, r, http.StatusBadRequest, "invalid_id", "File id must be a uuid")
		return
	}
	if h.audit == nil || h.db == nil {
		httpx.Error(w, r, http.StatusServiceUnavailable, "unavailable", "Chưa sẵn sàng")
		return
	}

	f, err := h.svc.Locate(r.Context(), actor.TenantID, fileID)
	if errors.Is(err, domain.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		httpx.Logger(r.Context()).Error("locating attachment", "error", err, "file_id", fileID)
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "Internal server error")
		return
	}

	// Holding submission.read across the tenant is not the same as holding it
	// over this project. A credential scoped to one project reaching a file in
	// another is the hole the link routes shipped with.
	if !actor.InProject(f.ProjectID) {
		http.NotFound(w, r)
		return
	}

	// Recorded before the link exists, and in a transaction, so a failure to
	// write the trail is a failure to issue the link. An audited action whose
	// audit is best-effort is an unaudited action.
	err = h.db.InTenantTx(r.Context(), actor.TenantID, func(tx pgx.Tx) error {
		return h.audit.Write(r.Context(), tx, contracts.AuditEntry{
			TenantID: actor.TenantID,
			Actor: contracts.AuditActor{
				Type: "user", ID: actor.UserID.String(), IPPrefix: httpx.IPPrefix(r),
			},
			Action: "submission.read_file",
			Target: map[string]any{"file_id": f.ID, "submission_id": f.SubmissionID},
			Payload: map[string]any{
				"filename":     f.OriginalName,
				"content_type": f.ContentType,
				"size_bytes":   f.SizeBytes,
			},
		})
	})
	if err != nil {
		httpx.Logger(r.Context()).Error("auditing attachment read", "error", err, "file_id", fileID)
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "Internal server error")
		return
	}

	httpx.JSON(w, r, http.StatusOK, map[string]any{
		"url":        h.SignedURL(f.ID, f.StorageKey),
		"filename":   f.OriginalName,
		"expires_at": time.Now().Add(app.DownloadTTL).UTC(),
	})
}

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
