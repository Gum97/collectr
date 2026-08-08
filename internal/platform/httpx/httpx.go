// Package httpx holds the HTTP plumbing shared by every module: problem+json
// responses, request correlation, panic recovery and access logging.
package httpx

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"
)

type ctxKey int

const (
	ctxKeyTraceID ctxKey = iota
	ctxKeyLogger
)

// Problem is an RFC 7807 error body. Detail must never carry internal error
// strings: those go to the log, keyed by the same trace id the client sees.
type Problem struct {
	Type    string         `json:"type,omitempty"`
	Title   string         `json:"title"`
	Status  int            `json:"status"`
	Detail  string         `json:"detail,omitempty"`
	Code    string         `json:"code,omitempty"`
	Fields  map[string]any `json:"fields,omitempty"`
	TraceID string         `json:"trace_id,omitempty"`
}

// JSON writes v as a JSON response with the given status code.
func JSON(w http.ResponseWriter, r *http.Request, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// The status line is already out; all we can do is record it.
		Logger(r.Context()).Error("encoding response", "error", err)
	}
}

// Error writes an RFC 7807 problem document and stamps it with the trace id.
func Error(w http.ResponseWriter, r *http.Request, status int, code, title string) {
	ErrorWithFields(w, r, status, code, title, nil)
}

// ErrorWithFields writes a problem document carrying per-field validation detail.
func ErrorWithFields(w http.ResponseWriter, r *http.Request, status int, code, title string, fields map[string]any) {
	w.Header().Set("Content-Type", "application/problem+json; charset=utf-8")
	w.WriteHeader(status)
	p := Problem{
		Title:   title,
		Status:  status,
		Code:    code,
		Fields:  fields,
		TraceID: TraceID(r.Context()),
	}
	if err := json.NewEncoder(w).Encode(p); err != nil {
		Logger(r.Context()).Error("encoding problem", "error", err)
	}
}

// DecodeJSON reads a JSON body of at most maxBytes into dst.
func DecodeJSON(w http.ResponseWriter, r *http.Request, dst any, maxBytes int64) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}

// TraceID returns the correlation id attached to ctx, or "" if there is none.
func TraceID(ctx context.Context) string {
	id, _ := ctx.Value(ctxKeyTraceID).(string)
	return id
}

// Logger returns the request-scoped logger, falling back to the default one.
func Logger(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(ctxKeyLogger).(*slog.Logger); ok {
		return l
	}
	return slog.Default()
}

// Trace assigns each request a correlation id (honouring an inbound one) and binds a
// logger carrying it, so every line from one request stitches into one story.
func Trace(base *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := r.Header.Get("X-Request-Id")
			if id == "" || len(id) > 64 {
				id = ulid.Make().String()
			}
			ctx := context.WithValue(r.Context(), ctxKeyTraceID, id)
			ctx = context.WithValue(ctx, ctxKeyLogger, base.With("trace_id", id))
			w.Header().Set("X-Request-Id", id)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// Recover turns a panicking handler into a 500 instead of killing the process.
func Recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				Logger(r.Context()).Error("panic in handler",
					"panic", rec,
					"stack", string(debug.Stack()),
					"path", r.URL.Path,
				)
				Error(w, r, http.StatusInternalServerError, "internal_error", "Internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// statusRecorder captures the status code for access logging.
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	n, err := s.ResponseWriter.Write(b)
	s.bytes += n
	return n, err
}

// AccessLog records one structured line per request. It deliberately logs neither
// query strings nor bodies: both routinely carry personal data.
func AccessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		if rec.status == 0 {
			rec.status = http.StatusOK
		}
		Logger(r.Context()).Info("http_request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"bytes", rec.bytes,
			"duration_ms", time.Since(start).Milliseconds(),
			"ip_prefix", IPPrefix(r),
		)
	})
}

// trustedProxyHops is how many reverse proxies sit in front of this process.
//
// Default 1: the shipped compose file puts Caddy in front. Set to 0 when the
// binary is exposed directly, which makes X-Forwarded-For ignored entirely.
var trustedProxyHops = 1

// SetTrustedProxyHops configures how much of X-Forwarded-For is believed. Call
// once at startup, before serving.
func SetTrustedProxyHops(n int) {
	if n < 0 {
		n = 0
	}
	trustedProxyHops = n
}

// ClientIP extracts the caller address.
//
// It reads X-Forwarded-For from the RIGHT, not the left. A proxy appends the
// address it saw to whatever the client sent, so the leftmost entry is supplied
// by the caller and the rightmost entries are the ones each trusted hop wrote.
// Reading the first entry -- which this did -- let anyone choose their own
// address by sending the header themselves.
//
// That is not only a rate-limit bypass. IPPrefix feeds the consent record and
// the audit trail, so a forged header put an attacker-chosen address into the
// evidence of where a consent decision came from.
func ClientIP(r *http.Request) net.IP {
	peer := socketIP(r)

	// The header is only believed when the connection carrying it came from the
	// reverse proxy rather than from the internet. Reading from the right end is
	// not enough on its own: a caller reaching the process directly sends a
	// header with one entry, and "the last entry" is then still their own.
	//
	// Private and loopback peers stand in for "the proxy", which is what the
	// shipped compose file arranges -- only Caddy publishes a port. Set
	// TRUSTED_PROXY_HOPS=0 when the binary is exposed directly on a network
	// where that assumption does not hold.
	if trustedProxyHops > 0 && isTrustedPeer(peer) {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			// The hop closest to us wrote the last entry; each further trusted
			// hop wrote the one before it.
			if i := len(parts) - trustedProxyHops; i >= 0 && i < len(parts) {
				if ip := net.ParseIP(strings.TrimSpace(parts[i])); ip != nil {
					return ip
				}
			}
		}
	}
	return peer
}

func socketIP(r *http.Request) net.IP {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return net.ParseIP(host)
}

// isTrustedPeer reports whether the immediate connection could be the local
// reverse proxy. A request arriving straight from a public address never gets
// to name its own origin.
func isTrustedPeer(ip net.IP) bool {
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()
}

// IPPrefix reduces the caller address to a /24 (or /48 for IPv6) before it is
// stored or logged, shrinking what counts as personal data to the minimum that
// still supports abuse investigation and coarse geography.
func IPPrefix(r *http.Request) string {
	ip := ClientIP(r)
	if ip == nil {
		return ""
	}
	if v4 := ip.To4(); v4 != nil {
		return ip.Mask(net.CIDRMask(24, 32)).String() + "/24"
	}
	return ip.Mask(net.CIDRMask(48, 128)).String() + "/48"
}
