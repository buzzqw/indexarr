package catalog_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/buzzqw/indexarr/internal/engine"
)

// The account variant used to send `search=` for an empty query. The site
// answers that page with an error, so every health check and every periodic
// run returned zero rows while real queries worked: a health problem, not a
// login one. params_multi drops the empty value, so the same request reads the
// listing instead.
func TestCorsaroAccountEmptyQueryOmitsTheSearchParameter(t *testing.T) {
	var withSearch, withoutSearch atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/index.php" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Has("search") {
			withSearch.Add(1)
		} else {
			withoutSearch.Add(1)
		}
		serveFile(t, w, "corsaro-account/search.html")
	}))
	t.Cleanup(server.Close)

	def := parseDefinition(t, "corsaro-account")
	if def.Health == nil || def.Health.Query != "" {
		t.Fatalf("health = %+v, this test covers the empty-query path", def.Health)
	}
	def.Session = nil
	def.Meta.URLs[0] = server.URL + "/"
	connector, err := engine.NewDefinitionConnector(engine.DefinitionConfig{
		Indexer:    engine.IndexerRef{Slug: def.Meta.ID, Name: def.Meta.Name},
		Definition: def,
		Settings:   map[string]string{"cookie": "session=1"},
		Transport:  engine.NewTransport(engine.TransportConfig{AllowPrivate: true}),
	})
	if err != nil {
		t.Fatalf("NewDefinitionConnector: %v", err)
	}

	// The health job sends an empty query with a limit of 100.
	items, err := connector.Search(context.Background(), engine.SearchRequest{Mode: engine.ModeSearch, Limit: 100})
	if err != nil {
		t.Fatalf("empty search: %v", err)
	}
	if len(items) < 3 {
		t.Fatalf("empty search items = %d, want the listing rows", len(items))
	}
	if got := withSearch.Load(); got != 0 {
		t.Errorf("empty search sent the search parameter %d times, want 0", got)
	}
	if got := withoutSearch.Load(); got == 0 {
		t.Error("empty search never reached the site")
	}

	// A real query still carries the parameter.
	items, err = connector.Search(context.Background(), engine.SearchRequest{
		Mode: engine.ModeSearch, Query: "Elsbeth", Limit: 100,
	})
	if err != nil {
		t.Fatalf("query search: %v", err)
	}
	if len(items) < 3 {
		t.Fatalf("query search items = %d, want the listing rows", len(items))
	}
	if got := withSearch.Load(); got == 0 {
		t.Error("a real query did not send the search parameter")
	}
}
