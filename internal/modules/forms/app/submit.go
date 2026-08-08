package app

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/collectr/collectr/internal/contracts"
	"github.com/collectr/collectr/internal/modules/forms/domain"
	"github.com/collectr/collectr/internal/modules/forms/engine"
	"github.com/collectr/collectr/internal/modules/forms/store"
	"github.com/collectr/collectr/internal/platform/crypto"
	"github.com/collectr/collectr/internal/platform/postgres"
)

// Submission errors, each mapping to a distinct response.
var (
	ErrVersionMismatch  = errors.New("submitted against a version that is no longer accepted")
	ErrConsentMismatch  = errors.New("consent text has changed since the form was rendered")
	ErrConsentMissing   = errors.New("a required processing purpose was not agreed to")
	ErrNoIdentifier     = errors.New("form has no identifier field")
	ErrDuplicateRequest = errors.New("this request was already processed")
)

// FieldErrors reports per-field validation problems.
type FieldErrors map[string]string

func (f FieldErrors) Error() string {
	return fmt.Sprintf("validation failed for %d field(s)", len(f))
}

// SubmitInput is one response arriving from a respondent.
type SubmitInput struct {
	PublicID       string
	IdempotencyKey string
	FormVersionID  uuid.UUID
	Answers        map[string]any
	Consents       []contracts.ConsentGrant
	DocumentID     uuid.UUID
	RenderedHash   []byte
	VisitID        *uuid.UUID
	Evidence       contracts.ConsentEvidence
}

// SubmitResult is what the respondent gets back.
type SubmitResult struct {
	SubmissionID uuid.UUID `json:"submission_id"`
	ReceiptToken string    `json:"receipt_token"`
}

// Submitter accepts responses.
//
// It depends on the consent module only through contracts: it never touches a
// consent table, yet the two still commit atomically because Record is handed
// the same transaction. That is the whole reason the boundary is drawn where it
// is -- the invariant survives without the coupling.
type Submitter struct {
	db       *postgres.DB
	forms    Repository
	subjects contracts.SubjectResolver
	consent  contracts.ConsentRecorder
	docs     contracts.DocumentProvider
	audit    contracts.AuditWriter
	files    contracts.FileBinder
	events   contracts.EventCollector
	log      *slog.Logger

	defaultRetention time.Duration
}

// SubmitterDeps are the collaborators a Submitter needs.
type SubmitterDeps struct {
	DB               *postgres.DB
	Forms            Repository
	Subjects         contracts.SubjectResolver
	Consent          contracts.ConsentRecorder
	Documents        contracts.DocumentProvider
	Audit            contracts.AuditWriter
	Files            contracts.FileBinder
	Events           contracts.EventCollector
	Log              *slog.Logger
	DefaultRetention time.Duration
}

// NewSubmitter returns a Submitter.
func NewSubmitter(d SubmitterDeps) *Submitter {
	return &Submitter{
		db: d.DB, forms: d.Forms, subjects: d.Subjects, consent: d.Consent,
		docs: d.Documents, audit: d.Audit, files: d.Files, events: d.Events, log: d.Log,
		defaultRetention: d.DefaultRetention,
	}
}

// SubmissionInserter writes a submission inside a caller-supplied transaction.
type SubmissionInserter interface {
	InsertSubmission(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, sub store.Submission, purgeAt *time.Time) error
}

// Submit validates and stores one response.
//
// The order matters and each step exists for a reason:
//
//  1. Resolve the version the client says it rendered.
//  2. Re-evaluate the branching server-side.
//  3. Discard answers for fields that were never shown.
//  4. Check required-ness only over what was shown.
//  5. Verify the consent text matches what was displayed.
//  6. Write submission, consent, audit and outbox in one transaction.
//
// Step 3 is a security control, not tidying: an answer for a hidden branch is an
// answer to a question whose consent text the respondent never saw.
func (s *Submitter) Submit(ctx context.Context, inserter SubmissionInserter, in SubmitInput) (SubmitResult, error) {
	pf, err := s.forms.ResolvePublic(ctx, in.PublicID)
	if err != nil {
		return SubmitResult{}, err
	}
	// Published versions are immutable, so accepting an older one is safe: there
	// is nothing ambiguous about what it contained. Only a withdrawn version is
	// refused.
	if in.FormVersionID != uuid.Nil && in.FormVersionID != pf.VersionID {
		return SubmitResult{}, ErrVersionMismatch
	}

	result, err := engine.Evaluate(pf.Schema, in.Answers)
	if err != nil {
		return SubmitResult{}, fmt.Errorf("evaluating form logic: %w", err)
	}

	visible := make([]string, 0, len(result.Visible))
	answers := make(map[string]any, len(result.Visible))
	for _, fid := range result.Visible {
		visible = append(visible, string(fid))
		if v, ok := in.Answers[string(fid)]; ok {
			answers[string(fid)] = v
		}
	}

	if fieldErrs := validateAnswers(pf.Schema, result, answers); len(fieldErrs) > 0 {
		return SubmitResult{}, fieldErrs
	}

	// Attachments are checked before anything is written: each must exist, be
	// unattached, and have been uploaded for this exact question. Checking only
	// that the file exists would let a caller attach someone else's upload by
	// guessing an id.
	attachments, fieldErrs := s.collectAttachments(ctx, pf, result, answers)
	if len(fieldErrs) > 0 {
		return SubmitResult{}, fieldErrs
	}

	// The consent text must be the one the respondent actually saw. Comparing the
	// client's hash of the rendered document against the stored one is what turns
	// the record from an assertion into evidence.
	doc, err := s.docs.ActiveDocument(ctx, pf.TenantID, "consent_text")
	if err != nil {
		return SubmitResult{}, fmt.Errorf("loading consent document: %w", err)
	}
	if in.DocumentID != doc.ID {
		return SubmitResult{}, ErrConsentMismatch
	}
	if len(in.RenderedHash) == 0 || !hashEqual(in.RenderedHash, doc.ContentHash) {
		return SubmitResult{}, ErrConsentMismatch
	}
	if err := checkRequiredPurposes(pf.Schema, in.Consents); err != nil {
		return SubmitResult{}, err
	}

	identifierField, _, hasIdentifier := pf.Schema.IdentifierField()
	if !hasIdentifier {
		return SubmitResult{}, ErrNoIdentifier
	}
	identifierValue, _ := answers[string(identifierField)].(string)
	if identifierValue == "" {
		return SubmitResult{}, FieldErrors{string(identifierField): "required"}
	}
	identifierKind := pf.Schema.Fields[identifierField].PII

	form, err := s.formMeta(ctx, pf)
	if err != nil {
		return SubmitResult{}, err
	}

	submissionID := uuid.New()
	receipt := "rt_" + uuid.NewString()

	err = s.db.InTenantTx(ctx, pf.TenantID, func(tx pgx.Tx) error {
		reserved, err := s.reserveIdempotency(ctx, tx, pf.TenantID, in)
		if err != nil {
			return err
		}
		if !reserved {
			return ErrDuplicateRequest
		}

		subject, err := s.subjects.UpsertSubject(ctx, tx, pf.TenantID, identifierKind, identifierValue)
		if err != nil {
			return err
		}

		plain, sealed, err := s.splitSensitive(ctx, tx, pf, subject.ID, submissionID, answers)
		if err != nil {
			return err
		}

		sub := store.Submission{
			ID: submissionID, FormID: pf.FormID, FormVersionID: pf.VersionID,
			Answers: plain, VisibleFields: visible,
		}
		if err := inserter.InsertSubmission(ctx, tx, pf.TenantID, sub, s.RetentionFor(form, time.Now().UTC())); err != nil {
			return err
		}
		if len(sealed) > 0 {
			if _, err := tx.Exec(ctx,
				`UPDATE forms.submissions SET answers_enc = $2, data_subject_id = $3 WHERE id = $1`,
				submissionID, sealed, subject.ID); err != nil {
				return fmt.Errorf("storing encrypted answers: %w", err)
			}
		} else if _, err := tx.Exec(ctx,
			`UPDATE forms.submissions SET data_subject_id = $2 WHERE id = $1`,
			submissionID, subject.ID); err != nil {
			return fmt.Errorf("linking data subject: %w", err)
		}

		if err := s.files.Bind(ctx, tx, pf.TenantID, submissionID, attachments); err != nil {
			return err
		}

		// The consent records are written here, in the same transaction. There is
		// no code path that stores a response without them.
		if err := s.consent.Record(ctx, tx, contracts.RecordConsentInput{
			TenantID: pf.TenantID, SubjectID: subject.ID, DocumentID: doc.ID,
			SubmissionID: &submissionID, FormVersionID: &pf.VersionID,
			Grants: in.Consents, Evidence: in.Evidence,
		}); err != nil {
			return err
		}

		if err := s.audit.Write(ctx, tx, contracts.AuditEntry{
			TenantID: pf.TenantID,
			Actor:    contracts.AuditActor{Type: "subject", ID: subject.ID.String(), IPPrefix: in.Evidence.IPPrefix},
			Action:   "submission.created",
			Target:   map[string]any{"submission_id": submissionID, "form_id": pf.FormID},
			// Deliberately no answers: the audit trail records that data was
			// collected, not a second copy of it.
			Payload: map[string]any{"form_version": pf.VersionNo, "consents": len(in.Consents)},
		}); err != nil {
			return err
		}

		if _, err := tx.Exec(ctx,
			`INSERT INTO core.outbox (tenant_id, topic, payload) VALUES ($1, $2, $3)`,
			pf.TenantID, "submission.created",
			// project_id is what the webhook fanout matches on: without it the
			// event is queued, marked sent, and delivered to nobody.
			map[string]any{
				"submission_id": submissionID,
				"form_id":       pf.FormID,
				"project_id":    form.ProjectID,
				"form_version":  pf.VersionNo,
			},
		); err != nil {
			return fmt.Errorf("enqueueing outbox event: %w", err)
		}

		return s.completeIdempotency(ctx, tx, pf.TenantID, in.IdempotencyKey, submissionID, receipt)
	})
	if err != nil {
		return SubmitResult{}, err
	}

	s.events.Collect(ctx, contracts.Event{
		EventID: uuid.NewString(), TenantID: pf.TenantID, Type: contracts.EventSubmit,
		FormID: &pf.FormID, FormVersionID: &pf.VersionID, VisitID: in.VisitID,
		OccurredAt: time.Now().UTC(),
	})

	return SubmitResult{SubmissionID: submissionID, ReceiptToken: receipt}, nil
}

// collectAttachments validates every file answer and returns the ids to bind.
func (s *Submitter) collectAttachments(ctx context.Context, pf store.PublicForm, result engine.Result, answers map[string]any) ([]uuid.UUID, FieldErrors) {
	errs := FieldErrors{}
	var ids []uuid.UUID

	for _, fid := range result.Visible {
		field, ok := pf.Schema.Fields[fid]
		if !ok || field.Type != domain.TypeFile {
			continue
		}
		raw, answered := answers[string(fid)]
		if !answered {
			continue
		}

		attachment, ok := parseFileAnswer(raw)
		if !ok {
			errs[string(fid)] = "must reference an uploaded file"
			continue
		}
		if err := s.files.Validate(ctx, pf.TenantID, attachment.FileID, pf.VersionID, string(fid)); err != nil {
			// One message for every rejection: unknown id, wrong question and
			// already-attached must not be distinguishable, or the endpoint
			// becomes a way to probe which uploads exist.
			errs[string(fid)] = "attachment is not valid for this question"
			continue
		}
		ids = append(ids, attachment.FileID)
	}

	if len(errs) == 0 {
		return ids, nil
	}
	return nil, errs
}

// splitSensitive separates answers that must be encrypted from those stored in
// clear, sealing the former under the data subject's own key.
func (s *Submitter) splitSensitive(ctx context.Context, tx pgx.Tx, pf store.PublicForm, subjectID, submissionID uuid.UUID, answers map[string]any) (plain map[string]any, sealed []byte, err error) {
	plain = make(map[string]any, len(answers))
	sensitive := make(map[string]any)
	for k, v := range answers {
		if f, ok := pf.Schema.Fields[domain.FieldID(k)]; ok && f.Sensitive {
			sensitive[k] = v
			continue
		}
		plain[k] = v
	}
	if len(sensitive) == 0 {
		return plain, nil, nil
	}

	dek, err := s.subjects.DataKey(ctx, tx, subjectID)
	if err != nil {
		return nil, nil, err
	}
	payload, err := json.Marshal(sensitive)
	if err != nil {
		return nil, nil, fmt.Errorf("encoding sensitive answers: %w", err)
	}
	// The context binds this ciphertext to this submission, so a blob cannot be
	// moved between records.
	aad := fmt.Appendf(nil, "%s|%s", pf.TenantID, submissionID)
	sealed, err = crypto.SealWith(dek, payload, aad)
	if err != nil {
		return nil, nil, err
	}
	return plain, sealed, nil
}

// reserveIdempotency claims the key, returning false if it was already used.
//
// The unique constraint is the lock. Checking first and inserting after leaves a
// window in which a double-tapped submit button creates two records.
func (s *Submitter) reserveIdempotency(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, in SubmitInput) (bool, error) {
	if in.IdempotencyKey == "" {
		return true, nil
	}
	body, err := json.Marshal(in.Answers)
	if err != nil {
		return false, fmt.Errorf("hashing request: %w", err)
	}
	sum := sha256.Sum256(body)

	tag, err := tx.Exec(ctx, `
		INSERT INTO core.idempotency_keys (tenant_id, scope, key, request_hash, status)
		VALUES ($1, 'submission', $2, $3, 'PENDING')
		ON CONFLICT (tenant_id, scope, key) DO NOTHING`,
		tenantID, in.IdempotencyKey, sum[:])
	if err != nil {
		return false, fmt.Errorf("reserving idempotency key: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

func (s *Submitter) completeIdempotency(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, key string, submissionID uuid.UUID, receipt string) error {
	if key == "" {
		return nil
	}
	if _, err := tx.Exec(ctx, `
		UPDATE core.idempotency_keys
		SET status = 'COMPLETED', response_body = $3
		WHERE tenant_id = $1 AND scope = 'submission' AND key = $2`,
		tenantID, key,
		map[string]any{"submission_id": submissionID, "receipt_token": receipt},
	); err != nil {
		return fmt.Errorf("completing idempotency key: %w", err)
	}
	return nil
}

func (s *Submitter) formMeta(ctx context.Context, pf store.PublicForm) (store.Form, error) {
	form, err := s.forms.GetForm(ctx, pf.TenantID, pf.FormID)
	if err != nil {
		return store.Form{}, err
	}
	return form, nil
}

// RetentionFor computes the purge date for a submission taken at now.
func (s *Submitter) RetentionFor(form store.Form, now time.Time) *time.Time {
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

// validateAnswers checks required-ness and value shape over the visible fields.
func validateAnswers(schema domain.Schema, result engine.Result, answers map[string]any) FieldErrors {
	errs := FieldErrors{}

	for _, fid := range result.Required {
		v, ok := answers[string(fid)]
		if !ok || isBlankValue(v) {
			errs[string(fid)] = "required"
		}
	}

	for id, v := range answers {
		fid := domain.FieldID(id)
		f, ok := schema.Fields[fid]
		if !ok {
			continue
		}
		if msg := validateValue(f, v); msg != "" {
			errs[id] = msg
		}
	}

	if len(errs) == 0 {
		return nil
	}
	return errs
}

// validateValue checks one answer against its field definition.
//
// Option ids are checked against the schema so a caller cannot invent a choice
// that was never offered, which would otherwise show up in reports as a real
// answer.
func validateValue(f domain.Field, v any) string {
	switch f.Type {
	case domain.TypeChoice, domain.TypeDropdown:
		s, ok := v.(string)
		if !ok {
			return "must be a single option id"
		}
		if s != "" && !f.HasOption(domain.OptionID(s)) {
			return "is not one of the offered options"
		}
	case domain.TypeMultiChoice:
		list, ok := v.([]any)
		if !ok {
			return "must be a list of option ids"
		}
		for _, item := range list {
			s, ok := item.(string)
			if !ok || !f.HasOption(domain.OptionID(s)) {
				return "contains an option that was not offered"
			}
		}
	case domain.TypeRating:
		n, ok := v.(float64)
		if !ok {
			return "must be a number"
		}
		if n < 1 || n > float64(f.Scale) {
			return fmt.Sprintf("must be between 1 and %d", f.Scale)
		}
	case domain.TypeText:
		s, ok := v.(string)
		if !ok {
			return "must be text"
		}
		if len(s) > 10_000 {
			return "is too long"
		}
	case domain.TypeDate:
		s, ok := v.(string)
		if !ok {
			return "must be a date"
		}
		if s != "" {
			if _, err := time.Parse(time.DateOnly, s); err != nil {
				return "must be a date in YYYY-MM-DD form"
			}
		}
	}
	return ""
}

// checkRequiredPurposes refuses a submission that skips a mandatory purpose.
//
// Silence is not agreement, so an absent grant counts as refused rather than as
// accepted-by-default.
func checkRequiredPurposes(schema domain.Schema, grants []contracts.ConsentGrant) error {
	for _, p := range schema.Consent.Purposes {
		if !p.Required {
			continue
		}
		idx := slices.IndexFunc(grants, func(g contracts.ConsentGrant) bool {
			return g.PurposeCode == p.Code
		})
		if idx < 0 || !grants[idx].Granted {
			return fmt.Errorf("%w: %s", ErrConsentMissing, p.Code)
		}
	}
	return nil
}

func isBlankValue(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case string:
		return t == ""
	case []any:
		return len(t) == 0
	default:
		return false
	}
}

func hashEqual(a, b []byte) bool {
	if len(a) != len(b) || len(a) == 0 {
		return false
	}
	var diff byte
	for i := range a {
		diff |= a[i] ^ b[i]
	}
	return diff == 0
}
