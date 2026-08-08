package store

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/collectr/collectr/internal/contracts"
)

// maxBreakdownRows caps each breakdown.
//
// A referrer list is long-tailed and the tail is noise; more to the point, an
// uncapped GROUP BY on a column filled from a request header is a way for a
// stranger to decide how much memory the report uses.
const maxBreakdownRows = 20

// LinkStats reports one link's traffic.
//
// The time series comes from the rollups and the breakdowns from raw events,
// which is why the result carries BreakdownFrom: the two halves cover different
// periods and the caller has to be able to say so.
func (s *Store) LinkStats(ctx context.Context, tenantID, linkID uuid.UUID, from, to time.Time, bucket time.Duration, rawRetention time.Duration) (contracts.LinkStats, error) {
	var out contracts.LinkStats

	out.BreakdownFrom = time.Now().UTC().Add(-rawRetention)
	if from.After(out.BreakdownFrom) {
		out.BreakdownFrom = from
	}

	const series = `
		SELECT date_bin($4, bucket, timestamptz '2000-01-01') AS b, sum(clicks)
		FROM analytics.funnel_rollups
		WHERE tenant_id = $1 AND link_id = $2
		  AND bucket >= $3 AND bucket < $5
		GROUP BY b
		ORDER BY b`

	// funnel_rollups is under row-level security: read outside a tenant-scoped
	// transaction and the API role sees nothing, which renders as a link that has
	// never been clicked.
	err := s.db.InTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, series, tenantID, linkID, from, bucket, to)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var p contracts.ClickPoint
			if err := rows.Scan(&p.Bucket, &p.Clicks); err != nil {
				return err
			}
			out.Points = append(out.Points, p)
			out.Clicks += p.Clicks
		}
		return rows.Err()
	})
	if err != nil {
		return out, fmt.Errorf("reading link series: %w", err)
	}

	// The network count and the extremes come from raw events: the rollup counts
	// clicks and holds nothing about where they came from.
	const totals = `
		SELECT count(*), count(DISTINCT meta->>'ip_prefix'),
		       min(occurred_at), max(occurred_at)
		FROM analytics.events
		WHERE tenant_id = $1 AND link_id = $2 AND type = 'click'
		  AND occurred_at >= $3 AND occurred_at < $4`

	if err := s.db.QueryRow(ctx, totals, tenantID, linkID, out.BreakdownFrom, to).
		Scan(&out.BreakdownClicks, &out.Networks, &out.FirstClick, &out.LastClick); err != nil {
		return out, fmt.Errorf("reading link totals: %w", err)
	}

	for _, d := range []struct {
		expr string
		into *[]contracts.Breakdown
	}{
		// COALESCE, not a filter: a click with no referrer is a real click and
		// dropping it would make the breakdown disagree with the total above it.
		{`COALESCE(NULLIF(meta->>'src', ''), 'direct')`, &out.Sources},
		{`COALESCE(NULLIF(meta->>'referrer', ''), 'không rõ')`, &out.Referrers},
		{`COALESCE(NULLIF(meta->>'ua', ''), 'không rõ')`, &out.Browsers},
		{`COALESCE(NULLIF(meta->>'utm_source', ''), 'không gắn UTM')`, &out.UTMSources},
		{`COALESCE(NULLIF(meta->>'utm_medium', ''), 'không gắn UTM')`, &out.UTMMediums},
		{`COALESCE(NULLIF(meta->>'utm_campaign', ''), 'không gắn UTM')`, &out.UTMCampaigns},
	} {
		b, err := s.breakdown(ctx, d.expr, tenantID, linkID, out.BreakdownFrom, to)
		if err != nil {
			return out, err
		}
		*d.into = b
	}

	return out, nil
}

func (s *Store) breakdown(ctx context.Context, expr string, tenantID, linkID uuid.UUID, from, to time.Time) ([]contracts.Breakdown, error) {
	// expr is one of a fixed set of literals defined above, never caller input.
	q := fmt.Sprintf(`
		SELECT %s AS k, count(*), count(DISTINCT meta->>'ip_prefix')
		FROM analytics.events
		WHERE tenant_id = $1 AND link_id = $2 AND type = 'click'
		  AND occurred_at >= $3 AND occurred_at < $4
		GROUP BY k
		ORDER BY 2 DESC
		LIMIT %d`, expr, maxBreakdownRows)

	rows, err := s.db.Query(ctx, q, tenantID, linkID, from, to)
	if err != nil {
		return nil, fmt.Errorf("reading link breakdown: %w", err)
	}
	defer rows.Close()

	var out []contracts.Breakdown
	for rows.Next() {
		var b contracts.Breakdown
		if err := rows.Scan(&b.Key, &b.Clicks, &b.Networks); err != nil {
			return nil, fmt.Errorf("scanning link breakdown: %w", err)
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// TopLinks ranks a project's links by clicks.
//
// Reads the rollups only. The leaderboard is the screen an operator opens first
// and it must stay fast as events accumulate, so it never touches the raw table.
func (s *Store) TopLinks(ctx context.Context, tenantID, projectID uuid.UUID, from, to time.Time, limit int) ([]contracts.LinkSummary, error) {
	const q = `
		SELECT r.link_id, sum(r.clicks), sum(r.submits), max(r.bucket)
		FROM analytics.funnel_rollups r
		JOIN links.links l ON l.id = r.link_id
		WHERE r.tenant_id = $1
		  AND l.project_id = $2 AND l.status <> 'deleted'
		  AND r.bucket >= $3 AND r.bucket < $4
		GROUP BY r.link_id
		HAVING sum(r.clicks) > 0
		ORDER BY 2 DESC
		LIMIT $5`

	var out []contracts.LinkSummary
	err := s.db.InTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, q, tenantID, projectID, from, to, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var l contracts.LinkSummary
			if err := rows.Scan(&l.LinkID, &l.Clicks, &l.Submits, &l.LastSeen); err != nil {
				return err
			}
			out = append(out, l)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("reading top links: %w", err)
	}
	return out, nil
}

// LinkReport assembles a project's link report.
//
// Two sources, joined in Go rather than in one query: the per-link totals come
// from the rollups, which hold the whole history, and the QR and network columns
// come from raw events, which do not. A single query over both would have to
// pick one time range for both halves and would quietly report the shorter one.
func (s *Store) LinkReport(ctx context.Context, tenantID, projectID uuid.UUID, from, to time.Time, rawRetention time.Duration) (contracts.LinkReport, error) {
	out := contracts.LinkReport{From: from, To: to}

	out.BreakdownFrom = time.Now().UTC().Add(-rawRetention)
	if from.After(out.BreakdownFrom) {
		out.BreakdownFrom = from
	}

	const rowsQ = `
		SELECT l.id, l.code, d.host, coalesce(l.target_url, ''), l.status, l.created_at,
		       coalesce(sum(r.clicks), 0), coalesce(sum(r.submits), 0), max(r.bucket)
		FROM links.links l
		JOIN links.domains d ON d.id = l.domain_id
		LEFT JOIN analytics.funnel_rollups r
		       ON r.link_id = l.id AND r.tenant_id = l.tenant_id
		      AND r.bucket >= $3 AND r.bucket < $4
		WHERE l.tenant_id = $1 AND l.project_id = $2 AND l.status <> 'deleted'
		GROUP BY l.id, l.code, d.host, l.target_url, l.status, l.created_at
		ORDER BY 7 DESC, l.created_at DESC`

	const rawQ = `
		SELECT e.link_id,
		       count(*) FILTER (WHERE e.meta->>'src' = 'qr'),
		       count(DISTINCT e.meta->>'ip_prefix')
		FROM analytics.events e
		JOIN links.links l ON l.id = e.link_id
		WHERE e.tenant_id = $1 AND l.project_id = $2 AND e.type = 'click'
		  AND e.occurred_at >= $3 AND e.occurred_at < $4
		GROUP BY e.link_id`

	const byDayQ = `
		SELECT date_bin(interval '1 day', r.bucket, timestamptz '2000-01-01'), sum(r.clicks)
		FROM analytics.funnel_rollups r
		JOIN links.links l ON l.id = r.link_id
		WHERE r.tenant_id = $1 AND l.project_id = $2
		  AND r.bucket >= $3 AND r.bucket < $4
		GROUP BY 1
		HAVING sum(r.clicks) > 0
		ORDER BY 1`

	type rawCounts struct{ qr, networks int }

	err := s.db.InTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		raw := make(map[uuid.UUID]rawCounts)
		rows, err := tx.Query(ctx, rawQ, tenantID, projectID, out.BreakdownFrom, to)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id uuid.UUID
			var c rawCounts
			if err := rows.Scan(&id, &c.qr, &c.networks); err != nil {
				rows.Close()
				return err
			}
			raw[id] = c
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}

		rows, err = tx.Query(ctx, rowsQ, tenantID, projectID, from, to)
		if err != nil {
			return err
		}
		for rows.Next() {
			var r contracts.LinkReportRow
			if err := rows.Scan(&r.LinkID, &r.Code, &r.Host, &r.Target, &r.Status,
				&r.CreatedAt, &r.Clicks, &r.Submits, &r.LastClick); err != nil {
				rows.Close()
				return err
			}
			if c, ok := raw[r.LinkID]; ok {
				r.QRClicks, r.Networks = c.qr, c.networks
			}
			out.Rows = append(out.Rows, r)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}

		rows, err = tx.Query(ctx, byDayQ, tenantID, projectID, from, to)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var p contracts.ClickPoint
			if err := rows.Scan(&p.Bucket, &p.Clicks); err != nil {
				return err
			}
			out.ByDay = append(out.ByDay, p)
		}
		return rows.Err()
	})
	if err != nil {
		return contracts.LinkReport{}, fmt.Errorf("reading link report: %w", err)
	}

	for _, d := range []struct {
		expr string
		into *[]contracts.Breakdown
	}{
		{`COALESCE(NULLIF(e.meta->>'src', ''), 'direct')`, &out.Sources},
		{`COALESCE(NULLIF(e.meta->>'referrer', ''), 'không rõ')`, &out.Referrers},
		{`COALESCE(NULLIF(e.meta->>'utm_source', ''), 'không gắn UTM')`, &out.UTMSources},
		{`COALESCE(NULLIF(e.meta->>'utm_medium', ''), 'không gắn UTM')`, &out.UTMMediums},
		{`COALESCE(NULLIF(e.meta->>'utm_campaign', ''), 'không gắn UTM')`, &out.UTMCampaigns},
	} {
		b, err := s.projectBreakdown(ctx, d.expr, tenantID, projectID, out.BreakdownFrom, to)
		if err != nil {
			return contracts.LinkReport{}, err
		}
		*d.into = b
	}

	return out, nil
}

func (s *Store) projectBreakdown(ctx context.Context, expr string, tenantID, projectID uuid.UUID, from, to time.Time) ([]contracts.Breakdown, error) {
	// expr is one of a fixed set of literals defined above, never caller input.
	q := fmt.Sprintf(`
		SELECT %s AS k, count(*), count(DISTINCT e.meta->>'ip_prefix')
		FROM analytics.events e
		JOIN links.links l ON l.id = e.link_id
		WHERE e.tenant_id = $1 AND l.project_id = $2 AND e.type = 'click'
		  AND e.occurred_at >= $3 AND e.occurred_at < $4
		GROUP BY k
		ORDER BY 2 DESC
		LIMIT %d`, expr, maxBreakdownRows)

	var out []contracts.Breakdown
	err := s.db.InTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, q, tenantID, projectID, from, to)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var b contracts.Breakdown
			if err := rows.Scan(&b.Key, &b.Clicks, &b.Networks); err != nil {
				return err
			}
			out = append(out, b)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("reading project breakdown: %w", err)
	}
	return out, nil
}
