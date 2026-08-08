package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/collectr/collectr/internal/modules/webhooks/domain"
	"github.com/collectr/collectr/internal/modules/webhooks/store"
	"github.com/collectr/collectr/internal/platform/crypto"
	"github.com/collectr/collectr/internal/platform/postgres"
)

// Dispatcher moves events from the outbox to receivers.
type Dispatcher struct {
	db     *postgres.DB
	store  *store.Store
	client *Client
	env    *crypto.Envelope
	log    *slog.Logger
	rand   *rand.Rand
}

// NewDispatcher returns a Dispatcher.
func NewDispatcher(db *postgres.DB, s *store.Store, env *crypto.Envelope, log *slog.Logger) *Dispatcher {
	return &Dispatcher{
		db: db, store: s, client: NewClient(), env: env, log: log,
		// Seeded from the clock: the jitter only needs to differ between
		// processes, not to be unpredictable to anyone.
		rand: rand.New(rand.NewPCG(uint64(time.Now().UnixNano()), 0x9E3779B9)),
	}
}

// Relay turns outbox rows into queued deliveries.
//
// Reading from the outbox rather than firing at the moment of the business write
// is what makes delivery survive a crash: the event and the data it describes
// were committed together, so nothing can be announced that did not happen, and
// nothing that happened goes unannounced.
func (d *Dispatcher) Relay(ctx context.Context) error {
	const batch = 100

	return d.db.InTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id, tenant_id, topic, payload
			FROM core.outbox
			WHERE sent_at IS NULL AND available_at <= now()
			ORDER BY id
			LIMIT $1
			FOR UPDATE SKIP LOCKED`, batch)
		if err != nil {
			return fmt.Errorf("reading outbox: %w", err)
		}

		type entry struct {
			id       int64
			tenantID uuid.UUID
			topic    string
			payload  map[string]any
		}
		var entries []entry
		for rows.Next() {
			var e entry
			if err := rows.Scan(&e.id, &e.tenantID, &e.topic, &e.payload); err != nil {
				rows.Close()
				return fmt.Errorf("scanning outbox row: %w", err)
			}
			entries = append(entries, e)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}

		for _, e := range entries {
			if err := d.fanout(ctx, tx, e.tenantID, e.topic, e.payload); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx,
				`UPDATE core.outbox SET sent_at = now() WHERE id = $1`, e.id); err != nil {
				return fmt.Errorf("marking outbox row sent: %w", err)
			}
		}
		return nil
	})
}

func (d *Dispatcher) fanout(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, topic string, payload map[string]any) error {
	if !domain.ValidEvent(topic) {
		// Not every outbox topic is a public event; the rest are internal.
		return nil
	}
	projectID, err := uuid.Parse(fmt.Sprint(payload["project_id"]))
	if err != nil {
		// Loud rather than silent: an event with no project matches no webhook,
		// so it would be marked sent and delivered to nobody.
		d.log.Error("event has no project id; no webhook can receive it",
			"topic", topic, "tenant_id", tenantID)
		return nil
	}

	eventID := uuid.New()
	body, err := json.Marshal(map[string]any{
		"id":         "evt_" + eventID.String(),
		"type":       topic,
		"created_at": time.Now().UTC().Format(time.RFC3339),
		"data":       payload,
	})
	if err != nil {
		return fmt.Errorf("encoding event: %w", err)
	}

	if _, err := d.store.Fanout(ctx, tx, tenantID, projectID, eventID, topic, body); err != nil {
		return err
	}
	return nil
}

// Deliver attempts every due delivery.
func (d *Dispatcher) Deliver(ctx context.Context) error {
	const batch = 20

	var due []store.Delivery
	if err := d.db.InTx(ctx, func(tx pgx.Tx) error {
		var err error
		due, err = d.store.ClaimDue(ctx, tx, batch)
		return err
	}); err != nil {
		return err
	}

	for _, delivery := range due {
		d.attempt(ctx, delivery)
	}
	return nil
}

func (d *Dispatcher) attempt(ctx context.Context, delivery store.Delivery) {
	secret, err := d.env.OpenBytes(delivery.SecretEnc)
	if err != nil {
		d.log.Error("unwrapping webhook secret", "error", err, "webhook_id", delivery.WebhookID)
		return
	}

	res := d.client.Send(ctx, delivery.URL, secret,
		delivery.EventType, delivery.ID.String(), delivery.Payload)

	switch {
	case res.Delivered():
		if err := d.store.MarkDelivered(ctx, delivery, res.StatusCode, res.Snippet); err != nil {
			d.log.Error("recording delivery", "error", err, "delivery_id", delivery.ID)
		}

	case res.Retryable() && delivery.Attempt+1 < domain.MaxAttempts:
		next := time.Now().Add(domain.Backoff(delivery.Attempt+1, d.rand))
		if err := d.store.MarkRetry(ctx, delivery, res.StatusCode, snippet(res), next); err != nil {
			d.log.Error("scheduling retry", "error", err, "delivery_id", delivery.ID)
		}

	default:
		// A 4xx stops immediately; a retryable failure that has run out of
		// attempts becomes dead and stays available for manual replay.
		final := domain.StatusFailed
		if res.Retryable() {
			final = domain.StatusDead
		}
		disabled, err := d.store.MarkFailed(ctx, delivery, res.StatusCode, snippet(res), final)
		if err != nil {
			d.log.Error("failing delivery", "error", err, "delivery_id", delivery.ID)
			return
		}
		if disabled {
			d.log.Error("webhook disabled after repeated failures",
				"webhook_id", delivery.WebhookID, "tenant_id", delivery.TenantID)
		}
	}
}

// PurgeDeliveries drops delivery history past its retention.
//
// The stored payloads are copies of personal data; they inherit the same
// obligation not to be kept indefinitely.
func (d *Dispatcher) PurgeDeliveries(ctx context.Context, olderThan time.Duration) error {
	n, err := d.store.PurgeOld(ctx, olderThan)
	if err != nil {
		return err
	}
	if n > 0 {
		d.log.Info("purged webhook delivery history", "count", n)
	}
	return nil
}

func snippet(res Result) string {
	if res.Err != nil {
		return res.Err.Error()
	}
	return res.Snippet
}
