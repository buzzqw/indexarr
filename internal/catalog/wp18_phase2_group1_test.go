package catalog_test

// Offline checks for the definitions added in WP18 phase 2, group 1. They run
// the real engine (or the native RSS family) against the cleaned fixtures, so
// a selector or parser change that no longer matches the captured pages fails
// here instead of in production.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/buzzqw/indexarr/internal/engine"
	"github.com/buzzqw/indexarr/internal/family"
)

func rssConnector(t *testing.T, id, feedURL string) engine.Connector {
	t.Helper()
	def := parseDefinition(t, id)
	registry := engine.NewRegistry()
	if err := family.Register(registry); err != nil {
		t.Fatalf("family.Register: %v", err)
	}
	settings := map[string]string{"url": feedURL}
	for _, setting := range def.Settings {
		if _, set := settings[setting.Name]; !set && setting.Default != "" {
			settings[setting.Name] = setting.Default
		}
	}
	connector, err := registry.NewConnector(def.Connector, engine.Config{
		Indexer:   engine.IndexerRef{Slug: def.Meta.ID, Name: def.Meta.Name},
		Settings:  settings,
		Transport: engine.NewTransport(engine.TransportConfig{AllowPrivate: true}),
	})
	if err != nil {
		t.Fatalf("NewConnector(%s): %v", id, err)
	}
	return connector
}

func TestNyaaFixture(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			serveFile(t, w, "nyaa/listing.html")
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)

	connector := nativeConnector(t, "nyaa", server.URL)
	items, err := connector.Search(context.Background(), engine.SearchRequest{Mode: engine.ModeSearch, Query: "test"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) < 5 {
		t.Fatalf("items = %d, want at least 5", len(items))
	}
	first := items[0]
	if first.Title == "" || !strings.HasPrefix(first.Magnet, "magnet:?xt=urn:btih:") {
		t.Errorf("first row: title=%q magnet=%q", first.Title, first.Magnet)
	}
	if first.InfoHash == "" || first.Size <= 0 || first.Seeders <= 0 {
		t.Errorf("first row: infohash=%q size=%d seeders=%d", first.InfoHash, first.Size, first.Seeders)
	}
	if len(first.Categories) != 1 || first.Categories[0] != 5070 {
		t.Errorf("first row categories = %v, want 5070", first.Categories)
	}
	if first.Published.IsZero() {
		t.Error("first row has no parsed date")
	}
}

func TestAnimeToshoFixture(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/search" {
			serveFile(t, w, "animetosho/listing.html")
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)

	connector := nativeConnector(t, "animetosho", server.URL)
	items, err := connector.Search(context.Background(), engine.SearchRequest{Mode: engine.ModeSearch, Query: "test"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) < 5 {
		t.Fatalf("items = %d, want at least 5", len(items))
	}
	for _, item := range items {
		if item.Title == "" || !strings.HasPrefix(item.Magnet, "magnet:?xt=urn:btih:") {
			t.Errorf("incomplete row: %+v", item)
		}
		if item.Size <= 0 {
			t.Errorf("%q: size was not extracted", item.Title)
		}
		if len(item.Categories) != 1 || item.Categories[0] != 5070 {
			t.Errorf("%q: categories = %v, want 5070", item.Title, item.Categories)
		}
	}
	if items[0].Seeders <= 0 {
		t.Errorf("first row seeders = %d", items[0].Seeders)
	}
}

func TestTokyoToshoRSSFixture(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("definitions", "fixtures", "tokyotosho", "feed.xml"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	}))
	t.Cleanup(server.Close)

	connector := rssConnector(t, "tokyotosho", server.URL+"/tokyotosho.rss")
	items, err := connector.Latest(context.Background(), engine.LatestRequest{})
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if len(items) < 5 {
		t.Fatalf("items = %d, want at least 5", len(items))
	}
	for _, item := range items {
		if item.DownloadURL == "" && item.Magnet == "" {
			t.Errorf("%q: no download link", item.Title)
		}
		if len(item.Categories) != 1 || item.Categories[0] != 5070 {
			t.Errorf("%q: categories = %v, want 5070", item.Title, item.Categories)
		}
	}
	filtered, err := connector.Search(context.Background(), engine.SearchRequest{Mode: engine.ModeSearch, Query: "1080p"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(filtered) == 0 {
		t.Fatal("client-side title filter returned no rows")
	}
	for _, item := range filtered {
		if !strings.Contains(strings.ToLower(item.Title), "1080p") {
			t.Errorf("Search(1080p) returned %q", item.Title)
		}
	}
}

func TestSubsPleaseRSSFixture(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("definitions", "fixtures", "subsplease", "feed.xml"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	}))
	t.Cleanup(server.Close)

	connector := rssConnector(t, "subsplease", server.URL+"/subsplease.rss")
	items, err := connector.Latest(context.Background(), engine.LatestRequest{})
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if len(items) < 5 {
		t.Fatalf("items = %d, want at least 5", len(items))
	}
	for _, item := range items {
		if !strings.HasPrefix(item.Magnet, "magnet:?xt=urn:btih:") || item.InfoHash == "" {
			t.Errorf("%q: magnet=%q infohash=%q", item.Title, item.Magnet, item.InfoHash)
		}
		if len(item.Categories) != 1 || item.Categories[0] != 5070 {
			t.Errorf("%q: categories = %v, want 5070", item.Title, item.Categories)
		}
	}
	filtered, err := connector.Search(context.Background(), engine.SearchRequest{Mode: engine.ModeSearch, Query: "1080p"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(filtered) == 0 {
		t.Fatal("client-side title filter returned no rows")
	}
}

func TestYTSFixture(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/list_movies.json" {
			serveFile(t, w, "yts/search.json")
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)

	connector := nativeConnector(t, "yts", server.URL)
	items, err := connector.Search(context.Background(), engine.SearchRequest{Mode: engine.ModeSearch, Query: "test"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) < 5 {
		t.Fatalf("items = %d, want at least 5", len(items))
	}
	for _, item := range items {
		if !strings.HasPrefix(item.Magnet, "magnet:?xt=urn:btih:") || item.InfoHash == "" {
			t.Errorf("%q: magnet=%q infohash=%q", item.Title, item.Magnet, item.InfoHash)
		}
		if item.Size <= 0 {
			t.Errorf("%q: size was not extracted", item.Title)
		}
		if len(item.Categories) != 1 || item.Categories[0] != 2000 {
			t.Errorf("%q: categories = %v, want 2000", item.Title, item.Categories)
		}
	}
	if items[0].Seeders <= 0 {
		t.Errorf("first row seeders = %d", items[0].Seeders)
	}
	if items[0].Published.IsZero() {
		t.Error("first row has no parsed date")
	}
}

func TestKickassFixture(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/usearch/" {
			serveFile(t, w, "kickasstorrents/listing.html")
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)

	connector := nativeConnector(t, "kickasstorrents", server.URL)
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
		if len(item.InfoHash) != 40 {
			t.Errorf("%q: infohash = %q", item.Title, item.InfoHash)
		}
		if item.Size <= 0 {
			t.Errorf("%q: size was not extracted", item.Title)
		}
		if item.Seeders <= 0 {
			t.Errorf("%q: seeders were not extracted", item.Title)
		}
	}
	if first := items[0]; len(first.Categories) != 1 || first.Categories[0] != 7000 {
		t.Errorf("first row categories = %v, want 7000 (Books)", first.Categories)
	}
}

func Test1337xFixture(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/search/":
			serveFile(t, w, "1337x/listing.html")
		case strings.HasPrefix(r.URL.Path, "/torrent/"):
			serveFile(t, w, "1337x/detail.html")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	connector := nativeConnector(t, "1337x", server.URL)
	items, err := connector.Search(context.Background(), engine.SearchRequest{Mode: engine.ModeSearch, Query: "test", Limit: 5})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) < 5 {
		t.Fatalf("items = %d, want at least 5", len(items))
	}
	for _, item := range items {
		if item.DetailsURL == "" {
			t.Errorf("%q: no details URL", item.Title)
		}
		if strings.Contains(item.Title, "...") || strings.Contains(item.Title, "-") {
			t.Errorf("%q: title was not rebuilt from the URL slug", item.Title)
		}
		if item.Size <= 0 || item.Seeders <= 0 {
			t.Errorf("%q: size=%d seeders=%d", item.Title, item.Size, item.Seeders)
		}
	}
	if first := items[0]; len(first.Categories) != 1 || first.Categories[0] != 6000 {
		t.Errorf("first row categories = %v, want 6000 (XXX)", first.Categories)
	}
	payload, err := connector.Download(context.Background(), engine.Item{
		Title:      items[0].Title,
		DetailsURL: items[0].DetailsURL,
	})
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if !strings.HasPrefix(payload.Magnet, "magnet:?xt=urn:btih:") {
		t.Errorf("detail magnet = %q", payload.Magnet)
	}
}

func TestTorrentDownloadsXMLFixture(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/rss.xml" {
			serveFile(t, w, "torrentdownloads/search.xml")
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)

	connector := nativeConnector(t, "torrentdownloads", server.URL)
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
		if item.Size <= 0 || item.Seeders <= 0 {
			t.Errorf("%q: size=%d seeders=%d", item.Title, item.Size, item.Seeders)
		}
		if item.Published.IsZero() {
			t.Errorf("%q: date was not parsed", item.Title)
		}
	}
	if first := items[0]; first.Size != 15350939 {
		t.Errorf("first row size = %d, want 15350939", first.Size)
	}
}

// TestTorrentDownloadsFixtureDeclaresLatin1 guards the fixture itself: the
// real feed declares ISO-8859-1, and the definition relies on the engine's
// charset reader. If the fixture were re-saved as UTF-8 this test would stop
// covering that path.
func TestTorrentDownloadsFixtureDeclaresLatin1(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("definitions", "fixtures", "torrentdownloads", "search.xml"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	if !strings.Contains(string(body[:80]), "iso-8859-1") {
		t.Fatalf("fixture no longer declares ISO-8859-1: %q", body[:80])
	}
}
