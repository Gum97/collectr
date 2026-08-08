-- 0002_forms: forms, immutable versions, submissions.
--
-- Consent tables arrive in 0003. Until then the public submit endpoint stays
-- unexposed: a submission written without its consent record would violate the
-- invariant the whole design rests on, and shipping the endpoint first would
-- make that violation the default.

CREATE SCHEMA IF NOT EXISTS forms;

CREATE TABLE forms.forms (
    id         UUID PRIMARY KEY,
    tenant_id  UUID NOT NULL REFERENCES iam.tenants (id) ON DELETE CASCADE,
    project_id UUID NOT NULL REFERENCES iam.projects (id) ON DELETE CASCADE,
    public_id  TEXT NOT NULL UNIQUE,
    title      TEXT NOT NULL,

    live_version_id UUID,
    draft_schema    JSONB,

    status TEXT NOT NULL DEFAULT 'draft'
        CHECK (status IN ('draft', 'live', 'closed', 'archived')),

    -- Retention is a property of the form because the lawful basis for keeping
    -- an answer belongs to the question that asked it, not to the workspace.
    retention_days   INT,
    retention_action TEXT NOT NULL DEFAULT 'delete'
        CHECK (retention_action IN ('delete', 'anonymize')),

    created_by UUID NOT NULL REFERENCES iam.users (id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX forms_by_project ON forms.forms (tenant_id, project_id, created_at DESC);

CREATE TABLE forms.form_versions (
    id         UUID PRIMARY KEY,
    tenant_id  UUID NOT NULL,
    form_id    UUID NOT NULL REFERENCES forms.forms (id) ON DELETE CASCADE,
    version_no INT NOT NULL,

    -- Immutable once written. Reconstructing exactly what a respondent saw --
    -- for a dispute about what they agreed to -- depends on this never changing.
    schema      JSONB NOT NULL,
    schema_hash BYTEA NOT NULL,

    consent_document_id UUID,

    published_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    published_by UUID NOT NULL REFERENCES iam.users (id),
    -- Set only when a version must be withdrawn urgently; it does not delete
    -- anything, it just stops new submissions being accepted against it.
    retired_at TIMESTAMPTZ,

    UNIQUE (form_id, version_no)
);

CREATE INDEX form_versions_by_form ON forms.form_versions (form_id, version_no DESC);

ALTER TABLE forms.forms
    ADD CONSTRAINT forms_live_version_fk
    FOREIGN KEY (live_version_id) REFERENCES forms.form_versions (id);

-- Published versions are immutable, enforced rather than documented: a trigger
-- survives an ORM change, a new endpoint, and a well-meaning migration.
CREATE OR REPLACE FUNCTION forms.reject_version_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'form versions are immutable and cannot be deleted (version %)', OLD.id;
    END IF;
    IF NEW.schema IS DISTINCT FROM OLD.schema
       OR NEW.schema_hash IS DISTINCT FROM OLD.schema_hash
       OR NEW.version_no IS DISTINCT FROM OLD.version_no
       OR NEW.form_id IS DISTINCT FROM OLD.form_id
       OR NEW.consent_document_id IS DISTINCT FROM OLD.consent_document_id THEN
        RAISE EXCEPTION 'form version % is published and immutable; publish a new version instead', OLD.id;
    END IF;
    RETURN NEW;   -- retired_at may still be set
END;
$$;

CREATE TRIGGER form_versions_immutable
    BEFORE UPDATE OR DELETE ON forms.form_versions
    FOR EACH ROW EXECUTE FUNCTION forms.reject_version_mutation();

CREATE TABLE forms.submissions (
    id              UUID PRIMARY KEY,
    tenant_id       UUID NOT NULL,
    form_id         UUID NOT NULL REFERENCES forms.forms (id) ON DELETE CASCADE,
    -- Pinned, so the row stays self-describing for as long as it is kept.
    form_version_id UUID NOT NULL REFERENCES forms.form_versions (id),
    data_subject_id UUID,

    answers JSONB NOT NULL,
    -- Sensitive answers, encrypted with the data subject's own key so that
    -- destroying that key erases them everywhere, backups included.
    answers_enc BYTEA,

    -- The fields actually shown, computed by the server. Without it, the grid
    -- cannot tell "never asked" from "hidden by a branch" from "left blank",
    -- and every completion statistic on a branching form becomes wrong.
    visible_fields TEXT[] NOT NULL DEFAULT '{}',

    visit_id UUID,
    meta     JSONB NOT NULL DEFAULT '{}'::jsonb,

    status TEXT NOT NULL DEFAULT 'active'
        CHECK (status IN ('active', 'restricted', 'erased')),

    submitted_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Computed at submission time from the policy then in force, so changing the
    -- policy later never silently deletes data collected under the old one.
    purge_at TIMESTAMPTZ
);

CREATE INDEX submissions_grid ON forms.submissions (tenant_id, form_id, submitted_at DESC);
CREATE INDEX submissions_by_subject ON forms.submissions (data_subject_id)
    WHERE data_subject_id IS NOT NULL;
CREATE INDEX submissions_purge ON forms.submissions (purge_at)
    WHERE status = 'active' AND purge_at IS NOT NULL;
CREATE INDEX submissions_answers ON forms.submissions USING GIN (answers jsonb_path_ops);

CREATE TABLE forms.submission_revisions (
    id             UUID PRIMARY KEY,
    tenant_id      UUID NOT NULL,
    submission_id  UUID NOT NULL REFERENCES forms.submissions (id) ON DELETE CASCADE,
    answers_before JSONB NOT NULL,
    changed_by     TEXT NOT NULL,
    change_source  TEXT NOT NULL
        CHECK (change_source IN ('dsr_self_service', 'admin_edit')),
    changed_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX submission_revisions_by_submission
    ON forms.submission_revisions (submission_id, changed_at DESC);

-- links.form_id could not be a foreign key in 0001 because forms did not exist.
ALTER TABLE links.links
    ADD CONSTRAINT links_form_fk
    FOREIGN KEY (form_id) REFERENCES forms.forms (id) ON DELETE SET NULL;

ALTER TABLE forms.forms ENABLE ROW LEVEL SECURITY;
ALTER TABLE forms.form_versions ENABLE ROW LEVEL SECURITY;
ALTER TABLE forms.submissions ENABLE ROW LEVEL SECURITY;
ALTER TABLE forms.submission_revisions ENABLE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON forms.forms
    USING (tenant_id::text = current_setting('app.tenant_id', true));
CREATE POLICY tenant_isolation ON forms.form_versions
    USING (tenant_id::text = current_setting('app.tenant_id', true));
CREATE POLICY tenant_isolation ON forms.submissions
    USING (tenant_id::text = current_setting('app.tenant_id', true));
CREATE POLICY tenant_isolation ON forms.submission_revisions
    USING (tenant_id::text = current_setting('app.tenant_id', true));

-- Public form rendering has the same problem as the redirect: it knows a public
-- id, not a tenant. One narrow SECURITY DEFINER function, rather than BYPASSRLS
-- for the whole application.
CREATE OR REPLACE FUNCTION forms.resolve_public(p_public_id TEXT)
RETURNS TABLE (
    form_id         UUID,
    tenant_id       UUID,
    title           TEXT,
    status          TEXT,
    version_id      UUID,
    version_no      INT,
    schema          JSONB,
    retired_at      TIMESTAMPTZ
)
LANGUAGE sql
SECURITY DEFINER
SET search_path = forms, pg_temp
STABLE
AS $$
    SELECT f.id, f.tenant_id, f.title, f.status,
           v.id, v.version_no, v.schema, v.retired_at
    FROM forms.forms f
    JOIN forms.form_versions v ON v.id = f.live_version_id
    WHERE f.public_id = p_public_id
      AND f.status = 'live';
$$;

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'collectr_app') THEN
        RETURN;
    END IF;
    GRANT USAGE ON SCHEMA forms TO collectr_app;
    GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA forms TO collectr_app;
    GRANT EXECUTE ON FUNCTION forms.resolve_public(TEXT) TO collectr_app;
END;
$$;
