package domain

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Errors returned by domain management.
var (
	// ErrHostTaken means the hostname is already routed to a tenant. Hostnames
	// are unique across the whole deployment, not per tenant.
	ErrHostTaken = errors.New("host already registered")
	// ErrDomainInUse means links still point at the host.
	ErrDomainInUse = errors.New("domain still has links")
	// ErrInvalidHost wraps every rejection from ValidateHost, so a caller can
	// tell "you typed this wrong" from "the database is down" with one check
	// while still showing the specific reason.
	ErrInvalidHost = errors.New("invalid host")
	// ErrNoDomain means the tenant has no hostname to issue codes on, so no link
	// can be created. Setup registers one, so reaching this means every domain
	// was removed by hand.
	ErrNoDomain = errors.New("no link domain configured")
)

// Domain is a hostname a tenant may issue short codes on.
type Domain struct {
	ID        uuid.UUID
	Host      string
	IsDefault bool
	LinkCount int
	CreatedAt time.Time
}

// ValidateHost normalises and checks a hostname.
//
// The value has to match what arrives in the Host header, because that is what
// links.resolve() looks a code up by. So it is stored lowercase, without a
// scheme or path, and with the port kept -- a development deployment really is
// reached at localhost:8080, and dropping the port would make its links
// unresolvable.
func ValidateHost(raw string) (string, error) {
	h := strings.ToLower(strings.TrimSpace(raw))
	h = strings.TrimPrefix(strings.TrimPrefix(h, "https://"), "http://")
	h = strings.TrimSuffix(h, "/")

	if h == "" {
		return "", fmt.Errorf("%w: %s", ErrInvalidHost, "host must not be empty")
	}
	if strings.ContainsAny(h, "/?#@ \t") {
		return "", fmt.Errorf("%w: %s", ErrInvalidHost, "host must be a bare hostname, without a scheme or path")
	}
	if len(h) > 253 {
		return "", fmt.Errorf("%w: %s", ErrInvalidHost, "host is too long")
	}

	name := h
	if host, _, err := net.SplitHostPort(h); err == nil {
		name = host
	}
	// An address rather than a name would work for the redirect but breaks the
	// certificate the operator has to obtain for it, so it is refused at the
	// point where the mistake is still cheap.
	if net.ParseIP(name) != nil {
		return "", fmt.Errorf("%w: %s", ErrInvalidHost, "host must be a domain name, not an IP address")
	}
	for _, label := range strings.Split(name, ".") {
		if label == "" {
			return "", fmt.Errorf("%w: %s", ErrInvalidHost, "host has an empty label")
		}
		for _, r := range label {
			if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' {
				continue
			}
			return "", fmt.Errorf("%w: %s", ErrInvalidHost, "host may only contain letters, digits, hyphens and dots")
		}
	}
	return h, nil
}
