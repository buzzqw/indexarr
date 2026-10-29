package engine

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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

func TestIndexReadsTheSeasonOfAFreeTextQuery(t *testing.T) {
	for query, want := range map[string][2]int{
		"Bones S06E05":      {6, 5},
		"bones s6":          {6, 0},
		"Bones 6x05":        {6, 5},
		"Bones":             {0, 0},
		"Station 19 S07":    {7, 0},
		"Blade Runner 2049": {0, 0},
	} {
		season, episode := indexQuerySeasonEpisode(query)
		if season != want[0] || episode != want[1] {
			t.Errorf("%q: season %d episode %d, want %v", query, season, episode, want)
		}
	}
}

const indexPagedDefinition = `schema: 1
meta:
  id: index-paged
  name: Index paged
  type: private
  protocol: ed2k
  urls:
    - http://REPLACE/
capabilities:
  search: [q]
  tv-search: [q]
  movie-search: [q]
search:
  index:
    cache: 6h
    response:
      type: html
      rows: { selector: 'a.postlink-local[href*="viewtopic.php?t="]' }
      fields:
        title:   { self: true }
        details: { self: true, attr: href, type: url }
    pages:
      - { get: "/viewtopic.php?t=100", standard: 5000, next: "a.right-box.right" }
      - { get: "/viewtopic.php?t=200", letter: S, standard: 2000 }
      - { get: "/viewtopic.php?t=201", letter: A, standard: 2000 }
`

func indexPagedConnector(t *testing.T, handler http.Handler) *DefinitionConnector {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	source := strings.ReplaceAll(indexPagedDefinition, "http://REPLACE/", server.URL+"/")
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

// The DDUnlimited TV list is one topic over three pages: page 1 holds # to H,
// the series from I to Z are on pages 2 and 3. Reading only the first page
// sent every query for "Silo" or "Walker" to the flood-limited site search.
func TestIndexFollowsTheNextPageOfAList(t *testing.T) {
	var requests atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/viewtopic.php" || r.URL.Query().Get("t") != "100" {
			http.NotFound(w, r)
			return
		}
		requests.Add(1)
		switch r.URL.Query().Get("start") {
		case "":
			writeBody(t, w, []byte(`<html><body>
<a class="postlink-local" href="memberlist.php?mode=group&amp;g=1">Responsabile di sezione:</a>
<a class="postlink-local" href="./viewtopic.php?t=11">BONES - Stagione 6</a>
<a href="./viewtopic.php?t=100&amp;start=10" class="right-box right">Prossimo</a>
<a href="./viewtopic.php?t=100&amp;start=10" class="right-box right">Prossimo</a>
</body></html>`))
		case "10":
			writeBody(t, w, []byte(`<html><body>
<a class="postlink-local" href="./viewtopic.php?t=12">NEW TRICKS - Stagione 1</a>
<a href="./viewtopic.php?t=100&amp;start=0" class="left-box left">Precedente</a>
<a href="./viewtopic.php?t=100&amp;start=20" class="right-box right">Prossimo</a>
</body></html>`))
		case "20":
			writeBody(t, w, []byte(`<html><body>
<a class="postlink-local" href="./viewtopic.php?t=13">SILO - Stagione 3</a>
<a class="postlink-local" href="./viewtopic.php?t=14">SILO - Stagione 2</a>
<a href="./viewtopic.php?t=100&amp;start=10" class="left-box left">Precedente</a>
</body></html>`))
		default:
			http.NotFound(w, r)
		}
	})
	connector := indexPagedConnector(t, handler)
	page := connector.def.Search.Index.Pages[0]
	entries, err := connector.indexEntries(context.Background(), page)
	if err != nil {
		t.Fatalf("indexEntries: %v", err)
	}
	var titles []string
	for _, entry := range entries {
		titles = append(titles, entry.Title)
	}
	want := []string{"BONES - Stagione 6", "NEW TRICKS - Stagione 1", "SILO - Stagione 3", "SILO - Stagione 2"}
	if strings.Join(titles, "|") != strings.Join(want, "|") {
		t.Fatalf("titles = %q, want %q", titles, want)
	}
	if requests.Load() != 3 {
		t.Errorf("list requests = %d, want 3", requests.Load())
	}
	matches := matchIndexEntries(entries, SearchRequest{Mode: ModeTV, Query: "Silo", Season: 3})
	if len(matches) != 1 || matches[0].Title != "SILO - Stagione 3" {
		t.Errorf("Silo S03 matches = %+v", matches)
	}
	// The three pages are one cached list.
	if _, err := connector.indexEntries(context.Background(), page); err != nil {
		t.Fatalf("cached indexEntries: %v", err)
	}
	if requests.Load() != 3 {
		t.Errorf("list requests after cache = %d, want 3", requests.Load())
	}
	cached := connector.indexCache[page.Get]
	if ttl := time.Until(cached.expires); ttl < 5*time.Hour {
		t.Errorf("cache ttl = %s, want the definition's 6h", ttl)
	}
}

// A movie letter topic starts with a bar of links to the other letter topics.
// "A" is not a film: a query for the movie "A" (or any one-letter title) must
// not open the A list as if it were a release.
func TestIndexSkipsLinksBackToTheLists(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/viewtopic.php" || r.URL.Query().Get("t") != "200" {
			http.NotFound(w, r)
			return
		}
		writeBody(t, w, []byte(`<html><body>
<a class="postlink-local" href="./viewtopic.php?t=201">A</a>
<a class="postlink-local" href="./viewtopic.php?t=200">S</a>
<a class="postlink-local" href="viewforum.php?f=1987">Indice</a>
<a class="postlink-local" href="./viewtopic.php?t=3861373">Solo - A Star Wars Story (Howard, 2018)</a>
</body></html>`))
	})
	connector := indexPagedConnector(t, handler)
	entries, err := connector.indexEntries(context.Background(), connector.def.Search.Index.Pages[1])
	if err != nil {
		t.Fatalf("indexEntries: %v", err)
	}
	if len(entries) != 1 || entries[0].Title != "Solo - A Star Wars Story (Howard, 2018)" {
		t.Fatalf("entries = %+v", entries)
	}
}

// "9-1-1", "S.W.A.T." and "N.C.I.S." lose their words to the normalization:
// every letter is a word of one character and "1" never starts "911".
func TestIndexMatchesTitlesWrittenWithInitials(t *testing.T) {
	entries := []indexEntry{
		{Title: "911- Stagione 2", Details: "/viewtopic.php?t=1"},
		{Title: "S.W.A.T. - Stagione 8", Details: "/viewtopic.php?t=2"},
		{Title: "N.C.I.S. - Stagione 9", Details: "/viewtopic.php?t=3"},
		{Title: "SWAT - Stagione 7", Details: "/viewtopic.php?t=4"},
		{Title: "Star Wars - Stagione 1", Details: "/viewtopic.php?t=5"},
	}
	cases := []struct {
		query  string
		season int
		want   []string
	}{
		{"9-1-1", 2, []string{"911- Stagione 2"}},
		{"S.W.A.T.", 0, []string{"SWAT - Stagione 7", "S.W.A.T. - Stagione 8"}},
		{"SWAT", 8, []string{"S.W.A.T. - Stagione 8"}},
		{"NCIS", 9, []string{"N.C.I.S. - Stagione 9"}},
	}
	for _, tc := range cases {
		matches := matchIndexEntries(entries, SearchRequest{Mode: ModeTV, Query: tc.query, Season: tc.season})
		var titles []string
		for _, match := range matches {
			titles = append(titles, match.Title)
		}
		if strings.Join(titles, "|") != strings.Join(tc.want, "|") {
			t.Errorf("%q S%d: matches = %q, want %q", tc.query, tc.season, titles, tc.want)
		}
	}
}
