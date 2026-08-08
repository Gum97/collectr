package domain

import (
	"slices"
	"strings"
	"testing"
)

// base returns a small schema that validates cleanly, for tests to break in one
// specific way each.
func base() Schema {
	return Schema{
		V: 1,
		Pages: []Page{
			{ID: "p1", Fields: []FieldID{"f_name", "f_used"}},
			{ID: "p2", Fields: []FieldID{"f_note"}},
		},
		Fields: map[FieldID]Field{
			// Identifier, because the schema below declares consent purposes and a
			// consent record has to attach to somebody. A fixture without one
			// describes a form that publishes and then refuses every submission.
			//
			// The kind is "email" rather than "name": the subject table accepts only
			// a reachable identifier, and this fixture used to say "name" -- which
			// described a form that published cleanly and then returned 500 to the
			// first respondent, at the insert.
			"f_name": {Type: TypeText, Label: "Email", Required: true, PII: "email", Identifier: true},
			"f_used": {Type: TypeChoice, Label: "Used?", Options: []Option{
				{ID: "o_yes", Label: "Yes"}, {ID: "o_no", Label: "No"},
			}},
			"f_note": {Type: TypeText, Label: "Note"},
		},
		Consent: ConsentBlock{Purposes: []Purpose{{Code: "service", Required: true}}},
	}
}

func hasIssue(res ValidationResult, code string) bool {
	return slices.ContainsFunc(res.Issues, func(i Issue) bool { return i.Code == code })
}

func TestValidateAcceptsHealthySchema(t *testing.T) {
	t.Parallel()

	if res := Validate(base()); !res.OK {
		t.Fatalf("Validate(base) rejected a healthy schema: %+v", res.Issues)
	}
}

func TestValidateRejects(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		mutate   func(*Schema)
		wantCode string
	}{
		{
			name:     "empty schema",
			mutate:   func(s *Schema) { s.Pages = nil },
			wantCode: IssueEmptySchema,
		},
		{
			name:     "page lists a field that does not exist",
			mutate:   func(s *Schema) { s.Pages[0].Fields = append(s.Pages[0].Fields, "f_ghost") },
			wantCode: IssueDanglingRef,
		},
		{
			name:     "page continues to a page that does not exist",
			mutate:   func(s *Schema) { s.Pages[0].Next = "p_ghost" },
			wantCode: IssueDanglingRef,
		},
		{
			name: "rule jumps to a page that does not exist",
			mutate: func(s *Schema) {
				s.Rules = append(s.Rules, Rule{ID: "r1", OnPage: "p1",
					When: Condition{Op: OpEq, Field: "f_used", Value: "o_yes"},
					Then: []Action{{Action: ActionGoto, Target: "p_ghost"}}})
			},
			wantCode: IssueDanglingRef,
		},
		{
			// The most common way an edit breaks a live form: someone deletes an
			// option that a branch still tests against.
			name: "rule tests an option that was deleted",
			mutate: func(s *Schema) {
				f := s.Fields["f_used"]
				f.Options = f.Options[:1] // drop o_no
				s.Fields["f_used"] = f
				s.Rules = append(s.Rules, Rule{ID: "r1", OnPage: "p1",
					When: Condition{Op: OpEq, Field: "f_used", Value: "o_no"}})
			},
			wantCode: IssueDanglingRef,
		},
		{
			name: "navigation can loop",
			mutate: func(s *Schema) {
				s.Pages[1].Next = "p1"
			},
			wantCode: IssueCycle,
		},
		{
			// The check that matters most: a required question nobody can ever be
			// shown makes the form reject every submission, silently.
			name: "required field is hidden and never shown",
			mutate: func(s *Schema) {
				f := s.Fields["f_note"]
				f.Required, f.Hidden = true, true
				s.Fields["f_note"] = f
			},
			wantCode: IssueRequiredHidden,
		},
		{
			name: "required field is hidden unconditionally by a rule",
			mutate: func(s *Schema) {
				f := s.Fields["f_note"]
				f.Required = true
				s.Fields["f_note"] = f
				s.Rules = append(s.Rules, Rule{ID: "r1", OnPage: "p2",
					When: Condition{Op: OpIsNotEmpty, Field: "f_name"},
					Then: []Action{{Action: ActionHide, Target: "f_note"}},
					Else: []Action{{Action: ActionHide, Target: "f_note"}}})
			},
			wantCode: IssueRequiredHidden,
		},
		{
			name:     "personal data without a declared purpose",
			mutate:   func(s *Schema) { s.Consent.Purposes = nil },
			wantCode: IssueConsentMissing,
		},
		{
			name: "sensitive data without an explicit notice",
			mutate: func(s *Schema) {
				f := s.Fields["f_note"]
				f.Sensitive = true
				s.Fields["f_note"] = f
			},
			wantCode: IssueSensitiveNotice,
		},
		{
			name: "choice field without options",
			mutate: func(s *Schema) {
				f := s.Fields["f_used"]
				f.Options = nil
				s.Fields["f_used"] = f
			},
			wantCode: IssueBadFieldConfig,
		},
		{
			name: "rating with an impossible scale",
			mutate: func(s *Schema) {
				s.Fields["f_note"] = Field{Type: TypeRating, Label: "R", Scale: 99}
			},
			wantCode: IssueBadFieldConfig,
		},
		{
			name: "unknown operator",
			mutate: func(s *Schema) {
				s.Rules = append(s.Rules, Rule{ID: "r1", OnPage: "p1",
					When: Condition{Op: "regex", Field: "f_name", Value: ".*"}})
			},
			wantCode: IssueBadFieldConfig,
		},
		{
			name: "same field on two pages",
			mutate: func(s *Schema) {
				s.Pages[1].Fields = append(s.Pages[1].Fields, "f_name")
			},
			wantCode: IssueDuplicateID,
		},
		{
			name: "file upload with a negative size limit",
			mutate: func(s *Schema) {
				s.Fields["f_note"] = Field{Type: TypeFile, Label: "CV", MaxMB: -1}
			},
			wantCode: IssueBadFieldConfig,
		},
		{
			name: "identifier field without a declared data kind",
			mutate: func(s *Schema) {
				f := s.Fields["f_note"]
				f.Identifier = true
				s.Fields["f_note"] = f
			},
			wantCode: IssueBadFieldConfig,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			s := base()
			tc.mutate(&s)
			res := Validate(s)
			if res.OK {
				t.Fatalf("Validate accepted a schema that should be rejected (%s)", tc.wantCode)
			}
			if !hasIssue(res, tc.wantCode) {
				t.Errorf("issues %+v do not include %s", res.Issues, tc.wantCode)
			}
		})
	}
}

func TestValidateWarnsWithoutBlocking(t *testing.T) {
	t.Parallel()

	s := base()
	s.Fields["f_loose"] = Field{Type: TypeText, Label: "Not placed anywhere"}

	res := Validate(s)
	if !res.OK {
		t.Errorf("an orphan field should warn, not block publishing: %+v", res.Issues)
	}
	if !hasIssue(res, IssueOrphanField) {
		t.Errorf("issues %+v do not include %s", res.Issues, IssueOrphanField)
	}
}

func TestDiffClassification(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		mutate    func(*Schema)
		wantKind  string
		wantClass string
	}{
		{
			name: "relabelling is harmless because answers are stored by id",
			mutate: func(s *Schema) {
				f := s.Fields["f_used"]
				f.Label = "Have you tried it?"
				s.Fields["f_used"] = f
			},
			wantKind: "label_changed", wantClass: ChangeNonBreaking,
		},
		{
			name: "adding an optional field is harmless",
			mutate: func(s *Schema) {
				s.Fields["f_new"] = Field{Type: TypeText, Label: "New"}
				s.Pages[1].Fields = append(s.Pages[1].Fields, "f_new")
			},
			wantKind: "field_added", wantClass: ChangeNonBreaking,
		},
		{
			name: "adding a required field is breaking",
			mutate: func(s *Schema) {
				s.Fields["f_new"] = Field{Type: TypeText, Label: "New", Required: true}
				s.Pages[1].Fields = append(s.Pages[1].Fields, "f_new")
			},
			wantKind: "field_added", wantClass: ChangeBreaking,
		},
		{
			name:     "removing a field keeps the answers and retires the column",
			mutate:   func(s *Schema) { delete(s.Fields, "f_note"); s.Pages[1].Fields = nil },
			wantKind: "field_removed", wantClass: ChangeBreaking,
		},
		{
			name: "changing a field's type splits the column rather than coercing",
			mutate: func(s *Schema) {
				s.Fields["f_note"] = Field{Type: TypeRating, Label: "Note", Scale: 5}
			},
			wantKind: "field_type_changed", wantClass: ChangeBreaking,
		},
		{
			name: "relabelling an option is invisible to stored data",
			mutate: func(s *Schema) {
				f := s.Fields["f_used"]
				f.Options = []Option{{ID: "o_yes", Label: "Yep"}, {ID: "o_no", Label: "Nope"}}
				s.Fields["f_used"] = f
			},
			wantKind: "", // no change reported at all
		},
		{
			name: "removing an option is breaking",
			mutate: func(s *Schema) {
				f := s.Fields["f_used"]
				f.Options = f.Options[:1]
				s.Fields["f_used"] = f
			},
			wantKind: "option_removed", wantClass: ChangeBreaking,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			old := base()
			draft := base()
			tc.mutate(&draft)
			res := Diff(old, draft)

			if tc.wantKind == "" {
				if len(res.Changes) != 0 {
					t.Fatalf("expected no changes, got %+v", res.Changes)
				}
				return
			}
			idx := slices.IndexFunc(res.Changes, func(c Change) bool { return c.Kind == tc.wantKind })
			if idx < 0 {
				t.Fatalf("changes %+v do not include kind %q", res.Changes, tc.wantKind)
			}
			if got := res.Changes[idx].Class; got != tc.wantClass {
				t.Errorf("change %q classified as %q, want %q", tc.wantKind, got, tc.wantClass)
			}
		})
	}
}

// TestBuildColumnRegistry pins the behaviour the submission grid depends on:
// every question ever asked keeps a column, and a field whose type changed is
// never merged into one column.
func TestBuildColumnRegistry(t *testing.T) {
	t.Parallel()

	v1 := base()
	v2 := base()
	delete(v2.Fields, "f_note")
	v2.Pages[1].Fields = nil
	v2.Fields["f_city"] = Field{Type: TypeText, Label: "City"}
	v2.Pages[1].Fields = []FieldID{"f_city"}

	v3 := base()
	delete(v3.Fields, "f_note")
	v3.Fields["f_city"] = Field{Type: TypeDropdown, Label: "City",
		Options: []Option{{ID: "o_hn", Label: "Ha Noi"}}}
	v3.Pages[1].Fields = []FieldID{"f_city"}

	cols := BuildColumnRegistry([]VersionedSchema{
		{VersionNo: 1, Schema: v1}, {VersionNo: 2, Schema: v2}, {VersionNo: 3, Schema: v3},
	})

	byField := map[FieldID][]Column{}
	for _, c := range cols {
		byField[c.FieldID] = append(byField[c.FieldID], c)
	}

	if got := len(byField["f_city"]); got != 2 {
		t.Errorf("f_city changed type, so it needs one column per type; got %d", got)
	}
	note := byField["f_note"]
	if len(note) != 1 {
		t.Fatalf("f_note should keep exactly one column, got %d", len(note))
	}
	if note[0].RetiredAfter != 1 {
		t.Errorf("f_note was removed after v1, want RetiredAfter=1, got %d", note[0].RetiredAfter)
	}
	if !slices.Equal(note[0].InVersions, []int{1}) {
		t.Errorf("f_note InVersions = %v, want [1]", note[0].InVersions)
	}
	if name := byField["f_name"]; len(name) != 1 || name[0].RetiredAfter != 0 {
		t.Errorf("f_name is present in every version, so it must not be marked retired: %+v", name)
	}
}

// TestCellState pins the three-way distinction the grid relies on. Collapsing
// them would make every completion statistic wrong for any branching form.
func TestCellState(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		inSchema bool
		visible  []string
		answers  map[string]any
		want     string
	}{
		{name: "version never had the field", inSchema: false, want: CellNotAsked},
		{name: "hidden by a branch", inSchema: true, visible: []string{"other"}, want: CellHidden},
		{name: "shown and skipped", inSchema: true, visible: []string{"f"},
			answers: map[string]any{}, want: CellBlank},
		{name: "shown and empty string", inSchema: true, visible: []string{"f"},
			answers: map[string]any{"f": ""}, want: CellBlank},
		{name: "answered", inSchema: true, visible: []string{"f"},
			answers: map[string]any{"f": "x"}, want: CellAnswered},
		{name: "empty multi-select counts as blank", inSchema: true, visible: []string{"f"},
			answers: map[string]any{"f": []any{}}, want: CellBlank},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := CellState(tc.inSchema, tc.visible, tc.answers, "f"); got != tc.want {
				t.Errorf("CellState = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestValidateRejectsCrossPageActions pins a rule that used to save, publish and
// read correctly in the builder while doing nothing at all.
//
// evalPage resolves visibility from the page's own field list, so an action
// aimed at a field on a different page is silently dropped. The respondent never
// sees the question, no error is raised anywhere, and the form looks correct to
// whoever built it.
func TestValidateRejectsCrossPageActions(t *testing.T) {
	t.Parallel()

	schema := Schema{
		Pages: []Page{
			{ID: "pg_one", Title: "One", Fields: []FieldID{"fld_trigger"}},
			{ID: "pg_two", Title: "Two", Fields: []FieldID{"fld_elsewhere"}},
		},
		Fields: map[FieldID]Field{
			"fld_trigger":   {Type: TypeText, Label: "Trigger"},
			"fld_elsewhere": {Type: TypeText, Label: "Elsewhere", Hidden: true},
		},
		Rules: []Rule{{
			ID: "rl_cross", OnPage: "pg_one",
			When: Condition{Op: OpIsNotEmpty, Field: "fld_trigger"},
			Then: []Action{{Action: ActionShow, Target: "fld_elsewhere"}},
		}},
	}

	res := Validate(schema)
	if res.OK {
		t.Fatal("Validate accepted a rule acting on a field from another page")
	}

	var found bool
	for _, issue := range res.Issues {
		if issue.Severity == SeverityError && strings.Contains(issue.Message, "pg_two") {
			found = true
		}
	}
	if !found {
		t.Errorf("no error naming the field's actual page; issues = %+v", res.Issues)
	}

	// The same action on the same page stays valid: that is the whole point of
	// show/hide, and over-tightening here would break every conditional field.
	schema.Pages[0].Fields = append(schema.Pages[0].Fields, "fld_elsewhere")
	schema.Pages[1].Fields = nil
	if res := Validate(schema); !res.OK {
		t.Errorf("Validate rejected a same-page show: %+v", res.Issues)
	}
}
