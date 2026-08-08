package app

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/collectr/collectr/internal/contracts"
	"github.com/collectr/collectr/internal/modules/forms/domain"
)

// UploadContext resolves a public form and field to the rules an upload must
// satisfy.
//
// This is the forms side of the files module's FormResolver: files never learns
// what a form is, and forms never learns where bytes are stored.
func (s *Service) UploadContext(ctx context.Context, publicID, fieldID string) (contracts.UploadContext, error) {
	pf, err := s.repo.ResolvePublic(ctx, publicID)
	if err != nil {
		return contracts.UploadContext{}, err
	}

	field, ok := pf.Schema.Fields[domain.FieldID(fieldID)]
	if !ok || field.Type != domain.TypeFile {
		// A field that is not an upload is reported as a missing form: the caller
		// learns nothing about the shape of a form they were not given.
		return contracts.UploadContext{}, domain.ErrFormNotFound
	}

	form, err := s.repo.GetForm(ctx, pf.TenantID, pf.FormID)
	if err != nil {
		return contracts.UploadContext{}, fmt.Errorf("loading form for upload: %w", err)
	}

	return contracts.UploadContext{
		TenantID:      pf.TenantID,
		ProjectID:     form.ProjectID,
		FormVersionID: pf.VersionID,
		MaxMB:         field.MaxMB,
		Accept:        field.Accept,
	}, nil
}

// fileAnswer is how an attachment appears in a submission's answers.
type fileAnswer struct {
	FileID uuid.UUID
}

// parseFileAnswer reads the {"file_id": "..."} shape the client submits.
func parseFileAnswer(v any) (fileAnswer, bool) {
	m, ok := v.(map[string]any)
	if !ok {
		return fileAnswer{}, false
	}
	raw, ok := m["file_id"].(string)
	if !ok {
		return fileAnswer{}, false
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return fileAnswer{}, false
	}
	return fileAnswer{FileID: id}, true
}

// LocatePublicForm resolves a public form id for the analytics module.
func (s *Service) LocatePublicForm(ctx context.Context, publicID string) (contracts.PublicFormRef, error) {
	pf, err := s.repo.ResolvePublic(ctx, publicID)
	if err != nil {
		return contracts.PublicFormRef{}, err
	}
	form, err := s.repo.GetForm(ctx, pf.TenantID, pf.FormID)
	if err != nil {
		return contracts.PublicFormRef{}, err
	}
	return contracts.PublicFormRef{
		TenantID: pf.TenantID, FormID: pf.FormID,
		VersionID: pf.VersionID, ProjectID: form.ProjectID,
	}, nil
}
