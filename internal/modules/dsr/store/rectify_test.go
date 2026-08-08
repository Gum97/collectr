package store

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

// A correction is written into the plaintext answers column wholesale, so what
// it may contain decides what ends up there.
//
// The sensitive case is the one that matters: those answers live in answers_enc,
// sealed under the subject's own key, and destroying that key is what erasure
// does. A sensitive value written here would sit readable beside the ciphertext
// and survive the erasure that reports success -- so crypto-shredding would go
// on being advertised while it no longer covered the value most worth covering.
// This was reachable from a live portal session before the check existed.
func TestCheckRectifiable(t *testing.T) {
	t.Parallel()

	schema := versionSchema{Fields: map[string]SubjectQuestion{
		"f_name":  {Label: "Tên", Type: "text"},
		"f_phone": {Label: "Điện thoại", Type: "text"},
		"f_id":    {Label: "Số CCCD", Type: "text", Sensitive: true},
	}}

	tests := []struct {
		name    string
		answers map[string]any
		wantErr bool
	}{
		{"an ordinary field", map[string]any{"f_name": "Anh"}, false},
		{"several ordinary fields", map[string]any{"f_name": "Anh", "f_phone": "0912345678"}, false},
		{"nothing at all", map[string]any{}, false},
		{"a sensitive field", map[string]any{"f_id": "001234567890"}, true},
		{"one sensitive field hidden among valid ones",
			map[string]any{"f_name": "Anh", "f_id": "001234567890"}, true},
		{"a field that is not in the schema", map[string]any{"f_invented": "x"}, true},
		{"an empty field id", map[string]any{"": "x"}, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := checkRectifiable(schema, tc.answers)
			if tc.wantErr && err == nil {
				t.Fatal("expected the correction to be refused, it was accepted")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("expected the correction to be accepted, got %v", err)
			}
		})
	}
}

// The label on a revision is what separates "the person fixed their own typo"
// from "an employee changed somebody else's answers". Both are legitimate; only
// one of them needs a request behind it, and a reader a year later has to be
// able to tell which happened.
func TestRectifierLabels(t *testing.T) {
	t.Parallel()

	subject := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	user := uuid.MustParse("22222222-2222-2222-2222-222222222222")

	s := SubjectRectifier(subject)
	if s.Source != "dsr_self_service" {
		t.Errorf("subject source = %q, want dsr_self_service", s.Source)
	}
	if !strings.HasPrefix(s.ChangedBy, "subject:") {
		t.Errorf("subject changed_by = %q, want a subject: prefix", s.ChangedBy)
	}

	o := OperatorRectifier(user)
	if o.Source != "dsr_operator" {
		t.Errorf("operator source = %q, want dsr_operator", o.Source)
	}
	if !strings.HasPrefix(o.ChangedBy, "user:") {
		t.Errorf("operator changed_by = %q, want a user: prefix", o.ChangedBy)
	}

	// The two must never collapse into one another: a trail that cannot
	// distinguish them makes the defensible case unprovable.
	if s.Source == o.Source || s.ChangedBy == o.ChangedBy {
		t.Fatal("subject and operator rectifications are indistinguishable in the revision row")
	}
}
