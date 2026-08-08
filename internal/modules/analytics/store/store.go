// Package store persists analytics events and funnel rollups.
package store

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/collectr/collectr/internal/contracts"
	"github.com/collectr/collectr/internal/platform/postgres"
)

// Store reads and writes the analytics schema.
type Store struct {
	db *postgres.DB
}

// New returns a Store backed by db.
func New(db *postgres.DB) *Store { return &Store{db: db} }

// InsertEvents writes a batch of events, ignoring ones already stored.
//
// Ingest is at-least-once, so duplicates are an expected input rather than an
// error: the unique index on (tenant_id, event_id, occurred_at) collapses them.
func (s *Store) InsertEvents(ctx context.Context, events []contracts.Event) (int64, error) {
	if len(events) == 0 {
		return 0, nil
	}

	const q = `
		INSERT INTO analytics.events
			(id, tenant_id, event_id, type, link_id, form_id, form_version_id,
			 page_id, visit_id, meta, occurred_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		ON CONFLICT DO NOTHING`

	batch := &pgx.Batch{}
	for _, e := range events {
		meta := e.Meta
		if meta == nil {
			meta = map[string]any{}
		}
		batch.Queue(q,
			uuid.New(), e.TenantID, e.EventID, e.Type,
			e.LinkID, e.FormID, e.FormVersionID,
			nullString(e.PageID), e.VisitID, meta, e.OccurredAt,
		)
	}

	res := s.db.SendBatch(ctx, batch)
	defer res.Close()

	var inserted int64
	for range events {
		tag, err := res.Exec()
		if err != nil {
			return inserted, fmt.Errorf("inserting event batch: %w", err)
		}
		inserted += tag.RowsAffected()
	}
	return inserted, nil
}

// RecomputeBucket rebuilds the funnel rollups for one closed time bucket.
//
// Rebuilding rather than incrementing is what makes the job idempotent: a worker
// that crashes after writing but before acknowledging simply recomputes the same
// numbers next time. Incremental counters would double-count in exactly that case,
// and the resulting drift is close to impossible to detect after the fact.
// RecomputeRange rebuilds every bucket between from and to in one statement.
//
// A range rather than a bucket at a time: catching up after an outage would
// otherwise mean one round trip per five-minute bucket -- roughly 26,000 of them
// for a ninety-day backlog -- which is slow enough that in practice the catch-up
// never happens.
//
// Recompute, not increment. A worker that dies between writing and acknowledging
// simply redoes the same arithmetic on the next pass; an incremental counter
// would double-count in that exact window, and the resulting drift is invisible
// until someone compares two reports and finds they disagree.
func (s *Store) RecomputeRange(ctx context.Context, from, to time.Time, width time.Duration) error {
	const del = `DELETE FROM analytics.funnel_rollups WHERE bucket >= $1 AND bucket < $2`
	const ins = `
		INSERT INTO analytics.funnel_rollups
			(tenant_id, project_id, form_id, link_id, bucket, clicks, views, starts, submits)
		SELECT
			e.tenant_id,
			NULL::uuid,
			COALESCE(e.form_id, '00000000-0000-0000-0000-000000000000'::uuid),
			COALESCE(e.link_id, '00000000-0000-0000-0000-000000000000'::uuid),
			date_bin($3, e.occurred_at, timestamptz '2000-01-01'),
			count(*) FILTER (WHERE e.type = 'click'),
			count(*) FILTER (WHERE e.type = 'form_view'),
			count(*) FILTER (WHERE e.type = 'form_start'),
			count(*) FILTER (WHERE e.type = 'submit')
		FROM analytics.events e
		WHERE e.occurred_at >= $1 AND e.occurred_at < $2
		GROUP BY e.tenant_id, e.form_id, e.link_id,
		         date_bin($3, e.occurred_at, timestamptz '2000-01-01')`

	return s.db.InTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, del, from, to); err != nil {
			return fmt.Errorf("clearing rollup range: %w", err)
		}
		if _, err := tx.Exec(ctx, ins, from, to, width); err != nil {
			return fmt.Errorf("recomputing rollup range: %w", err)
		}
		return nil
	})
}

// RollupCursor reports how far the roller has already processed.
func (s *Store) RollupCursor(ctx context.Context) (time.Time, bool, error) {
	var cursor time.Time
	err := s.db.QueryRow(ctx, `SELECT cursor FROM analytics.rollup_state WHERE id`).Scan(&cursor)
	if postgres.IsNoRows(err) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, fmt.Errorf("reading rollup cursor: %w", err)
	}
	return cursor, true, nil
}

// SetRollupCursor records progress so a restart resumes instead of skipping.
func (s *Store) SetRollupCursor(ctx context.Context, cursor time.Time) error {
	_, err := s.db.Exec(ctx,
		`INSERT INTO analytics.rollup_state (id, cursor) VALUES (true, $1)
		 ON CONFLICT (id) DO UPDATE SET cursor = EXCLUDED.cursor`, cursor)
	if err != nil {
		return fmt.Errorf("writing rollup cursor: %w", err)
	}
	return nil
}

// EnsurePartitions creates the daily event partitions covering the next `days`.
//
// Creating them ahead of time turns a missing partition from a write failure at
// midnight into a no-op during a scheduled job.
func (s *Store) EnsurePartitions(ctx context.Context, days int) error {
	day := time.Now().UTC().Truncate(24 * time.Hour)
	for i := range days {
		if _, err := s.db.Exec(ctx,
			"SELECT analytics.ensure_events_partition($1::date)",
			day.AddDate(0, 0, i),
		); err != nil {
			return fmt.Errorf("ensuring event partition: %w", err)
		}
	}
	return nil
}

// DropPartitionsBefore removes event partitions older than cutoff.
//
// Dropping a partition is instant; a DELETE over the same rows would lock the
// table and inflate WAL for hours at the volumes this table reaches.
func (s *Store) DropPartitionsBefore(ctx context.Context, cutoff time.Time) ([]string, error) {
	const q = `
		SELECT c.relname
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'analytics'
		  AND c.relname ~ '^events_[0-9]{8}$'
		  AND c.relname < $1`

	rows, err := s.db.Query(ctx, q, "events_"+cutoff.Format("20060102"))
	if err != nil {
		return nil, fmt.Errorf("listing event partitions: %w", err)
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("scanning partition name: %w", err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating partitions: %w", err)
	}

	for _, name := range names {
		// The name comes from pg_class filtered by a strict pattern, so it cannot
		// carry injected SQL, but it still has to be an identifier, not a parameter.
		if _, err := s.db.Exec(ctx, fmt.Sprintf("DROP TABLE analytics.%s", pgx.Identifier{name}.Sanitize())); err != nil {
			return nil, fmt.Errorf("dropping partition %s: %w", name, err)
		}
	}
	return names, nil
}

func nullString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
