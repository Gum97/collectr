// Package webui serves the built admin interface from inside the binary.
//
// Embedded rather than mounted from a volume or served by a second container:
// this deployment ships as one image, and an operator who upgrades the server
// but not the assets gets a screen that looks fine and calls endpoints that no
// longer exist. One artefact cannot drift from itself.
//
// Same-origin also matters more than it looks. The admin session is a cookie; a
// separate asset origin would mean CORS, and CORS with credentials means
// loosening SameSite -- which is the thing protecting that session. Teams end up
// moving tokens into localStorage to escape that corner, and that is strictly
// worse.
package webui

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// dist holds the Vite output. Vite writes here directly (see web/vite.config.ts)
// so a local `go build` and the Docker build see the same tree.
//
//go:embed all:dist
var dist embed.FS

// Assets exposes the built tree.
//
// The public pages are rendered by Go but still load one hashed script, and the
// hash changes on every build. They cannot embed the directory themselves --
// go:embed does not reach outside its own package -- so the one package that
// does own it hands over a read-only view.
func Assets() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic("webui: dist not embedded: " + err.Error())
	}
	return sub
}

// Handler serves the built assets with an SPA fallback.
//
// devProxy, when non-empty, is the origin of a running Vite server; requests are
// passed to it instead. That keeps hot reload working without a second way to
// route the API, which is where dev and production setups usually start to
// disagree.
func Handler() http.Handler {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		// Only reachable if the embed directive above stops matching, which is a
		// build-time mistake rather than anything a running server can recover
		// from.
		panic("webui: dist not embedded: " + err.Error())
	}
	files := http.FileServerFS(sub)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")

		if f, err := sub.Open(name); err == nil {
			defer f.Close()
			if st, err := f.Stat(); err == nil && !st.IsDir() {
				setCaching(w, name)
				files.ServeHTTP(w, r)
				return
			}
		}

		// Anything else is a client route. Serving index.html for it is what makes
		// a deep link work on first load rather than only after in-app navigation.
		serveIndex(w, r, sub)
	})
}

// setCaching decides how long a file may be held.
//
// Hashed asset names are immutable by construction, so they get a year. Anything
// else -- fonts, icons, the odd static file -- gets a day: long enough to matter,
// short enough that a wrong answer corrects itself the same day.
func setCaching(w http.ResponseWriter, name string) {
	switch {
	case strings.HasPrefix(name, "assets/"):
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	case strings.HasPrefix(name, "fonts/"):
		w.Header().Set("Cache-Control", "public, max-age=2592000")
	default:
		w.Header().Set("Cache-Control", "public, max-age=86400")
	}
}

func serveIndex(w http.ResponseWriter, r *http.Request, sub fs.FS) {
	index, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		http.Error(w, "interface not built", http.StatusInternalServerError)
		return
	}
	// Never cached: it is the document that names the current asset hashes, so a
	// stale copy pins a browser to a build that has been deleted.
	// The admin app had no policy at all, which made it the one page in the
	// deployment where an injected script would run freely -- and the one that
	// carries the session cookie. Nothing here loads from another origin, so the
	// policy can be strict without a per-page exception.
	//
	// 'unsafe-inline' for styles only: Tailwind emits a stylesheet, but React
	// sets inline style attributes, and a nonce cannot cover those.
	w.Header().Set("Content-Security-Policy",
		"default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; "+
			"img-src 'self' data:; font-src 'self'; connect-src 'self'; "+
			"frame-ancestors 'none'; base-uri 'none'; form-action 'self'; "+
			"object-src 'none'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Referrer-Policy", "same-origin")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	_, _ = w.Write(index)
}
