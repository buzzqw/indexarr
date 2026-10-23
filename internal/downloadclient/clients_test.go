package downloadclient

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/buzzqw/indexarr/internal/store"
)

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func TestDelugeLoginCookieAndMagnet(t *testing.T) {
	var loggedIn bool
	var magnet string
	var options struct {
		AddPaused        bool   `json:"add_paused"`
		DownloadLocation string `json:"download_location"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/json" {
			t.Errorf("path = %q", r.URL.Path)
		}
		var request struct {
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
			ID     int               `json:"id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if request.ID == 0 {
			t.Error("missing request id")
		}
		switch request.Method {
		case "auth.login":
			var user, password string
			_ = json.Unmarshal(request.Params[0], &user)
			_ = json.Unmarshal(request.Params[1], &password)
			if user != "user" || password != "pass" {
				t.Errorf("login = %q/%q", user, password)
			}
			http.SetCookie(w, &http.Cookie{Name: "_session_id", Value: "sess-1", Path: "/"})
			writeJSON(w, map[string]any{"result": true, "error": nil, "id": request.ID})
		case "core.add_torrent_magnet":
			cookie, err := r.Cookie("_session_id")
			if err != nil || cookie.Value != "sess-1" {
				t.Errorf("session cookie = %v, %v", cookie, err)
			}
			loggedIn = true
			_ = json.Unmarshal(request.Params[0], &magnet)
			_ = json.Unmarshal(request.Params[1], &options)
			writeJSON(w, map[string]any{"result": "hash-1", "error": nil, "id": request.ID})
		case "core.get_session_state":
			writeJSON(w, map[string]any{"result": []string{"a", "b"}, "error": nil, "id": request.ID})
		default:
			t.Errorf("unexpected method %q", request.Method)
		}
	}))
	defer server.Close()

	cfg := Config{
		Kind: KindDeluge, URL: server.URL, Username: "user", Password: "pass",
		Category: "indexarr", SavePath: "/downloads", Paused: true,
	}
	if err := Add(context.Background(), cfg,
		store.Release{Magnet: "magnet:?xt=urn:btih:abc"}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if !loggedIn || magnet != "magnet:?xt=urn:btih:abc" {
		t.Errorf("loggedIn=%v magnet=%q", loggedIn, magnet)
	}
	if !options.AddPaused || options.DownloadLocation != "/downloads" {
		t.Errorf("options = %+v", options)
	}
	description, err := Test(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	if !strings.Contains(description, "Deluge") || !strings.Contains(description, "2 torrents") {
		t.Errorf("description = %q", description)
	}
}

func TestDelugeReusesCheckedSession(t *testing.T) {
	var logins, checks, adds int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		switch request.Method {
		case "auth.login":
			logins++
			http.SetCookie(w, &http.Cookie{Name: "_session_id", Value: "sess", Path: "/"})
			writeJSON(w, map[string]any{"result": true, "error": nil})
		case "auth.check_session":
			checks++
			writeJSON(w, map[string]any{"result": true, "error": nil})
		case "core.add_torrent_magnet":
			adds++
			writeJSON(w, map[string]any{"result": "hash", "error": nil})
		}
	}))
	defer server.Close()
	client, err := New(Config{Kind: KindDeluge, URL: server.URL, Username: "u", Password: "p"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for i := 0; i < 2; i++ {
		if err := client.Send(context.Background(), Item{Magnet: "magnet:x"}); err != nil {
			t.Fatalf("Send %d: %v", i, err)
		}
	}
	if logins != 1 || checks != 1 || adds != 2 {
		t.Errorf("logins=%d checks=%d adds=%d", logins, checks, adds)
	}
}

func TestDelugeTorrentFileBase64(t *testing.T) {
	var filename, dump string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		switch request.Method {
		case "auth.login":
			writeJSON(w, map[string]any{"result": true, "error": nil})
		case "core.add_torrent_file":
			_ = json.Unmarshal(request.Params[0], &filename)
			_ = json.Unmarshal(request.Params[1], &dump)
			writeJSON(w, map[string]any{"result": true, "error": nil})
		default:
			t.Errorf("unexpected method %q", request.Method)
		}
	}))
	defer server.Close()
	err := Add(context.Background(), Config{Kind: KindDeluge, URL: server.URL},
		store.Release{Title: "A/B: Test", Torrent: []byte("torrent-bytes")})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if filename != "A_B_ Test.torrent" {
		t.Errorf("filename = %q", filename)
	}
	if dump != base64.StdEncoding.EncodeToString([]byte("torrent-bytes")) {
		t.Errorf("dump = %q", dump)
	}
}

func TestDelugeReloginOnUnauthorized(t *testing.T) {
	logins, adds := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		switch request.Method {
		case "auth.login":
			logins++
			writeJSON(w, map[string]any{"result": true, "error": nil})
		case "core.add_torrent_magnet":
			adds++
			if adds == 1 {
				http.Error(w, "session expired", http.StatusUnauthorized)
				return
			}
			writeJSON(w, map[string]any{"result": "hash", "error": nil})
		}
	}))
	defer server.Close()
	err := Add(context.Background(), Config{Kind: KindDeluge, URL: server.URL},
		store.Release{Magnet: "magnet:?xt=urn:btih:abc"})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if logins != 2 || adds != 2 {
		t.Errorf("logins=%d adds=%d", logins, adds)
	}
}

func TestDelugeAuthFailureRedaction(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"result": nil,
			"error":  map[string]any{"message": "bad password hunter2 for user", "code": 1},
		})
	}))
	defer server.Close()
	err := Add(context.Background(), Config{
		Kind: KindDeluge, URL: server.URL, Username: "user", Password: "hunter2",
	}, store.Release{Magnet: "magnet:x"})
	if err == nil {
		t.Fatal("login failure accepted")
	}
	if strings.Contains(err.Error(), "hunter2") || !strings.Contains(err.Error(), "***") {
		t.Errorf("error not redacted: %v", err)
	}
}

func TestDelugeMalformedJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("{not-json"))
	}))
	defer server.Close()
	err := Add(context.Background(), Config{Kind: KindDeluge, URL: server.URL},
		store.Release{Magnet: "magnet:x"})
	if err == nil || !strings.Contains(err.Error(), "Deluge response") {
		t.Fatalf("error = %v", err)
	}
}

type testRPCCall struct {
	XMLName xml.Name `xml:"methodCall"`
	Method  string   `xml:"methodName"`
	Params  []struct {
		Value struct {
			String string `xml:"string"`
			Base64 string `xml:"base64"`
		} `xml:"value"`
	} `xml:"params>param"`
}

func decodeRPCCall(t *testing.T, r *http.Request) testRPCCall {
	t.Helper()
	var call testRPCCall
	if err := xml.NewDecoder(r.Body).Decode(&call); err != nil {
		t.Fatalf("decode XML-RPC: %v", err)
	}
	return call
}

func writeRPCString(w http.ResponseWriter, value string) {
	var escaped bytes.Buffer
	_ = xml.EscapeText(&escaped, []byte(value))
	_, _ = fmt.Fprintf(w, `<?xml version="1.0"?><methodResponse><params><param><value><string>%s</string></value></param></params></methodResponse>`, escaped.String())
}

func TestRTorrentMagnet(t *testing.T) {
	var calls []testRPCCall
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/RPC2" {
			t.Errorf("path = %q", r.URL.Path)
		}
		user, password, ok := r.BasicAuth()
		if !ok || user != "user" || password != "pass" {
			t.Errorf("basic auth = %q/%q/%v", user, password, ok)
		}
		call := decodeRPCCall(t, r)
		calls = append(calls, call)
		switch call.Method {
		case "load.start":
			writeRPCString(w, "0")
		case "d.directory.set":
			writeRPCString(w, "0")
		case "system.listMethods":
			_, _ = fmt.Fprintf(w, `<?xml version="1.0"?><methodResponse><params><param><value><array><data>`+
				`<value><string>load.start</string></value><value><string>d.start</string></value>`+
				`</data></array></value></param></params></methodResponse>`)
		default:
			t.Errorf("unexpected method %q", call.Method)
		}
	}))
	defer server.Close()
	cfg := Config{
		Kind: KindRTorrent, URL: server.URL, Username: "user", Password: "pass",
		SavePath: "/downloads",
	}
	err := Add(context.Background(), cfg, store.Release{Magnet: "magnet:?xt=urn:btih:def"})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if len(calls) != 1 || calls[0].Method != "load.start" {
		t.Fatalf("calls = %+v", calls)
	}
	if len(calls[0].Params) != 3 ||
		calls[0].Params[1].Value.String != "magnet:?xt=urn:btih:def" ||
		calls[0].Params[2].Value.String != "d.directory.set=/downloads" {
		t.Errorf("params = %+v", calls[0].Params)
	}
	description, err := Test(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	if !strings.Contains(description, "2 methods") {
		t.Errorf("description = %q", description)
	}
}

func TestRTorrentPausedMagnetUsesLoadNormal(t *testing.T) {
	var method string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := decodeRPCCall(t, r)
		method = call.Method
		writeRPCString(w, "0")
	}))
	defer server.Close()
	err := Add(context.Background(), Config{Kind: KindRTorrent, URL: server.URL, Paused: true},
		store.Release{Magnet: "magnet:x"})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if method != "load.normal" {
		t.Errorf("method = %q", method)
	}
}

func TestRTorrentTorrentFileBase64(t *testing.T) {
	var methods []string
	var dump string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := decodeRPCCall(t, r)
		methods = append(methods, call.Method)
		switch call.Method {
		case "d.load.raw":
			if len(call.Params) != 2 {
				t.Errorf("params = %d", len(call.Params))
			}
			dump = call.Params[1].Value.Base64
			writeRPCString(w, "HASH")
		case "d.start":
			if len(call.Params) != 1 || call.Params[0].Value.String != "HASH" {
				t.Errorf("d.start params = %+v", call.Params)
			}
			writeRPCString(w, "0")
		}
	}))
	defer server.Close()
	err := Add(context.Background(), Config{Kind: KindRTorrent, URL: server.URL},
		store.Release{Torrent: []byte("raw-torrent")})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if dump != base64.StdEncoding.EncodeToString([]byte("raw-torrent")) {
		t.Errorf("dump = %q", dump)
	}
	if len(methods) != 2 || methods[0] != "d.load.raw" || methods[1] != "d.start" {
		t.Errorf("methods = %v", methods)
	}
}

func TestRTorrentPausedTorrentDoesNotStart(t *testing.T) {
	var methods []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := decodeRPCCall(t, r)
		methods = append(methods, call.Method)
		writeRPCString(w, "HASH")
	}))
	defer server.Close()
	err := Add(context.Background(), Config{Kind: KindRTorrent, URL: server.URL, Paused: true},
		store.Release{Torrent: []byte("raw")})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if len(methods) != 1 || methods[0] != "d.load.raw" {
		t.Errorf("methods = %v", methods)
	}
}

func TestRTorrentFaultRedaction(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<?xml version="1.0"?><methodResponse><fault><value><struct>` +
			`<member><name>faultCode</name><value><int>1</int></value></member>` +
			`<member><name>faultString</name><value><string>bad secret sx-secret</string></value></member>` +
			`</struct></value></fault></methodResponse>`))
	}))
	defer server.Close()
	err := Add(context.Background(), Config{
		Kind: KindRTorrent, URL: server.URL, Username: "u", Password: "sx-secret",
	}, store.Release{Magnet: "magnet:x"})
	if err == nil {
		t.Fatal("fault accepted")
	}
	if strings.Contains(err.Error(), "sx-secret") || !strings.Contains(err.Error(), "***") {
		t.Errorf("error not redacted: %v", err)
	}
}

func TestRTorrentMalformedXML(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<methodResponse><params>`))
	}))
	defer server.Close()
	err := Add(context.Background(), Config{Kind: KindRTorrent, URL: server.URL},
		store.Release{Magnet: "magnet:x"})
	if err == nil || !strings.Contains(err.Error(), "rTorrent response") {
		t.Fatalf("error = %v", err)
	}
}

func TestSABnzbdAddURL(t *testing.T) {
	var form map[string][]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("method = %q", r.Method)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatalf("form: %v", err)
		}
		form = r.PostForm
		writeJSON(w, map[string]any{"status": true, "nzo_ids": []string{"SABnzbd_nzo_1"}})
	}))
	defer server.Close()
	err := Add(context.Background(), Config{
		Kind: KindSABnzbd, URL: server.URL, Password: "api-key",
		Category: "tv", Paused: true,
	}, store.Release{Title: "Example.Release", Magnet: "magnet:?xt=urn:btih:abc"})
	// A magnet is not an NZB: the usenet client must reject it rather than
	// silently posting a torrent magnet as a URL.
	if err == nil {
		t.Fatal("magnet accepted by SABnzbd")
	}
	err = Send(context.Background(), Config{
		Kind: KindSABnzbd, URL: server.URL, Password: "api-key",
		Category: "tv", Paused: true,
	}, Item{Title: "Example.Release", URL: "https://example.test/file.nzb"})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	want := map[string]string{
		"mode": "addurl", "t": "addurl", "apikey": "api-key", "cat": "tv",
		"nzbname": "Example.Release", "priority": "-1", "output": "json",
		"name": "https://example.test/file.nzb",
	}
	for key, value := range want {
		if got := form[key]; len(got) != 1 || got[0] != value {
			t.Errorf("%s = %v, want %q", key, got, value)
		}
	}
}

func TestSABnzbdAddFileMultipart(t *testing.T) {
	var filename, content, mode, category string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(4 << 20); err != nil {
			t.Fatalf("multipart: %v", err)
		}
		mode = r.FormValue("mode")
		category = r.FormValue("cat")
		file, header, err := r.FormFile("name")
		if err != nil {
			t.Fatalf("form file: %v", err)
		}
		filename = header.Filename
		body, _ := io.ReadAll(file)
		content = string(body)
		writeJSON(w, map[string]any{"status": true, "nzo_ids": []string{"nzo"}})
	}))
	defer server.Close()
	err := Send(context.Background(), Config{
		Kind: KindSABnzbd, URL: server.URL + "/", Password: "key", Category: "movies",
	}, Item{Title: "A/B: Release", NZB: []byte("<nzb>payload</nzb>")})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if mode != "addfile" || category != "movies" {
		t.Errorf("mode=%q category=%q", mode, category)
	}
	if filename != "A_B_ Release.nzb" || content != "<nzb>payload</nzb>" {
		t.Errorf("file = %q %q", filename, content)
	}
}

func TestAddUsenetPayload(t *testing.T) {
	var mode, content string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(4 << 20); err != nil {
			t.Fatalf("multipart: %v", err)
		}
		mode = r.FormValue("mode")
		file, _, err := r.FormFile("name")
		if err != nil {
			t.Fatalf("form file: %v", err)
		}
		body, _ := io.ReadAll(file)
		content = string(body)
		writeJSON(w, map[string]any{"status": true})
	}))
	defer server.Close()
	err := Add(context.Background(), Config{
		Kind: KindSABnzbd, URL: server.URL, Password: "key",
	}, store.Release{
		Title:       "Usenet.Release",
		Protocol:    "usenet",
		Payload:     []byte("<nzb>usenet</nzb>"),
		PayloadType: "application/x-nzb",
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if mode != "addfile" || content != "<nzb>usenet</nzb>" {
		t.Errorf("mode=%q content=%q", mode, content)
	}
}

func TestSABnzbdVersionTest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %q", r.Method)
		}
		if r.URL.Query().Get("mode") != "version" || r.URL.Query().Get("apikey") != "key" {
			t.Errorf("query = %q", r.URL.RawQuery)
		}
		writeJSON(w, map[string]any{"version": "4.3.2"})
	}))
	defer server.Close()
	description, err := Test(context.Background(), Config{
		Kind: KindSABnzbd, URL: server.URL, APIKey: "key",
	})
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	if description != "SABnzbd 4.3.2" {
		t.Errorf("description = %q", description)
	}
}

func TestSABnzbdFailureRedaction(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"status": false, "error": "API Key Incorrect: sab-secret"})
	}))
	defer server.Close()
	err := Send(context.Background(), Config{Kind: KindSABnzbd, URL: server.URL, Password: "sab-secret"},
		Item{URL: "https://example.test/file.nzb"})
	if err == nil {
		t.Fatal("rejected add accepted")
	}
	if strings.Contains(err.Error(), "sab-secret") || !strings.Contains(err.Error(), "***") {
		t.Errorf("error not redacted: %v", err)
	}
}

func TestSABnzbdMalformedJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()
	err := Send(context.Background(), Config{Kind: KindSABnzbd, URL: server.URL},
		Item{URL: "https://example.test/file.nzb"})
	if err == nil || !strings.Contains(err.Error(), "SABnzbd response") {
		t.Fatalf("error = %v", err)
	}
}

type nzbgetTestRequest struct {
	Method string            `json:"method"`
	Params []json.RawMessage `json:"params"`
	ID     int               `json:"id"`
}

func TestNZBGetAppend(t *testing.T) {
	var filename, content, category string
	var paused bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/jsonrpc" {
			t.Errorf("path = %q", r.URL.Path)
		}
		user, password, ok := r.BasicAuth()
		if !ok || user != "user" || password != "pass" {
			t.Errorf("basic auth = %q/%q/%v", user, password, ok)
		}
		var request nzbgetTestRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if request.Method != "append" {
			t.Errorf("method = %q", request.Method)
		}
		if len(request.Params) != 8 {
			t.Fatalf("params = %d", len(request.Params))
		}
		_ = json.Unmarshal(request.Params[0], &filename)
		_ = json.Unmarshal(request.Params[1], &content)
		_ = json.Unmarshal(request.Params[2], &category)
		_ = json.Unmarshal(request.Params[4], &paused)
		writeJSON(w, map[string]any{"result": 42, "error": nil, "id": request.ID})
	}))
	defer server.Close()
	err := Send(context.Background(), Config{
		Kind: KindNZBGet, URL: server.URL, Username: "user", Password: "pass",
		Category: "tv", Paused: true,
	}, Item{Title: "Release.Name", NZB: []byte("<nzb/>")})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if filename != "Release.Name.nzb" || category != "tv" || !paused {
		t.Errorf("filename=%q category=%q paused=%v", filename, category, paused)
	}
	if content != base64.StdEncoding.EncodeToString([]byte("<nzb/>")) {
		t.Errorf("content = %q", content)
	}
}

func TestNZBGetAppendURL(t *testing.T) {
	var method, url string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request nzbgetTestRequest
		_ = json.NewDecoder(r.Body).Decode(&request)
		method = request.Method
		_ = json.Unmarshal(request.Params[1], &url)
		writeJSON(w, map[string]any{"result": 7, "error": nil})
	}))
	defer server.Close()
	err := Send(context.Background(), Config{Kind: KindNZBGet, URL: server.URL},
		Item{Title: "Name", URL: "https://example.test/file.nzb"})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if method != "appendurl" || url != "https://example.test/file.nzb" {
		t.Errorf("method=%q url=%q", method, url)
	}
}

func TestNZBGetVersionTest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request nzbgetTestRequest
		_ = json.NewDecoder(r.Body).Decode(&request)
		if request.Method != "version" {
			t.Errorf("method = %q", request.Method)
		}
		writeJSON(w, map[string]any{"result": "21.1", "error": nil})
	}))
	defer server.Close()
	description, err := Test(context.Background(), Config{Kind: KindNZBGet, URL: server.URL})
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	if description != "NZBGet 21.1" {
		t.Errorf("description = %q", description)
	}
}

func TestNZBGetFailureRedaction(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"result": nil,
			"error":  map[string]any{"code": 1, "message": "bad password nzb-secret"},
		})
	}))
	defer server.Close()
	err := Send(context.Background(), Config{
		Kind: KindNZBGet, URL: server.URL, Username: "user", Password: "nzb-secret",
	}, Item{NZB: []byte("<nzb/>")})
	if err == nil {
		t.Fatal("rejected add accepted")
	}
	if strings.Contains(err.Error(), "nzb-secret") || !strings.Contains(err.Error(), "***") {
		t.Errorf("error not redacted: %v", err)
	}
}

func TestNZBGetMalformedJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("{broken"))
	}))
	defer server.Close()
	err := Send(context.Background(), Config{Kind: KindNZBGet, URL: server.URL},
		Item{NZB: []byte("<nzb/>")})
	if err == nil || !strings.Contains(err.Error(), "NZBGet response") {
		t.Fatalf("error = %v", err)
	}
}

func TestRedactTextAndSafeURL(t *testing.T) {
	long := strings.Repeat("x", 400) + " apikey=supersecret"
	redacted := redactText(long, maxErrorBody, "supersecret")
	if len(redacted) > maxErrorBody+3 {
		t.Errorf("redacted length = %d", len(redacted))
	}
	if strings.Contains(redacted, "supersecret") {
		t.Errorf("secret leaked: %q", redacted)
	}
	safe := safeURL("http://user:pass@example.test:8080/json?apikey=secret")
	if strings.Contains(safe, "user") || strings.Contains(safe, "pass") || strings.Contains(safe, "secret") {
		t.Errorf("safeURL leaked credentials: %q", safe)
	}
	message := redactText(`Post "http://user:pass@example.test/json?apikey=abc": refused`, 0)
	if strings.Contains(message, "user") || strings.Contains(message, "pass") || strings.Contains(message, "abc") {
		t.Errorf("redacted message leaked credentials: %q", message)
	}
}

func TestQBittorrentTest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/app/version":
			_, _ = w.Write([]byte("4.6.0"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	description, err := Test(context.Background(), Config{Kind: KindQBittorrent, URL: server.URL})
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	if description != "qBittorrent 4.6.0" {
		t.Errorf("description = %q", description)
	}
}

func TestTransmissionTest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Transmission-Session-Id") == "" {
			w.Header().Set("X-Transmission-Session-Id", "session")
			http.Error(w, "conflict", http.StatusConflict)
			return
		}
		writeJSON(w, map[string]any{
			"result":    "success",
			"arguments": map[string]any{"version": "4.0.5"},
		})
	}))
	defer server.Close()
	description, err := Test(context.Background(), Config{Kind: KindTransmission, URL: server.URL})
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	if description != "Transmission 4.0.5" {
		t.Errorf("description = %q", description)
	}
}

func TestUnknownClient(t *testing.T) {
	if err := Add(context.Background(), Config{Kind: "bogus"}, store.Release{Magnet: "magnet:x"}); err == nil {
		t.Error("unknown client accepted")
	}
	if _, err := New(Config{Kind: "bogus"}); err == nil {
		t.Error("unknown client built")
	}
	if _, err := New(Config{Kind: KindNone}); err == nil {
		t.Error("none client built")
	}
}

func TestNewEveryKind(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()
	for _, kind := range []string{KindQBittorrent, KindTransmission, KindDeluge, KindRTorrent, KindSABnzbd, KindNZBGet} {
		client, err := Build(Config{Kind: kind, URL: server.URL})
		if err != nil {
			t.Errorf("Build(%s): %v", kind, err)
			continue
		}
		if _, ok := client.(Tester); !ok {
			t.Errorf("%s does not implement Tester", kind)
		}
	}
}
