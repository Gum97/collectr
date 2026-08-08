// Package app implements link creation and the redirect hot path.
package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"golang.org/x/sync/singleflight"

	"github.com/collectr/collectr/internal/modules/links/domain"
	"github.com/collectr/collectr/internal/platform/idgen"
	"github.com/collectr/collectr/internal/platform/redisx"
)

// negativeMarker is what a "this code does not exist" cache entry holds.
//
// Caching absence matters more here than it looks: without it, anyone walking the
// code space bypasses the cache entirely on every request and turns a scan into a
// database load test. A shortener fronting forms that collect personal data is a
// natural target for exactly that walk.
const negativeMarker = "-"

// Repository is the persistence the service needs.
type Repository interface {
	Resolve(ctx context.Context, host, code string) (domain.Resolution, error)
	Insert(ctx context.Context, l domain.Link) error
	Get(ctx context.Context, tenantID, id uuid.UUID) (domain.Link, error)
	ListByProject(ctx context.Context, tenantID, projectID uuid.UUID, before time.Time, limit int) ([]domain.Link, error)
	UpdateStatus(ctx context.Context, tenantID, id uuid.UUID, status string) (host, code string, err error)
	DefaultDomain(ctx context.Context, tenantID uuid.UUID) (uuid.UUID, string, error)
	ListDomains(ctx context.Context, tenantID uuid.UUID) ([]domain.Domain, error)
	InsertDomain(ctx context.Context, tenantID uuid.UUID, host string, makeDefault bool) (domain.Domain, error)
	SetDefaultDomain(ctx context.Context, tenantID, id uuid.UUID) error
	DeleteDomain(ctx context.Context, tenantID, id uuid.UUID) error
}

// Service resolves and manages links.
type Service struct {
	repo  Repository
	rdb   *redisx.Client
	log   *slog.Logger
	group singleflight.Group

	cacheTTL    time.Duration
	negativeTTL time.Duration
	selfHosts   []string
}

// Options configures a Service.
type Options struct {
	CacheTTL         time.Duration
	NegativeCacheTTL time.Duration
	// SelfHosts are hosts this deployment answers on; links may not target them.
	SelfHosts []string
}

// NewService returns a Service.
func NewService(repo Repository, rdb *redisx.Client, log *slog.Logger, opts Options) *Service {
	return &Service{
		repo:        repo,
		rdb:         rdb,
		log:         log,
		cacheTTL:    opts.CacheTTL,
		negativeTTL: opts.NegativeCacheTTL,
		selfHosts:   opts.SelfHosts,
	}
}

// Resolve returns the destination for (host, code).
//
// Read path: Redis, then the database behind a single-flight guard, then back
// into Redis. Cache failures are logged and ignored -- a degraded cache must slow
// the redirect down, never break it.
func (s *Service) Resolve(ctx context.Context, host, code string) (domain.Resolution, error) {
	host = strings.ToLower(host)
	key := cacheKey(host, code)

	if res, found, err := s.fromCache(ctx, key); err != nil {
		s.log.Warn("link cache read", "error", err)
	} else if found {
		return res, res.Check(time.Now())
	}

	// One database lookup per key at a time. Without this, a popular code
	// expiring under load sends every concurrent request to Postgres at once.
	v, err, _ := s.group.Do(key, func() (any, error) {
		res, err := s.repo.Resolve(ctx, host, code)
		if errors.Is(err, domain.ErrNotFound) {
			s.cacheNegative(ctx, key)
			return nil, err
		}
		if err != nil {
			return nil, err
		}
		s.cacheResolution(ctx, key, res)
		return res, nil
	})
	if err != nil {
		return domain.Resolution{}, err
	}

	res := v.(domain.Resolution)
	return res, res.Check(time.Now())
}

func (s *Service) fromCache(ctx context.Context, key string) (domain.Resolution, bool, error) {
	raw, err := s.rdb.Get(ctx, key).Result()
	if errors.Is(err, redis.Nil) {
		return domain.Resolution{}, false, nil
	}
	if err != nil {
		return domain.Resolution{}, false, fmt.Errorf("reading cache: %w", err)
	}
	if raw == negativeMarker {
		return domain.Resolution{}, true, nil
	}
	var res domain.Resolution
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		return domain.Resolution{}, false, fmt.Errorf("decoding cached link: %w", err)
	}
	return res, true, nil
}

func (s *Service) cacheResolution(ctx context.Context, key string, res domain.Resolution) {
	payload, err := json.Marshal(res)
	if err != nil {
		s.log.Error("encoding link for cache", "error", err)
		return
	}
	ttl := redisx.JitterTTL(s.cacheTTL)
	// Never outlive the link itself: an entry that survives expiry would keep
	// serving a destination the operator has already retired.
	if res.ExpiresAt != nil {
		if remaining := time.Until(*res.ExpiresAt); remaining > 0 && remaining < ttl {
			ttl = remaining
		}
	}
	if err := s.rdb.Set(ctx, key, payload, ttl).Err(); err != nil {
		s.log.Warn("link cache write", "error", err)
	}
}

func (s *Service) cacheNegative(ctx context.Context, key string) {
	if err := s.rdb.Set(ctx, key, negativeMarker, redisx.JitterTTL(s.negativeTTL)).Err(); err != nil {
		s.log.Warn("negative cache write", "error", err)
	}
}

// Invalidate drops the cached entry for a code.
//
// Delete rather than overwrite: an overwrite can race with an in-flight fill and
// lose, leaving a stale value behind until its TTL. A delete costs one miss.
func (s *Service) Invalidate(ctx context.Context, host, code string) {
	if err := s.rdb.Del(ctx, cacheKey(strings.ToLower(host), code)).Err(); err != nil {
		s.log.Warn("link cache invalidation", "error", err, "code", code)
	}
}

// CreateInput describes a link to create.
type CreateInput struct {
	TenantID  uuid.UUID
	ProjectID uuid.UUID
	CreatedBy uuid.UUID
	TargetURL string
	FormID    *uuid.UUID
	Alias     string
	ExpiresAt *time.Time
}

// Create stores a new link.
//
// With a custom alias a conflict is reported to the user. With a generated code
// it is retried: at 62^7 the collision rate is negligible, and the unique index
// -- not a prior existence check -- is what decides.
func (s *Service) Create(ctx context.Context, in CreateInput) (domain.Link, error) {
	if in.TargetURL == "" && in.FormID == nil {
		return domain.Link{}, domain.ErrInvalidTarget
	}

	domainID, host, err := s.repo.DefaultDomain(ctx, in.TenantID)
	if err != nil {
		return domain.Link{}, err
	}

	target := in.TargetURL
	if target != "" {
		selfHosts := append([]string{host}, s.selfHosts...)
		if target, err = domain.ValidateTarget(target, selfHosts); err != nil {
			return domain.Link{}, err
		}
	}

	link := domain.Link{
		TenantID:  in.TenantID,
		ProjectID: in.ProjectID,
		DomainID:  domainID,
		Host:      host,
		TargetURL: target,
		FormID:    in.FormID,
		ExpiresAt: in.ExpiresAt,
		Status:    domain.StatusActive,
		CreatedBy: in.CreatedBy,
	}

	if in.Alias != "" {
		if err := idgen.ValidateAlias(in.Alias); err != nil {
			return domain.Link{}, fmt.Errorf("%w: %s", domain.ErrInvalidTarget, err)
		}
		link.ID = uuid.New()
		link.Code = in.Alias
		link.CreatedAt = time.Now().UTC()
		if err := s.repo.Insert(ctx, link); err != nil {
			return domain.Link{}, err
		}
		s.Invalidate(ctx, host, link.Code)
		return link, nil
	}

	const maxAttempts = 3
	for attempt := range maxAttempts {
		code, err := idgen.Code(idgen.DefaultCodeLength)
		if err != nil {
			return domain.Link{}, err
		}
		link.ID = uuid.New()
		link.Code = code
		link.CreatedAt = time.Now().UTC()

		err = s.repo.Insert(ctx, link)
		switch {
		case err == nil:
			s.Invalidate(ctx, host, code)
			return link, nil
		case errors.Is(err, domain.ErrAliasTaken):
			s.log.Warn("short code collision", "attempt", attempt+1, "code", code)
		default:
			return domain.Link{}, err
		}
	}
	return domain.Link{}, fmt.Errorf("generating unique short code after %d attempts", maxAttempts)
}

// Get returns one link.
func (s *Service) Get(ctx context.Context, tenantID, id uuid.UUID) (domain.Link, error) {
	return s.repo.Get(ctx, tenantID, id)
}

// List returns a page of links for a project.
func (s *Service) List(ctx context.Context, tenantID, projectID uuid.UUID, before time.Time, limit int) ([]domain.Link, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	if before.IsZero() {
		before = time.Now().UTC().Add(time.Minute)
	}
	return s.repo.ListByProject(ctx, tenantID, projectID, before, limit)
}

// Delete soft-deletes a link; visitors then receive 410 rather than 404.
func (s *Service) Delete(ctx context.Context, tenantID, id uuid.UUID) error {
	host, code, err := s.repo.UpdateStatus(ctx, tenantID, id, domain.StatusDeleted)
	if err != nil {
		return err
	}
	s.Invalidate(ctx, host, code)
	return nil
}

// cacheKey carries a version prefix.
//
// Bump it whenever a field is added to domain.Resolution. Entries written by the
// previous build decode without error -- the missing field is simply zero -- so
// a rollout would serve links that silently lost whatever the new field carries,
// for as long as the TTL lasts. Changing the key retires them instead.
func cacheKey(host, code string) string {
	return "l2:" + host + ":" + strings.ToLower(code)
}

// Domains lists the hosts a tenant may issue short codes on.
func (s *Service) Domains(ctx context.Context, tenantID uuid.UUID) ([]domain.Domain, error) {
	return s.repo.ListDomains(ctx, tenantID)
}

// AddDomain registers a hostname.
func (s *Service) AddDomain(ctx context.Context, tenantID uuid.UUID, host string, makeDefault bool) (domain.Domain, error) {
	normalised, err := domain.ValidateHost(host)
	if err != nil {
		return domain.Domain{}, err
	}
	return s.repo.InsertDomain(ctx, tenantID, normalised, makeDefault)
}

// SetDefaultDomain chooses which host new links are created on.
func (s *Service) SetDefaultDomain(ctx context.Context, tenantID, id uuid.UUID) error {
	return s.repo.SetDefaultDomain(ctx, tenantID, id)
}

// RemoveDomain deletes a host that carries no links.
func (s *Service) RemoveDomain(ctx context.Context, tenantID, id uuid.UUID) error {
	return s.repo.DeleteDomain(ctx, tenantID, id)
}

// SetStatus moves a link between active, disabled and legal hold.
//
// Deletion is not reachable here: it has its own path with its own audit entry,
// and a status field that can quietly delete is a status field somebody will
// eventually set from a dropdown.
func (s *Service) SetStatus(ctx context.Context, tenantID, id uuid.UUID, status string) (domain.Link, error) {
	switch status {
	case domain.StatusActive, domain.StatusDisabled, domain.StatusLegalHold:
	default:
		return domain.Link{}, fmt.Errorf("%w: trạng thái phải là active, disabled hoặc legal_hold",
			domain.ErrInvalidTarget)
	}

	host, code, err := s.repo.UpdateStatus(ctx, tenantID, id, status)
	if err != nil {
		return domain.Link{}, err
	}
	// The cached resolution decides whether a scan redirects or is refused, so it
	// has to go now rather than at the end of its TTL: a link put on hold that
	// keeps redirecting for another minute is the hold not working.
	s.Invalidate(ctx, host, code)
	return s.repo.Get(ctx, tenantID, id)
}
