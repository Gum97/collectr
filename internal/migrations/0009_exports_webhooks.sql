-- 0009: export jobs and outbound webhooks.

CREATE SCHEMA IF NOT EXISTS integrations;

CREATE TABLE core.exports (
    id         UUID PRIMARY KEY,
    tenant_id  UUID NOT NULL REFERENCES iam.tenants (id) ON DELETE CASCADE,
    project_id UUID REFERENCES iam.projects (id) ON DELETE CASCADE,
    kind       TEXT NOT NULL CHECK (kind IN ('form_report', 'link_report')),
    target_id  UUID NOT NULL,
    params     JSONB NOT NULL DEFAULT '{}'::jsonb,

    requested_by UUID NOT NULL REFERENCES iam.users (id),
    -- Recorded per job because it decides whether sensitive answers are written
    -- in clear or masked, and that is exactly what an investigation into a leak
    -- would need to know afterwards.
    include_sensitive BOOLEAN NOT NULL DEFAULT false,

    status TEXT NOT NULL DEFAULT 'queued'
        CHECK (status IN ('queued', 'running', 'ready', 'failed', 'expired')),
    row_count   INT,
    storage_key TEXT,
    filename    TEXT,
    error       TEXT,
    -- Short: the artefact is a file full of personal data sitting on disk.
    expires_at TIMESTAMPTZ,
    started_at TIMESTAMPTZ,
    ready_at   TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX exports_queued ON core.exports (created_at) WHERE status = 'queued';
CREATE INDEX exports_by_tenant ON core.exports (tenant_id, created_at DESC);
CREATE INDEX exports_expiring ON core.exports (expires_at) WHERE status = 'ready';

CREATE TABLE integrations.webhooks (
    id         UUID PRIMARY KEY,
    tenant_id  UUID NOT NULL REFERENCES iam.tenants (id) ON DELETE CASCADE,
    project_id UUID NOT NULL REFERENCES iam.projects (id) ON DELETE CASCADE,
    url        TEXT NOT NULL,
    events     TEXT[] NOT NULL,
    -- Encrypted at rest: it is the key the receiver uses to tell a genuine
    -- delivery from a forged one.
    secret_enc BYTEA NOT NULL,
    active     BOOLEAN NOT NULL DEFAULT true,

    -- Off by default. A webhook that ships answers is a transfer of personal
    -- data to a third party, and that should be a deliberate act rather than
    -- something that happens because a box was ticked by default.
    include_answers BOOLEAN NOT NULL DEFAULT false,

    consecutive_failures INT NOT NULL DEFAULT 0,
    disabled_at          TIMESTAMPTZ,
    disabled_reason      TEXT,
    created_by UUID NOT NULL REFERENCES iam.users (id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX webhooks_by_project ON integrations.webhooks (tenant_id, project_id)
    WHERE active AND disabled_at IS NULL;

CREATE TABLE integrations.deliveries (
    id         UUID PRIMARY KEY,
    tenant_id  UUID NOT NULL,
    webhook_id UUID NOT NULL REFERENCES integrations.webhooks (id) ON DELETE CASCADE,
    event_id   UUID NOT NULL,
    event_type TEXT NOT NULL,
    payload    JSONB NOT NULL,

    attempt INT NOT NULL DEFAULT 0,
    status  TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'delivered', 'failed', 'dead')),
    response_code    INT,
    -- Truncated. The receiver's response body is not ours to store in full, and
    -- error pages routinely echo back what was sent.
    response_snippet TEXT,
    next_attempt_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    delivered_at     TIMESTAMPTZ,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX deliveries_due ON integrations.deliveries (next_attempt_at)
    WHERE status = 'pending';
CREATE INDEX deliveries_by_webhook ON integrations.deliveries (webhook_id, created_at DESC);

ALTER TABLE core.exports ENABLE ROW LEVEL SECURITY;
ALTER TABLE integrations.webhooks ENABLE ROW LEVEL SECURITY;
ALTER TABLE integrations.deliveries ENABLE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON core.exports
    USING (tenant_id::text = current_setting('app.tenant_id', true));
CREATE POLICY tenant_isolation ON integrations.webhooks
    USING (tenant_id::text = current_setting('app.tenant_id', true));
CREATE POLICY tenant_isolation ON integrations.deliveries
    USING (tenant_id::text = current_setting('app.tenant_id', true));

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'collectr_app') THEN
        RETURN;
    END IF;
    GRANT USAGE ON SCHEMA integrations TO collectr_app;
    GRANT SELECT, INSERT, UPDATE, DELETE ON core.exports TO collectr_app;
    GRANT SELECT, INSERT, UPDATE, DELETE
        ON integrations.webhooks, integrations.deliveries TO collectr_app;
END;
$$;
