package engine

import "testing"

func TestEffectiveKeywords(t *testing.T) {
	cases := []struct {
		name   string
		req    SearchRequest
		format string
		want   string
	}{
		{"tv episode default", SearchRequest{Mode: ModeTV, Query: "Stranger Things", Season: 4, Episode: 1}, "", "Stranger Things S04E01"},
		{"tv season only", SearchRequest{Mode: ModeTV, Query: "Stranger Things", Season: 4}, "", "Stranger Things S04"},
		{"tv existing marker", SearchRequest{Mode: ModeTV, Query: "Show S04E01", Season: 4, Episode: 1}, "", "Show S04E01"},
		{"tv 1x05", SearchRequest{Mode: ModeTV, Query: "Show", Season: 4, Episode: 1}, "1x05", "Show 4x01"},
		{"tv separate", SearchRequest{Mode: ModeTV, Query: "Show", Season: 4, Episode: 1}, "separate", "Show"},
		{"search unchanged", SearchRequest{Mode: ModeSearch, Query: "Show", Season: 4, Episode: 1}, "", "Show"},
		{"movie year", SearchRequest{Mode: ModeMovie, Query: "Dune", Year: 2021}, "", "Dune 2021"},
		{"empty query", SearchRequest{Mode: ModeTV, Season: 4, Episode: 1}, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			values := queryTemplateMap(tc.req, tc.format)
			if values["keywords"] != tc.want {
				t.Errorf("keywords = %q, want %q", values["keywords"], tc.want)
			}
			if values["keywords_raw"] != tc.req.Query {
				t.Errorf("keywords_raw = %q, want %q", values["keywords_raw"], tc.req.Query)
			}
		})
	}
}

func TestKeywordsTitleDropsTheEpisodeMarker(t *testing.T) {
	for query, want := range map[string]string{
		"I misteri di Murdoch S13E01": "I misteri di Murdoch",
		"FBI: International S03":      "FBI: International",
		"Bones 6x12":                  "Bones",
		"Onslaught 2026":              "Onslaught 2026",
		"S01E01":                      "S01E01",
	} {
		if got := queryTemplateMap(SearchRequest{Query: query}, "separate")["keywords_title"]; got != want {
			t.Errorf("keywords_title(%q) = %q, want %q", query, got, want)
		}
	}
}
