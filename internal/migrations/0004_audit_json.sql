-- 0004_audit_json: store audit payloads as json, not jsonb.
--
-- jsonb is a parsed representation: it reorders object keys, drops insignificant
-- whitespace and renormalises numbers. That is exactly what makes it good for
-- querying, and exactly what breaks a hash chain -- the bytes hashed on the way
-- in are not the bytes read back on the way out, so verification reports
-- tampering on a chain nobody has touched.
--
-- json keeps the text verbatim. For a tamper-evident log, byte fidelity is worth
-- more than indexable payloads; the columns that need querying (tenant_id,
-- action, occurred_at) are plain columns already.

ALTER TABLE audit.entries
    ALTER COLUMN actor TYPE json USING actor::text::json,
    ALTER COLUMN target TYPE json USING target::text::json,
    ALTER COLUMN payload TYPE json USING payload::text::json;

-- Entries written before this migration were hashed over pre-jsonb bytes that no
-- longer exist, so their hashes cannot be reproduced. Rather than leave a chain
-- that fails verification forever and trains everyone to ignore the alarm, the
-- pre-release history is dropped and each tenant's chain restarts from genesis.
--
-- This is safe only because the project has not been released. Doing the same to
-- a deployed system would destroy real evidence: there, the migration would have
-- to keep the old rows and start a new chain segment alongside them.
DELETE FROM audit.entries;
DELETE FROM audit.checkpoints;
