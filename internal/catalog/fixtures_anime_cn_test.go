package catalog_test

// Offline checks for three Chinese anime definitions (Mikan, Bangumi Moe, ACGNX).
// They run the real engine against the cleaned fixtures.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/buzzqw/indexarr/internal/engine"
)

func TestMikanFixture(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/Home/Search" {
			serveFile(t, w, "mikan/listing.html")
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)

	connector := nativeConnector(t, "mikan", server.URL)
	items, err := connector.Search(context.Background(), engine.SearchRequest{Mode: engine.ModeSearch, Query: "test"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) < 5 {
		t.Fatalf("items = %d, want at least 5", len(items))
	}
	for _, item := range items {
		if !strings.HasPrefix(item.Magnet, "magnet:?xt=urn:btih:") {
			t.Errorf("%q: magnet = %q", item.Title, item.Magnet)
		}
		if item.InfoHash == "" || item.Size <= 0 || item.DetailsURL == "" {
			t.Errorf("%q: infohash=%q size=%d details=%q", item.Title, item.InfoHash, item.Size, item.DetailsURL)
		}
		if len(item.Categories) != 1 || item.Categories[0] != 5070 {
			t.Errorf("%q: categories = %v, want 5070", item.Title, item.Categories)
		}
	}
	if items[0].Published.IsZero() {
		t.Error("first row has no parsed date")
	}
}

func TestBangumiMoeFixture(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/torrent/search" {
			serveFile(t, w, "bangumimoe/search.json")
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)

	connector := nativeConnector(t, "bangumimoe", server.URL)
	items, err := connector.Search(context.Background(), engine.SearchRequest{Mode: engine.ModeSearch, Query: "test"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) < 3 {
		t.Fatalf("items = %d, want at least 3", len(items))
	}
	for _, item := range items {
		if !strings.HasPrefix(item.Magnet, "magnet:?xt=urn:btih:") || len(item.InfoHash) != 40 {
			t.Errorf("%q: magnet=%q infohash=%q", item.Title, item.Magnet, item.InfoHash)
		}
		if item.Size <= 0 || item.DetailsURL == "" {
			t.Errorf("%q: size=%d details=%q", item.Title, item.Size, item.DetailsURL)
		}
		if len(item.Categories) != 1 || item.Categories[0] != 5070 {
			t.Errorf("%q: categories = %v, want 5070", item.Title, item.Categories)
		}
	}
	if items[0].Published.IsZero() {
		t.Error("first row has no parsed date")
	}
}

func TestACGNXFixture(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/rss.xml" {
			serveFile(t, w, "acgnx/search.xml")
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)

	def := parseDefinition(t, "acgnx")
	// ACGNX uses an absolute RSS URL on the live site; point that URL at the
	// fixture server so the test never leaves the process.
	def.Search.Request.Get = server.URL + "/rss.xml"
	connector := nativeConnectorFromDefinition(t, def, server.URL)
	items, err := connector.Search(context.Background(), engine.SearchRequest{Mode: engine.ModeSearch, Query: "test"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) < 5 {
		t.Fatalf("items = %d, want at least 5", len(items))
	}
	for _, item := range items {
		if !strings.HasPrefix(item.Magnet, "magnet:?xt=urn:btih:") || len(item.InfoHash) != 40 {
			t.Errorf("%q: magnet=%q infohash=%q", item.Title, item.Magnet, item.InfoHash)
		}
		if item.Size <= 0 || item.DetailsURL == "" {
			t.Errorf("%q: size=%d details=%q", item.Title, item.Size, item.DetailsURL)
		}
		if len(item.Categories) != 1 {
			t.Errorf("%q: categories = %v, want exactly one", item.Title, item.Categories)
		}
	}
	// ACGNX carries several categories; the first fixture item is 動畫.
	if first := items[0]; first.Categories[0] != 5070 {
		t.Errorf("first row categories = %v, want 5070 (Anime)", first.Categories)
	}
	if items[0].Published.IsZero() {
		t.Error("first row has no parsed date")
	}
}
