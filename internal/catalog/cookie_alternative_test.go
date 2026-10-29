package catalog_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/buzzqw/indexarr/internal/engine"
)

// cookieTorrentLeech serves the TorrentLeech fixtures and answers with the
// login page while dropped is set, as the site does for an expired session.
func cookieTorrentLeech(t *testing.T, logins *atomic.Int32, dropped *atomic.Bool, pasted *atomic.Int32) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.Header.Get("Cookie"), "tl_pasted=1") {
			pasted.Add(1)
		}
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
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func cookieConnector(t *testing.T, server *httptest.Server, settings map[string]string) *engine.DefinitionConnector {
	t.Helper()
	def := parseDefinition(t, "torrentleech")
	def.Meta.URLs[0] = server.URL + "/"
	connector, err := engine.NewDefinitionConnector(engine.DefinitionConfig{
		Indexer:    engine.IndexerRef{Slug: "torrentleech", Name: "TorrentLeech"},
		Definition: def,
		Settings:   settings,
		Transport:  engine.NewTransport(engine.TransportConfig{AllowPrivate: true}),
	})
	if err != nil {
		t.Fatalf("NewDefinitionConnector: %v", err)
	}
	return connector
}

func TestPastedCookieReplacesTheLogin(t *testing.T) {
	var logins, pasted atomic.Int32
	var dropped atomic.Bool
	server := cookieTorrentLeech(t, &logins, &dropped, &pasted)
	connector := cookieConnector(t, server, map[string]string{"cookie": "tl_pasted=1"})

	items, err := connector.Search(context.Background(), engine.SearchRequest{Mode: engine.ModeSearch, Query: "test"})
	if err != nil || len(items) != 3 {
		t.Fatalf("search: %d items, err %v", len(items), err)
	}
	if logins.Load() != 0 {
		t.Errorf("logins = %d, want 0: the cookie replaces the login form", logins.Load())
	}
	if pasted.Load() == 0 {
		t.Error("the search did not carry the pasted cookie")
	}

	dropped.Store(true)
	_, err = connector.Search(context.Background(), engine.SearchRequest{Mode: engine.ModeSearch, Query: "other"})
	if err == nil || !strings.Contains(err.Error(), "session cookie pasted from the browser has expired") {
		t.Fatalf("search with an expired cookie and no login: err %v", err)
	}
	if logins.Load() != 0 {
		t.Errorf("logins = %d, want 0 without a username and password", logins.Load())
	}
}

func TestExpiredPastedCookieFallsBackToTheLogin(t *testing.T) {
	var logins, pasted atomic.Int32
	var dropped atomic.Bool
	server := cookieTorrentLeech(t, &logins, &dropped, &pasted)
	connector := cookieConnector(t, server, map[string]string{
		"cookie": "tl_pasted=1", "username": "synthetic-user", "password": "synthetic-password",
	})

	if items, err := connector.Search(context.Background(), engine.SearchRequest{Mode: engine.ModeSearch, Query: "test"}); err != nil || len(items) != 3 {
		t.Fatalf("first search: %d items, err %v", len(items), err)
	}
	if logins.Load() != 0 {
		t.Fatalf("logins = %d, want 0 while the cookie works", logins.Load())
	}

	dropped.Store(true)
	if items, err := connector.Search(context.Background(), engine.SearchRequest{Mode: engine.ModeSearch, Query: "other"}); err != nil || len(items) != 3 {
		t.Fatalf("search after the cookie expired: %d items, err %v", len(items), err)
	}
	if logins.Load() != 1 {
		t.Errorf("logins = %d, want 1: the expired cookie leaves the way to the login", logins.Load())
	}
	before := pasted.Load()
	if _, err := connector.Search(context.Background(), engine.SearchRequest{Mode: engine.ModeSearch, Query: "third"}); err != nil {
		t.Fatalf("third search: %v", err)
	}
	if pasted.Load() != before {
		t.Error("the expired cookie is still sent after the login")
	}
}
