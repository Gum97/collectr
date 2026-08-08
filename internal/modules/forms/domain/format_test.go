package domain

import (
	"encoding/json"
	"os"
	"testing"
)

// The fixture is shared with web/src/shared/format.test.ts. Neither side owns a
// copy: the client checks formats so a respondent sees the problem while they
// are still typing, and the server rechecks because a submission is an HTTP
// request. Two validators that drift apart produce the worst outcome available
// here -- a value the page accepted and the server refused, after the person had
// already finished filling the form in.
type formatCase struct {
	Name  string `json:"name"`
	Field Field  `json:"field"`
	Value string `json:"value"`
	OK    bool   `json:"ok"`
}

func loadFormatCases(t *testing.T) []formatCase {
	t.Helper()
	raw, err := os.ReadFile("testdata/formats.json")
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	var file struct {
		Cases []formatCase `json:"cases"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("parsing fixture: %v", err)
	}
	return file.Cases
}

func TestCheckFormat(t *testing.T) {
	t.Parallel()
	cases := loadFormatCases(t)
	if len(cases) < 40 {
		t.Fatalf("fixture has shrunk to %d cases; it is shared with the client", len(cases))
	}

	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()
			msg := CheckFormat(tc.Field, tc.Value)
			if tc.OK && msg != "" {
				t.Fatalf("CheckFormat(%q) rejected a valid value: %s", tc.Value, msg)
			}
			if !tc.OK && msg == "" {
				t.Fatalf("CheckFormat(%q) accepted an invalid value", tc.Value)
			}
		})
	}
}

// A rejection the respondent cannot act on is a dead end. Every format has to
// say what to type, and none may quote its own pattern back.
func TestEveryFormatExplainsItself(t *testing.T) {
	t.Parallel()
	for _, name := range FormatNames() {
		spec, ok := formats[name]
		if !ok {
			t.Fatalf("FormatNames lists %q, which is not in formats", name)
		}
		if spec.Hint == "" || spec.Label == "" || spec.InputMode == "" {
			t.Errorf("format %q is missing a label, hint or input mode", name)
		}
	}
	if len(FormatNames()) != len(formats) {
		t.Errorf("FormatNames lists %d formats but %d are defined; the builder would hide one",
			len(FormatNames()), len(formats))
	}
}

func TestValidateFormatRejectsMisconfiguration(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		field Field
		want  bool // want at least one issue
	}{
		{"format on a choice field", Field{Type: TypeChoice, Format: FormatEmail}, true},
		{"unknown format", Field{Type: TypeText, Format: "iban"}, true},
		{"multiline holding a single value", Field{Type: TypeText, Format: FormatTaxCode, Multiline: true}, true},
		{"bounds the wrong way round", Field{Type: TypeText, Format: FormatInteger, Min: "10", Max: "1"}, true},
		{"non-numeric bound on a numeric format", Field{Type: TypeText, Format: FormatInteger, Min: "mười"}, true},
		{"bounds on a format that has none", Field{Type: TypeText, Format: FormatEmail, Min: "1"}, true},
		{"date bounds the wrong way round", Field{Type: TypeDate, Min: "2030-01-01", Max: "2020-01-01"}, true},
		{"date bound that is not a date", Field{Type: TypeDate, Min: "hôm qua"}, true},
		{"a valid numeric range", Field{Type: TypeText, Format: FormatInteger, Min: "0", Max: "100"}, false},
		{"a valid date range", Field{Type: TypeDate, Min: "2020-01-01", Max: "2030-01-01"}, false},
		{"no format and no bounds", Field{Type: TypeText}, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := validateFormat("f1", tc.field)
			if tc.want && len(got) == 0 {
				t.Fatal("expected an issue, got none")
			}
			if !tc.want && len(got) > 0 {
				t.Fatalf("expected no issue, got %v", got)
			}
		})
	}
}
