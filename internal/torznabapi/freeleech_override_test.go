package torznabapi

import (
	"net/url"
	"testing"

	"github.com/buzzqw/indexarr/internal/store"
)

// TestFreeleechOverrideParsing covers the three accepted forms and the
// default: anything else keeps the source's saved choice, because a client
// that sends an unexpected value must still receive its results.
func TestFreeleechOverrideParsing(t *testing.T) {
	cases := []struct {
		query string
		want  *bool
	}{
		{"", nil},
		{"freeleech=0", boolPtr(false)},
		{"freeleech=off", boolPtr(false)},
		{"freeleech=false", boolPtr(false)},
		{"freeleech=1", boolPtr(true)},
		{"freeleech=yes", boolPtr(true)},
		{"ignore_filters=1", boolPtr(false)},
		{"ignore_filters=true", boolPtr(false)},
		{"ignore_filters=0", nil},
		{"freeleech=maybe", nil},
		// freeleech wins over the alias, and 0 is a real answer, not "unset".
		{"freeleech=1&ignore_filters=1", boolPtr(true)},
	}
	for _, tc := range cases {
		values, err := url.ParseQuery(tc.query)
		if err != nil {
			t.Fatalf("ParseQuery(%q): %v", tc.query, err)
		}
		got := freeleechOverride(values)
		switch {
		case tc.want == nil && got != nil:
			t.Errorf("%q: got %v, want nil", tc.query, *got)
		case tc.want != nil && got == nil:
			t.Errorf("%q: got nil, want %v", tc.query, *tc.want)
		case tc.want != nil && *got != *tc.want:
			t.Errorf("%q: got %v, want %v", tc.query, *got, *tc.want)
		}
	}
}

// TestLiveQueryCarriesTheOverride proves the parsed value reaches the fan-out:
// the connector is where the filter is applied, so losing it on the way would
// make the parameter silently useless.
func TestLiveQueryCarriesTheOverride(t *testing.T) {
	filter := store.Filter{Query: "x"}
	on := true
	query := liveQueryFor("search", filter, "sonarr", &on)
	if query.FreeleechOnly == nil || !*query.FreeleechOnly {
		t.Fatalf("FreeleechOnly = %v, want true", query.FreeleechOnly)
	}
	if query := liveQueryFor("search", filter, "sonarr", nil); query.FreeleechOnly != nil {
		t.Fatalf("FreeleechOnly = %v, want nil", *query.FreeleechOnly)
	}
}

func boolPtr(value bool) *bool { return &value }
