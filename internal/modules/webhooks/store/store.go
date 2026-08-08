// Package store persists webhook endpoints and their deliveries.
package store

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/collectr/collectr/internal/modules/webhooks/domain"
	"github.com/collectr/collectr/internal/platform/postgres"
)

// Store reads and writes the integrations schema.
type Store struct{ db *postgres.DB }

// New returns a Store.
func New(db *postgres.DB) *Store { return &Store{db: db} }

// Webhook is one configured endpoint.
type Webhook struct {
	ID             uuid.UUID
	TenantID       uuid.UUID
	ProjectID      uuid.UUID
	URL            string
	Events         []string
	SecretEnc      []byte
	Active         bool
	IncludeAnswers bool
	Failures       int
	DisabledAt     *time.Time
	DisabledReason string
	CreatedBy      uuid.UUID
	CreatedAt      time.Time
}

// Delivery is one attempt to hand an event to an endpoint.
type Delivery struct {
	ID        uuid.UUID
	TenantID  uuid.UUID
	WebhookID uuid.UUID
	EventID   uuid.UUID
	EventType string
	Payload   []byte
	Attempt   int
	Status    string
	URL       string
	SecretEnc []byte
}

// Create stores a new endpoint.
func (s *Store) Create(ctx context.Context, w Webhook) error {
	const q = `
		INSERT INTO integrations.webhooks
			(id, tenant_id, project_id, url, events, secret_enc, include_answers, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`

	err := s.db.InTenantTx(ctx, w.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, q, w.ID, w.TenantID, w.ProjectID, w.URL,
			w.Events, w.SecretEnc, w.IncludeAnswers, w.CreatedBy)
		return err
	})
	if err != nil {
		return fmt.Errorf("creating webhook: %w", err)
	}
	return nil
}

// List returns a project's endpoints.
func (s *Store) List(ctx context.Context, tenantID, projectID uuid.UUID) ([]Webhook, error) {
	const q = `
		SELECT id, project_id, url, events, active, include_answers,
		       consecutive_failures, disabled_at, coalesce(disabled_reason, ''), created_at
		FROM integrations.webhooks
		WHERE tenant_id = $1 AND ($2::uuid IS NULL OR project_id = $2)
		ORDER BY created_at DESC`

	var out []Webhook
	err := s.db.InTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, q, tenantID, nullUUID(projectID))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			w := Webhook{TenantID: tenantID}
			if err := rows.Scan(&w.ID, &w.ProjectID, &w.URL, &w.Events, &w.Active,
				&w.IncludeAnswers, &w.Failures, &w.DisabledAt, &w.DisabledReason, &w.CreatedAt); err != nil {
				return err
			}
			out = append(out, w)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("listing webhooks: %w", err)
	}
	return out, nil
}

// Delete removes an endpoint.
func (s *Store) Delete(ctx context.Context, tenantID, id uuid.UUID) error {
	err := s.db.InTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM integrations.webhooks WHERE id = $1`, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return domain.ErrNotFound
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("deleting webhook: %w", err)
	}
	return nil
}

// Fanout queues one event to every endpoint subscribed to it.
//
// Runs inside the relay's transaction, so an event is either queued to all its
// subscribers or to none: a partial fanout would deliver an event to half a
// customer's integrations and leave the other half silently behind.
func (s *Store) Fanout(ctx context.Context, tx pgx.Tx, tenantID, projectID uuid.UUID, eventID uuid.UUID, eventType string, payload []byte) (int, error) {
	const q = `
		INSERT INTO integrations.deliveries
			(id, tenant_id, webhook_id, event_id, event_type, payload)
		SELECT gen_random_uuid(), $1, w.id, $3, $4, $5
		FROM integrations.webhooks w
		WHERE w.tenant_id = $1 AND w.project_id = $2
		  AND w.active AND w.disabled_at IS NULL
		  AND $4 = ANY(w.events)`

	tag, err := tx.Exec(ctx, q, tenantID, projectID, eventID, eventType, payload)
	if err != nil {
		return 0, fmt.Errorf("queueing deliveries: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// ClaimDue takes a batch of deliveries that are due.
func (s *Store) ClaimDue(ctx context.Context, tx pgx.Tx, limit int) ([]Delivery, error) {
	const q = `
		SELECT d.id, d.tenant_id, d.webhook_id, d.event_id, d.event_type,
		       d.payload, d.attempt, w.url, w.secret_enc
		FROM integrations.deliveries d
		JOIN integrations.webhooks w ON w.id = d.webhook_id
		WHERE d.status = 'pending' AND d.next_attempt_at <= now()
		  AND w.active AND w.disabled_at IS NULL
		ORDER BY d.next_attempt_at
		LIMIT $1
		FOR UPDATE OF d SKIP LOCKED`

	rows, err := tx.Query(ctx, q, limit)
	if err != nil {
		return nil, fmt.Errorf("claiming deliveries: %w", err)
	}
	defer rows.Close()

	var out []Delivery
	for rows.Next() {
		var d Delivery
		if err := rows.Scan(&d.ID, &d.TenantID, &d.WebhookID, &d.EventID,
			&d.EventType, &d.Payload, &d.Attempt, &d.URL, &d.SecretEnc); err != nil {
			return nil, fmt.Errorf("scanning delivery: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// MarkDelivered records success and clears the endpoint's failure streak.
func (s *Store) MarkDelivered(ctx context.Context, d Delivery, status int, snippet string) error {
	return s.db.InTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			UPDATE integrations.deliveries
			SET status = 'delivered', delivered_at = now(), attempt = attempt + 1,
			    response_code = $2, response_snippet = $3
			WHERE id = $1`, d.ID, status, truncate(snippet)); err != nil {
			return err
		}
		_, err := tx.Exec(ctx,
			`UPDATE integrations.webhooks SET consecutive_failures = 0 WHERE id = $1`, d.WebhookID)
		return err
	})
}

// MarkRetry schedules another attempt.
func (s *Store) MarkRetry(ctx context.Context, d Delivery, status int, snippet string, next time.Time) error {
	if _, err := s.db.Exec(ctx, `
		UPDATE integrations.deliveries
		SET attempt = attempt + 1, next_attempt_at = $4,
		    response_code = $2, response_snippet = $3
		WHERE id = $1`, d.ID, nullInt(status), truncate(snippet), next); err != nil {
		return fmt.Errorf("scheduling retry: %w", err)
	}
	return nil
}

// MarkFailed ends a delivery, and disables the endpoint once it has failed
// enough times in a row.
//
// An endpoint that has been dead for days should stop consuming the queue and
// filling the log; whoever owns it re-enables once it is fixed.
func (s *Store) MarkFailed(ctx context.Context, d Delivery, status int, snippet, finalStatus string) (disabled bool, err error) {
	err = s.db.InTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			UPDATE integrations.deliveries
			SET status = $4, attempt = attempt + 1, response_code = $2, response_snippet = $3
			WHERE id = $1`, d.ID, nullInt(status), truncate(snippet), finalStatus); err != nil {
			return err
		}

		var failures int
		if err := tx.QueryRow(ctx, `
			UPDATE integrations.webhooks
			SET consecutive_failures = consecutive_failures + 1
			WHERE id = $1
			RETURNING consecutive_failures`, d.WebhookID).Scan(&failures); err != nil {
			return err
		}
		if failures >= domain.DisableAfterFailures {
			if _, err := tx.Exec(ctx, `
				UPDATE integrations.webhooks
				SET disabled_at = now(), disabled_reason = $2
				WHERE id = $1 AND disabled_at IS NULL`,
				d.WebhookID, fmt.Sprintf("%d consecutive failures", failures)); err != nil {
				return err
			}
			disabled = true
		}
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("failing delivery: %w", err)
	}
	return disabled, nil
}

// ListDeliveries returns the recent history for one endpoint.
func (s *Store) ListDeliveries(ctx context.Context, tenantID, webhookID uuid.UUID, limit int) ([]Delivery, error) {
	const q = `
		SELECT id, webhook_id, event_id, event_type, attempt, status
		FROM integrations.deliveries
		WHERE webhook_id = $1
		ORDER BY created_at DESC
		LIMIT $2`

	var out []Delivery
	err := s.db.InTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, q, webhookID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var d Delivery
			if err := rows.Scan(&d.ID, &d.WebhookID, &d.EventID, &d.EventType,
				&d.Attempt, &d.Status); err != nil {
				return err
			}
			out = append(out, d)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("listing deliveries: %w", err)
	}
	return out, nil
}

// Replay re-queues a dead delivery for another attempt.
func (s *Store) Replay(ctx context.Context, tenantID, deliveryID uuid.UUID) error {
	err := s.db.InTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE integrations.deliveries
			SET status = 'pending', attempt = 0, next_attempt_at = now()
			WHERE id = $1 AND status IN ('dead', 'failed')`, deliveryID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return domain.ErrNotFound
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("replaying delivery: %w", err)
	}
	return nil
}

// PurgeOld removes delivery history past its retention.
func (s *Store) PurgeOld(ctx context.Context, olderThan time.Duration) (int64, error) {
	tag, err := s.db.Exec(ctx,
		`DELETE FROM integrations.deliveries WHERE created_at < $1`,
		time.Now().UTC().Add(-olderThan))
	if err != nil {
		return 0, fmt.Errorf("purging deliveries: %w", err)
	}
	return tag.RowsAffected(), nil
}

// DB exposes the pool for transactional fanout.
func (s *Store) DB() *postgres.DB { return s.db }

func truncate(s string) string {
	if len(s) > 1024 {
		return s[:1024]
	}
	return s
}

func nullInt(n int) *int {
	if n == 0 {
		return nil
	}
	return &n
}

func nullUUID(id uuid.UUID) *uuid.UUID {
	if id == uuid.Nil {
		return nil
	}
	return &id
}
