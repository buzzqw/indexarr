package engine

import (
	"slices"
	"testing"

	"github.com/buzzqw/indexarr/internal/definition"
)

// TestParseLanguagesMapsCodesAndNames covers what a definition may write in
// its optional `languages` row field: codes, English names, the site's own
// name, several separators. The result is the English name Torznab carries.
func TestParseLanguagesMapsCodesAndNames(t *testing.T) {
	cases := []struct {
		in   []string
		want []string
	}{
		{[]string{"ita"}, []string{"Italian"}},
		{[]string{"ITA/ENG"}, []string{"Italian", "English"}},
		{[]string{"English, French, Portuguese"}, []string{"English", "French", "Portuguese"}},
		{[]string{"inglese", ""}, nil},
		{[]string{"it;it;Italian"}, []string{"Italian"}},
		{[]string{"", "  "}, nil},
		{[]string{"italiano", "en"}, []string{"Italian", "English"}},
	}
	for _, tc := range cases {
		got := ParseLanguages(tc.in...)
		if !slices.Equal(got, tc.want) {
			t.Errorf("ParseLanguages(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// TestParseLanguagesIsBounded keeps one page with every audio track from
// turning a single release into a long attribute list.
func TestParseLanguagesIsBounded(t *testing.T) {
	got := ParseLanguages("English,French,German,Italian,Spanish,Portuguese,Russian,Japanese,Polish,Dutch")
	if len(got) != maxLanguages {
		t.Fatalf("len = %d, want %d: %v", len(got), maxLanguages, got)
	}
}

// TestNormalizeRowReadsTheLanguagesField proves the row field reaches the
// Item, and that a definition without it stays "unknown" instead of empty
// strings or a guessed language.
func TestNormalizeRowReadsTheLanguagesField(t *testing.T) {
	def := loadTestDefinition(t, "pagination.yaml", "http://127.0.0.1:1")
	def.Search.Response.Fields["languages"] = definition.Field{Selector: definition.Selector{".lang"}}
	connector, err := NewDefinitionConnector(DefinitionConfig{
		Definition: def,
		Settings:   map[string]string{},
	})
	if err != nil {
		t.Fatal(err)
	}

	item, ok, err := connector.normalizeRow(map[string]string{
		"title":     "Film.2024.ITA.ENG.1080p",
		"details":   "/d/1",
		"download":  "/d/1.torrent",
		"languages": "ita, eng",
	}, nil)
	if err != nil || !ok {
		t.Fatalf("normalizeRow = %+v, ok=%v, err=%v", item, ok, err)
	}
	if !slices.Equal(item.Languages, []string{"Italian", "English"}) {
		t.Fatalf("languages = %v", item.Languages)
	}

	item, ok, err = connector.normalizeRow(map[string]string{
		"title":    "Film.2024.1080p",
		"details":  "/d/2",
		"download": "/d/2.torrent",
	}, nil)
	if err != nil || !ok {
		t.Fatalf("normalizeRow without languages = %+v, ok=%v, err=%v", item, ok, err)
	}
	if len(item.Languages) != 0 {
		t.Fatalf("languages without the field = %v, want none", item.Languages)
	}
}
