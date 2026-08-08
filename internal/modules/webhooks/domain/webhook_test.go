package domain

import (
	"errors"
	"math/rand/v2"
	"net"
	"strconv"
	"testing"
	"time"
)

// TestValidateURLRejectsInternalTargets is the SSRF guard. A webhook makes our
// server fetch a URL somebody else chose; without this it is a door into the
// private network the server sits in.
func TestValidateURLRejectsInternalTargets(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		url  string
	}{
		{name: "loopback", url: "https://127.0.0.1/hook"},
		{name: "loopback ipv6", url: "https://[::1]/hook"},
		{name: "private class A", url: "https://10.0.0.5/hook"},
		{name: "private class B", url: "https://172.16.4.4/hook"},
		{name: "private class C", url: "https://192.168.1.1/hook"},
		{name: "cloud metadata", url: "https://169.254.169.254/latest/meta-data/"},
		{name: "carrier grade nat", url: "https://100.64.0.1/hook"},
		{name: "ipv4-mapped ipv6", url: "https://[::ffff:127.0.0.1]/hook"},
		{name: "unique local ipv6", url: "https://[fc00::1]/hook"},

		{name: "plain http", url: "http://example.vn/hook"},
		{name: "file scheme", url: "file:///etc/passwd"},
		{name: "gopher scheme", url: "gopher://example.vn/"},
		{name: "credentials in the url", url: "https://user:pass@example.vn/hook"},
		{name: "no host", url: "https://"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := ValidateURL(tc.url, false); !errors.Is(err, ErrInvalidURL) {
				t.Errorf("ValidateURL(%q) = %v, want ErrInvalidURL", tc.url, err)
			}
		})
	}
}

func TestValidateURLAcceptsPublicHTTPS(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{
		"https://khachhang.vn/hooks/collectr",
		"https://api.example.com:8443/webhook?token=abc",
		"  https://example.vn/hook  ",
	} {
		if _, err := ValidateURL(raw, false); err != nil {
			t.Errorf("ValidateURL(%q) = %v, want nil", raw, err)
		}
	}
}

func TestIsPrivateIP(t *testing.T) {
	t.Parallel()

	tests := []struct {
		ip   string
		want bool
	}{
		{ip: "8.8.8.8", want: false},
		{ip: "203.113.1.1", want: false},
		{ip: "2001:4860:4860::8888", want: false},
		{ip: "127.0.0.1", want: true},
		{ip: "10.1.2.3", want: true},
		{ip: "169.254.169.254", want: true},
		{ip: "::1", want: true},
		{ip: "fe80::1", want: true},
	}

	for _, tc := range tests {
		t.Run(tc.ip, func(t *testing.T) {
			t.Parallel()
			if got := IsPrivateIP(net.ParseIP(tc.ip)); got != tc.want {
				t.Errorf("IsPrivateIP(%s) = %v, want %v", tc.ip, got, tc.want)
			}
		})
	}
	if !IsPrivateIP(nil) {
		t.Error("an unparseable address must be treated as private, not as public")
	}
}

func TestRetryable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		status int
		want   bool
	}{
		{status: 0, want: true},   // no response at all
		{status: 408, want: true}, // the receiver said "took too long"
		{status: 429, want: true}, // the receiver said "later"
		{status: 500, want: true},
		{status: 503, want: true},

		{status: 200},
		{status: 204},
		// A 4xx means the request is wrong. Repeating it changes nothing and
		// only hammers the receiver.
		{status: 400},
		{status: 401},
		{status: 403},
		{status: 404},
		{status: 422},
	}

	for _, tc := range tests {
		t.Run(strconv.Itoa(tc.status), func(t *testing.T) {
			t.Parallel()
			if got := Retryable(tc.status); got != tc.want {
				t.Errorf("Retryable(%d) = %v, want %v", tc.status, got, tc.want)
			}
		})
	}
}

// TestBackoffGrowsAndIsJittered pins both properties. Growth stops a dead
// receiver being hammered; jitter stops every queued delivery arriving at the
// same instant once it recovers.
func TestBackoffGrowsAndIsJittered(t *testing.T) {
	t.Parallel()

	r := rand.New(rand.NewPCG(1, 2))

	var previous time.Duration
	for attempt := 1; attempt <= MaxAttempts; attempt++ {
		d := Backoff(attempt, r)
		if d <= 0 {
			t.Fatalf("Backoff(%d) = %v, want positive", attempt, d)
		}
		if d > 12*time.Hour {
			t.Errorf("Backoff(%d) = %v, want it capped at 12h", attempt, d)
		}
		if attempt > 1 && attempt < 7 && d <= previous/2 {
			t.Errorf("Backoff(%d) = %v, did not grow beyond %v", attempt, d, previous)
		}
		previous = d
	}

	seen := make(map[time.Duration]struct{})
	for range 50 {
		seen[Backoff(3, r)] = struct{}{}
	}
	if len(seen) < 10 {
		t.Errorf("Backoff produced only %d distinct delays across 50 calls; "+
			"without jitter every queued delivery comes due at once", len(seen))
	}
}

func TestValidEvent(t *testing.T) {
	t.Parallel()

	if !ValidEvent(EventSubmissionCreated) || !ValidEvent(EventConsentWithdrawn) {
		t.Error("a known event was rejected")
	}
	if ValidEvent("submission.deleted") || ValidEvent("") {
		t.Error("an unknown event was accepted")
	}
}
