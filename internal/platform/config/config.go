// Package config loads and validates runtime configuration from the environment.
//
// Every compliance-relevant deadline (DSR SLA, retention) is configuration rather
// than a constant: the statutory numbers are subject to legal interpretation and
// may change with implementing guidance.
package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is the fully validated configuration of one Collectr process.
type Config struct {
	Env      string // dev | prod
	HTTPAddr string
	BaseURL  string // public origin of the app: form pages, DSR portal, admin API
	// ShortURLBase is the origin short links are served from. Defaults to
	// BaseURL, so a single-domain deployment needs no extra configuration; set
	// it to run the shortener on its own hostname, which is the usual reason to
	// have a shortener at all -- a short link on a long domain is not short.
	ShortURLBase string

	// TrustedProxyHops is how many reverse proxies sit in front of the process.
	TrustedProxyHops int
	// MFAGrace is how long a role that requires MFA may run without it.
	MFAGrace time.Duration

	DatabaseURL string
	RedisURL    string

	Storage Storage
	SMTP    SMTP

	// TenantKEK is the root key wrapping every per-subject data key. Losing it
	// destroys all sensitive data irrecoverably; that is what makes erasure real.
	TenantKEK []byte
	// VisitPepper keys the HMAC over visit tokens, keeping visit ids from being
	// forgeable and from correlating across deployments.
	VisitPepper []byte

	DSRSLA               time.Duration
	DefaultRetention     time.Duration
	RawEventRetention    time.Duration
	DeploymentRole       string // controller | processor
	RunWorkerInline      bool
	EventStreamMaxLen    int64
	EventBufferSize      int
	LinkCacheTTL         time.Duration
	LinkNegativeCacheTTL time.Duration
	VisitTokenTTL        time.Duration
}

// SMTP configures outbound mail. When Host is empty the deployment falls back to
// writing messages to the log, which is usable for evaluation and not for
// anything else -- an invited colleague cannot read your container logs.
type SMTP struct {
	Host     string
	Port     int
	Username string
	Password string
	From     string
	StartTLS bool
}

// Configured reports whether mail can actually be sent.
func (s SMTP) Configured() bool { return s.Host != "" && s.From != "" }

// Storage selects where uploaded files live. The local driver is the default;
// see docs/06-deep-dives.md for the thresholds that justify switching to s3.
type Storage struct {
	Driver    string // local | s3
	LocalPath string
	S3        S3
}

// S3 holds the object storage settings used when Storage.Driver is "s3".
type S3 struct {
	Endpoint  string
	Bucket    string
	Region    string
	AccessKey string
	SecretKey string
	UseSSL    bool
}

// Load reads configuration from the process environment.
//
// It returns every validation problem at once rather than the first: an operator
// bringing up a new deployment should not have to rerun to discover each mistake.
func Load() (Config, error) {
	var errs []error
	fail := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf(format, args...))
	}

	cfg := Config{
		Env:                  envOr("ENV", "dev"),
		HTTPAddr:             envOr("HTTP_ADDR", ":8080"),
		BaseURL:              strings.TrimRight(envOr("BASE_URL", "http://localhost:8080"), "/"),
		ShortURLBase:         strings.TrimRight(envOr("SHORT_URL_BASE", ""), "/"),
		DatabaseURL:          os.Getenv("DATABASE_URL"),
		RedisURL:             os.Getenv("REDIS_URL"),
		DeploymentRole:       envOr("DEPLOYMENT_ROLE", "controller"),
		EventStreamMaxLen:    int64(envInt("EVENT_STREAM_MAXLEN", 1_000_000, &errs)),
		EventBufferSize:      envInt("EVENT_BUFFER_SIZE", 10_000, &errs),
		LinkCacheTTL:         time.Duration(envInt("LINK_CACHE_TTL_SECONDS", 300, &errs)) * time.Second,
		LinkNegativeCacheTTL: time.Duration(envInt("LINK_NEGATIVE_CACHE_TTL_SECONDS", 30, &errs)) * time.Second,
		VisitTokenTTL:        time.Duration(envInt("VISIT_TOKEN_TTL_MINUTES", 30, &errs)) * time.Minute,
		DSRSLA:               time.Duration(envInt("DSR_SLA_HOURS", 72, &errs)) * time.Hour,
		DefaultRetention:     time.Duration(envInt("DEFAULT_RETENTION_DAYS", 730, &errs)) * 24 * time.Hour,
		RawEventRetention:    time.Duration(envInt("RAW_EVENT_RETENTION_DAYS", 90, &errs)) * 24 * time.Hour,
		RunWorkerInline:      os.Getenv("RUN_WORKER_INLINE") == "true",
		SMTP: SMTP{
			Host:     os.Getenv("SMTP_HOST"),
			Port:     envInt("SMTP_PORT", 587, &errs),
			Username: os.Getenv("SMTP_USERNAME"),
			Password: os.Getenv("SMTP_PASSWORD"),
			From:     os.Getenv("SMTP_FROM"),
			StartTLS: os.Getenv("SMTP_STARTTLS") != "false",
		},
		Storage: Storage{
			Driver:    envOr("STORAGE_DRIVER", "local"),
			LocalPath: envOr("STORAGE_LOCAL_PATH", "/data/files"),
			S3: S3{
				Endpoint:  os.Getenv("STORAGE_S3_ENDPOINT"),
				Bucket:    os.Getenv("STORAGE_S3_BUCKET"),
				Region:    envOr("STORAGE_S3_REGION", "us-east-1"),
				AccessKey: os.Getenv("STORAGE_S3_ACCESS_KEY"),
				SecretKey: os.Getenv("STORAGE_S3_SECRET_KEY"),
				UseSSL:    os.Getenv("STORAGE_S3_USE_SSL") != "false",
			},
		},
	}

	// How much of X-Forwarded-For to believe. One hop matches the shipped
	// compose file; zero is correct when the binary is exposed directly, and
	// anything else means the operator knows their own topology.
	cfg.TrustedProxyHops = envInt("TRUSTED_PROXY_HOPS", 1, &errs)

	// How long a privileged account may work before it must enrol a second
	// factor. Zero enforces immediately.
	//
	// Not zero by default: on a fresh self-hosted install the first thing the
	// owner does is look around, and a product that answers "you may not" to
	// every screen before showing anything gets uninstalled rather than
	// secured. Three days is long enough to set up an authenticator without
	// being long enough to forget.
	cfg.MFAGrace = time.Duration(envInt("MFA_GRACE_HOURS", 72, &errs)) * time.Hour

	if cfg.ShortURLBase == "" {
		cfg.ShortURLBase = cfg.BaseURL
	}

	if cfg.DatabaseURL == "" {
		fail("DATABASE_URL is required")
	}
	if cfg.RedisURL == "" {
		fail("REDIS_URL is required")
	}

	cfg.TenantKEK = decodeKey("TENANT_KEK", 32, &errs)
	cfg.VisitPepper = decodeKey("VISIT_PEPPER", 32, &errs)

	switch cfg.Storage.Driver {
	case "local":
		if cfg.Storage.LocalPath == "" {
			fail("STORAGE_LOCAL_PATH is required when STORAGE_DRIVER=local")
		}
	case "s3":
		if cfg.Storage.S3.Bucket == "" || cfg.Storage.S3.AccessKey == "" {
			fail("STORAGE_S3_BUCKET and STORAGE_S3_ACCESS_KEY are required when STORAGE_DRIVER=s3")
		}
	default:
		fail("STORAGE_DRIVER must be local or s3, got %q", cfg.Storage.Driver)
	}

	// Half-configured mail is worse than none: it fails at the moment someone is
	// waiting for an invitation, not at startup where it would be noticed.
	if (cfg.SMTP.Host != "") != (cfg.SMTP.From != "") {
		fail("SMTP_HOST and SMTP_FROM must be set together")
	}

	switch cfg.DeploymentRole {
	case "controller", "processor":
	default:
		fail("DEPLOYMENT_ROLE must be controller or processor, got %q", cfg.DeploymentRole)
	}

	if err := errors.Join(errs...); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int, errs *[]error) int {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		*errs = append(*errs, fmt.Errorf("%s must be an integer: %w", key, err))
		return fallback
	}
	return n
}

func decodeKey(key string, want int, errs *[]error) []byte {
	raw := os.Getenv(key)
	if raw == "" {
		*errs = append(*errs, fmt.Errorf("%s is required (run `make secrets` to generate one)", key))
		return nil
	}
	b, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		*errs = append(*errs, fmt.Errorf("%s must be base64: %w", key, err))
		return nil
	}
	if len(b) != want {
		*errs = append(*errs, fmt.Errorf("%s must decode to %d bytes, got %d", key, want, len(b)))
		return nil
	}
	return b
}
