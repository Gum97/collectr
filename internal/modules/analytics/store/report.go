package store

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/collectr/collectr/internal/contracts"
)

// Funnel returns the conversion funnel for a form over a period.
//
// Read from the rollups, not the raw events: the rollups are recomputed per
// closed bucket and are already the shape a report wants.
func (s *Store) Funnel(ctx context.Context, tenantID, formID uuid.UUID, from, to time.Time, bucket time.Duration) (contracts.FunnelSummary, error) {
	const q = `
		SELECT date_bin($4, bucket, timestamptz '2000-01-01') AS b,
		       sum(clicks), sum(views), sum(starts), sum(submits)
		FROM analytics.funnel_rollups
		WHERE tenant_id = $1 AND form_id = $2
		  AND bucket >= $3 AND bucket < $5
		GROUP BY b
		ORDER BY b`

	// InTenantTx, not a bare Query: funnel_rollups is under row-level security,
	// and without the tenant setting the API role reads zero rows and reports an
	// empty funnel rather than an error. This worked only because the report
	// generator runs in the worker, which connects as the owner and is therefore
	// exempt from the policy it was written to enforce.
	var out contracts.FunnelSummary
	err := s.db.InTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, q, tenantID, formID, from, bucket, to)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var p contracts.FunnelPoint
			if err := rows.Scan(&p.Bucket, &p.Clicks, &p.Views, &p.Starts, &p.Submits); err != nil {
				return err
			}
			out.Points = append(out.Points, p)
			out.Clicks += p.Clicks
			out.Views += p.Views
			out.Starts += p.Starts
			out.Submits += p.Submits
		}
		return rows.Err()
	})
	if err != nil {
		return contracts.FunnelSummary{}, fmt.Errorf("reading funnel: %w", err)
	}
	return out, nil
}

// PageDropOff reports where respondents stopped.
//
// Computed from page-view events per visit: the last page a visit reached, minus
// the visits that went on to submit. Without page events this is unanswerable,
// which is why form_page_view exists as its own event type.
func (s *Store) PageDropOff(ctx context.Context, tenantID, formID uuid.UUID, from, to time.Time) ([]contracts.PageDropOff, error) {
	const q = `
		WITH visits AS (
			SELECT visit_id,
			       max(occurred_at) FILTER (WHERE type = 'form_page_view') AS last_page_at,
			       bool_or(type = 'submit') AS submitted
			FROM analytics.events
			WHERE tenant_id = $1 AND form_id = $2
			  AND occurred_at >= $3 AND occurred_at < $4
			  AND visit_id IS NOT NULL
			GROUP BY visit_id
		),
		last_page AS (
			SELECT e.page_id, v.submitted
			FROM visits v
			JOIN analytics.events e
			  ON e.visit_id = v.visit_id AND e.occurred_at = v.last_page_at
			 AND e.type = 'form_page_view' AND e.tenant_id = $1
			WHERE e.page_id IS NOT NULL
		),
		entered AS (
			SELECT page_id, count(DISTINCT visit_id) AS n
			FROM analytics.events
			WHERE tenant_id = $1 AND form_id = $2 AND type = 'form_page_view'
			  AND occurred_at >= $3 AND occurred_at < $4 AND page_id IS NOT NULL
			GROUP BY page_id
		)
		SELECT e.page_id, e.n,
		       coalesce(count(l.page_id) FILTER (WHERE NOT l.submitted), 0)
		FROM entered e
		LEFT JOIN last_page l ON l.page_id = e.page_id
		GROUP BY e.page_id, e.n
		ORDER BY e.page_id`

	rows, err := s.db.Query(ctx, q, tenantID, formID, from, to)
	if err != nil {
		return nil, fmt.Errorf("reading page drop-off: %w", err)
	}
	defer rows.Close()

	var out []contracts.PageDropOff
	for rows.Next() {
		var d contracts.PageDropOff
		if err := rows.Scan(&d.PageID, &d.Entered, &d.Left); err != nil {
			return nil, fmt.Errorf("scanning drop-off: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
