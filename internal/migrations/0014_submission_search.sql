-- Make the submissions grid searchable.
--
-- Every data subject request starts the same way: somebody rings up and reads
-- out a phone number. Until now the only filters were the two date boxes, so
-- the operator's first move had no answer -- including in the correction flow
-- built for exactly that phone call.
--
-- Two different lookups, because the data is stored two different ways.
--
-- The identifier (email or phone) is held as an HMAC in consent.data_subjects
-- and nowhere in readable form. That is deliberate: it stops the table being a
-- directory of everyone's contact details. It also means the only possible
-- match is an exact one, done by hashing the query and looking up the hash --
-- which needs no index here at all, and is why "0912 345" will never find
-- "0912345678".
--
-- Everything else the respondent typed -- name, company, address -- sits in the
-- plaintext answers column, and that is what this index is for. Searching it
-- discloses nothing new: those values are already on screen in the grid for
-- anyone holding submission.read. The search filters what they can already see.
--
-- Sensitive answers are searchable by nobody, including the owner. They live in
-- answers_enc, sealed under each subject's own key, and there is no key here to
-- open them with -- one row, one key, by design, because that is what makes
-- erasure a matter of destroying a key. The consequence has to be stated on
-- screen rather than left to be discovered: a search that quietly skips them
-- would answer "not found" about a record that exists.
CREATE EXTENSION IF NOT EXISTS pg_trgm;
CREATE EXTENSION IF NOT EXISTS unaccent;

-- unaccent is STABLE, not IMMUTABLE, so it cannot be used in a generated column
-- or an expression index directly. The two-argument form with the dictionary
-- named explicitly is deterministic, which is what the wrapper asserts.
--
-- The caveat, stated because it is invisible otherwise: if the unaccent
-- dictionary is ever replaced, stored values computed under the old one stay as
-- they were and the column has to be rebuilt. Nothing errors -- some names just
-- stop being findable.
CREATE OR REPLACE FUNCTION forms.immutable_unaccent(TEXT) RETURNS TEXT
    LANGUAGE sql IMMUTABLE PARALLEL SAFE STRICT
AS $$ SELECT public.unaccent('public.unaccent'::regdictionary, $1) $$;

-- The values only, never the field ids.
--
-- Indexing answers::text would have matched the keys too, so searching "mail"
-- would return every record on a form with an f_mail question -- confidently,
-- and with no way for the reader to tell why.
--
-- String answers only. A file answer is the object {"file_id": "..."}, and
-- putting those uuids in the haystack means a stray paste of one silently
-- matches a record by its attachment.
CREATE OR REPLACE FUNCTION forms.answers_text(a JSONB) RETURNS TEXT
    LANGUAGE sql IMMUTABLE PARALLEL SAFE STRICT
AS $$
    SELECT forms.immutable_unaccent(coalesce(string_agg(kv.value, ' '), ''))
    FROM jsonb_each_text(a) AS kv
    WHERE jsonb_typeof(a -> kv.key) = 'string'
$$;

-- Generated rather than maintained by the application: a column the writer has
-- to remember to update is a column that goes stale on the one path somebody
-- forgot, and the symptom would be a record that cannot be found rather than an
-- error anybody notices.
ALTER TABLE forms.submissions
    ADD COLUMN IF NOT EXISTS answers_text TEXT
    GENERATED ALWAYS AS (forms.answers_text(answers)) STORED;

-- Trigram, because the operator types a fragment of what the caller said.
-- A btree would only serve prefix matches, and "the surname is Nguyễn" is not a
-- prefix of anything useful.
--
-- Stored without diacritics, and the query is folded the same way before it is
-- compared. Vietnamese is typed both ways constantly -- an operator taking a
-- name down the phone types "Nguyen Van Duc", and a search that answers "not
-- found" because the record says "Nguyễn Văn Đức" is worse than no search.
CREATE INDEX IF NOT EXISTS submissions_answers_text_trgm
    ON forms.submissions USING gin (answers_text gin_trgm_ops);
