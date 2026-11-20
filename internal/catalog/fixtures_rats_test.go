package catalog_test

// The Rats Search definition is a connector, not a page: the node answers JSON
// over its REST API. This test runs the real family connector through the
// settings the YAML declares and against the shipped fixture, so the
// definition, the connector and the fixture cannot describe three different
// protocols: a renamed method, a changed envelope, a new content type or a
// capability that only exists in one of them fails here.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"testing"

	"github.com/buzzqw/indexarr/internal/definition"
	"github.com/buzzqw/indexarr/internal/engine"
	"github.com/buzzqw/indexarr/internal/family"
)

func TestRatsNodeFixture(t *testing.T) {
	def := parseDefinition(t, "rats-generic")
	if len(def.Tests) == 0 {
		t.Fatal("definition has no fixture test")
	}
	test := def.Tests[0]
	if test.Fixture != "fixtures/rats/search.json" {
		t.Fatalf("fixture = %q, want fixtures/rats/search.json", test.Fixture)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/search.torrents", "/api/search.recent":
			// tests[].fixture is written relative to the definition directory
			// (fixtures/<id>/<file>), the way indexarr-def -check-fixtures
			// resolves it; serveFile is already rooted at definitions/fixtures.
			serveFile(t, w, strings.TrimPrefix(test.Fixture, "fixtures/"))
		case "/api/stats.get":
			_, _ = w.Write([]byte(`{"success": true, "data": {"torrents": 13, "files": 2089, "size": 15000000000, "peers": 0}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	// The shipped default address must at least parse: it is what the form
	// prefills and what a user with a node on this machine will keep.
	defaultBase, hasDefault := ratsSetting(def, "url")
	if !hasDefault || strings.TrimSpace(defaultBase) == "" {
		t.Fatal("the definition has no default node address")
	}
	if _, err := family.NewRats(family.RatsConfig{BaseURL: defaultBase}); err != nil {
		t.Fatalf("the default address %q does not parse: %v", defaultBase, err)
	}

	connector, err := family.NewRats(family.RatsConfig{
		Indexer:    engine.IndexerRef{Slug: def.Meta.ID, Name: def.Meta.Name},
		BaseURL:    server.URL, // what a user pastes: /api is added by the connector
		Transport:  engine.NewTransport(engine.TransportConfig{AllowPrivate: true}),
		SafeSearch: strings.EqualFold(ratsSettingValue(def, "safesearch"), "true"),
	})
	if err != nil {
		t.Fatalf("NewRats: %v", err)
	}

	items, err := connector.Search(context.Background(), engine.SearchRequest{
		Mode:  engine.ModeSearch,
		Query: "test",
		Limit: 50,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) < test.Expect.MinRows {
		t.Fatalf("items = %d, want at least %d", len(items), test.Expect.MinRows)
	}
	for _, item := range items {
		for _, required := range test.Expect.Required {
			switch required {
			case "title":
				if strings.TrimSpace(item.Title) == "" {
					t.Errorf("a row has no title: %+v", item)
				}
			case "magnet":
				if !strings.HasPrefix(item.Magnet, "magnet:?xt=urn:btih:") {
					t.Errorf("%q: magnet = %q", item.Title, item.Magnet)
				}
			case "size":
				if item.Size <= 0 {
					t.Errorf("%q: size = %d", item.Title, item.Size)
				}
			case "category":
				if len(item.Categories) == 0 {
					t.Errorf("%q: no category", item.Title)
				}
			default:
				t.Errorf("unknown required field %q in the definition", required)
			}
		}
		if item.InfoHash != strings.ToLower(item.InfoHash) || len(item.InfoHash) != 40 {
			t.Errorf("%q: infohash = %q, want lowercase hexadecimal", item.Title, item.InfoHash)
		}
	}

	// A video row is a movie or an episode: the node reads the file type, and
	// a stored release carries one category. The search that names no category
	// stores it under Movies.
	if video, ok := ratsItemByTitle(items, "Sintel 2010 2160p"); ok {
		if len(video.Categories) != 1 || video.Categories[0] != 2000 {
			t.Errorf("video categories = %v, want [2000]", video.Categories)
		}
	} else {
		t.Errorf("the fixture has no video row")
	}
	if book, ok := ratsItemByTitle(items, "The Dispossessed - Ursula K. Le Guin (epub)"); ok {
		if len(book.Categories) != 1 || book.Categories[0] != 7000 {
			t.Errorf("books categories = %v, want [7000]", book.Categories)
		}
	}

	// The newest list is the periodic walk: same object format, other method.
	latest, err := connector.Latest(context.Background(), engine.LatestRequest{Limit: 20})
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if len(latest) < test.Expect.MinRows {
		t.Fatalf("latest = %d, want at least %d", len(latest), test.Expect.MinRows)
	}

	// The definition is the documented face of the connector: every mode and
	// every parameter it advertises must be the ones the connector answers.
	caps := connector.Capabilities()
	documented := map[engine.Mode][]string{
		engine.ModeSearch: def.Capabilities.Search,
		engine.ModeTV:     def.Capabilities.TVSearch,
		engine.ModeMovie:  def.Capabilities.MovieSearch,
		engine.ModeMusic:  def.Capabilities.MusicSearch,
		engine.ModeBook:   def.Capabilities.BookSearch,
	}
	if len(caps.Modes) != len(documented) {
		t.Errorf("connector advertises %d modes, the definition documents %d", len(caps.Modes), len(documented))
	}
	for mode, params := range documented {
		if !caps.Supports(mode) {
			t.Errorf("connector does not advertise %q", mode)
			continue
		}
		if !sameStrings(sortedCopy(caps.Params[mode]), sortedCopy(params)) {
			t.Errorf("%q parameters = %v, the definition documents %v", mode, caps.Params[mode], params)
		}
	}

	// The connection test reports the node and its index size.
	result, err := connector.Test(context.Background())
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	if !result.OK {
		t.Fatalf("Test = %+v, want a passing check", result)
	}
	if len(result.Phases) != 2 || !strings.Contains(result.Phases[0].Message, "13") {
		t.Errorf("phases = %+v, want a reach phase reporting the index size", result.Phases)
	}
}

// TestRatsCategoryTableMatchesTheConnector keeps the table of the definition
// and the mapping in the connector from drifting apart. The node has one coarse
// content type, so the table IS the contract with the user: every row of the
// YAML is asked back as a query, and both halves must agree — the content type
// the connector sends and the standard category it stores.
func TestRatsCategoryTableMatchesTheConnector(t *testing.T) {
	def := parseDefinition(t, "rats-generic")
	if len(def.Categories) == 0 {
		t.Fatal("the definition declares no content types")
	}

	var last url.URL
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		last = *r.URL
		serveFile(t, w, "rats/search.json")
	}))
	t.Cleanup(server.Close)

	connector, err := family.NewRats(family.RatsConfig{
		Indexer:   engine.IndexerRef{Slug: def.Meta.ID, Name: def.Meta.Name},
		BaseURL:   server.URL,
		Transport: engine.NewTransport(engine.TransportConfig{AllowPrivate: true}),
	})
	if err != nil {
		t.Fatalf("NewRats: %v", err)
	}

	seen := make(map[int]bool, len(def.Categories))
	for _, category := range def.Categories {
		site := strings.ToLower(strings.TrimSpace(category.Site))
		if seen[category.Standard] && category.Standard >= 8000 {
			continue // pictures, archive and other are the same Other bucket
		}
		seen[category.Standard] = true

		last = url.URL{}
		items, err := connector.Search(context.Background(), engine.SearchRequest{
			Mode:       engine.ModeSearch,
			Query:      "test",
			Categories: []int{category.Standard},
		})
		if err != nil {
			t.Fatalf("%s: Search: %v", site, err)
		}

		// The node's type: a category with a content type of its own must ask
		// for it, and the Other bucket must not narrow the node at all.
		wantType := site
		if category.Standard >= 8000 {
			wantType = ""
		}
		if got := last.Query().Get("type"); got != wantType {
			t.Errorf("%s (%d): type = %q, want %q", site, category.Standard, got, wantType)
		}
		if len(items) == 0 {
			t.Errorf("%s (%d): the fixture has no row of that type", site, category.Standard)
		}
		for _, item := range items {
			// A request for a top-level category keeps its whole subtree (PC
			// also answers with PC/Games), so the check is the thousand.
			if len(item.Categories) != 1 || item.Categories[0]/1000 != category.Standard/1000 {
				t.Errorf("%s (%d): %q stored as %v", site, category.Standard, item.Title, item.Categories)
			}
		}
	}

	// TV is the one standard category the table does not list as a content
	// type: the node has no way to tell an episode from a film, and the search
	// that asks for TV is what stores the row there.
	last = url.URL{}
	items, err := connector.Search(context.Background(), engine.SearchRequest{
		Mode:       engine.ModeTV,
		Query:      "test",
		Categories: []int{5000},
	})
	if err != nil {
		t.Fatalf("tv: Search: %v", err)
	}
	if got := last.Query().Get("type"); got != "video" {
		t.Errorf("tv: type = %q, want video", got)
	}
	if len(items) == 0 {
		t.Fatal("tv: the fixture has no video row")
	}
	for _, item := range items {
		if len(item.Categories) != 1 || item.Categories[0] != 5000 {
			t.Errorf("tv: %q stored as %v, want [5000]", item.Title, item.Categories)
		}
	}
}

// ratsSetting returns the default of a declared setting.
func ratsSetting(def *definition.Definition, name string) (string, bool) {
	for _, setting := range def.Settings {
		if setting.Name == name {
			return setting.Default, true
		}
	}
	return "", false
}

func ratsSettingValue(def *definition.Definition, name string) string {
	value, _ := ratsSetting(def, name)
	return value
}

func ratsItemByTitle(items []engine.Item, title string) (engine.Item, bool) {
	for _, item := range items {
		if item.Title == title {
			return item, true
		}
	}
	return engine.Item{}, false
}

func sortedCopy(values []string) []string {
	out := append([]string(nil), values...)
	sort.Strings(out)
	return out
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
