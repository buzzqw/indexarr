package downloadclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// gx-torrent answers a login with "Fails." when no Web UI password is set,
// while its API is already open on the LAN. The client must skip the login in
// that case instead of treating the daemon as unreachable.
func TestGexttoSkipsLoginWithoutPasswordAndReadsVersion(t *testing.T) {
	loginCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			loginCalls++
			_, _ = w.Write([]byte("Fails."))
		case "/api/v2/app/version":
			_, _ = w.Write([]byte("v4.6.7"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	description, err := Test(context.Background(), Config{Kind: KindGextto, URL: server.URL})
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	if description != "Gextto / gx-torrent v4.6.7" {
		t.Errorf("description = %q", description)
	}
	if loginCalls != 0 {
		t.Errorf("login calls = %d, want 0 without a password", loginCalls)
	}
}

// A configured API key travels as X-Api-Key on every request, including
// torrents/add; no login session is needed.
func TestGexttoSendsAPIKeyAndTorrent(t *testing.T) {
	var gotKey, gotMagnet, gotCategory string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			t.Errorf("unexpected login with an API key")
			_, _ = w.Write([]byte("Fails."))
		case "/api/v2/torrents/add":
			gotKey = r.Header.Get("X-Api-Key")
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Fatalf("multipart: %v", err)
			}
			gotMagnet = r.FormValue("urls")
			gotCategory = r.FormValue("category")
			_, _ = w.Write([]byte("Ok."))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	err := Send(context.Background(), Config{
		Kind: KindGextto, URL: server.URL, APIKey: "gx-secret", Category: "tv",
	}, Item{Magnet: "magnet:?xt=urn:btih:abc"})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if gotKey != "gx-secret" {
		t.Errorf("X-Api-Key = %q", gotKey)
	}
	if gotMagnet != "magnet:?xt=urn:btih:abc" || gotCategory != "tv" {
		t.Errorf("urls=%q category=%q", gotMagnet, gotCategory)
	}
}

func TestGexttoLogsInWithPasswordAndUploadsTorrent(t *testing.T) {
	loggedIn := false
	var uploaded bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			if err := r.ParseForm(); err != nil {
				t.Fatalf("form: %v", err)
			}
			if r.FormValue("username") != "admin" || r.FormValue("password") != "secret" {
				t.Errorf("credentials = %q/%q", r.FormValue("username"), r.FormValue("password"))
			}
			loggedIn = true
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/add":
			uploaded = true
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Fatalf("multipart: %v", err)
			}
			if _, _, err := r.FormFile("torrents"); err != nil {
				t.Errorf("torrents part: %v", err)
			}
			_, _ = w.Write([]byte("Ok."))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	err := Send(context.Background(), Config{
		Kind: KindGextto, URL: server.URL, Username: "admin", Password: "secret",
	}, Item{Title: "A Show", Torrent: []byte("torrent-bytes")})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if !loggedIn || !uploaded {
		t.Errorf("loggedIn=%v uploaded=%v", loggedIn, uploaded)
	}
}

func TestGexttoBuildsAndValidatesURL(t *testing.T) {
	client, err := Build(Config{Kind: KindGextto, URL: "http://127.0.0.1:8080/"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if _, ok := client.(Tester); !ok {
		t.Error("Gextto client does not implement Tester")
	}
	if _, err := Build(Config{Kind: KindGextto, URL: "not a URL"}); err == nil {
		t.Error("invalid URL accepted")
	}
	if _, err := Build(Config{Kind: KindGextto}); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Errorf("empty URL error = %v", err)
	}
}
