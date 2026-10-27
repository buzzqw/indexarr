package engine

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/buzzqw/indexarr/internal/definition"
)

const indexTestDefinition = `schema: 1
meta:
  id: index-test
  name: Index test
  type: private
  protocol: ed2k
  urls:
    - http://REPLACE/
capabilities:
  search: [q]
  tv-search: [q]
search:
  index:
    max_topics: 2
    response:
      type: html
      rows: { selector: "a.postlink-local" }
      fields:
        title:   { self: true }
        details: { self: true, attr: href, type: url }
    pages:
      - { get: /list-a, letter: A, standard: 2000 }
      - { get: /list-b, letter: B, standard: 5000 }
download:
  resolve_on: search
  expand: file
  from: details
  extract:
    - ed2k: { body: true }
      all: true
`

func indexTestConnector(t *testing.T, handler http.Handler) *DefinitionConnector {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	source := strings.ReplaceAll(indexTestDefinition, "http://REPLACE/", server.URL+"/")
	def, err := definition.Parse([]byte(source))
	if err != nil {
		t.Fatalf("parse definition: %v", err)
	}
	connector, err := NewDefinitionConnector(DefinitionConfig{
		Indexer:    IndexerRef{Slug: def.Meta.ID, Name: def.Meta.Name},
		Definition: def,
		Transport:  NewTransport(TransportConfig{AllowPrivate: true}),
	})
	if err != nil {
		t.Fatalf("NewDefinitionConnector: %v", err)
	}
	return connector
}

func TestIndexSearchFetchesOnlyTheQueryLetterAndOpensMatchingTopics(t *testing.T) {
	var listA, listB atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/list-a":
			listA.Add(1)
			writeBody(t, w, []byte(`<html><body><a class="postlink-local" href="/viewtopic.php?t=1">Anora</a></body></html>`))
		case "/list-b":
			listB.Add(1)
			writeBody(t, w, []byte(`<html><body>
<a class="postlink-local" href="/viewtopic.php?t=2">Bones - Stagione 6 [COMPLETA]</a>
<a class="postlink-local" href="/viewtopic.php?t=3">Another Show</a>
</body></html>`))
		case "/viewtopic.php":
			if r.URL.Query().Get("t") != "2" {
				http.NotFound(w, r)
				return
			}
			writeBody(t, w, []byte(`<html><body>
<a href="ed2k://|file|Bones.S06E01.mkv|100|11111111111111111111111111111111|/">Bones S06E01</a>
<a href="ed2k://|file|Bones.S06E02.mkv|200|22222222222222222222222222222222|/">Bones S06E02</a>
</body></html>`))
		default:
			http.NotFound(w, r)
		}
	})
	connector := indexTestConnector(t, handler)
	items, err := connector.Search(context.Background(), SearchRequest{Mode: ModeTV, Query: "Bones", Season: 6})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("items = %d, want 2: %+v", len(items), items)
	}
	if items[0].Ed2k == "" || !strings.Contains(items[0].Title, "Bones") {
		t.Errorf("first item = %+v", items[0])
	}
	if items[0].Categories[0] != 5000 {
		t.Errorf("category = %v, want 5000", items[0].Categories)
	}
	if listA.Load() != 0 {
		t.Errorf("letter A page was fetched for a B query")
	}
	if listB.Load() != 1 {
		t.Errorf("letter B requests = %d, want 1", listB.Load())
	}
	// The second query reuses the cached page.
	if _, err := connector.Search(context.Background(), SearchRequest{Mode: ModeTV, Query: "Bones"}); err != nil {
		t.Fatalf("second Search: %v", err)
	}
	if listB.Load() != 1 {
		t.Errorf("letter B requests after cache = %d, want 1", listB.Load())
	}
}

func TestIndexMatchesTitlesWithDifferentSpelling(t *testing.T) {
	entries := []indexEntry{
		{Title: "Dune - Parte due", Details: "/viewtopic.php?t=1"},
		{Title: "Dune (Lynch, 1984)", Details: "/viewtopic.php?t=2"},
		{Title: "Another Movie", Details: "/viewtopic.php?t=3"},
	}
	matches := matchIndexEntries(entries, SearchRequest{Query: "Dune Part Two", Mode: ModeMovie})
	if len(matches) == 0 || matches[0].Title != "Dune - Parte due" {
		t.Fatalf("matches = %+v", matches)
	}
	if got := indexQueryLetter("The Last of Us"); got != "T" {
		t.Errorf("letter = %q, want T", got)
	}
	if got := indexQueryLetter("1917"); got != "#" {
		t.Errorf("letter = %q, want #", got)
	}
}

func TestIndexSearchFallsBackToTheSiteSearch(t *testing.T) {
	var searchCalls atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/list-a":
			writeBody(t, w, []byte(`<html><body><a class="postlink-local" href="/viewtopic.php?t=1">Anora</a></body></html>`))
		case "/list-b":
			writeBody(t, w, []byte(`<html><body><a class="postlink-local" href="/viewtopic.php?t=2">Bones</a></body></html>`))
		case "/search.php":
			searchCalls.Add(1)
			writeBody(t, w, []byte(`<html><body><ul class="topics"><li class="row">
<a class="title" href="/viewtopic.php?t=9">Fallback Result</a>
</li></ul></body></html>`))
		case "/viewtopic.php":
			writeBody(t, w, []byte(`<html><body><a href="ed2k://|file|Fallback.mkv|10|33333333333333333333333333333333|/">Fallback</a></body></html>`))
		default:
			http.NotFound(w, r)
		}
	})
	connector := indexTestConnector(t, handler)
	// The fixture definition has no normal search request, so add a minimal
	// fallback to the parsed definition.
	connector.def.Search.Request.Get = "/search.php"
	connector.def.Search.Response = definition.Response{
		Type: "html",
		Rows: definition.RowSelector{Selector: definition.Selector{"ul.topics > li.row"}},
		Fields: map[string]definition.Field{
			"title":   {Selector: definition.Selector{"a.title"}},
			"details": {Selector: definition.Selector{"a.title"}, Attr: "href", Type: "url"},
		},
		Required: []string{"title", "details"},
	}
	connector.def.Download = nil // the fallback row carries no link; only the search is under test
	items, err := connector.Search(context.Background(), SearchRequest{Mode: ModeSearch, Query: "Nomatch"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if searchCalls.Load() != 1 {
		t.Fatalf("search.php calls = %d, want 1", searchCalls.Load())
	}
	if len(items) != 1 || items[0].Title != "Fallback Result" {
		t.Fatalf("items = %+v", items)
	}
}

func TestIndexMatchesWholeWordsOnly(t *testing.T) {
	entries := []indexEntry{
		{Title: "Superman (2025)", Details: "/viewtopic.php?t=1"},
		{Title: "Hitman", Details: "/viewtopic.php?t=2"},
		{Title: "Il ritorno", Details: "/viewtopic.php?t=3"},
		{Title: "Up (2009)", Details: "/viewtopic.php?t=4"},
	}
	cases := map[string][]string{
		"Up":  {"Up (2009)"},
		"It":  nil,
		"Man": nil,
	}
	for query, want := range cases {
		var got []string
		for _, match := range matchIndexEntries(entries, SearchRequest{Query: query}) {
			got = append(got, match.Title)
		}
		if strings.Join(got, "|") != strings.Join(want, "|") {
			t.Errorf("%q matches %v, want %v", query, got, want)
		}
	}
}

func TestIndexIsSkippedForBookAndMusic(t *testing.T) {
	tv := definition.IndexPage{Get: "/tv", Standard: 5000}
	movies := definition.IndexPage{Get: "/a", Letter: "A", Standard: 2000}
	for _, mode := range []Mode{ModeBook, ModeMusic} {
		if indexPageForMode(tv, mode) || indexPageForMode(movies, mode) {
			t.Errorf("%s search reads the film/TV index", mode)
		}
	}
	if !indexPageForMode(tv, ModeSearch) || !indexPageForMode(movies, ModeSearch) {
		t.Error("a generic search must read every list")
	}
	if !indexPageForMode(tv, ModeTV) || indexPageForMode(movies, ModeTV) {
		t.Error("a TV search must read only the TV list")
	}
	if indexPageForMode(tv, ModeMovie) || !indexPageForMode(movies, ModeMovie) {
		t.Error("a movie search must read only the movie letters")
	}
}

func TestIndexSkipsTopicsOfAnotherSeason(t *testing.T) {
	entries := []indexEntry{
		{Title: "DARK MATTER - Stagione 2", Details: "/viewtopic.php?t=1"},
		{Title: "DARK MATTER - Stagione 1", Details: "/viewtopic.php?t=2"},
		{Title: "DARK MATTER - Stagione 3", Details: "/viewtopic.php?t=3"},
		{Title: "HEROES REBORN - DARK MATTER - Miniserie", Details: "/viewtopic.php?t=4"},
	}
	var got []string
	for _, match := range matchIndexEntries(entries, SearchRequest{Query: "Dark Matter", Mode: ModeTV, Season: 2, Episode: 9}) {
		got = append(got, match.Title)
	}
	want := "DARK MATTER - Stagione 2|HEROES REBORN - DARK MATTER - Miniserie"
	if strings.Join(got, "|") != want {
		t.Errorf("matches = %v, want %s", got, want)
	}
	all := matchIndexEntries(entries, SearchRequest{Query: "Dark Matter", Mode: ModeTV})
	if len(all) != 4 {
		t.Errorf("without a season every topic stays a candidate, got %d", len(all))
	}
}
