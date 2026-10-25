package textutil

import "testing"

func TestEscapeLike(t *testing.T) {
	cases := []struct{ in, want string }{
		{"plain", "plain"},
		{"a%b", `a\%b`},
		{"a_b", `a\_b`},
		{`a\b`, `a\\b`},
		{`%_\`, `\%\_\\`},
	}
	for _, tc := range cases {
		if got := EscapeLike(tc.in); got != tc.want {
			t.Errorf("EscapeLike(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
