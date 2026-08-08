-- Index for reading one link's events back.
--
-- The only index on analytics.events was (occurred_at, tenant_id), which suits
-- the rollup job -- it sweeps a time window across all tenants -- and nothing
-- else. A breakdown for a single link had to scan every event in the window:
-- 13ms against 196k rows here, and linear from there, on a page that issues
-- several such queries at once.
--
-- Partial, because a click is the only event type that carries a link_id and
-- form events outnumber them on any deployment that uses forms. Indexing the
-- NULLs would grow the index without ever being read through it.
CREATE INDEX events_link_scan ON analytics.events (tenant_id, link_id, occurred_at)
    WHERE link_id IS NOT NULL;

-- Reading a project's links ordered by clicks. Without it the leaderboard sorts
-- by scanning every rollup row the tenant owns.
--
-- The predicate excludes the all-zero uuid, not NULL: the rollup job writes that
-- sentinel for rows belonging to no link, so link_id is never actually NULL here
-- and a NOT NULL partial index would exclude nothing.
CREATE INDEX funnel_rollups_link ON analytics.funnel_rollups (tenant_id, link_id, bucket)
    WHERE link_id <> '00000000-0000-0000-0000-000000000000'::uuid;
