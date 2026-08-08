package slug

import "testing"

func TestMake(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		// The case that motivated the package: a plain a-z filter deletes every
		// accented vowel and leaves an unreadable stub.
		{name: "vietnamese", input: "Tuyển dụng Q4", want: "tuyen-dung-q4"},
		{name: "d with stroke", input: "Đăng ký dùng thử", want: "dang-ky-dung-thu"},
		{name: "all tone marks on one vowel", input: "à á ả ã ạ", want: "a-a-a-a-a"},
		{name: "horn vowels", input: "Khảo sát người dùng", want: "khao-sat-nguoi-dung"},

		{name: "plain ascii", input: "Marketing 2026", want: "marketing-2026"},
		{name: "collapses separators", input: "a   b___c...d", want: "a-b-c-d"},
		{name: "trims edges", input: "  --hello--  ", want: "hello"},
		{name: "drops punctuation", input: "Tết & Xuân!", want: "tet-xuan"},
		{name: "drops other scripts", input: "日本語 test", want: "test"},
		{name: "empty", input: "", want: ""},
		{name: "only punctuation", input: "!!!", want: ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := Make(tc.input); got != tc.want {
				t.Errorf("Make(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// TestMakeIsIdempotent: slugging a slug must not change it, or a value that
// round-trips through the interface would quietly rename itself.
func TestMakeIsIdempotent(t *testing.T) {
	t.Parallel()

	for _, in := range []string{"Tuyển dụng Q4", "a   b", "--x--", "Đăng ký"} {
		once := Make(in)
		if twice := Make(once); twice != once {
			t.Errorf("Make(%q) = %q, but Make(%q) = %q", in, once, once, twice)
		}
	}
}
