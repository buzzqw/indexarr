package downloadclient

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/buzzqw/indexarr/internal/store"
)

func TestQBittorrentMagnet(t *testing.T) {
	var added bool
	var category, paused string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_ = r.ParseForm()
			if r.Form.Get("username") != "user" || r.Form.Get("password") != "pass" {
				http.Error(w, "bad login", http.StatusForbidden)
				return
			}
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/add":
			added = true
			_ = r.ParseMultipartForm(4 << 20)
			if got := r.FormValue("urls"); got != "magnet:?xt=urn:btih:abc" {
				t.Errorf("urls = %q", got)
			}
			category = r.FormValue("category")
			paused = r.FormValue("paused")
			_, _ = w.Write([]byte("Ok."))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	release := store.Release{Title: "Test", Magnet: "magnet:?xt=urn:btih:abc"}
	err := Add(context.Background(), Config{
		Kind: "qbittorrent", URL: server.URL, Username: "user", Password: "pass",
		Category: "indexarr",
	}, release)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if !added || category != "indexarr" || paused != "false" {
		t.Errorf("added=%v category=%q paused=%q", added, category, paused)
	}
}

func TestQBittorrentTorrentFile(t *testing.T) {
	var filename string
	var content []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/add":
			_ = r.ParseMultipartForm(4 << 20)
			file, header, err := r.FormFile("torrents")
			if err != nil {
				t.Errorf("torrent file: %v", err)
			} else {
				filename = header.Filename
				content, _ = io.ReadAll(file)
			}
			_, _ = w.Write([]byte("Ok."))
		}
	}))
	defer server.Close()
	err := Add(context.Background(), Config{Kind: "qbittorrent", URL: server.URL},
		store.Release{Title: "A/B: Test", Torrent: []byte("torrent-bytes")})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if filename != "A_B_ Test.torrent" || string(content) != "torrent-bytes" {
		t.Errorf("file = %q, %q", filename, content)
	}
}

func TestTransmissionSession(t *testing.T) {
	var attempts int
	var filename string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if r.URL.Path != "/transmission/rpc" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if r.Header.Get("X-Transmission-Session-Id") == "" {
			w.Header().Set("X-Transmission-Session-Id", "session-1")
			http.Error(w, "conflict", http.StatusConflict)
			return
		}
		var body struct {
			Arguments struct {
				Filename string `json:"filename"`
			} `json:"arguments"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		filename = body.Arguments.Filename
		_, _ = w.Write([]byte(`{"result":"success"}`))
	}))
	defer server.Close()
	err := Add(context.Background(), Config{Kind: "transmission", URL: server.URL},
		store.Release{Magnet: "magnet:?xt=urn:btih:def"})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if attempts != 2 || filename != "magnet:?xt=urn:btih:def" {
		t.Errorf("attempts=%d filename=%q", attempts, filename)
	}
}

func TestTransmissionMetainfo(t *testing.T) {
	var metainfo string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Transmission-Session-Id") == "" {
			w.Header().Set("X-Transmission-Session-Id", "s")
			http.Error(w, "conflict", http.StatusConflict)
			return
		}
		var body struct {
			Arguments struct {
				Metainfo string `json:"metainfo"`
				Paused   bool   `json:"paused"`
			} `json:"arguments"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		metainfo = body.Arguments.Metainfo
		if !body.Arguments.Paused {
			t.Error("paused not sent")
		}
		_, _ = w.Write([]byte(`{"result":"success"}`))
	}))
	defer server.Close()
	err := Add(context.Background(), Config{Kind: "transmission", URL: server.URL, Paused: true},
		store.Release{Torrent: []byte("bytes")})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if metainfo != base64.StdEncoding.EncodeToString([]byte("bytes")) {
		t.Errorf("metainfo = %q", metainfo)
	}
}

func TestUnknownClient(t *testing.T) {
	if err := Add(context.Background(), Config{Kind: "deluge"}, store.Release{Magnet: "magnet:x"}); err == nil {
		t.Error("unknown client accepted")
	}
}
