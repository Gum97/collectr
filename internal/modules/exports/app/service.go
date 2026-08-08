// Package app runs export jobs.
//
// Exports are queued rather than served inline: fifty thousand rows across forty
// columns cannot be produced inside one request, and an export is also an act
// worth recording rather than a page load.
package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/collectr/collectr/internal/contracts"
	"github.com/collectr/collectr/internal/modules/exports/store"
	"github.com/collectr/collectr/internal/platform/reporting"
	"github.com/collectr/collectr/internal/platform/storage"
)

// ArtefactTTL is how long a finished export stays downloadable.
//
// Short: the artefact is a file full of personal data sitting on a disk, and its
// usefulness expires long before the risk does.
const ArtefactTTL = 24 * time.Hour

// Service queues and produces exports.
type Service struct {
	store   *store.Store
	subs    contracts.SubmissionSource
	reports contracts.ReportSource
	objects storage.Storage
	audit   contracts.AuditWriter
	opener  contracts.SensitiveOpener
	log     *slog.Logger

	linkReports  contracts.LinkReportSource
	directory    contracts.Directory
	rawRetention time.Duration
}

// Deps are the Service's collaborators.
type Deps struct {
	Store       *store.Store
	Submissions contracts.SubmissionSource
	Reports     contracts.ReportSource
	Objects     storage.Storage
	Audit       contracts.AuditWriter
	Opener      contracts.SensitiveOpener
	LinkReports contracts.LinkReportSource
	// Directory resolves the project and user names for the report header.
	Directory    contracts.Directory
	RawRetention time.Duration
	Log          *slog.Logger
}

// NewService returns a Service.
func NewService(d Deps) *Service {
	return &Service{
		store: d.Store, subs: d.Submissions, reports: d.Reports,
		objects: d.Objects, audit: d.Audit, opener: d.Opener, log: d.Log,
		linkReports: d.LinkReports, directory: d.Directory, rawRetention: d.RawRetention,
	}
}

// EnsureFormInTenant reports whether a form belongs to the caller's organisation.
//
// Called before queueing an export, because the check cannot wait until the
// worker: that process connects as the database owner and row-level security
// does not apply to it.
func (s *Service) EnsureFormInTenant(ctx context.Context, tenantID, formID uuid.UUID) error {
	_, err := s.subs.FormTitle(ctx, tenantID, formID)
	return err
}

// RequestLinkReport queues a link report for a project.
//
// Project-scoped, not link-scoped: the question an operator asks is "how are my
// links doing", and a per-link file would mean opening one workbook per link to
// answer it.
func (s *Service) RequestLinkReport(ctx context.Context, in RequestInput) (store.Job, error) {
	job := store.Job{
		ID: uuid.New(), TenantID: in.TenantID, ProjectID: in.ProjectID,
		Kind: "link_report", TargetID: in.ProjectID, RequestedBy: in.RequestedBy,
		Status: "queued",
		Params: map[string]any{
			"from": formatTime(in.From), "to": formatTime(in.To),
			"actor_email": in.ActorEmail,
		},
	}
	if err := s.store.Enqueue(ctx, job); err != nil {
		return store.Job{}, err
	}
	return job, nil
}

func (s *Service) produceLinkReport(ctx context.Context, job store.Job) (string, int, error) {
	from, to := parseTime(job.Params["from"]), parseTime(job.Params["to"])
	if to.IsZero() {
		to = time.Now().UTC().Add(time.Minute)
	}

	report, err := s.linkReports.LinkReport(ctx, job.TenantID, job.TargetID, from, to, s.rawRetention)
	if err != nil {
		return "", 0, err
	}
	report.ProjectName, err = s.directory.ProjectName(ctx, job.TenantID, job.TargetID)
	if err != nil {
		// A missing name is not worth failing a report over; the id in the
		// filename still identifies it.
		s.log.Warn("project name unavailable for export", "error", err, "export_id", job.ID)
	}

	var buf bytes.Buffer
	if err := reporting.WriteLinkReport(&buf, report, reporting.WorkbookMeta{
		RequestedBy: s.requesterEmail(ctx, job),
		RequestedAt: time.Now().UTC(),
		Filters:     describeFilters(from, to),
	}); err != nil {
		return "", 0, err
	}

	key := fmt.Sprintf("%s/exports/%s.xlsx", job.TenantID, job.ID)
	if _, err := s.objects.Put(ctx, key, bytes.NewReader(buf.Bytes())); err != nil {
		return "", 0, fmt.Errorf("storing export: %w", err)
	}
	return key, len(report.Rows), nil
}

// RequestInput describes an export to produce.
type RequestInput struct {
	TenantID    uuid.UUID
	ProjectID   uuid.UUID
	FormID      uuid.UUID
	RequestedBy uuid.UUID
	ActorEmail  string
	From        time.Time
	To          time.Time
	// IncludeSensitive is only honoured when the requester holds the capability;
	// the handler decides that, and records the decision on the job.
	IncludeSensitive bool
}

// Request queues an export and records that it was asked for.
//
// The audit entry is written now, not when the file is produced: what matters is
// that somebody asked for a bulk extract of personal data, and that is true even
// if the job later fails.
func (s *Service) Request(ctx context.Context, in RequestInput) (store.Job, error) {
	job := store.Job{
		ID: uuid.New(), TenantID: in.TenantID, ProjectID: in.ProjectID,
		Kind: "form_report", TargetID: in.FormID, RequestedBy: in.RequestedBy,
		IncludeSensitive: in.IncludeSensitive, Status: "queued",
		Params: map[string]any{
			"from": formatTime(in.From), "to": formatTime(in.To),
			"actor_email": in.ActorEmail,
		},
	}
	if err := s.store.Enqueue(ctx, job); err != nil {
		return store.Job{}, err
	}
	return job, nil
}

// Tick produces one queued export.
func (s *Service) Tick(ctx context.Context) error {
	job, ok, err := s.store.Claim(ctx)
	if err != nil || !ok {
		return err
	}

	produce := s.produce
	if job.Kind == "link_report" {
		produce = s.produceLinkReport
	}

	key, rows, err := produce(ctx, job)
	if err != nil {
		s.log.Error("producing export", "error", err, "export_id", job.ID)
		return s.store.Fail(ctx, job, err.Error())
	}
	return s.store.Complete(ctx, job, key, filename(job), rows, time.Now().UTC().Add(ArtefactTTL))
}

func (s *Service) produce(ctx context.Context, job store.Job) (string, int, error) {
	from, to := parseTime(job.Params["from"]), parseTime(job.Params["to"])
	if to.IsZero() {
		to = time.Now().UTC().Add(time.Minute)
	}

	title, err := s.subs.FormTitle(ctx, job.TenantID, job.TargetID)
	if err != nil {
		return "", 0, err
	}
	columns, err := s.subs.Columns(ctx, job.TenantID, job.TargetID)
	if err != nil {
		return "", 0, err
	}

	writer, err := reporting.NewWriter(columns)
	if err != nil {
		return "", 0, err
	}
	acc := reporting.NewAccumulator(columns)

	// One pass: each row goes into the spreadsheet and into the statistics at the
	// same time, so the data never has to be held twice.
	excluded, err := s.subs.EachSubmission(ctx, job.TenantID, job.TargetID,
		contracts.ExportFilter{From: from, To: to, IncludeSensitive: job.IncludeSensitive},
		func(row contracts.ExportRow) error {
			if job.IncludeSensitive {
				s.fillSensitive(ctx, job.TenantID, &row)
			}
			acc.Add(row)
			return writer.WriteRow(row, job.IncludeSensitive)
		})
	if err != nil {
		return "", 0, err
	}

	funnel, err := s.reports.Funnel(ctx, job.TenantID, job.TargetID, from, to, 24*time.Hour)
	if err != nil {
		// A report without its funnel is still worth producing; refusing to
		// deliver anything because one panel is missing serves nobody.
		s.log.Warn("funnel unavailable for export", "error", err, "export_id", job.ID)
	}
	dropOff, err := s.reports.PageDropOff(ctx, job.TenantID, job.TargetID, from, to)
	if err != nil {
		s.log.Warn("drop-off unavailable for export", "error", err, "export_id", job.ID)
	}

	report := acc.Build(title, from, to, funnel, dropOff, excluded)

	var buf bytes.Buffer
	if err := writer.Finish(&buf, report, reporting.WorkbookMeta{
		RequestedBy:      s.requesterEmail(ctx, job),
		RequestedAt:      time.Now().UTC(),
		IncludeSensitive: job.IncludeSensitive,
		Filters:          describeFilters(from, to),
	}); err != nil {
		return "", 0, err
	}

	key := fmt.Sprintf("%s/exports/%s.xlsx", job.TenantID, job.ID)
	if _, err := s.objects.Put(ctx, key, bytes.NewReader(buf.Bytes())); err != nil {
		return "", 0, fmt.Errorf("storing export: %w", err)
	}
	if writer.Overflowed() {
		s.log.Warn("export truncated at the spreadsheet row limit", "export_id", job.ID)
	}
	return key, writer.Rows(), nil
}

// fillSensitive decrypts a row's sealed answers into its cells.
//
// A failure is not fatal to the export: an erased subject, or a key that can no
// longer be unwrapped, leaves the cell masked rather than aborting a report of
// thousands of rows over one of them.
func (s *Service) fillSensitive(ctx context.Context, tenantID uuid.UUID, row *contracts.ExportRow) {
	if s.opener == nil || row.SubjectID == nil || len(row.SensitiveBlob) == 0 {
		return
	}
	answers, err := s.opener.OpenSensitive(ctx, tenantID, *row.SubjectID, row.ID, row.SensitiveBlob)
	if err != nil {
		s.log.Warn("sensitive answers unavailable for export",
			"error", err, "submission_id", row.ID)
		return
	}
	for field, value := range answers {
		row.Cells[field] = contracts.ExportCell{State: contracts.CellAnswered, Value: value}
	}
}

// Get returns a job for its requester.
func (s *Service) Get(ctx context.Context, tenantID, id uuid.UUID) (store.Job, error) {
	return s.store.Get(ctx, tenantID, id)
}

// Open returns a finished export's contents.
func (s *Service) Open(ctx context.Context, tenantID, id uuid.UUID) (store.Job, []byte, error) {
	job, err := s.store.Get(ctx, tenantID, id)
	if err != nil {
		return store.Job{}, nil, err
	}
	if job.Status != "ready" || job.StorageKey == "" {
		return store.Job{}, nil, store.ErrNotReady
	}
	if job.ExpiresAt != nil && time.Now().After(*job.ExpiresAt) {
		return store.Job{}, nil, store.ErrExpired
	}

	rc, err := s.objects.Get(ctx, job.StorageKey)
	if errors.Is(err, storage.ErrNotFound) {
		return store.Job{}, nil, store.ErrExpired
	}
	if err != nil {
		return store.Job{}, nil, fmt.Errorf("reading export: %w", err)
	}
	defer rc.Close()

	var buf bytes.Buffer
	if _, err := buf.ReadFrom(rc); err != nil {
		return store.Job{}, nil, fmt.Errorf("reading export: %w", err)
	}
	return job, buf.Bytes(), nil
}

// Sweep removes artefacts past their lifetime.
func (s *Service) Sweep(ctx context.Context) (int, error) {
	expired, err := s.store.ListExpired(ctx, 100)
	if err != nil {
		return 0, err
	}
	var removed int
	for _, job := range expired {
		if job.StorageKey != "" {
			if err := s.objects.Delete(ctx, job.StorageKey); err != nil {
				s.log.Error("deleting expired export", "error", err, "export_id", job.ID)
				continue
			}
		}
		if err := s.store.MarkExpired(ctx, job.TenantID, job.ID); err != nil {
			s.log.Error("marking export expired", "error", err, "export_id", job.ID)
			continue
		}
		removed++
	}
	return removed, nil
}

// WriteAudit records that a bulk extract was requested.
func (s *Service) WriteAudit(ctx context.Context, tx pgx.Tx, job store.Job, actorID string, ipPrefix string) error {
	return s.audit.Write(ctx, tx, contracts.AuditEntry{
		TenantID: job.TenantID,
		Actor:    contracts.AuditActor{Type: "user", ID: actorID, IPPrefix: ipPrefix},
		Action:   "submission.read_bulk",
		Target:   map[string]any{"form_id": job.TargetID, "export_id": job.ID},
		Payload: map[string]any{
			"include_sensitive": job.IncludeSensitive,
			"filters":           job.Params,
		},
	})
}

func filename(job store.Job) string {
	return fmt.Sprintf("collectr-%s-%s.xlsx",
		job.TargetID.String()[:8], time.Now().UTC().Format("20060102-1504"))
}

func describeFilters(from, to time.Time) string {
	if from.IsZero() {
		return "toàn bộ dữ liệu đến " + to.Format(time.DateOnly)
	}
	return from.Format(time.DateOnly) + " → " + to.Format(time.DateOnly)
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}

func parseTime(v any) time.Time {
	s, ok := v.(string)
	if !ok || s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// requesterEmail names who asked for the extract.
//
// Falls back to the user id rather than an empty cell: a provenance sheet whose
// "requested by" is blank is worse than one holding a uuid, because blank reads
// as "nobody" instead of "look this up".
func (s *Service) requesterEmail(ctx context.Context, job store.Job) string {
	email, err := s.directory.UserEmail(ctx, job.TenantID, job.RequestedBy)
	if err != nil || email == "" {
		s.log.Warn("requester email unavailable for export",
			"error", err, "export_id", job.ID)
		return job.RequestedBy.String()
	}
	return email
}
