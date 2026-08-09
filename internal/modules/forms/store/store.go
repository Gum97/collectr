// Package store persists forms, their immutable versions, and submissions.
package store

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/collectr/collectr/internal/modules/forms/domain"
	"github.com/collectr/collectr/internal/platform/postgres"
)

// Store reads and writes the forms schema.
type Store struct {
	db *postgres.DB
}

// New returns a Store backed by db.
func New(db *postgres.DB) *Store { return &Store{db: db} }

// Form is a form's metadata, without any schema.
type Form struct {
	ID              uuid.UUID
	TenantID        uuid.UUID
	ProjectID       uuid.UUID
	PublicID        string
	Title           string
	LiveVersionID   *uuid.UUID
	Status          string
	RetentionDays   *int
	RetentionAction string
	CreatedBy       uuid.UUID
	CreatedAt       time.Time
}

// Version is one published, immutable schema version.
type Version struct {
	ID          uuid.UUID
	FormID      uuid.UUID
	VersionNo   int
	Schema      domain.Schema
	SchemaHash  []byte
	PublishedAt time.Time
	PublishedBy uuid.UUID
	RetiredAt   *time.Time
}

// PublicForm is what the renderer needs to draw a form for a respondent.
type PublicForm struct {
	FormID    uuid.UUID
	TenantID  uuid.UUID
	Title     string
	VersionID uuid.UUID
	VersionNo int
	Schema    domain.Schema
}

// Submission is one stored response.
type Submission struct {
	ID            uuid.UUID
	FormID        uuid.UUID
	FormVersionID uuid.UUID
	VersionNo     int
	Answers       map[string]any
	VisibleFields []string
	Status        string
	SubmittedAt   time.Time
	// SubjectID and SensitiveBlob are what the grid needs to show a sensitive
	// answer. Answers holds only the plaintext column; a sensitive answer is
	// sealed separately, which is why a grid built from Answers alone reported
	// every one of them as unanswered.
	SubjectID     *uuid.UUID
	SensitiveBlob []byte
	// RevisionCount is how many times the answers have been corrected. Carried
	// on the row so the grid can show which records were changed after the fact
	// without a request per row -- that is the compliance-relevant signal, and
	// hiding it behind a click means nobody looks.
	RevisionCount int
}

// CreateForm inserts a form with its initial draft.
func (s *Store) CreateForm(ctx context.Context, f Form, draft domain.Schema) error {
	const q = `
		INSERT INTO forms.forms
			(id, tenant_id, project_id, public_id, title, draft_schema, status,
			 retention_days, retention_action, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, 'draft', $7, $8, $9)`

	err := s.db.InTenantTx(ctx, f.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, q, f.ID, f.TenantID, f.ProjectID, f.PublicID, f.Title,
			draft, f.RetentionDays, f.RetentionAction, f.CreatedBy)
		return err
	})
	if err != nil {
		return fmt.Errorf("creating form: %w", err)
	}
	return nil
}

// GetForm returns a form's metadata.
func (s *Store) GetForm(ctx context.Context, tenantID, formID uuid.UUID) (Form, error) {
	// The tenant is in the predicate, not only in the transaction setting.
	//
	// InTenantTx sets app.tenant_id, which row-level security enforces -- but
	// only for the API role. The worker connects as the database owner, which is
	// exempt, and the worker is what produces exports. Relying on RLS alone meant
	// a form id from another organisation returned that organisation's form.
	const q = `
		SELECT id, tenant_id, project_id, public_id, title, live_version_id, status,
		       retention_days, retention_action, created_by, created_at
		FROM forms.forms WHERE id = $1 AND tenant_id = $2`

	var f Form
	err := s.db.InTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, q, formID, tenantID).Scan(
			&f.ID, &f.TenantID, &f.ProjectID, &f.PublicID, &f.Title, &f.LiveVersionID,
			&f.Status, &f.RetentionDays, &f.RetentionAction, &f.CreatedBy, &f.CreatedAt)
	})
	if postgres.IsNoRows(err) {
		return Form{}, domain.ErrFormNotFound
	}
	if err != nil {
		return Form{}, fmt.Errorf("getting form: %w", err)
	}
	return f, nil
}

// GetDraft returns the working copy of a form's schema.
func (s *Store) GetDraft(ctx context.Context, tenantID, formID uuid.UUID) (domain.Schema, error) {
	const q = `SELECT draft_schema FROM forms.forms WHERE id = $1`

	var raw []byte
	err := s.db.InTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, q, formID).Scan(&raw)
	})
	if postgres.IsNoRows(err) {
		return domain.Schema{}, domain.ErrFormNotFound
	}
	if err != nil {
		return domain.Schema{}, fmt.Errorf("getting draft: %w", err)
	}
	if len(raw) == 0 {
		return domain.Schema{}, domain.ErrNoDraft
	}
	var schema domain.Schema
	if err := json.Unmarshal(raw, &schema); err != nil {
		return domain.Schema{}, fmt.Errorf("decoding draft schema: %w", err)
	}
	return schema, nil
}

// SaveDraft overwrites the working copy. Drafts are mutable by design; only
// published versions are frozen.
func (s *Store) SaveDraft(ctx context.Context, tenantID, formID uuid.UUID, schema domain.Schema) error {
	const q = `UPDATE forms.forms SET draft_schema = $2, updated_at = now() WHERE id = $1`

	err := s.db.InTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, q, formID, schema)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return domain.ErrFormNotFound
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("saving draft: %w", err)
	}
	return nil
}

// Publish freezes the draft as a new version and points the form at it.
//
// Version numbering is derived inside the transaction from the current maximum,
// so two concurrent publishes cannot mint the same number: the unique constraint
// on (form_id, version_no) is what ultimately decides.
// onPublished, when supplied, runs inside the publishing transaction with the
// version that was just written. It exists so the audit entry commits with the
// version or not at all.
func (s *Store) Publish(
	ctx context.Context,
	tenantID, formID, publishedBy uuid.UUID,
	schema domain.Schema,
	onPublished func(pgx.Tx, Version) error,
) (Version, error) {
	payload, err := json.Marshal(schema)
	if err != nil {
		return Version{}, fmt.Errorf("encoding schema: %w", err)
	}
	hash := sha256.Sum256(payload)

	var v Version
	err = s.db.InTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		var next int
		if err := tx.QueryRow(ctx,
			`SELECT coalesce(max(version_no), 0) + 1 FROM forms.form_versions WHERE form_id = $1`,
			formID,
		).Scan(&next); err != nil {
			return err
		}

		v = Version{
			ID: uuid.New(), FormID: formID, VersionNo: next,
			Schema: schema, SchemaHash: hash[:], PublishedBy: publishedBy,
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO forms.form_versions
				(id, tenant_id, form_id, version_no, schema, schema_hash, published_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			RETURNING published_at`,
			v.ID, tenantID, formID, next, schema, hash[:], publishedBy,
		).Scan(&v.PublishedAt); err != nil {
			return err
		}

		// The draft is cleared on publish so the builder cannot keep showing
		// edits that are already live under a different number.
		tag, err := tx.Exec(ctx, `
			UPDATE forms.forms
			SET live_version_id = $2, status = 'live', draft_schema = NULL, updated_at = now()
			WHERE id = $1`, formID, v.ID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return domain.ErrFormNotFound
		}
		// Runs inside the same transaction, so a trail that cannot be written is
		// a version that does not get published. Publishing mints an immutable
		// artifact that decides what every later respondent consents to; if that
		// can happen without a record of who did it, the chain has a hole exactly
		// where it matters most.
		if onPublished != nil {
			return onPublished(tx, v)
		}
		return nil
	})
	if err != nil {
		return Version{}, fmt.Errorf("publishing form: %w", err)
	}
	return v, nil
}

// GetVersion returns one published version.
func (s *Store) GetVersion(ctx context.Context, tenantID, versionID uuid.UUID) (Version, error) {
	const q = `
		SELECT id, form_id, version_no, schema, schema_hash, published_at, published_by, retired_at
		FROM forms.form_versions WHERE id = $1`

	var (
		v   Version
		raw []byte
	)
	err := s.db.InTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, q, versionID).Scan(
			&v.ID, &v.FormID, &v.VersionNo, &raw, &v.SchemaHash,
			&v.PublishedAt, &v.PublishedBy, &v.RetiredAt)
	})
	if postgres.IsNoRows(err) {
		return Version{}, domain.ErrVersionNotFound
	}
	if err != nil {
		return Version{}, fmt.Errorf("getting version: %w", err)
	}
	if err := json.Unmarshal(raw, &v.Schema); err != nil {
		return Version{}, fmt.Errorf("decoding version schema: %w", err)
	}
	return v, nil
}

// ListVersions returns every published version of a form, oldest first.
//
// The submission grid needs all of them: a column exists if any version ever
// asked the question.
func (s *Store) ListVersions(ctx context.Context, tenantID, formID uuid.UUID) ([]Version, error) {
	const q = `
		SELECT id, form_id, version_no, schema, schema_hash, published_at, published_by, retired_at
		FROM forms.form_versions
		WHERE form_id = $1
		ORDER BY version_no`

	var out []Version
	err := s.db.InTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, q, formID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var (
				v   Version
				raw []byte
			)
			if err := rows.Scan(&v.ID, &v.FormID, &v.VersionNo, &raw, &v.SchemaHash,
				&v.PublishedAt, &v.PublishedBy, &v.RetiredAt); err != nil {
				return err
			}
			if err := json.Unmarshal(raw, &v.Schema); err != nil {
				return fmt.Errorf("decoding schema of version %d: %w", v.VersionNo, err)
			}
			out = append(out, v)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("listing versions: %w", err)
	}
	return out, nil
}

// ResolvePublic returns the live version of a form by its public id.
func (s *Store) ResolvePublic(ctx context.Context, publicID string) (PublicForm, error) {
	const q = `
		SELECT form_id, tenant_id, title, status, version_id, version_no, schema, retired_at
		FROM forms.resolve_public($1)`

	var (
		pf        PublicForm
		status    string
		raw       []byte
		retiredAt *time.Time
	)
	err := s.db.QueryRow(ctx, q, publicID).Scan(
		&pf.FormID, &pf.TenantID, &pf.Title, &status,
		&pf.VersionID, &pf.VersionNo, &raw, &retiredAt)
	if postgres.IsNoRows(err) {
		return PublicForm{}, domain.ErrFormNotFound
	}
	if err != nil {
		return PublicForm{}, fmt.Errorf("resolving public form: %w", err)
	}
	if retiredAt != nil {
		return PublicForm{}, domain.ErrVersionRetired
	}
	if err := json.Unmarshal(raw, &pf.Schema); err != nil {
		return PublicForm{}, fmt.Errorf("decoding public schema: %w", err)
	}
	return pf, nil
}

// InsertSubmission stores one response.
//
// It takes a transaction rather than opening its own: from v0.3 the submission
// and its consent records must commit together or not at all.
func (s *Store) InsertSubmission(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, sub Submission, purgeAt *time.Time) error {
	const q = `
		INSERT INTO forms.submissions
			(id, tenant_id, form_id, form_version_id, answers, visible_fields, purge_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`

	if _, err := tx.Exec(ctx, q, sub.ID, tenantID, sub.FormID, sub.FormVersionID,
		sub.Answers, sub.VisibleFields, purgeAt); err != nil {
		return fmt.Errorf("inserting submission: %w", err)
	}
	return nil
}

// ListSubmissions returns a page of responses, newest first.
func (s *Store) ListSubmissions(ctx context.Context, tenantID, formID uuid.UUID, before time.Time, limit int, f SubmissionFilter) ([]Submission, error) {
	const q = `
		SELECT s.id, s.form_id, s.form_version_id, v.version_no, s.answers,
		       s.visible_fields, s.status, s.submitted_at,
		       s.data_subject_id, s.answers_enc,
		       (SELECT count(*) FROM forms.submission_revisions r
		         WHERE r.submission_id = s.id)
		FROM forms.submissions s
		JOIN forms.form_versions v ON v.id = s.form_version_id
		WHERE s.form_id = $1 AND s.status <> 'erased' AND s.submitted_at < $2
		  AND ($4 = '' OR s.answers_text ILIKE forms.immutable_unaccent($4) ESCAPE '\' OR s.data_subject_id = ANY($5))
		ORDER BY s.submitted_at DESC
		LIMIT $3`

	var out []Submission
	err := s.db.InTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, q, formID, before, limit, f.textPattern(), f.SubjectIDs)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var sub Submission
			if err := rows.Scan(&sub.ID, &sub.FormID, &sub.FormVersionID, &sub.VersionNo,
				&sub.Answers, &sub.VisibleFields, &sub.Status, &sub.SubmittedAt,
				&sub.SubjectID, &sub.SensitiveBlob, &sub.RevisionCount); err != nil {
				return err
			}
			out = append(out, sub)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("listing submissions: %w", err)
	}
	return out, nil
}

// DB exposes the pool so callers can compose a submission with other modules'
// writes inside one transaction.
func (s *Store) DB() *postgres.DB { return s.db }

// Revision is one recorded change to a submission's answers.
//
// The row stores the answers as they were *before* the change and nothing else.
// Reconstructing what actually changed is left to the caller, which needs the
// following state to do it -- see Service.Revisions.
type Revision struct {
	ID        uuid.UUID
	ChangedBy string
	Source    string
	Before    map[string]any
	CreatedAt time.Time
	// ActorEmail is empty when the change was made by the data subject rather
	// than a member, and when a member has since been deleted. Both are ordinary
	// and the reader is told which, rather than shown a bare uuid.
	ActorEmail string
	ActorName  string
}

// ListRevisions returns a submission's change history, oldest first.
//
// Ascending because the history is read as a sequence: each revision's "after"
// state is the next one's "before", and the last one's is the answers on file
// now. Descending would make that walk read backwards for no gain.
func (s *Store) ListRevisions(ctx context.Context, tenantID, submissionID uuid.UUID) ([]Revision, map[string]any, uuid.UUID, error) {
	// changed_by holds "user:<uuid>" or "subject:<uuid>", and only the first has
	// a name to look up.
	//
	// The uuid is extracted with a regex that yields NULL for anything else,
	// rather than stripping the prefix under a LIKE guard in the same ON clause.
	// Postgres does not promise to evaluate the guard first, and it did not: the
	// cast ran on a "subject:" row and the whole query failed with an invalid
	// uuid. It passed every test until a subject actually corrected something,
	// because until then every row began with "user:".
	const q = `
		SELECT r.id, r.changed_by, r.change_source, r.answers_before, r.changed_at,
		       coalesce(u.email, ''), coalesce(u.name, '')
		FROM forms.submission_revisions r
		LEFT JOIN iam.users u
		       ON u.id = substring(r.changed_by from
		              '^user:([0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})$')::uuid
		WHERE r.submission_id = $1
		ORDER BY r.changed_at ASC, r.id ASC`

	var revs []Revision
	var current map[string]any
	var formID uuid.UUID
	err := s.db.InTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		// The current answers come from the same transaction as the revisions.
		// Read separately they could straddle a concurrent correction, and the
		// screen would show a change that never happened: the newest revision's
		// "after" would be diffed against answers already moved on from it.
		err := tx.QueryRow(ctx,
			`SELECT answers, form_id FROM forms.submissions WHERE id = $1`,
			submissionID).Scan(&current, &formID)
		if postgres.IsNoRows(err) {
			return domain.ErrSubmissionNotFound
		}
		if err != nil {
			return err
		}

		rows, err := tx.Query(ctx, q, submissionID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r Revision
			if err := rows.Scan(&r.ID, &r.ChangedBy, &r.Source, &r.Before,
				&r.CreatedAt, &r.ActorEmail, &r.ActorName); err != nil {
				return err
			}
			revs = append(revs, r)
		}
		return rows.Err()
	})
	if err != nil {
		if errors.Is(err, domain.ErrSubmissionNotFound) {
			return nil, nil, uuid.Nil, err
		}
		return nil, nil, uuid.Nil, fmt.Errorf("listing revisions: %w", err)
	}
	return revs, current, formID, nil
}

// SubmissionFilter narrows the grid.
type SubmissionFilter struct {
	// Text is matched against the answers the respondent typed, ignoring case
	// and diacritics. Both sides are folded: the column when it is generated and
	// the query in the WHERE clause. Folding one side only would mean typing the
	// name correctly, with its accents, is the way to fail to find it. Empty means no text condition at all -- not "match the
	// empty string", which would be every row.
	Text string
	// SubjectIDs are subjects whose identifier matched the query exactly. The
	// identifier is only ever stored as an HMAC, so it can be matched but never
	// scanned: hashing "0912 345" produces a hash of "0912 345", not a prefix of
	// anything. Kept beside the text condition and OR-ed with it so a phone
	// number finds the record whether it was the identifier or just an answer.
	SubjectIDs []uuid.UUID
}

// textPattern renders Text as an ILIKE pattern with the wildcards escaped.
//
// Without escaping, a "%" typed into the search box matches every record, and
// "_" matches any character -- so a search would silently return rows that do
// not contain what was typed. Both appear in real input; "%" is in more email
// addresses than one would like.
func (f SubmissionFilter) textPattern() string {
	if strings.TrimSpace(f.Text) == "" {
		return ""
	}
	r := strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`)
	return "%" + r.Replace(strings.TrimSpace(f.Text)) + "%"
}
