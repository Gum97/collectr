// Package app implements analytics ingest and rollup.
package app

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/collectr/collectr/internal/contracts"
	"github.com/collectr/collectr/internal/platform/redisx"
)

// StreamKey is the Redis stream every producer writes funnel events to.
const StreamKey = "collectr:events"

// Collector buffers funnel events on their way to storage.
//
// The degradation chain is deliberate and ordered by what the system owes the
// user at each step:
//
//	Redis XADD  ->  in-process buffer  ->  drop (counted)
//
// A redirect that fails because a metric could not be written would be a worse
// outcome than a metric that is never written, so the last step is a drop and
// not an error. Submissions and consent take the opposite route entirely: they
// go through the outbox inside the business transaction.
type Collector struct {
	rdb    *redisx.Client
	log    *slog.Logger
	maxLen int64

	buf chan contracts.Event

	enqueued  atomic.Int64
	buffered  atomic.Int64
	dropped   atomic.Int64
	redisFail atomic.Int64
}

// NewCollector returns a Collector writing to the shared event stream.
func NewCollector(rdb *redisx.Client, log *slog.Logger, maxLen int64, bufSize int) *Collector {
	return &Collector{
		rdb:    rdb,
		log:    log,
		maxLen: maxLen,
		buf:    make(chan contracts.Event, bufSize),
	}
}

// Collect implements contracts.EventCollector.
func (c *Collector) Collect(ctx context.Context, e contracts.Event) {
	if e.OccurredAt.IsZero() {
		e.OccurredAt = time.Now().UTC()
	}
	payload, err := json.Marshal(e)
	if err != nil {
		// A non-marshalable event is a programming error, not a runtime condition.
		c.log.Error("marshalling event", "error", err, "type", e.Type)
		c.dropped.Add(1)
		return
	}

	// Bound the wait independently of the caller's context: the caller may have a
	// generous deadline, but the hot path's budget for telemetry is a few ms.
	addCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Millisecond)
	defer cancel()

	err = c.rdb.XAdd(addCtx, &redis.XAddArgs{
		Stream: StreamKey,
		MaxLen: c.maxLen,
		Approx: true,
		Values: map[string]any{"e": payload},
	}).Err()
	if err == nil {
		c.enqueued.Add(1)
		return
	}

	c.redisFail.Add(1)
	select {
	case c.buf <- e:
		c.buffered.Add(1)
	default:
		c.dropped.Add(1)
		c.log.Warn("event dropped: redis unavailable and buffer full",
			"type", e.Type, "dropped_total", c.dropped.Load())
	}
}

// Buffered returns the channel of events that could not reach Redis, so that a
// local flusher can drain them straight to Postgres.
func (c *Collector) Buffered() <-chan contracts.Event { return c.buf }

// Stats reports ingest counters for monitoring.
func (c *Collector) Stats() (enqueued, buffered, dropped, redisFailures int64) {
	return c.enqueued.Load(), c.buffered.Load(), c.dropped.Load(), c.redisFail.Load()
}
