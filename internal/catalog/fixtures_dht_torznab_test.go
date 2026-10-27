package catalog_test

// Offline checks for the definitions for DHT search engines and Torznab sites. They run
// the real engine (definition interpreter or family connector) against the
// cleaned fixtures, so a selector or regex change that no longer matches the
// captured pages fails here instead of in production.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/buzzqw/indexarr/internal/engine"
	"github.com/buzzqw/indexarr/internal/family"
)

func TestBT4GFixture(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/search" {
			serveFile(t, w, "bt4g/search.rss")
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)

	connector := nativeConnector(t, "bt4g", server.URL)
	items, err := connector.Search(context.Background(), engine.SearchRequest{Mode: engine.ModeSearch, Query: "test"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) < 10 {
		t.Fatalf("items = %d, want at least 10", len(items))
	}
	for _, item := range items {
		if item.Title == "" || !strings.HasPrefix(item.Magnet, "magnet:?xt=urn:btih:") {
			t.Errorf("incomplete row: %+v", item)
		}
		if item.InfoHash == "" || item.Size <= 0 {
			t.Errorf("%q: infohash=%q size=%d", item.Title, item.InfoHash, item.Size)
		}
		if len(item.Categories) != 1 {
			t.Errorf("%q: categories = %v, want exactly one", item.Title, item.Categories)
		}
	}
}

func TestSolidTorrentsAndBitsearchFixtures(t *testing.T) {
	tests := []struct {
		id      string
		fixture string
	}{
		{"solidtorrents", "solidtorrents/listing.html"},
		{"bitsearch", "bitsearch/listing.html"},
	}
	for _, tc := range tests {
		t.Run(tc.id, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/search" {
					serveFile(t, w, tc.fixture)
					return
				}
				http.NotFound(w, r)
			}))
			t.Cleanup(server.Close)

			connector := nativeConnector(t, tc.id, server.URL)
			items, err := connector.Search(context.Background(), engine.SearchRequest{Mode: engine.ModeSearch, Query: "test"})
			if err != nil {
				t.Fatalf("Search: %v", err)
			}
			if len(items) < 10 {
				t.Fatalf("items = %d, want at least 10", len(items))
			}
			seen := map[int]bool{}
			for _, item := range items {
				if item.Title == "" || item.DetailsURL == "" {
					t.Errorf("incomplete row: %+v", item)
				}
				if item.Size <= 0 || item.Seeders <= 0 {
					t.Errorf("%q: size=%d seeders=%d", item.Title, item.Size, item.Seeders)
				}
				if !strings.HasPrefix(item.Magnet, "magnet:?xt=urn:btih:") || item.InfoHash == "" {
					t.Errorf("%q: missing magnet/infohash", item.Title)
				}
				if item.Published.IsZero() {
					t.Errorf("%q: date was not extracted", item.Title)
				}
				if len(item.Categories) != 1 {
					t.Errorf("%q: categories = %v", item.Title, item.Categories)
				}
				for _, category := range item.Categories {
					seen[category] = true
				}
			}
			// The fixture contains Other/Archive, Softwares/Windows and
			// Anime/Dub/Dual Audio, which exercise the label-to-id mapping.
			for _, want := range []int{8000, 4000, 5070} {
				if !seen[want] {
					t.Errorf("categories = %v, want %d", seen, want)
				}
			}
			first := items[0]
			payload, err := connector.Download(context.Background(), first)
			if err != nil {
				t.Fatalf("Download: %v", err)
			}
			if payload.Magnet == "" {
				t.Errorf("download returned no magnet")
			}
		})
	}
}

func TestBTDiggFixture(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/search" {
			serveFile(t, w, "btdig/listing.html")
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)

	connector := nativeConnector(t, "btdig", server.URL)
	items, err := connector.Search(context.Background(), engine.SearchRequest{Mode: engine.ModeSearch, Query: "test"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) < 10 {
		t.Fatalf("items = %d, want at least 10", len(items))
	}
	for _, item := range items {
		if item.Title == "" || item.DetailsURL == "" {
			t.Errorf("incomplete row: %+v", item)
		}
		if item.Size <= 0 {
			t.Errorf("%q: size was not extracted", item.Title)
		}
		if !strings.HasPrefix(item.Magnet, "magnet:?xt=urn:btih:") {
			t.Errorf("%q: magnet = %q", item.Title, item.Magnet)
		}
		if len(item.Categories) != 1 || item.Categories[0] != 8000 {
			t.Errorf("%q: categories = %v, want the Other fallback", item.Title, item.Categories)
		}
	}
}

func TestLinuxTrackerFixture(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/torrents/":
			serveFile(t, w, "linuxtracker/listing.html")
		case strings.HasPrefix(r.URL.Path, "/torrents/"):
			serveFile(t, w, "linuxtracker/detail.html")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	connector := nativeConnector(t, "linuxtracker", server.URL)
	items, err := connector.Search(context.Background(), engine.SearchRequest{Mode: engine.ModeSearch, Query: "test"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) < 10 {
		t.Fatalf("items = %d, want at least 10", len(items))
	}
	withSeeders := 0
	for _, item := range items {
		if item.Title == "" || item.DetailsURL == "" {
			t.Errorf("incomplete row: %+v", item)
		}
		if item.Size <= 0 {
			t.Errorf("%q: size was not extracted", item.Title)
		}
		if item.Seeders > 0 {
			withSeeders++
		}
		if item.Published.IsZero() {
			t.Errorf("%q: date was not extracted", item.Title)
		}
	}
	if withSeeders == 0 {
		t.Errorf("no row has a seeder count")
	}
	payload, err := connector.Download(context.Background(), items[0])
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if !strings.HasPrefix(payload.Magnet, "magnet:?xt=urn:btih:") {
		t.Errorf("magnet was not resolved from the detail page: %q", payload.Magnet)
	}
	// The listing exposes no per-row category and the table does not carry the
	// numeric distro id, so rows use the engine fallback.
	if got := items[0].Categories; len(got) != 1 || got[0] != 8000 {
		t.Errorf("first row category = %v, want the Other fallback", got)
	}
}

func TestAcademicTorrentsFixture(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serveFile(t, w, "academictorrents/feed.rss")
	}))
	t.Cleanup(server.Close)

	connector, err := family.NewRSS(family.RSSConfig{
		Indexer:         engine.IndexerRef{Slug: "academictorrents", Name: "Academic Torrents"},
		URL:             server.URL + "/feed.rss",
		DefaultCategory: "8000",
		Transport:       engine.NewTransport(engine.TransportConfig{AllowPrivate: true}),
	})
	if err != nil {
		t.Fatalf("NewRSS: %v", err)
	}
	// The feed has no server-side search: an empty query returns every item,
	// while the health query "test" only matches the newest entries.
	items, err := connector.Search(context.Background(), engine.SearchRequest{Mode: engine.ModeSearch})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) < 10 {
		t.Fatalf("items = %d, want at least 10", len(items))
	}
	for _, item := range items {
		if item.Title == "" || item.InfoHash == "" {
			t.Errorf("incomplete row: %+v", item)
		}
		if item.Size <= 0 {
			t.Errorf("%q: size was not extracted", item.Title)
		}
		if !strings.HasPrefix(item.Magnet, "magnet:?xt=urn:btih:") {
			t.Errorf("%q: magnet = %q", item.Title, item.Magnet)
		}
	}
}

func TestTorznabSiteFixtures(t *testing.T) {
	tests := []struct {
		id      string
		fixture string
	}{
		{"torlock", "torlock/search.xml"},
		{"torrentfunk", "torrentfunk/search.xml"},
	}
	for _, tc := range tests {
		t.Run(tc.id, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/torznab/api" {
					serveFile(t, w, tc.fixture)
					return
				}
				http.NotFound(w, r)
			}))
			t.Cleanup(server.Close)

			connector, err := family.NewTorznab(family.TorznabConfig{
				Indexer:   engine.IndexerRef{Slug: tc.id, Name: tc.id},
				BaseURL:   server.URL + "/torznab/api",
				Transport: engine.NewTransport(engine.TransportConfig{AllowPrivate: true}),
			})
			if err != nil {
				t.Fatalf("NewTorznab: %v", err)
			}
			items, err := connector.Search(context.Background(), engine.SearchRequest{Mode: engine.ModeSearch, Query: "test"})
			if err != nil {
				t.Fatalf("Search: %v", err)
			}
			if len(items) < 10 {
				t.Fatalf("items = %d, want at least 10", len(items))
			}
			for _, item := range items {
				if item.Title == "" || item.InfoHash == "" {
					t.Errorf("incomplete row: %+v", item)
				}
				if item.Size <= 0 || item.Seeders <= 0 {
					t.Errorf("%q: size=%d seeders=%d", item.Title, item.Size, item.Seeders)
				}
				if !strings.HasPrefix(item.Magnet, "magnet:?xt=urn:btih:") {
					t.Errorf("%q: magnet = %q", item.Title, item.Magnet)
				}
				if len(item.Categories) != 1 {
					t.Errorf("%q: categories = %v, want exactly one", item.Title, item.Categories)
				}
			}
			payload, err := connector.Download(context.Background(), items[0])
			if err != nil {
				t.Fatalf("Download: %v", err)
			}
			if payload.Magnet == "" {
				t.Errorf("download returned no magnet")
			}
		})
	}
}
