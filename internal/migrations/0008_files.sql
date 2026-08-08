-- 0008_files: attachments.
--
-- The `file` question type has existed in the schema since 0002 but publishing a
-- form containing one was blocked, because there was nowhere to put the bytes.
-- This migration and the files module together remove that block.

-- The docs described this table from the start, but no migration ever created
-- it: nothing referenced it while uploads were unimplemented, so nothing noticed.
CREATE SCHEMA IF NOT EXISTS files;

CREATE TABLE files.files (
    id         UUID PRIMARY KEY,
    tenant_id  UUID NOT NULL REFERENCES iam.tenants (id) ON DELETE CASCADE,
    project_id UUID REFERENCES iam.projects (id) ON DELETE CASCADE,

    form_version_id UUID REFERENCES forms.form_versions (id),
    field_id        TEXT,

    -- Metadata only. The bytes live in object storage; a database holding
    -- attachments makes dumps enormous and restores slow.
    storage_key   TEXT NOT NULL,
    original_name TEXT NOT NULL,
    -- Determined from the file's own leading bytes, never from what the client
    -- declared.
    content_type TEXT NOT NULL,
    size_bytes   BIGINT NOT NULL,
    checksum     BYTEA NOT NULL,

    encrypted BOOLEAN NOT NULL DEFAULT true,
    -- Per-file data key, wrapped under the deployment KEK. Erasing an attachment
    -- destroys this row's key, and the ciphertext -- including copies inside
    -- backups taken earlier -- stops being readable at that moment.
    --
    -- Per file rather than per data subject because an upload happens before the
    -- respondent has identified themselves: at that point there is no subject to
    -- key against yet.
    dek_wrapped BYTEA,

    submission_id UUID REFERENCES forms.submissions (id) ON DELETE CASCADE,
    status        TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'bound', 'erased')),

    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX files_by_submission ON files.files (submission_id)
    WHERE submission_id IS NOT NULL;
CREATE INDEX files_orphans ON files.files (created_at)
    WHERE status = 'pending' AND submission_id IS NULL;

ALTER TABLE files.files ENABLE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON files.files
    USING (tenant_id::text = current_setting('app.tenant_id', true));

-- Upload and download both know a file id, not a tenant: the respondent filling
-- in a public form has not identified themselves to any organisation. One narrow
-- SECURITY DEFINER function, rather than BYPASSRLS for the whole application.
CREATE OR REPLACE FUNCTION files.resolve_public(p_id UUID)
RETURNS TABLE (
    id            UUID,
    tenant_id     UUID,
    storage_key   TEXT,
    original_name TEXT,
    content_type  TEXT,
    size_bytes    BIGINT,
    dek_wrapped   BYTEA,
    status        TEXT,
    submission_id UUID
)
LANGUAGE sql
SECURITY DEFINER
SET search_path = files, pg_temp
STABLE
AS $$
    SELECT f.id, f.tenant_id, f.storage_key, f.original_name, f.content_type,
           f.size_bytes, f.dek_wrapped, f.status, f.submission_id
    FROM files.files f
    WHERE f.id = p_id AND f.status <> 'erased';
$$;

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'collectr_app') THEN
        RETURN;
    END IF;
    GRANT USAGE ON SCHEMA files TO collectr_app;
    GRANT SELECT, INSERT, UPDATE, DELETE ON files.files TO collectr_app;
    GRANT EXECUTE ON FUNCTION files.resolve_public(UUID) TO collectr_app;
END;
$$;
