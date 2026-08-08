-- Persist how far the rollup job has got.
--
-- The cursor lived in the Roller struct, initialised to now minus one hour at
-- every start. Any worker outage longer than that lookback left a hole in the
-- rollups that nothing ever filled: the events were still in the raw table, the
-- job reported success, and the click totals were simply lower than reality with
-- nothing to indicate it. On this deployment the gap was 39 rollup clicks
-- against 161 real ones.
CREATE TABLE analytics.rollup_state (
    id     BOOLEAN PRIMARY KEY DEFAULT true CHECK (id),
    cursor TIMESTAMPTZ NOT NULL
);

-- Seed from the oldest event still held, so the first run after this migration
-- rebuilds whatever the old cursor skipped. There is nothing to recover from
-- before that: the partitions are gone.
INSERT INTO analytics.rollup_state (id, cursor)
SELECT true, COALESCE(min(occurred_at), now())
FROM analytics.events
ON CONFLICT (id) DO NOTHING;
