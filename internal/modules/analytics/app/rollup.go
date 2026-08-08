package app

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// BucketWidth is the granularity of funnel rollups. Five minutes is small enough
// for a dashboard to feel live and large enough that recomputing one bucket stays
// cheap.
const BucketWidth = 5 * time.Minute

// closedBucketLag keeps the roller off buckets that may still be receiving
// events, so a recompute never captures a half-written window.
const closedBucketLag = time.Minute

// maxCatchUp bounds how much history one Tick rebuilds.
//
// Without it, a worker starting after a long outage would rebuild months in a
// single transaction, holding locks on the rollup table for as long as it took.
// A day per tick clears a ninety-day backlog in ninety ticks while leaving the
// dashboard readable throughout.
const maxCatchUp = 24 * time.Hour

// RollupStore is the persistence the roller needs.
type RollupStore interface {
	RecomputeRange(ctx context.Context, from, to time.Time, width time.Duration) error
	RollupCursor(ctx context.Context) (time.Time, bool, error)
	SetRollupCursor(ctx context.Context, cursor time.Time) error
	EnsurePartitions(ctx context.Context, days int) error
	DropPartitionsBefore(ctx context.Context, cutoff time.Time) ([]string, error)
}

// Roller keeps funnel rollups up to date.
type Roller struct {
	store    RollupStore
	log      *slog.Logger
	lookback time.Duration
	cursor   time.Time
	loaded   bool
}

// NewRoller returns a Roller. lookback is only used when no stored cursor exists.
func NewRoller(store RollupStore, log *slog.Logger, lookback time.Duration) *Roller {
	return &Roller{store: store, log: log, lookback: lookback}
}

// Tick recomputes every bucket that has closed since the last call.
//
// Recompute, not increment. A worker that dies between writing and acknowledging
// simply redoes the same arithmetic on the next pass; an incremental counter
// would double-count in that exact window, and the resulting drift is invisible
// until someone compares two reports and finds they disagree.
//
// The cursor is read from the database on the first tick and written back after
// every advance. Holding it only in memory meant a restart resumed from
// lookback ago and abandoned everything before that -- silently, since the
// events were still there and the job still reported success.
func (r *Roller) Tick(ctx context.Context) error {
	if !r.loaded {
		stored, ok, err := r.store.RollupCursor(ctx)
		if err != nil {
			return err
		}
		if ok {
			r.cursor = stored.UTC().Truncate(BucketWidth)
		} else {
			r.cursor = time.Now().UTC().Add(-r.lookback).Truncate(BucketWidth)
		}
		r.loaded = true
	}

	limit := time.Now().UTC().Add(-closedBucketLag).Truncate(BucketWidth)
	if !r.cursor.Before(limit) {
		return nil
	}

	end := limit
	if behind := limit.Sub(r.cursor); behind > maxCatchUp {
		end = r.cursor.Add(maxCatchUp)
		r.log.Info("rolling up backlog",
			"from", r.cursor.Format(time.RFC3339),
			"behind_hours", int(behind.Hours()))
	}

	if err := r.store.RecomputeRange(ctx, r.cursor, end, BucketWidth); err != nil {
		return fmt.Errorf("recomputing %s..%s: %w",
			r.cursor.Format(time.RFC3339), end.Format(time.RFC3339), err)
	}
	// Written only after the recompute succeeded, so a failure repeats the range
	// rather than skipping it.
	if err := r.store.SetRollupCursor(ctx, end); err != nil {
		return err
	}
	r.cursor = end
	return nil
}

// Maintain creates upcoming event partitions and drops expired ones.
func (r *Roller) Maintain(ctx context.Context, retention time.Duration) error {
	if err := r.store.EnsurePartitions(ctx, 7); err != nil {
		return err
	}
	cutoff := time.Now().UTC().Add(-retention)
	dropped, err := r.store.DropPartitionsBefore(ctx, cutoff)
	if err != nil {
		return err
	}
	if len(dropped) > 0 {
		// Retention deletions are worth a log line: someone will eventually ask
		// where a month of raw events went.
		r.log.Info("dropped expired event partitions",
			"count", len(dropped), "cutoff", cutoff.Format(time.DateOnly), "partitions", dropped)
	}
	return nil
}
