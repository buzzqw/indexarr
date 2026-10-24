package family

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/buzzqw/indexarr/internal/engine"
)

func readRSSFixture(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read testdata %s: %v", name, err)
	}
	return body
}

func rssTestTransport() *engine.Transport {
	return engine.NewTransport(engine.TransportConfig{AllowPrivate: true})
}

func newRSSServer(t *testing.T, path string, body []byte) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != path {
			http.NotFound(w, r)
			return
		}
		requests.Add(1)
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = w.Write(body)
	}))
	t.Cleanup(server.Close)
	return server, &requests
}

func newTestRSS(t *testing.T, server *httptest.Server, path string) *RSS {
	t.Helper()
	connector, err := NewRSS(RSSConfig{
		Indexer:         engine.IndexerRef{Slug: "feed"},
		URL:             server.URL + path,
		DefaultCategory: "5000",
		Transport:       rssTestTransport(),
	})
	if err != nil {
		t.Fatalf("NewRSS: %v", err)
	}
	return connector
}

func TestRSSLatestAndMapping(t *testing.T) {
	server, _ := newRSSServer(t, "/rss", readRSSFixture(t, "rss_feed.xml"))
	connector := newTestRSS(t, server, "/rss")

	items, err := connector.Latest(context.Background(), engine.LatestRequest{Limit: 2})
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("items = %d, want 2", len(items))
	}

	first := items[0]
	if first.Protocol != engine.ProtocolTorrent {
		t.Errorf("protocol = %q", first.Protocol)
	}
	if first.Title != "Ubuntu 24.04 Desktop amd64" || first.GUID != "feed-ubuntu-1" {
		t.Errorf("identity = %q / %q", first.Title, first.GUID)
	}
	if first.InfoHash != "0123456789abcdef0123456789abcdef01234567" {
		t.Errorf("infohash = %q", first.InfoHash)
	}
	if !strings.HasPrefix(first.Magnet, "magnet:?xt=urn:btih:") {
		t.Errorf("magnet = %q", first.Magnet)
	}
	if first.Size != 123456 || first.Seeders != 12 || first.Leechers != 20 {
		t.Errorf("size/seeders/leechers = %d/%d/%d", first.Size, first.Seeders, first.Leechers)
	}
	if len(first.Categories) != 1 || first.Categories[0] != 5000 {
		t.Errorf("categories = %v", first.Categories)
	}
	want := time.Date(2026, 10, 5, 10, 17, 44, 0, time.UTC)
	if !first.Published.Equal(want) {
		t.Errorf("published = %v, want %v", first.Published, want)
	}

	second := items[1]
	if second.Title != "Debian 12 netinst" || second.GUID != "feed-debian-2" {
		t.Errorf("second identity = %q / %q", second.Title, second.GUID)
	}
	if second.DownloadURL != "https://feed.example.invalid/download/2.torrent" {
		t.Errorf("torrent URL = %q", second.DownloadURL)
	}
	if second.Magnet != "" || second.InfoHash != "" {
		t.Errorf("unexpected magnet/infohash = %q / %q", second.Magnet, second.InfoHash)
	}
	if second.Size != 999 {
		t.Errorf("size = %d", second.Size)
	}
	// The feed item has no numeric category, so the configured default wins.
	if len(second.Categories) != 1 || second.Categories[0] != 5000 {
		t.Errorf("default category = %v", second.Categories)
	}

	all, err := connector.Latest(context.Background(), engine.LatestRequest{})
	if err != nil {
		t.Fatalf("Latest(all): %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("all items = %d, want 3", len(all))
	}
	third := all[2]
	if third.Categories[0] != 5000 {
		t.Errorf("text-only category should fall back to the default, got %v", third.Categories)
	}
	if third.DownloadURL != "" || third.DetailsURL != "https://feed.example.invalid/details/3" {
		t.Errorf("third URLs = download %q, details %q", third.DownloadURL, third.DetailsURL)
	}
}

func TestRSSAtom(t *testing.T) {
	server, _ := newRSSServer(t, "/atom", readRSSFixture(t, "rss_atom.xml"))
	connector := newTestRSS(t, server, "/atom")

	items, err := connector.Latest(context.Background(), engine.LatestRequest{})
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("items = %d, want 2", len(items))
	}
	if items[0].Title != "Atom Show S01E01 1080p" || items[0].GUID != "atom-show-1" {
		t.Errorf("first = %+v", items[0])
	}
	if items[0].DownloadURL != "https://feed.example.invalid/download/atom1.torrent" {
		t.Errorf("torrent URL = %q", items[0].DownloadURL)
	}
	if items[0].Size != 4242 {
		t.Errorf("size = %d", items[0].Size)
	}
	if len(items[0].Categories) != 1 || items[0].Categories[0] != 2000 {
		t.Errorf("categories = %v", items[0].Categories)
	}
	if !strings.HasPrefix(items[1].Magnet, "magnet:?xt=urn:btih:") {
		t.Errorf("second magnet = %q", items[1].Magnet)
	}
	if items[1].Published.IsZero() {
		t.Error("second published is zero")
	}
}

func TestRSSSearchFiltersClientSide(t *testing.T) {
	server, _ := newRSSServer(t, "/rss", readRSSFixture(t, "rss_feed.xml"))
	connector := newTestRSS(t, server, "/rss")
	ctx := context.Background()

	cases := []struct {
		name  string
		query string
		want  int
	}{
		{"exact", "ubuntu", 1},
		{"case insensitive and all keywords", "UBUNTU 24.04", 1},
		{"no match", "windows", 0},
		{"empty query", "", 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			items, err := connector.Search(ctx, engine.SearchRequest{Mode: engine.ModeSearch, Query: tc.query})
			if err != nil {
				t.Fatalf("Search(%q): %v", tc.query, err)
			}
			if len(items) != tc.want {
				t.Fatalf("Search(%q) = %d items, want %d", tc.query, len(items), tc.want)
			}
		})
	}

	limited, err := connector.Search(ctx, engine.SearchRequest{Query: "ubuntu", Limit: 1})
	if err != nil {
		t.Fatalf("Search(limit): %v", err)
	}
	if len(limited) != 1 {
		t.Fatalf("limited = %d", len(limited))
	}
	offset, err := connector.Search(ctx, engine.SearchRequest{Offset: 1, Limit: 1})
	if err != nil {
		t.Fatalf("Search(offset): %v", err)
	}
	if len(offset) != 1 || offset[0].Title != "Debian 12 netinst" {
		t.Fatalf("offset = %+v", offset)
	}

	if _, err := connector.Search(ctx, engine.SearchRequest{Mode: engine.ModeTV}); err == nil {
		t.Fatal("Search accepted an unsupported mode")
	}
}

func TestRSSConfiguredQueryIsAnAdditionalFilter(t *testing.T) {
	server, _ := newRSSServer(t, "/rss", readRSSFixture(t, "rss_feed.xml"))
	connector, err := NewRSS(RSSConfig{
		Indexer:   engine.IndexerRef{Slug: "feed"},
		URL:       server.URL + "/rss",
		Query:     "debian",
		Transport: rssTestTransport(),
	})
	if err != nil {
		t.Fatalf("NewRSS: %v", err)
	}

	items, err := connector.Latest(context.Background(), engine.LatestRequest{})
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if len(items) != 1 || items[0].Title != "Debian 12 netinst" {
		t.Fatalf("configured query items = %+v", items)
	}
	items, err = connector.Search(context.Background(), engine.SearchRequest{Query: "ubuntu"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("configured query did not narrow the search: %+v", items)
	}
}

func TestRSSCategoryFilter(t *testing.T) {
	server, _ := newRSSServer(t, "/rss", readRSSFixture(t, "rss_feed.xml"))
	connector := newTestRSS(t, server, "/rss")
	ctx := context.Background()

	items, err := connector.Search(ctx, engine.SearchRequest{
		Query: "ubuntu", Categories: []int{5040},
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("category filter returned %+v", items)
	}
	items, err = connector.Latest(ctx, engine.LatestRequest{Categories: []int{5000}, Limit: 1})
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if len(items) != 1 || items[0].Title != "Ubuntu 24.04 Desktop amd64" {
		t.Fatalf("Latest category = %+v", items)
	}
}

func TestRSSCacheTTL(t *testing.T) {
	server, requests := newRSSServer(t, "/rss", readRSSFixture(t, "rss_feed.xml"))
	connector := newTestRSS(t, server, "/rss")
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	connector.now = func() time.Time { return now }
	ctx := context.Background()

	if _, err := connector.Search(ctx, engine.SearchRequest{Query: "ubuntu"}); err != nil {
		t.Fatalf("first Search: %v", err)
	}
	if _, err := connector.Search(ctx, engine.SearchRequest{Query: "debian"}); err != nil {
		t.Fatalf("second Search: %v", err)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("requests = %d, want 1 (cached feed)", got)
	}

	now = now.Add(rssFeedTTL + time.Second)
	if _, err := connector.Search(ctx, engine.SearchRequest{Query: "ubuntu"}); err != nil {
		t.Fatalf("Search after TTL: %v", err)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("requests = %d, want 2 after TTL", got)
	}
}

func TestRSSFailedFetchIsNotCached(t *testing.T) {
	var fail atomic.Bool
	fail.Store(true)
	var requests atomic.Int64
	body := readRSSFixture(t, "rss_feed.xml")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if fail.Load() {
			http.Error(w, "temporary", http.StatusBadGateway)
			return
		}
		_, _ = w.Write(body)
	}))
	t.Cleanup(server.Close)
	connector := newTestRSS(t, server, "/rss")

	if _, err := connector.Search(context.Background(), engine.SearchRequest{}); err == nil {
		t.Fatal("Search succeeded while the feed was failing")
	}
	fail.Store(false)
	items, err := connector.Search(context.Background(), engine.SearchRequest{Query: "ubuntu"})
	if err != nil {
		t.Fatalf("Search after recovery: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %d", len(items))
	}
	if got := requests.Load(); got != 4 {
		t.Fatalf("requests = %d, want 4 (three attempts then success)", got)
	}
}

func TestRSSDownload(t *testing.T) {
	payload := []byte("d4:infod4:name4:testee")
	var redirects atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/download/2.torrent":
			w.Header().Set("Content-Type", "application/x-bittorrent")
			_, _ = w.Write(payload)
		case "/redirect":
			redirects.Add(1)
			http.Redirect(w, r, "magnet:?xt=urn:btih:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb&dn=Redirect", http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	connector, err := NewRSS(RSSConfig{
		Indexer:   engine.IndexerRef{Slug: "feed"},
		URL:       server.URL + "/rss",
		Transport: rssTestTransport(),
	})
	if err != nil {
		t.Fatalf("NewRSS: %v", err)
	}
	ctx := context.Background()

	magnet := "magnet:?xt=urn:btih:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa&dn=Test"
	got, err := connector.Download(ctx, engine.Item{Title: "Magnet", Magnet: magnet})
	if err != nil {
		t.Fatalf("Download(magnet): %v", err)
	}
	if got.Magnet != magnet || len(got.Body) != 0 {
		t.Fatalf("magnet payload = %+v", got)
	}

	got, err = connector.Download(ctx, engine.Item{
		Title:       "Direct",
		DownloadURL: server.URL + "/download/2.torrent",
	})
	if err != nil {
		t.Fatalf("Download(direct): %v", err)
	}
	if string(got.Body) != string(payload) {
		t.Errorf("body = %q", got.Body)
	}
	if got.ContentType != "application/x-bittorrent" {
		t.Errorf("content type = %q", got.ContentType)
	}
	if !strings.HasSuffix(got.Filename, ".torrent") {
		t.Errorf("filename = %q", got.Filename)
	}

	// A .torrent enclosure wins over a magnet reconstructed from the hash:
	// private trackers carry the passkey in the file.
	got, err = connector.Download(ctx, engine.Item{
		Title:       "Private",
		DownloadURL: server.URL + "/download/2.torrent",
		InfoHash:    "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Magnet:      magnet,
	})
	if err != nil {
		t.Fatalf("Download(private torrent): %v", err)
	}
	if string(got.Body) != string(payload) {
		t.Errorf("private torrent returned magnet/body = %q", got.Body)
	}

	got, err = connector.Download(ctx, engine.Item{
		Title:       "Redirect",
		DownloadURL: server.URL + "/redirect",
	})
	if err != nil {
		t.Fatalf("Download(redirect): %v", err)
	}
	if !strings.HasPrefix(got.Magnet, "magnet:?xt=urn:btih:bbbb") {
		t.Fatalf("redirect magnet = %q", got.Magnet)
	}
	if redirects.Load() != 1 {
		t.Fatalf("redirect requests = %d", redirects.Load())
	}

	if _, err := connector.Download(ctx, engine.Item{Title: "Missing"}); err == nil {
		t.Fatal("Download without a magnet or torrent URL succeeded")
	}
	if _, err := connector.Download(ctx, engine.Item{
		Title:      "Details only",
		DetailsURL: "https://feed.example.invalid/details/3",
	}); err == nil {
		t.Fatal("Download fetched a details page as a torrent")
	}
}

func TestRSSErrorsRedactFeedKeys(t *testing.T) {
	const secret = "SECRET-RSS-KEY-0123456789"
	connector, err := NewRSS(RSSConfig{
		Indexer:   engine.IndexerRef{Slug: "feed"},
		URL:       "http://127.0.0.1:1/rss/" + secret + "?rsskey=" + secret,
		Transport: engine.NewTransport(engine.TransportConfig{AllowPrivate: true}),
	})
	if err != nil {
		t.Fatalf("NewRSS: %v", err)
	}
	_, err = connector.Search(context.Background(), engine.SearchRequest{})
	if err == nil {
		t.Fatal("Search against a closed port succeeded")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error leaked the feed key: %v", err)
	}

	result, err := connector.Test(context.Background())
	if err != nil {
		t.Fatalf("Test returned an error: %v", err)
	}
	if result.OK {
		t.Fatal("Test against a closed port succeeded")
	}
	for _, phase := range result.Phases {
		if strings.Contains(phase.Message, secret) {
			t.Fatalf("Test phase %q leaked the feed key: %s", phase.Name, phase.Message)
		}
	}
}

func TestRSSTestPhases(t *testing.T) {
	server, _ := newRSSServer(t, "/rss", readRSSFixture(t, "rss_feed.xml"))
	connector := newTestRSS(t, server, "/rss")

	result, err := connector.Test(context.Background())
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	if !result.OK {
		t.Fatalf("Test not OK: %+v", result.Phases)
	}
	if len(result.Phases) != 3 {
		t.Fatalf("phases = %+v", result.Phases)
	}
	for i, name := range []string{"reach", "parse", "items"} {
		if result.Phases[i].Name != name || !result.Phases[i].OK {
			t.Errorf("phase %d = %+v, want %q OK", i, result.Phases[i], name)
		}
	}

	empty := []byte(`<?xml version="1.0"?><rss version="2.0"><channel><title>Empty</title></channel></rss>`)
	emptyServer, _ := newRSSServer(t, "/rss", empty)
	emptyConnector := newTestRSS(t, emptyServer, "/rss")
	result, err = emptyConnector.Test(context.Background())
	if err != nil {
		t.Fatalf("Test(empty): %v", err)
	}
	if result.OK {
		t.Fatal("Test accepted an empty feed")
	}
	itemsPhase := result.Phases[len(result.Phases)-1]
	if itemsPhase.Name != "items" || itemsPhase.OK || !strings.Contains(itemsPhase.Message, "at least") {
		t.Errorf("items phase = %+v", itemsPhase)
	}
}

func TestRSSCapabilities(t *testing.T) {
	connector, err := NewRSS(RSSConfig{
		Indexer:         engine.IndexerRef{Slug: "feed"},
		URL:             "https://feed.example/rss",
		DefaultCategory: "5000",
		Categories:      []int{2000, 5000},
	})
	if err != nil {
		t.Fatalf("NewRSS: %v", err)
	}
	caps := connector.Capabilities()
	if caps.Protocol != engine.ProtocolTorrent {
		t.Errorf("protocol = %q", caps.Protocol)
	}
	if !caps.Supports(engine.ModeSearch) || !caps.SupportsParam(engine.ModeSearch, engine.ParamQuery) {
		t.Errorf("caps = %+v", caps)
	}
	if len(caps.Categories) != 2 || caps.Categories[0] != 5000 || caps.Categories[1] != 2000 {
		t.Errorf("categories = %v", caps.Categories)
	}
}

func TestNewRSSValidation(t *testing.T) {
	if _, err := NewRSS(RSSConfig{}); err == nil {
		t.Fatal("NewRSS accepted an empty URL")
	}
	if _, err := NewRSS(RSSConfig{URL: "ftp://feed.example/rss"}); err == nil {
		t.Fatal("NewRSS accepted a non-HTTP URL")
	}
	if _, err := NewRSS(RSSConfig{URL: "https://feed.example/rss", DefaultCategory: "tv"}); err == nil {
		t.Fatal("NewRSS accepted a non-numeric default category")
	}
}

func TestRSSFactory(t *testing.T) {
	registry := engine.NewRegistry()
	if err := Register(registry); err != nil {
		t.Fatalf("Register: %v", err)
	}
	connector, err := registry.NewConnector(ConnectorRSS, engine.Config{
		Indexer: engine.IndexerRef{Slug: "feed"},
		Settings: map[string]string{
			"url":              "https://feed.example/rss",
			"default_category": "5040",
			"query":            "ubuntu",
			"user_agent":       "family-test",
			"categories":       "5000, 5040",
		},
	})
	if err != nil {
		t.Fatalf("NewConnector(rss): %v", err)
	}
	rss, ok := connector.(*RSS)
	if !ok {
		t.Fatalf("connector = %T, want *RSS", connector)
	}
	caps := rss.Capabilities()
	if len(caps.Categories) != 2 || caps.Categories[0] != 5040 || caps.Categories[1] != 5000 {
		t.Errorf("categories = %v", caps.Categories)
	}
	if rss.query != "ubuntu" {
		t.Errorf("query = %q", rss.query)
	}

	if _, err := registry.NewConnector(ConnectorRSS, engine.Config{
		Settings: map[string]string{"url": "https://feed.example/rss", "default_category": "tv"},
	}); err == nil {
		t.Fatal("factory accepted an invalid default category")
	}
	if _, err := registry.NewConnector(ConnectorRSS, engine.Config{
		Settings: map[string]string{"default_category": "5000"},
	}); err == nil {
		t.Fatal("factory accepted a missing URL")
	}
}

func TestRSSFeedRetriesThreeTimes(t *testing.T) {
	var requests atomic.Int64
	body := readRSSFixture(t, "rss_feed.xml")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) < 3 {
			http.Error(w, "temporary", http.StatusBadGateway)
			return
		}
		_, _ = w.Write(body)
	}))
	t.Cleanup(server.Close)
	connector := newTestRSS(t, server, "/rss")
	if _, err := connector.Search(context.Background(), engine.SearchRequest{Query: "ubuntu"}); err != nil {
		t.Fatalf("Search with two transient errors: %v", err)
	}
	if got := requests.Load(); got != 3 {
		t.Fatalf("requests = %d, want 3", got)
	}
}
