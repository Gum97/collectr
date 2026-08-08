package domain

import (
	"errors"
	"testing"
	"time"
)

func TestResolutionCheck(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	past := now.Add(-time.Hour)
	future := now.Add(time.Hour)

	tests := []struct {
		name string
		res  Resolution
		want error
	}{
		{name: "active without expiry", res: Resolution{Status: StatusActive}},
		{name: "active before expiry", res: Resolution{Status: StatusActive, ExpiresAt: &future}},
		{name: "active after expiry", res: Resolution{Status: StatusActive, ExpiresAt: &past}, want: ErrGone},
		{name: "disabled", res: Resolution{Status: StatusDisabled}, want: ErrGone},
		{name: "deleted", res: Resolution{Status: StatusDeleted}, want: ErrGone},
		{name: "legal hold", res: Resolution{Status: StatusLegalHold}, want: ErrLegalHold},
		{name: "unknown status fails closed", res: Resolution{Status: "something-new"}, want: ErrGone},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := tc.res.Check(now)
			if !errors.Is(err, tc.want) {
				t.Errorf("Check() = %v, want %v", err, tc.want)
			}
		})
	}
}

// TestCheckDistinguishesGoneFromLegalHold pins a behaviour that is easy to
// collapse by accident: an expired campaign and a link withheld on legal grounds
// are different answers to the visitor and different signals in the logs.
func TestCheckDistinguishesGoneFromLegalHold(t *testing.T) {
	t.Parallel()

	now := time.Now()
	if err := (Resolution{Status: StatusLegalHold}).Check(now); errors.Is(err, ErrGone) {
		t.Error("legal hold reported as ErrGone; the two must stay distinguishable")
	}
}

func TestValidateTarget(t *testing.T) {
	t.Parallel()

	selfHosts := []string{"links.acme.vn", "collectr.local"}

	tests := []struct {
		name    string
		raw     string
		wantErr bool
	}{
		{name: "https", raw: "https://acme.vn/landing"},
		{name: "http", raw: "http://acme.vn/landing"},
		{name: "with query", raw: "https://acme.vn/l?utm_source=qr"},
		{name: "surrounding whitespace", raw: "  https://acme.vn/  "},
		{name: "javascript scheme", raw: "javascript:alert(1)", wantErr: true},
		{name: "data scheme", raw: "data:text/html,<script>", wantErr: true},
		{name: "file scheme", raw: "file:///etc/passwd", wantErr: true},
		{name: "no host", raw: "https://", wantErr: true},
		{name: "relative", raw: "/landing", wantErr: true},
		{name: "self host causes a loop", raw: "https://links.acme.vn/r/abc", wantErr: true},
		{name: "self host ignores case", raw: "https://LINKS.ACME.VN/r/abc", wantErr: true},
		{name: "other self host", raw: "https://collectr.local/r/abc", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := ValidateTarget(tc.raw, selfHosts)
			if tc.wantErr {
				if err == nil {
					t.Errorf("ValidateTarget(%q) = %q, want error", tc.raw, got)
				}
				return
			}
			if err != nil {
				t.Errorf("ValidateTarget(%q) = %v, want nil", tc.raw, err)
			}
		})
	}
}
