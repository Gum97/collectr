-- Let a revision say it was made by an employee fulfilling a request.
--
-- change_source allowed 'dsr_self_service' and 'admin_edit'. The second was
-- reserved for a path that was never built, and it says the wrong thing about
-- the one that now exists: an operator cannot open a record and edit it here.
-- The only way through is to raise a rectification request, name how the caller
-- was verified, and have the request and the correction commit together.
--
-- 'admin_edit' and 'dsr_operator' are kept apart on purpose. A year from now a
-- reader of the revision trail has to be able to tell "somebody with access
-- changed this" from "the subject asked us to change it and we did" -- those
-- are different facts, and collapsing them would make the more defensible one
-- unprovable.
--
-- 'admin_edit' stays permitted: no rows use it today, and removing a value the
-- schema has always allowed is a change that belongs with whatever eventually
-- writes it, not with this one.
ALTER TABLE forms.submission_revisions
    DROP CONSTRAINT IF EXISTS submission_revisions_change_source_check;

ALTER TABLE forms.submission_revisions
    ADD CONSTRAINT submission_revisions_change_source_check
    CHECK (change_source = ANY (ARRAY['dsr_self_service', 'admin_edit', 'dsr_operator']));
