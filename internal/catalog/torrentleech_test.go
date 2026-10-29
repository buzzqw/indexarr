package catalog_test

// Offline check for the TorrentLeech definition. It runs the real engine on
// the captured JSON shape so a change in the browse response (or in the
// download precedence) fails here instead of in production.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/buzzqw/indexarr/internal/engine"
)

func TestTorrentLeechJSONFixture(t *testing.T) {
	torrent := []byte("torrent-bytes")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/torrents/browse/list/query/"):
			serveFile(t, w, "torrentleech/search.json")
		case r.URL.Path == "/torrent/241861196":
			serveFile(t, w, "torrentleech/detail.html")
		case r.URL.Path == "/download/9001/release-sintetica-uno.torrent":
			_, _ = w.Write(torrent)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	def := parseDefinition(t, "torrentleech")
	def.Meta.URLs[0] = server.URL + "/"
	def.Auth = nil // exercised by the auth phase, not by the listing fixture

	connector, err := engine.NewDefinitionConnector(engine.DefinitionConfig{
		Indexer:    engine.IndexerRef{Slug: "torrentleech", Name: "TorrentLeech"},
		Definition: def,
		Settings:   map[string]string{"username": "synthetic-user", "password": "synthetic-password"},
		Transport:  engine.NewTransport(engine.TransportConfig{AllowPrivate: true}),
	})
	if err != nil {
		t.Fatalf("NewDefinitionConnector: %v", err)
	}

	items, err := connector.Search(context.Background(), engine.SearchRequest{Mode: engine.ModeSearch, Query: "test"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("items = %d, want 3", len(items))
	}
	wantCategories := map[string]int{
		"Example Series S01E02 1080p WEB-DL H 264-GROUP": 5040,
		"Example Movie 2026 BlurayRip 1080p x264-GROUP":  2040,
		"Example Album 2026 FLAC":                        3000,
	}
	for _, item := range items {
		if item.Title == "" || item.DetailsURL == "" {
			t.Errorf("incomplete row: %+v", item)
		}
		if !strings.HasPrefix(item.DetailsURL, server.URL+"/torrent/") {
			t.Errorf("%q: details = %q", item.Title, item.DetailsURL)
		}
		if item.Size <= 0 {
			t.Errorf("%q: size was not extracted", item.Title)
		}
		if item.Seeders < 0 {
			t.Errorf("%q: seeders = %d", item.Title, item.Seeders)
		}
		if want, ok := wantCategories[item.Title]; ok {
			if len(item.Categories) != 1 || item.Categories[0] != want {
				t.Errorf("%q: categories = %v, want %d", item.Title, item.Categories, want)
			}
		}
	}

	// A grab must read the details page, never a DownloadURL carried by the
	// item: imported releases used to point at the local Prowlarr and the SSRF
	// guard refused the request.
	payload, err := connector.Download(context.Background(), engine.Item{
		Title:       items[0].Title,
		DetailsURL:  server.URL + "/torrent/241861196",
		DownloadURL: "http://192.168.1.10:9696/14/download",
	})
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if string(payload.Body) != string(torrent) {
		t.Errorf("body = %q, want %q", payload.Body, torrent)
	}
}

// On 2026-10-28 TorrentLeech dropped the session hours before refresh_after:
// the browse list answered the HTML login page ("invalid character '<'") and
// release pages came without the download button ("no download target") until
// a restart. auth.logged_in makes both log in again once and read again.
func TestTorrentLeechLogsInAgainWhenTheSiteDropsTheSession(t *testing.T) {
	torrent := []byte("torrent-bytes")
	var logins atomic.Int32
	dropped := atomic.Bool{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/":
			serveFile(t, w, "torrentleech/login.html")
		case r.URL.Path == "/user/account/login/":
			logins.Add(1)
			dropped.Store(false)
			serveFile(t, w, "torrentleech/login-ok.html")
		case dropped.Load():
			serveFile(t, w, "torrentleech/login.html")
		case strings.HasPrefix(r.URL.Path, "/torrents/browse/list/query/"):
			serveFile(t, w, "torrentleech/search.json")
		case r.URL.Path == "/torrent/241861196":
			serveFile(t, w, "torrentleech/detail.html")
		case r.URL.Path == "/download/9001/release-sintetica-uno.torrent":
			_, _ = w.Write(torrent)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	def := parseDefinition(t, "torrentleech")
	def.Meta.URLs[0] = server.URL + "/"
	connector, err := engine.NewDefinitionConnector(engine.DefinitionConfig{
		Indexer:    engine.IndexerRef{Slug: "torrentleech", Name: "TorrentLeech"},
		Definition: def,
		Settings:   map[string]string{"username": "synthetic-user", "password": "synthetic-password"},
		Transport:  engine.NewTransport(engine.TransportConfig{AllowPrivate: true}),
	})
	if err != nil {
		t.Fatalf("NewDefinitionConnector: %v", err)
	}
	if items, err := connector.Search(context.Background(), engine.SearchRequest{Mode: engine.ModeSearch, Query: "test"}); err != nil || len(items) != 3 {
		t.Fatalf("first search: %d items, err %v", len(items), err)
	}

	dropped.Store(true)
	if items, err := connector.Search(context.Background(), engine.SearchRequest{Mode: engine.ModeSearch, Query: "other"}); err != nil || len(items) != 3 {
		t.Fatalf("search after the drop: %d items, err %v", len(items), err)
	}
	dropped.Store(true)
	payload, err := connector.Download(context.Background(), engine.Item{Title: "x", DetailsURL: server.URL + "/torrent/241861196"})
	if err != nil {
		t.Fatalf("download after the drop: %v", err)
	}
	if string(payload.Body) != string(torrent) {
		t.Errorf("body = %q, want %q", payload.Body, torrent)
	}
	if logins.Load() != 3 {
		t.Errorf("logins = %d, want 3 (first, after the search drop, after the page drop)", logins.Load())
	}
}
