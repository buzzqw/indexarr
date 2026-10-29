package catalog_test

// Offline check for the IPTorrents definition. The site cannot be reached
// without a browser session (the login form has a Cloudflare Turnstile), so
// the fixture keeps the search shape published by Jackett's IPTorrents
// indexer: table#torrents, a.hv titles, /download.php/ links, the ?<id>
// category icon and span.free for a freeleech release. The test also pins the
// pasted cookie and the User-Agent the site ties the session to.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/buzzqw/indexarr/internal/engine"
)

const (
	iptSyntheticCookie = "uid=synthetic; pass=synthetic-session"
	iptSyntheticAgent  = "SyntheticBrowser/1.0"
)

func newIPTorrentsConnector(t *testing.T, serverURL string, extra map[string]string) *engine.DefinitionConnector {
	t.Helper()
	def := parseDefinition(t, "iptorrents")
	def.Meta.URLs[0] = serverURL + "/"
	settings := map[string]string{
		"cookie":     iptSyntheticCookie,
		"user_agent": iptSyntheticAgent,
	}
	for key, value := range extra {
		settings[key] = value
	}
	connector, err := engine.NewDefinitionConnector(engine.DefinitionConfig{
		Indexer:    engine.IndexerRef{Slug: "iptorrents", Name: "IPTorrents"},
		Definition: def,
		Settings:   settings,
		Transport:  engine.NewTransport(engine.TransportConfig{AllowPrivate: true}),
	})
	if err != nil {
		t.Fatalf("NewDefinitionConnector: %v", err)
	}
	return connector
}

func TestIPTorrentsCookieSearchAndFreeleech(t *testing.T) {
	var cookieOK, agentOK, freeParam, freePresent atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/t":
			cookieOK.Store(r.Header.Get("Cookie") == iptSyntheticCookie)
			agentOK.Store(r.Header.Get("User-Agent") == iptSyntheticAgent)
			freePresent.Store(r.URL.Query().Has("free"))
			freeParam.Store(r.URL.Query().Get("free") == "on")
			if r.URL.Query().Get("q") != "synthetic" {
				t.Errorf("q = %q", r.URL.Query().Get("q"))
			}
			serveFile(t, w, "iptorrents/search.html")
		case strings.HasPrefix(r.URL.Path, "/download.php/"):
			cookieOK.Store(r.Header.Get("Cookie") == iptSyntheticCookie)
			agentOK.Store(r.Header.Get("User-Agent") == iptSyntheticAgent)
			_, _ = w.Write([]byte("torrent-bytes"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	connector := newIPTorrentsConnector(t, server.URL, map[string]string{"freeleech": "false"})
	items, err := connector.Search(context.Background(), engine.SearchRequest{Mode: engine.ModeSearch, Query: "synthetic"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("items = %d, want 3", len(items))
	}
	if !cookieOK.Load() || !agentOK.Load() {
		t.Errorf("cookie = %v, user agent = %v", cookieOK.Load(), agentOK.Load())
	}
	if freePresent.Load() {
		t.Error("free parameter sent with the option off")
	}
	if freeParam.Load() {
		t.Error("free=on sent with the option off")
	}

	wantCategories := map[string]int{
		"Synthetic.Show.S01E01.1080p":       5045,
		"Synthetic.Movie.2026.1080p.BluRay": 2040,
		"Synthetic.Show.S01E02.720p":        5045,
	}
	freeRows := 0
	for _, item := range items {
		if item.DetailsURL == "" || item.DownloadURL == "" {
			t.Errorf("%q: incomplete row", item.Title)
		}
		if item.Size <= 0 || item.Seeders <= 0 {
			t.Errorf("%q: size = %d, seeders = %d", item.Title, item.Size, item.Seeders)
		}
		if want, ok := wantCategories[item.Title]; ok {
			if len(item.Categories) != 1 || item.Categories[0] != want {
				t.Errorf("%q: categories = %v, want %d", item.Title, item.Categories, want)
			}
		}
		if item.Freeleech {
			freeRows++
		}
	}
	if freeRows != 2 {
		t.Errorf("freeleech rows = %d, want 2", freeRows)
	}

	// The option on sends free=on and drops the one paid row the fixture
	// returns on purpose.
	onlyFree := newIPTorrentsConnector(t, server.URL, map[string]string{"freeleech": "true"})
	freeItems, err := onlyFree.Search(context.Background(), engine.SearchRequest{Mode: engine.ModeSearch, Query: "synthetic"})
	if err != nil {
		t.Fatalf("Search freeleech only: %v", err)
	}
	if !freeParam.Load() {
		t.Error("free=on not sent with the option on")
	}
	if len(freeItems) != 2 {
		t.Fatalf("freeleech items = %d, want 2", len(freeItems))
	}
	for _, item := range freeItems {
		if !item.Freeleech {
			t.Errorf("%q is not freeleech", item.Title)
		}
	}

	payload, err := connector.Download(context.Background(), engine.Item{
		Title:       items[0].Title,
		DownloadURL: items[0].DownloadURL,
	})
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if string(payload.Body) != "torrent-bytes" {
		t.Errorf("body = %q", payload.Body)
	}
}
