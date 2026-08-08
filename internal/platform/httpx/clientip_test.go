package httpx

import (
	"net/http"
	"testing"
)

// TestClientIPReadsForwardedForFromTheRight pins a demonstrated bypass: the old
// code took the leftmost entry, which is whatever the caller sent. A proxy
// appends the address it saw, so the caller's own value always came first and
// always won -- defeating the per-address rate limit and forging the address
// recorded as evidence of a consent decision.
func TestClientIPReadsForwardedForFromTheRight(t *testing.T) {
	tests := []struct {
		name  string
		hops  int
		xff   string
		remot string
		want  string
	}{
		{
			// The header arrives from a public address, so nothing in front of us
			// wrote it: it is the caller describing themselves.
			name: "header sent straight from the internet is ignored",
			hops: 1, xff: "198.51.100.7", remot: "203.0.113.9:1234",
			want: "203.0.113.9",
		},
		{
			name: "caller forges a header behind one proxy",
			hops: 1,
			// 198.51.100.7 is what the attacker sent; 203.0.113.9 is what the
			// proxy saw and appended.
			xff: "198.51.100.7, 203.0.113.9", remot: "10.0.0.2:1234",
			want: "203.0.113.9",
		},
		{
			name: "honest caller behind one proxy",
			hops: 1, xff: "203.0.113.9", remot: "10.0.0.2:1234",
			want: "203.0.113.9",
		},
		{
			name: "two trusted hops",
			hops: 2, xff: "198.51.100.7, 203.0.113.9, 10.0.0.9", remot: "10.0.0.2:1234",
			want: "203.0.113.9",
		},
		{
			name: "no proxy configured: the header is ignored even from a private peer",
			hops: 0, xff: "198.51.100.7", remot: "10.0.0.2:1234",
			want: "10.0.0.2",
		},
		{
			name: "header shorter than the configured hops falls back to the socket",
			hops: 2, xff: "198.51.100.7", remot: "10.0.0.2:1234",
			want: "10.0.0.2",
		},
	}

	original := trustedProxyHops
	t.Cleanup(func() { trustedProxyHops = original })

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			SetTrustedProxyHops(tc.hops)
			r := &http.Request{Header: http.Header{}, RemoteAddr: tc.remot}
			if tc.xff != "" {
				r.Header.Set("X-Forwarded-For", tc.xff)
			}
			if got := ClientIP(r); got.String() != tc.want {
				t.Errorf("ClientIP() = %s, want %s", got, tc.want)
			}
		})
	}
}
