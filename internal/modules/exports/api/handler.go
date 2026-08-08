// Package api exposes export requests and downloads.
package api

import (
	"bytes"
	"errors"
	"mime"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/collectr/collectr/internal/modules/exports/app"
	"github.com/collectr/collectr/internal/modules/exports/store"
	"github.com/collectr/collectr/internal/platform/authn"
	"github.com/collectr/collectr/internal/platform/httpx"
	"github.com/collectr/collectr/internal/platform/postgres"
)

// Handler serves the export routes.
type Handler struct {
	svc *app.Service
	db  *postgres.DB
}

// New returns a Handler.
func New(svc *app.Service, db *postgres.DB) *Handler { return &Handler{svc: svc, db: db} }

// RegisterAdmin mounts the routes.
func (h *Handler) RegisterAdmin(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/forms/{id}/exports", h.request)
	mux.HandleFunc("POST /api/v1/projects/{id}/link-exports", h.requestLinkReport)
	mux.HandleFunc("GET /api/v1/exports/{id}", h.status)
	mux.HandleFunc("GET /api/v1/exports/{id}/download", h.download)
}

type requestBody struct {
	From             string `json:"from"`
	To               string `json:"to"`
	IncludeSensitive bool   `json:"include_sensitive"`
	ProjectID        string `json:"project_id"`
}

// request queues an export.
//
// Two separate permissions apply. Exporting at all needs submission.export;
// including the sensitive columns needs submission.read_sensitive on top. Asking
// for sensitive data without holding that capability is not an error -- the file
// is simply produced with those columns masked.
func (h *Handler) request(w http.ResponseWriter, r *http.Request) {
	actor, ok := authn.ActorFrom(r.Context())
	if !ok {
		httpx.Error(w, r, http.StatusUnauthorized, "unauthenticated", "Authentication required")
		return
	}
	if !actor.Can(authn.CapSubmissionExport) {
		httpx.Error(w, r, http.StatusForbidden, "forbidden", "Insufficient permissions")
		return
	}
	formID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.Error(w, r, http.StatusBadRequest, "invalid_id", "Form id must be a uuid")
		return
	}

	var body requestBody
	if err := httpx.DecodeJSON(w, r, &body, 8<<10); err != nil {
		httpx.Error(w, r, http.StatusBadRequest, "invalid_body", "Request body is not valid JSON")
		return
	}

	from, err := parseDate(body.From)
	if err != nil {
		httpx.ErrorWithFields(w, r, http.StatusUnprocessableEntity, "validation_failed",
			"Invalid request", map[string]any{"from": "must be YYYY-MM-DD"})
		return
	}
	to, err := parseDate(body.To)
	if err != nil {
		httpx.ErrorWithFields(w, r, http.StatusUnprocessableEntity, "validation_failed",
			"Invalid request", map[string]any{"to": "must be YYYY-MM-DD"})
		return
	}

	var projectID uuid.UUID
	if body.ProjectID != "" {
		if projectID, err = uuid.Parse(body.ProjectID); err != nil {
			httpx.ErrorWithFields(w, r, http.StatusUnprocessableEntity, "validation_failed",
				"Invalid request", map[string]any{"project_id": "must be a uuid"})
			return
		}
		if !actor.InProject(projectID) {
			httpx.Error(w, r, http.StatusForbidden, "forbidden", "Insufficient permissions")
			return
		}
	}

	includeSensitive := body.IncludeSensitive && actor.Can(authn.CapSubmissionReadSensitive)

	// Ownership is checked here, before the job is queued, because the worker
	// that runs it connects as the database owner and is therefore exempt from
	// the row-level security every other read relies on. Without this, a form id
	// from another organisation produced that organisation's spreadsheet.
	if err := h.svc.EnsureFormInTenant(r.Context(), actor.TenantID, formID); err != nil {
		httpx.Error(w, r, http.StatusNotFound, "not_found", "Form not found")
		return
	}

	job, err := h.svc.Request(r.Context(), app.RequestInput{
		TenantID: actor.TenantID, ProjectID: projectID, FormID: formID,
		RequestedBy: actor.UserID, From: from, To: to,
		IncludeSensitive: includeSensitive,
	})
	if err != nil {
		httpx.Logger(r.Context()).Error("queueing export", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "Internal server error")
		return
	}

	// Recorded now, not when the file appears: the fact worth keeping is that
	// somebody asked for a bulk extract of personal data.
	if err := h.db.InTenantTx(r.Context(), actor.TenantID, func(tx pgx.Tx) error {
		return h.svc.WriteAudit(r.Context(), tx, job, actor.UserID.String(), httpx.IPPrefix(r))
	}); err != nil {
		httpx.Logger(r.Context()).Error("auditing export request", "error", err)
	}

	httpx.JSON(w, r, http.StatusAccepted, map[string]any{
		"export_id":         job.ID,
		"status":            job.Status,
		"include_sensitive": includeSensitive,
		"masked":            !includeSensitive && body.IncludeSensitive,
	})
}

// requestLinkReport queues a project's link report.
//
// Needs analytics.read rather than submission.export: the workbook holds click
// counts and campaign labels, not anybody's answers. Requiring the export
// capability would mean nobody can see how their links performed without also
// being allowed to extract personal data.
func (h *Handler) requestLinkReport(w http.ResponseWriter, r *http.Request) {
	actor, ok := authn.ActorFrom(r.Context())
	if !ok {
		httpx.Error(w, r, http.StatusUnauthorized, "unauthenticated", "Authentication required")
		return
	}
	if !actor.Can(authn.CapAnalyticsRead) {
		httpx.Error(w, r, http.StatusForbidden, "forbidden", "Insufficient permissions")
		return
	}
	projectID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.Error(w, r, http.StatusBadRequest, "invalid_id", "Project id must be a uuid")
		return
	}
	if !actor.InProject(projectID) {
		httpx.Error(w, r, http.StatusForbidden, "forbidden", "Insufficient permissions")
		return
	}

	var body requestBody
	if err := httpx.DecodeJSON(w, r, &body, 8<<10); err != nil {
		httpx.Error(w, r, http.StatusBadRequest, "invalid_body", "Request body is not valid JSON")
		return
	}
	from, err := parseDate(body.From)
	if err != nil {
		httpx.ErrorWithFields(w, r, http.StatusUnprocessableEntity, "validation_failed",
			"Invalid request", map[string]any{"from": "must be YYYY-MM-DD"})
		return
	}
	to, err := parseDate(body.To)
	if err != nil {
		httpx.ErrorWithFields(w, r, http.StatusUnprocessableEntity, "validation_failed",
			"Invalid request", map[string]any{"to": "must be YYYY-MM-DD"})
		return
	}

	job, err := h.svc.RequestLinkReport(r.Context(), app.RequestInput{
		TenantID: actor.TenantID, ProjectID: projectID,
		RequestedBy: actor.UserID, From: from, To: to,
	})
	if err != nil {
		httpx.Logger(r.Context()).Error("queueing link report", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "Internal server error")
		return
	}

	// Audited like any other export. It holds no answers, but it is still a bulk
	// extract of how a campaign performed, and who pulled it is worth knowing.
	if err := h.db.InTenantTx(r.Context(), actor.TenantID, func(tx pgx.Tx) error {
		return h.svc.WriteAudit(r.Context(), tx, job, actor.UserID.String(), httpx.IPPrefix(r))
	}); err != nil {
		httpx.Logger(r.Context()).Error("auditing link report request", "error", err)
	}

	httpx.JSON(w, r, http.StatusAccepted, map[string]any{
		"export_id": job.ID, "status": job.Status, "kind": job.Kind,
	})
}

func (h *Handler) status(w http.ResponseWriter, r *http.Request) {
	actor, job, ok := h.load(w, r)
	if !ok {
		return
	}
	_ = actor

	body := map[string]any{
		"export_id": job.ID,
		"status":    job.Status,
		"row_count": job.RowCount,
	}
	if job.Status == "ready" {
		body["download_url"] = "/api/v1/exports/" + job.ID.String() + "/download"
		body["expires_at"] = job.ExpiresAt
	}
	if job.Status == "failed" {
		body["error"] = job.Error
	}
	httpx.JSON(w, r, http.StatusOK, body)
}

func (h *Handler) download(w http.ResponseWriter, r *http.Request) {
	actor, job, ok := h.load(w, r)
	if !ok {
		return
	}

	fullJob, content, err := h.svc.Open(r.Context(), actor.TenantID, job.ID)
	switch {
	case errors.Is(err, store.ErrNotReady):
		httpx.Error(w, r, http.StatusConflict, "not_ready", "Tệp chưa sẵn sàng")
		return
	case errors.Is(err, store.ErrExpired):
		httpx.Error(w, r, http.StatusGone, "expired",
			"Tệp đã hết hạn. Hãy yêu cầu xuất lại.")
		return
	case err != nil:
		httpx.Logger(r.Context()).Error("reading export", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "Internal server error")
		return
	}

	w.Header().Set("Content-Type",
		"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition",
		mime.FormatMediaType("attachment", map[string]string{"filename": fullJob.Filename}))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// Never cached: the file is a bulk extract of personal data, and a shared
	// cache holding one is a leak waiting for a second reader.
	w.Header().Set("Cache-Control", "private, no-store")

	http.ServeContent(w, r, fullJob.Filename, time.Now(), bytes.NewReader(content))
}

// load resolves the actor and job, enforcing the export capability and tenant
// ownership.
func (h *Handler) load(w http.ResponseWriter, r *http.Request) (authn.Actor, store.Job, bool) {
	actor, ok := authn.ActorFrom(r.Context())
	if !ok {
		httpx.Error(w, r, http.StatusUnauthorized, "unauthenticated", "Authentication required")
		return authn.Actor{}, store.Job{}, false
	}
	if !actor.Can(authn.CapSubmissionExport) {
		httpx.Error(w, r, http.StatusForbidden, "forbidden", "Insufficient permissions")
		return authn.Actor{}, store.Job{}, false
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.Error(w, r, http.StatusNotFound, "not_found", "Export not found")
		return authn.Actor{}, store.Job{}, false
	}

	// Scoped by tenant in the query itself: an export id from another
	// organisation resolves to nothing rather than to someone else's data.
	job, err := h.svc.Get(r.Context(), actor.TenantID, id)
	if errors.Is(err, store.ErrNotFound) {
		httpx.Error(w, r, http.StatusNotFound, "not_found", "Export not found")
		return authn.Actor{}, store.Job{}, false
	}
	if err != nil {
		httpx.Logger(r.Context()).Error("loading export", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "Internal server error")
		return authn.Actor{}, store.Job{}, false
	}
	return actor, job, true
}

func parseDate(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.DateOnly, s)
}
