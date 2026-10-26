package engine

import "testing"

func TestParseSizeAcceptsJSONExponent(t *testing.T) {
	cases := []struct {
		raw  string
		want int64
	}{
		{"4.769627296e+09", 4769627296},
		{"2.218930069e+09", 2218930069},
		{"5.36870912e+08", 536870912},
		{"1073741824", 1073741824},
		{"1.5 GB", 1500000000},
		{"1,5 GiB", 1610612736},
		{"", 0},
		{"unknown", 0},
		{"1.5", 15}, // historical: plain numbers lose the separator
	}
	for _, tc := range cases {
		if got := parseSize(tc.raw); got != tc.want {
			t.Errorf("parseSize(%q) = %d, want %d", tc.raw, got, tc.want)
		}
	}
}
