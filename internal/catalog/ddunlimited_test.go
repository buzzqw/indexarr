package catalog_test

// Offline checks for DDUnlimited. The search fixture is a sanitized capture of
// the phpBB search results, the topic fixture is synthetic (valid MD4 hashes,
// no real links) and contains three files so the expansion path is exercised
// end to end: one topic must become three eD2k releases.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/buzzqw/indexarr/internal/engine"
)

func TestDDUnlimitedFixtures(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ucp.php":
			if r.Method == http.MethodPost {
				serveFile(t, w, "ddunlimited/login-ok.html")
				return
			}
			serveFile(t, w, "ddunlimited/login.html")
		case "/search.php":
			serveFile(t, w, "ddunlimited/search.html")
		case "/viewtopic.php":
			serveFile(t, w, "ddunlimited/topic.html")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	def := parseDefinition(t, "ddunlimited")
	// The fixture server speaks plain HTTP: the FlareSolverr session is not
	// part of this offline extraction test.
	def.Session = nil
	def.Meta.URLs[0] = server.URL + "/"
	connector, err := engine.NewDefinitionConnector(engine.DefinitionConfig{
		Indexer:    engine.IndexerRef{Slug: def.Meta.ID, Name: def.Meta.Name},
		Definition: def,
		Settings:   map[string]string{"username": "synthetic-user", "password": "synthetic-password"},
		Transport:  engine.NewTransport(engine.TransportConfig{AllowPrivate: true}),
	})
	if err != nil {
		t.Fatalf("NewDefinitionConnector: %v", err)
	}
	items, err := connector.Search(context.Background(), engine.SearchRequest{Mode: engine.ModeSearch, Query: "bones", Limit: 1})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("items = %d, want 3", len(items))
	}
	wantHashes := []string{
		"0123456789abcdef0123456789abcdef",
		"11111111111111111111111111111111",
		"22222222222222222222222222222222",
	}
	for i, item := range items {
		if item.Ed2k == "" || item.Ed2kHash != wantHashes[i] {
			t.Errorf("item %d = %+v", i, item)
		}
		if item.Size != int64(1234567890+i) {
			t.Errorf("item %d size = %d", i, item.Size)
		}
		if !strings.Contains(item.Title, "Bones") {
			t.Errorf("item %d title = %q", i, item.Title)
		}
	}
	payload, err := connector.Download(context.Background(), items[2])
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if payload.Ed2k != items[2].Ed2k {
		t.Errorf("grab returned %q, want %q", payload.Ed2k, items[2].Ed2k)
	}
}

func TestDDUnlimitedTorrentFallback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ucp.php":
			if r.Method == http.MethodPost {
				serveFile(t, w, "ddunlimited/login-ok.html")
				return
			}
			serveFile(t, w, "ddunlimited/login.html")
		case "/search.php":
			serveFile(t, w, "ddunlimited/search.html")
		case "/viewtopic.php":
			serveFile(t, w, "ddunlimited/topic-torrent.html")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	def := parseDefinition(t, "ddunlimited")
	def.Session = nil
	def.Meta.URLs[0] = server.URL + "/"
	connector, err := engine.NewDefinitionConnector(engine.DefinitionConfig{
		Indexer:    engine.IndexerRef{Slug: def.Meta.ID, Name: def.Meta.Name},
		Definition: def,
		Settings:   map[string]string{"username": "synthetic-user", "password": "synthetic-password"},
		Transport:  engine.NewTransport(engine.TransportConfig{AllowPrivate: true}),
	})
	if err != nil {
		t.Fatalf("NewDefinitionConnector: %v", err)
	}
	items, err := connector.Search(context.Background(), engine.SearchRequest{Mode: engine.ModeSearch, Query: "souls", Limit: 1})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1", len(items))
	}
	if items[0].Magnet == "" {
		t.Errorf("magnet missing: %+v", items[0])
	}
	if strings.Contains(items[0].DownloadURL, ".torrent") {
		t.Errorf("the .torrent should not win over the magnet: %+v", items[0])
	}
}
