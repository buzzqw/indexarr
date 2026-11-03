package textutil

import "testing"

func TestLanguageNeedles(t *testing.T) {
	for _, test := range []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"ita", []string{"Italian", "ita"}},
		{"Italian", []string{"Italian"}},
		{"italiano", []string{"Italian", "italiano"}},
		{"FRA", []string{"French", "FRA"}},
		{"klingon", []string{"klingon"}},
	} {
		got := LanguageNeedles(test.in)
		if len(got) != len(test.want) {
			t.Fatalf("LanguageNeedles(%q) = %v, want %v", test.in, got, test.want)
		}
		for i := range got {
			if got[i] != test.want[i] {
				t.Fatalf("LanguageNeedles(%q) = %v, want %v", test.in, got, test.want)
			}
		}
	}
}
