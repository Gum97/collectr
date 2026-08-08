package store

import "testing"

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
