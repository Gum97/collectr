package store

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Attachment is one received file as an operator sees it.
//
// No storage key and no signed URL. The key is an internal address, and a list
// endpoint that hands out fetchable links turns "see what came in" into "read
// everything" for anyone who can call it once.
type Attachment struct {
	ID           uuid.UUID  `json:"id"`
	FieldID      string     `json:"field_id"`
	OriginalName string     `json:"original_name"`
	ContentType  string     `json:"content_type"`
	SizeBytes    int64      `json:"size_bytes"`
	Status       string     `json:"status"`
	SubmissionID *uuid.UUID `json:"submission_id"`
	// Encrypted is true once the file is keyed to a subject, which is what makes
	// erasure destroy it rather than merely unlink it.
	Encrypted bool      `json:"encrypted"`
	CreatedAt time.Time `json:"created_at"`
}

// ListByForm returns the attachments received by one form, newest first.
func (s *Store) ListByForm(ctx context.Context, tenantID, formID uuid.UUID, limit int) ([]Attachment, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	const q = `
		SELECT f.id, coalesce(f.field_id, ''), f.original_name, f.content_type,
		       f.size_bytes, f.status, f.submission_id,
		       f.dek_wrapped IS NOT NULL, f.created_at
		FROM files.files f
		JOIN forms.form_versions v ON v.id = f.form_version_id
		WHERE f.tenant_id = $1 AND v.form_id = $2 AND f.status <> 'erased'
		ORDER BY f.created_at DESC
		LIMIT $3`

	// Empty rather than nil: a JSON null makes every client that calls .length
	// on the result throw, and an empty list is the honest answer.
	out := []Attachment{}
	err := s.db.InTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, q, tenantID, formID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var a Attachment
			if err := rows.Scan(&a.ID, &a.FieldID, &a.OriginalName, &a.ContentType,
				&a.SizeBytes, &a.Status, &a.SubmissionID, &a.Encrypted,
				&a.CreatedAt); err != nil {
				return err
			}
			out = append(out, a)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("listing attachments: %w", err)
	}
	return out, nil
}
