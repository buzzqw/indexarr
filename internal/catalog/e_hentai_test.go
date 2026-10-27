package catalog_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/buzzqw/indexarr/internal/engine"
)

// TestEHentaiNativeFixture pins the gallery search captured from E-Hentai: the
// f_search parameter reaches the site, every row has a title and a gallery
// torrent page, and no row is invented from the layout cells.
//
// The grab itself is not exercised here because the .torrent lives on a second
// host (ehtracker.org) and the fixture keeps the absolute URL the page really
// serves; the live grab was checked on 2026-10-27: the ehtracker URL answered
// 200 application/x-bittorrent with a valid bencoded file.
func TestEHentaiNativeFixture(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		if got := r.URL.Query().Get("f_search"); got != "matrix" {
			t.Errorf("f_search = %q, want matrix", got)
		}
		serveFile(t, w, "e-hentai/search.html")
	}))
	t.Cleanup(server.Close)

	connector := nativeConnector(t, "e-hentai", server.URL)
	items, err := connector.Search(context.Background(), engine.SearchRequest{
		Mode: engine.ModeSearch, Query: "matrix", Limit: 10,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) != 10 {
		t.Fatalf("items = %d, want the 10 requested", len(items))
	}
	for i, item := range items {
		if strings.TrimSpace(item.Title) == "" {
			t.Errorf("item %d: empty title", i)
		}
		if !strings.Contains(item.DetailsURL, "gallerytorrents.php") {
			t.Errorf("item %d (%q): details = %q, want the gallery torrent page", i, item.Title, item.DetailsURL)
		}
		if item.Magnet != "" || item.DownloadURL != "" {
			t.Errorf("item %d (%q): magnet/download = %q/%q, the listing exposes neither",
				i, item.Title, item.Magnet, item.DownloadURL)
		}
	}
}
