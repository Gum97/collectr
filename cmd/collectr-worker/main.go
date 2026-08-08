// Command collectr-worker runs Collectr's background jobs.
//
// It is the same module as the API server but a separate process: exports and
// retention sweeps hold long transactions and produce garbage-collector pauses,
// and neither belongs anywhere near the redirect path's latency budget.
//
// It connects as the database owner, because its work -- rollups, retention,
// partition maintenance -- is cross-tenant by nature and therefore cannot run
// under the row-level security policies that constrain the API server.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	analyticsapp "github.com/collectr/collectr/internal/modules/analytics/app"
	analyticsstore "github.com/collectr/collectr/internal/modules/analytics/store"
	"github.com/collectr/collectr/internal/modules/audit"
	consentstore "github.com/collectr/collectr/internal/modules/consent/store"
	dsrapp "github.com/collectr/collectr/internal/modules/dsr/app"
	dsrstore "github.com/collectr/collectr/internal/modules/dsr/store"
	exportsapp "github.com/collectr/collectr/internal/modules/exports/app"
	exportsstore "github.com/collectr/collectr/internal/modules/exports/store"
	filesapp "github.com/collectr/collectr/internal/modules/files/app"
	filesstore "github.com/collectr/collectr/internal/modules/files/store"
	formsstore "github.com/collectr/collectr/internal/modules/forms/store"
	iamstore "github.com/collectr/collectr/internal/modules/iam/store"
	linksstore "github.com/collectr/collectr/internal/modules/links/store"
	webhooksapp "github.com/collectr/collectr/internal/modules/webhooks/app"
	webhooksstore "github.com/collectr/collectr/internal/modules/webhooks/store"
	"github.com/collectr/collectr/internal/platform/config"
	"github.com/collectr/collectr/internal/platform/crypto"
	"github.com/collectr/collectr/internal/platform/postgres"
	"github.com/collectr/collectr/internal/platform/redisx"
	"github.com/collectr/collectr/internal/platform/storage"
)

func main() {
	if err := run(); err != nil {
		slog.Error("worker failed", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})).
		With("service", "worker")
	slog.SetDefault(log)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	db, err := postgres.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer db.Close()

	rdb, err := redisx.Open(ctx, cfg.RedisURL)
	if err != nil {
		return err
	}
	defer func() {
		if err := rdb.Close(); err != nil {
			log.Warn("closing redis", "error", err)
		}
	}()

	hostname, err := os.Hostname()
	if err != nil {
		hostname = "worker"
	}

	events := analyticsstore.New(db)
	links := linksstore.New(db)
	roller := analyticsapp.NewRoller(events, log, time.Hour)
	dsrProcessor := dsrapp.NewProcessor(db, dsrstore.New(db), audit.NewWriter(db), log)
	iam := iamstore.New(db)

	envelope, err := crypto.NewEnvelope(cfg.TenantKEK)
	if err != nil {
		return err
	}
	objects, err := storage.NewLocal(cfg.Storage.LocalPath, cfg.BaseURL, cfg.VisitPepper)
	if err != nil {
		return err
	}
	fileSvc := filesapp.NewService(filesstore.New(db), objects, envelope, log)

	exportSvc := exportsapp.NewService(exportsapp.Deps{
		Store: exportsstore.New(db), Submissions: formsstore.New(db),
		Reports: events, Objects: objects, Audit: audit.NewWriter(db),
		Opener: consentstore.New(db, envelope, cfg.VisitPepper), Log: log,
		LinkReports: events, Directory: iamstore.New(db),
		RawRetention: cfg.RawEventRetention,
	})
	dispatcher := webhooksapp.NewDispatcher(db, webhooksstore.New(db), envelope, log)
	ingestor := analyticsapp.NewIngestor(rdb, events, log, hostname)

	jobs := []job{
		{name: "event-ingest", run: ingestor.Run},
		{name: "funnel-rollup", every: 30 * time.Second, tick: roller.Tick},
		{
			name:  "partition-maintenance",
			every: time.Hour,
			tick: func(ctx context.Context) error {
				return roller.Maintain(ctx, cfg.RawEventRetention)
			},
		},
		{
			// Erasure and withdrawal complete without a human, because a deadline
			// that depends on someone being at their desk is not a deadline.
			name:  "dsr-processor",
			every: time.Minute,
			tick:  dsrProcessor.Tick,
		},
		{
			name:  "dsr-overdue-report",
			every: 5 * time.Minute,
			tick:  dsrProcessor.ReportOverdue,
		},
		{
			name:  "retention-sweeper",
			every: time.Hour,
			tick: func(ctx context.Context) error {
				_, err := dsrProcessor.PurgeRetention(ctx)
				return err
			},
		},
		{
			name:  "dsr-token-purge",
			every: time.Hour,
			tick:  dsrProcessor.PurgeTokens,
		},
		{
			// Exports are queued because fifty thousand rows across forty columns
			// cannot be produced inside one request.
			name:  "export-runner",
			every: 5 * time.Second,
			tick:  exportSvc.Tick,
		},
		{
			// The artefact is a file full of personal data on a disk; its
			// usefulness expires long before its risk does.
			name:  "export-sweeper",
			every: time.Hour,
			tick: func(ctx context.Context) error {
				_, err := exportSvc.Sweep(ctx)
				return err
			},
		},
		{
			// Reading the outbox rather than firing at the moment of the business
			// write is what makes delivery survive a crash: the event and the data
			// it describes were committed together.
			name:  "webhook-relay",
			every: 5 * time.Second,
			tick:  dispatcher.Relay,
		},
		{
			name:  "webhook-delivery",
			every: 5 * time.Second,
			tick:  dispatcher.Deliver,
		},
		{
			name:  "webhook-history-purge",
			every: 6 * time.Hour,
			tick: func(ctx context.Context) error {
				// Stored payloads are copies of personal data and inherit the same
				// obligation not to be kept indefinitely.
				return dispatcher.PurgeDeliveries(ctx, 30*24*time.Hour)
			},
		},
		{
			// People abandon half-filled forms constantly. Their attachments have
			// no retention clock attached to anything, so they need their own.
			name:  "orphan-file-sweeper",
			every: time.Hour,
			tick: func(ctx context.Context) error {
				if _, err := fileSvc.SweepOrphans(ctx); err != nil {
					return err
				}
				// Erasure destroys the key, which makes the bytes unreadable
				// immediately; this reclaims the space afterwards.
				_, err := fileSvc.PurgeErased(ctx)
				return err
			},
		},
		{
			// Spent and expired credentials are not evidence of anything; keeping
			// them is only a store of things that were once secret.
			name:  "credential-purge",
			every: time.Hour,
			tick: func(ctx context.Context) error {
				if _, err := iam.PurgeExpiredSessions(ctx, 7*24*time.Hour); err != nil {
					return err
				}
				if _, err := iam.PurgeExpiredResetTokens(ctx, 24*time.Hour); err != nil {
					return err
				}
				_, err := iam.PurgeExpiredInvitations(ctx)
				return err
			},
		},
		{
			name:  "link-expiry",
			every: 15 * time.Minute,
			tick: func(ctx context.Context) error {
				n, err := links.ExpireStale(ctx, time.Now().UTC())
				if err != nil {
					return err
				}
				if n > 0 {
					log.Info("expired links", "count", n)
				}
				return nil
			},
		},
	}

	done := make(chan struct{})
	for _, j := range jobs {
		go func() {
			defer func() { done <- struct{}{} }()
			j.start(ctx, log)
		}()
	}

	<-ctx.Done()
	log.Info("shutdown signal received, draining jobs")

	// Jobs observe the same cancelled context; wait for each to finish its
	// current unit of work rather than killing it mid-write.
	drain := time.NewTimer(20 * time.Second)
	defer drain.Stop()
	for range jobs {
		select {
		case <-done:
		case <-drain.C:
			log.Warn("drain timeout: exiting with jobs still running")
			return nil
		}
	}
	log.Info("shutdown complete")
	return nil
}

// job is either a long-running loop (run) or a periodic task (tick + every).
type job struct {
	name  string
	every time.Duration
	run   func(context.Context) error
	tick  func(context.Context) error
}

func (j job) start(ctx context.Context, log *slog.Logger) {
	log = log.With("job", j.name)

	if j.run != nil {
		if err := j.run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			log.Error("job stopped", "error", err)
		}
		return
	}

	ticker := time.NewTicker(j.every)
	defer ticker.Stop()
	for {
		// Run immediately on start, then on each tick: waiting a full interval
		// after a deploy delays the first rollup for no reason.
		if err := j.tick(ctx); err != nil && !errors.Is(err, context.Canceled) {
			log.Error("job iteration", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
