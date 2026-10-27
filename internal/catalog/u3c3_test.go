package catalog_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/buzzqw/indexarr/internal/engine"
)

// TestU3C3NativeFixture pins the U3C3 search: the fixed search2 token and the
// query reach the home page, the ad cells at the end of the row are ignored,
// and every row keeps its magnet, size, date and category.
func TestU3C3NativeFixture(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		if got := r.URL.Query().Get("search2"); got != "eelja3lfe1a1" {
			t.Errorf("search2 = %q, want the site token", got)
		}
		if got := r.URL.Query().Get("search"); got != "matrix" {
			t.Errorf("search = %q, want matrix", got)
		}
		serveFile(t, w, "u3c3/search.html")
	}))
	t.Cleanup(server.Close)

	connector := nativeConnector(t, "u3c3", server.URL)
	items, err := connector.Search(context.Background(), engine.SearchRequest{
		Mode: engine.ModeSearch, Query: "matrix", Limit: 60,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) < 20 {
		t.Fatalf("items = %d, want at least 20", len(items))
	}
	for i, item := range items {
		if strings.TrimSpace(item.Title) == "" {
			t.Errorf("item %d: empty title", i)
		}
		if !strings.HasPrefix(item.Magnet, "magnet:?xt=urn:btih:") {
			t.Errorf("item %d (%q): magnet = %q", i, item.Title, item.Magnet)
		}
		if item.Size <= 0 {
			t.Errorf("item %d (%q): size = %d", i, item.Title, item.Size)
		}
		if len(item.Categories) == 0 {
			t.Errorf("item %d (%q): no category", i, item.Title)
		}
	}
	if items[0].Published.IsZero() {
		t.Error("first row has no publish date")
	}
}
