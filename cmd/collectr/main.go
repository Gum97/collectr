// Command collectr runs the Collectr API server.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/collectr/collectr/internal/migrations"
	analyticsapi "github.com/collectr/collectr/internal/modules/analytics/api"
	analyticsapp "github.com/collectr/collectr/internal/modules/analytics/app"
	analyticsstore "github.com/collectr/collectr/internal/modules/analytics/store"
	"github.com/collectr/collectr/internal/modules/audit"
	consentapi "github.com/collectr/collectr/internal/modules/consent/api"
	consentstore "github.com/collectr/collectr/internal/modules/consent/store"
	dsrapi "github.com/collectr/collectr/internal/modules/dsr/api"
	dsrapp "github.com/collectr/collectr/internal/modules/dsr/app"
	dsrstore "github.com/collectr/collectr/internal/modules/dsr/store"
	exportsapi "github.com/collectr/collectr/internal/modules/exports/api"
	exportsapp "github.com/collectr/collectr/internal/modules/exports/app"
	exportsstore "github.com/collectr/collectr/internal/modules/exports/store"
	filesapi "github.com/collectr/collectr/internal/modules/files/api"
	filesapp "github.com/collectr/collectr/internal/modules/files/app"
	filesstore "github.com/collectr/collectr/internal/modules/files/store"
	formsapi "github.com/collectr/collectr/internal/modules/forms/api"
	formsapp "github.com/collectr/collectr/internal/modules/forms/app"
	formsstore "github.com/collectr/collectr/internal/modules/forms/store"
	iamapi "github.com/collectr/collectr/internal/modules/iam/api"
	iamapp "github.com/collectr/collectr/internal/modules/iam/app"
	iamstore "github.com/collectr/collectr/internal/modules/iam/store"
	linksapi "github.com/collectr/collectr/internal/modules/links/api"
	linksapp "github.com/collectr/collectr/internal/modules/links/app"
	linksstore "github.com/collectr/collectr/internal/modules/links/store"
	webhooksapi "github.com/collectr/collectr/internal/modules/webhooks/api"
	webhooksstore "github.com/collectr/collectr/internal/modules/webhooks/store"
	"github.com/collectr/collectr/internal/platform/authn"
	"github.com/collectr/collectr/internal/platform/config"
	"github.com/collectr/collectr/internal/platform/crypto"
	"github.com/collectr/collectr/internal/platform/httpx"
	"github.com/collectr/collectr/internal/platform/metrics"
	"github.com/collectr/collectr/internal/platform/notify"
	"github.com/collectr/collectr/internal/platform/postgres"
	"github.com/collectr/collectr/internal/platform/ratelimit"
	"github.com/collectr/collectr/internal/platform/redisx"
	"github.com/collectr/collectr/internal/platform/signing"
	"github.com/collectr/collectr/internal/platform/storage"
	"github.com/collectr/collectr/internal/webpages"
	"github.com/collectr/collectr/internal/webui"
)

var (
	// healthcheck lets the container probe itself without shipping curl or wget
	// in the image, which is what keeps the runtime image at distroless size.
	healthcheck = flag.Bool("healthcheck", false, "probe the local server and exit")

	// migrate runs the schema migrations and exits.
	//
	// It is a separate mode rather than something the server does on boot,
	// because migrating and serving need different database privileges. The
	// server runs as a role constrained by row-level security and deliberately
	// cannot create tables; migrations run as the owner. Letting the server
	// migrate would mean granting it DDL rights it must never have.
	migrate = flag.Bool("migrate", false, "apply database migrations and exit")
)

func main() {
	flag.Parse()
	switch {
	case *healthcheck:
		os.Exit(probe())
	case *migrate:
		if err := runMigrations(); err != nil {
			slog.Error("migration failed", "error", err)
			os.Exit(1)
		}
		return
	}
	if err := run(); err != nil {
		slog.Error("startup failed", "error", err)
		os.Exit(1)
	}
}

func runMigrations() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}
	log := newLogger(cfg.Env)
	slog.SetDefault(log)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	db, err := postgres.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer db.Close()

	if err := postgres.Migrate(ctx, db, migrations.FS, migrations.Dir, log); err != nil {
		return fmt.Errorf("migrating database: %w", err)
	}
	log.Info("migrations up to date")
	return nil
}

func probe() int {
	addr := os.Getenv("HTTP_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://127.0.0.1" + addr + "/healthz")
	if err != nil {
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	log := newLogger(cfg.Env)
	slog.SetDefault(log)

	// Signal-aware root context: everything below inherits cancellation, so a
	// SIGTERM unwinds the whole process rather than just the HTTP listener.
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

	signer, err := signing.NewSigner(cfg.VisitPepper, cfg.VisitTokenTTL)
	if err != nil {
		return err
	}

	reg := metrics.New()
	reg.MustRegister(metrics.NewStateCollector(ctx, db, log))
	limiter := ratelimit.New(rdb, func(rule string) {
		reg.RateLimited.WithLabelValues(rule).Inc()
	})

	collector := analyticsapp.NewCollector(rdb, log, cfg.EventStreamMaxLen, cfg.EventBufferSize)
	linkSvc := linksapp.NewService(
		linksstore.New(db), rdb, log,
		linksapp.Options{
			CacheTTL:         cfg.LinkCacheTTL,
			NegativeCacheTTL: cfg.LinkNegativeCacheTTL,
			SelfHosts:        []string{hostOf(cfg.BaseURL), hostOf(cfg.ShortURLBase)},
		},
	)
	linkHandler := linksapi.New(linkSvc, collector, analyticsstore.New(db), signer,
		cfg.BaseURL, cfg.ShortURLBase, cfg.RawEventRetention)

	envelope, err := crypto.NewEnvelope(cfg.TenantKEK)
	if err != nil {
		return err
	}

	formStore := formsstore.New(db)
	formSvc := formsapp.NewService(formStore, cfg.DefaultRetention)
	formHandler := formsapi.New(formSvc, collector, signer)

	objects, err := buildStorage(cfg)
	if err != nil {
		return err
	}
	fileSvc := filesapp.NewService(filesstore.New(db), objects, envelope, log)
	fileHandler := filesapi.New(fileSvc, formSvc, cfg.VisitPepper, cfg.BaseURL)
	beaconHandler := analyticsapi.New(collector, formSvc, signer)

	consentStore := consentstore.New(db, envelope, cfg.VisitPepper)

	// The public form page displays the consent text and returns a digest of what
	// it displayed, so the read path needs the same document the submit path
	// validates that digest against.
	formSvc.SetDocuments(consentStore)
	consentHandler := consentapi.New(consentStore)
	auditWriter := audit.NewWriter(db)
	auditHandler := audit.NewHandler(auditWriter)

	// The forms module reaches consent and audit only through contracts, yet all
	// three commit in one transaction: Record and Write take the caller's tx.
	submitter := formsapp.NewSubmitter(formsapp.SubmitterDeps{
		DB:               db,
		Forms:            formStore,
		Subjects:         consentStore,
		Consent:          consentStore,
		Documents:        consentStore,
		Audit:            auditWriter,
		Files:            fileSvc,
		Events:           collector,
		Log:              log,
		DefaultRetention: cfg.DefaultRetention,
	})
	submitHandler := formsapi.NewSubmitHandler(submitter, formStore, signer)

	notifier, err := buildNotifier(cfg, log)
	if err != nil {
		return err
	}

	sessionSigner, err := signing.NewSubjectSigner(cfg.VisitPepper, 30*time.Minute)
	if err != nil {
		return err
	}
	dsrSvc := dsrapp.NewService(dsrapp.Deps{
		DB:       db,
		Store:    dsrstore.New(db),
		Subjects: consentStore,
		Audit:    auditWriter,
		Notifier: notifier,
		Log:      log,
		BaseURL:  cfg.BaseURL,
		SLA:      cfg.DSRSLA,
	})
	dsrHandler := dsrapi.New(dsrSvc, sessionSigner, cfg.Env != "dev")
	dsrAdminHandler := dsrapi.NewAdmin(db, dsrstore.New(db), auditWriter)

	exportSvc := exportsapp.NewService(exportsapp.Deps{
		Store: exportsstore.New(db), Submissions: formStore,
		Reports: analyticsstore.New(db), Objects: objects,
		Audit: auditWriter, Opener: consentStore, Log: log,
		LinkReports: analyticsstore.New(db), Directory: iamstore.New(db),
		RawRetention: cfg.RawEventRetention,
	})
	exportHandler := exportsapi.New(exportSvc, db)
	webhookHandler := webhooksapi.New(webhooksstore.New(db), envelope, cfg.Env == "dev")

	iamSvc := iamapp.NewService(iamapp.Deps{
		Store: iamstore.New(db), Redis: rdb, Envelope: envelope,
		Notifier: notifier, Log: log, BaseURL: cfg.BaseURL,
		LinkHost: hostPortOf(cfg.ShortURLBase),
	})
	iamHandler := iamapi.New(iamSvc, db, auditWriter, cfg.Env != "dev")

	auth := authn.NewAuthenticator(db)

	public := http.NewServeMux()
	public.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, r, http.StatusOK, map[string]string{"status": "ok"})
	})
	public.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := db.Ping(ctx); err != nil {
			httpx.Error(w, r, http.StatusServiceUnavailable, "not_ready", "Database unavailable")
			return
		}
		httpx.JSON(w, r, http.StatusOK, map[string]string{"status": "ready"})
	})
	linkHandler.RegisterPublic(public)
	formHandler.RegisterPublic(public)
	beaconHandler.Register(public)

	// Public write endpoints are throttled twice over: once per caller network,
	// once per form. The first bounds one person flooding; the second bounds one
	// form being flooded from everywhere, which the per-caller rule alone misses
	// entirely.
	//
	// Both fail OPEN. When Redis is down the choice is between accepting some
	// spam during an outage that is already paging somebody, and refusing a
	// customer's form response, which is gone for good. The DSR and login limits
	// fail closed for the opposite reason -- there, being briefly unavailable
	// beats handing out an oracle.
	writes := http.NewServeMux()
	submitHandler.Register(writes)
	fileHandler.Register(writes)
	throttled := limiter.Chain(
		ratelimit.Rule{
			Name: "public_write_ip", Limit: 60, Window: time.Minute,
			Key: ratelimit.ByIPPrefix, OnFailure: ratelimit.FailOpen,
		},
		ratelimit.Rule{
			Name: "public_write_form", Limit: 600, Window: time.Minute,
			Key: ratelimit.ByPathValue("public_id"), OnFailure: ratelimit.FailOpen,
		},
	)(writes)
	public.Handle("/api/pub/forms/{public_id}/submissions", throttled)
	public.Handle("/api/pub/forms/{public_id}/uploads", throttled)
	public.Handle("/api/pub/files/{key...}", writes)
	consentHandler.RegisterPublic(public)
	dsrHandler.Register(public)
	iamHandler.RegisterPublic(public)
	iamHandler.RegisterInvitePublic(public)
	iamHandler.RegisterRecoveryPublic(public)

	admin := http.NewServeMux()
	linkHandler.RegisterAdmin(admin)
	linkHandler.RegisterDomains(admin)
	linkHandler.RegisterStats(admin)
	formHandler.RegisterAdmin(admin)
	consentHandler.RegisterAdmin(admin)
	auditHandler.RegisterAdmin(admin)
	dsrAdminHandler.RegisterAdmin(admin)
	iamHandler.RegisterAdmin(admin)
	iamHandler.RegisterMembers(admin)
	iamHandler.RegisterProjects(admin)
	iamHandler.RegisterRecoveryAdmin(admin)
	exportHandler.RegisterAdmin(admin)
	webhookHandler.RegisterAdmin(admin)
	// Either credential reaches the same handlers with the same rules; only the
	// capability sets differ, and an API key can never hold the sensitive ones.
	public.Handle("/api/v1/", iamHandler.Authenticate(auth)(
		iamapi.RequireSameOrigin(cfg.BaseURL)(admin)))

	// Scrape endpoint. Bind it behind the reverse proxy or a firewall: the
	// numbers are not personal data, but they do describe the shape of a
	// deployment in more detail than a stranger needs.
	public.Handle("GET /metrics", reg.Handler())

	// The public pages are server-rendered, before the SPA fallback claims "/".
	// They are the pages a customer waits for, so they carry no framework: see
	// internal/webpages.
	pages := webpages.New(webpages.Config{
		Forms:          formPages{formSvc},
		Documents:      consentDocs{consentStore},
		Assets:         webui.Assets(),
		Brand:          hostOf(cfg.BaseURL),
		Support:        cfg.SMTP.From,
		ResponseWindow: cfg.DSRSLA,
	})
	pages.Register(public)
	// The redirect answers dead codes with a rendered page rather than plain
	// text: whoever hits one is holding a poster, not a terminal.
	linkHandler.SetDeadLinkPages(pages)

	// The interface, last: it answers every path no other route claimed, so it
	// must be registered after them. Mounting it earlier would have "/" swallow
	// the API.
	public.Handle("/", webui.Handler())

	handler := httpx.Trace(log)(httpx.Recover(httpx.AccessLog(reg.Middleware(public))))

	// How much of X-Forwarded-For to believe, decided once at startup. Reading
	// the header from the wrong end let any caller choose the address used for
	// rate limiting -- and the address written into consent evidence.
	httpx.SetTrustedProxyHops(cfg.TrustedProxyHops)

	srv := &http.Server{
		Addr:    cfg.HTTPAddr,
		Handler: handler,
		// A server without timeouts leaks connections until it stops accepting
		// new ones; these bound every phase of a request's life.
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelError),
	}

	// The fallback flusher lands events that could not reach Redis. It runs even
	// in the API process so a Redis outage costs latency, not data.
	go analyticsapp.FlushBuffered(ctx, collector.Buffered(), analyticsstore.New(db), log)

	errc := make(chan error, 1)
	go func() {
		log.Info("http server listening", "addr", cfg.HTTPAddr, "env", cfg.Env)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- err
		}
	}()

	select {
	case err := <-errc:
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
		log.Info("shutdown signal received")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutting down http server: %w", err)
	}
	log.Info("shutdown complete")
	return nil
}

func newLogger(env string) *slog.Logger {
	level := slog.LevelInfo
	if env == "dev" {
		level = slog.LevelDebug
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
}

// buildStorage selects the object store.
//
// The s3 driver is not implemented: the volumes this system targets do not need
// it yet, and shipping a stub that silently loses files would be worse than
// refusing to start. See docs/06-deep-dives.md for the thresholds that make the
// switch worth building.
func buildStorage(cfg config.Config) (storage.Storage, error) {
	switch cfg.Storage.Driver {
	case "local":
		return storage.NewLocal(cfg.Storage.LocalPath, cfg.BaseURL, cfg.VisitPepper)
	case "s3":
		return nil, fmt.Errorf("STORAGE_DRIVER=s3 is not implemented yet; use local storage")
	default:
		return nil, fmt.Errorf("unknown storage driver %q", cfg.Storage.Driver)
	}
}

// buildNotifier returns the mail sender, falling back to the log when no SMTP
// server is configured.
//
// The fallback is loud on purpose: a deployment that silently swallows
// invitations looks healthy while nobody can join it.
func buildNotifier(cfg config.Config, log *slog.Logger) (notify.Notifier, error) {
	if !cfg.SMTP.Configured() {
		log.Warn("no SMTP configured: invitations and data subject links will be written to the log, not sent")
		return notify.NewLogNotifier(log), nil
	}
	n, err := notify.NewSMTPNotifier(notify.SMTPConfig{
		Host: cfg.SMTP.Host, Port: cfg.SMTP.Port,
		Username: cfg.SMTP.Username, Password: cfg.SMTP.Password,
		From: cfg.SMTP.From, StartTLS: cfg.SMTP.StartTLS,
	})
	if err != nil {
		return nil, fmt.Errorf("configuring smtp: %w", err)
	}
	log.Info("smtp configured", "host", cfg.SMTP.Host, "from", cfg.SMTP.From)
	return n, nil
}

// hostOf extracts the host from a base URL for self-redirect detection.
// hostPortOf keeps the port, unlike hostOf.
//
// A link domain has to equal what arrives in the Host header, because that is
// what links.resolve() matches on. Dropping the port would register
// "example.local" for a deployment reached at "example.local:8080", and every
// short code on it would resolve to nothing.
func hostPortOf(baseURL string) string {
	u, err := url.Parse(baseURL)
	if err != nil {
		return ""
	}
	return u.Host
}

func hostOf(baseURL string) string {
	u, err := url.Parse(baseURL)
	if err != nil {
		return ""
	}
	return u.Hostname()
}
