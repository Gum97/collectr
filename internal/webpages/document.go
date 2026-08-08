package webpages

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/collectr/collectr/internal/platform/httpx"
)

type documentData struct {
	chrome
	Doc Document
	// URL is the page's own address, printed at the top so a copy on paper says
	// where it came from. Paper cannot be clicked, and this page's whole purpose
	// is to survive being printed.
	URL string
}

// ConsentDocument serves /consent/{id}, the permalink to one immutable version
// of a consent text.
//
// This is the page a person is sent to before they tick a box, and the page
// their consent record points at afterwards. Those have to be the same bytes:
// a notice that can be edited later cannot show what somebody was told at the
// moment they agreed, which is the only thing the record needs to prove. The
// version number, the publication date and the sha256 are all on the page so
// that anyone holding the record can check by hand that the two match.
//
// It carries no script. Not "no script yet" -- none, ever: a legal document
// whose text depends on code having run is a legal document with a condition
// attached.
func (h *Handler) ConsentDocument(w http.ResponseWriter, r *http.Request) {
	if h.cfg.Documents == nil {
		httpx.Logger(r.Context()).Error("consent permalink requested with no document source configured")
		h.DeadLink(w, r, StateUnavailable)
		return
	}

	id := r.PathValue("id")
	doc, err := h.cfg.Documents.ConsentDocument(r.Context(), id)
	switch {
	case errors.Is(err, ErrNotFound):
		h.DeadLink(w, r, StateMissing)
		return
	case err != nil:
		httpx.Logger(r.Context()).Error("reading consent document", "error", err, "id", id)
		h.DeadLink(w, r, StateUnavailable)
		return
	}

	if doc.Title == "" {
		doc.Title = "Văn bản đồng ý"
	}

	data := documentData{
		chrome: h.chrome(doc.Title+" · bản v"+strconv.Itoa(doc.Version), ""),
		Doc:    doc,
		URL:    absolute(r),
	}
	h.render(w, r, h.document, http.StatusOK, docCSP, immutable, data)
}

// absolute rebuilds the request's own URL.
//
// X-Forwarded-Host is honoured because the deployment runs behind Caddy and the
// public hostname is the one printed on the page; the scheme is https unless the
// connection really was plaintext, which in practice is only local development.
func absolute(r *http.Request) string {
	host := r.Header.Get("X-Forwarded-Host")
	if host == "" {
		host = r.Host
	}
	scheme := "https"
	if r.TLS == nil && r.Header.Get("X-Forwarded-Proto") == "" {
		if isLocal(host) {
			scheme = "http"
		}
	}
	if p := r.Header.Get("X-Forwarded-Proto"); p == "http" {
		scheme = "http"
	}
	return scheme + "://" + host + r.URL.Path
}

func isLocal(host string) bool {
	for _, prefix := range []string{"localhost", "127.0.0.1", "[::1]"} {
		if strings.HasPrefix(host, prefix) {
			return true
		}
	}
	return false
}
