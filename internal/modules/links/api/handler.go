// Package api exposes the link HTTP endpoints: the public redirect and QR
// routes, plus the authenticated management routes.
package api

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	qrcode "github.com/skip2/go-qrcode"

	"github.com/collectr/collectr/internal/contracts"
	"github.com/collectr/collectr/internal/modules/links/app"
	"github.com/collectr/collectr/internal/modules/links/domain"
	"github.com/collectr/collectr/internal/platform/authn"
	"github.com/collectr/collectr/internal/platform/httpx"
	"github.com/collectr/collectr/internal/platform/postgres"
	"github.com/collectr/collectr/internal/platform/signing"
)

// Handler serves the link routes.
type Handler struct {
	svc       *app.Service
	events    contracts.EventCollector
	signer    *signing.Signer
	baseURL   string
	scheme    string
	formsPath string

	// pages renders the human-facing answers for a link that cannot be followed.
	// Optional: without it the redirect falls back to plain text, which is what a
	// customer scanning a printed QR code used to get.
	pages DeadLinkPages

	reports      contracts.LinkReporter
	db           *postgres.DB
	audit        contracts.AuditWriter
	rawRetention time.Duration
}

// New returns a Handler.
func New(svc *app.Service, events contracts.EventCollector, reports contracts.LinkReporter, signer *signing.Signer, baseURL, shortURLBase string, rawRetention time.Duration) *Handler {
	return &Handler{
		svc:       svc,
		events:    events,
		signer:    signer,
		baseURL:   baseURL,
		scheme:    schemeOf(shortURLBase),
		formsPath: "/f/",

		reports:      reports,
		rawRetention: rawRetention,
	}
}

// DeadLinkPages renders the three ways a short link can fail to resolve.
//
// An interface rather than a direct dependency: the links module must not import
// the page renderer, and the composition root is the only place that knows both.
type DeadLinkPages interface {
	LinkNotFound(w http.ResponseWriter, r *http.Request)
	LinkEnded(w http.ResponseWriter, r *http.Request)
	LinkLegalHold(w http.ResponseWriter, r *http.Request)
}

// SetDeadLinkPages attaches the renderer. Called once at startup.
func (h *Handler) SetDeadLinkPages(p DeadLinkPages) { h.pages = p }

// RegisterPublic mounts the unauthenticated routes.
func (h *Handler) RegisterPublic(mux *http.ServeMux) {
	mux.HandleFunc("GET /r/{code}", h.redirect)
	mux.HandleFunc("GET /q/{code}", h.qr)
}

// RegisterAdmin mounts the management routes. Authentication and authorisation
// are applied by the caller as middleware.
func (h *Handler) RegisterAdmin(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/links", h.create)
	mux.HandleFunc("GET /api/v1/links", h.list)
	mux.HandleFunc("GET /api/v1/links/{id}", h.get)
	mux.HandleFunc("PATCH /api/v1/links/{id}", h.patch)
	mux.HandleFunc("DELETE /api/v1/links/{id}", h.delete)
}

// redirect is the hot path. Everything it does is either required to produce the
// Location header or is explicitly allowed to fail.
func (h *Handler) redirect(w http.ResponseWriter, r *http.Request) {
	code := r.PathValue("code")
	host := hostOf(r)

	res, err := h.svc.Resolve(r.Context(), host, code)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		h.deadLink(w, r, notFound)
		return
	case errors.Is(err, domain.ErrGone):
		// 410 rather than 404: the visitor scanning a printed code after the
		// campaign ended should learn the link is over, not that it never existed.
		w.Header().Set("Cache-Control", "no-store")
		h.deadLink(w, r, ended)
		return
	case errors.Is(err, domain.ErrLegalHold):
		w.Header().Set("Cache-Control", "no-store")
		h.deadLink(w, r, legalHold)
		return
	case err != nil:
		httpx.Logger(r.Context()).Error("resolving link", "error", err, "code", code)
		httpx.Error(w, r, http.StatusServiceUnavailable, "resolve_failed", "Could not resolve this link")
		return
	}

	visitID := uuid.New()
	token := h.signer.Mint(visitID, res.LinkID, time.Now())

	target, err := h.destination(res, token, r.URL.Query())
	if err != nil {
		httpx.Logger(r.Context()).Error("building redirect target", "error", err, "link_id", res.LinkID)
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "Internal server error")
		return
	}

	meta := map[string]any{
		"ip_prefix": httpx.IPPrefix(r),
		"ua":        uaFamily(r.UserAgent()),
		"referrer":  referrerHost(r.Referer()),
		"src":       r.URL.Query().Get("src"),
	}
	for k, v := range campaign(r.URL.Query()) {
		meta[k] = v
	}

	// Best-effort by design; see analytics.Collector for the degradation chain.
	h.events.Collect(r.Context(), contracts.Event{
		EventID:    uuid.NewString(),
		TenantID:   res.TenantID,
		Type:       contracts.EventClick,
		LinkID:     &res.LinkID,
		FormID:     res.FormID,
		VisitID:    &visitID,
		OccurredAt: time.Now().UTC(),
		Meta:       meta,
	})

	// 302, not 301: repeat visits must keep reaching the server so the funnel
	// stays measurable, expiry takes effect immediately, and -- the reason that
	// settles it -- a link can be withdrawn the moment an erasure request lands.
	// A 301 sits in browser caches indefinitely and cannot be recalled.
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	http.Redirect(w, r, target, http.StatusFound)
}

// ownParams are the query keys the shortener uses itself. They are consumed
// here rather than passed on: cx is an internal visit token and src marks how
// the code was reached, and neither means anything at the destination.
var ownParams = map[string]bool{"cx": true, "src": true}

// destination builds the final URL, carrying the visit token so a later form view
// can be joined to this click without a cookie.
//
// Query parameters on the short link are forwarded to the target. A marketing
// team puts utm_source on the short link and expects it to arrive; dropping them
// -- which is what this did before -- makes every campaign show up as direct
// traffic in whatever analytics the destination runs, and the shortener gets
// blamed for a number nobody can explain.
func (h *Handler) destination(res domain.Resolution, token string, incoming url.Values) (string, error) {
	raw := res.TargetURL
	if raw == "" && res.FormPublicID != "" {
		// The public id, not res.FormID: the endpoint that serves a form is keyed
		// by it, and the primary key is deliberately not exposed to visitors.
		raw = h.baseURL + h.formsPath + res.FormPublicID
	}
	if raw == "" {
		// A link with neither a target nor a resolvable form. The row should not
		// exist, but redirecting to the empty string sends the visitor back to
		// the shortener, which looks like a loop rather than a fault.
		return "", errors.New("link has no destination")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	q := u.Query()
	for key, values := range incoming {
		if ownParams[key] || q.Has(key) {
			// A parameter already on the target was put there deliberately by
			// whoever created the link; a visitor appending the same key does not
			// get to override it.
			continue
		}
		for _, v := range values {
			q.Add(key, v)
		}
	}
	// The visit token goes only to our own form page.
	//
	// It is a signed identifier for this visit, and attaching it to an external
	// destination hands that identifier -- and the ability to replay it as
	// attribution -- to whoever runs the destination, plus their analytics and
	// their logs. It exists to join a click to a form view on this deployment;
	// there is no form view anywhere else.
	if sameOrigin(u, h.baseURL) {
		q.Set("cx", token)
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// campaign extracts the UTM parameters worth reporting on.
//
// Only the three that describe the campaign, and each truncated: these come
// straight off the query string, so an unbounded copy into the event row is an
// invitation to write a megabyte per click.
func campaign(q url.Values) map[string]any {
	out := make(map[string]any, 3)
	for _, key := range []string{"utm_source", "utm_medium", "utm_campaign"} {
		if v := strings.TrimSpace(q.Get(key)); v != "" {
			if len(v) > 120 {
				v = v[:120]
			}
			out[key] = v
		}
	}
	return out
}

// qr renders the QR code for a short link.
func (h *Handler) qr(w http.ResponseWriter, r *http.Request) {
	code := r.PathValue("code")
	if _, err := h.svc.Resolve(r.Context(), hostOf(r), code); err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		// Expired or disabled links still render: the printed code exists in the
		// world regardless, and refusing to draw it helps nobody.
	}

	size := 512
	if raw := r.URL.Query().Get("size"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 64 || n > 2048 {
			httpx.Error(w, r, http.StatusBadRequest, "invalid_size", "size must be between 64 and 2048")
			return
		}
		size = n
	}

	// The QR target carries src=qr so reports can separate scans from clicks.
	// The two behave differently enough -- a scan usually happens in front of the
	// thing being advertised -- that merging them hides the more interesting one.
	target := h.scheme + "://" + hostOf(r) + "/r/" + code + "?src=qr"

	png, err := qrcode.Encode(target, qrcode.Medium, size)
	if err != nil {
		httpx.Logger(r.Context()).Error("encoding qr", "error", err, "code", code)
		httpx.Error(w, r, http.StatusInternalServerError, "qr_failed", "Could not render QR code")
		return
	}

	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	if _, err := w.Write(png); err != nil {
		httpx.Logger(r.Context()).Warn("writing qr response", "error", err)
	}
}

type createRequest struct {
	ProjectID string     `json:"project_id"`
	TargetURL string     `json:"target_url"`
	FormID    string     `json:"form_id"`
	Alias     string     `json:"alias"`
	ExpiresAt *time.Time `json:"expires_at"`
}

type linkResponse struct {
	ID        string     `json:"id"`
	Code      string     `json:"code"`
	ShortURL  string     `json:"short_url"`
	QRURL     string     `json:"qr_url"`
	TargetURL string     `json:"target_url,omitempty"`
	FormID    *uuid.UUID `json:"form_id,omitempty"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	Status    string     `json:"status"`
	CreatedAt time.Time  `json:"created_at"`
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	actor, ok := authn.ActorFrom(r.Context())
	if !ok {
		httpx.Error(w, r, http.StatusUnauthorized, "unauthenticated", "Authentication required")
		return
	}

	var req createRequest
	if err := httpx.DecodeJSON(w, r, &req, 64*1024); err != nil {
		httpx.Error(w, r, http.StatusBadRequest, "invalid_body", "Request body is not valid JSON")
		return
	}

	projectID, err := uuid.Parse(req.ProjectID)
	if err != nil {
		httpx.ErrorWithFields(w, r, http.StatusUnprocessableEntity, "validation_failed",
			"Invalid request", map[string]any{"project_id": "must be a uuid"})
		return
	}

	if !h.allowed(w, r, actor, authn.CapLinkWrite, projectID) {
		return
	}

	in := app.CreateInput{
		TenantID:  actor.TenantID,
		ProjectID: projectID,
		CreatedBy: actor.UserID,
		TargetURL: req.TargetURL,
		Alias:     req.Alias,
		ExpiresAt: req.ExpiresAt,
	}
	if req.FormID != "" {
		formID, err := uuid.Parse(req.FormID)
		if err != nil {
			httpx.ErrorWithFields(w, r, http.StatusUnprocessableEntity, "validation_failed",
				"Invalid request", map[string]any{"form_id": "must be a uuid"})
			return
		}
		in.FormID = &formID
	}

	link, err := h.svc.Create(r.Context(), in)
	switch {
	case errors.Is(err, domain.ErrNoDomain):
		httpx.Error(w, r, http.StatusConflict, "no_domain",
			"Tổ chức chưa có tên miền nào để phát mã. Thêm một tên miền tại "+
				"POST /api/v1/domains trước khi tạo link.")
		return
	case errors.Is(err, domain.ErrAliasTaken):
		httpx.Error(w, r, http.StatusConflict, "alias_taken", "That alias is already in use")
		return
	case errors.Is(err, domain.ErrInvalidTarget):
		httpx.ErrorWithFields(w, r, http.StatusUnprocessableEntity, "validation_failed",
			"Invalid request", map[string]any{"target_url": err.Error()})
		return
	case err != nil:
		httpx.Logger(r.Context()).Error("creating link", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "Internal server error")
		return
	}

	httpx.JSON(w, r, http.StatusCreated, h.present(link))
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	actor, ok := authn.ActorFrom(r.Context())
	if !ok {
		httpx.Error(w, r, http.StatusUnauthorized, "unauthenticated", "Authentication required")
		return
	}

	projectID, err := uuid.Parse(r.URL.Query().Get("project_id"))
	if err != nil {
		httpx.ErrorWithFields(w, r, http.StatusUnprocessableEntity, "validation_failed",
			"Invalid request", map[string]any{"project_id": "must be a uuid"})
		return
	}

	if !h.allowed(w, r, actor, authn.CapLinkRead, projectID) {
		return
	}

	var before time.Time
	if cursor := r.URL.Query().Get("cursor"); cursor != "" {
		if before, err = time.Parse(time.RFC3339Nano, cursor); err != nil {
			httpx.Error(w, r, http.StatusBadRequest, "invalid_cursor", "Cursor is not a valid timestamp")
			return
		}
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))

	links, err := h.svc.List(r.Context(), actor.TenantID, projectID, before, limit)
	if err != nil {
		httpx.Logger(r.Context()).Error("listing links", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "Internal server error")
		return
	}

	items := make([]linkResponse, 0, len(links))
	for _, l := range links {
		items = append(items, h.present(l))
	}
	body := map[string]any{"data": items}
	if n := len(links); n > 0 {
		body["next_cursor"] = links[n-1].CreatedAt.Format(time.RFC3339Nano)
	}
	httpx.JSON(w, r, http.StatusOK, body)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	actor, ok := authn.ActorFrom(r.Context())
	if !ok {
		httpx.Error(w, r, http.StatusUnauthorized, "unauthenticated", "Authentication required")
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.Error(w, r, http.StatusBadRequest, "invalid_id", "Link id must be a uuid")
		return
	}

	link, err := h.svc.Get(r.Context(), actor.TenantID, id)
	if errors.Is(err, domain.ErrNotFound) {
		httpx.Error(w, r, http.StatusNotFound, "not_found", "Link not found")
		return
	}
	if err != nil {
		httpx.Logger(r.Context()).Error("getting link", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "Internal server error")
		return
	}
	if !h.allowed(w, r, actor, authn.CapLinkRead, link.ProjectID) {
		return
	}

	httpx.JSON(w, r, http.StatusOK, h.present(link))
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	actor, ok := authn.ActorFrom(r.Context())
	if !ok {
		httpx.Error(w, r, http.StatusUnauthorized, "unauthenticated", "Authentication required")
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.Error(w, r, http.StatusBadRequest, "invalid_id", "Link id must be a uuid")
		return
	}

	link, err := h.svc.Get(r.Context(), actor.TenantID, id)
	if err != nil {
		httpx.Error(w, r, http.StatusNotFound, "not_found", "Link not found")
		return
	}
	if !h.allowed(w, r, actor, authn.CapLinkDelete, link.ProjectID) {
		return
	}

	err = h.svc.Delete(r.Context(), actor.TenantID, id)
	if errors.Is(err, domain.ErrNotFound) {
		httpx.Error(w, r, http.StatusNotFound, "not_found", "Link not found")
		return
	}
	if err != nil {
		httpx.Logger(r.Context()).Error("deleting link", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "Internal server error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// present renders a link using the host the link itself lives on.
//
// Not the host of the request that asked for it: an operator running the admin
// panel on one domain and the shortener on another would otherwise get admin
// URLs back, and the QR image built from one of those goes on a poster where it
// cannot be corrected.
func (h *Handler) present(l domain.Link) linkResponse {
	return linkResponse{
		ID:        l.ID.String(),
		Code:      l.Code,
		ShortURL:  h.scheme + "://" + l.Host + "/r/" + l.Code,
		QRURL:     h.scheme + "://" + l.Host + "/q/" + l.Code,
		TargetURL: l.TargetURL,
		FormID:    l.FormID,
		ExpiresAt: l.ExpiresAt,
		Status:    l.Status,
		CreatedAt: l.CreatedAt,
	}
}

// schemeOf reads the scheme from the configured origin so that generated URLs
// work in a local deployment served over plain HTTP as well as in production.
func schemeOf(baseURL string) string {
	if strings.HasPrefix(baseURL, "http://") {
		return "http"
	}
	return "https"
}

func hostOf(r *http.Request) string {
	if h := r.Header.Get("X-Forwarded-Host"); h != "" {
		return h
	}
	return r.Host
}

// uaFamily reduces a user agent to a coarse family. Storing the full string would
// be a high-entropy fingerprint of the visitor for no analytical gain.
func uaFamily(ua string) string {
	switch {
	case ua == "":
		return "unknown"
	case strings.Contains(ua, "Edg/"):
		return "edge"
	case strings.Contains(ua, "OPR/"), strings.Contains(ua, "Opera"):
		return "opera"
	case strings.Contains(ua, "Chrome/"):
		return "chrome"
	case strings.Contains(ua, "Firefox/"):
		return "firefox"
	case strings.Contains(ua, "Safari/"):
		return "safari"
	case strings.Contains(ua, "bot"), strings.Contains(ua, "Bot"), strings.Contains(ua, "spider"):
		return "bot"
	default:
		return "other"
	}
}

// referrerHost keeps only the host of a referrer: the path can carry personal
// data placed there by whoever linked to us.
func referrerHost(ref string) string {
	if ref == "" {
		return ""
	}
	u, err := url.Parse(ref)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

type deadLinkKind int

const (
	notFound deadLinkKind = iota
	ended
	legalHold
)

// deadLink answers a link that cannot be followed.
//
// The person on the other end scanned something printed on a poster. A bare
// "404 page not found" tells them the shop got it wrong; a page that says the
// campaign has ended tells them what actually happened.
func (h *Handler) deadLink(w http.ResponseWriter, r *http.Request, kind deadLinkKind) {
	if h.pages != nil {
		switch kind {
		case notFound:
			h.pages.LinkNotFound(w, r)
		case ended:
			h.pages.LinkEnded(w, r)
		case legalHold:
			h.pages.LinkLegalHold(w, r)
		}
		return
	}
	switch kind {
	case notFound:
		http.NotFound(w, r)
	case ended:
		http.Error(w, "This link is no longer available.", http.StatusGone)
	case legalHold:
		http.Error(w, "This link is unavailable for legal reasons.",
			http.StatusUnavailableForLegalReasons)
	}
}

// allowed checks the two questions every object-level write has to answer.
//
// Holding a capability is not enough on its own: the object also has to belong
// to something the caller was granted. These handlers checked neither -- a user
// with link.read created and deleted links in a project they had no grant on.
func (h *Handler) allowed(
	w http.ResponseWriter, r *http.Request, actor authn.Actor, cap string, projectID uuid.UUID,
) bool {
	if !actor.Can(cap) {
		httpx.Error(w, r, http.StatusForbidden, "forbidden", "Insufficient permissions")
		return false
	}
	if projectID != uuid.Nil && !actor.InProject(projectID) {
		// Same answer as an unknown project. Confirming that one exists but is
		// out of reach tells a caller what to ask to be added to.
		httpx.Error(w, r, http.StatusForbidden, "forbidden", "Insufficient permissions")
		return false
	}
	return true
}

// patch changes a link's status.
//
// Legal hold is the reason this exists. It freezes a link and, with it, the
// records reached through it: the retention sweeper must not delete what a
// court has asked to be preserved. That makes it the one setting here that
// deliberately overrides the erasure schedule, so it is recorded with who set
// it and why rather than as a status flip.
func (h *Handler) patch(w http.ResponseWriter, r *http.Request) {
	actor, ok := authn.ActorFrom(r.Context())
	if !ok {
		httpx.Error(w, r, http.StatusUnauthorized, "unauthenticated", "Authentication required")
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.Error(w, r, http.StatusNotFound, "not_found", "Link not found")
		return
	}

	var body struct {
		Status string `json:"status"`
		Reason string `json:"reason"`
	}
	if err := httpx.DecodeJSON(w, r, &body, 8<<10); err != nil {
		httpx.Error(w, r, http.StatusBadRequest, "invalid_body", "Request body is not valid JSON")
		return
	}

	link, err := h.svc.Get(r.Context(), actor.TenantID, id)
	if errors.Is(err, domain.ErrNotFound) {
		httpx.Error(w, r, http.StatusNotFound, "not_found", "Link not found")
		return
	}
	if err != nil {
		httpx.Logger(r.Context()).Error("loading link", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "Internal server error")
		return
	}
	if !h.allowed(w, r, actor, authn.CapLinkWrite, link.ProjectID) {
		return
	}

	// A hold suspends an obligation the organisation is otherwise under. An
	// unexplained one is indistinguishable later from a mistake.
	if body.Status == domain.StatusLegalHold && strings.TrimSpace(body.Reason) == "" {
		httpx.ErrorWithFields(w, r, http.StatusUnprocessableEntity, "validation_failed",
			"Invalid request",
			map[string]any{"reason": "phải nêu lý do khi đặt tạm ngưng vì lý do pháp lý"})
		return
	}

	updated, err := h.svc.SetStatus(r.Context(), actor.TenantID, id, body.Status)
	switch {
	case errors.Is(err, domain.ErrInvalidTarget):
		httpx.ErrorWithFields(w, r, http.StatusUnprocessableEntity, "validation_failed",
			"Invalid request", map[string]any{"status": err.Error()})
		return
	case errors.Is(err, domain.ErrNotFound):
		httpx.Error(w, r, http.StatusNotFound, "not_found", "Link not found")
		return
	case err != nil:
		httpx.Logger(r.Context()).Error("updating link status", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "Internal server error")
		return
	}

	action := "link.status_changed"
	if body.Status == domain.StatusLegalHold {
		action = "link.legal_hold_set"
	} else if link.Status == domain.StatusLegalHold {
		// Lifting is the change that lets deletion resume, so it is named
		// separately: it is the one an auditor looks for.
		action = "link.legal_hold_lifted"
	}
	h.writeAudit(r, actor, action,
		map[string]any{"link_id": id, "project_id": link.ProjectID},
		map[string]any{"from": link.Status, "to": body.Status, "reason": body.Reason})

	httpx.JSON(w, r, http.StatusOK, h.present(updated))
}

// SetAudit attaches the trail writer. Optional at construction so the links
// module keeps no compile-time dependency on the audit module.
func (h *Handler) SetAudit(db *postgres.DB, w contracts.AuditWriter) { h.db, h.audit = db, w }

func (h *Handler) writeAudit(r *http.Request, actor authn.Actor, action string, target, payload map[string]any) {
	if h.db == nil || h.audit == nil {
		return
	}
	err := h.db.InTenantTx(r.Context(), actor.TenantID, func(tx pgx.Tx) error {
		return h.audit.Write(r.Context(), tx, contracts.AuditEntry{
			TenantID: actor.TenantID,
			Actor: contracts.AuditActor{
				Type: "user", ID: actor.UserID.String(), IPPrefix: httpx.IPPrefix(r),
			},
			Action: action, Target: target, Payload: payload,
		})
	})
	if err != nil {
		httpx.Logger(r.Context()).Error("writing audit entry", "error", err, "action", action)
	}
}

// sameOrigin reports whether u points at this deployment's own interface.
func sameOrigin(u *url.URL, baseURL string) bool {
	base, err := url.Parse(baseURL)
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Host, base.Host)
}
