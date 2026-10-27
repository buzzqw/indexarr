package catalog_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/buzzqw/indexarr/internal/engine"
)

// TestXXXTorNativeFixture pins the adult tracker search captured behind
// Cloudflare: the search parameter reaches /b.php, both row classes (gai/tum)
// are read, and every row carries a magnet, a detail link and a size.
func TestXXXTorNativeFixture(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/b.php" {
			http.NotFound(w, r)
			return
		}
		if got := r.URL.Query().Get("search"); got != "anal" {
			t.Errorf("search = %q, want anal", got)
		}
		serveFile(t, w, "xxxtor/search.html")
	}))
	t.Cleanup(server.Close)

	connector := nativeConnector(t, "xxxtor", server.URL)
	items, err := connector.Search(context.Background(), engine.SearchRequest{
		Mode: engine.ModeSearch, Query: "anal", Limit: 100,
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
		if !strings.Contains(item.DetailsURL, "/torrent/") {
			t.Errorf("item %d (%q): details = %q", i, item.Title, item.DetailsURL)
		}
	}
	if items[0].Seeders == 0 {
		t.Errorf("first row seeders = 0, want the green counter")
	}
}
