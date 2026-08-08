// Package metrics exposes the numbers docs/07-operations.md sets thresholds on.
//
// Three layers, and the third is the one that earns its keep: system metrics say
// the machine is alive, RED metrics say the endpoints are answering, and the
// business metrics say whether the product is doing its job. Only the last kind
// catches "every response is 200 and every form is silently broken".
package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Registry holds every collector this process exports.
type Registry struct {
	reg *prometheus.Registry

	requestDuration *prometheus.HistogramVec
	requestsTotal   *prometheus.CounterVec

	SubmissionsTotal  *prometheus.CounterVec
	ConsentRecords    prometheus.Counter
	EventsDropped     prometheus.Counter
	CacheLookups      *prometheus.CounterVec
	AuditVerifyFailed prometheus.Counter
	OrphanFilesSwept  prometheus.Counter
	PublishBlocked    *prometheus.CounterVec
	ExportsTotal      *prometheus.CounterVec
	WebhookDeliveries *prometheus.CounterVec
	RateLimited       *prometheus.CounterVec
}

// New builds the registry.
func New() *Registry {
	reg := prometheus.NewRegistry()

	// Process and Go runtime metrics: free, and the "USE" layer for CPU and
	// memory comes from them.
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)

	m := &Registry{
		reg: reg,
		requestDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "collectr_http_request_duration_seconds",
			Help: "Request latency by route.",
			// Buckets chosen around the targets the design states: 80ms for the
			// redirect, 300ms for a render, 500ms for a submission. Default
			// buckets would put all three in the same bin and make the p99
			// thresholds unmeasurable.
			Buckets: []float64{
				0.001, 0.005, 0.01, 0.025, 0.05, 0.08,
				0.1, 0.2, 0.3, 0.5, 1, 2.5, 5, 10,
			},
		}, []string{"route", "method", "status"}),

		requestsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "collectr_http_requests_total",
			Help: "Requests by route and status class.",
		}, []string{"route", "method", "status_class"}),

		SubmissionsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "collectr_submissions_total",
			Help: "Form submissions accepted or rejected. The product's reason to exist: a sustained drop here is an outage even when every endpoint returns 200.",
		}, []string{"outcome"}),

		ConsentRecords: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "collectr_consent_records_written_total",
			Help: "Consent decisions recorded. Should track submissions; a divergence means the invariant that no response is stored without its lawful basis has broken.",
		}),

		EventsDropped: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "collectr_analytics_events_dropped_total",
			Help: "Funnel events discarded because Redis was unavailable and the local buffer was full. Analytics is best-effort by design, but sustained drops mean the funnel is lying.",
		}),

		CacheLookups: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "collectr_link_cache_lookups_total",
			Help: "Redirect cache hits and misses. A falling hit ratio is usually the first symptom of a hot key or a broken invalidation.",
		}, []string{"result"}),

		AuditVerifyFailed: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "collectr_audit_chain_verification_failed_total",
			Help: "Audit chain verifications that found a break. Any non-zero value means the trail was altered or forked.",
		}),

		OrphanFilesSwept: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "collectr_orphan_files_swept_total",
			Help: "Abandoned uploads deleted.",
		}),

		PublishBlocked: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "collectr_form_publish_blocked_total",
			Help: "Publish attempts refused by validation, by reason. Shows where people get stuck in the builder.",
		}, []string{"reason"}),

		ExportsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "collectr_exports_total",
			Help: "Bulk extracts of personal data, by outcome and whether sensitive columns were included.",
		}, []string{"outcome", "sensitive"}),

		WebhookDeliveries: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "collectr_webhook_deliveries_total",
			Help: "Webhook delivery attempts by outcome.",
		}, []string{"outcome"}),

		RateLimited: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "collectr_rate_limited_total",
			Help: "Requests refused by a rate limit, by rule. Near-zero for a long time usually means the rule is decoration rather than protection.",
		}, []string{"rule"}),
	}

	reg.MustRegister(
		m.requestDuration, m.requestsTotal, m.SubmissionsTotal, m.ConsentRecords,
		m.EventsDropped, m.CacheLookups, m.AuditVerifyFailed, m.OrphanFilesSwept,
		m.PublishBlocked, m.ExportsTotal, m.WebhookDeliveries, m.RateLimited,
	)
	return m
}

// MustRegister adds an extra collector, panicking on conflict.
func (m *Registry) MustRegister(c prometheus.Collector) { m.reg.MustRegister(c) }

// Handler serves the exposition endpoint.
func (m *Registry) Handler() http.Handler {
	return promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{
		// A scrape must never take the process down with it.
		ErrorHandling: promhttp.ContinueOnError,
	})
}

// Middleware records RED metrics, labelled by the route that matched.
//
// The label comes from the mux's matched pattern, never from the URL path. The
// path would mint a new time series per short code, and a few thousand links
// would turn the scrape endpoint into a memory leak; the pattern stays bounded
// by the number of routes, which is what the per-route thresholds in
// docs/07-operations.md are stated against.
func (m *Registry) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		next.ServeHTTP(rec, r)

		// Set by net/http once a route matches. Empty means nothing matched, and
		// every unrouted request shares one series rather than one per attempted
		// path -- which is also what stops a scanner inflating the metric set.
		route := r.Pattern
		if route == "" {
			route = "unmatched"
		}

		status := strconv.Itoa(rec.status)
		m.requestDuration.WithLabelValues(route, r.Method, status).
			Observe(time.Since(start).Seconds())
		m.requestsTotal.WithLabelValues(route, r.Method, statusClass(rec.status)).Inc()
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status  int
	written bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if !s.written {
		s.status, s.written = code, true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	s.written = true
	return s.ResponseWriter.Write(b)
}

func statusClass(status int) string {
	switch {
	case status < 200:
		return "1xx"
	case status < 300:
		return "2xx"
	case status < 400:
		return "3xx"
	case status < 500:
		return "4xx"
	default:
		return "5xx"
	}
}
