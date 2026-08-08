// Package api exposes the form HTTP endpoints.
//
// There is deliberately no public submit endpoint yet. Accepting a response
// requires writing its consent record in the same transaction, and the consent
// module lands in v0.3. Shipping the endpoint first would make "a submission
// without a lawful basis" the default behaviour for anyone who upgrades early.
package api

import (
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/collectr/collectr/internal/contracts"
	"github.com/collectr/collectr/internal/modules/forms/app"
	"github.com/collectr/collectr/internal/modules/forms/domain"
	"github.com/collectr/collectr/internal/platform/authn"
	"github.com/collectr/collectr/internal/platform/httpx"
	"github.com/collectr/collectr/internal/platform/signing"
)

// Handler serves the form routes.
type Handler struct {
	svc    *app.Service
	events contracts.EventCollector
	signer *signing.Signer
	// reports is optional: the funnel endpoint says so rather than 500ing when
	// the composition root has not attached one.
	reports contracts.ReportSource
}

// SetReports attaches the analytics source. Called once at startup by the
// composition root, which is the only place that knows both modules.
func (h *Handler) SetReports(r contracts.ReportSource) { h.reports = r }

// New returns a Handler.
func New(svc *app.Service, events contracts.EventCollector, signer *signing.Signer) *Handler {
	return &Handler{svc: svc, events: events, signer: signer}
}

// RegisterPublic mounts the unauthenticated routes.
func (h *Handler) RegisterPublic(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/pub/forms/{public_id}", h.publicSchema)
}

// RegisterAdmin mounts the management routes.
func (h *Handler) RegisterAdmin(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/forms", h.list)
	mux.HandleFunc("GET /api/v1/forms/{id}", h.get)
	mux.HandleFunc("POST /api/v1/forms", h.create)
	mux.HandleFunc("PUT /api/v1/forms/{id}/draft", h.saveDraft)
	mux.HandleFunc("POST /api/v1/forms/{id}/draft/validate", h.validateDraft)
	mux.HandleFunc("POST /api/v1/forms/{id}/draft/publish", h.publish)
	mux.HandleFunc("GET /api/v1/forms/{id}/versions", h.versions)
	mux.HandleFunc("GET /api/v1/forms/{id}/versions/{a}/diff/{b}", h.versionDiff)
	mux.HandleFunc("GET /api/v1/forms/{id}/analytics/funnel", h.funnel)
	mux.HandleFunc("GET /api/v1/forms/{id}/submissions", h.submissions)
}

// publicSchema returns the live version for rendering.
func (h *Handler) publicSchema(w http.ResponseWriter, r *http.Request) {
	pf, err := h.svc.Public(r.Context(), r.PathValue("public_id"))
	switch {
	case errors.Is(err, domain.ErrFormNotFound), errors.Is(err, domain.ErrVersionRetired):
		// One response for "no such form", "closed" and "withdrawn". Telling
		// them apart would let a caller enumerate which forms a tenant runs.
		httpx.Error(w, r, http.StatusNotFound, "not_found", "Form not found")
		return
	case err != nil:
		httpx.Logger(r.Context()).Error("resolving public form", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "Internal server error")
		return
	}

	// Recorded here rather than left to a beacon from the page. A view is the
	// denominator of the completion rate, and a denominator that depends on the
	// client running JavaScript is a denominator that is sometimes missing --
	// which shows up as a conversion rate above 100%.
	h.recordView(r, pf)

	// The client must echo version_id back on submit so the answers are validated
	// against exactly the schema the respondent saw.
	out := map[string]any{
		"form": map[string]any{
			"public_id": r.PathValue("public_id"),
			"title":     pf.Form.Title,
		},
		"version": map[string]any{
			"id": pf.Form.VersionID,
			"no": pf.Form.VersionNo,
		},
		"schema": pf.Schema,
	}
	if pf.Consent != nil {
		// The text itself, not a reference to it. The submission must carry a
		// digest of what was displayed, so the page has to be given the thing it
		// is going to display and hash.
		out["consent"] = map[string]any{
			"document_id":  pf.Consent.ID,
			"version":      pf.Consent.VersionNo,
			"body_html":    pf.Consent.BodyHTML,
			"content_hash": hex.EncodeToString(pf.Consent.ContentHash),
			"permalink":    "/consent/" + pf.Consent.ID.String(),
		}
	}
	httpx.JSON(w, r, http.StatusOK, out)
}

// recordView emits the form_view event for one render.
func (h *Handler) recordView(r *http.Request, pf app.PublicForm) {
	if h.events == nil {
		return
	}
	var visitID *uuid.UUID
	if token := r.URL.Query().Get("cx"); token != "" && h.signer != nil {
		if v, err := h.signer.Verify(token, time.Now()); err == nil {
			visitID = &v.VisitID
		}
	}

	h.events.Collect(r.Context(), contracts.Event{
		EventID:       uuid.NewString(),
		TenantID:      pf.Form.TenantID,
		Type:          contracts.EventFormView,
		FormID:        &pf.Form.FormID,
		FormVersionID: &pf.Form.VersionID,
		VisitID:       visitID,
		OccurredAt:    time.Now().UTC(),
		Meta:          map[string]any{"ip_prefix": httpx.IPPrefix(r)},
	})
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	actor, ok := authn.ActorFrom(r.Context())
	if !ok {
		httpx.Error(w, r, http.StatusUnauthorized, "unauthenticated", "Authentication required")
		return
	}
	if !actor.Can(authn.CapFormRead) {
		httpx.Error(w, r, http.StatusForbidden, "forbidden", "Insufficient permissions")
		return
	}

	var projectID *uuid.UUID
	if raw := r.URL.Query().Get("project_id"); raw != "" {
		id, err := uuid.Parse(raw)
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
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))

	forms, err := h.svc.List(r.Context(), actor.TenantID, projectID, limit)
	if err != nil {
		httpx.Logger(r.Context()).Error("listing forms", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "Internal server error")
		return
	}

	out := make([]map[string]any, 0, len(forms))
	for _, f := range forms {
		out = append(out, map[string]any{
			"id": f.ID, "project_id": f.ProjectID, "public_id": f.PublicID,
			"title": f.Title, "status": f.Status,
			"live_version": f.LiveVersionNo,
			// The count is of active submissions only: erased and restricted
			// records are not there to be counted.
			"submission_count": f.SubmissionCount,
			"retention_days":   f.RetentionDays,
			"created_at":       f.CreatedAt,
		})
	}
	httpx.JSON(w, r, http.StatusOK, map[string]any{"data": out})
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	actor, formID, ok := h.authorize(w, r, authn.CapFormRead)
	if !ok {
		return
	}

	detail, err := h.svc.Detail(r.Context(), actor.TenantID, formID)
	switch {
	case errors.Is(err, domain.ErrFormNotFound):
		httpx.Error(w, r, http.StatusNotFound, "not_found", "Form not found")
		return
	case err != nil:
		httpx.Logger(r.Context()).Error("getting form", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "Internal server error")
		return
	}

	form := detail.Form
	out := map[string]any{
		"id": form.ID, "project_id": form.ProjectID, "public_id": form.PublicID,
		"title": form.Title, "status": form.Status,
		"live_version_id":  form.LiveVersionID,
		"live_version_no":  detail.LiveVersion,
		"retention_days":   form.RetentionDays,
		"retention_action": form.RetentionAction,
		"created_at":       form.CreatedAt,
		// The working copy, so the builder and every screen that needs to know
		// which fields exist can read it here rather than from the public
		// endpoint, which counts a view for whoever asks.
		"draft_schema": detail.Draft,
		// Stated separately: an absent draft and an empty one are different
		// answers, and a builder that cannot tell them apart will happily
		// overwrite a published form with nothing.
		"has_draft": detail.HasDraft,
	}
	if detail.Live != nil {
		out["live_schema"] = detail.Live.Schema
	}
	httpx.JSON(w, r, http.StatusOK, out)
}

type createRequest struct {
	ProjectID     string        `json:"project_id"`
	Title         string        `json:"title"`
	RetentionDays *int          `json:"retention_days"`
	Draft         domain.Schema `json:"draft"`
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	actor, ok := authn.ActorFrom(r.Context())
	if !ok {
		httpx.Error(w, r, http.StatusUnauthorized, "unauthenticated", "Authentication required")
		return
	}
	if !actor.Can(authn.CapFormWrite) {
		httpx.Error(w, r, http.StatusForbidden, "forbidden", "Insufficient permissions")
		return
	}

	var req createRequest
	if err := httpx.DecodeJSON(w, r, &req, 1<<20); err != nil {
		httpx.Error(w, r, http.StatusBadRequest, "invalid_body", "Request body is not valid JSON")
		return
	}
	projectID, err := uuid.Parse(req.ProjectID)
	if err != nil {
		httpx.ErrorWithFields(w, r, http.StatusUnprocessableEntity, "validation_failed",
			"Invalid request", map[string]any{"project_id": "must be a uuid"})
		return
	}
	if !actor.InProject(projectID) {
		httpx.Error(w, r, http.StatusForbidden, "forbidden", "Insufficient permissions")
		return
	}
	if req.Title == "" {
		httpx.ErrorWithFields(w, r, http.StatusUnprocessableEntity, "validation_failed",
			"Invalid request", map[string]any{"title": "must not be empty"})
		return
	}

	form, err := h.svc.Create(r.Context(), app.CreateInput{
		TenantID:      actor.TenantID,
		ProjectID:     projectID,
		CreatedBy:     actor.UserID,
		Title:         req.Title,
		RetentionDays: req.RetentionDays,
		Draft:         req.Draft,
	})
	if err != nil {
		httpx.Logger(r.Context()).Error("creating form", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "Internal server error")
		return
	}

	httpx.JSON(w, r, http.StatusCreated, map[string]any{
		"id":        form.ID,
		"public_id": form.PublicID,
		"title":     form.Title,
		"status":    form.Status,
	})
}

func (h *Handler) saveDraft(w http.ResponseWriter, r *http.Request) {
	actor, formID, ok := h.authorize(w, r, authn.CapFormWrite)
	if !ok {
		return
	}

	var schema domain.Schema
	if err := httpx.DecodeJSON(w, r, &schema, 4<<20); err != nil {
		httpx.Error(w, r, http.StatusBadRequest, "invalid_body", "Request body is not valid JSON")
		return
	}

	if err := h.svc.SaveDraft(r.Context(), actor.TenantID, formID, schema); err != nil {
		if errors.Is(err, domain.ErrFormNotFound) {
			httpx.Error(w, r, http.StatusNotFound, "not_found", "Form not found")
			return
		}
		httpx.Logger(r.Context()).Error("saving draft", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "Internal server error")
		return
	}

	// Validation is advisory here so a half-built form can still be saved; it
	// only blocks at publish.
	httpx.JSON(w, r, http.StatusOK, domain.Validate(schema))
}

// validateDraft reports what publishing would do, without doing it.
func (h *Handler) validateDraft(w http.ResponseWriter, r *http.Request) {
	actor, formID, ok := h.authorize(w, r, authn.CapFormWrite)
	if !ok {
		return
	}

	preview, err := h.svc.Preview(r.Context(), actor.TenantID, formID)
	switch {
	case errors.Is(err, domain.ErrFormNotFound):
		httpx.Error(w, r, http.StatusNotFound, "not_found", "Form not found")
		return
	case errors.Is(err, domain.ErrNoDraft):
		httpx.Error(w, r, http.StatusConflict, "no_draft", "This form has no draft to validate")
		return
	case err != nil:
		httpx.Logger(r.Context()).Error("previewing publish", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "Internal server error")
		return
	}
	httpx.JSON(w, r, http.StatusOK, preview)
}

func (h *Handler) publish(w http.ResponseWriter, r *http.Request) {
	actor, formID, ok := h.authorize(w, r, authn.CapFormPublish)
	if !ok {
		return
	}

	version, validation, err := h.svc.Publish(r.Context(), actor.TenantID, formID, actor.UserID, httpx.IPPrefix(r))
	switch {
	case errors.Is(err, app.ErrDraftInvalid):
		// 422 with the full issue list: the publisher needs to know exactly what
		// to fix, and every issue here is one a respondent would otherwise hit.
		httpx.JSON(w, r, http.StatusUnprocessableEntity, map[string]any{
			"error":      "draft_invalid",
			"validation": validation,
		})
		return
	case errors.Is(err, domain.ErrFormNotFound):
		httpx.Error(w, r, http.StatusNotFound, "not_found", "Form not found")
		return
	case errors.Is(err, domain.ErrNoDraft):
		httpx.Error(w, r, http.StatusConflict, "no_draft", "This form has no draft to publish")
		return
	case err != nil:
		httpx.Logger(r.Context()).Error("publishing form", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "Internal server error")
		return
	}

	httpx.JSON(w, r, http.StatusCreated, map[string]any{
		"version_id":   version.ID,
		"version_no":   version.VersionNo,
		"published_at": version.PublishedAt,
	})
}

func (h *Handler) versions(w http.ResponseWriter, r *http.Request) {
	actor, formID, ok := h.authorize(w, r, authn.CapFormRead)
	if !ok {
		return
	}

	versions, err := h.svc.Versions(r.Context(), actor.TenantID, formID)
	if err != nil {
		httpx.Logger(r.Context()).Error("listing versions", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "Internal server error")
		return
	}

	out := make([]map[string]any, 0, len(versions))
	for _, v := range versions {
		out = append(out, map[string]any{
			"id":           v.ID,
			"version_no":   v.VersionNo,
			"published_at": v.PublishedAt,
			"retired_at":   v.RetiredAt,
			"field_count":  len(v.Schema.Fields),
			"rule_count":   len(v.Schema.Rules),
		})
	}
	httpx.JSON(w, r, http.StatusOK, map[string]any{"data": out})
}

func (h *Handler) submissions(w http.ResponseWriter, r *http.Request) {
	actor, formID, ok := h.authorize(w, r, authn.CapSubmissionRead)
	if !ok {
		return
	}

	var (
		before time.Time
		err    error
	)
	if cursor := r.URL.Query().Get("cursor"); cursor != "" {
		if before, err = time.Parse(time.RFC3339Nano, cursor); err != nil {
			httpx.Error(w, r, http.StatusBadRequest, "invalid_cursor", "Cursor is not a valid timestamp")
			return
		}
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))

	// Seeing a record and seeing the sensitive data inside it are separate
	// permissions, and the caller has to ask for the second one explicitly.
	reveal := r.URL.Query().Get("include_sensitive") == "true" &&
		actor.Can(authn.CapSubmissionReadSensitive)

	grid, err := h.svc.Submissions(r.Context(), actor.TenantID, formID, before, limit, reveal)
	if err != nil {
		httpx.Logger(r.Context()).Error("building submission grid", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "Internal server error")
		return
	}
	httpx.JSON(w, r, http.StatusOK, grid)
}

// authorize resolves the actor, checks the capability and parses the form id.
func (h *Handler) authorize(w http.ResponseWriter, r *http.Request, capability string) (authn.Actor, uuid.UUID, bool) {
	actor, ok := authn.ActorFrom(r.Context())
	if !ok {
		httpx.Error(w, r, http.StatusUnauthorized, "unauthenticated", "Authentication required")
		return authn.Actor{}, uuid.Nil, false
	}
	if !actor.Can(capability) {
		httpx.Error(w, r, http.StatusForbidden, "forbidden", "Insufficient permissions")
		return authn.Actor{}, uuid.Nil, false
	}
	formID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.Error(w, r, http.StatusBadRequest, "invalid_id", "Form id must be a uuid")
		return authn.Actor{}, uuid.Nil, false
	}
	return actor, formID, true
}

// funnel reports the conversion path for one form.
func (h *Handler) funnel(w http.ResponseWriter, r *http.Request) {
	actor, formID, ok := h.authorize(w, r, authn.CapAnalyticsRead)
	if !ok {
		return
	}
	if h.reports == nil {
		httpx.Error(w, r, http.StatusServiceUnavailable, "unavailable",
			"Báo cáo chưa sẵn sàng")
		return
	}

	to := time.Now().UTC()
	from := to.AddDate(0, 0, -30)
	if raw := r.URL.Query().Get("from"); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			httpx.ErrorWithFields(w, r, http.StatusUnprocessableEntity, "validation_failed",
				"Invalid request", map[string]any{"from": "phải theo định dạng RFC3339"})
			return
		}
		from = t.UTC()
	}
	if raw := r.URL.Query().Get("to"); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			httpx.ErrorWithFields(w, r, http.StatusUnprocessableEntity, "validation_failed",
				"Invalid request", map[string]any{"to": "phải theo định dạng RFC3339"})
			return
		}
		to = t.UTC()
	}
	bucket := 24 * time.Hour
	switch r.URL.Query().Get("group_by") {
	case "hour":
		bucket = time.Hour
	case "week":
		bucket = 7 * 24 * time.Hour
	}

	summary, err := h.reports.Funnel(r.Context(), actor.TenantID, formID, from, to, bucket)
	if err != nil {
		httpx.Logger(r.Context()).Error("reading funnel", "error", err, "form_id", formID)
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "Internal server error")
		return
	}
	// A missing drop-off breakdown is not a failed report: it needs page events,
	// which only exist once a respondent has moved between pages.
	pages, err := h.reports.PageDropOff(r.Context(), actor.TenantID, formID, from, to)
	if err != nil {
		httpx.Logger(r.Context()).Warn("reading drop-off", "error", err, "form_id", formID)
	}

	points := make([]map[string]any, 0, len(summary.Points))
	for _, p := range summary.Points {
		points = append(points, map[string]any{
			"bucket": p.Bucket, "clicks": p.Clicks, "views": p.Views,
			"starts": p.Starts, "submits": p.Submits,
		})
	}
	out := map[string]any{
		"from": from, "to": to,
		"clicks": summary.Clicks, "views": summary.Views,
		"starts": summary.Starts, "submits": summary.Submits,
		"points": points,
	}
	// Rates are omitted rather than sent as zero when their denominator is
	// missing. A form opened directly, never through a short link, has no clicks
	// -- and "0% completion" on a form with thousands of submissions is a number
	// people act on.
	if summary.Views > 0 {
		out["completion_rate"] = summary.CompletionRate()
		// A completion rate above 1 is not a very good form. It means views were
		// not recorded for submissions that happened -- data predating the
		// server-side view counter, or a page that never reported one -- so the
		// denominator is short. Said out loud rather than printed as 542200%,
		// because a number that large reads as a bug in the report and a number
		// like 87% would read as the truth.
		if summary.Submits > summary.Views {
			out["denominator_incomplete"] = true
			out["denominator_note"] = "Số lượt gửi lớn hơn số lượt xem, nên tỉ lệ " +
				"hoàn thành không đọc được: một phần lượt gửi không có lượt xem " +
				"tương ứng được ghi nhận."
		}
	}
	if summary.Starts > 0 {
		out["abandon_rate"] = summary.AbandonRate()
	}
	rows := make([]map[string]any, 0, len(pages))
	for _, p := range pages {
		row := map[string]any{"page_id": p.PageID, "entered": p.Entered, "left": p.Left}
		if p.Entered > 0 {
			row["rate"] = p.Rate()
		}
		rows = append(rows, row)
	}
	out["pages"] = rows
	httpx.JSON(w, r, http.StatusOK, out)
}

// versionDiff compares two published versions.
//
// Both are immutable, so the answer never changes and the endpoint is safe to
// cache. It exists so a publish decision can be made against what actually
// changed rather than against a memory of what was edited.
func (h *Handler) versionDiff(w http.ResponseWriter, r *http.Request) {
	actor, formID, ok := h.authorize(w, r, authn.CapFormRead)
	if !ok {
		return
	}
	aID, err1 := uuid.Parse(r.PathValue("a"))
	bID, err2 := uuid.Parse(r.PathValue("b"))
	if err1 != nil || err2 != nil {
		httpx.Error(w, r, http.StatusBadRequest, "invalid_id", "Version ids must be uuids")
		return
	}

	a, b, err := h.svc.VersionPair(r.Context(), actor.TenantID, formID, aID, bID)
	switch {
	case errors.Is(err, domain.ErrFormNotFound):
		httpx.Error(w, r, http.StatusNotFound, "not_found", "Version not found")
		return
	case err != nil:
		httpx.Logger(r.Context()).Error("reading versions", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "Internal server error")
		return
	}

	result := domain.Diff(a.Schema, b.Schema)
	httpx.JSON(w, r, http.StatusOK, map[string]any{
		"from":     map[string]any{"id": a.ID, "version_no": a.VersionNo},
		"to":       map[string]any{"id": b.ID, "version_no": b.VersionNo},
		"changes":  result.Changes,
		"breaking": result.Breaking,
		"blocked":  result.Blocked,
	})
}
