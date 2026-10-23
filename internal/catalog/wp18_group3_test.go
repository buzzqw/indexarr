package catalog_test

// Offline checks for the definitions added in WP18 phase 2, group 3. They run
// the real engine against the cleaned fixtures, so a selector change that no
// longer matches the captured pages fails here instead of in production.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/buzzqw/indexarr/internal/engine"
	"github.com/buzzqw/indexarr/internal/family"
)

func TestShanaProjectFixture(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/search/" {
			serveFile(t, w, "shanaproject/search.html")
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)

	connector := nativeConnector(t, "shanaproject", server.URL)
	items, err := connector.Search(context.Background(), engine.SearchRequest{Mode: engine.ModeSearch, Query: "test"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) < 3 {
		t.Fatalf("items = %d, want at least 3", len(items))
	}
	for _, item := range items {
		if item.Title == "" || item.Size <= 0 {
			t.Errorf("incomplete row: %+v", item)
		}
		if !strings.Contains(item.DownloadURL, "/download/") {
			t.Errorf("%q: download URL = %q", item.Title, item.DownloadURL)
		}
		if len(item.Categories) != 1 || item.Categories[0] != 5070 {
			t.Errorf("%q: categories = %v, want 5070", item.Title, item.Categories)
		}
	}
}

func TestAniRenaFixture(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/rss" {
			serveFile(t, w, "anirena/search.xml")
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)

	connector := nativeConnector(t, "anirena", server.URL)
	items, err := connector.Search(context.Background(), engine.SearchRequest{Mode: engine.ModeSearch, Query: "test"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) < 3 {
		t.Fatalf("items = %d, want at least 3", len(items))
	}
	for _, item := range items {
		if item.Title == "" || item.Size <= 0 {
			t.Errorf("incomplete row: %+v", item)
		}
		if !strings.HasSuffix(item.DownloadURL, ".torrent") {
			t.Errorf("%q: download URL = %q", item.Title, item.DownloadURL)
		}
	}
	if got := items[0].Categories; len(got) != 1 || got[0] != 5070 {
		t.Errorf("first row categories = %v, want 5070", got)
	}
}

func TestAudioBookBayFixture(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/":
			serveFile(t, w, "audiobookbay/search.html")
		case strings.HasPrefix(r.URL.Path, "/abss/"):
			serveFile(t, w, "audiobookbay/detail.html")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	connector := nativeConnector(t, "audiobookbay", server.URL)
	items, err := connector.Search(context.Background(), engine.SearchRequest{Mode: engine.ModeSearch, Query: "test"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) < 5 {
		t.Fatalf("items = %d, want at least 5", len(items))
	}
	for _, item := range items {
		if item.Title == "" || item.Size <= 0 || item.DetailsURL == "" {
			t.Errorf("incomplete row: %+v", item)
		}
		if !strings.HasPrefix(item.Magnet, "magnet:?xt=urn:btih:") {
			t.Errorf("%q: magnet = %q", item.Title, item.Magnet)
		}
		if len(item.Categories) != 1 || item.Categories[0] != 3030 {
			t.Errorf("%q: categories = %v, want 3030", item.Title, item.Categories)
		}
	}
	wonder := ""
	for _, item := range items {
		if strings.Contains(item.Title, "Wonder Test") {
			wonder = hashFromMagnet(item.Magnet)
			break
		}
	}
	if wonder != "7ec2c09c1feeb095a0202b6a64412b6d3c280965" {
		t.Errorf("Wonder Test infohash = %q", wonder)
	}
}

func TestInternetArchiveFixture(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/advancedsearch.php":
			serveFile(t, w, "internetarchive/search.json")
		case strings.HasPrefix(r.URL.Path, "/details/"):
			serveFile(t, w, "internetarchive/detail.html")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	connector := nativeConnector(t, "internetarchive", server.URL)
	items, err := connector.Search(context.Background(), engine.SearchRequest{Mode: engine.ModeSearch, Query: "test"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) < 3 {
		t.Fatalf("items = %d, want at least 3", len(items))
	}
	for _, item := range items {
		if item.Title == "" || item.Size <= 0 {
			t.Errorf("incomplete row: %+v", item)
		}
		if !strings.Contains(item.DetailsURL, "/details/") {
			t.Errorf("%q: details URL = %q", item.Title, item.DetailsURL)
		}
		if !strings.HasSuffix(item.DownloadURL, "_archive.torrent") {
			t.Errorf("%q: download URL = %q", item.Title, item.DownloadURL)
		}
		if len(item.Categories) != 1 {
			t.Errorf("%q: categories = %v, want exactly one", item.Title, item.Categories)
		}
	}
}

func TestEZTVFixture(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/feed.xml" {
			serveFile(t, w, "eztv/feed.xml")
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)

	connector, err := family.NewRSS(family.RSSConfig{
		Indexer:         engine.IndexerRef{Slug: "eztv", Name: "EZTV"},
		URL:             server.URL + "/feed.xml",
		DefaultCategory: "5000",
		Transport:       engine.NewTransport(engine.TransportConfig{AllowPrivate: true}),
	})
	if err != nil {
		t.Fatalf("NewRSS: %v", err)
	}
	items, err := connector.Search(context.Background(), engine.SearchRequest{Mode: engine.ModeSearch, Query: "a certain dark item"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) < 2 {
		t.Fatalf("items = %d, want at least 2", len(items))
	}
	for _, item := range items {
		if item.Title == "" || item.Size <= 0 {
			t.Errorf("incomplete row: %+v", item)
		}
		if item.DownloadURL == "" {
			t.Errorf("%q: no torrent enclosure", item.Title)
		}
		if len(item.Categories) != 1 || item.Categories[0] != 5000 {
			t.Errorf("%q: categories = %v, want 5000", item.Title, item.Categories)
		}
	}
}

func TestTorrent9Fixture(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/search_torrent/" && r.Method == http.MethodPost:
			serveFile(t, w, "torrent9/search.html")
		case strings.HasPrefix(r.URL.Path, "/torrent/"):
			serveFile(t, w, "torrent9/detail.html")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	connector := nativeConnector(t, "torrent9", server.URL)
	items, err := connector.Search(context.Background(), engine.SearchRequest{Mode: engine.ModeSearch, Query: "test"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) < 5 {
		t.Fatalf("items = %d, want at least 5", len(items))
	}
	for _, item := range items {
		if item.Title == "" || item.Size <= 0 || item.DetailsURL == "" {
			t.Errorf("incomplete row: %+v", item)
		}
		if !strings.HasPrefix(item.Magnet, "magnet:?xt=urn:btih:") {
			t.Errorf("%q: magnet = %q", item.Title, item.Magnet)
		}
	}
	if got := items[0].Categories; len(got) != 1 || got[0] != 5000 {
		t.Errorf("first row categories = %v, want 5000", got)
	}
	if items[0].Seeders <= 0 {
		t.Errorf("first row seeders = %d", items[0].Seeders)
	}
}

func TestLibriVoxFixture(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/feed/audiobooks/":
			serveFile(t, w, "librivox/search.json")
		case strings.HasPrefix(r.URL.Path, "/alices-adventures-in-wonderland-by-lewis-carroll/"):
			serveFile(t, w, "librivox/detail.html")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	connector := nativeConnector(t, "librivox", server.URL)
	items, err := connector.Search(context.Background(), engine.SearchRequest{Mode: engine.ModeSearch, Query: "Alice's Adventures in Wonderland"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1", len(items))
	}
	item := items[0]
	if item.Title != "Alice's Adventures in Wonderland" {
		t.Errorf("title = %q", item.Title)
	}
	if !strings.HasSuffix(item.DownloadURL, "_archive.torrent") {
		t.Errorf("download URL = %q", item.DownloadURL)
	}
	if len(item.Categories) != 1 || item.Categories[0] != 3030 {
		t.Errorf("categories = %v, want 3030", item.Categories)
	}
}

func TestFOSSTorrentsFixture(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/search/":
			serveFile(t, w, "fosstorrents/search.html")
		case r.URL.Path == "/thankyou/":
			serveFile(t, w, "fosstorrents/detail.html")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	connector := nativeConnector(t, "fosstorrents", server.URL)
	items, err := connector.Search(context.Background(), engine.SearchRequest{Mode: engine.ModeSearch, Query: "ubuntu"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) < 1 {
		t.Fatalf("items = %d, want at least 1", len(items))
	}
	for _, item := range items {
		if item.Title == "" || item.DetailsURL == "" {
			t.Errorf("incomplete row: %+v", item)
		}
		if !strings.HasSuffix(item.DownloadURL, ".torrent") {
			t.Errorf("%q: download URL = %q", item.Title, item.DownloadURL)
		}
		if len(item.Categories) != 1 || item.Categories[0] != 4000 {
			t.Errorf("%q: categories = %v, want 4000", item.Title, item.Categories)
		}
	}
}
