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
	"sync/atomic"
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
			switch r.URL.Query().Get("t") {
			case "61463", "3941485":
				serveFile(t, w, "ddunlimited/index.html")
			default:
				serveFile(t, w, "ddunlimited/topic.html")
			}
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
			switch r.URL.Query().Get("t") {
			case "61463", "3941485":
				serveFile(t, w, "ddunlimited/index.html")
			default:
				serveFile(t, w, "ddunlimited/topic-torrent.html")
			}
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

// The forum keeps one session per account: a login from another browser drops
// ours, and a topic then answers 200 as a guest page without links. The
// logout marker (auth.logged_in) is missing, so the engine logs in once more
// and reads the topic again instead of losing every result.
func TestDDUnlimitedLogsInAgainWhenTheForumDropsTheSession(t *testing.T) {
	var logins, guestPages atomic.Int32
	dropped := atomic.Bool{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ucp.php":
			if r.Method == http.MethodPost {
				if logins.Add(1) == 1 {
					// Another browser logs in with the same account right after.
					dropped.Store(true)
				} else {
					dropped.Store(false)
				}
				serveFile(t, w, "ddunlimited/login-ok.html")
				return
			}
			serveFile(t, w, "ddunlimited/login.html")
		case "/viewtopic.php":
			switch r.URL.Query().Get("t") {
			case "61463", "3941485":
				serveFile(t, w, "ddunlimited/index.html")
			default:
				if dropped.Load() {
					guestPages.Add(1)
					_, _ = w.Write([]byte(`<html><body><p>Devi essere registrato per vedere i link.</p></body></html>`))
					return
				}
				serveFile(t, w, "ddunlimited/topic.html")
			}
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
	items, err := connector.Search(context.Background(), engine.SearchRequest{Mode: engine.ModeSearch, Query: "bones", Limit: 1})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) != 3 || items[0].Ed2k == "" {
		t.Fatalf("items = %d (%+v), want the three eD2k files after a new login", len(items), items)
	}
	if logins.Load() != 2 || guestPages.Load() != 1 {
		t.Errorf("logins = %d, guest pages = %d; want 2 and 1", logins.Load(), guestPages.Load())
	}
}

// The forum search has a flood control: one search every few minutes, then a
// 200 "Informazione" box. Gextto asks the same series once per episode, so the
// result pages are reused (search.cache) and a refusal is an error instead of
// an empty result. A search answered as a guest (the account logged in from
// another browser) logs in again and is read once more.
func TestDDUnlimitedSearchCacheFloodControlAndGuestSearch(t *testing.T) {
	var searches, logins atomic.Int32
	guest := atomic.Bool{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ucp.php":
			if r.Method == http.MethodPost {
				logins.Add(1)
				guest.Store(false)
				serveFile(t, w, "ddunlimited/login-ok.html")
				return
			}
			serveFile(t, w, "ddunlimited/login.html")
		case "/search.php":
			searches.Add(1)
			switch {
			case guest.Load():
				_, _ = w.Write([]byte(`<html><body><div id="message"><div class="inner"><h2>Informazione</h2><p>Spiacente ma non ti è permesso di utilizzare il sistema di ricerca.</p></div></div><a href="./ucp.php?mode=login">Login</a></body></html>`))
			case r.URL.Query().Get("keywords") == "flood":
				_, _ = w.Write([]byte(`<html><body><a href="./ucp.php?mode=logout&amp;sid=0">Esci</a><div id="message"><div class="inner"><h2>Informazione</h2><p>Al momento non ti è permesso fare ricerche. Attendi qualche minuto oppure naviga nelle liste presenti in ogni sezione.</p></div></div></body></html>`))
			default:
				serveFile(t, w, "ddunlimited/search.html")
			}
		case "/viewtopic.php":
			switch r.URL.Query().Get("t") {
			case "61463", "3941485", "3941474", "3941473", "3941480":
				serveFile(t, w, "ddunlimited/index.html")
			default:
				serveFile(t, w, "ddunlimited/topic.html")
			}
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
	ctx := context.Background()
	request := engine.SearchRequest{Mode: engine.ModeSearch, Query: "marvel", Limit: 1, SkipDetails: true}

	first, err := connector.Search(ctx, request)
	if err != nil || len(first) == 0 {
		t.Fatalf("first search: %d items, err %v", len(first), err)
	}
	before := searches.Load()
	second, err := connector.Search(ctx, request)
	if err != nil || len(second) != len(first) {
		t.Fatalf("second search: %d items, err %v; want %d", len(second), err, len(first))
	}
	if searches.Load() != before {
		t.Errorf("the same search asked the forum again (%d -> %d requests); want the cached pages", before, searches.Load())
	}

	_, err = connector.Search(ctx, engine.SearchRequest{Mode: engine.ModeSearch, Query: "flood", Limit: 1, SkipDetails: true})
	if err == nil || !strings.Contains(err.Error(), "Attendi qualche minuto") {
		t.Fatalf("flood control err = %v, want the forum message", err)
	}

	loginsBefore := logins.Load()
	guest.Store(true)
	items, err := connector.Search(ctx, engine.SearchRequest{Mode: engine.ModeSearch, Query: "guest", Limit: 1, SkipDetails: true})
	if err != nil || len(items) == 0 {
		t.Fatalf("guest search: %d items, err %v; want the results after a new login", len(items), err)
	}
	if logins.Load() != loginsBefore+1 {
		t.Errorf("logins = %d, want one more than %d", logins.Load(), loginsBefore)
	}
}
