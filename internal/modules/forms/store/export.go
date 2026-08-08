package store

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/collectr/collectr/internal/contracts"
	"github.com/collectr/collectr/internal/modules/forms/domain"
)

// FormTitle returns a form's display name.
func (s *Store) FormTitle(ctx context.Context, tenantID, formID uuid.UUID) (string, error) {
	f, err := s.GetForm(ctx, tenantID, formID)
	if err != nil {
		return "", err
	}
	return f.Title, nil
}

// Columns returns the merged column set across every version of a form.
func (s *Store) Columns(ctx context.Context, tenantID, formID uuid.UUID) ([]contracts.ExportColumn, error) {
	versions, err := s.ListVersions(ctx, tenantID, formID)
	if err != nil {
		return nil, err
	}

	vs := make([]domain.VersionedSchema, 0, len(versions))
	for _, v := range versions {
		vs = append(vs, domain.VersionedSchema{VersionNo: v.VersionNo, Schema: v.Schema})
	}
	registry := domain.BuildColumnRegistry(vs)

	// Option labels come from the newest version that still declares them, so a
	// report shows current wording while the stored ids stay untouched.
	labels := map[domain.FieldID][]contracts.ExportOption{}
	for _, v := range versions {
		for fid, f := range v.Schema.Fields {
			if !f.IsChoice() {
				continue
			}
			opts := make([]contracts.ExportOption, 0, len(f.Options))
			for _, o := range f.Options {
				opts = append(opts, contracts.ExportOption{ID: string(o.ID), Label: o.Label})
			}
			labels[fid] = opts
		}
	}

	out := make([]contracts.ExportColumn, 0, len(registry))
	for _, c := range registry {
		out = append(out, contracts.ExportColumn{
			FieldID: string(c.FieldID), Label: c.Label, Type: c.Type,
			Sensitive: c.Sensitive, InVersions: c.InVersions,
			RetiredAfter: c.RetiredAfter, TypeVariant: c.TypeVariant,
			Options: labels[c.FieldID],
		})
	}
	return out, nil
}

// EachSubmission streams submissions to fn.
//
// Streaming rather than collecting: a year of responses does not belong in
// memory at once, and the writer consuming them is streaming too.
//
// It returns how many rows were left out. Rows are excluded when they have been
// erased or restricted -- and that count is reported rather than swallowed,
// because a report that silently omits records reads as complete when it is not.
func (s *Store) EachSubmission(ctx context.Context, tenantID, formID uuid.UUID, f contracts.ExportFilter, fn func(contracts.ExportRow) error) (int, error) {
	const q = `
		SELECT s.id, v.version_no, s.answers, s.visible_fields, s.submitted_at, s.status,
		       s.data_subject_id, s.answers_enc,
		       coalesce(s.meta->>'country', ''), coalesce(s.meta->>'ua', ''),
		       coalesce((SELECT l.code FROM links.links l
		                 WHERE l.form_id = s.form_id ORDER BY l.created_at LIMIT 1), ''),
		       -- Read from consent.records, which holds what was agreed *with this
		       -- submission*. The earlier version joined current_consents to get
		       -- today's state instead, and did it without tenant_id -- the leading
		       -- column of that table's primary key -- so every row triggered a
		       -- sequential scan of the whole table. Invisible under a thousand
		       -- rows, and quadratic above it.
		       coalesce(
		         (SELECT jsonb_object_agg(p.code, r.action = 'granted')
		          FROM consent.records r
		          JOIN consent.purposes p ON p.id = r.purpose_id
		          WHERE r.submission_id = s.id),
		         '{}'::jsonb)
		FROM forms.submissions s
		JOIN forms.form_versions v ON v.id = s.form_version_id
		-- s.tenant_id is in the predicate rather than left to row-level security:
		-- this query runs in the worker, which connects as the database owner and
		-- is exempt from every policy.
		WHERE s.form_id = $1 AND s.tenant_id = $4
		  AND ($2::timestamptz IS NULL OR s.submitted_at >= $2)
		  AND ($3::timestamptz IS NULL OR s.submitted_at < $3)
		ORDER BY s.submitted_at`

	schemas, err := s.schemasByVersion(ctx, tenantID, formID)
	if err != nil {
		return 0, err
	}

	var excluded int
	err = s.db.InTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, q, formID, nullTime(f.From), nullTime(f.To), tenantID)
		if err != nil {
			return err
		}
		defer rows.Close()

		for rows.Next() {
			var (
				row       contracts.ExportRow
				answers   map[string]any
				visible   []string
				versionNo int
				consents  map[string]bool
			)
			if err := rows.Scan(&row.ID, &versionNo, &answers, &visible,
				&row.SubmittedAt, &row.Status, &row.SubjectID, &row.SensitiveBlob,
				&row.Country, &row.Device, &row.SourceLink, &consents); err != nil {
				return err
			}

			// Erased and restricted rows never reach a report: one has been
			// deleted, the other is under an explicit instruction not to process.
			if row.Status != "active" {
				excluded++
				continue
			}

			row.VersionNo = versionNo
			row.Consents = consents
			row.Cells = buildCells(schemas[versionNo], visible, answers, len(row.SensitiveBlob) > 0)
			if err := fn(row); err != nil {
				return err
			}
		}
		return rows.Err()
	})
	if err != nil {
		return excluded, fmt.Errorf("streaming submissions: %w", err)
	}
	return excluded, nil
}

// buildCells classifies every field of a submission against its own version.
//
// Sensitive answers are not in `answers` -- they are sealed in answers_enc -- so
// they would otherwise classify as blank, which reads as "the respondent skipped
// this". They are marked answered with no value; the export layer either fills it
// in or masks it, according to what the requester is entitled to.
func buildCells(schema domain.Schema, visible []string, answers map[string]any, hasSealed bool) map[string]contracts.ExportCell {
	cells := make(map[string]contracts.ExportCell, len(schema.Fields))
	for fid, field := range schema.Fields {
		state := domain.CellState(true, visible, answers, fid)
		if field.Sensitive && hasSealed && state == domain.CellBlank {
			state = domain.CellAnswered
		}
		cell := contracts.ExportCell{State: state}
		if state == domain.CellAnswered {
			cell.Value = answers[string(fid)]
		}
		cells[string(fid)] = cell
	}
	return cells
}

func (s *Store) schemasByVersion(ctx context.Context, tenantID, formID uuid.UUID) (map[int]domain.Schema, error) {
	versions, err := s.ListVersions(ctx, tenantID, formID)
	if err != nil {
		return nil, err
	}
	out := make(map[int]domain.Schema, len(versions))
	for _, v := range versions {
		out[v.VersionNo] = v.Schema
	}
	return out, nil
}

func nullTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// Summary is a form as it appears in a list.
type Summary struct {
	ID              uuid.UUID
	ProjectID       uuid.UUID
	PublicID        string
	Title           string
	Status          string
	LiveVersionNo   *int
	SubmissionCount int
	RetentionDays   *int
	CreatedAt       time.Time
}

// ListForms returns a project's forms, or the whole organisation's when
// projectID is nil.
func (s *Store) ListForms(ctx context.Context, tenantID uuid.UUID, projectID *uuid.UUID, limit int) ([]Summary, error) {
	const q = `
		SELECT f.id, f.project_id, f.public_id, f.title, f.status,
		       v.version_no, f.retention_days, f.created_at,
		       (SELECT count(*) FROM forms.submissions s
		        WHERE s.form_id = f.id AND s.status = 'active')
		FROM forms.forms f
		LEFT JOIN forms.form_versions v ON v.id = f.live_version_id
		WHERE f.tenant_id = $1 AND ($2::uuid IS NULL OR f.project_id = $2)
		ORDER BY f.created_at DESC
		LIMIT $3`

	var out []Summary
	err := s.db.InTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, q, tenantID, projectID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var f Summary
			if err := rows.Scan(&f.ID, &f.ProjectID, &f.PublicID, &f.Title, &f.Status,
				&f.LiveVersionNo, &f.RetentionDays, &f.CreatedAt, &f.SubmissionCount); err != nil {
				return err
			}
			out = append(out, f)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("listing forms: %w", err)
	}
	return out, nil
}
