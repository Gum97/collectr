package domain

import (
	"errors"
	"testing"
)

func TestValidateHost(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{name: "plain", input: "rutgon.example.com", want: "rutgon.example.com"},
		{name: "uppercase folds", input: "RutGon.Example.COM", want: "rutgon.example.com"},
		{name: "trims space", input: "  rutgon.vn  ", want: "rutgon.vn"},

		// Operators paste an address bar, so the scheme and trailing slash are
		// stripped rather than rejected.
		{name: "strips scheme", input: "https://rutgon.vn", want: "rutgon.vn"},
		{name: "strips scheme and slash", input: "http://rutgon.vn/", want: "rutgon.vn"},

		// The port is part of what arrives in the Host header, so a development
		// deployment on localhost:8080 must keep it or its links stop resolving.
		{name: "keeps port", input: "localhost:8080", want: "localhost:8080"},

		{name: "rejects path", input: "rutgon.vn/r", wantErr: true},
		{name: "rejects query", input: "rutgon.vn?a=1", wantErr: true},
		{name: "rejects userinfo", input: "user@rutgon.vn", wantErr: true},
		{name: "rejects empty", input: "", wantErr: true},
		{name: "rejects space inside", input: "rut gon.vn", wantErr: true},
		{name: "rejects empty label", input: "rutgon..vn", wantErr: true},
		{name: "rejects underscore", input: "rut_gon.vn", wantErr: true},
		// An address resolves fine but no certificate authority will issue for
		// it, so it fails at deploy time instead of here.
		{name: "rejects ipv4", input: "203.0.113.10", wantErr: true},
		{name: "rejects ipv4 with port", input: "203.0.113.10:8080", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := ValidateHost(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ValidateHost(%q) = %q, want error", tc.input, got)
				}
				// Every rejection must be recognisable by one sentinel, or the
				// API cannot tell a typo from an outage.
				if !errors.Is(err, ErrInvalidHost) {
					t.Errorf("ValidateHost(%q) error = %v, want ErrInvalidHost", tc.input, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ValidateHost(%q): %v", tc.input, err)
			}
			if got != tc.want {
				t.Errorf("ValidateHost(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// TestValidateHostIsIdempotent: a stored host fed back through validation must
// not change, or editing a domain would silently rewrite what links resolve by.
func TestValidateHostIsIdempotent(t *testing.T) {
	t.Parallel()

	for _, in := range []string{"rutgon.example.com", "https://Rutgon.VN/", "localhost:8080"} {
		once, err := ValidateHost(in)
		if err != nil {
			t.Fatalf("ValidateHost(%q): %v", in, err)
		}
		twice, err := ValidateHost(once)
		if err != nil {
			t.Fatalf("ValidateHost(%q): %v", once, err)
		}
		if twice != once {
			t.Errorf("ValidateHost(%q) = %q, but ValidateHost(%q) = %q", in, once, once, twice)
		}
	}
}
