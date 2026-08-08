// Package webpages serves the pages a member of the public actually sees: the
// form they fill in, the portal they land on from a magic link, the consent text
// they agreed to, and the four ways a link can be dead.
//
// These are Go templates, not React, and that is the whole point. The admin
// interface is 89.5 KB gzip behind a login, where the cost is paid once by
// someone who is at work. The form page is 3.3 KB and is paid for by a customer
// standing in a shop on a phone -- and it is the denominator of the completion
// rate this product reports on. Shipping a framework here would mean every
// respondent downloads the form builder's runtime to answer six questions, and
// the number the product is judged by would move because of it.
//
// The package knows nothing about any module. Every page takes a view struct
// defined here, supplied by an adapter in the composition root, which keeps the
// module boundaries in internal/arch intact and keeps these templates testable
// without a database.
package webpages

import (
	"bytes"
	"context"
	"embed"
	"errors"
	"html/template"
	"io/fs"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/collectr/collectr/internal/platform/httpx"
)

//go:embed templates/*.html assets/public.css
var files embed.FS

// Errors a source returns to select which dead-link page is rendered.
//
// They deliberately mirror what the link resolver already distinguishes, no
// more: "expired" and "withdrawn" are separate values here because the copy
// differs, but a source that cannot tell them apart should return ErrGone and
// get the neutral wording.
var (
	// ErrNotFound means no such thing was ever issued.
	ErrNotFound = errors.New("webpages: not found")
	// ErrGone means it existed and has been closed.
	ErrGone = errors.New("webpages: gone")
	// ErrWithdrawn means it was taken down on request. See the note on
	// StateWithdrawn before using it.
	ErrWithdrawn = errors.New("webpages: withdrawn")
	// ErrLegalHold means it is suspended under a legal hold.
	ErrLegalHold = errors.New("webpages: legal hold")
)

// FormSource supplies the server-rendered frame of a form page.
//
// It returns only the parts that must be readable with JavaScript off: the
// title, who is collecting, and what is being consented to. The questions
// themselves are the module's job.
type FormSource interface {
	PublicFormPage(ctx context.Context, publicID string) (FormPage, error)
}

// DocumentSource supplies one immutable consent document version.
type DocumentSource interface {
	ConsentDocument(ctx context.Context, id string) (Document, error)
}

// Config wires the handler. Every field is optional; a page whose source is
// missing degrades to the frame it can render from the request alone rather
// than failing, because a half-rendered consent notice is still better than a
// 500 for the person holding the phone.
type Config struct {
	// Forms and Documents are adapters over the forms and consent modules. See
	// the package README note in the handler's doc comment for the shape of the
	// adapter the composition root writes.
	Forms     FormSource
	Documents DocumentSource

	// Assets is the built asset tree (internal/webui/dist). The public form
	// module is written there by Vite under a content-hashed name, which is why
	// the tree has to be readable rather than the name hard-coded. Nil means no
	// module is attached and every page falls back to its no-JavaScript form.
	Assets fs.FS

	// Brand is the controller's short name, used where the page addresses the
	// visitor ("Dữ liệu của bạn tại Acme").
	Brand string

	// Support is the address a visitor can write to when the portal cannot open,
	// shown only in the no-JavaScript fallback. A page that says "this does not
	// work" without saying who to tell is a dead end.
	Support string

	// ResponseWindow is the statutory deadline for answering a data subject
	// request. Rendered as a promise the visitor can hold the controller to, so
	// it must match the SLA the DSR module actually schedules against.
	ResponseWindow time.Duration

	// PortalSession is how long a portal session lasts once the magic link is
	// exchanged.
	PortalSession time.Duration
}

// Handler serves the public pages.
type Handler struct {
	cfg   Config
	style template.CSS

	// One resolved URL per Vite entry. Kept separate rather than shared: loading
	// the form module on the portal would ship a rule engine to a page with no
	// form on it, on the same slow connection this package exists to protect.
	formScript   string
	portalScript string

	form     *template.Template
	portal   *template.Template
	document *template.Template
	dead     *template.Template
}

// New parses the templates and resolves the asset names once, at startup.
//
// It panics if a template is malformed: that is a build mistake, and a server
// that boots and then renders a broken consent notice to the public is strictly
// worse than one that refuses to start.
func New(cfg Config) *Handler {
	if cfg.ResponseWindow <= 0 {
		cfg.ResponseWindow = 30 * 24 * time.Hour
	}
	if cfg.PortalSession <= 0 {
		cfg.PortalSession = 30 * time.Minute
	}

	css, err := files.ReadFile("assets/public.css")
	if err != nil {
		panic("webpages: stylesheet not embedded: " + err.Error())
	}

	return &Handler{
		cfg: cfg,
		// Trusted: it is this package's own file, read out of the binary. Its
		// comments explain the decisions to whoever edits it next and are of no
		// use to a phone, so they are squeezed out once here rather than sent
		// down every connection.
		style:        template.CSS(squeeze(string(css))),
		formScript:   script(cfg.Assets, "form"),
		portalScript: script(cfg.Assets, "portal"),

		form:     page("form.html"),
		portal:   page("portal.html"),
		document: page("document.html"),
		dead:     page("deadlink.html"),
	}
}

// Register mounts the public pages.
//
// /dsr is registered without a trailing slash as well, because that is the
// exact URL the magic link in the email carries; a route that only answered
// /dsr/ would send every recipient to the admin single-page app instead.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /f/{public_id}", h.FormPage)
	mux.HandleFunc("GET /dsr", h.Portal)
	mux.HandleFunc("GET /dsr/{$}", h.Portal)
	mux.HandleFunc("GET /consent/{id}", h.ConsentDocument)
}

// page builds one template set: the layout plus exactly one page file, so the
// "main" block each page defines cannot collide with another's.
func page(name string) *template.Template {
	return template.Must(
		template.New("layout.html").Funcs(funcs).ParseFS(files, "templates/layout.html", "templates/"+name),
	)
}

var funcs = template.FuncMap{
	"vndate": vndate,
	"short":  short,
	"days":   days,
}

// vndate formats a date the way it is written in Vietnam.
func vndate(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format("02/01/2006")
}

// short abbreviates a digest for a line that has to fit on a phone. The full
// value is always printed somewhere on the page it belongs to; this is the
// glance version.
func short(hash string) string {
	hash = strings.TrimPrefix(hash, "sha256:")
	if len(hash) <= 12 {
		return hash
	}
	return hash[:8] + "…"
}

func days(d time.Duration) int {
	n := int(d.Hours() / 24)
	if n < 1 {
		return 1
	}
	return n
}

// script resolves the URL of a built entry chunk.
//
// Vite writes assets/<entry>-<hash>.js and the hash changes with every build,
// so the name cannot be spelled out in a template. When the tree is missing the
// result is empty and no script tag is emitted at all -- a visitor then sees
// exactly what a visitor with JavaScript disabled sees, which is a state these
// pages are written to survive, rather than a 404 in the console and a blank
// container.
func script(assets fs.FS, entry string) string {
	if assets == nil {
		return ""
	}
	matches, err := fs.Glob(assets, "assets/"+entry+"-*.js")
	if err != nil || len(matches) == 0 {
		return ""
	}
	sort.Strings(matches)
	return "/" + matches[0]
}

// squeeze removes comments and collapses whitespace in the stylesheet.
//
// Deliberately the least clever thing that works: it tracks quoted strings so
// that a selector like a[href^="http"] survives, and otherwise treats every run
// of whitespace as one space. It does not reorder, rename or shorten anything.
// A stylesheet that renders wrong on somebody's consent form is a far worse
// outcome than a kilobyte, so this stays a squeezer and never becomes a
// minifier.
func squeeze(css string) string {
	var b strings.Builder
	b.Grow(len(css))

	var quote byte
	space := false

	for i := 0; i < len(css); i++ {
		c := css[i]

		if quote != 0 {
			b.WriteByte(c)
			if c == '\\' && i+1 < len(css) {
				i++
				b.WriteByte(css[i])
				continue
			}
			if c == quote {
				quote = 0
			}
			continue
		}

		switch {
		case c == '"' || c == '\'':
			if space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			space = false
			quote = c
			b.WriteByte(c)
		case c == '/' && i+1 < len(css) && css[i+1] == '*':
			end := strings.Index(css[i+2:], "*/")
			if end < 0 {
				i = len(css)
			} else {
				i += end + 3
			}
			// A removed comment separates tokens exactly as whitespace does.
			space = true
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			space = true
		default:
			if space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			space = false
			b.WriteByte(c)
		}
	}
	return strings.TrimSpace(b.String())
}

// chrome is the part of every page that does not depend on which page it is.
type chrome struct {
	PageTitle string
	Script    string
	Style     template.CSS
	Brand     string
}

func (h *Handler) chrome(title, script string) chrome {
	return chrome{PageTitle: title, Script: script, Style: h.style, Brand: h.cfg.Brand}
}

// Content security policies.
//
// Set here as well as in deploy/Caddyfile, and kept character-for-character
// identical to it. Two places is not duplication by accident: the reverse proxy
// is the one an operator can misconfigure, and the binary is the one that must
// still be safe when somebody runs it without a proxy in front.
const (
	// appCSP covers the pages that run this project's own module.
	appCSP = "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; " +
		"script-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; " +
		"form-action 'self'"

	// docCSP covers the consent permalink, which runs no script at all.
	//
	// The document body is HTML written by a tenant administrator and injected
	// unescaped -- it has to be, it is a formatted legal text. script-src is
	// absent from a 'none' default, so if that editor is ever compromised the
	// worst it can produce here is ugly typography.
	docCSP = "default-src 'none'; img-src 'self' data:; style-src 'unsafe-inline'; " +
		"font-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'"
)

// Caching.
const (
	// noStore is used for the form page and the portal. A form link that is
	// withdrawn has to be dead on the next tap, not after a cache entry expires,
	// and the portal must never survive in the cache of a shared phone.
	noStore = "no-store"

	// immutable is used for the consent permalink, which is addressed by version
	// and therefore can never change.
	immutable = "public, max-age=31536000, immutable"
)

// render writes a page, or a plain fallback if the template fails.
//
// The page is built into a buffer first. Writing straight to the response would
// mean a template error arrives after a 200 and half a document, which a browser
// renders as a broken page rather than an error.
func (h *Handler) render(w http.ResponseWriter, r *http.Request, t *template.Template, status int, csp, cache string, data any) {
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "layout", data); err != nil {
		httpx.Logger(r.Context()).Error("rendering public page", "error", err, "path", r.URL.Path)
		header(w, appCSP, noStore)
		http.Error(w, "Không hiển thị được trang này. Vui lòng thử lại.", http.StatusInternalServerError)
		return
	}

	header(w, csp, cache)
	w.WriteHeader(status)
	if r.Method == http.MethodHead {
		return
	}
	if _, err := buf.WriteTo(w); err != nil {
		httpx.Logger(r.Context()).Warn("writing public page", "error", err, "path", r.URL.Path)
	}
}

func header(w http.ResponseWriter, csp, cache string) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Security-Policy", csp)
	h.Set("Cache-Control", cache)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	// A form URL can name the campaign that led someone here, which for a health
	// questionnaire is itself personal data. It must not travel onward.
	h.Set("Referrer-Policy", "no-referrer")
}
