package catalog_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/buzzqw/indexarr/internal/engine"
)

// TestTorrentByNativeFixture pins the torrent.by search: the query reaches
// /search/, both row classes are read, and every row carries a magnet, a size
// and the release link. The definition sets meta.insecure_tls because the site
// serves an incomplete chain; the transport part is covered in the engine
// package, here only the extraction is checked.
func TestTorrentByNativeFixture(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search/" {
			http.NotFound(w, r)
			return
		}
		if got := r.URL.Query().Get("search"); got != "1080p" {
			t.Errorf("search = %q, want 1080p", got)
		}
		serveFile(t, w, "torrentby/search.html")
	}))
	t.Cleanup(server.Close)

	connector := nativeConnector(t, "torrentby", server.URL)
	items, err := connector.Search(context.Background(), engine.SearchRequest{
		Mode: engine.ModeSearch, Query: "1080p", Limit: 50,
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
		if !strings.HasPrefix(item.DetailsURL, server.URL+"/") {
			t.Errorf("item %d (%q): details = %q", i, item.Title, item.DetailsURL)
		}
	}
	withSeeders := 0
	for _, item := range items {
		if item.Seeders > 0 {
			withSeeders++
		}
	}
	if withSeeders == 0 {
		t.Error("no row has a seeder count")
	}
}
