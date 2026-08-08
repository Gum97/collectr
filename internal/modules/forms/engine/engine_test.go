package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/collectr/collectr/internal/modules/forms/domain"
)

type goldenFile struct {
	Schema domain.Schema `json:"schema"`
	Cases  []goldenCase  `json:"cases"`
}

type goldenCase struct {
	Name         string         `json:"name"`
	Answers      map[string]any `json:"answers"`
	WantPath     []string       `json:"want_path"`
	WantVisible  []string       `json:"want_visible"`
	WantRequired []string       `json:"want_required"`
}

// TestGolden runs the fixtures shared with any client-side port of the engine.
//
// The server is authoritative, but a browser that disagrees produces forms which
// look valid on screen and fail on submit. This file is what keeps the two
// honest, so it lives in testdata rather than in Go source.
func TestGolden(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile("testdata/golden.json")
	if err != nil {
		t.Fatalf("reading golden fixtures: %v", err)
	}
	var gf goldenFile
	if err := json.Unmarshal(raw, &gf); err != nil {
		t.Fatalf("parsing golden fixtures: %v", err)
	}
	if len(gf.Cases) == 0 {
		t.Fatal("golden fixtures contain no cases")
	}

	// The fixture schema must itself be publishable; a broken fixture would
	// make every case below meaningless.
	if res := domain.Validate(gf.Schema); !res.OK {
		t.Fatalf("golden schema does not validate: %+v", res.Issues)
	}

	for _, tc := range gf.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()

			got, err := Evaluate(gf.Schema, tc.Answers)
			if err != nil {
				t.Fatalf("Evaluate: %v", err)
			}
			assertIDs(t, "path", toStrings(got.Path), tc.WantPath)
			assertIDs(t, "visible", toStrings(got.Visible), tc.WantVisible)
			assertIDs(t, "required", toStrings(got.Required), tc.WantRequired)
		})
	}
}

// TestEvaluateIsDeterministic guards the property the whole design leans on: the
// same schema and answers must always produce the same visible set, or the
// server and the browser can reach different conclusions about what a respondent
// was shown.
func TestEvaluateIsDeterministic(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile("testdata/golden.json")
	if err != nil {
		t.Fatalf("reading golden fixtures: %v", err)
	}
	var gf goldenFile
	if err := json.Unmarshal(raw, &gf); err != nil {
		t.Fatalf("parsing golden fixtures: %v", err)
	}

	answers := map[string]any{"fld_name": "A", "fld_used": "opt_yes", "fld_rating": 1}
	first, err := Evaluate(gf.Schema, answers)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	// Enough repeats to shake out any dependence on Go's randomised map order.
	for range 200 {
		got, err := Evaluate(gf.Schema, answers)
		if err != nil {
			t.Fatalf("Evaluate: %v", err)
		}
		if !slices.Equal(got.Visible, first.Visible) || !slices.Equal(got.Path, first.Path) {
			t.Fatalf("evaluation is not deterministic:\nfirst: %v %v\nnow:   %v %v",
				first.Path, first.Visible, got.Path, got.Visible)
		}
	}
}

func TestEvaluateRejectsCycles(t *testing.T) {
	t.Parallel()

	// Publishing rejects this, but a schema written directly into the database
	// must still not be able to hang a request.
	s := domain.Schema{
		Pages: []domain.Page{
			{ID: "a", Fields: []domain.FieldID{"f"}},
			{ID: "b"},
		},
		Fields: map[domain.FieldID]domain.Field{"f": {Type: domain.TypeText}},
		Rules: []domain.Rule{
			{ID: "r1", OnPage: "a", When: domain.Condition{Op: domain.OpIsEmpty, Field: "f"},
				Then: []domain.Action{{Action: domain.ActionGoto, Target: "b"}}},
			{ID: "r2", OnPage: "b", When: domain.Condition{Op: domain.OpIsEmpty, Field: "f"},
				Then: []domain.Action{{Action: domain.ActionGoto, Target: "a"}}},
		},
	}
	if _, err := Evaluate(s, map[string]any{}); err == nil {
		t.Fatal("Evaluate on a cyclic schema = nil error, want a cycle error")
	}
}

func TestEvalCondition(t *testing.T) {
	t.Parallel()

	// Read from the shared fixture rather than declared here. These cases used to
	// be Go-only, which left the client port unexercised on exactly the operators
	// where two implementations drift -- in, contains, between, date ordering,
	// numeric-string coercion. A case added here now runs on both sides or fails
	// on one.
	var fixture struct {
		Cases []struct {
			Name      string           `json:"name"`
			Cond      domain.Condition `json:"cond"`
			Answers   map[string]any   `json:"answers"`
			Want      bool             `json:"want"`
			WantError bool             `json:"want_error"`
		} `json:"condition_cases"`
	}
	raw, err := os.ReadFile(filepath.Join("testdata", "golden.json"))
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatalf("parsing fixture: %v", err)
	}
	if len(fixture.Cases) < 15 {
		t.Fatalf("fixture holds %d condition cases, want the full operator set", len(fixture.Cases))
	}

	for _, tc := range fixture.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()

			got, err := evalCondition(tc.Cond, tc.Answers)
			if tc.WantError {
				if err == nil {
					t.Fatalf("evalCondition = %v, want error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("evalCondition: %v", err)
			}
			if got != tc.Want {
				t.Errorf("evalCondition = %v, want %v", got, tc.Want)
			}
		})
	}
}

// FuzzEvaluate checks that no set of answers can make the engine panic or hang.
// Answers come straight from the public internet, so "malformed input is the
// caller's problem" is not an available position.
func FuzzEvaluate(f *testing.F) {
	raw, err := os.ReadFile("testdata/golden.json")
	if err != nil {
		f.Fatalf("reading golden fixtures: %v", err)
	}
	var gf goldenFile
	if err := json.Unmarshal(raw, &gf); err != nil {
		f.Fatalf("parsing golden fixtures: %v", err)
	}

	f.Add(`{"fld_used":"opt_yes","fld_rating":1}`)
	f.Add(`{"fld_used":null}`)
	f.Add(`{"fld_rating":"not a number"}`)
	f.Add(`{"fld_tags":[1,2,{"a":"b"}]}`)
	f.Add(`{}`)

	f.Fuzz(func(t *testing.T, answersJSON string) {
		var answers map[string]any
		if err := json.Unmarshal([]byte(answersJSON), &answers); err != nil {
			t.Skip()
		}
		// Errors are fine; panics and hangs are not.
		res, err := Evaluate(gf.Schema, answers)
		if err != nil {
			return
		}
		for _, id := range res.Required {
			if !res.IsVisible(id) {
				t.Fatalf("field %q is required but not visible; validation would demand an answer to a question never shown", id)
			}
		}
	})
}

func toStrings[T ~string](in []T) []string {
	out := make([]string, len(in))
	for i, v := range in {
		out[i] = string(v)
	}
	return out
}

func assertIDs(t *testing.T, what string, got, want []string) {
	t.Helper()
	if want == nil {
		want = []string{}
	}
	if got == nil {
		got = []string{}
	}
	if !slices.Equal(got, want) {
		t.Errorf("%s = %v, want %v", what, got, want)
	}
}
