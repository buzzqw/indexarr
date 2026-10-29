package downloadclient

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testScratch keeps temporary files outside /tmp, as the project rules require.
func testScratch(t *testing.T) string {
	t.Helper()
	base := os.Getenv("INDEXARR_TEST_DIR")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Fatal(err)
		}
		base = filepath.Join(home, "indexarr-arrtest")
	}
	if err := os.MkdirAll(base, 0o750); err != nil {
		return t.TempDir()
	}
	dir, err := os.MkdirTemp(base, "downloadclient-")
	if err != nil {
		return t.TempDir()
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func TestBlackholeWritesTorrentMagnetAndNzb(t *testing.T) {
	dir := testScratch(t)
	cfg := Config{Kind: KindTorrentBlackhole, SavePath: dir}

	if err := Send(context.Background(), cfg, Item{Title: "A/B: Test", Torrent: []byte("torrent-bytes")}); err != nil {
		t.Fatalf("Send torrent: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(dir, "A_B_ Test.torrent"))
	if err != nil || string(content) != "torrent-bytes" {
		t.Fatalf("torrent file = %q, %v", content, err)
	}
	if err := Send(context.Background(), cfg, Item{Title: "Magnet Release", Magnet: "magnet:?xt=urn:btih:abc"}); err != nil {
		t.Fatalf("Send magnet: %v", err)
	}
	content, err = os.ReadFile(filepath.Join(dir, "Magnet Release.magnet"))
	if err != nil || string(content) != "magnet:?xt=urn:btih:abc" {
		t.Fatalf("magnet file = %q, %v", content, err)
	}

	usenet := Config{Kind: KindUsenetBlackhole, SavePath: dir}
	if err := Send(context.Background(), usenet, Item{Title: "Usenet Release", NZB: []byte("<nzb/>")}); err != nil {
		t.Fatalf("Send nzb: %v", err)
	}
	content, err = os.ReadFile(filepath.Join(dir, "Usenet Release.nzb"))
	if err != nil || string(content) != "<nzb/>" {
		t.Fatalf("nzb file = %q, %v", content, err)
	}

	description, err := Test(context.Background(), cfg)
	if err != nil || !strings.Contains(description, "Torrent Blackhole") {
		t.Fatalf("Test = %q, %v", description, err)
	}
}

func TestBlackholeNeedsSavePathAndRejectsWrongPayload(t *testing.T) {
	if _, err := New(Config{Kind: KindTorrentBlackhole}); err == nil {
		t.Error("blackhole without a save path was accepted")
	}
	dir := testScratch(t)
	err := Send(context.Background(), Config{Kind: KindUsenetBlackhole, SavePath: dir},
		Item{Title: "Wrong", Torrent: []byte("bytes")})
	if err == nil || !strings.Contains(err.Error(), ".nzb") {
		t.Errorf("usenet blackhole error = %v", err)
	}
	err = Send(context.Background(), Config{Kind: KindTorrentBlackhole, SavePath: dir},
		Item{Title: "Wrong", NZB: []byte("<nzb/>")})
	if err == nil || !strings.Contains(err.Error(), ".torrent") {
		t.Errorf("torrent blackhole error = %v", err)
	}
}

func TestBlackholeFilenameIsBoundedAndSafe(t *testing.T) {
	dir := testScratch(t)
	title := strings.Repeat("ä", 300) + "/../../escape"
	if err := Send(context.Background(), Config{Kind: KindTorrentBlackhole, SavePath: dir},
		Item{Title: title, Torrent: []byte("x")}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("entries = %v, %v", entries, err)
	}
	name := entries[0].Name()
	if strings.ContainsAny(name, `/\`) || len(name) > 200 {
		t.Errorf("unsafe filename %q", name)
	}
}

type aria2TestRequest struct {
	JSONRPC string            `json:"jsonrpc"`
	ID      string            `json:"id"`
	Method  string            `json:"method"`
	Params  []json.RawMessage `json:"params"`
}

func TestAria2MagnetTokenDirAndPause(t *testing.T) {
	var request aria2TestRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/jsonrpc" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode: %v", err)
		}
		writeJSON(w, map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": "gid-1"})
	}))
	defer server.Close()

	err := Send(context.Background(), Config{
		Kind: KindAria2, URL: server.URL, APIKey: "rpc-secret",
		SavePath: "/downloads", Paused: true,
	}, Item{Magnet: "magnet:?xt=urn:btih:abc"})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if request.Method != "aria2.addUri" || len(request.Params) != 3 {
		t.Fatalf("request = %+v", request)
	}
	var token string
	var urls []string
	var options map[string]string
	_ = json.Unmarshal(request.Params[0], &token)
	_ = json.Unmarshal(request.Params[1], &urls)
	_ = json.Unmarshal(request.Params[2], &options)
	if token != "token:rpc-secret" || len(urls) != 1 || urls[0] != "magnet:?xt=urn:btih:abc" {
		t.Errorf("token=%q urls=%v", token, urls)
	}
	if options["dir"] != "/downloads" || options["pause"] != "true" {
		t.Errorf("options = %v", options)
	}
}

func TestAria2TorrentBase64(t *testing.T) {
	var request aria2TestRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&request)
		writeJSON(w, map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": "gid-2"})
	}))
	defer server.Close()
	err := Send(context.Background(), Config{Kind: KindAria2, URL: server.URL, APIKey: "rpc-secret"},
		Item{Title: "Test", Torrent: []byte("torrent-bytes")})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if request.Method != "aria2.addTorrent" || len(request.Params) != 4 {
		t.Fatalf("request = %+v", request)
	}
	var encoded string
	_ = json.Unmarshal(request.Params[1], &encoded)
	if encoded != base64.StdEncoding.EncodeToString([]byte("torrent-bytes")) {
		t.Errorf("base64 = %q", encoded)
	}
}

func TestAria2VersionAndErrorRedaction(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request aria2TestRequest
		_ = json.NewDecoder(r.Body).Decode(&request)
		if request.Method == "aria2.getVersion" {
			writeJSON(w, map[string]any{"jsonrpc": "2.0", "id": request.ID,
				"result": map[string]string{"version": "1.36.0"}})
			return
		}
		writeJSON(w, map[string]any{"jsonrpc": "2.0", "id": request.ID,
			"error": map[string]any{"code": 1, "message": "bad secret rpc-secret"}})
	}))
	defer server.Close()
	description, err := Test(context.Background(), Config{Kind: KindAria2, URL: server.URL})
	if err != nil || description != "Aria2 1.36.0" {
		t.Fatalf("Test = %q, %v", description, err)
	}
	err = Send(context.Background(), Config{Kind: KindAria2, URL: server.URL, APIKey: "rpc-secret"},
		Item{Magnet: "magnet:x"})
	if err == nil || strings.Contains(err.Error(), "rpc-secret") || !strings.Contains(err.Error(), "***") {
		t.Errorf("error not redacted: %v", err)
	}
}

func TestUTorrentTokenMagnetFileAndVersion(t *testing.T) {
	var addURL, addFile bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, password, ok := r.BasicAuth()
		if !ok || user != "user" || password != "pass" {
			t.Errorf("basic auth = %q/%q/%v", user, password, ok)
		}
		switch r.URL.Path {
		case "/gui/token.html":
			_, _ = io.WriteString(w, `<html><div id='token' style='display:none;'>tok-1</div></html>`)
		case "/gui/":
			if r.URL.Query().Get("token") != "tok-1" {
				t.Errorf("token = %q", r.URL.Query().Get("token"))
			}
			switch r.URL.Query().Get("action") {
			case "add-url":
				addURL = true
				if r.URL.Query().Get("s") != "magnet:?xt=urn:btih:def" {
					t.Errorf("s = %q", r.URL.Query().Get("s"))
				}
			case "add-file":
				addFile = true
				if err := r.ParseMultipartForm(4 << 20); err != nil {
					t.Fatalf("multipart: %v", err)
				}
				file, header, err := r.FormFile("torrent_file")
				if err != nil {
					t.Fatalf("form file: %v", err)
				}
				content, _ := io.ReadAll(file)
				if header.Filename != "A_B_ Test.torrent" || string(content) != "torrent-bytes" {
					t.Errorf("file = %q, %q", header.Filename, content)
				}
			case "getsettings":
				writeJSON(w, map[string]any{"build": 12345})
			default:
				t.Errorf("unexpected action %q", r.URL.Query().Get("action"))
			}
		default:
			t.Errorf("path = %q", r.URL.Path)
		}
	}))
	defer server.Close()

	cfg := Config{Kind: KindUTorrent, URL: server.URL, Username: "user", Password: "pass"}
	if err := Send(context.Background(), cfg, Item{Magnet: "magnet:?xt=urn:btih:def"}); err != nil {
		t.Fatalf("Send magnet: %v", err)
	}
	if err := Send(context.Background(), cfg, Item{Title: "A/B: Test", Torrent: []byte("torrent-bytes")}); err != nil {
		t.Fatalf("Send file: %v", err)
	}
	description, err := Test(context.Background(), cfg)
	if err != nil || description != "uTorrent build 12345" {
		t.Fatalf("Test = %q, %v", description, err)
	}
	if !addURL || !addFile {
		t.Errorf("addURL=%v addFile=%v", addURL, addFile)
	}
}

func TestUTorrentAuthFailureDoesNotLeakPassword(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad password ut-secret", http.StatusUnauthorized)
	}))
	defer server.Close()
	_, err := Test(context.Background(), Config{
		Kind: KindUTorrent, URL: server.URL, Username: "user", Password: "ut-secret",
	})
	if err == nil || strings.Contains(err.Error(), "ut-secret") {
		t.Errorf("error = %v", err)
	}
}

func TestFloodAddURLTagsDestinationAndFile(t *testing.T) {
	var authenticated, addedURL, addedFile bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/auth/authenticate":
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode auth: %v", err)
			}
			if body["username"] != "user" || body["password"] != "pass" {
				t.Errorf("auth = %v", body)
			}
			authenticated = true
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "s1", Path: "/"})
			writeJSON(w, map[string]any{"username": "user"})
		case "/api/torrents/add-urls":
			addedURL = true
			var body struct {
				URLs        []string `json:"urls"`
				Tags        []string `json:"tags"`
				Destination string   `json:"destination"`
				Start       bool     `json:"start"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if len(body.URLs) != 1 || body.URLs[0] != "magnet:?xt=urn:btih:abc" {
				t.Errorf("urls = %v", body.URLs)
			}
			if len(body.Tags) != 1 || body.Tags[0] != "tv" || body.Destination != "/downloads" || !body.Start {
				t.Errorf("body = %+v", body)
			}
		case "/api/auth/verify":
			// The session cookie set by authenticate is enough for Flood.
		case "/api/torrents/add-files":
			addedFile = true
			var body struct {
				Files []string `json:"files"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if len(body.Files) != 1 || body.Files[0] != base64.StdEncoding.EncodeToString([]byte("torrent-bytes")) {
				t.Errorf("files = %v", body.Files)
			}
		default:
			t.Errorf("path = %q", r.URL.Path)
		}
	}))
	defer server.Close()

	cfg := Config{Kind: KindFlood, URL: server.URL, Username: "user", Password: "pass",
		Category: "tv", SavePath: "/downloads"}
	if err := Send(context.Background(), cfg, Item{Magnet: "magnet:?xt=urn:btih:abc"}); err != nil {
		t.Fatalf("Send magnet: %v", err)
	}
	if err := Send(context.Background(), cfg, Item{Title: "Test", Torrent: []byte("torrent-bytes")}); err != nil {
		t.Fatalf("Send file: %v", err)
	}
	description, err := Test(context.Background(), cfg)
	if err != nil || description != "Flood OK" {
		t.Fatalf("Test = %q, %v", description, err)
	}
	if !authenticated || !addedURL || !addedFile {
		t.Errorf("authenticated=%v addURL=%v addFile=%v", authenticated, addedURL, addedFile)
	}
}

func TestFloodAuthFailureRedaction(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad password flood-secret", http.StatusUnauthorized)
	}))
	defer server.Close()
	_, err := Test(context.Background(), Config{
		Kind: KindFlood, URL: server.URL, Username: "u", Password: "flood-secret",
	})
	if err == nil || strings.Contains(err.Error(), "flood-secret") {
		t.Errorf("error = %v", err)
	}
}

func TestDownloadStationLoginURLAndFile(t *testing.T) {
	var login, addedURL, addedFile bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/webapi/auth.cgi":
			if err := r.ParseForm(); err != nil {
				t.Fatalf("auth form: %v", err)
			}
			if r.PostForm.Get("api") != "SYNO.API.Auth" || r.PostForm.Get("account") != "user" ||
				r.PostForm.Get("passwd") != "pass" || r.PostForm.Get("format") != "sid" ||
				r.PostForm.Get("session") != "DownloadStation" {
				t.Errorf("auth form = %v", r.PostForm)
			}
			login = true
			writeJSON(w, map[string]any{"success": true, "data": map[string]string{"sid": "sid-1"}})
		case "/webapi/DownloadStation/task.cgi":
			if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
				addedFile = true
				if err := r.ParseMultipartForm(4 << 20); err != nil {
					t.Fatalf("multipart: %v", err)
				}
				if r.FormValue("_sid") != "sid-1" || r.FormValue("method") != "create" {
					t.Errorf("file form = %v", r.Form)
				}
				file, header, err := r.FormFile("file")
				if err != nil {
					t.Fatalf("file: %v", err)
				}
				content, _ := io.ReadAll(file)
				if header.Filename != "Test.torrent" || string(content) != "torrent-bytes" {
					t.Errorf("file = %q, %q", header.Filename, content)
				}
				writeJSON(w, map[string]any{"success": true})
				return
			}
			if err := r.ParseForm(); err != nil {
				t.Fatalf("task form: %v", err)
			}
			if r.PostForm.Get("method") == "list" {
				writeJSON(w, map[string]any{"success": true, "data": map[string]any{"total": 2}})
				return
			}
			addedURL = true
			if r.PostForm.Get("_sid") != "sid-1" || r.PostForm.Get("uri") != "magnet:?xt=urn:btih:abc" ||
				r.PostForm.Get("destination") != "/downloads" {
				t.Errorf("task form = %v", r.PostForm)
			}
			writeJSON(w, map[string]any{"success": true})
		default:
			t.Errorf("path = %q", r.URL.Path)
		}
	}))
	defer server.Close()

	cfg := Config{Kind: KindDownloadStation, URL: server.URL, Username: "user", Password: "pass",
		SavePath: "/downloads"}
	if err := Send(context.Background(), cfg, Item{Magnet: "magnet:?xt=urn:btih:abc"}); err != nil {
		t.Fatalf("Send magnet: %v", err)
	}
	if err := Send(context.Background(), cfg, Item{Title: "Test", Torrent: []byte("torrent-bytes")}); err != nil {
		t.Fatalf("Send file: %v", err)
	}
	description, err := Test(context.Background(), cfg)
	if err != nil || !strings.Contains(description, "2 tasks") {
		t.Fatalf("Test = %q, %v", description, err)
	}
	if !login || !addedURL || !addedFile {
		t.Errorf("login=%v addURL=%v addFile=%v", login, addedURL, addedFile)
	}
}

func TestDownloadStationErrorAndRedaction(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/webapi/auth.cgi" {
			writeJSON(w, map[string]any{"success": true, "data": map[string]string{"sid": "sid"}})
			return
		}
		writeJSON(w, map[string]any{"success": false, "error": map[string]int{"code": 400}})
	}))
	defer server.Close()
	err := Send(context.Background(), Config{
		Kind: KindDownloadStation, URL: server.URL, Username: "user", Password: "ds-secret",
	}, Item{Magnet: "magnet:x"})
	if err == nil || !strings.Contains(err.Error(), "400") || strings.Contains(err.Error(), "ds-secret") {
		t.Errorf("error = %v", err)
	}
}

func TestNZBVortexNonceLoginAddAndVersion(t *testing.T) {
	const key = "vortex-key"
	var login, add bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/auth/nonce":
			writeJSON(w, map[string]string{"authNonce": "nonce-1"})
		case "/api/auth/login":
			query := r.URL.Query()
			expected := sha256.Sum256([]byte("nonce-1:" + query.Get("cnonce") + ":" + key))
			if query.Get("nonce") != "nonce-1" ||
				query.Get("hash") != base64.StdEncoding.EncodeToString(expected[:]) {
				t.Errorf("login query = %v", query)
			}
			login = true
			writeJSON(w, map[string]any{"result": "ok", "loginResult": "successful", "sessionId": "sess-1"})
		case "/api/nzb/add":
			add = true
			if r.URL.Query().Get("sessionid") != "sess-1" || r.URL.Query().Get("groupname") != "tv" {
				t.Errorf("add query = %v", r.URL.Query())
			}
			if err := r.ParseMultipartForm(4 << 20); err != nil {
				t.Fatalf("multipart: %v", err)
			}
			file, header, err := r.FormFile("name")
			if err != nil {
				t.Fatalf("file: %v", err)
			}
			content, _ := io.ReadAll(file)
			if header.Filename != "Test.nzb" || string(content) != "<nzb/>" {
				t.Errorf("file = %q, %q", header.Filename, content)
			}
			writeJSON(w, map[string]any{"result": "ok", "add_uuid": "u1"})
		case "/api/app/appversion":
			writeJSON(w, map[string]any{"result": "ok", "version": "3.1.4"})
		default:
			t.Errorf("path = %q", r.URL.Path)
		}
	}))
	defer server.Close()

	cfg := Config{Kind: KindNZBVortex, URL: server.URL, APIKey: key, Category: "tv"}
	client, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := client.Send(context.Background(), Item{Title: "Test", NZB: []byte("<nzb/>")}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	description, err := client.Test(context.Background())
	if err != nil || description != "NZBVortex 3.1.4" {
		t.Fatalf("Test = %q, %v", description, err)
	}
	if !login || !add {
		t.Errorf("login=%v add=%v", login, add)
	}
}

func TestNZBVortexAuthFailureAndURLRejected(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/auth/nonce" {
			writeJSON(w, map[string]string{"authNonce": "n"})
			return
		}
		writeJSON(w, map[string]any{"result": "ok", "loginResult": "failed"})
	}))
	defer server.Close()
	_, err := Test(context.Background(), Config{Kind: KindNZBVortex, URL: server.URL, APIKey: "bad"})
	if err == nil || !strings.Contains(err.Error(), "authentication failed") {
		t.Errorf("error = %v", err)
	}
	err = Send(context.Background(), Config{Kind: KindNZBVortex, URL: server.URL, APIKey: "bad"},
		Item{URL: "https://example.test/file.nzb"})
	if err == nil || !strings.Contains(err.Error(), ".nzb") {
		t.Errorf("URL error = %v", err)
	}
}

func TestHadoukenAddFileURLAndVersion(t *testing.T) {
	var addedFile, addedURL bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if user, password, ok := r.BasicAuth(); !ok || user != "user" || password != "pass" {
			t.Errorf("basic auth = %q/%q/%v", user, password, ok)
		}
		var request aria2TestRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode: %v", err)
		}
		switch request.Method {
		case "webui.addTorrent":
			var kind string
			_ = json.Unmarshal(request.Params[0], &kind)
			var payload string
			_ = json.Unmarshal(request.Params[1], &payload)
			var options map[string]string
			_ = json.Unmarshal(request.Params[2], &options)
			switch kind {
			case "file":
				addedFile = true
				if payload != base64.StdEncoding.EncodeToString([]byte("torrent-bytes")) {
					t.Errorf("payload = %q", payload)
				}
			case "url":
				addedURL = true
				if payload != "magnet:?xt=urn:btih:abc" {
					t.Errorf("payload = %q", payload)
				}
			}
			if options["label"] != "tv" {
				t.Errorf("options = %v", options)
			}
			writeJSON(w, map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": "hash"})
		case "core.getSystemInfo":
			writeJSON(w, map[string]any{"jsonrpc": "2.0", "id": request.ID,
				"result": map[string]any{"versions": map[string]string{"hadouken": "5.1.2"}}})
		default:
			t.Errorf("method = %q", request.Method)
		}
	}))
	defer server.Close()

	cfg := Config{Kind: KindHadouken, URL: server.URL, Username: "user", Password: "pass", Category: "tv"}
	if err := Send(context.Background(), cfg, Item{Title: "Test", Torrent: []byte("torrent-bytes")}); err != nil {
		t.Fatalf("Send file: %v", err)
	}
	if err := Send(context.Background(), cfg, Item{Magnet: "magnet:?xt=urn:btih:abc"}); err != nil {
		t.Fatalf("Send magnet: %v", err)
	}
	description, err := Test(context.Background(), cfg)
	if err != nil || description != "Hadouken 5.1.2" {
		t.Fatalf("Test = %q, %v", description, err)
	}
	if !addedFile || !addedURL {
		t.Errorf("addedFile=%v addedURL=%v", addedFile, addedURL)
	}
}

func TestHadoukenErrorRedaction(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"jsonrpc": "2.0", "id": "indexarr",
			"error": map[string]any{"code": 1, "message": "bad password hd-secret"}})
	}))
	defer server.Close()
	err := Send(context.Background(), Config{
		Kind: KindHadouken, URL: server.URL, Username: "user", Password: "hd-secret",
	}, Item{Magnet: "magnet:x"})
	if err == nil || strings.Contains(err.Error(), "hd-secret") || !strings.Contains(err.Error(), "***") {
		t.Errorf("error not redacted: %v", err)
	}
}

func TestNewExtendedKindsImplementTester(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()
	for _, kind := range []string{KindTorrentBlackhole, KindUsenetBlackhole, KindAria2,
		KindUTorrent, KindFlood, KindDownloadStation, KindNZBVortex, KindHadouken} {
		client, err := Build(Config{Kind: kind, URL: server.URL, SavePath: testScratch(t)})
		if err != nil {
			t.Errorf("Build(%s): %v", kind, err)
			continue
		}
		if _, ok := client.(Tester); !ok {
			t.Errorf("%s does not implement Tester", kind)
		}
	}
}
