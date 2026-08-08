// Package app implements the data subject portal.
package app

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/collectr/collectr/internal/contracts"
	"github.com/collectr/collectr/internal/modules/dsr/domain"
	"github.com/collectr/collectr/internal/modules/dsr/store"
	"github.com/collectr/collectr/internal/platform/notify"
	"github.com/collectr/collectr/internal/platform/postgres"
)

// Rate limits for the identify endpoint.
const (
	identifyLimit  = 3
	identifyWindow = time.Hour
)

// SubjectLookup resolves an identifying value to an existing subject without
// creating one.
type SubjectLookup interface {
	FindSubject(ctx context.Context, tenantID uuid.UUID, kind, value string) (contracts.Subject, error)
}

// Service drives the portal.
type Service struct {
	db       *postgres.DB
	store    *store.Store
	subjects SubjectLookup
	audit    contracts.AuditWriter
	notifier notify.Notifier
	log      *slog.Logger
	baseURL  string
	sla      time.Duration
}

// Deps are the Service's collaborators.
type Deps struct {
	DB       *postgres.DB
	Store    *store.Store
	Subjects SubjectLookup
	Audit    contracts.AuditWriter
	Notifier notify.Notifier
	Log      *slog.Logger
	BaseURL  string
	SLA      time.Duration
}

// NewService returns a Service.
func NewService(d Deps) *Service {
	return &Service{
		db: d.DB, store: d.Store, subjects: d.Subjects, audit: d.Audit,
		notifier: d.Notifier, log: d.Log, baseURL: d.BaseURL, sla: d.SLA,
	}
}

// Identify starts the portal flow for an email address or phone number.
//
// It always reports success, whether or not the identifier is known, and it does
// the same amount of work either way. An endpoint that answers "we have never
// heard of this address" is a membership oracle: point it at a list of customers
// and it tells you which of them a company holds data on.
//
// The link is sent through the identifier itself, so only whoever controls that
// mailbox or number can continue.
func (s *Service) Identify(ctx context.Context, tenantID uuid.UUID, kind, value string) {
	subject, err := s.subjects.FindSubject(ctx, tenantID, kind, value)
	switch {
	case err != nil:
		s.log.Error("resolving data subject", "error", err)
		return
	case subject.ID == uuid.Nil:
		// Unknown identifier. Nothing is sent, and the caller cannot tell.
		return
	case subject.Erased:
		// Already erased: there is nothing left to show, and reissuing access
		// would undo the point of the erasure.
		return
	}

	allowed, err := s.store.RegisterAttempt(ctx, tenantID, subject.IdentifierHash, identifyLimit, identifyWindow)
	if err != nil {
		s.log.Error("rate limiting identify", "error", err)
		return
	}
	if !allowed {
		// Silent. Telling the caller they are rate limited confirms the
		// identifier exists, which is precisely what the endpoint must not reveal.
		s.log.Warn("identify rate limit reached", "tenant_id", tenantID)
		return
	}

	raw, err := newToken()
	if err != nil {
		s.log.Error("generating dsr token", "error", err)
		return
	}
	if _, err := s.store.IssueToken(ctx, tenantID, subject.ID, raw, domain.ScopePortal, nil, domain.TokenTTL); err != nil {
		s.log.Error("issuing dsr token", "error", err)
		return
	}

	// The tenant travels with the token because the page that receives this link
	// has no other way to learn it: the portal cookie is scoped to /api/dsr, so
	// the browser does not send anything identifying on the way in. Without it
	// POST /api/dsr/session cannot be called at all, and the portal answers 401
	// to the person whose data it exists to show them.
	//
	// The tenant id is not a secret -- it appears in every admin URL. The token
	// is, and it is hashed at rest; keeping the tenant in the exchange call stays
	// as a second condition rather than the only one.
	link := fmt.Sprintf("%s/dsr?token=%s&t=%s", s.baseURL, raw, tenantID)
	if err := s.notifier.Send(ctx, notify.Message{
		To:      value,
		Subject: "Truy cập dữ liệu cá nhân của bạn",
		Body: "Nhấn vào liên kết dưới đây để xem, sửa hoặc yêu cầu xóa dữ liệu cá nhân của bạn.\n\n" +
			link + "\n\nLiên kết có hiệu lực trong 15 phút và chỉ dùng được một lần.\n" +
			"Nếu bạn không yêu cầu, hãy bỏ qua email này.",
	}); err != nil {
		s.log.Error("sending dsr link", "error", err)
	}
}

// Session is a verified portal session.
type Session struct {
	TenantID     uuid.UUID
	SubjectID    uuid.UUID
	Scope        string
	SubmissionID *uuid.UUID
	ExpiresAt    time.Time
}

// CanSee reports whether the session may access a given submission.
//
// A receipt link covers exactly one record. Widening it to the whole history
// would turn a convenience into a way to read someone's entire relationship with
// a company from a single forwarded email.
func (s Session) CanSee(submissionID uuid.UUID) bool {
	if s.Scope == domain.ScopePortal {
		return true
	}
	return s.SubmissionID != nil && *s.SubmissionID == submissionID
}

// Exchange trades a magic-link token for a session.
func (s *Service) Exchange(ctx context.Context, tenantID uuid.UUID, raw string) (Session, error) {
	t, err := s.store.ConsumeToken(ctx, tenantID, raw)
	if err != nil {
		return Session{}, err
	}
	return Session{
		TenantID: tenantID, SubjectID: t.SubjectID, Scope: t.Scope,
		SubmissionID: t.SubmissionID, ExpiresAt: time.Now().UTC().Add(domain.SessionTTL),
	}, nil
}

// MySubmissions returns what the session is entitled to see.
func (s *Service) MySubmissions(ctx context.Context, sess Session) ([]store.SubjectSubmission, error) {
	return s.store.SubjectSubmissions(ctx, sess.TenantID, sess.SubjectID, sess.SubmissionID)
}

// MyRequests returns the subject's request history.
func (s *Service) MyRequests(ctx context.Context, sess Session) ([]domain.Request, error) {
	return s.store.ListRequests(ctx, sess.TenantID, sess.SubjectID)
}

// Rectify corrects one submission.
func (s *Service) Rectify(ctx context.Context, sess Session, submissionID uuid.UUID, answers map[string]any) error {
	if !sess.CanSee(submissionID) {
		return domain.ErrForbidden
	}
	if err := s.store.RectifySubmission(ctx, sess.TenantID, sess.SubjectID, submissionID, answers); err != nil {
		return err
	}

	return s.db.InTenantTx(ctx, sess.TenantID, func(tx pgx.Tx) error {
		return s.audit.Write(ctx, tx, contracts.AuditEntry{
			TenantID: sess.TenantID,
			Actor:    contracts.AuditActor{Type: "subject", ID: sess.SubjectID.String()},
			Action:   "submission.updated",
			Target:   map[string]any{"submission_id": submissionID},
			// The changed values are not recorded here: the audit trail proves an
			// edit happened and who made it. The values live in the revision row.
			Payload: map[string]any{"source": "dsr_self_service"},
		})
	})
}

// Raise records an exercise of a right.
//
// Erasure and withdrawal are queued rather than performed inline: they touch many
// rows across several modules, and a request that is recorded and then completed
// by a worker survives a crash in a way that one performed mid-HTTP-request does
// not.
func (s *Service) Raise(ctx context.Context, sess Session, reqType string) (domain.Request, error) {
	if !domain.ValidType(reqType) {
		return domain.Request{}, fmt.Errorf("unknown request type %q", reqType)
	}
	if sess.Scope != domain.ScopePortal && reqType != domain.TypeRectify {
		// A receipt link may correct its own record but not erase an entire
		// history.
		return domain.Request{}, domain.ErrForbidden
	}

	var req domain.Request
	err := s.db.InTenantTx(ctx, sess.TenantID, func(tx pgx.Tx) error {
		var err error
		req, err = s.store.CreateRequest(ctx, tx, sess.TenantID, sess.SubjectID, reqType, "magic_link", s.sla)
		if err != nil {
			return err
		}
		return s.audit.Write(ctx, tx, contracts.AuditEntry{
			TenantID: sess.TenantID,
			Actor:    contracts.AuditActor{Type: "subject", ID: sess.SubjectID.String()},
			Action:   "dsr.received",
			Target:   map[string]any{"request_id": req.ID},
			Payload:  map[string]any{"type": reqType, "due_at": req.DueAt},
		})
	})
	if err != nil {
		return domain.Request{}, err
	}
	return req, nil
}

// IssueReceipt mints the narrow token handed out at submission time.
func (s *Service) IssueReceipt(ctx context.Context, tenantID, subjectID, submissionID uuid.UUID, ttl time.Duration) (string, error) {
	raw, err := newToken()
	if err != nil {
		return "", err
	}
	if _, err := s.store.IssueToken(ctx, tenantID, subjectID, raw, domain.ScopeReceipt, &submissionID, ttl); err != nil {
		return "", err
	}
	return raw, nil
}

// newToken returns 256 bits of URL-safe randomness.
func newToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, buf); err != nil {
		return "", fmt.Errorf("generating token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
