-- 0003_consent: lawful basis, evidence, and the tamper-evident audit chain.
--
-- This is the migration the rest of the system exists to serve. From here on a
-- submission cannot be written without its consent record: the two are inserted
-- in one transaction, and the endpoint that accepts responses only appears now.

CREATE SCHEMA IF NOT EXISTS consent;

CREATE TABLE consent.purposes (
    id          UUID PRIMARY KEY,
    tenant_id   UUID NOT NULL REFERENCES iam.tenants (id) ON DELETE CASCADE,
    code        TEXT NOT NULL,
    name        TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    -- Consent is one lawful basis among several. Recording which one applies is
    -- what makes "we may keep this" answerable later.
    legal_basis TEXT NOT NULL DEFAULT 'consent'
        CHECK (legal_basis IN ('consent', 'contract', 'legal_obligation', 'vital_interest')),
    retention_days INT,
    is_required    BOOLEAN NOT NULL DEFAULT false,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, code)
);

CREATE TABLE consent.documents (
    id         UUID PRIMARY KEY,
    tenant_id  UUID NOT NULL REFERENCES iam.tenants (id) ON DELETE CASCADE,
    kind       TEXT NOT NULL CHECK (kind IN ('privacy_notice', 'consent_text')),
    version_no INT NOT NULL,
    body_html  TEXT NOT NULL,
    -- sha256 of body_html. The client returns the hash of what it actually
    -- rendered; a mismatch means the text on screen was not this text, and the
    -- submission is refused. Without this check the "evidence" is only the
    -- server's word for it.
    content_hash   BYTEA NOT NULL,
    effective_from TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_by     UUID NOT NULL REFERENCES iam.users (id),
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, kind, version_no)
);

CREATE OR REPLACE FUNCTION consent.reject_document_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'consent documents are immutable and cannot be deleted (%)', OLD.id;
    END IF;
    RAISE EXCEPTION 'consent document % is published and immutable; publish a new version instead', OLD.id;
END;
$$;

CREATE TRIGGER consent_documents_immutable
    BEFORE UPDATE OR DELETE ON consent.documents
    FOR EACH ROW EXECUTE FUNCTION consent.reject_document_mutation();

CREATE TABLE consent.data_subjects (
    id        UUID PRIMARY KEY,
    tenant_id UUID NOT NULL REFERENCES iam.tenants (id) ON DELETE CASCADE,
    -- HMAC, not the raw value: the table can answer "is this the same person"
    -- without itself being a directory of everyone's email and phone number.
    identifier_hash BYTEA NOT NULL,
    identifier_kind TEXT NOT NULL CHECK (identifier_kind IN ('email', 'phone')),
    -- The data subject's own encryption key, wrapped by the deployment KEK.
    -- Destroying this row's key renders their sensitive data unreadable
    -- everywhere it exists, including in any backup taken after the key was destroyed.
    dek_wrapped BYTEA,
    erased_at   TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, identifier_kind, identifier_hash)
);

CREATE TABLE consent.records (
    id              UUID PRIMARY KEY,
    tenant_id       UUID NOT NULL,
    data_subject_id UUID NOT NULL REFERENCES consent.data_subjects (id) ON DELETE CASCADE,
    purpose_id      UUID NOT NULL REFERENCES consent.purposes (id),
    submission_id   UUID,
    form_version_id UUID,
    -- Withdrawal appends a row. Having once agreed is a historical fact, and the
    -- controller still has to be able to show it for the period it applied.
    action      TEXT NOT NULL CHECK (action IN ('granted', 'withdrawn')),
    document_id UUID NOT NULL REFERENCES consent.documents (id),
    evidence    JSONB NOT NULL,
    occurred_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX consent_records_subject
    ON consent.records (tenant_id, data_subject_id, purpose_id, occurred_at DESC);
CREATE INDEX consent_records_submission ON consent.records (submission_id)
    WHERE submission_id IS NOT NULL;

CREATE OR REPLACE FUNCTION consent.reject_record_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'consent records are append-only; record a withdrawal instead of editing (%)', OLD.id;
END;
$$;

CREATE TRIGGER consent_records_append_only
    BEFORE UPDATE OR DELETE ON consent.records
    FOR EACH ROW EXECUTE FUNCTION consent.reject_record_mutation();

-- Derived view of the latest state per (subject, purpose). consent.records stays
-- the source of truth; this exists so the hot path does not run a window
-- function on every check.
CREATE TABLE consent.current_consents (
    tenant_id       UUID NOT NULL,
    data_subject_id UUID NOT NULL REFERENCES consent.data_subjects (id) ON DELETE CASCADE,
    purpose_id      UUID NOT NULL REFERENCES consent.purposes (id),
    granted         BOOLEAN NOT NULL,
    last_record_id  UUID NOT NULL,
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, data_subject_id, purpose_id)
);

CREATE TABLE consent.dsr_requests (
    id              UUID PRIMARY KEY,
    tenant_id       UUID NOT NULL REFERENCES iam.tenants (id) ON DELETE CASCADE,
    data_subject_id UUID NOT NULL REFERENCES consent.data_subjects (id) ON DELETE CASCADE,
    type            TEXT NOT NULL
        CHECK (type IN ('access', 'rectify', 'erase', 'restrict', 'withdraw', 'export', 'object')),
    status TEXT NOT NULL DEFAULT 'received'
        CHECK (status IN ('received', 'verified', 'in_progress', 'fulfilled', 'rejected')),
    verification_method TEXT,
    received_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- received_at + the configured SLA, stored rather than computed so that
    -- changing the configuration cannot retroactively move a live deadline.
    due_at          TIMESTAMPTZ NOT NULL,
    fulfilled_at    TIMESTAMPTZ,
    handled_by      UUID REFERENCES iam.users (id),
    resolution_note TEXT,
    artifact_key    TEXT
);

-- Feeds the alert that matters most in the whole system: requests past their
-- statutory deadline.
CREATE INDEX dsr_open ON consent.dsr_requests (tenant_id, status, due_at)
    WHERE status <> 'fulfilled';

ALTER TABLE consent.purposes ENABLE ROW LEVEL SECURITY;
ALTER TABLE consent.documents ENABLE ROW LEVEL SECURITY;
ALTER TABLE consent.data_subjects ENABLE ROW LEVEL SECURITY;
ALTER TABLE consent.records ENABLE ROW LEVEL SECURITY;
ALTER TABLE consent.current_consents ENABLE ROW LEVEL SECURITY;
ALTER TABLE consent.dsr_requests ENABLE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON consent.purposes
    USING (tenant_id::text = current_setting('app.tenant_id', true));
CREATE POLICY tenant_isolation ON consent.documents
    USING (tenant_id::text = current_setting('app.tenant_id', true));
CREATE POLICY tenant_isolation ON consent.data_subjects
    USING (tenant_id::text = current_setting('app.tenant_id', true));
CREATE POLICY tenant_isolation ON consent.records
    USING (tenant_id::text = current_setting('app.tenant_id', true));
CREATE POLICY tenant_isolation ON consent.current_consents
    USING (tenant_id::text = current_setting('app.tenant_id', true));
CREATE POLICY tenant_isolation ON consent.dsr_requests
    USING (tenant_id::text = current_setting('app.tenant_id', true));

-- The public policy permalink, like the redirect, knows an id and not a tenant.
-- Serving the document from an immutable row -- rather than letting a form link
-- to any URL the tenant happens to control -- is what keeps the text that was
-- shown reconstructable after the fact.
CREATE OR REPLACE FUNCTION consent.public_document(p_id UUID)
RETURNS TABLE (
    id           UUID,
    kind         TEXT,
    version_no   INT,
    body_html    TEXT,
    content_hash BYTEA,
    created_at   TIMESTAMPTZ
)
LANGUAGE sql
SECURITY DEFINER
SET search_path = consent, pg_temp
STABLE
AS $$
    SELECT d.id, d.kind, d.version_no, d.body_html, d.content_hash, d.created_at
    FROM consent.documents d
    WHERE d.id = p_id;
$$;

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'collectr_app') THEN
        RETURN;
    END IF;
    GRANT USAGE ON SCHEMA consent TO collectr_app;
    GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA consent TO collectr_app;
    GRANT EXECUTE ON FUNCTION consent.public_document(UUID) TO collectr_app;
END;
$$;
