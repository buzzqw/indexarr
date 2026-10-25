package catalog_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/buzzqw/indexarr/internal/engine"
)

// TestPandaCDNativeFixture exercises the definition against the cleaned public
// pages: the search card list, the release detail with the format table, and
// the anonymous torrent endpoint. The detail fixture is served for every
// release link because it only needs to prove the size and torrent extraction.
func TestPandaCDNativeFixture(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		switch {
		case r.URL.Path == "/index.php" && query.Get("p") == "release":
			if query.Get("action") != "download" {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/x-bittorrent")
			_, _ = w.Write([]byte("d8:announce16:udp://tracker/e"))
		case r.URL.Path == "/index.php":
			if got := query.Get("search"); got != "matrix" {
				t.Errorf("search param = %q, want matrix", got)
			}
			serveFile(t, w, "pandacd/search.html")
		case strings.HasPrefix(r.URL.Path, "/release/"):
			serveFile(t, w, "pandacd/detail.html")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	connector := nativeConnector(t, "pandacd", server.URL)
	items, err := connector.Search(context.Background(), engine.SearchRequest{
		Mode: engine.ModeSearch, Query: "matrix", Limit: 10,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("items = %d, want 3", len(items))
	}
	first := items[0]
	if first.Title != "Valcard \u2013 Within the Matrix" {
		t.Errorf("title = %q", first.Title)
	}
	if first.Size != 42058383 {
		t.Errorf("size = %d, want 42058383 (40.11 MiB)", first.Size)
	}
	if !strings.Contains(first.DownloadURL, "action=download") {
		t.Errorf("download = %q, want the torrent endpoint", first.DownloadURL)
	}
	for _, item := range items {
		if item.Size <= 0 {
			t.Errorf("%q: size was not resolved from the detail page", item.Title)
		}
		if !strings.Contains(item.DownloadURL, "action=download") {
			t.Errorf("%q: torrent URL missing: %q", item.Title, item.DownloadURL)
		}
	}

	payload, err := connector.Download(context.Background(), first)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if len(payload.Body) == 0 {
		t.Error("Download returned an empty torrent body")
	}
	if !strings.Contains(payload.ContentType, "bittorrent") {
		t.Errorf("Content-Type = %q, want a torrent content type", payload.ContentType)
	}
}
