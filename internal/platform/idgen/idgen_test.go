package idgen

import (
	"strings"
	"testing"
)

func TestCode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		length  int
		wantErr bool
	}{
		{name: "default length", length: DefaultCodeLength},
		{name: "single character", length: 1},
		{name: "long code", length: 64},
		{name: "zero is rejected", length: 0, wantErr: true},
		{name: "negative is rejected", length: -1, wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := Code(tc.length)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Code(%d) = %q, want error", tc.length, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("Code(%d): %v", tc.length, err)
			}
			if len(got) != tc.length {
				t.Errorf("len(Code(%d)) = %d, want %d", tc.length, len(got), tc.length)
			}
			for _, r := range got {
				if !strings.ContainsRune(CodeAlphabet, r) {
					t.Errorf("Code produced %q containing %q, which is outside the alphabet", got, r)
				}
			}
		})
	}
}

// TestCodeUniqueness is a smoke test, not a statistical proof: at 62^7 a
// collision inside 10k draws would signal a broken generator, not bad luck.
func TestCodeUniqueness(t *testing.T) {
	t.Parallel()

	const draws = 10_000
	seen := make(map[string]struct{}, draws)
	for range draws {
		code, err := Code(DefaultCodeLength)
		if err != nil {
			t.Fatalf("Code: %v", err)
		}
		if _, dup := seen[code]; dup {
			t.Fatalf("duplicate code %q within %d draws", code, draws)
		}
		seen[code] = struct{}{}
	}
}

// TestCodeDistribution guards the rejection sampling. Naive modulo over 256 would
// over-represent the first 8 letters of the alphabet by roughly 3%.
func TestCodeDistribution(t *testing.T) {
	t.Parallel()

	const draws = 20_000
	counts := make(map[rune]int, len(CodeAlphabet))
	for range draws {
		code, err := Code(1)
		if err != nil {
			t.Fatalf("Code: %v", err)
		}
		counts[rune(code[0])]++
	}

	expected := float64(draws) / float64(len(CodeAlphabet))
	for _, r := range CodeAlphabet {
		got := float64(counts[r])
		if got < expected*0.7 || got > expected*1.3 {
			t.Errorf("character %q appeared %.0f times, want roughly %.0f", r, got, expected)
		}
	}
}

func TestValidateAlias(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		alias   string
		wantErr bool
	}{
		{name: "simple", alias: "tet-2026"},
		{name: "underscores", alias: "khao_sat_01"},
		{name: "minimum length", alias: "abc"},
		{name: "too short", alias: "ab", wantErr: true},
		{name: "too long", alias: strings.Repeat("a", 65), wantErr: true},
		{name: "reserved route", alias: "api", wantErr: true},
		{name: "reserved route ignores case", alias: "ADMIN", wantErr: true},
		{name: "slash", alias: "a/b", wantErr: true},
		{name: "space", alias: "a b", wantErr: true},
		{name: "unicode", alias: "khảo-sát", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := ValidateAlias(tc.alias)
			if tc.wantErr && err == nil {
				t.Errorf("ValidateAlias(%q) = nil, want error", tc.alias)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("ValidateAlias(%q) = %v, want nil", tc.alias, err)
			}
		})
	}
}
