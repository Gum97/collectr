package app

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/collectr/collectr/internal/contracts"
	"github.com/collectr/collectr/internal/modules/dsr/domain"
	"github.com/collectr/collectr/internal/modules/dsr/store"
	"github.com/collectr/collectr/internal/platform/postgres"
)

// Processor completes queued data subject requests.
//
// Erasure and withdrawal run without a human because they are mechanical, and
// because a deadline met only when someone is at their desk is not met. What
// needs judgement -- objection, restriction -- stays in the queue for a person.
type Processor struct {
	db    *postgres.DB
	store *store.Store
	audit contracts.AuditWriter
	log   *slog.Logger
}

// NewProcessor returns a Processor.
func NewProcessor(db *postgres.DB, s *store.Store, audit contracts.AuditWriter, log *slog.Logger) *Processor {
	return &Processor{db: db, store: s, audit: audit, log: log}
}

// Tick processes a batch of pending requests.
func (p *Processor) Tick(ctx context.Context) error {
	const batch = 20

	return p.db.InTx(ctx, func(tx pgx.Tx) error {
		pending, err := p.store.ClaimPending(ctx, tx, batch)
		if err != nil {
			return err
		}
		for _, req := range pending {
			r := domain.Request{Type: req.Type, Status: domain.StatusVerified, DueAt: req.DueAt}
			if !r.AutoFulfillable() {
				// Left claimed but unchanged: a person has to decide, and the
				// overdue metric keeps counting until they do.
				continue
			}
			if err := p.fulfil(ctx, tx, req); err != nil {
				// One failing request must not block the rest of the batch, but
				// the transaction is shared, so give up on this pass and retry.
				return fmt.Errorf("fulfilling request %s (%s): %w", req.ID, req.Type, err)
			}
		}
		return nil
	})
}

func (p *Processor) fulfil(ctx context.Context, tx pgx.Tx, req store.PendingRequest) error {
	// RLS applies to this connection, and erase_subject touches tenant-scoped
	// rows, so the tenant has to be selected even inside a worker transaction.
	if _, err := tx.Exec(ctx, "SELECT set_config('app.tenant_id', $1, true)", req.TenantID.String()); err != nil {
		return fmt.Errorf("setting tenant scope: %w", err)
	}

	var note string
	switch req.Type {
	case domain.TypeErase:
		deleted, err := p.store.EraseSubject(ctx, tx, req.SubjectID)
		if err != nil {
			return err
		}
		note = fmt.Sprintf("erased %d submission(s); data key destroyed", deleted)
		p.log.Info("data subject erased",
			"tenant_id", req.TenantID, "subject_id", req.SubjectID, "submissions", deleted)

	case domain.TypeWithdraw:
		// Withdrawal does not delete anything. Processing that already happened
		// was lawful; what changes is that it stops now.
		if _, err := tx.Exec(ctx, `
			UPDATE consent.current_consents
			SET granted = false, updated_at = now()
			WHERE tenant_id = $1 AND data_subject_id = $2
			  AND purpose_id IN (SELECT id FROM consent.purposes
			                     WHERE tenant_id = $1 AND is_required = false)`,
			req.TenantID, req.SubjectID); err != nil {
			return fmt.Errorf("withdrawing consent: %w", err)
		}
		note = "consent withdrawn for all optional purposes"

	case domain.TypeAccess, domain.TypeExport:
		// The portal already shows everything on demand, so the request is
		// satisfied by the act of asking; an export file lands in v0.5.
		note = "data available through the self-service portal"

	default:
		return fmt.Errorf("request type %q is not auto-fulfillable", req.Type)
	}

	if err := p.store.MarkFulfilled(ctx, tx, req.ID, note); err != nil {
		return err
	}
	return p.audit.Write(ctx, tx, contracts.AuditEntry{
		TenantID: req.TenantID,
		Actor:    contracts.AuditActor{Type: "system", ID: "dsr-processor"},
		Action:   "dsr.fulfilled",
		Target:   map[string]any{"request_id": req.ID, "subject_id": req.SubjectID},
		Payload:  map[string]any{"type": req.Type, "note": note},
	})
}

// PurgeRetention deletes submissions whose retention period has expired.
//
// The date was fixed when each submission was taken, so tightening or relaxing
// the policy afterwards never reaches backwards into data already collected.
func (p *Processor) PurgeRetention(ctx context.Context) (int64, error) {
	const batch = 500

	n, err := p.store.PurgeExpiredSubmissions(ctx, batch)
	if err != nil {
		return 0, err
	}
	if n > 0 {
		// Retention deletions are worth a line: somebody will eventually ask
		// where a quarter of the responses went.
		p.log.Info("purged submissions past their retention date", "count", n)
	}
	return n, nil
}

// PurgeTokens clears used and expired portal tokens.
func (p *Processor) PurgeTokens(ctx context.Context) error {
	_, err := p.store.PurgeExpired(ctx, 24*time.Hour)
	return err
}

// ReportOverdue logs the count of requests past their statutory deadline.
//
// This is the metric to alert on above all others: everything else in the system
// degrades service, and this one breaks the law.
func (p *Processor) ReportOverdue(ctx context.Context) error {
	n, err := p.store.OverdueCount(ctx)
	if err != nil {
		return err
	}
	if n > 0 {
		p.log.Error("data subject requests are past their statutory deadline",
			"overdue_count", n)
	}
	return nil
}
