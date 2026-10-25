package catalog_test

// Offline checks for the public sources added on 2026-10-25. Each test runs
// the real definition engine against the captured public page, including the
// detail page when the definition resolves the magnet or torrent there.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/buzzqw/indexarr/internal/engine"
)

func serveSearchOrDetail(t *testing.T, searchPath, searchFixture, detailFixture string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == searchPath {
			serveFile(t, w, searchFixture)
			return
		}
		serveFile(t, w, detailFixture)
	}))
	t.Cleanup(server.Close)
	return server
}

func TestPublicSourceFixtures(t *testing.T) {
	tests := []struct {
		id            string
		searchPath    string
		searchFixture string
		detailFixture string
		minRows       int
		needMagnet    bool
		needSize      bool
	}{
		{"torrentbyte", "/", "torrentbyte/search.html", "", 10, true, true},
		{"btdirectory", "/search.html", "btdirectory/search.html", "btdirectory/detail.html", 10, true, true},
		{"ebookbay", "/", "ebookbay/search.html", "ebookbay/detail.html", 4, false, true},
		{"sexypics", "/search/", "sexypics/search.html", "", 2, true, true},
		{"rintor", "/", "rintor/search.html", "rintor/detail.html", 1, true, true},
	}
	for _, tc := range tests {
		t.Run(tc.id, func(t *testing.T) {
			server := serveSearchOrDetail(t, tc.searchPath, tc.searchFixture, tc.detailFixture)
			connector := nativeConnector(t, tc.id, server.URL)
			items, err := connector.Search(context.Background(), engine.SearchRequest{
				Mode:  engine.ModeSearch,
				Query: "matrix",
			})
			if err != nil {
				t.Fatalf("Search: %v", err)
			}
			if len(items) < tc.minRows {
				t.Fatalf("items = %d, want at least %d", len(items), tc.minRows)
			}
			for i, item := range items {
				if item.Title == "" || item.DetailsURL == "" {
					t.Errorf("item %d: title=%q details=%q", i, item.Title, item.DetailsURL)
				}
				if tc.needMagnet && item.Magnet == "" && item.DownloadURL == "" {
					t.Errorf("item %d (%q): no magnet or torrent", i, item.Title)
				}
				if tc.needSize && item.Size <= 0 {
					t.Errorf("item %d (%q): size=%d", i, item.Title, item.Size)
				}
			}
		})
	}
}
