-- The redirect needs the form's public id, not its primary key.
--
-- A link attached to a form used to send visitors to /f/<uuid>, while the only
-- endpoint that can serve a form is keyed by public_id. Every such link therefore
-- led to a page that could not load its own schema. Nothing caught it because no
-- form page exists yet to fail on.
--
-- Returned from resolve() rather than fetched separately: the redirect is the hot
-- path, it already reads this row, and a second query per click to translate one
-- identifier into another would double its cost. The join lives here because this
-- function is already the one place allowed to read across tenants -- it runs
-- before any tenant is known -- so it is also the right place to read across
-- schemas. It could not be written in 0001 because forms.forms did not exist yet.
DROP FUNCTION IF EXISTS links.resolve(TEXT, TEXT);

CREATE FUNCTION links.resolve(p_host TEXT, p_code TEXT)
RETURNS TABLE (
    link_id         UUID,
    tenant_id       UUID,
    project_id      UUID,
    target_url      TEXT,
    form_id         UUID,
    form_public_id  TEXT,
    status          TEXT,
    expires_at      TIMESTAMPTZ
)
LANGUAGE sql
SECURITY DEFINER
SET search_path = links, forms, pg_temp
STABLE
AS $$
    SELECT l.id, l.tenant_id, l.project_id, l.target_url, l.form_id, f.public_id,
           l.status, l.expires_at
    FROM links.links l
    JOIN links.domains d ON d.id = l.domain_id
    -- LEFT, because most links point at an external URL and have no form at all.
    LEFT JOIN forms.forms f ON f.id = l.form_id
    WHERE d.host = lower(p_host)
      AND lower(l.code) = lower(p_code);
$$;

REVOKE ALL ON FUNCTION links.resolve(TEXT, TEXT) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION links.resolve(TEXT, TEXT) TO collectr_app;
