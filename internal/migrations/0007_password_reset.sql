-- 0007_password_reset: recovering an account.
--
-- Until now a forgotten password was a dead end: an administrator cannot set
-- someone else's password (re-inviting an existing account deliberately does not
-- overwrite credentials), so the only recourse was psql.

CREATE TABLE iam.password_reset_tokens (
    id      UUID PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES iam.users (id) ON DELETE CASCADE,
    -- Hashed, like every other token here: a dump of this table must not be a
    -- set of live keys to every account.
    token_hash BYTEA NOT NULL UNIQUE,
    -- Recorded so an investigation can see where a reset was requested from.
    -- Prefix only; the full address is never stored anywhere in this system.
    requested_ip_prefix TEXT,
    expires_at TIMESTAMPTZ NOT NULL,
    used_at    TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX password_reset_expiry ON iam.password_reset_tokens (expires_at)
    WHERE used_at IS NULL;
CREATE INDEX password_reset_by_user ON iam.password_reset_tokens (user_id)
    WHERE used_at IS NULL;

-- A separate counter from login_attempts on purpose. Sharing one would let an
-- attacker lock a victim out of signing in simply by requesting resets for their
-- address -- turning a recovery feature into a denial of service.
CREATE TABLE iam.password_reset_attempts (
    email        CITEXT NOT NULL,
    window_start TIMESTAMPTZ NOT NULL,
    attempts     INT NOT NULL DEFAULT 1,
    PRIMARY KEY (email, window_start)
);

CREATE INDEX password_reset_attempts_cleanup ON iam.password_reset_attempts (window_start);

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'collectr_app') THEN
        RETURN;
    END IF;
    GRANT SELECT, INSERT, UPDATE, DELETE
        ON iam.password_reset_tokens, iam.password_reset_attempts
        TO collectr_app;
END;
$$;
