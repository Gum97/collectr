// Package api exposes attachment upload and download.
package api

import (
	"bytes"
	"errors"
	"mime"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/collectr/collectr/internal/contracts"
	"github.com/collectr/collectr/internal/modules/files/app"
	"github.com/collectr/collectr/internal/modules/files/domain"
	"github.com/collectr/collectr/internal/platform/httpx"
	"github.com/collectr/collectr/internal/platform/storage"
)

// Handler serves the attachment routes.
type Handler struct {
	svc      *app.Service
	forms    contracts.UploadResolver
	pepper   []byte
	fileHost string
	lister   AttachmentLister
}

// New returns a Handler. fileHost is the origin attachments are served from.
func New(svc *app.Service, forms contracts.UploadResolver, pepper []byte, fileHost string) *Handler {
	return &Handler{svc: svc, forms: forms, pepper: pepper, fileHost: fileHost}
}

// Register mounts the public routes.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/pub/forms/{public_id}/uploads", h.upload)
	mux.HandleFunc("GET /api/pub/files/{key...}", h.download)
}

// upload accepts one attachment for one question.
func (h *Handler) upload(w http.ResponseWriter, r *http.Request) {
	fieldID := r.URL.Query().Get("field_id")
	if fieldID == "" {
		httpx.ErrorWithFields(w, r, http.StatusUnprocessableEntity, "validation_failed",
			"Invalid request", map[string]any{"field_id": "is required"})
		return
	}

	fc, err := h.forms.UploadContext(r.Context(), r.PathValue("public_id"), fieldID)
	if err != nil {
		// One answer for unknown form, closed form and a field that is not an
		// upload: none of them should be distinguishable from outside.
		httpx.Error(w, r, http.StatusNotFound, "not_found", "Form not found")
		return
	}

	// A hard ceiling before any parsing, so a hostile body cannot exhaust memory
	// no matter what the multipart headers claim.
	r.Body = http.MaxBytesReader(w, r.Body, domain.MaxUploadBytes+(1<<20))

	file, header, err := r.FormFile("file")
	if err != nil {
		httpx.ErrorWithFields(w, r, http.StatusUnprocessableEntity, "validation_failed",
			"Invalid request", map[string]any{"file": "a file part is required"})
		return
	}
	defer func() { _ = file.Close() }()

	uploaded, err := h.svc.Upload(r.Context(), app.UploadInput{
		TenantID: fc.TenantID, ProjectID: fc.ProjectID,
		FormVersionID: fc.FormVersionID, FieldID: fieldID,
		Filename: header.Filename, MaxMB: fc.MaxMB, Accept: fc.Accept,
	}, file)

	switch {
	case errors.Is(err, domain.ErrEmpty):
		httpx.Error(w, r, http.StatusUnprocessableEntity, "file_empty", "Tệp rỗng")
		return
	case errors.Is(err, domain.ErrTooLarge):
		httpx.Error(w, r, http.StatusRequestEntityTooLarge, "file_too_large", err.Error())
		return
	case errors.Is(err, domain.ErrTypeMismatch):
		// The client is told what happened without being told which signatures
		// the detector knows: that list is a map for anyone trying to slip
		// something past it.
		httpx.Error(w, r, http.StatusUnsupportedMediaType, "file_type_rejected",
			"Không nhận dạng được định dạng tệp này")
		return
	case errors.Is(err, domain.ErrTypeNotAllowed):
		httpx.Error(w, r, http.StatusUnsupportedMediaType, "file_type_rejected",
			"Câu hỏi này không nhận định dạng tệp đó")
		return
	case err != nil:
		httpx.Logger(r.Context()).Error("uploading file", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "Internal server error")
		return
	}

	httpx.JSON(w, r, http.StatusCreated, uploaded)
}

// download serves an attachment against a signed URL.
func (h *Handler) download(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	sig := r.URL.Query().Get("sig")
	exp, err := strconv.ParseInt(r.URL.Query().Get("exp"), 10, 64)
	if err != nil || sig == "" {
		httpx.Error(w, r, http.StatusForbidden, "invalid_signature", "This link is not valid")
		return
	}
	if !storage.VerifySignature(h.pepper, key, sig, exp, time.Now()) {
		// Expired and forged are one answer: distinguishing them tells an
		// attacker whether a key exists.
		httpx.Error(w, r, http.StatusForbidden, "invalid_signature",
			"This link has expired or is not valid")
		return
	}

	fileID, err := uuid.Parse(r.URL.Query().Get("id"))
	if err != nil {
		httpx.Error(w, r, http.StatusForbidden, "invalid_signature", "This link is not valid")
		return
	}

	f, content, err := h.svc.Open(r.Context(), fileID)
	if errors.Is(err, domain.ErrNotFound) {
		// Also the answer once the key has been destroyed by an erasure.
		http.NotFound(w, r)
		return
	}
	if err != nil {
		httpx.Logger(r.Context()).Error("reading attachment", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "Internal server error")
		return
	}

	// Always an attachment, never rendered inline. A PDF or an image served
	// inline from an origin that also holds a session is an XSS surface; the
	// download path deliberately refuses to be one.
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition",
		mime.FormatMediaType("attachment", map[string]string{"filename": f.OriginalName}))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Length", strconv.Itoa(len(content)))

	http.ServeContent(w, r, f.OriginalName, f.CreatedAt, bytes.NewReader(content))
}

// SignedURL builds a time-limited link to an attachment.
func (h *Handler) SignedURL(fileID uuid.UUID, storageKey string) string {
	exp := time.Now().Add(app.DownloadTTL).Unix()
	return h.fileHost + "/api/pub/files/" + storageKey +
		"?id=" + fileID.String() +
		"&exp=" + strconv.FormatInt(exp, 10) +
		"&sig=" + storage.Sign(h.pepper, storageKey, exp)
}
