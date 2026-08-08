// Package domain holds webhook delivery rules: what may be called, how a
// delivery is signed, and when to give up.
package domain

import (
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"net"
	"net/url"
	"slices"
	"strings"
	"time"
)

// Event types a webhook can subscribe to.
const (
	EventLinkCreated       = "link.created"
	EventFormPublished     = "form.published"
	EventSubmissionCreated = "submission.created"
	EventSubmissionUpdated = "submission.updated"
	// EventConsentWithdrawn and EventDSRReceived matter most: they let a CRM on
	// the other end stop processing immediately rather than at the next sync.
	EventConsentWithdrawn = "consent.withdrawn"
	EventDSRReceived      = "dsr.received"
	EventExportReady      = "export.ready"
)

// Delivery statuses.
const (
	StatusPending   = "pending"
	StatusDelivered = "delivered"
	StatusFailed    = "failed"
	StatusDead      = "dead"
)

// MaxAttempts is how many times a delivery is tried before it is abandoned.
const MaxAttempts = 8

// DisableAfterFailures is how many consecutive failures disable an endpoint.
//
// A receiver that has been dead for days should stop consuming the queue and
// filling the delivery log; whoever owns it can re-enable once it is fixed.
const DisableAfterFailures = 20

// Errors returned when a webhook is refused.
var (
	ErrNotFound     = errors.New("webhook not found")
	ErrInvalidURL   = errors.New("webhook url is not acceptable")
	ErrUnknownEvent = errors.New("unknown event type")
)

// ValidEvent reports whether e is a subscribable event.
func ValidEvent(e string) bool {
	return slices.Contains([]string{
		EventLinkCreated, EventFormPublished, EventSubmissionCreated,
		EventSubmissionUpdated, EventConsentWithdrawn, EventDSRReceived, EventExportReady,
	}, e)
}

// privateBlocks are the ranges a webhook may never reach.
//
// A webhook is a feature that makes *our* server fetch a URL that *someone else*
// chose. Without this list it is a way into the private network the server sits
// in: the metadata service, the database, an internal admin panel.
var privateBlocks = func() []*net.IPNet {
	cidrs := []string{
		"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8",
		"169.254.0.0/16", // link-local, and the cloud metadata endpoint
		"172.16.0.0/12", "192.0.0.0/24", "192.168.0.0/16", "198.18.0.0/15",
		"224.0.0.0/4", "240.0.0.0/4",
		"::1/128", "fc00::/7", "fe80::/10",
		// Deliberately not ::ffff:0:0/96 here: net.ParseIP returns the 16-byte
		// IPv4-mapped form for every IPv4 address, so that block would match
		// literally all of them and refuse every legitimate webhook. Mapped
		// addresses are unwrapped in IsPrivateIP instead.
	}
	out := make([]*net.IPNet, 0, len(cidrs))
	for _, c := range cidrs {
		if _, block, err := net.ParseCIDR(c); err == nil {
			out = append(out, block)
		}
	}
	return out
}()

// IsPrivateIP reports whether an address is inside the blocked ranges.
//
// An unparseable address counts as private: failing closed is the only safe
// default when the question is "may the server call this".
func IsPrivateIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	// ::ffff:127.0.0.1 is 127.0.0.1 wearing a hat. Unwrap before comparing, or a
	// mapped loopback address walks straight through the IPv4 blocks.
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	// Ask the address what it is before consulting a list of what it might be.
	//
	// "::" is the case that got through: it is the IPv6 unspecified address, no
	// CIDR in the list covers it, and connecting to it lands on loopback. Its
	// IPv4 twin 0.0.0.0 was caught only because 0.0.0.0/8 happened to be listed.
	// A list of ranges will always be one notation short of the next writer of a
	// bypass; these predicates are properties of the address itself.
	if ip.IsUnspecified() || ip.IsLoopback() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() {
		return true
	}

	// Addresses that carry an IPv4 address inside them, unwrapped and re-checked.
	// 6to4 and NAT64 both embed one, and both reach it when a route exists.
	if v4 := embeddedV4(ip); v4 != nil {
		return IsPrivateIP(v4)
	}

	for _, block := range privateBlocks {
		if block.Contains(ip) {
			return true
		}
	}
	return false
}

// ValidateURL checks a webhook destination before it is stored.
//
// This is the first of two checks. It cannot be the only one: DNS can be changed
// after the URL is saved, so the address is resolved and checked again at the
// moment of every delivery.
func ValidateURL(raw string, allowHTTP bool) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, ErrInvalidURL
	}
	switch {
	case u.Scheme == "https":
	case u.Scheme == "http" && allowHTTP:
	default:
		return nil, fmt.Errorf("%w: must be https", ErrInvalidURL)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("%w: missing host", ErrInvalidURL)
	}
	if u.User != nil {
		return nil, fmt.Errorf("%w: credentials in the url are not accepted", ErrInvalidURL)
	}

	// A literal private address is refused outright; a hostname is resolved at
	// delivery time.
	if ip := net.ParseIP(u.Hostname()); ip != nil && IsPrivateIP(ip) {
		return nil, fmt.Errorf("%w: private addresses are not reachable", ErrInvalidURL)
	}
	return u, nil
}

// Backoff returns how long to wait before attempt n (1-based).
//
// Exponential, with jitter. The jitter is not decoration: after a receiver has
// been down for an hour, thousands of deliveries come due at the same instant,
// and without spreading them we would knock the receiver over again the moment
// it recovers.
func Backoff(attempt int, r *rand.Rand) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	base := 10 * time.Second
	backoff := time.Duration(math.Pow(3, float64(attempt-1))) * base
	if backoff > 12*time.Hour {
		backoff = 12 * time.Hour
	}

	// Full jitter: anywhere in [backoff/2, backoff].
	half := backoff / 2
	return half + time.Duration(r.Int64N(int64(half)+1))
}

// Retryable reports whether an HTTP status is worth trying again.
//
// A 4xx means the request itself is wrong; repeating it changes nothing and only
// hammers the receiver. The exceptions are the two that explicitly mean "later".
func Retryable(status int) bool {
	switch {
	case status == 0: // transport failure: no response at all
		return true
	case status == 408, status == 429:
		return true
	case status >= 500:
		return true
	default:
		return false
	}
}

// embeddedV4 extracts the IPv4 address carried inside a translation format.
//
// 2002::/16 (6to4) holds it in bytes 2-5; 64:ff9b::/96 (NAT64) and the
// deprecated IPv4-compatible form hold it in the last four. Each is a way to
// write an IPv4 destination that a plain CIDR comparison against IPv6 ranges
// does not see.
func embeddedV4(ip net.IP) net.IP {
	v6 := ip.To16()
	if v6 == nil || ip.To4() != nil {
		return nil
	}
	switch {
	case v6[0] == 0x20 && v6[1] == 0x02: // 2002::/16
		return net.IPv4(v6[2], v6[3], v6[4], v6[5])
	case nat64Prefix.Contains(ip): // 64:ff9b::/96
		return net.IPv4(v6[12], v6[13], v6[14], v6[15])
	case isIPv4Compatible(v6):
		return net.IPv4(v6[12], v6[13], v6[14], v6[15])
	}
	return nil
}

var nat64Prefix = func() *net.IPNet {
	_, n, err := net.ParseCIDR("64:ff9b::/96")
	if err != nil {
		panic("webhooks: bad nat64 prefix: " + err.Error())
	}
	return n
}()

// isIPv4Compatible matches ::a.b.c.d, the deprecated form that parses as a
// distinct address from ::ffff:a.b.c.d and so misses the mapped-address unwrap
// above.
func isIPv4Compatible(v6 net.IP) bool {
	for _, b := range v6[:12] {
		if b != 0 {
			return false
		}
	}
	// :: and ::1 are handled by the predicates above; anything else here is a
	// v4 address written the old way.
	return v6[12] != 0 || v6[13] != 0 || v6[14] != 0 || v6[15] > 1
}
