package webpages

import (
	"net/http"
	"time"
)

type portalData struct {
	chrome
	Support        string
	ResponseDays   int
	SessionMinutes int
}

// Portal serves /dsr, the page a magic link from an email lands on.
//
// This page is a frame and nothing else, and that is forced by the session
// design rather than chosen: the portal cookie is issued with Path=/api/dsr, so
// the browser does not send it to /dsr at all. The server therefore cannot know
// who is looking, which is exactly the property that makes a forwarded link
// harmless to render. The records, the consents and the buttons are fetched by
// the client module against /api/dsr/me/*, where the cookie does travel.
//
// What the server does render is the part that must be true whether or not any
// of that works: what each right means, and what it costs. Erasure in
// particular -- the key is destroyed, so the backups go with it -- must be on
// the page before anyone taps anything, not in a confirmation dialog drawn by a
// script that may not have loaded.
func (h *Handler) Portal(w http.ResponseWriter, r *http.Request) {
	title := "Dữ liệu của bạn"
	if h.cfg.Brand != "" {
		title += " tại " + h.cfg.Brand
	}

	data := portalData{
		chrome:         h.chrome(title, h.portalScript),
		Support:        h.cfg.Support,
		ResponseDays:   days(h.cfg.ResponseWindow),
		SessionMinutes: int(h.cfg.PortalSession / time.Minute),
	}
	h.render(w, r, h.portal, http.StatusOK, appCSP, noStore, data)
}
