// Package api exposes consent documents and purposes.
package api

import (
	"encoding/hex"
	"errors"
	"html"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	consentdomain "github.com/collectr/collectr/internal/modules/consent/domain"
	"github.com/collectr/collectr/internal/modules/consent/store"
	"github.com/collectr/collectr/internal/platform/authn"
	"github.com/collectr/collectr/internal/platform/httpx"
)

// Handler serves the consent routes.
type Handler struct {
	store *store.Store
}

// New returns a Handler.
func New(s *store.Store) *Handler { return &Handler{store: s} }

// RegisterPublic mounts the permalink.
func (h *Handler) RegisterPublic(mux *http.ServeMux) {
	mux.HandleFunc("GET /p/{id}", h.permalink)
	mux.HandleFunc("GET /api/pub/documents/{id}", h.document)
}

// RegisterAdmin mounts the management routes.
func (h *Handler) RegisterAdmin(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/consent/documents", h.listDocuments)
	mux.HandleFunc("GET /api/v1/consent/purposes", h.listPurposes)
	mux.HandleFunc("GET /api/v1/consent/withdrawals", h.withdrawals)
	mux.HandleFunc("POST /api/v1/consent/documents", h.createDocument)
	mux.HandleFunc("POST /api/v1/consent/purposes", h.createPurpose)
}

// permalink serves an immutable copy of one consent document version.
//
// A form must link here rather than to a URL the tenant controls elsewhere.
// A page that can be edited afterwards cannot show what a person was told at
// the moment they agreed, which is the only thing the record needs to prove.
func (h *Handler) permalink(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	doc, err := h.store.PublicDocument(r.Context(), id)
	if errors.Is(err, store.ErrDocumentNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		httpx.Logger(r.Context()).Error("reading consent document", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "Internal server error")
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// The content can never change, so it can be cached forever. That is the
	// point of addressing it by version rather than by name.
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// Matched to the stricter renderer in internal/webpages: form-action and
	// base-uri do not fall back to default-src, so their absence let a document
	// post a visitor's input elsewhere and rewrite every relative link.
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Robots-Tag", "noindex")
	w.Header().Set("Content-Security-Policy",
		"default-src 'none'; style-src 'unsafe-inline'; img-src 'self' data:; font-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")

	// The digest is displayed so anyone holding a consent record can check by
	// hand that this is the text it refers to.
	page := `<!doctype html><meta charset="utf-8">` +
		`<meta name="viewport" content="width=device-width,initial-scale=1">` +
		`<title>` + html.EscapeString(doc.Kind) + ` v` + strconv.Itoa(doc.VersionNo) + `</title>` +
		`<style>body{font:16px/1.6 system-ui,sans-serif;max-width:46rem;margin:2rem auto;padding:0 1rem}` +
		`footer{margin-top:3rem;padding-top:1rem;border-top:1px solid #ccc;color:#666;font-size:.85em}` +
		`code{word-break:break-all}</style>` +
		doc.BodyHTML +
		`<footer>Version ` + strconv.Itoa(doc.VersionNo) + ` &middot; sha256 <code>` +
		hex.EncodeToString(doc.Hash) + `</code><br>This version is immutable.</footer>`

	if _, err := w.Write([]byte(page)); err != nil {
		httpx.Logger(r.Context()).Warn("writing consent document", "error", err)
	}
}

// document returns the same content as JSON, for the form renderer.
func (h *Handler) document(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.Error(w, r, http.StatusNotFound, "not_found", "Document not found")
		return
	}
	doc, err := h.store.PublicDocument(r.Context(), id)
	if errors.Is(err, store.ErrDocumentNotFound) {
		httpx.Error(w, r, http.StatusNotFound, "not_found", "Document not found")
		return
	}
	if err != nil {
		httpx.Logger(r.Context()).Error("reading consent document", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "Internal server error")
		return
	}

	httpx.JSON(w, r, http.StatusOK, map[string]any{
		"id":         doc.ID,
		"kind":       doc.Kind,
		"version_no": doc.VersionNo,
		"body_html":  doc.BodyHTML,
		// The client hashes what it renders and returns it on submit; the server
		// refuses the submission if the two disagree.
		"content_hash": "sha256:" + hex.EncodeToString(doc.Hash),
		"permalink":    "/consent/" + doc.ID.String(),
	})
}

type createDocumentRequest struct {
	Kind     string `json:"kind"`
	BodyHTML string `json:"body_html"`
}

func (h *Handler) createDocument(w http.ResponseWriter, r *http.Request) {
	actor, ok := authn.ActorFrom(r.Context())
	if !ok || !actor.Can(authn.CapConsentManage) {
		httpx.Error(w, r, http.StatusForbidden, "forbidden", "Insufficient permissions")
		return
	}

	var req createDocumentRequest
	if err := httpx.DecodeJSON(w, r, &req, 1<<20); err != nil {
		httpx.Error(w, r, http.StatusBadRequest, "invalid_body", "Request body is not valid JSON")
		return
	}
	switch req.Kind {
	case "privacy_notice", "consent_text":
	default:
		httpx.ErrorWithFields(w, r, http.StatusUnprocessableEntity, "validation_failed",
			"Invalid request", map[string]any{"kind": "must be privacy_notice or consent_text"})
		return
	}
	// Refused, not sanitised: these bytes get hashed, and that hash is what a
	// consent record cites as proof of what a person was shown. Quietly editing
	// the text would make the proof describe something nobody saw.
	if err := consentdomain.ValidateBodyHTML(req.BodyHTML); err != nil {
		httpx.ErrorWithFields(w, r, http.StatusUnprocessableEntity, "unsafe_body",
			"Văn bản đồng ý không hợp lệ", map[string]any{"body_html": err.Error()})
		return
	}
	if req.BodyHTML == "" {
		httpx.ErrorWithFields(w, r, http.StatusUnprocessableEntity, "validation_failed",
			"Invalid request", map[string]any{"body_html": "must not be empty"})
		return
	}

	ref, err := h.store.CreateDocument(r.Context(), actor.TenantID, actor.UserID, req.Kind, req.BodyHTML)
	if err != nil {
		httpx.Logger(r.Context()).Error("creating consent document", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "Internal server error")
		return
	}

	httpx.JSON(w, r, http.StatusCreated, map[string]any{
		"id":           ref.ID,
		"version_no":   ref.VersionNo,
		"content_hash": "sha256:" + hex.EncodeToString(ref.ContentHash),
		"permalink":    "/consent/" + ref.ID.String(),
	})
}

type createPurposeRequest struct {
	Code          string `json:"code"`
	Name          string `json:"name"`
	Description   string `json:"description"`
	LegalBasis    string `json:"legal_basis"`
	Required      bool   `json:"required"`
	RetentionDays *int   `json:"retention_days"`
}

func (h *Handler) createPurpose(w http.ResponseWriter, r *http.Request) {
	actor, ok := authn.ActorFrom(r.Context())
	if !ok || !actor.Can(authn.CapConsentManage) {
		httpx.Error(w, r, http.StatusForbidden, "forbidden", "Insufficient permissions")
		return
	}

	var req createPurposeRequest
	if err := httpx.DecodeJSON(w, r, &req, 64<<10); err != nil {
		httpx.Error(w, r, http.StatusBadRequest, "invalid_body", "Request body is not valid JSON")
		return
	}
	if req.Code == "" || req.Name == "" {
		httpx.ErrorWithFields(w, r, http.StatusUnprocessableEntity, "validation_failed",
			"Invalid request", map[string]any{"code": "code and name are required"})
		return
	}
	if req.LegalBasis == "" {
		req.LegalBasis = "consent"
	}
	switch req.LegalBasis {
	case "consent", "contract", "legal_obligation", "vital_interest":
	default:
		httpx.ErrorWithFields(w, r, http.StatusUnprocessableEntity, "validation_failed",
			"Invalid request", map[string]any{"legal_basis": "is not a recognised lawful basis"})
		return
	}

	id, err := h.store.CreatePurpose(r.Context(), actor.TenantID,
		req.Code, req.Name, req.Description, req.LegalBasis, req.Required, req.RetentionDays)
	if err != nil {
		httpx.Logger(r.Context()).Error("creating purpose", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "Internal server error")
		return
	}
	httpx.JSON(w, r, http.StatusCreated, map[string]any{"id": id, "code": req.Code})
}

// listDocuments returns every consent document version.
//
// Readable with consent.manage or with the audit capability: the people who
// review whether a record is defensible are not always the people who write the
// text, and requiring the write permission to read the evidence would put the
// two in the same pair of hands.
func (h *Handler) listDocuments(w http.ResponseWriter, r *http.Request) {
	actor, ok := authn.ActorFrom(r.Context())
	if !ok || (!actor.Can(authn.CapConsentManage) && !actor.Can(authn.CapAuditRead)) {
		httpx.Error(w, r, http.StatusForbidden, "forbidden", "Insufficient permissions")
		return
	}
	docs, err := h.store.ListDocuments(r.Context(), actor.TenantID)
	if err != nil {
		httpx.Logger(r.Context()).Error("listing consent documents", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "Internal server error")
		return
	}
	httpx.JSON(w, r, http.StatusOK, map[string]any{"data": docs})
}

// listPurposes returns the declared processing purposes.
func (h *Handler) listPurposes(w http.ResponseWriter, r *http.Request) {
	actor, ok := authn.ActorFrom(r.Context())
	if !ok || (!actor.Can(authn.CapConsentManage) && !actor.Can(authn.CapAuditRead) &&
		!actor.Can(authn.CapFormRead)) {
		httpx.Error(w, r, http.StatusForbidden, "forbidden", "Insufficient permissions")
		return
	}
	purposes, err := h.store.ListPurposes(r.Context(), actor.TenantID)
	if err != nil {
		httpx.Logger(r.Context()).Error("listing purposes", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "Internal server error")
		return
	}
	httpx.JSON(w, r, http.StatusOK, map[string]any{"data": purposes})
}

// withdrawals reports how many people withdrew consent in a window.
//
// The compliance screen showed a dash here and said so plainly, because zero
// and "nobody counted" lead to opposite conclusions: a purpose nobody is
// withdrawing from and a purpose nobody has measured look identical if the
// number is invented.
func (h *Handler) withdrawals(w http.ResponseWriter, r *http.Request) {
	actor, ok := authn.ActorFrom(r.Context())
	if !ok || !actor.Can(authn.CapConsentManage) {
		httpx.Error(w, r, http.StatusForbidden, "forbidden", "Insufficient permissions")
		return
	}

	days := 30
	if raw := r.URL.Query().Get("days"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 365 {
			httpx.ErrorWithFields(w, r, http.StatusUnprocessableEntity, "validation_failed",
				"Invalid request", map[string]any{"days": "must be between 1 and 365"})
			return
		}
		days = n
	}

	since := time.Now().UTC().AddDate(0, 0, -days)
	total, byPurpose, err := h.store.WithdrawalCount(r.Context(), actor.TenantID, since)
	if err != nil {
		httpx.Logger(r.Context()).Error("counting withdrawals", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "Internal server error")
		return
	}

	httpx.JSON(w, r, http.StatusOK, map[string]any{
		"days":       days,
		"since":      since,
		"total":      total,
		"by_purpose": byPurpose,
	})
}
