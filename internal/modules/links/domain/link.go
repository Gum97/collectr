// Package domain holds the link entity and the rules that decide what a visitor
// gets back. It has no dependencies on storage or transport so the rules stay
// testable in isolation.
package domain

import (
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Link statuses.
const (
	StatusActive    = "active"
	StatusDisabled  = "disabled"
	StatusDeleted   = "deleted"
	StatusLegalHold = "legal_hold"
)

// Errors returned by link creation and resolution.
var (
	ErrNotFound      = errors.New("link not found")
	ErrGone          = errors.New("link is no longer available")
	ErrLegalHold     = errors.New("link withheld for legal reasons")
	ErrAliasTaken    = errors.New("alias already in use")
	ErrInvalidTarget = errors.New("invalid target url")
)

// Link is a short code pointing at either an external URL or an internal form.
type Link struct {
	ID        uuid.UUID
	TenantID  uuid.UUID
	ProjectID uuid.UUID
	DomainID  uuid.UUID
	// Host is the domain this code lives on. Carried on the link rather than
	// taken from the request that asks about it: an operator running a separate
	// shortener domain reads this list from the admin host, and building the
	// short URL from the request would print the admin host onto a QR code that
	// then goes on a poster.
	Host      string
	Code      string
	TargetURL string
	FormID    *uuid.UUID
	ExpiresAt *time.Time
	Status    string
	CreatedBy uuid.UUID
	CreatedAt time.Time
}

// Resolution is the minimal projection the redirect path needs. Keeping it
// narrow keeps the cache entry small and keeps personal data out of Redis.
type Resolution struct {
	LinkID    uuid.UUID  `json:"l"`
	TenantID  uuid.UUID  `json:"t"`
	ProjectID uuid.UUID  `json:"p"`
	TargetURL string     `json:"u,omitempty"`
	FormID    *uuid.UUID `json:"f,omitempty"`
	// FormPublicID is the identifier the public form endpoint accepts. Carried
	// alongside FormID because the redirect builds a visitor-facing URL and the
	// primary key is not what that URL is keyed by.
	FormPublicID string     `json:"fp,omitempty"`
	Status       string     `json:"s"`
	ExpiresAt    *time.Time `json:"e,omitempty"`
}

// Check reports whether the link may be followed at time now.
//
// The distinction between "never existed" and "existed but is over" is kept on
// purpose: a visitor who scans a printed QR code after a campaign ends deserves
// 410 rather than 404, and a link withheld on legal grounds is neither.
func (r Resolution) Check(now time.Time) error {
	switch r.Status {
	case StatusLegalHold:
		return ErrLegalHold
	case StatusDisabled, StatusDeleted:
		return ErrGone
	case StatusActive:
	default:
		return ErrGone
	}
	if r.ExpiresAt != nil && now.After(*r.ExpiresAt) {
		return ErrGone
	}
	return nil
}

// ValidateTarget checks a user-supplied destination URL.
//
// selfHosts are the hosts this deployment serves; pointing a short link at one
// of them invites redirect loops, and a loop is both a self-inflicted outage and
// a way to amplify traffic at someone else's expense.
func ValidateTarget(raw string, selfHosts []string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", ErrInvalidTarget
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", ErrInvalidTarget
	}
	if u.Host == "" {
		return "", ErrInvalidTarget
	}
	host := strings.ToLower(u.Hostname())
	for _, self := range selfHosts {
		if host == strings.ToLower(self) {
			return "", ErrInvalidTarget
		}
	}
	return u.String(), nil
}
