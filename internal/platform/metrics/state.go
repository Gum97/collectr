package metrics

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/collectr/collectr/internal/platform/postgres"
)

// stateRefresh is how often the database is asked for the gauge values.
//
// The gauges are read on scrape, but computed on a timer: a Prometheus scrape
// that runs five queries against a loaded database would make monitoring a
// source of load rather than a view of it.
const stateRefresh = 30 * time.Second

// StateCollector exports the gauges that have to be read from the database.
//
// The one that matters most is dsr_overdue_count. Everything else here degrades
// service; that one breaks the law, and it is the only metric in the system
// whose threshold is zero.
type StateCollector struct {
	db  *postgres.DB
	log *slog.Logger

	mu      sync.RWMutex
	values  map[string]float64
	lastErr error

	descs map[string]*prometheus.Desc
}

// stateQueries are the gauges and how to compute them.
var stateQueries = map[string]struct {
	help  string
	query string
}{
	"collectr_dsr_overdue_count": {
		help: "Data subject requests past their statutory deadline. The only metric here whose correct value is always zero.",
		query: `SELECT count(*) FROM consent.dsr_requests
		        WHERE status NOT IN ('fulfilled','rejected') AND due_at < now()`,
	},
	"collectr_dsr_due_within_24h": {
		help: "Requests approaching their deadline. Warns before the cliff rather than after it.",
		query: `SELECT count(*) FROM consent.dsr_requests
		        WHERE status NOT IN ('fulfilled','rejected')
		          AND due_at BETWEEN now() AND now() + interval '24 hours'`,
	},
	"collectr_outbox_pending": {
		help:  "Outbox rows awaiting relay.",
		query: `SELECT count(*) FROM core.outbox WHERE sent_at IS NULL`,
	},
	"collectr_outbox_oldest_seconds": {
		help: "Age of the oldest unsent outbox row. Depth alone lies -- ten thousand draining in thirty seconds is fine, a hundred growing forever is an incident -- so time-to-drain is what to alert on.",
		query: `SELECT coalesce(extract(epoch from (now() - min(available_at))), 0)
		        FROM core.outbox WHERE sent_at IS NULL`,
	},
	"collectr_webhook_deliveries_pending": {
		help:  "Webhook deliveries queued for another attempt.",
		query: `SELECT count(*) FROM integrations.deliveries WHERE status = 'pending'`,
	},
	"collectr_webhooks_disabled": {
		help:  "Webhook endpoints switched off after repeated failure.",
		query: `SELECT count(*) FROM integrations.webhooks WHERE disabled_at IS NOT NULL`,
	},
	"collectr_exports_queued": {
		help:  "Export jobs waiting to run.",
		query: `SELECT count(*) FROM core.exports WHERE status = 'queued'`,
	},
	"collectr_files_orphaned": {
		help: "Uploads never attached to a submission and older than a day.",
		query: `SELECT count(*) FROM files.files
		        WHERE status = 'pending' AND submission_id IS NULL
		          AND created_at < now() - interval '24 hours'`,
	},
	"collectr_submissions_awaiting_purge": {
		help: "Submissions past their retention date that the sweeper has not yet removed.",
		query: `SELECT count(*) FROM forms.submissions
		        WHERE status = 'active' AND purge_at IS NOT NULL AND purge_at < now()`,
	},
	"collectr_db_pool_in_use": {
		help: "Database connections currently checked out.",
	},
	"collectr_db_pool_total": {
		help: "Database connections open.",
	},
}

// NewStateCollector returns a collector and starts its refresh loop.
func NewStateCollector(ctx context.Context, db *postgres.DB, log *slog.Logger) *StateCollector {
	c := &StateCollector{
		db: db, log: log,
		values: make(map[string]float64, len(stateQueries)),
		descs:  make(map[string]*prometheus.Desc, len(stateQueries)),
	}
	for name, q := range stateQueries {
		c.descs[name] = prometheus.NewDesc(name, q.help, nil, nil)
	}

	c.refresh(ctx)
	go c.loop(ctx)
	return c
}

func (c *StateCollector) loop(ctx context.Context) {
	ticker := time.NewTicker(stateRefresh)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.refresh(ctx)
		}
	}
}

func (c *StateCollector) refresh(ctx context.Context) {
	// Bounded independently of the caller: a slow database must not make the
	// refresh loop pile up.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()

	values := make(map[string]float64, len(stateQueries))
	var firstErr error

	for name, q := range stateQueries {
		if q.query == "" {
			continue
		}
		var v float64
		if err := c.db.QueryRow(ctx, q.query).Scan(&v); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		values[name] = v
	}

	stat := c.db.Stat()
	values["collectr_db_pool_in_use"] = float64(stat.AcquiredConns())
	values["collectr_db_pool_total"] = float64(stat.TotalConns())

	c.mu.Lock()
	// Only replace what was read successfully: a transient failure should leave
	// the previous value visible rather than report zero, which reads as "all
	// clear" on exactly the gauge whose zero means all clear.
	for k, v := range values {
		c.values[k] = v
	}
	c.lastErr = firstErr
	c.mu.Unlock()

	if firstErr != nil {
		c.log.Warn("refreshing state metrics", "error", firstErr)
	}
}

// Describe implements prometheus.Collector.
func (c *StateCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range c.descs {
		ch <- d
	}
}

// Collect implements prometheus.Collector.
func (c *StateCollector) Collect(ch chan<- prometheus.Metric) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	for name, desc := range c.descs {
		v, ok := c.values[name]
		if !ok {
			continue
		}
		ch <- prometheus.MustNewConstMetric(desc, prometheus.GaugeValue, v)
	}
}
