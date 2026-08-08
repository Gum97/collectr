package store

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Document is one version of a consent text or privacy notice.
type Document struct {
	ID            uuid.UUID `json:"id"`
	Kind          string    `json:"kind"`
	VersionNo     int       `json:"version_no"`
	ContentHash   string    `json:"content_hash"`
	EffectiveFrom time.Time `json:"effective_from"`
	CreatedAt     time.Time `json:"created_at"`
	// Permalink is where the exact text a person agreed to can be read back.
	// Built here rather than by the client so one deployment cannot disagree
	// with itself about where its own evidence lives.
	Permalink string `json:"permalink"`
	// InUse marks the version a new submission would record against.
	InUse bool `json:"in_use"`
}

// ListDocuments returns every version, newest first within each kind.
//
// Every version, not just the current one: a published document is immutable
// evidence of what somebody was shown, and a list that hides superseded versions
// hides the record for everyone who agreed before the last edit.
func (s *Store) ListDocuments(ctx context.Context, tenantID uuid.UUID) ([]Document, error) {
	const q = `
		SELECT d.id, d.kind, d.version_no, encode(d.content_hash, 'hex'),
		       d.effective_from, d.created_at,
		       d.version_no = max(d.version_no) OVER (PARTITION BY d.kind)
		FROM consent.documents d
		WHERE d.tenant_id = $1
		ORDER BY d.kind, d.version_no DESC`

	var out []Document
	err := s.db.InTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, q, tenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var d Document
			if err := rows.Scan(&d.ID, &d.Kind, &d.VersionNo, &d.ContentHash,
				&d.EffectiveFrom, &d.CreatedAt, &d.InUse); err != nil {
				return err
			}
			d.ContentHash = "sha256:" + d.ContentHash
			d.Permalink = "/consent/" + d.ID.String()
			out = append(out, d)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("listing consent documents: %w", err)
	}
	return out, nil
}

// Purpose is one declared reason for processing.
type Purpose struct {
	ID            uuid.UUID `json:"id"`
	Code          string    `json:"code"`
	Name          string    `json:"name"`
	Description   string    `json:"description"`
	LegalBasis    string    `json:"legal_basis"`
	RetentionDays *int      `json:"retention_days"`
	Required      bool      `json:"required"`
	CreatedAt     time.Time `json:"created_at"`
}

// ListPurposes returns the tenant's processing purposes.
func (s *Store) ListPurposes(ctx context.Context, tenantID uuid.UUID) ([]Purpose, error) {
	const q = `
		SELECT id, code, name, description, legal_basis, retention_days,
		       is_required, created_at
		FROM consent.purposes
		WHERE tenant_id = $1
		ORDER BY is_required DESC, code`

	var out []Purpose
	err := s.db.InTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, q, tenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var p Purpose
			if err := rows.Scan(&p.ID, &p.Code, &p.Name, &p.Description, &p.LegalBasis,
				&p.RetentionDays, &p.Required, &p.CreatedAt); err != nil {
				return err
			}
			out = append(out, p)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("listing purposes: %w", err)
	}
	return out, nil
}
