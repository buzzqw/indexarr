package catalog_test

// Offline checks for the definitions for the classic public trackers. They run the real
// engine against the cleaned fixtures, so a selector change that no longer
// matches the captured pages fails here instead of in production.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/buzzqw/indexarr/internal/engine"
)

func TestTorrentGalaxyCloneFixture(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/get-posts/keywords:"):
			serveFile(t, w, "torrentgalaxyclone/listing.html")
		case strings.HasPrefix(r.URL.Path, "/post-detail/"):
			serveFile(t, w, "torrentgalaxyclone/detail.html")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	connector := nativeConnector(t, "torrentgalaxyclone", server.URL)
	items, err := connector.Search(context.Background(), engine.SearchRequest{Mode: engine.ModeSearch, Query: "test"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) < 5 {
		t.Fatalf("items = %d, want at least 5", len(items))
	}
	for _, item := range items {
		if item.Title == "" || item.DetailsURL == "" {
			t.Errorf("incomplete row: %+v", item)
		}
		if item.Size <= 0 {
			t.Errorf("%q: size was not extracted", item.Title)
		}
		if len(item.Categories) != 1 {
			t.Errorf("%q: categories = %v, want exactly one", item.Title, item.Categories)
		}
		if strings.Contains(item.Title, "TEST 2025 1080p WEB DL") && item.Categories[0] != 2000 {
			t.Errorf("%q: category = %v, want 2000 (Movies)", item.Title, item.Categories)
		}
	}
	payload, err := connector.Download(context.Background(), engine.Item{
		Title:      items[0].Title,
		DetailsURL: items[0].DetailsURL,
	})
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if !strings.HasPrefix(payload.Magnet, "magnet:?xt=urn:btih:") {
		t.Errorf("magnet = %q", payload.Magnet)
	}
}

func TestLimeTorrentsFixture(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/search/all/"):
			serveFile(t, w, "limetorrents/listing.html")
		case strings.HasSuffix(r.URL.Path, "-torrent-19927869.html"):
			serveFile(t, w, "limetorrents/detail.html")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	connector := nativeConnector(t, "limetorrents", server.URL)
	items, err := connector.Search(context.Background(), engine.SearchRequest{Mode: engine.ModeSearch, Query: "test"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) < 5 {
		t.Fatalf("items = %d, want at least 5", len(items))
	}
	first := items[0]
	if first.Size <= 0 || first.Seeders <= 0 {
		t.Errorf("first row: size=%d seeders=%d", first.Size, first.Seeders)
	}
	if len(first.Categories) != 1 || first.Categories[0] != 8000 {
		t.Errorf("first row category = %v, want the Other fallback", first.Categories)
	}
	payload, err := connector.Download(context.Background(), engine.Item{
		Title:      first.Title,
		DetailsURL: first.DetailsURL,
	})
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if got := hashFromMagnet(payload.Magnet); got != "cd300e0cd2823088d925e66b9a11ff6e1f529b32" {
		t.Errorf("detail magnet hash = %q", got)
	}
}

func TestThePirateBayFixture(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/q.php" {
			serveFile(t, w, "thepiratebay/search.json")
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)

	connector := nativeConnector(t, "thepiratebay", server.URL)
	items, err := connector.Search(context.Background(), engine.SearchRequest{Mode: engine.ModeSearch, Query: "test"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) < 10 {
		t.Fatalf("items = %d, want at least 10", len(items))
	}
	first := items[0]
	if first.Magnet == "" || first.InfoHash == "" {
		t.Errorf("first row has no magnet/infohash: %+v", first)
	}
	if first.Size <= 0 || first.Seeders <= 0 {
		t.Errorf("first row: size=%d seeders=%d", first.Size, first.Seeders)
	}
	if len(first.Categories) != 1 || first.Categories[0] != 5040 {
		t.Errorf("first row category = %v, want 5040", first.Categories)
	}
	if !strings.Contains(first.DetailsURL, "thepiratebay.org/description.php?id=") {
		t.Errorf("details URL = %q", first.DetailsURL)
	}
}

func TestTorrentsCSVFixture(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/service/search" {
			serveFile(t, w, "torrentscsv/search.json")
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)

	connector := nativeConnector(t, "torrentscsv", server.URL)
	items, err := connector.Search(context.Background(), engine.SearchRequest{Mode: engine.ModeSearch, Query: "test"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) < 10 {
		t.Fatalf("items = %d, want at least 10", len(items))
	}
	for _, item := range items {
		if item.Magnet == "" || item.InfoHash == "" {
			t.Errorf("%q: missing magnet/infohash", item.Title)
		}
		if item.Size <= 0 {
			t.Errorf("%q: size was not extracted", item.Title)
		}
	}
}
