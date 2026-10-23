package family

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/buzzqw/indexarr/internal/engine"
)

type trackerRequest struct {
	Method string
	Path   string
	Query  url.Values
	Form   url.Values
	Cookie string
}

type trackerRequestLog struct {
	mu      sync.Mutex
	entries []trackerRequest
}

func (l *trackerRequestLog) add(r *http.Request) {
	_ = r.ParseForm()
	entry := trackerRequest{
		Method: r.Method,
		Path:   r.URL.Path,
		Query:  cloneValues(r.URL.Query()),
		Form:   cloneValues(r.PostForm),
		Cookie: r.Header.Get("Cookie"),
	}
	l.mu.Lock()
	l.entries = append(l.entries, entry)
	l.mu.Unlock()
}

func cloneValues(values url.Values) url.Values {
	if values == nil {
		return nil
	}
	out := make(url.Values, len(values))
	for key, list := range values {
		out[key] = append([]string(nil), list...)
	}
	return out
}

func (l *trackerRequestLog) all() []trackerRequest {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]trackerRequest(nil), l.entries...)
}

func (l *trackerRequestLog) count(method, path string) int {
	total := 0
	for _, entry := range l.all() {
		if entry.Method == method && entry.Path == path {
			total++
		}
	}
	return total
}

func (l *trackerRequestLog) find(method, path string) (trackerRequest, bool) {
	for _, entry := range l.all() {
		if entry.Method == method && entry.Path == path {
			return entry, true
		}
	}
	return trackerRequest{}, false
}

func (l *trackerRequestLog) queryValues(key string) []string {
	var out []string
	for _, entry := range l.all() {
		if entry.Method != http.MethodGet || entry.Query == nil {
			continue
		}
		out = append(out, entry.Query.Get(key))
	}
	return out
}

func (l *trackerRequestLog) queryValuesFor(path, key string) []string {
	var out []string
	for _, entry := range l.all() {
		if entry.Method != http.MethodGet || entry.Path != path || entry.Query == nil {
			continue
		}
		out = append(out, entry.Query.Get(key))
	}
	return out
}

func TestTorrentTraderSearchLoginCookiesAndParsing(t *testing.T) {
	loginFixture := readFixture(t, "torrenttrader_login.html")
	searchFixture := readFixture(t, "torrenttrader_search.html")
	log := &trackerRequestLog{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.add(r)
		switch {
		case r.URL.Path == "/takelogin.php" && r.Method == http.MethodGet:
			http.SetCookie(w, &http.Cookie{Name: "sid", Value: "pre-session", Path: "/"})
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write(loginFixture)
		case r.URL.Path == "/takelogin.php" && r.Method == http.MethodPost:
			http.SetCookie(w, &http.Cookie{Name: "uid", Value: "42", Path: "/"})
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, `<html><body><a href="logout.php">Logout</a></body></html>`)
		case r.URL.Path == "/torrents.php":
			if !strings.Contains(r.Header.Get("Cookie"), "uid=42") {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "text/html")
			if r.URL.Query().Get("page") == "1" {
				_, _ = w.Write(searchFixture)
				return
			}
			_, _ = io.WriteString(w, `<html><body><table class="table"><tbody></tbody></table></body></html>`)
		case r.URL.Path == "/":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write(loginFixture)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	connector, err := NewTorrentTrader(TorrentTraderConfig{
		Indexer:    engine.IndexerRef{Slug: "tt"},
		BaseURL:    server.URL,
		Username:   "alice",
		Password:   "hunter2",
		Passkey:    "pk-secret",
		Transport:  newTestTransport(),
		Categories: []int{5},
		UserAgent:  "tt-test-agent",
	})
	if err != nil {
		t.Fatalf("NewTorrentTrader: %v", err)
	}

	items, err := connector.Search(context.Background(), engine.SearchRequest{
		Mode:  engine.ModeSearch,
		Query: "Example",
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2", len(items))
	}

	login, ok := log.find(http.MethodPost, "/takelogin.php")
	if !ok {
		t.Fatal("login POST not observed")
	}
	checks := map[string]string{
		"username":   "alice",
		"password":   "hunter2",
		"csrf_token": "tt-csrf-token",
		"returnto":   "/torrents.php",
	}
	for key, want := range checks {
		if got := login.Form.Get(key); got != want {
			t.Errorf("login form %s = %q, want %q", key, got, want)
		}
	}

	search, ok := log.find(http.MethodGet, "/torrents.php")
	if !ok {
		t.Fatal("search request not observed")
	}
	if search.Query.Get("search") != "Example" {
		t.Errorf("search = %q", search.Query.Get("search"))
	}
	if search.Query.Get("cat") != "5" {
		t.Errorf("cat = %q, want 5", search.Query.Get("cat"))
	}
	if search.Query.Get("page") != "1" {
		t.Errorf("page = %q", search.Query.Get("page"))
	}
	if !strings.Contains(search.Cookie, "uid=42") || !strings.Contains(search.Cookie, "sid=pre-session") {
		t.Errorf("search cookie = %q, want the login session", search.Cookie)
	}
	if got := search.Query["search"]; len(got) != 1 {
		t.Errorf("search parameter sent %d times", len(got))
	}

	first := items[0]
	if first.Title != "Example.Movie.2024.1080p.WEB-DL.x264-GROUP" {
		t.Errorf("title = %q", first.Title)
	}
	if first.GUID != "101" {
		t.Errorf("guid = %q, want 101", first.GUID)
	}
	if first.Size != 1610612736 {
		t.Errorf("size = %d, want 1610612736", first.Size)
	}
	if first.Seeders != 12 || first.Leechers != 3 {
		t.Errorf("seeders/leechers = %d/%d", first.Seeders, first.Leechers)
	}
	if got := first.Published.Format("2006-01-02 15:04:05"); got != "2024-05-06 07:08:09" {
		t.Errorf("published = %q", got)
	}
	if len(first.Categories) != 1 || first.Categories[0] != 5 {
		t.Errorf("categories = %v, want [5]", first.Categories)
	}
	if !strings.Contains(first.DetailsURL, "details.php?id=101") {
		t.Errorf("details = %q", first.DetailsURL)
	}
	if !strings.Contains(first.DownloadURL, "download.php?id=101") || !strings.Contains(first.DownloadURL, "passkey=pk-secret") {
		t.Errorf("download = %q", first.DownloadURL)
	}

	second := items[1]
	if second.Size != 734527488 {
		t.Errorf("second size = %d, want 734527488", second.Size)
	}
	if second.Seeders != 1024 || second.Leechers != 8 {
		t.Errorf("second seeders/leechers = %d/%d", second.Seeders, second.Leechers)
	}
	if got := second.Published.Format("2006-01-02"); got != "2024-05-06" {
		t.Errorf("second published = %q", got)
	}
	if second.Attributes["category"] != "TV" {
		t.Errorf("category attribute = %q", second.Attributes["category"])
	}
	if len(second.Categories) != 1 || second.Categories[0] != 12 {
		t.Errorf("second categories = %v, want [12]", second.Categories)
	}

	pages := log.queryValuesFor("/torrents.php", "page")
	if strings.Join(pages, ",") != "1,2" {
		t.Errorf("requested pages = %v, want [1 2]", pages)
	}

	// The second search must reuse the cached login.
	if _, err := connector.Search(context.Background(), engine.SearchRequest{Query: "Example", Limit: 1}); err != nil {
		t.Fatalf("second Search: %v", err)
	}
	if got := log.count(http.MethodPost, "/takelogin.php"); got != 1 {
		t.Errorf("login POST count = %d, want 1 (login cache)", got)
	}
}

func TestTorrentTraderReloginOnUnauthorized(t *testing.T) {
	loginFixture := readFixture(t, "torrenttrader_login.html")
	searchFixture := readFixture(t, "torrenttrader_search.html")
	log := &trackerRequestLog{}
	var searchCalls atomic.Int64

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.add(r)
		switch {
		case r.URL.Path == "/takelogin.php" && r.Method == http.MethodGet:
			_, _ = w.Write(loginFixture)
		case r.URL.Path == "/takelogin.php" && r.Method == http.MethodPost:
			http.SetCookie(w, &http.Cookie{Name: "uid", Value: "42", Path: "/"})
			_, _ = io.WriteString(w, "ok")
		case r.URL.Path == "/torrents.php":
			if searchCalls.Add(1) == 1 {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if r.URL.Query().Get("page") == "1" {
				_, _ = w.Write(searchFixture)
				return
			}
			_, _ = io.WriteString(w, `<table class="table"><tbody></tbody></table>`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	connector, err := NewTorrentTrader(TorrentTraderConfig{
		BaseURL:   server.URL,
		Username:  "alice",
		Password:  "hunter2",
		Transport: newTestTransport(),
	})
	if err != nil {
		t.Fatalf("NewTorrentTrader: %v", err)
	}
	items, err := connector.Search(context.Background(), engine.SearchRequest{Query: "Example", Limit: 10})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2", len(items))
	}
	if got := log.count(http.MethodPost, "/takelogin.php"); got != 2 {
		t.Errorf("login POST count = %d, want 2 after a 401", got)
	}
}

func TestTorrentTraderPaginationDedupeAndLimit(t *testing.T) {
	row := func(id, title, category string) string {
		return `<tr>` +
			`<td class="category"><a href="torrents.php?cat=` + category + `">Cat</a></td>` +
			`<td class="name"><a href="details.php?id=` + id + `">` + title + `</a></td>` +
			`<td class="size">1 GB</td><td class="seeders">1</td><td class="leechers">1</td>` +
			`<td class="added">2024-01-01 00:00:00</td>` +
			`<td class="download"><a href="download.php?id=` + id + `">DL</a></td></tr>`
	}
	page := func(rows ...string) string {
		return `<table class="table"><tbody>` + strings.Join(rows, "") + `</tbody></table>`
	}

	log := &trackerRequestLog{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.add(r)
		switch r.URL.Query().Get("page") {
		case "1":
			_, _ = io.WriteString(w, page(row("301", "One", "5"), row("302", "Two", "5")))
		case "2":
			_, _ = io.WriteString(w, page(row("301", "One", "5"), row("303", "Three", "5")))
		default:
			_, _ = io.WriteString(w, page())
		}
	}))
	defer server.Close()

	connector, err := NewTorrentTrader(TorrentTraderConfig{
		BaseURL:   server.URL,
		Transport: newTestTransport(),
	})
	if err != nil {
		t.Fatalf("NewTorrentTrader: %v", err)
	}
	items, err := connector.Search(context.Background(), engine.SearchRequest{Query: "x", Limit: 10})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("got %d items, want 3 unique", len(items))
	}
	if got := strings.Join(log.queryValuesFor("/torrents.php", "page"), ","); got != "1,2,3" {
		t.Errorf("pages = %q, want 1,2,3", got)
	}

	// A hard limit stops pagination as soon as it is satisfied.
	log = &trackerRequestLog{}
	items, err = connector.Search(context.Background(), engine.SearchRequest{Query: "x", Limit: 2})
	if err != nil {
		t.Fatalf("limited Search: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2", len(items))
	}
	if got := strings.Join(log.queryValuesFor("/torrents.php", "page"), ","); got != "1" {
		t.Errorf("pages with limit 2 = %q, want 1", got)
	}
}

func TestTorrentTraderMaxFivePages(t *testing.T) {
	log := &trackerRequestLog{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.add(r)
		page := r.URL.Query().Get("page")
		_, _ = io.WriteString(w, `<table class="table"><tbody><tr>`+
			`<td><a href="details.php?id=`+page+`">Title `+page+`</a></td>`+
			`<td class="size">1 MB</td><td class="seeders">1</td><td class="leechers">0</td>`+
			`<td class="added">2024-01-01</td><td><a href="download.php?id=`+page+`">DL</a></td>`+
			`</tr></tbody></table>`)
	}))
	defer server.Close()

	connector, err := NewTorrentTrader(TorrentTraderConfig{BaseURL: server.URL, Transport: newTestTransport()})
	if err != nil {
		t.Fatalf("NewTorrentTrader: %v", err)
	}
	items, err := connector.Search(context.Background(), engine.SearchRequest{Query: "x"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) != 5 {
		t.Fatalf("got %d items, want 5 (page cap)", len(items))
	}
	if got := strings.Join(log.queryValuesFor("/torrents.php", "page"), ","); got != "1,2,3,4,5" {
		t.Errorf("pages = %q", got)
	}
}

func TestTorrentTraderLatest(t *testing.T) {
	fixture := readFixture(t, "torrenttrader_search.html")
	log := &trackerRequestLog{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.add(r)
		if r.URL.Query().Get("page") == "1" {
			_, _ = w.Write(fixture)
			return
		}
		_, _ = io.WriteString(w, `<table class="table"><tbody></tbody></table>`)
	}))
	defer server.Close()

	connector, err := NewTorrentTrader(TorrentTraderConfig{
		BaseURL:    server.URL,
		Transport:  newTestTransport(),
		Categories: []int{5},
	})
	if err != nil {
		t.Fatalf("NewTorrentTrader: %v", err)
	}
	items, err := connector.Latest(context.Background(), engine.LatestRequest{Limit: 10})
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2", len(items))
	}
	query := log.all()[0].Query
	if query.Get("search") != "" {
		t.Errorf("latest sent a search term: %v", query)
	}
	if query.Get("cat") != "5" {
		t.Errorf("latest cat = %q", query.Get("cat"))
	}
}

func TestTorrentTraderDownloadDirectMagnetAndPasskey(t *testing.T) {
	const magnet = "magnet:?xt=urn:btih:0123456789abcdef&dn=Example"
	log := &trackerRequestLog{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.add(r)
		switch r.URL.Path {
		case "/files/release.torrent":
			if r.URL.Query().Get("passkey") != "pk-secret" {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			w.Header().Set("Content-Type", "application/x-bittorrent")
			_, _ = io.WriteString(w, "d8:announce5:https")
		case "/redirect-magnet":
			w.Header().Set("Location", magnet)
			w.WriteHeader(http.StatusFound)
		case "/download.php":
			if r.URL.Query().Get("id") != "9" || r.URL.Query().Get("passkey") != "pk-secret" {
				http.Error(w, "bad params", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/x-bittorrent")
			_, _ = io.WriteString(w, "torrent-bytes")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	connector, err := NewTorrentTrader(TorrentTraderConfig{
		BaseURL:   server.URL,
		Passkey:   "pk-secret",
		Transport: newTestTransport(),
	})
	if err != nil {
		t.Fatalf("NewTorrentTrader: %v", err)
	}

	payload, err := connector.Download(context.Background(), engine.Item{
		Title:       "Example.Release",
		DownloadURL: server.URL + "/files/release.torrent",
	})
	if err != nil {
		t.Fatalf("Download direct: %v", err)
	}
	if string(payload.Body) != "d8:announce5:https" {
		t.Errorf("body = %q", payload.Body)
	}
	if payload.ContentType != "application/x-bittorrent" || payload.Filename != "Example.Release.torrent" {
		t.Errorf("payload metadata = %q/%q", payload.ContentType, payload.Filename)
	}

	payload, err = connector.Download(context.Background(), engine.Item{
		Title:       "Example.Release",
		DownloadURL: server.URL + "/redirect-magnet",
	})
	if err != nil {
		t.Fatalf("Download magnet redirect: %v", err)
	}
	if payload.Magnet != magnet || len(payload.Body) != 0 {
		t.Errorf("magnet payload = %q/%d bytes", payload.Magnet, len(payload.Body))
	}

	payload, err = connector.Download(context.Background(), engine.Item{
		Title:  "Example.Release",
		Magnet: magnet,
	})
	if err != nil {
		t.Fatalf("Download magnet item: %v", err)
	}
	if payload.Magnet != magnet {
		t.Errorf("magnet = %q", payload.Magnet)
	}

	payload, err = connector.Download(context.Background(), engine.Item{
		Title:      "Example.Release",
		Attributes: map[string]string{"id": "9"},
	})
	if err != nil {
		t.Fatalf("Download by id: %v", err)
	}
	if string(payload.Body) != "torrent-bytes" {
		t.Errorf("derived download body = %q", payload.Body)
	}
}

func TestTorrentTraderDownloadErrorRedaction(t *testing.T) {
	const passkey = "super-secret-passkey"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "exploded for "+r.URL.String(), http.StatusInternalServerError)
	}))
	defer server.Close()

	connector, err := NewTorrentTrader(TorrentTraderConfig{
		BaseURL:   server.URL,
		Passkey:   passkey,
		Transport: newTestTransport(),
	})
	if err != nil {
		t.Fatalf("NewTorrentTrader: %v", err)
	}
	_, err = connector.Download(context.Background(), engine.Item{
		Title:      "Example.Release",
		Attributes: map[string]string{"id": "7"},
	})
	if err == nil {
		t.Fatal("Download succeeded, want an error")
	}
	if strings.Contains(err.Error(), passkey) {
		t.Fatalf("error leaked the passkey: %v", err)
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error = %v, want the HTTP status", err)
	}
}

func TestTorrentTraderTestPhases(t *testing.T) {
	loginFixture := readFixture(t, "torrenttrader_login.html")
	searchFixture := readFixture(t, "torrenttrader_search.html")
	log := &trackerRequestLog{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.add(r)
		switch {
		case r.URL.Path == "/takelogin.php" && r.Method == http.MethodGet:
			_, _ = w.Write(loginFixture)
		case r.URL.Path == "/takelogin.php":
			http.SetCookie(w, &http.Cookie{Name: "uid", Value: "42", Path: "/"})
			_, _ = io.WriteString(w, "welcome")
		case r.URL.Path == "/torrents.php":
			if r.URL.Query().Get("page") == "1" {
				_, _ = w.Write(searchFixture)
				return
			}
			_, _ = io.WriteString(w, `<table class="table"><tbody></tbody></table>`)
		default:
			_, _ = w.Write(loginFixture)
		}
	}))
	defer server.Close()

	connector, err := NewTorrentTrader(TorrentTraderConfig{
		BaseURL:   server.URL,
		Username:  "alice",
		Password:  "hunter2",
		Passkey:   "pk-secret",
		Transport: newTestTransport(),
	})
	if err != nil {
		t.Fatalf("NewTorrentTrader: %v", err)
	}
	result, err := connector.Test(context.Background())
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	if !result.OK {
		t.Fatalf("Test not OK: %+v", result.Phases)
	}
	names := make([]string, 0, len(result.Phases))
	for _, phase := range result.Phases {
		names = append(names, phase.Name)
	}
	if strings.Join(names, ",") != "reach,login,search" {
		t.Errorf("phases = %v", names)
	}
	for _, phase := range result.Phases {
		if phase.Message != "" && strings.Contains(phase.Message, "hunter2") {
			t.Fatalf("phase %q leaked the password: %s", phase.Name, phase.Message)
		}
	}
}

func TestTorrentTraderTestReportsLoginFailure(t *testing.T) {
	loginFixture := readFixture(t, "torrenttrader_login.html")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/takelogin.php" && r.Method == http.MethodGet:
			_, _ = w.Write(loginFixture)
		case r.URL.Path == "/takelogin.php":
			_, _ = io.WriteString(w, `<div class="error">Login failed: invalid username or password</div>`)
		default:
			_, _ = w.Write(loginFixture)
		}
	}))
	defer server.Close()

	connector, err := NewTorrentTrader(TorrentTraderConfig{
		BaseURL:   server.URL,
		Username:  "alice",
		Password:  "hunter2",
		Transport: newTestTransport(),
	})
	if err != nil {
		t.Fatalf("NewTorrentTrader: %v", err)
	}
	result, err := connector.Test(context.Background())
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	if result.OK {
		t.Fatal("Test reported OK with rejected credentials")
	}
	byName := map[string]engine.TestPhase{}
	for _, phase := range result.Phases {
		byName[phase.Name] = phase
	}
	if byName["login"].OK {
		t.Errorf("login phase = %+v, want failure", byName["login"])
	}
	if strings.Contains(byName["login"].Message, "hunter2") {
		t.Errorf("login phase leaked the password: %s", byName["login"].Message)
	}
}

func TestTorrentTraderFactoryAndValidation(t *testing.T) {
	var gotPasskey atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPasskey.Store(r.URL.Query().Get("passkey"))
		w.Header().Set("Content-Type", "application/x-bittorrent")
		_, _ = io.WriteString(w, "torrent")
	}))
	defer server.Close()

	connector, err := TorrentTraderFactory(engine.Config{
		Indexer: engine.IndexerRef{Slug: "tt"},
		Settings: map[string]string{
			"url":        server.URL,
			"api_key":    "factory-key",
			"categories": "5, 12",
		},
		Transport: newTestTransport(),
	})
	if err != nil {
		t.Fatalf("TorrentTraderFactory: %v", err)
	}
	tracker, ok := connector.(*TorrentTrader)
	if !ok {
		t.Fatalf("factory returned %T", connector)
	}
	if len(tracker.Capabilities().Categories) != 2 {
		t.Errorf("categories = %v", tracker.Capabilities().Categories)
	}
	if _, err := connector.Download(context.Background(), engine.Item{
		Title:       "Example",
		DownloadURL: server.URL + "/download.php?id=1",
	}); err != nil {
		t.Fatalf("Download: %v", err)
	}
	if gotPasskey.Load() != "factory-key" {
		t.Errorf("passkey = %q, want the api_key fallback", gotPasskey.Load())
	}

	if _, err := TorrentTraderFactory(engine.Config{
		Settings: map[string]string{"base_url": server.URL, "categories": "not-a-number"},
	}); err == nil {
		t.Error("invalid categories did not fail")
	}
	if _, err := NewTorrentTrader(TorrentTraderConfig{Transport: newTestTransport()}); err == nil {
		t.Error("missing base URL did not fail")
	}
	if _, err := NewTorrentTrader(TorrentTraderConfig{BaseURL: "ftp://tracker.example", Transport: newTestTransport()}); err == nil {
		t.Error("non-HTTP base URL did not fail")
	}
	if _, err := NewTorrentTrader(TorrentTraderConfig{BaseURL: "https://tracker.example", Username: "alice", Transport: newTestTransport()}); err == nil {
		t.Error("half-configured credentials did not fail")
	}
	if _, err := NewTorrentTrader(TorrentTraderConfig{
		BaseURL:   "https://tracker.example",
		Transport: newTestTransport(),
		Profile:   TorrentTraderProfile{TitleSelector: "a["},
	}); err == nil {
		t.Error("invalid selector did not fail")
	}
}

func TestDefaultTorrentTraderProfile(t *testing.T) {
	profile := DefaultTorrentTraderProfile()
	checks := map[string]string{
		"login":    profile.LoginPath,
		"search":   profile.SearchPath,
		"rows":     profile.RowsSelector,
		"title":    profile.TitleSelector,
		"download": profile.DownloadSelector,
		"page":     profile.PageParam,
	}
	for name, value := range checks {
		if strings.TrimSpace(value) == "" {
			t.Errorf("default %s selector is empty", name)
		}
	}
	if profile.LoginPath != "takelogin.php" {
		t.Errorf("login path = %q", profile.LoginPath)
	}
	if profile.PageParam != "page" {
		t.Errorf("page param = %q", profile.PageParam)
	}
	if !strings.Contains(profile.SearchPath, "torrents.php") || !strings.Contains(profile.SearchPath, "search=") {
		t.Errorf("search path = %q", profile.SearchPath)
	}
	if !strings.Contains(profile.TitleSelector, "details.php?id=") {
		t.Errorf("title selector = %q", profile.TitleSelector)
	}
}

func TestTorrentTraderSearchModeValidation(t *testing.T) {
	connector, err := NewTorrentTrader(TorrentTraderConfig{
		BaseURL:    "https://tracker.example",
		Transport:  newTestTransport(),
		Categories: []int{5},
	})
	if err != nil {
		t.Fatalf("NewTorrentTrader: %v", err)
	}
	caps := connector.Capabilities()
	if caps.Protocol != engine.ProtocolTorrent {
		t.Errorf("protocol = %q", caps.Protocol)
	}
	for _, mode := range []engine.Mode{engine.ModeSearch, engine.ModeTV, engine.ModeMovie} {
		if !caps.Supports(mode) {
			t.Errorf("mode %q not declared", mode)
		}
	}
	if !caps.SupportsParam(engine.ModeTV, engine.ParamSeason) {
		t.Errorf("tv params = %v", caps.Params[engine.ModeTV])
	}
	if _, err := connector.Search(context.Background(), engine.SearchRequest{Mode: engine.ModeMusic}); err == nil {
		t.Error("unsupported mode did not fail")
	}
}

func TestTorrentTraderDateParsing(t *testing.T) {
	cases := map[string]time.Time{
		"2024-05-06 07:08:09": time.Date(2024, 5, 6, 7, 8, 9, 0, time.UTC),
		"06/05/2024 07:08":    time.Date(2024, 5, 6, 7, 8, 0, 0, time.UTC),
		"06-05-2024 07:08":    time.Date(2024, 5, 6, 7, 8, 0, 0, time.UTC),
	}
	for value, want := range cases {
		if got := trackerParseDate(value); !got.Equal(want) {
			t.Errorf("trackerParseDate(%q) = %s, want %s", value, got, want)
		}
	}
}
