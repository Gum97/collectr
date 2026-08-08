-- 0006_sessions: signing in.
--
-- Until now the only credential was an API key, which is deliberately barred from
-- the capabilities that need an accountable person behind them. The result was an
-- admin surface nobody could reach.

CREATE TABLE iam.sessions (
    id      UUID PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES iam.users (id) ON DELETE CASCADE,
    -- Sessions live in the database, not in a signed cookie. A stateless token
    -- cannot be revoked before it expires, and removing someone's access has to
    -- take effect when the decision is made, not up to an hour later.
    token_hash BYTEA NOT NULL UNIQUE,
    -- The tenant this session is acting within. Someone belonging to two
    -- organisations gets one session per organisation, so a capability can never
    -- leak sideways between them.
    tenant_id  UUID NOT NULL REFERENCES iam.tenants (id) ON DELETE CASCADE,
    ip_prefix  TEXT,
    user_agent TEXT,
    -- Set when a password is changed, a role is revoked, or the user signs out.
    revoked_at     TIMESTAMPTZ,
    revoked_reason TEXT,
    expires_at     TIMESTAMPTZ NOT NULL,
    last_seen_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX sessions_by_user ON iam.sessions (user_id) WHERE revoked_at IS NULL;
CREATE INDEX sessions_expiry ON iam.sessions (expires_at) WHERE revoked_at IS NULL;

CREATE TABLE iam.login_attempts (
    email        CITEXT NOT NULL,
    window_start TIMESTAMPTZ NOT NULL,
    attempts     INT NOT NULL DEFAULT 1,
    PRIMARY KEY (email, window_start)
);

CREATE INDEX login_attempts_cleanup ON iam.login_attempts (window_start);

CREATE TABLE iam.invitations (
    id         UUID PRIMARY KEY,
    tenant_id  UUID NOT NULL REFERENCES iam.tenants (id) ON DELETE CASCADE,
    email      CITEXT NOT NULL,
    org_role   TEXT NOT NULL CHECK (org_role IN ('owner', 'admin', 'member', 'dpo')),
    project_grants JSONB NOT NULL DEFAULT '[]'::jsonb,
    token_hash BYTEA NOT NULL UNIQUE,
    expires_at TIMESTAMPTZ NOT NULL,
    accepted_at TIMESTAMPTZ,
    invited_by UUID NOT NULL REFERENCES iam.users (id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX invitations_pending ON iam.invitations (tenant_id, email)
    WHERE accepted_at IS NULL;

-- Recovery codes for people who lose their phone. Without them, mandatory MFA
-- turns a lost device into a permanently locked organisation.
CREATE TABLE iam.mfa_recovery_codes (
    id        UUID PRIMARY KEY,
    user_id   UUID NOT NULL REFERENCES iam.users (id) ON DELETE CASCADE,
    code_hash BYTEA NOT NULL,
    used_at   TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX recovery_codes_by_user ON iam.mfa_recovery_codes (user_id) WHERE used_at IS NULL;

-- Revokes every live session for a user, in one statement, so that "you no
-- longer have access" is true the moment it is decided rather than whenever the
-- token happens to expire.
CREATE OR REPLACE FUNCTION iam.revoke_user_sessions(p_user_id UUID, p_reason TEXT)
RETURNS INT
LANGUAGE plpgsql
AS $$
DECLARE
    n INT;
BEGIN
    UPDATE iam.sessions
    SET revoked_at = now(), revoked_reason = p_reason
    WHERE user_id = p_user_id AND revoked_at IS NULL;
    GET DIAGNOSTICS n = ROW_COUNT;
    RETURN n;
END;
$$;

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'collectr_app') THEN
        RETURN;
    END IF;
    GRANT SELECT, INSERT, UPDATE, DELETE
        ON iam.sessions, iam.login_attempts, iam.invitations, iam.mfa_recovery_codes
        TO collectr_app;
    GRANT EXECUTE ON FUNCTION iam.revoke_user_sessions(UUID, TEXT) TO collectr_app;
END;
$$;
