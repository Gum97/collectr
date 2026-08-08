-- 0005_dsr: the data subject's own way in.
--
-- Every right in the law is worthless if exercising it means emailing someone and
-- hoping. This migration adds the tables behind a portal the subject drives
-- themselves: prove control of an email or phone, see what is held, correct it,
-- or have it erased.

-- Single-use tokens for proving control of an identifier.
CREATE TABLE consent.dsr_tokens (
    id         UUID PRIMARY KEY,
    tenant_id  UUID NOT NULL REFERENCES iam.tenants (id) ON DELETE CASCADE,
    -- Stored hashed. A leaked backup of this table must not hand over live
    -- access to anyone's personal data.
    token_hash BYTEA NOT NULL UNIQUE,
    data_subject_id UUID NOT NULL REFERENCES consent.data_subjects (id) ON DELETE CASCADE,
    -- Narrow tokens exist so the receipt link handed out at submission time
    -- cannot be used to enumerate everything else that person ever submitted.
    scope         TEXT NOT NULL DEFAULT 'portal' CHECK (scope IN ('portal', 'receipt')),
    submission_id UUID REFERENCES forms.submissions (id) ON DELETE CASCADE,
    expires_at    TIMESTAMPTZ NOT NULL,
    used_at       TIMESTAMPTZ,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT receipt_needs_submission
        CHECK (scope <> 'receipt' OR submission_id IS NOT NULL)
);

CREATE INDEX dsr_tokens_expiry ON consent.dsr_tokens (expires_at) WHERE used_at IS NULL;

-- Rate limiting for the identify endpoint, kept in the database rather than in
-- Redis: this is the one limiter that must survive a cache outage, because
-- failing open here means handing an attacker an oracle for which phone numbers
-- a company holds.
CREATE TABLE consent.dsr_attempts (
    tenant_id       UUID NOT NULL,
    identifier_hash BYTEA NOT NULL,
    window_start    TIMESTAMPTZ NOT NULL,
    attempts        INT NOT NULL DEFAULT 1,
    PRIMARY KEY (tenant_id, identifier_hash, window_start)
);

CREATE INDEX dsr_attempts_cleanup ON consent.dsr_attempts (window_start);

ALTER TABLE consent.dsr_tokens ENABLE ROW LEVEL SECURITY;
ALTER TABLE consent.dsr_attempts ENABLE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON consent.dsr_tokens
    USING (tenant_id::text = current_setting('app.tenant_id', true));
CREATE POLICY tenant_isolation ON consent.dsr_attempts
    USING (tenant_id::text = current_setting('app.tenant_id', true));

-- Erasure keeps the consent record but severs it from the person.
--
-- Two duties collide here: the right to erasure, and the controller's duty to be
-- able to show consent was obtained for the period it applied. The resolution is
-- to keep the record in a form that can no longer be traced to a human -- the
-- identifier hash is overwritten and the data key destroyed, so nothing links
-- the row to an email address or phone number any more.
CREATE OR REPLACE FUNCTION consent.erase_subject(p_subject_id UUID)
RETURNS TABLE (submissions_deleted INT, files_deleted INT)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = consent, forms, pg_temp
AS $$
DECLARE
    v_subs INT := 0;
    v_files INT := 0;
BEGIN
    DELETE FROM forms.submissions WHERE data_subject_id = p_subject_id;
    GET DIAGNOSTICS v_subs = ROW_COUNT;

    -- Destroying the wrapped data key is what makes this irreversible: every
    -- copy of the ciphertext, including copies inside backups taken before this
    -- moment, becomes undecryptable.
    UPDATE consent.data_subjects
    SET dek_wrapped     = NULL,
        identifier_hash = sha256(id::text::bytea),
        erased_at       = now()
    WHERE id = p_subject_id;

    RETURN QUERY SELECT v_subs, v_files;
END;
$$;

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'collectr_app') THEN
        RETURN;
    END IF;
    GRANT SELECT, INSERT, UPDATE, DELETE ON consent.dsr_tokens, consent.dsr_attempts TO collectr_app;
    GRANT EXECUTE ON FUNCTION consent.erase_subject(UUID) TO collectr_app;
END;
$$;
