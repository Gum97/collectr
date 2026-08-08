package api

import (
	"encoding/hex"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/collectr/collectr/internal/contracts"
	"github.com/collectr/collectr/internal/modules/forms/app"
	"github.com/collectr/collectr/internal/modules/forms/domain"
	"github.com/collectr/collectr/internal/modules/forms/store"
	"github.com/collectr/collectr/internal/platform/httpx"
	"github.com/collectr/collectr/internal/platform/signing"
)

// maxSubmissionBytes bounds a response body. Answers are text; anything larger is
// either a mistake or an attempt to exhaust memory.
const maxSubmissionBytes = 256 << 10

// SubmitHandler serves the public submission endpoint.
type SubmitHandler struct {
	submitter *app.Submitter
	inserter  app.SubmissionInserter
	signer    *signing.Signer
}

// NewSubmitHandler returns a SubmitHandler.
func NewSubmitHandler(submitter *app.Submitter, inserter app.SubmissionInserter, signer *signing.Signer) *SubmitHandler {
	return &SubmitHandler{submitter: submitter, inserter: inserter, signer: signer}
}

// Register mounts the submission route.
func (h *SubmitHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/pub/forms/{public_id}/submissions", h.submit)
}

type submitRequest struct {
	FormVersionID string         `json:"form_version_id"`
	VisitToken    string         `json:"visit_token"`
	Answers       map[string]any `json:"answers"`
	Consents      []struct {
		Purpose string `json:"purpose"`
		Granted bool   `json:"granted"`
	} `json:"consents"`
	ConsentProof struct {
		DocumentID   string `json:"document_id"`
		RenderedHash string `json:"rendered_hash"`
	} `json:"consent_proof"`
	ClientTime time.Time `json:"client_time"`
	Locale     string    `json:"locale"`
}

func (h *SubmitHandler) submit(w http.ResponseWriter, r *http.Request) {
	var req submitRequest
	if err := httpx.DecodeJSON(w, r, &req, maxSubmissionBytes); err != nil {
		httpx.Error(w, r, http.StatusBadRequest, "invalid_body", "Request body is not valid JSON")
		return
	}

	versionID, err := uuid.Parse(req.FormVersionID)
	if err != nil {
		httpx.ErrorWithFields(w, r, http.StatusUnprocessableEntity, "validation_failed",
			"Invalid request", map[string]any{"form_version_id": "must be a uuid"})
		return
	}
	documentID, err := uuid.Parse(req.ConsentProof.DocumentID)
	if err != nil {
		httpx.ErrorWithFields(w, r, http.StatusUnprocessableEntity, "validation_failed",
			"Invalid request", map[string]any{"consent_proof.document_id": "must be a uuid"})
		return
	}
	renderedHash, err := hex.DecodeString(trimHashPrefix(req.ConsentProof.RenderedHash))
	if err != nil {
		httpx.ErrorWithFields(w, r, http.StatusUnprocessableEntity, "validation_failed",
			"Invalid request", map[string]any{"consent_proof.rendered_hash": "must be a hex sha256 digest"})
		return
	}

	grants := make([]contracts.ConsentGrant, 0, len(req.Consents))
	for _, c := range req.Consents {
		grants = append(grants, contracts.ConsentGrant{PurposeCode: c.Purpose, Granted: c.Granted})
	}

	in := app.SubmitInput{
		PublicID:       r.PathValue("public_id"),
		IdempotencyKey: r.Header.Get("Idempotency-Key"),
		FormVersionID:  versionID,
		Answers:        req.Answers,
		Consents:       grants,
		DocumentID:     documentID,
		RenderedHash:   renderedHash,
		VisitID:        h.visitID(req.VisitToken),
		Evidence: contracts.ConsentEvidence{
			RenderedHash: renderedHash,
			Method:       "checkbox",
			// Only the network prefix, never the full address: the evidence needs
			// to show roughly where and when, not to identify a household.
			IPPrefix:   httpx.IPPrefix(r),
			UserAgent:  r.UserAgent(),
			Locale:     req.Locale,
			ClientTime: req.ClientTime,
		},
	}

	res, err := h.submitter.Submit(r.Context(), h.inserter, in)
	switch {
	case err == nil:
	case errors.Is(err, domain.ErrFormNotFound), errors.Is(err, domain.ErrVersionRetired):
		httpx.Error(w, r, http.StatusNotFound, "not_found", "Form not found")
		return
	case errors.Is(err, app.ErrVersionMismatch):
		// 409 with the current version, so a respondent who had the page open
		// while it was republished can be re-rendered rather than rejected.
		httpx.JSON(w, r, http.StatusConflict, map[string]any{
			"error":   "form_version_retired",
			"message": "This form was updated while you were filling it in.",
		})
		return
	case errors.Is(err, app.ErrConsentMismatch):
		// The one thing that must never be waved through: if the text on screen
		// was not the text on file, there is no provable consent.
		httpx.JSON(w, r, http.StatusConflict, map[string]any{
			"error":   "consent_document_changed",
			"message": "The consent text changed while you were filling this in. Please review it again.",
		})
		return
	case errors.Is(err, app.ErrConsentMissing):
		httpx.Error(w, r, http.StatusUnprocessableEntity, "consent_required",
			"A required processing purpose was not agreed to")
		return
	case errors.Is(err, app.ErrDuplicateRequest):
		httpx.Error(w, r, http.StatusConflict, "duplicate_request",
			"This submission was already received")
		return
	case errors.Is(err, app.ErrNoIdentifier):
		httpx.Logger(r.Context()).Error("form has no identifier field", "public_id", in.PublicID)
		httpx.Error(w, r, http.StatusInternalServerError, "form_misconfigured",
			"This form cannot accept responses")
		return
	default:
		var fieldErrs app.FieldErrors
		if errors.As(err, &fieldErrs) {
			fields := make(map[string]any, len(fieldErrs))
			for k, v := range fieldErrs {
				fields[k] = v
			}
			httpx.ErrorWithFields(w, r, http.StatusUnprocessableEntity, "validation_failed",
				"Some answers need attention", fields)
			return
		}
		httpx.Logger(r.Context()).Error("submitting response", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "Internal server error")
		return
	}

	httpx.JSON(w, r, http.StatusCreated, map[string]any{
		"submission_id": res.SubmissionID,
		"receipt_token": res.ReceiptToken,
	})
}

// visitID recovers the funnel visit id from the token, if it is still valid.
// A missing or expired token costs attribution, never the submission.
func (h *SubmitHandler) visitID(token string) *uuid.UUID {
	if token == "" {
		return nil
	}
	v, err := h.signer.Verify(token, time.Now())
	if err != nil {
		return nil
	}
	return &v.VisitID
}

func trimHashPrefix(s string) string {
	if len(s) > 7 && s[:7] == "sha256:" {
		return s[7:]
	}
	return s
}

// compile-time check that the store satisfies the inserter the submitter needs.
var _ app.SubmissionInserter = (*store.Store)(nil)
