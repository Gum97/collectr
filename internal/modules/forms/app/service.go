// Package app implements form authoring, publishing and the submission grid.
package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/oklog/ulid/v2"

	"github.com/collectr/collectr/internal/contracts"
	"github.com/collectr/collectr/internal/modules/forms/domain"
	"github.com/collectr/collectr/internal/modules/forms/store"
)

// Repository is the persistence the service needs.
type Repository interface {
	CreateForm(ctx context.Context, f store.Form, draft domain.Schema) error
	GetForm(ctx context.Context, tenantID, formID uuid.UUID) (store.Form, error)
	GetDraft(ctx context.Context, tenantID, formID uuid.UUID) (domain.Schema, error)
	SaveDraft(ctx context.Context, tenantID, formID uuid.UUID, schema domain.Schema) error
	Publish(ctx context.Context, tenantID, formID, publishedBy uuid.UUID, schema domain.Schema,
		onPublished func(pgx.Tx, store.Version) error) (store.Version, error)
	GetVersion(ctx context.Context, tenantID, versionID uuid.UUID) (store.Version, error)
	ListVersions(ctx context.Context, tenantID, formID uuid.UUID) ([]store.Version, error)
	ResolvePublic(ctx context.Context, publicID string) (store.PublicForm, error)
	ListSubmissions(ctx context.Context, tenantID, formID uuid.UUID, before time.Time, limit int) ([]store.Submission, error)
	ListForms(ctx context.Context, tenantID uuid.UUID, projectID *uuid.UUID, limit int) ([]store.Summary, error)
}

// Service authors and serves forms.
type Service struct {
	repo             Repository
	defaultRetention time.Duration
	docs             contracts.DocumentProvider
	audit            contracts.AuditWriter
	opener           contracts.SensitiveOpener
}

// SetSensitiveOpener attaches the decryptor for sealed answers.
//
// Without it the grid cannot show a sensitive answer at all, and says so rather
// than reporting the question as unanswered.
func (s *Service) SetSensitiveOpener(o contracts.SensitiveOpener) { s.opener = o }

// NewService returns a Service.
func NewService(repo Repository, defaultRetention time.Duration) *Service {
	return &Service{repo: repo, defaultRetention: defaultRetention}
}

// SetAudit attaches the trail publishing is recorded in.
//
// A setter rather than a constructor argument, matching SetDocuments: the audit
// writer and this service are wired in the composition root, and the forms
// module reaches it only through the contract.
func (s *Service) SetAudit(w contracts.AuditWriter) { s.audit = w }

// ErrDraftInvalid means the draft cannot be published as it stands.
var ErrDraftInvalid = errors.New("draft schema is not publishable")

// CreateInput describes a new form.
type CreateInput struct {
	TenantID      uuid.UUID
	ProjectID     uuid.UUID
	CreatedBy     uuid.UUID
	Title         string
	RetentionDays *int
	Draft         domain.Schema
}

// Create makes a form with an initial draft. Nothing is publishable yet.
func (s *Service) Create(ctx context.Context, in CreateInput) (store.Form, error) {
	f := store.Form{
		ID:              uuid.New(),
		TenantID:        in.TenantID,
		ProjectID:       in.ProjectID,
		PublicID:        "fm_" + ulid.Make().String(),
		Title:           in.Title,
		Status:          "draft",
		RetentionDays:   in.RetentionDays,
		RetentionAction: "delete",
		CreatedBy:       in.CreatedBy,
	}
	// A form with no page has nowhere to put a question, so the builder renders
	// an empty column and the only affordance for adding one never appears. The
	// first page is part of what "a form" means, not something to make somebody
	// discover.
	draft := in.Draft
	if len(draft.Pages) == 0 {
		draft.Pages = []domain.Page{{ID: domain.PageID("pg_" + ulid.Make().String()), Title: "Trang 1"}}
	}
	if draft.Fields == nil {
		draft.Fields = map[domain.FieldID]domain.Field{}
	}

	if err := s.repo.CreateForm(ctx, f, draft); err != nil {
		return store.Form{}, err
	}
	return f, nil
}

// SaveDraft stores a working copy without validating it.
//
// Half-finished drafts are normal while building; validation belongs at publish
// time, where it can still block something that would break for respondents.
func (s *Service) SaveDraft(ctx context.Context, tenantID, formID uuid.UUID, schema domain.Schema) error {
	return s.repo.SaveDraft(ctx, tenantID, formID, schema)
}

// PublishPreview reports what publishing the current draft would do.
type PublishPreview struct {
	Validation domain.ValidationResult `json:"validation"`
	Diff       domain.DiffResult       `json:"diff"`
}

// Preview validates the draft and diffs it against the live version.
func (s *Service) Preview(ctx context.Context, tenantID, formID uuid.UUID) (PublishPreview, error) {
	draft, err := s.repo.GetDraft(ctx, tenantID, formID)
	if err != nil {
		return PublishPreview{}, err
	}

	preview := PublishPreview{Validation: domain.Validate(draft)}

	form, err := s.repo.GetForm(ctx, tenantID, formID)
	if err != nil {
		return PublishPreview{}, err
	}
	if form.LiveVersionID == nil {
		return preview, nil
	}
	live, err := s.repo.GetVersion(ctx, tenantID, *form.LiveVersionID)
	if err != nil {
		return PublishPreview{}, err
	}
	preview.Diff = domain.Diff(live.Schema, draft)
	return preview, nil
}

// Publish freezes the draft as a new immutable version.
//
// Validation blocks here and nowhere later. Once published, the version cannot
// be corrected -- and a respondent who hits a broken branch does not file a bug,
// they close the tab.
func (s *Service) Publish(ctx context.Context, tenantID, formID, publishedBy uuid.UUID, ipPrefix string) (store.Version, domain.ValidationResult, error) {
	draft, err := s.repo.GetDraft(ctx, tenantID, formID)
	if err != nil {
		return store.Version{}, domain.ValidationResult{}, err
	}
	if res := domain.Validate(draft); !res.OK {
		return store.Version{}, res, ErrDraftInvalid
	}

	v, err := s.repo.Publish(ctx, tenantID, formID, publishedBy, draft,
		func(tx pgx.Tx, version store.Version) error {
			if s.audit == nil {
				return nil
			}
			return s.audit.Write(ctx, tx, contracts.AuditEntry{
				TenantID: tenantID,
				Actor:    contracts.AuditActor{Type: "user", ID: publishedBy.String(), IPPrefix: ipPrefix},
				Action:   "form.published",
				Target:   map[string]any{"form_id": formID, "version_id": version.ID},
				Payload: map[string]any{
					"version_no": version.VersionNo,
					// The hash is what makes the entry evidence rather than a
					// note: it pins which bytes were published, so a later claim
					// about what a form asked can be checked against it.
					"schema_hash": fmt.Sprintf("sha256:%x", version.SchemaHash),
					"purposes":    purposeCodes(draft),
					"sensitive":   draft.HasSensitiveFields(),
				},
			})
		})
	if err != nil {
		return store.Version{}, domain.ValidationResult{}, err
	}
	return v, domain.ValidationResult{OK: true}, nil
}

// purposeCodes lists what a version declares it collects for, in schema order.
func purposeCodes(s domain.Schema) []string {
	out := make([]string, 0, len(s.Consent.Purposes))
	for _, p := range s.Consent.Purposes {
		out = append(out, p.Code)
	}
	return out
}

// PublicForm is a form ready to render, plus everything the client needs to
// submit against exactly the version it was shown.
type PublicForm struct {
	Form   store.PublicForm
	Schema domain.Schema
	// Consent is the text this form collects agreement against, when it declares
	// any purposes. The page must display it and return a digest of what it
	// displayed; without it here, no submission can carry the proof the server
	// requires, and none can be accepted.
	Consent *contracts.DocumentBody
}

// Public returns the live version of a form for rendering.
func (s *Service) Public(ctx context.Context, publicID string) (PublicForm, error) {
	pf, err := s.repo.ResolvePublic(ctx, publicID)
	if err != nil {
		return PublicForm{}, err
	}
	out := PublicForm{Form: pf, Schema: pf.Schema}

	// Only when the form actually asks for consent. A form declaring no purposes
	// collects nothing that needs one, and demanding a document for it would stop
	// it being answerable at all.
	if len(pf.Schema.Consent.Purposes) > 0 && s.docs != nil {
		doc, err := s.docs.ActiveDocumentBody(ctx, pf.TenantID, "consent_text")
		if err != nil {
			// Not fatal here. The submit path refuses without valid proof, so a
			// missing document fails at the point where refusing is the safe
			// answer rather than turning every page load into an error.
			// Nothing is logged here: this path has no logger, and the submit
			// handler already refuses and records the reason where it matters.
			_ = err
		} else {
			out.Consent = &doc
		}
	}
	return out, nil
}

// SetDocuments attaches the consent document source. Called at startup by the
// composition root, which is the only place that knows both modules.
func (s *Service) SetDocuments(d contracts.DocumentProvider) { s.docs = d }

// Versions returns every published version of a form.
func (s *Service) Versions(ctx context.Context, tenantID, formID uuid.UUID) ([]store.Version, error) {
	return s.repo.ListVersions(ctx, tenantID, formID)
}

// Grid is the submission table: one column set spanning every version, plus the
// rows.
type Grid struct {
	Columns []domain.Column `json:"columns"`
	Rows    []Row           `json:"rows"`
	Cursor  string          `json:"next_cursor,omitempty"`
}

// Row is one submission rendered for the grid.
type Row struct {
	ID          uuid.UUID       `json:"id"`
	VersionNo   int             `json:"form_version"`
	SubmittedAt time.Time       `json:"submitted_at"`
	Status      string          `json:"status"`
	Cells       map[string]Cell `json:"cells"`
}

// Cell is one answer with the state that explains an absent value.
type Cell struct {
	State string `json:"state"`
	Value any    `json:"value,omitempty"`
}

// Submissions builds the grid for a form.
//
// Columns are the union across every version, and each cell carries why it is
// empty. Merging the three kinds of emptiness into one blank would make the
// grid read as though respondents skipped questions they were never shown.
func (s *Service) Submissions(ctx context.Context, tenantID, formID uuid.UUID, before time.Time, limit int, revealSensitive bool) (Grid, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	if before.IsZero() {
		before = time.Now().UTC().Add(time.Minute)
	}

	versions, err := s.repo.ListVersions(ctx, tenantID, formID)
	if err != nil {
		return Grid{}, err
	}
	if len(versions) == 0 {
		return Grid{Columns: []domain.Column{}, Rows: []Row{}}, nil
	}

	vs := make([]domain.VersionedSchema, 0, len(versions))
	schemaByID := make(map[uuid.UUID]domain.Schema, len(versions))
	for _, v := range versions {
		vs = append(vs, domain.VersionedSchema{VersionNo: v.VersionNo, Schema: v.Schema})
		schemaByID[v.ID] = v.Schema
	}
	columns := domain.BuildColumnRegistry(vs)

	subs, err := s.repo.ListSubmissions(ctx, tenantID, formID, before, limit)
	if err != nil {
		return Grid{}, err
	}

	// Opened once for the page, and only when the caller asked to see them.
	// Decrypting on every grid load would spend a key unwrap per row for values
	// that are about to be replaced by dots, and would make an ordinary listing
	// indistinguishable from a sensitive read in the metrics.
	sensitive := map[uuid.UUID]map[string]any{}
	if revealSensitive && s.opener != nil {
		for _, sub := range subs {
			if len(sub.SensitiveBlob) == 0 || sub.SubjectID == nil {
				continue
			}
			opened, err := s.opener.OpenSensitive(ctx, tenantID, *sub.SubjectID, sub.ID, sub.SensitiveBlob)
			if err != nil {
				// One unreadable record must not blank the page. The cell falls
				// back to the masked state, which is honest: the value exists and
				// is not being shown.
				continue
			}
			sensitive[sub.ID] = opened
		}
	}

	grid := Grid{Columns: columns, Rows: make([]Row, 0, len(subs))}
	for _, sub := range subs {
		schema := schemaByID[sub.FormVersionID]
		row := Row{
			ID: sub.ID, VersionNo: sub.VersionNo, SubmittedAt: sub.SubmittedAt,
			Status: sub.Status, Cells: make(map[string]Cell, len(columns)),
		}
		for _, col := range columns {
			field, inSchema := schema.Fields[col.FieldID]
			// A column split by a type change only applies to the versions that
			// used that type; otherwise one submission would fill both halves.
			if inSchema && col.TypeVariant != "" && field.Type != col.TypeVariant {
				inSchema = false
			}
			// A sensitive answer is not in sub.Answers -- it is sealed in
			// answers_enc -- so the state has to be decided against the opened
			// copy. Deciding it against the plaintext column reported every
			// sensitive answer as CellBlank, and CellBlank is rendered as "the
			// respondent left this empty": a statement about a person who did
			// answer, and one an operator would act on.
			answers := sub.Answers
			if field.Sensitive && len(sensitive[sub.ID]) > 0 {
				answers = sensitive[sub.ID]
			}
			state := domain.CellState(inSchema, sub.VisibleFields, answers, col.FieldID)
			cell := Cell{State: state}
			if state == domain.CellAnswered {
				cell.Value = answers[string(col.FieldID)]
				// Reading a record and reading the sensitive data inside it are
				// separate permissions; the default is to mask.
				if field.Sensitive && !revealSensitive {
					cell.Value = "••••••"
				}
			}
			row.Cells[string(col.FieldID)] = cell
		}
		grid.Rows = append(grid.Rows, row)
	}
	// Only when the page came back full. Returning a cursor for a short page
	// tells the client there is more, so "Trang sau" stayed enabled on the last
	// page and one click produced an empty grid under a header still reading
	// "25 bản ghi đang hoạt động" -- the screen contradicting itself, with the
	// false half the more prominent one.
	if len(subs) == limit {
		grid.Cursor = subs[len(subs)-1].SubmittedAt.Format(time.RFC3339Nano)
	}
	return grid, nil
}

// RetentionFor returns when a submission taken now should be purged.
//
// Computed at submission time and stored, so that later policy changes never
// silently delete data gathered under the old one.
func (s *Service) RetentionFor(form store.Form, now time.Time) *time.Time {
	d := s.defaultRetention
	if form.RetentionDays != nil {
		d = time.Duration(*form.RetentionDays) * 24 * time.Hour
	}
	if d <= 0 {
		return nil
	}
	t := now.Add(d)
	return &t
}

// SortColumns orders columns for display, keeping retired ones last so the live
// questions stay together on the left.
func SortColumns(cols []domain.Column) []domain.Column {
	out := slices.Clone(cols)
	slices.SortStableFunc(out, func(a, b domain.Column) int {
		switch {
		case a.RetiredAfter == 0 && b.RetiredAfter != 0:
			return -1
		case a.RetiredAfter != 0 && b.RetiredAfter == 0:
			return 1
		default:
			return 0
		}
	})
	return out
}

// List returns a page of forms for the organisation, or for one project.
func (s *Service) List(ctx context.Context, tenantID uuid.UUID, projectID *uuid.UUID, limit int) ([]store.Summary, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	return s.repo.ListForms(ctx, tenantID, projectID, limit)
}

// Get returns one form's metadata.
func (s *Service) Get(ctx context.Context, tenantID, formID uuid.UUID) (store.Form, error) {
	return s.repo.GetForm(ctx, tenantID, formID)
}

// FormDetail is a form together with the schemas an editor needs to see.
type FormDetail struct {
	Form store.Form
	// Draft is the working copy; HasDraft says whether one exists at all. The
	// distinction matters to the builder: an absent draft and an empty draft
	// look identical in the schema alone, and saving over the wrong one deletes
	// every question.
	Draft       domain.Schema
	HasDraft    bool
	Live        *store.Version
	LiveVersion int
}

// Detail returns the form with its draft and live schema.
//
// The schemas belong on the admin endpoint because the alternative is what
// happened without it: screens reached for GET /api/pub/forms/{public_id} to
// read the fields, and that handler records a form_view. Opening an admin tab
// then added a phantom visitor to the funnel and quietly deflated the completion
// rate -- the exact failure the server-side view counter was introduced to stop.
func (s *Service) Detail(ctx context.Context, tenantID, formID uuid.UUID) (FormDetail, error) {
	form, err := s.repo.GetForm(ctx, tenantID, formID)
	if err != nil {
		return FormDetail{}, err
	}
	// A missing draft is a normal state, not a failure: a form published and
	// never edited since has nothing in the working copy. Treating ErrNoDraft as
	// an error here turned every such form into a 500 on a screen that only
	// wanted its title.
	draft, err := s.repo.GetDraft(ctx, tenantID, formID)
	if err != nil && !errors.Is(err, domain.ErrNoDraft) {
		return FormDetail{}, err
	}
	out := FormDetail{Form: form, Draft: draft, HasDraft: err == nil}
	if form.LiveVersionID != nil {
		live, err := s.repo.GetVersion(ctx, tenantID, *form.LiveVersionID)
		if err != nil {
			return FormDetail{}, err
		}
		out.Live = &live
		out.LiveVersion = live.VersionNo
	}
	return out, nil
}

// VersionPair reads two published versions of the same form, for comparison.
func (s *Service) VersionPair(ctx context.Context, tenantID, formID, aID, bID uuid.UUID) (store.Version, store.Version, error) {
	a, err := s.repo.GetVersion(ctx, tenantID, aID)
	if err != nil {
		return store.Version{}, store.Version{}, err
	}
	b, err := s.repo.GetVersion(ctx, tenantID, bID)
	if err != nil {
		return store.Version{}, store.Version{}, err
	}
	// Both must belong to the form in the path. Without this, two version ids
	// from anywhere would compare happily and leak one form's schema through
	// another form's URL.
	if a.FormID != formID || b.FormID != formID {
		return store.Version{}, store.Version{}, domain.ErrFormNotFound
	}
	return a, b, nil
}
