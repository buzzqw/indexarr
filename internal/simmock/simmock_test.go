package simmock

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/buzzqw/indexarr/internal/appsync"
	"github.com/buzzqw/indexarr/internal/downloadclient"
	"github.com/buzzqw/indexarr/internal/feed"
)

func newTestServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	server := New(Config{
		APIKey:      "demo-key",
		SonarrKey:   "demo-sonarr",
		ProwlarrKey: "demo-prowlarr",
		Logger:      log.New(io.Discard, "", 0),
	})
	ts := httptest.NewServer(server.Handler())
	t.Cleanup(ts.Close)
	return server, ts
}

func getBody(t *testing.T, client *http.Client, rawURL string, headers map[string]string) (int, []byte, http.Header) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", rawURL, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, body, resp.Header
}

func TestHealthz(t *testing.T) {
	_, ts := newTestServer(t)
	status, body, _ := getBody(t, ts.Client(), ts.URL+"/healthz", nil)
	if status != http.StatusOK || !strings.Contains(string(body), `"status":"ok"`) {
		t.Fatalf("healthz = %d %s", status, body)
	}
}

func TestTorznabCapsSearchAndGet(t *testing.T) {
	server, ts := newTestServer(t)

	status, _, _ := getBody(t, ts.Client(), ts.URL+"/torznab/api?t=search", nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("search without key = %d, want 401", status)
	}

	status, caps, _ := getBody(t, ts.Client(), ts.URL+"/torznab/api?t=caps&apikey=demo-key", nil)
	if status != http.StatusOK {
		t.Fatalf("caps = %d", status)
	}
	for _, needle := range []string{"<caps>", `<category id="5000"`, `<subcat id="5040"`, "tv-search"} {
		if !strings.Contains(string(caps), needle) {
			t.Errorf("caps missing %q", needle)
		}
	}

	status, search, _ := getBody(t, ts.Client(), ts.URL+"/torznab/api?t=search&apikey=demo-key", nil)
	if status != http.StatusOK {
		t.Fatalf("search = %d", status)
	}
	if got := strings.Count(string(search), "<item>"); got != 3 {
		t.Fatalf("search items = %d, want 3", got)
	}
	for _, needle := range []string{
		"The.Simpsons.S01E01.1080p.WEB-DL",
		"Sim.Demo.Movie.2026.1080p.BluRay.x264",
		"Sim.Demo.Album.2026.FLAC",
		`<torznab:attr name="infohash"`,
		`<torznab:attr name="magneturl"`,
		`<newznab:attr name="category"`,
		`type="application/x-nzb"`,
	} {
		if !strings.Contains(string(search), needle) {
			t.Errorf("search missing %q", needle)
		}
	}

	status, torrent, headers := getBody(t, ts.Client(),
		ts.URL+"/torznab/api?t=get&id=demo-torrent-2&apikey=demo-key", nil)
	if status != http.StatusOK {
		t.Fatalf("torrent get = %d", status)
	}
	if contentType := headers.Get("Content-Type"); !strings.Contains(contentType, "bittorrent") {
		t.Errorf("torrent content type = %q", contentType)
	}
	hash, err := feed.InfoHash(torrent)
	if err != nil || len(hash) != 40 {
		t.Fatalf("torrent infohash = %q, %v", hash, err)
	}

	status, nzb, headers := getBody(t, ts.Client(),
		ts.URL+"/newznab/api?t=get&id=demo-nzb-3&apikey=demo-key", nil)
	if status != http.StatusOK {
		t.Fatalf("nzb get = %d", status)
	}
	if contentType := headers.Get("Content-Type"); !strings.Contains(contentType, "nzb") {
		t.Errorf("nzb content type = %q", contentType)
	}
	if !bytes.Contains(nzb, []byte("<nzb")) {
		t.Errorf("nzb body = %s", nzb)
	}

	status, newznabSearch, _ := getBody(t, ts.Client(), ts.URL+"/newznab/api?t=search&apikey=demo-key", nil)
	if status != http.StatusOK || strings.Count(string(newznabSearch), "<item>") != 3 {
		t.Fatalf("newznab search = %d, %s", status, newznabSearch)
	}

	if len(server.releases) != 3 {
		t.Fatalf("fixtures = %d", len(server.releases))
	}
}

func TestTrackerLoginSearchDownload(t *testing.T) {
	_, ts := newTestServer(t)
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}

	status, loginPage, _ := getBody(t, client, ts.URL+"/tracker/login", nil)
	if status != http.StatusOK || !strings.Contains(string(loginPage), `name="csrf_token"`) {
		t.Fatalf("login page = %d %s", status, loginPage)
	}

	form := url.Values{
		"csrf_token": {"wrong"},
		"username":   {"demo"},
		"password":   {"demo"},
	}
	resp, err := client.PostForm(ts.URL+"/tracker/login", form)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("bad csrf = %d, want 403", resp.StatusCode)
	}

	form.Set("csrf_token", trackerCSRF)
	resp, err = client.PostForm(ts.URL+"/tracker/login", form)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login = %d", resp.StatusCode)
	}

	status, search, _ := getBody(t, client, ts.URL+"/tracker/search", nil)
	if status != http.StatusOK {
		t.Fatalf("search = %d", status)
	}
	for _, needle := range []string{
		`<table class="torrents">`,
		`class="download"`,
		`class="size"`,
		`class="seeders"`,
		`class="date"`,
		`class="category"`,
		"1.5 GB",
	} {
		if !strings.Contains(string(search), needle) {
			t.Errorf("tracker search missing %q", needle)
		}
	}
	if got := strings.Count(string(search), `class="download"`); got != 2 {
		t.Errorf("tracker download links = %d, want 2", got)
	}

	status, torrent, headers := getBody(t, client, ts.URL+"/tracker/download/1.torrent", nil)
	if status != http.StatusOK || len(torrent) == 0 {
		t.Fatalf("tracker download = %d (%d bytes)", status, len(torrent))
	}
	if contentType := headers.Get("Content-Type"); !strings.Contains(contentType, "bittorrent") {
		t.Errorf("tracker content type = %q", contentType)
	}

	anonymous := ts.Client()
	status, _, _ = getBody(t, anonymous, ts.URL+"/tracker/search", nil)
	if status != http.StatusUnauthorized {
		t.Errorf("anonymous search = %d, want 401", status)
	}
	status, _, _ = getBody(t, anonymous, ts.URL+"/tracker/download/1.torrent", nil)
	if status != http.StatusUnauthorized {
		t.Errorf("anonymous download = %d, want 401", status)
	}
}

func TestProwlarrShapes(t *testing.T) {
	_, ts := newTestServer(t)

	status, _, _ := getBody(t, ts.Client(), ts.URL+"/prowlarr/api/v1/indexer", nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("prowlarr without key = %d", status)
	}

	headers := map[string]string{"X-Api-Key": "demo-prowlarr"}
	status, indexers, _ := getBody(t, ts.Client(), ts.URL+"/prowlarr/api/v1/indexer", headers)
	if status != http.StatusOK {
		t.Fatalf("prowlarr indexers = %d", status)
	}
	for _, needle := range []string{`"Torznab"`, `"Newznab"`, `"baseUrl"`, `"apiKey"`} {
		if !strings.Contains(string(indexers), needle) {
			t.Errorf("prowlarr indexers missing %q", needle)
		}
	}

	status, apps, _ := getBody(t, ts.Client(), ts.URL+"/prowlarr/api/v1/applications", headers)
	if status != http.StatusOK {
		t.Fatalf("prowlarr applications = %d", status)
	}
	for _, needle := range []string{`"Sonarr"`, `"syncLevel":"full"`, `"baseUrl"`} {
		if !strings.Contains(string(apps), needle) {
			t.Errorf("prowlarr applications missing %q", needle)
		}
	}
}

func TestSonarrAddUpdateDeleteList(t *testing.T) {
	_, ts := newTestServer(t)
	headers := map[string]string{"X-Api-Key": "demo-sonarr", "Content-Type": "application/json"}

	status, _, _ := getBody(t, ts.Client(), ts.URL+"/sonarr/api/v3/system/status", nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("sonarr without key = %d", status)
	}

	status, body, _ := getBody(t, ts.Client(), ts.URL+"/sonarr/api/v3/system/status", headers)
	if status != http.StatusOK || !strings.Contains(string(body), `"appName":"Sonarr"`) {
		t.Fatalf("status = %d %s", status, body)
	}

	status, schema, _ := getBody(t, ts.Client(), ts.URL+"/sonarr/api/v3/indexer/schema", headers)
	if status != http.StatusOK || !strings.Contains(string(schema), "TorznabSettings") {
		t.Fatalf("schema = %d %s", status, schema)
	}

	create := `{"name":"indexarr · Sim Torznab","implementation":"Torznab",` +
		`"configContract":"TorznabSettings","protocol":"torrent","priority":25,` +
		`"fields":[{"name":"baseUrl","value":"http://127.0.0.1:18484/torznab/sim-torznab/api"}]}`
	resp, err := ts.Client().Do(jsonRequest(t, http.MethodPost, ts.URL+"/sonarr/api/v3/indexer", create, headers))
	if err != nil {
		t.Fatal(err)
	}
	createdBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create = %d %s", resp.StatusCode, createdBody)
	}
	var created map[string]any
	if err := json.Unmarshal(createdBody, &created); err != nil {
		t.Fatal(err)
	}
	id, ok := created["id"].(float64)
	if !ok || id <= 0 {
		t.Fatalf("created id = %v", created["id"])
	}
	idText := strconv.Itoa(int(id))

	status, list, _ := getBody(t, ts.Client(), ts.URL+"/sonarr/api/v3/indexer", headers)
	if status != http.StatusOK || !strings.Contains(string(list), "indexarr · Sim Torznab") {
		t.Fatalf("list = %d %s", status, list)
	}

	status, one, _ := getBody(t, ts.Client(), ts.URL+"/sonarr/api/v3/indexer/"+idText, headers)
	if status != http.StatusOK || !strings.Contains(string(one), "sim-torznab") {
		t.Fatalf("get = %d %s", status, one)
	}

	update := `{"name":"indexarr · Sim Torznab","implementation":"Torznab",` +
		`"configContract":"TorznabSettings","protocol":"torrent","priority":10,` +
		`"fields":[{"name":"baseUrl","value":"http://127.0.0.1:18484/torznab/sim-torznab/api"}]}`
	resp, err = ts.Client().Do(jsonRequest(t, http.MethodPut, ts.URL+"/sonarr/api/v3/indexer/"+idText, update, headers))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("update = %d", resp.StatusCode)
	}
	_, updated, _ := getBody(t, ts.Client(), ts.URL+"/sonarr/api/v3/indexer/"+idText, headers)
	if !strings.Contains(string(updated), `"priority":10`) {
		t.Errorf("updated payload = %s", updated)
	}

	resp, err = ts.Client().Do(jsonRequest(t, http.MethodDelete, ts.URL+"/sonarr/api/v3/indexer/"+idText, "", headers))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete = %d", resp.StatusCode)
	}
	status, list, _ = getBody(t, ts.Client(), ts.URL+"/sonarr/api/v3/indexer", headers)
	if status != http.StatusOK || strings.Contains(string(list), "sim-torznab") {
		t.Errorf("after delete list = %s", list)
	}
	status, _, _ = getBody(t, ts.Client(), ts.URL+"/sonarr/api/v3/indexer/"+idText, headers)
	if status != http.StatusNotFound {
		t.Errorf("get after delete = %d, want 404", status)
	}
}

func TestDownloadClientEndpoints(t *testing.T) {
	server, ts := newTestServer(t)

	t.Run("sabnzbd", func(t *testing.T) {
		status, body, _ := getBody(t, ts.Client(), ts.URL+"/sabnzbd/api?mode=version&apikey=demo-sab", nil)
		if status != http.StatusOK || !strings.Contains(string(body), "4.3.0-sim") {
			t.Fatalf("version = %d %s", status, body)
		}

		form := url.Values{
			"mode":    {"addurl"},
			"name":    {"http://127.0.0.1:19080/newznab/api?t=get&id=demo-nzb-3"},
			"nzbname": {"Sim.NZB"},
			"apikey":  {"demo-sab"},
		}
		resp, err := ts.Client().PostForm(ts.URL+"/sabnzbd/api", form)
		if err != nil {
			t.Fatal(err)
		}
		body, _ = io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"status":true`) {
			t.Fatalf("addurl = %d %s", resp.StatusCode, body)
		}

		var payload bytes.Buffer
		writer := multipart.NewWriter(&payload)
		_ = writer.WriteField("mode", "addfile")
		_ = writer.WriteField("nzbname", "Sim.File")
		part, err := writer.CreateFormFile("name", "Sim.File.nzb")
		if err != nil {
			t.Fatal(err)
		}
		_, _ = part.Write([]byte(`<nzb/>`))
		_ = writer.Close()
		req, err := http.NewRequest(http.MethodPost, ts.URL+"/sabnzbd/api", &payload)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", writer.FormDataContentType())
		resp, err = ts.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ = io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"status":true`) {
			t.Fatalf("addfile = %d %s", resp.StatusCode, body)
		}
	})

	t.Run("nzbget", func(t *testing.T) {
		body := `{"method":"version","params":[],"id":1}`
		resp, err := ts.Client().Post(ts.URL+"/nzbget/jsonrpc", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		version, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || !strings.Contains(string(version), `"21.1-sim"`) {
			t.Fatalf("version = %d %s", resp.StatusCode, version)
		}

		appendBody := `{"method":"append","params":["Sim.NZB","PHN6LW56YiA=","tv",0,false,"",0,"SCORE"],"id":1}`
		resp, err = ts.Client().Post(ts.URL+"/nzbget/jsonrpc", "application/json", strings.NewReader(appendBody))
		if err != nil {
			t.Fatal(err)
		}
		appended, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || !strings.Contains(string(appended), `"result":1`) {
			t.Fatalf("append = %d %s", resp.StatusCode, appended)
		}
	})

	t.Run("deluge", func(t *testing.T) {
		jar, _ := cookiejar.New(nil)
		client := &http.Client{Jar: jar}
		resp, err := client.Post(ts.URL+"/deluge/json", "application/json",
			strings.NewReader(`{"method":"auth.login","params":["demo","demo"],"id":1}`))
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"result":true`) {
			t.Fatalf("login = %d %s", resp.StatusCode, body)
		}

		resp, err = client.Post(ts.URL+"/deluge/json", "application/json",
			strings.NewReader(`{"method":"core.add_torrent_magnet","params":["magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567"],"id":1}`))
		if err != nil {
			t.Fatal(err)
		}
		body, _ = io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "demo-deluge-hash") {
			t.Fatalf("add magnet = %d %s", resp.StatusCode, body)
		}
	})

	t.Run("rtorrent", func(t *testing.T) {
		listBody := `<?xml version="1.0"?><methodCall><methodName>system.listMethods</methodName><params></params></methodCall>`
		resp, err := ts.Client().Post(ts.URL+"/rtorrent/RPC2", "text/xml", strings.NewReader(listBody))
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "system.listMethods") {
			t.Fatalf("listMethods = %d %s", resp.StatusCode, body)
		}

		loadBody := `<?xml version="1.0"?><methodCall><methodName>load.start</methodName><params>` +
			`<param><value><string></string></value></param>` +
			`<param><value><string>magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567</string></value></param>` +
			`</params></methodCall>`
		resp, err = ts.Client().Post(ts.URL+"/rtorrent/RPC2", "text/xml", strings.NewReader(loadBody))
		if err != nil {
			t.Fatal(err)
		}
		body, _ = io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "<i4>0</i4>") {
			t.Fatalf("load.start = %d %s", resp.StatusCode, body)
		}
	})

	status, rawState, _ := getBody(t, ts.Client(), ts.URL+"/debug/state", nil)
	if status != http.StatusOK {
		t.Fatalf("debug state = %d", status)
	}
	var state State
	if err := json.Unmarshal(rawState, &state); err != nil {
		t.Fatal(err)
	}
	if len(state.Sabnzbd) != 2 {
		t.Errorf("sabnzbd records = %d, want 2", len(state.Sabnzbd))
	}
	if len(state.NZBGet) != 1 || state.NZBGet[0].Bytes == 0 {
		t.Errorf("nzbget records = %+v", state.NZBGet)
	}
	if len(state.Deluge) != 1 || state.Deluge[0].Magnet == "" {
		t.Errorf("deluge records = %+v", state.Deluge)
	}
	if len(state.RTorrent) != 1 || state.RTorrent[0].Magnet == "" {
		t.Errorf("rtorrent records = %+v", state.RTorrent)
	}
	if server.state.TorznabSearches != 0 {
		t.Errorf("unexpected torznab searches: %d", server.state.TorznabSearches)
	}
}

func TestDebugStateExposesSonarrAndTorznab(t *testing.T) {
	server, ts := newTestServer(t)

	_, _, _ = getBody(t, ts.Client(), ts.URL+"/torznab/api?t=search&apikey=demo-key", nil)
	_, _, _ = getBody(t, ts.Client(), ts.URL+"/torznab/api?t=get&id=2&apikey=demo-key", nil)

	create := `{"name":"indexarr · Sim Torznab","implementation":"Torznab","configContract":"TorznabSettings"}`
	headers := map[string]string{"X-Api-Key": "demo-sonarr", "Content-Type": "application/json"}
	resp, err := ts.Client().Do(jsonRequest(t, http.MethodPost, ts.URL+"/sonarr/api/v3/indexer", create, headers))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	status, rawState, _ := getBody(t, ts.Client(), ts.URL+"/debug/state", nil)
	if status != http.StatusOK {
		t.Fatalf("debug state = %d", status)
	}
	var state State
	if err := json.Unmarshal(rawState, &state); err != nil {
		t.Fatal(err)
	}
	if state.TorznabSearches != 1 || state.TorznabDownloads != 1 {
		t.Errorf("torznab counters = %d/%d", state.TorznabSearches, state.TorznabDownloads)
	}
	if len(state.SonarrIndexers) != 1 || state.SonarrIndexers[0]["name"] != "indexarr · Sim Torznab" {
		t.Errorf("sonarr indexers = %+v", state.SonarrIndexers)
	}
	if server.state.ProwlarrRequests != 0 {
		t.Errorf("unexpected prowlarr requests: %d", server.state.ProwlarrRequests)
	}
}

func TestRealClientsAgainstMock(t *testing.T) {
	_, ts := newTestServer(t)
	ctx := context.Background()

	sab, err := downloadclient.New(downloadclient.Config{
		Kind:   downloadclient.KindSABnzbd,
		URL:    ts.URL + "/sabnzbd",
		APIKey: "demo-sab",
	})
	if err != nil {
		t.Fatal(err)
	}
	if description, err := sab.Test(ctx); err != nil || !strings.Contains(description, "4.3.0-sim") {
		t.Fatalf("SABnzbd Test = %q, %v", description, err)
	}
	if err := sab.Send(ctx, downloadclient.Item{Title: "Sim.NZB", NZB: []byte(`<nzb/>`)}); err != nil {
		t.Fatalf("SABnzbd Send: %v", err)
	}

	nzbget, err := downloadclient.New(downloadclient.Config{
		Kind: downloadclient.KindNZBGet,
		URL:  ts.URL + "/nzbget",
	})
	if err != nil {
		t.Fatal(err)
	}
	if description, err := nzbget.Test(ctx); err != nil || !strings.Contains(description, "21.1-sim") {
		t.Fatalf("NZBGet Test = %q, %v", description, err)
	}
	if err := nzbget.Send(ctx, downloadclient.Item{Title: "Sim.NZB", NZB: []byte(`<nzb/>`)}); err != nil {
		t.Fatalf("NZBGet Send: %v", err)
	}

	deluge, err := downloadclient.New(downloadclient.Config{
		Kind:     downloadclient.KindDeluge,
		URL:      ts.URL + "/deluge",
		Username: "demo",
		Password: "demo",
	})
	if err != nil {
		t.Fatal(err)
	}
	if description, err := deluge.Test(ctx); err != nil || !strings.Contains(description, "Deluge OK") {
		t.Fatalf("Deluge Test = %q, %v", description, err)
	}
	if err := deluge.Send(ctx, downloadclient.Item{
		Title:  "Sim.Magnet",
		Magnet: "magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567",
	}); err != nil {
		t.Fatalf("Deluge Send: %v", err)
	}

	rtorrent, err := downloadclient.New(downloadclient.Config{
		Kind: downloadclient.KindRTorrent,
		URL:  ts.URL + "/rtorrent/RPC2",
	})
	if err != nil {
		t.Fatal(err)
	}
	if description, err := rtorrent.Test(ctx); err != nil || !strings.Contains(description, "rTorrent OK") {
		t.Fatalf("rTorrent Test = %q, %v", description, err)
	}
	if err := rtorrent.Send(ctx, downloadclient.Item{
		Title:  "Sim.Magnet",
		Magnet: "magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567",
	}); err != nil {
		t.Fatalf("rTorrent Send: %v", err)
	}

	sonarr, err := appsync.New(appsync.Config{
		Kind:      appsync.KindSonarr,
		URL:       ts.URL + "/sonarr",
		APIKey:    "demo-sonarr",
		SyncLevel: "full",
	})
	if err != nil {
		t.Fatal(err)
	}
	if description, err := sonarr.Test(ctx); err != nil || !strings.Contains(description, "Sonarr 4.0.0-sim") {
		t.Fatalf("Sonarr Test = %q, %v", description, err)
	}
	spec := appsync.IndexerSpec{
		Name:                    "indexarr · Sim Client",
		Implementation:          "Torznab",
		ConfigContract:          "TorznabSettings",
		BaseURL:                 "http://127.0.0.1:18484/torznab/sim-torznab/api",
		APIKey:                  "indexarr-key",
		Categories:              []int{5000},
		Priority:                25,
		Protocol:                "torrent",
		EnableRSS:               true,
		EnableAutomaticSearch:   true,
		EnableInteractiveSearch: true,
	}
	created, err := sonarr.AddIndexer(ctx, spec)
	if err != nil || created.ID == 0 {
		t.Fatalf("Sonarr AddIndexer = %+v, %v", created, err)
	}
	list, err := sonarr.ListIndexers(ctx)
	if err != nil || len(list) != 1 || list[0].Name != spec.Name {
		t.Fatalf("Sonarr ListIndexers = %+v, %v", list, err)
	}
	spec.Priority = 10
	updated, err := sonarr.UpdateIndexer(ctx, created.ID, spec)
	if err != nil || updated.Priority != 10 {
		t.Fatalf("Sonarr UpdateIndexer = %+v, %v", updated, err)
	}
	if err := sonarr.DeleteIndexer(ctx, created.ID); err != nil {
		t.Fatalf("Sonarr DeleteIndexer: %v", err)
	}
	list, err = sonarr.ListIndexers(ctx)
	if err != nil || len(list) != 0 {
		t.Fatalf("Sonarr ListIndexers after delete = %+v, %v", list, err)
	}
}

func jsonRequest(t *testing.T, method, rawURL, body string, headers map[string]string) *http.Request {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, rawURL, reader)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	return req
}
