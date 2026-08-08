package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/collectr/collectr/internal/contracts"
	"github.com/collectr/collectr/internal/platform/redisx"
)

const (
	consumerGroup = "ingest"
	batchSize     = 500
	blockDuration = 2 * time.Second
)

// EventWriter persists a batch of events.
type EventWriter interface {
	InsertEvents(ctx context.Context, events []contracts.Event) (int64, error)
}

// Ingestor drains the Redis event stream into Postgres.
type Ingestor struct {
	rdb      *redisx.Client
	store    EventWriter
	log      *slog.Logger
	consumer string
}

// NewIngestor returns an Ingestor. consumer must be unique per worker process.
func NewIngestor(rdb *redisx.Client, store EventWriter, log *slog.Logger, consumer string) *Ingestor {
	return &Ingestor{rdb: rdb, store: store, log: log, consumer: consumer}
}

// Run consumes events until ctx is cancelled.
func (in *Ingestor) Run(ctx context.Context) error {
	// BUSYGROUP simply means another worker created the group first.
	if err := in.rdb.XGroupCreateMkStream(ctx, StreamKey, consumerGroup, "0").Err(); err != nil &&
		!strings.Contains(err.Error(), "BUSYGROUP") {
		return fmt.Errorf("creating consumer group: %w", err)
	}
	in.log.Info("analytics ingest started", "consumer", in.consumer)

	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		n, err := in.drainOnce(ctx)
		switch {
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			return nil
		case err != nil:
			in.log.Error("analytics ingest cycle", "error", err)
			// Back off rather than spin: the usual cause is Redis or Postgres
			// being briefly unavailable, and hammering either makes it worse.
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(time.Second):
			}
		case n == 0:
			// XReadGroup already blocked; nothing more to wait for.
		}
	}
}

func (in *Ingestor) drainOnce(ctx context.Context) (int, error) {
	streams, err := in.rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
		Group:    consumerGroup,
		Consumer: in.consumer,
		Streams:  []string{StreamKey, ">"},
		Count:    batchSize,
		Block:    blockDuration,
	}).Result()
	if errors.Is(err, redis.Nil) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("reading event stream: %w", err)
	}

	var (
		events []contracts.Event
		ids    []string
		bad    int
	)
	for _, s := range streams {
		for _, msg := range s.Messages {
			raw, ok := msg.Values["e"].(string)
			if !ok {
				// Unparseable entries can never succeed; acknowledge so they do
				// not block the group forever.
				ids = append(ids, msg.ID)
				bad++
				continue
			}
			var e contracts.Event
			if err := json.Unmarshal([]byte(raw), &e); err != nil {
				ids = append(ids, msg.ID)
				bad++
				continue
			}
			events = append(events, e)
			ids = append(ids, msg.ID)
		}
	}
	if bad > 0 {
		in.log.Warn("discarded malformed events", "count", bad)
	}
	if len(events) == 0 {
		in.ack(ctx, ids)
		return 0, nil
	}

	if _, err := in.store.InsertEvents(ctx, events); err != nil {
		// Do not acknowledge: the entries stay pending and are retried. Inserting
		// is idempotent, so a partial success costs nothing on the retry.
		return 0, fmt.Errorf("persisting events: %w", err)
	}
	in.ack(ctx, ids)
	return len(events), nil
}

func (in *Ingestor) ack(ctx context.Context, ids []string) {
	if len(ids) == 0 {
		return
	}
	if err := in.rdb.XAck(ctx, StreamKey, consumerGroup, ids...).Err(); err != nil {
		// The events are already stored; a failed ack only means they will be
		// redelivered and deduplicated.
		in.log.Warn("acknowledging events", "error", err, "count", len(ids))
	}
}

// FlushBuffered drains the collector's local fallback buffer into storage. It is
// the second half of the degradation chain described on Collector.
func FlushBuffered(ctx context.Context, src <-chan contracts.Event, store EventWriter, log *slog.Logger) {
	const (
		flushSize     = 200
		flushInterval = 2 * time.Second
	)
	pending := make([]contracts.Event, 0, flushSize)
	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()

	flush := func() {
		if len(pending) == 0 {
			return
		}
		// Deliberately detached from ctx: a shutdown should still land whatever is
		// already in hand rather than throw it away.
		writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if _, err := store.InsertEvents(writeCtx, pending); err != nil {
			log.Error("flushing buffered events", "error", err, "count", len(pending))
		}
		pending = pending[:0]
	}

	for {
		select {
		case <-ctx.Done():
			flush()
			return
		case e := <-src:
			pending = append(pending, e)
			if len(pending) >= flushSize {
				flush()
			}
		case <-ticker.C:
			flush()
		}
	}
}
