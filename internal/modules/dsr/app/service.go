// Package app implements the data subject portal.
package app

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"
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
	// RekeyIdentifier moves the subject's lookup key when they correct the very
	// answer they are recognised by. See contracts.SubjectResolver.
	RekeyIdentifier(ctx context.Context, tx pgx.Tx, tenantID, subjectID uuid.UUID, kind, value string) error
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
	opener   contracts.SensitiveOpener
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
	// Opener decrypts a submission's sealed answers. Optional: without it the
	// portal shows the plaintext answers only, and says so rather than implying
	// those are all the data there is.
	Opener contracts.SensitiveOpener
}

// NewService returns a Service.
func NewService(d Deps) *Service {
	return &Service{
		db: d.DB, store: d.Store, subjects: d.Subjects, audit: d.Audit,
		notifier: d.Notifier, log: d.Log, baseURL: d.BaseURL, sla: d.SLA,
		opener: d.Opener,
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
	subs, err := s.store.SubjectSubmissions(ctx, sess.TenantID, sess.SubjectID, sess.SubmissionID)
	if err != nil {
		return nil, err
	}

	// Sensitive answers are merged into what the subject sees.
	//
	// Article 4 of Law 91/2025 gives a data subject the right to view their
	// personal data; the limits it sets are national defence and security, and
	// harm to another person's life or health. Being sensitive is not one of
	// them -- and sensitive is precisely the category defined by how much its
	// exposure affects the person, so their interest in seeing it is stronger
	// than for anything else on the page, not weaker. Withholding it here while
	// the same link offers irreversible erasure would be incoherent.
	//
	// The key is the subject's own. This is the one party who can decrypt these
	// bytes without anybody having to decide whether they should be allowed to.
	for i := range subs {
		if len(subs[i].SensitiveBlob) == 0 || s.opener == nil {
			continue
		}
		sealed, err := s.opener.OpenSensitive(ctx, sess.TenantID, sess.SubjectID, subs[i].ID, subs[i].SensitiveBlob)
		if err != nil {
			// Logged and skipped rather than failing the whole page: a subject
			// who cannot read one encrypted field must still be able to see the
			// rest of their data and to exercise the other rights. The portal
			// marks the field as unreadable rather than omitting it silently.
			s.log.Error("opening sensitive answers for subject",
				"error", err, "submission_id", subs[i].ID)
			subs[i].SensitiveUnreadable = true
			continue
		}
		if subs[i].Answers == nil {
			subs[i].Answers = make(map[string]any, len(sealed))
		}
		for k, v := range sealed {
			subs[i].Answers[k] = v
		}
	}
	return subs, nil
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
	// Read before the write so a corrected identifier can be recognised as one.
	identity, err := s.store.IdentityOf(ctx, sess.TenantID, submissionID)
	if err != nil {
		return err
	}

	// One transaction for the correction, the re-key and the audit entry.
	//
	// They used to be two: the answers were written in their own transaction and
	// the audit entry followed in another. A correction could therefore succeed
	// and leave no trace of itself, which is the one thing an audit trail exists
	// to make impossible.
	return s.db.InTenantTx(ctx, sess.TenantID, func(tx pgx.Tx) error {
		if err := s.store.RectifyTx(ctx, tx, sess.TenantID, sess.SubjectID, submissionID,
			answers, store.SubjectRectifier(sess.SubjectID)); err != nil {
			return err
		}
		// The same two-places problem the operator path has: the identifier is
		// an answer in the clear and an HMAC used to find people, and correcting
		// one without the other leaves the subject unable to sign in with the
		// address their own record now shows.
		if err := s.rekeyIfIdentifierChanged(ctx, tx, sess, identity, answers); err != nil {
			return err
		}
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

// rekeyIfIdentifierChanged moves the subject's lookup key when the correction
// changed the answer they are recognised by.
//
// Silent no-op when the form has no identifier question, when the correction did
// not touch it, or when the value is unchanged -- all of them ordinary.
func (s *Service) rekeyIfIdentifierChanged(ctx context.Context, tx pgx.Tx, sess Session,
	identity store.Identity, answers map[string]any,
) error {
	if s.subjects == nil || identity.FieldID == "" {
		return nil
	}
	next, ok := answers[identity.FieldID].(string)
	if !ok {
		return nil
	}
	next = strings.TrimSpace(next)
	if next == "" || next == identity.Value {
		return nil
	}
	// The kind comes from the schema, not from what the new value looks like: a
	// phone-identified form whose answer now contains an "@" is a bad
	// correction, not a change of channel.
	if !slices.Contains([]string{"email", "phone"}, identity.Kind) {
		return nil
	}
	return s.subjects.RekeyIdentifier(ctx, tx, sess.TenantID, sess.SubjectID, identity.Kind, next)
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
