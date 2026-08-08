-- 0001_core: IAM, links, analytics, audit, core infrastructure.
-- Forms, submissions and consent arrive in 0002 (v0.2/v0.3).

CREATE EXTENSION IF NOT EXISTS citext;

CREATE SCHEMA IF NOT EXISTS core;
CREATE SCHEMA IF NOT EXISTS iam;
CREATE SCHEMA IF NOT EXISTS links;
CREATE SCHEMA IF NOT EXISTS analytics;
CREATE SCHEMA IF NOT EXISTS audit;

-- ---------------------------------------------------------------- IAM

CREATE TABLE iam.tenants (
    id         UUID PRIMARY KEY,
    name       TEXT NOT NULL,
    slug       TEXT NOT NULL UNIQUE,
    settings   JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE iam.users (
    id            UUID PRIMARY KEY,
    email         CITEXT NOT NULL UNIQUE,
    password_hash TEXT,
    name          TEXT NOT NULL DEFAULT '',
    mfa_secret_enc BYTEA,
    mfa_enabled   BOOLEAN NOT NULL DEFAULT false,
    status        TEXT NOT NULL DEFAULT 'active'
        CHECK (status IN ('active', 'suspended', 'deleted')),
    last_login_at TIMESTAMPTZ,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE iam.org_members (
    tenant_id UUID NOT NULL REFERENCES iam.tenants (id) ON DELETE CASCADE,
    user_id   UUID NOT NULL REFERENCES iam.users (id) ON DELETE CASCADE,
    role      TEXT NOT NULL CHECK (role IN ('owner', 'admin', 'member', 'dpo')),
    joined_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, user_id)
);

CREATE TABLE iam.projects (
    id                     UUID PRIMARY KEY,
    tenant_id              UUID NOT NULL REFERENCES iam.tenants (id) ON DELETE CASCADE,
    name                   TEXT NOT NULL,
    slug                   TEXT NOT NULL,
    default_retention_days INT,
    archived_at            TIMESTAMPTZ,
    created_by             UUID NOT NULL REFERENCES iam.users (id),
    created_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, slug)
);

CREATE TABLE iam.project_members (
    tenant_id  UUID NOT NULL,
    project_id UUID NOT NULL REFERENCES iam.projects (id) ON DELETE CASCADE,
    user_id    UUID NOT NULL REFERENCES iam.users (id) ON DELETE CASCADE,
    role       TEXT NOT NULL CHECK (role IN ('manager', 'editor', 'analyst', 'viewer')),
    granted_by UUID NOT NULL REFERENCES iam.users (id),
    granted_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (project_id, user_id)
);

CREATE TABLE iam.api_keys (
    id           UUID PRIMARY KEY,
    tenant_id    UUID NOT NULL REFERENCES iam.tenants (id) ON DELETE CASCADE,
    project_id   UUID REFERENCES iam.projects (id) ON DELETE CASCADE,
    name         TEXT NOT NULL,
    prefix       TEXT NOT NULL UNIQUE,
    key_hash     BYTEA NOT NULL,
    scopes       TEXT[] NOT NULL,
    ip_allowlist INET[],
    expires_at   TIMESTAMPTZ,
    last_used_at TIMESTAMPTZ,
    revoked_at   TIMESTAMPTZ,
    created_by   UUID NOT NULL REFERENCES iam.users (id),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ---------------------------------------------------------------- Links

CREATE TABLE links.domains (
    id         UUID PRIMARY KEY,
    tenant_id  UUID NOT NULL REFERENCES iam.tenants (id) ON DELETE CASCADE,
    host       TEXT NOT NULL UNIQUE,
    is_default BOOLEAN NOT NULL DEFAULT false,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE links.links (
    id         UUID PRIMARY KEY,
    tenant_id  UUID NOT NULL REFERENCES iam.tenants (id) ON DELETE CASCADE,
    project_id UUID NOT NULL REFERENCES iam.projects (id) ON DELETE CASCADE,
    domain_id  UUID NOT NULL REFERENCES links.domains (id),
    code       TEXT NOT NULL,
    target_url TEXT,
    form_id    UUID,                                  -- FK added in 0002 with forms
    expires_at TIMESTAMPTZ,
    status     TEXT NOT NULL DEFAULT 'active'
        CHECK (status IN ('active', 'disabled', 'deleted', 'legal_hold')),
    created_by UUID NOT NULL REFERENCES iam.users (id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT link_has_destination CHECK (target_url IS NOT NULL OR form_id IS NOT NULL)
);

-- The only hot-path query: resolve (host, code) -> destination.
CREATE UNIQUE INDEX links_lookup ON links.links (domain_id, lower(code));
CREATE INDEX links_by_project ON links.links (tenant_id, project_id, created_at DESC);
CREATE INDEX links_expiring ON links.links (expires_at)
    WHERE status = 'active' AND expires_at IS NOT NULL;

-- ---------------------------------------------------------------- Analytics

CREATE TABLE analytics.events (
    id              UUID NOT NULL,
    tenant_id       UUID NOT NULL,
    event_id        TEXT NOT NULL,
    type            TEXT NOT NULL
        CHECK (type IN ('click', 'form_view', 'form_page_view', 'form_start', 'submit')),
    link_id         UUID,
    form_id         UUID,
    form_version_id UUID,
    page_id         TEXT,
    visit_id        UUID,
    meta            JSONB NOT NULL DEFAULT '{}'::jsonb,
    occurred_at     TIMESTAMPTZ NOT NULL
) PARTITION BY RANGE (occurred_at);

-- Idempotency for at-least-once ingest; also the dedupe key for beacon retries.
CREATE UNIQUE INDEX events_event_id ON analytics.events (tenant_id, event_id, occurred_at);
CREATE INDEX events_rollup_scan ON analytics.events (occurred_at, tenant_id);

-- Creates the daily partition covering `day` if it does not exist yet.
CREATE OR REPLACE FUNCTION analytics.ensure_events_partition(day DATE)
RETURNS void
LANGUAGE plpgsql
AS $$
DECLARE
    part_name TEXT := format('events_%s', to_char(day, 'YYYYMMDD'));
BEGIN
    IF to_regclass('analytics.' || part_name) IS NOT NULL THEN
        RETURN;
    END IF;
    EXECUTE format(
        'CREATE TABLE analytics.%I PARTITION OF analytics.events FOR VALUES FROM (%L) TO (%L)',
        part_name, day, day + 1
    );
END;
$$;

DO $$
DECLARE
    d DATE := current_date - 1;
BEGIN
    WHILE d <= current_date + 7 LOOP
        PERFORM analytics.ensure_events_partition(d);
        d := d + 1;
    END LOOP;
END;
$$;

CREATE TABLE analytics.funnel_rollups (
    tenant_id  UUID NOT NULL,
    project_id UUID,
    form_id    UUID,
    link_id    UUID,
    bucket     TIMESTAMPTZ NOT NULL,
    clicks     INT NOT NULL DEFAULT 0,
    views      INT NOT NULL DEFAULT 0,
    starts     INT NOT NULL DEFAULT 0,
    submits    INT NOT NULL DEFAULT 0,
    PRIMARY KEY (tenant_id, bucket, form_id, link_id)
);

-- ---------------------------------------------------------------- Audit

CREATE TABLE audit.entries (
    tenant_id   UUID NOT NULL,
    seq         BIGINT NOT NULL,
    actor       JSONB NOT NULL,
    action      TEXT NOT NULL,
    target      JSONB NOT NULL,
    payload     JSONB,
    prev_hash   BYTEA NOT NULL,
    hash        BYTEA NOT NULL,
    occurred_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, seq)
);

CREATE TABLE audit.checkpoints (
    tenant_id  UUID NOT NULL,
    seq        BIGINT NOT NULL,
    hash       BYTEA NOT NULL,
    signature  BYTEA NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, seq)
);

-- ---------------------------------------------------------------- Core

CREATE TABLE core.idempotency_keys (
    tenant_id     UUID NOT NULL,
    scope         TEXT NOT NULL,
    key           TEXT NOT NULL,
    request_hash  BYTEA NOT NULL,
    status        TEXT NOT NULL CHECK (status IN ('PENDING', 'COMPLETED', 'FAILED')),
    response_body JSONB,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, scope, key)
);

CREATE TABLE core.outbox (
    id           BIGSERIAL PRIMARY KEY,
    tenant_id    UUID NOT NULL,
    topic        TEXT NOT NULL,
    payload      JSONB NOT NULL,
    available_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    attempts     INT NOT NULL DEFAULT 0,
    locked_until TIMESTAMPTZ,
    sent_at      TIMESTAMPTZ,
    last_error   TEXT
);

CREATE INDEX outbox_pending ON core.outbox (available_at) WHERE sent_at IS NULL;

-- ---------------------------------------------------------------- RLS

ALTER TABLE links.links ENABLE ROW LEVEL SECURITY;
ALTER TABLE links.domains ENABLE ROW LEVEL SECURITY;
ALTER TABLE iam.projects ENABLE ROW LEVEL SECURITY;
ALTER TABLE analytics.funnel_rollups ENABLE ROW LEVEL SECURITY;

-- current_setting(..., true) returns NULL when unset, which the policy treats as
-- "no tenant selected" -> no rows visible. Every tenant-scoped query must run
-- inside a transaction that has done SET LOCAL app.tenant_id.
CREATE POLICY tenant_isolation ON links.links
    USING (tenant_id::text = current_setting('app.tenant_id', true));
CREATE POLICY tenant_isolation ON links.domains
    USING (tenant_id::text = current_setting('app.tenant_id', true));
CREATE POLICY tenant_isolation ON iam.projects
    USING (tenant_id::text = current_setting('app.tenant_id', true));
CREATE POLICY tenant_isolation ON analytics.funnel_rollups
    USING (tenant_id::text = current_setting('app.tenant_id', true));

-- The redirect path is the one place that must read across tenants: it only knows
-- (host, code) and cannot set app.tenant_id before the lookup. Rather than granting
-- the app BYPASSRLS -- which would disable isolation everywhere -- it gets exactly
-- one SECURITY DEFINER function returning only the fields a redirect needs.
CREATE OR REPLACE FUNCTION links.resolve(p_host TEXT, p_code TEXT)
RETURNS TABLE (
    link_id    UUID,
    tenant_id  UUID,
    project_id UUID,
    target_url TEXT,
    form_id    UUID,
    status     TEXT,
    expires_at TIMESTAMPTZ
)
LANGUAGE sql
SECURITY DEFINER
SET search_path = links, pg_temp
STABLE
AS $$
    SELECT l.id, l.tenant_id, l.project_id, l.target_url, l.form_id, l.status, l.expires_at
    FROM links.links l
    JOIN links.domains d ON d.id = l.domain_id
    WHERE d.host = lower(p_host)
      AND lower(l.code) = lower(p_code);
$$;

-- ---------------------------------------------------------------- Grants
--
-- RLS policies do not apply to the table owner. The API server therefore connects
-- as collectr_app (created by deploy/postgres/init/10-roles.sql), for which the
-- policies above are enforced; the worker connects as the owner because its jobs
-- -- rollups, retention sweeps, partition maintenance -- are cross-tenant by
-- nature. Running both as the owner would leave the policies decorative.
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'collectr_app') THEN
        RAISE NOTICE 'role collectr_app not found; skipping grants (RLS will not be enforced)';
        RETURN;
    END IF;

    GRANT USAGE ON SCHEMA core, iam, links, analytics, audit TO collectr_app;
    GRANT SELECT, INSERT, UPDATE, DELETE
        ON ALL TABLES IN SCHEMA core, iam, links, analytics TO collectr_app;
    GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA core TO collectr_app;
    GRANT EXECUTE ON FUNCTION links.resolve(TEXT, TEXT) TO collectr_app;

    -- The audit trail is append-only for the application: no UPDATE, no DELETE.
    -- Tamper evidence is worth little if the process writing the log can rewrite it.
    GRANT SELECT, INSERT ON audit.entries TO collectr_app;
    GRANT SELECT ON audit.checkpoints TO collectr_app;
END;
$$;
