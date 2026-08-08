// Package api receives funnel events from the browser.
//
// Without this endpoint the funnel only ever holds clicks and submissions, so
// the completion rate divides by zero views and reports 0% for a form that is
// working perfectly well. A number that is always wrong is worse than no number.
package api

import (
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/collectr/collectr/internal/contracts"
	"github.com/collectr/collectr/internal/platform/httpx"
	"github.com/collectr/collectr/internal/platform/signing"
)

// maxBatch caps how many events one request may carry.
const maxBatch = 20

// Handler serves the beacon endpoint.
type Handler struct {
	events  contracts.EventCollector
	forms   contracts.FormLocator
	signer  *signing.Signer
	maxBody int64
}

// New returns a Handler.
func New(events contracts.EventCollector, forms contracts.FormLocator, signer *signing.Signer) *Handler {
	return &Handler{events: events, forms: forms, signer: signer, maxBody: 16 << 10}
}

// Register mounts the public route.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/pub/events", h.ingest)
}

type beaconRequest struct {
	FormID string `json:"form_id"`
	Events []struct {
		EventID string    `json:"event_id"`
		Type    string    `json:"type"`
		PageID  string    `json:"page_id"`
		Token   string    `json:"visit_token"`
		At      time.Time `json:"occurred_at"`
	} `json:"events"`
}

// ingest records a batch of interaction events.
//
// Always 202, whatever happens. This is a beacon fired from a page the
// respondent is still using: an error here would surface as a broken form for a
// measurement nobody asked them to provide.
func (h *Handler) ingest(w http.ResponseWriter, r *http.Request) {
	var req beaconRequest
	if err := httpx.DecodeJSON(w, r, &req, h.maxBody); err != nil {
		httpx.JSON(w, r, http.StatusAccepted, accepted())
		return
	}
	if req.FormID == "" || len(req.Events) == 0 {
		httpx.JSON(w, r, http.StatusAccepted, accepted())
		return
	}

	ref, err := h.forms.LocatePublicForm(r.Context(), req.FormID)
	if err != nil {
		// An unknown form is not reported as such: the endpoint is unauthenticated,
		// and telling a caller which public ids exist is a disclosure.
		httpx.JSON(w, r, http.StatusAccepted, accepted())
		return
	}

	now := time.Now().UTC()
	for i, e := range req.Events {
		if i >= maxBatch {
			break
		}
		if !clientReportable(e.Type) {
			continue
		}
		// A client-supplied id is what makes a retried beacon collapse into one
		// row instead of inflating the funnel.
		if e.EventID == "" {
			continue
		}

		at := e.At
		// Clock skew is normal on phones. A timestamp far outside the plausible
		// window would land in the wrong rollup bucket, or in a partition that
		// does not exist yet, so it is replaced rather than trusted.
		if at.IsZero() || at.After(now.Add(5*time.Minute)) || at.Before(now.Add(-24*time.Hour)) {
			at = now
		}

		h.events.Collect(r.Context(), contracts.Event{
			EventID:       e.EventID,
			TenantID:      ref.TenantID,
			Type:          e.Type,
			FormID:        &ref.FormID,
			FormVersionID: &ref.VersionID,
			PageID:        e.PageID,
			VisitID:       h.visitID(e.Token, now),
			OccurredAt:    at,
			Meta: map[string]any{
				"ip_prefix": httpx.IPPrefix(r),
			},
		})
	}

	httpx.JSON(w, r, http.StatusAccepted, accepted())
}

// clientReportable lists the events a browser may claim.
//
// Deliberately narrow. Clicks are recorded by the redirect and submissions by
// the submit handler, both server-side; accepting them here would let anyone
// inflate another tenant's conversion figures with a curl loop.
func clientReportable(t string) bool {
	switch t {
	case contracts.EventFormStart, contracts.EventFormPageView:
		return true
	default:
		return false
	}
}

// visitID recovers the funnel visit from its token. A missing or expired token
// costs attribution, never the event.
func (h *Handler) visitID(token string, now time.Time) *uuid.UUID {
	if token == "" {
		return nil
	}
	v, err := h.signer.Verify(token, now)
	if err != nil {
		return nil
	}
	return &v.VisitID
}

func accepted() map[string]string {
	return map[string]string{"status": "accepted"}
}
