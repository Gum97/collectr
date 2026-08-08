-- Runs once, on first container start, before any migration.
--
-- Two roles, on purpose:
--
--   collectr        owner. Runs migrations and the worker's cross-tenant jobs
--                   (rollups, retention sweeps, partition maintenance).
--   collectr_app    the API server. Row-level security policies apply to it
--                   because it does not own the tables -- which is the only
--                   configuration in which those policies actually do anything.
--
-- The password is replaced at first boot by the value of APP_DB_PASSWORD.

\set app_password `echo "${APP_DB_PASSWORD:-collectr_app_dev}"`

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'collectr_app') THEN
        CREATE ROLE collectr_app LOGIN;
    END IF;
END;
$$;

ALTER ROLE collectr_app WITH PASSWORD :'app_password';

-- Least privilege: the application cannot create or drop schema objects.
REVOKE ALL ON SCHEMA public FROM collectr_app;
ALTER ROLE collectr_app SET statement_timeout = '15s';
ALTER ROLE collectr_app SET idle_in_transaction_session_timeout = '30s';

-- The schemas must exist before default privileges can be attached to them;
-- migration 0001 creates them again with IF NOT EXISTS.
CREATE SCHEMA IF NOT EXISTS core;
CREATE SCHEMA IF NOT EXISTS iam;
CREATE SCHEMA IF NOT EXISTS links;
CREATE SCHEMA IF NOT EXISTS analytics;
CREATE SCHEMA IF NOT EXISTS audit;

-- Objects created by later migrations inherit these grants automatically.
ALTER DEFAULT PRIVILEGES IN SCHEMA core, iam, links, analytics
    GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO collectr_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA core
    GRANT USAGE, SELECT ON SEQUENCES TO collectr_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA audit
    GRANT SELECT, INSERT ON TABLES TO collectr_app;
