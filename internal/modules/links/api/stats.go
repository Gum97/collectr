package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/collectr/collectr/internal/contracts"
	"github.com/collectr/collectr/internal/modules/links/domain"
	"github.com/collectr/collectr/internal/platform/authn"
	"github.com/collectr/collectr/internal/platform/httpx"
)

// RegisterStats mounts the reporting routes.
//
// A shortener without these is write-only: every click is recorded and none of
// it can be read back, which is the same as not recording it.
func (h *Handler) RegisterStats(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/links/stats", h.topLinks)
	mux.HandleFunc("GET /api/v1/links/{id}/stats", h.linkStats)
}

func (h *Handler) linkStats(w http.ResponseWriter, r *http.Request) {
	actor, ok := authn.ActorFrom(r.Context())
	if !ok {
		httpx.Error(w, r, http.StatusUnauthorized, "unauthenticated", "Authentication required")
		return
	}
	if !actor.Can(authn.CapAnalyticsRead) {
		httpx.Error(w, r, http.StatusForbidden, "forbidden", "Insufficient permissions")
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.Error(w, r, http.StatusNotFound, "not_found", "Link not found")
		return
	}

	// Resolved first so an unknown id is a 404 rather than a report full of
	// zeros, which reads as a link nobody clicked.
	link, err := h.svc.Get(r.Context(), actor.TenantID, id)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		httpx.Error(w, r, http.StatusNotFound, "not_found", "Link not found")
		return
	case err != nil:
		httpx.Logger(r.Context()).Error("loading link", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "Internal server error")
		return
	}

	from, to, bucket, ok := h.window(w, r)
	if !ok {
		return
	}

	stats, err := h.reports.LinkStats(r.Context(), actor.TenantID, id, from, to, bucket, h.rawRetention)
	if err != nil {
		httpx.Logger(r.Context()).Error("reading link stats", "error", err, "link_id", id)
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "Internal server error")
		return
	}

	points := make([]map[string]any, 0, len(stats.Points))
	for _, p := range stats.Points {
		points = append(points, map[string]any{"bucket": p.Bucket, "clicks": p.Clicks})
	}

	httpx.JSON(w, r, http.StatusOK, map[string]any{
		"link": map[string]any{
			"id": link.ID, "code": link.Code,
			"short_url": h.scheme + "://" + link.Host + "/r/" + link.Code,
			"status":    link.Status,
		},
		"from": from, "to": to,
		"clicks": stats.Clicks,
		// Reported next to the ratios that are taken against it, so a reader can
		// check them. It is smaller than "clicks" whenever the window reaches
		// past the raw event retention.
		"breakdown_clicks": stats.BreakdownClicks,
		// Networks, never "visitors": nothing here can recognise a returning
		// person, and naming it after people would invite a reader to divide by
		// it and call the result a unique-visitor count.
		"networks":           stats.Networks,
		"clicks_per_network": round(stats.ClicksPerNetwork()),
		"qr_share":           round(stats.QRShare()),
		"first_click":        stats.FirstClick,
		"last_click":         stats.LastClick,
		"points":             points,
		"sources":            present(stats.Sources),
		"referrers":          present(stats.Referrers),
		"browsers":           present(stats.Browsers),
		"utm_sources":        present(stats.UTMSources),
		"utm_mediums":        present(stats.UTMMediums),
		"utm_campaigns":      present(stats.UTMCampaigns),
		"breakdown_at":       stats.BreakdownFrom,
		// Stated, not implied: the series comes from rollups that outlive the raw
		// events the breakdowns are computed from. Without this the tables below
		// the chart look like a drop in traffic rather than a shorter window.
		"breakdown_note": "Phân tích nguồn chỉ tính từ " +
			stats.BreakdownFrom.Format(time.DateOnly) +
			", theo hạn lưu sự kiện thô. Biểu đồ và tổng lượt bấm không bị giới hạn này.",
	})
}

func (h *Handler) topLinks(w http.ResponseWriter, r *http.Request) {
	actor, ok := authn.ActorFrom(r.Context())
	if !ok {
		httpx.Error(w, r, http.StatusUnauthorized, "unauthenticated", "Authentication required")
		return
	}
	if !actor.Can(authn.CapAnalyticsRead) {
		httpx.Error(w, r, http.StatusForbidden, "forbidden", "Insufficient permissions")
		return
	}
	projectID, err := uuid.Parse(r.URL.Query().Get("project_id"))
	if err != nil {
		httpx.ErrorWithFields(w, r, http.StatusUnprocessableEntity, "validation_failed",
			"Invalid request", map[string]any{"project_id": "bắt buộc, phải là uuid"})
		return
	}
	if !actor.InProject(projectID) {
		httpx.Error(w, r, http.StatusForbidden, "forbidden", "Insufficient permissions")
		return
	}

	from, to, _, ok := h.window(w, r)
	if !ok {
		return
	}

	rows, err := h.reports.TopLinks(r.Context(), actor.TenantID, projectID, from, to, 50)
	if err != nil {
		httpx.Logger(r.Context()).Error("reading top links", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "Internal server error")
		return
	}

	out := make([]map[string]any, 0, len(rows))
	for _, s := range rows {
		row := map[string]any{
			"link_id": s.LinkID, "clicks": s.Clicks, "last_seen": s.LastSeen,
		}
		// Only meaningful for links that point at a form; for the rest a
		// conversion rate would be a ratio against something that cannot happen.
		if s.Submits > 0 {
			row["submits"] = s.Submits
			row["conversion_rate"] = round(float64(s.Submits) / float64(s.Clicks))
		}
		out = append(out, row)
	}
	httpx.JSON(w, r, http.StatusOK, map[string]any{"from": from, "to": to, "data": out})
}

// window parses the reporting period.
//
// Defaults to the last 30 days by day. The upper bound on the range is not
// politeness: each breakdown is a GROUP BY over the raw events in the window.
func (h *Handler) window(w http.ResponseWriter, r *http.Request) (from, to time.Time, bucket time.Duration, ok bool) {
	to = time.Now().UTC()
	from = to.AddDate(0, 0, -30)
	bucket = 24 * time.Hour

	q := r.URL.Query()
	if raw := q.Get("from"); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			httpx.ErrorWithFields(w, r, http.StatusUnprocessableEntity, "validation_failed",
				"Invalid request", map[string]any{"from": "phải theo định dạng RFC3339"})
			return
		}
		from = t.UTC()
	}
	if raw := q.Get("to"); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			httpx.ErrorWithFields(w, r, http.StatusUnprocessableEntity, "validation_failed",
				"Invalid request", map[string]any{"to": "phải theo định dạng RFC3339"})
			return
		}
		to = t.UTC()
	}
	switch q.Get("bucket") {
	case "hour":
		bucket = time.Hour
	case "week":
		bucket = 7 * 24 * time.Hour
	case "", "day":
	default:
		httpx.ErrorWithFields(w, r, http.StatusUnprocessableEntity, "validation_failed",
			"Invalid request", map[string]any{"bucket": "phải là hour, day hoặc week"})
		return
	}

	if !from.Before(to) {
		httpx.ErrorWithFields(w, r, http.StatusUnprocessableEntity, "validation_failed",
			"Invalid request", map[string]any{"from": "phải trước 'to'"})
		return
	}
	if to.Sub(from) > 366*24*time.Hour {
		httpx.ErrorWithFields(w, r, http.StatusUnprocessableEntity, "validation_failed",
			"Invalid request", map[string]any{"from": "khoảng thời gian tối đa là 366 ngày"})
		return
	}
	return from, to, bucket, true
}

func present(bs []contracts.Breakdown) []map[string]any {
	out := make([]map[string]any, 0, len(bs))
	for _, b := range bs {
		out = append(out, map[string]any{
			"key": b.Key, "clicks": b.Clicks, "networks": b.Networks,
		})
	}
	return out
}

// round trims a rate to three decimals. Reporting a conversion rate to fifteen
// significant figures implies a precision the sample does not have.
func round(v float64) float64 {
	return float64(int(v*1000+0.5)) / 1000
}
