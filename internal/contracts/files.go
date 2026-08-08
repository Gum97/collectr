package contracts

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// UploadContext is what an attachment must satisfy: which tenant and form
// version it belongs to, and the limits the question itself imposes.
type UploadContext struct {
	TenantID      uuid.UUID
	ProjectID     uuid.UUID
	FormVersionID uuid.UUID
	// MaxMB is the question's own size limit, applied on top of the system
	// ceiling. Zero means only the ceiling applies.
	MaxMB int
	// Accept lists the content types the question takes. Empty means any type
	// the system recognises -- still a closed set.
	Accept []string
}

// UploadResolver answers "may this file be uploaded here, and under what
// limits", without the files module ever learning what a form is.
type UploadResolver interface {
	UploadContext(ctx context.Context, publicID, fieldID string) (UploadContext, error)
}

// FileBinder attaches uploads to a submission.
//
// Bind takes the caller's transaction so a response and its attachments become
// visible together or not at all -- the same reason ConsentRecorder does.
type FileBinder interface {
	// Validate checks that a file exists, is unattached, and was uploaded for
	// this exact question. Without the last check a caller could upload against
	// a permissive question and attach the result to a stricter one.
	Validate(ctx context.Context, tenantID, fileID, formVersionID uuid.UUID, fieldID string) error
	Bind(ctx context.Context, tx pgx.Tx, tenantID, submissionID uuid.UUID, fileIDs []uuid.UUID) error
}
