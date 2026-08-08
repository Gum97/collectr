package contracts

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Consent actions.
const (
	ConsentGranted   = "granted"
	ConsentWithdrawn = "withdrawn"
)

// Subject is a person whose data the system holds, identified by a keyed hash of
// their email or phone number rather than by the value itself.
type Subject struct {
	ID       uuid.UUID
	TenantID uuid.UUID
	Kind     string
	Erased   bool
	// IdentifierHash is the keyed hash of the identifying value. It is exposed so
	// callers can rate-limit per person without ever handling the raw address.
	IdentifierHash []byte
}

// ConsentGrant is one purpose the respondent did or did not agree to.
type ConsentGrant struct {
	PurposeCode string
	Granted     bool
}

// ConsentEvidence is what makes a record provable rather than merely recorded.
//
// RenderedHash is the hash of the text the client actually displayed. The server
// compares it against the stored document and refuses the submission if they
// differ -- otherwise "the data subject agreed to this text" would rest on
// nothing but the server's own assertion.
type ConsentEvidence struct {
	RenderedHash []byte
	Method       string // checkbox | signature
	IPPrefix     string
	UserAgent    string
	Locale       string
	ClientTime   time.Time
}

// RecordConsentInput is one consent decision being written.
type RecordConsentInput struct {
	TenantID      uuid.UUID
	SubjectID     uuid.UUID
	DocumentID    uuid.UUID
	SubmissionID  *uuid.UUID
	FormVersionID *uuid.UUID
	Grants        []ConsentGrant
	Evidence      ConsentEvidence
}

// ConsentRecorder writes consent decisions.
//
// Record takes the caller's transaction rather than opening its own. That is
// what keeps "a submission and its lawful basis are written together or not at
// all" true without the forms module needing to know a single consent table --
// the boundary survives, and so does the invariant.
type ConsentRecorder interface {
	Record(ctx context.Context, tx pgx.Tx, in RecordConsentInput) error
}

// ConsentChecker answers whether a purpose is currently agreed to.
//
// Every export, sync or message must ask before acting: a withdrawal is only
// meaningful if something checks it.
type ConsentChecker interface {
	HasActive(ctx context.Context, tenantID, subjectID uuid.UUID, purposeCode string) (bool, error)
}

// SubjectResolver maps an identifying value to a stable subject.
type SubjectResolver interface {
	UpsertSubject(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, kind, value string) (Subject, error)
	// FindSubject looks up an existing subject without creating one. It returns a
	// zero-valued Subject when the identifier is unknown, so that callers cannot
	// accidentally turn a lookup into a way to confirm someone's existence.
	FindSubject(ctx context.Context, tenantID uuid.UUID, kind, value string) (Subject, error)
	// DataKey returns the subject's decrypted data key, creating one if needed.
	// It returns ErrSubjectErased once the key has been destroyed.
	DataKey(ctx context.Context, tx pgx.Tx, subjectID uuid.UUID) ([]byte, error)
	// RekeyIdentifier moves a subject's lookup key to a corrected value.
	//
	// Needed because the identifier is stored as an HMAC and the answer holding
	// it is stored in the clear: correcting the answer alone leaves the two
	// disagreeing. The consequences are not cosmetic. The person who owns the
	// corrected address cannot reach their own data through the portal, and the
	// mistyped address -- which may well belong to somebody else -- still can.
	//
	// It changes only how the subject is found. The subject id, the data key and
	// the consent history are untouched, so nothing already recorded moves.
	//
	// Returns ErrIdentifierTaken when the new value already belongs to a
	// different subject. Joining two subjects means merging two consent
	// histories and two keys, which is a different operation with different
	// consequences, and doing it silently as part of fixing a typo would be the
	// wrong way to decide it.
	RekeyIdentifier(ctx context.Context, tx pgx.Tx, tenantID, subjectID uuid.UUID, kind, value string) error
}

// ErrIdentifierTaken reports that an identifier already belongs to another
// subject.
var ErrIdentifierTaken = errors.New("identifier already belongs to another subject")

// DocumentRef identifies a published consent document and carries the hash the
// client's rendering must match.
type DocumentRef struct {
	ID          uuid.UUID
	ContentHash []byte
	VersionNo   int
}

// DocumentBody is a consent document together with the text to display.
type DocumentBody struct {
	ID          uuid.UUID
	VersionNo   int
	BodyHTML    string
	ContentHash []byte
}

// DocumentProvider resolves consent documents for a tenant.
type DocumentProvider interface {
	ActiveDocument(ctx context.Context, tenantID uuid.UUID, kind string) (DocumentRef, error)
	// ActiveDocumentBody returns the same document with its text.
	//
	// The public form page needs the text itself, not just its hash: the proof a
	// submission carries is the digest of what was actually put on the page, and
	// a client that only ever saw a hash could echo it back without having shown
	// anybody anything.
	ActiveDocumentBody(ctx context.Context, tenantID uuid.UUID, kind string) (DocumentBody, error)
	PurposeIDs(ctx context.Context, tenantID uuid.UUID, codes []string) (map[string]uuid.UUID, error)
}

// AuditActor is who performed an audited action.
// Tagged because this struct is stored as JSON in audit.entries and read back
// by SQL. Without tags the keys were the Go identifiers -- Type, ID, IPPrefix --
// which is the only place in the codebase that spelling reaches a stored
// document, so an ad-hoc query written in the house snake_case finds nothing and
// reads as "the entry does not say who did it".
//
// The listing query already coalesces both spellings, which is why the audit
// screen never showed the problem; rows written before this change keep the old
// keys, so that coalesce has to stay.
type AuditActor struct {
	Type     string `json:"type"` // user | subject | system
	ID       string `json:"id"`
	IPPrefix string `json:"ip_prefix"`
}

// AuditEntry is one line of the tamper-evident trail.
type AuditEntry struct {
	TenantID uuid.UUID
	Actor    AuditActor
	Action   string
	Target   map[string]any
	Payload  map[string]any
}

// AuditWriter appends to the audit chain.
//
// It only appends. Nothing in the system can update or delete an entry -- the
// application's database role lacks the privilege, so a compromised process
// cannot quietly erase its own tracks.
type AuditWriter interface {
	Write(ctx context.Context, tx pgx.Tx, e AuditEntry) error
}
