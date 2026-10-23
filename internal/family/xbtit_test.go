package family

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/buzzqw/indexarr/internal/engine"
)

func TestXbtitSearchLoginFallbackAndParsing(t *testing.T) {
	loginFixture := readFixture(t, "xbtit_login.html")
	searchFixture := readFixture(t, "xbtit_search.html")
	log := &trackerRequestLog{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.add(r)
		switch {
		case r.URL.Path == "/index.php" && r.URL.Query().Get("page") == "login" && r.Method == http.MethodGet:
			// The default login route is unavailable: the connector must fall
			// back to takelogin.php.
			http.NotFound(w, r)
		case r.URL.Path == "/takelogin.php" && r.Method == http.MethodGet:
			http.SetCookie(w, &http.Cookie{Name: "xb_sid", Value: "pre-session", Path: "/"})
			_, _ = w.Write(loginFixture)
		case r.URL.Path == "/takelogin.php" && r.Method == http.MethodPost:
			http.SetCookie(w, &http.Cookie{Name: "uid", Value: "99", Path: "/"})
			_, _ = io.WriteString(w, `<html><body><a href="index.php?page=logout">Logout</a></body></html>`)
		case r.URL.Path == "/index.php" && r.URL.Query().Get("page") == "torrents":
			if !strings.Contains(r.Header.Get("Cookie"), "uid=99") {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if r.URL.Query().Get("pages") == "" || r.URL.Query().Get("pages") == "1" {
				_, _ = w.Write(searchFixture)
				return
			}
			_, _ = io.WriteString(w, `<table id="sortabletable"><tbody></tbody></table>`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	connector, err := NewXbtit(XbtitConfig{
		Indexer:    engine.IndexerRef{Slug: "xb"},
		BaseURL:    server.URL,
		Username:   "bob",
		Password:   "s3cret",
		Passkey:    "xb-passkey",
		Transport:  newTestTransport(),
		Categories: []int{10},
		UserAgent:  "xbtit-test-agent",
	})
	if err != nil {
		t.Fatalf("NewXbtit: %v", err)
	}

	items, err := connector.Search(context.Background(), engine.SearchRequest{
		Mode:  engine.ModeSearch,
		Query: "Show",
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
		"username":   "bob",
		"password":   "s3cret",
		"csrf_token": "xb-csrf-token",
		"returnto":   "index.php?page=torrents",
	}
	for key, want := range checks {
		if got := login.Form.Get(key); got != want {
			t.Errorf("login form %s = %q, want %q", key, got, want)
		}
	}

	var search trackerRequest
	var sawLoginRoute bool
	for _, entry := range log.all() {
		if entry.Method == http.MethodGet && entry.Path == "/index.php" && entry.Query.Get("page") == "login" {
			sawLoginRoute = true
		}
		if entry.Method == http.MethodGet && entry.Path == "/index.php" && entry.Query.Get("page") == "torrents" &&
			entry.Query.Get("pages") == "1" {
			search = entry
		}
	}
	if !sawLoginRoute {
		t.Error("default login route was never tried")
	}
	if search.Query.Get("search") != "Show" {
		t.Errorf("search = %q", search.Query.Get("search"))
	}
	if search.Query.Get("category") != "10" {
		t.Errorf("category = %q, want 10", search.Query.Get("category"))
	}
	if search.Query.Get("pages") != "1" {
		t.Errorf("pages = %q", search.Query.Get("pages"))
	}
	if search.Query.Get("page") != "torrents" {
		t.Errorf("route page = %q, want torrents", search.Query.Get("page"))
	}
	if !strings.Contains(search.Cookie, "uid=99") || !strings.Contains(search.Cookie, "xb_sid=pre-session") {
		t.Errorf("search cookie = %q", search.Cookie)
	}

	first := items[0]
	if first.Title != "Example.Show.S01E02.720p.HDTV.x264-GROUP" {
		t.Errorf("title = %q", first.Title)
	}
	if first.GUID != "201" {
		t.Errorf("guid = %q, want 201", first.GUID)
	}
	if first.Size != 367001600 {
		t.Errorf("size = %d, want 367001600", first.Size)
	}
	if first.Seeders != 42 || first.Leechers != 5 {
		t.Errorf("seeders/leechers = %d/%d", first.Seeders, first.Leechers)
	}
	if got := first.Published.Format("2006-01-02 15:04"); got != "2024-05-06 07:08" {
		t.Errorf("published = %q", got)
	}
	if len(first.Categories) != 1 || first.Categories[0] != 10 {
		t.Errorf("categories = %v, want [10]", first.Categories)
	}
	if !strings.Contains(first.DownloadURL, "page=download") ||
		!strings.Contains(first.DownloadURL, "id=201") ||
		!strings.Contains(first.DownloadURL, "passkey=xb-passkey") {
		t.Errorf("download = %q", first.DownloadURL)
	}

	second := items[1]
	if second.Size != 2415919104 {
		t.Errorf("second size = %d, want 2415919104", second.Size)
	}
	if second.Seeders != 7 || second.Leechers != 1 {
		t.Errorf("second seeders/leechers = %d/%d", second.Seeders, second.Leechers)
	}
	if got := second.Published.Format("2006-01-02"); got != "2024-01-02" {
		t.Errorf("second published = %q", got)
	}
	if len(second.Categories) != 1 || second.Categories[0] != 11 {
		t.Errorf("second categories = %v, want [11]", second.Categories)
	}
	if !strings.Contains(second.DownloadURL, "download.php?id=202") {
		t.Errorf("second download = %q", second.DownloadURL)
	}

	var pages []string
	for _, entry := range log.all() {
		if entry.Method == http.MethodGet && entry.Path == "/index.php" && entry.Query.Get("page") == "torrents" {
			pages = append(pages, entry.Query.Get("pages"))
		}
	}
	if strings.Join(pages, ",") != "1,2" {
		t.Errorf("pages = %v, want [1 2]", pages)
	}
}

func TestXbtitPaginationDedupeAndLimit(t *testing.T) {
	row := func(id, title string) string {
		return `<tr>` +
			`<td class="name"><a href="index.php?page=torrent-details&amp;id=` + id + `">` + title + `</a></td>` +
			`<td class="category"><a href="index.php?page=torrents&amp;category=10">TV</a></td>` +
			`<td class="size">1 GB</td><td class="seeders">1</td><td class="leechers">1</td>` +
			`<td class="date">2024-01-01 00:00:00</td>` +
			`<td><a href="index.php?page=download&amp;id=` + id + `">DL</a></td></tr>`
	}
	page := func(rows ...string) string {
		return `<table id="sortabletable"><tbody>` + strings.Join(rows, "") + `</tbody></table>`
	}

	log := &trackerRequestLog{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.add(r)
		if r.URL.Query().Get("page") != "torrents" {
			http.NotFound(w, r)
			return
		}
		switch r.URL.Query().Get("pages") {
		case "1":
			_, _ = io.WriteString(w, page(row("401", "One"), row("402", "Two")))
		case "2":
			_, _ = io.WriteString(w, page(row("401", "One"), row("403", "Three")))
		default:
			_, _ = io.WriteString(w, page())
		}
	}))
	defer server.Close()

	connector, err := NewXbtit(XbtitConfig{BaseURL: server.URL, Transport: newTestTransport()})
	if err != nil {
		t.Fatalf("NewXbtit: %v", err)
	}
	items, err := connector.Search(context.Background(), engine.SearchRequest{Query: "x", Limit: 10})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("got %d items, want 3 unique", len(items))
	}
	if items[0].Title != "One" || items[2].Title != "Three" {
		t.Errorf("dedupe order = %q/%q", items[0].Title, items[2].Title)
	}

	items, err = connector.Search(context.Background(), engine.SearchRequest{Query: "x", Limit: 2})
	if err != nil {
		t.Fatalf("limited Search: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2", len(items))
	}
}

func TestXbtitDownloadDirectMagnetAndPasskey(t *testing.T) {
	const magnet = "magnet:?xt=urn:btih:feedface&dn=Example"
	log := &trackerRequestLog{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.add(r)
		switch {
		case r.URL.Path == "/files/abc.torrent":
			if r.URL.Query().Get("passkey") != "xb-passkey" {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			w.Header().Set("Content-Type", "application/x-bittorrent")
			_, _ = io.WriteString(w, "torrent-direct")
		case r.URL.Path == "/magnet-redirect":
			w.Header().Set("Location", magnet)
			w.WriteHeader(http.StatusFound)
		case r.URL.Path == "/index.php" && r.URL.Query().Get("page") == "download":
			if r.URL.Query().Get("id") != "203" || r.URL.Query().Get("passkey") != "xb-passkey" {
				http.Error(w, "bad params", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/x-bittorrent")
			_, _ = io.WriteString(w, "torrent-derived")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	connector, err := NewXbtit(XbtitConfig{
		BaseURL:   server.URL,
		Passkey:   "xb-passkey",
		Transport: newTestTransport(),
	})
	if err != nil {
		t.Fatalf("NewXbtit: %v", err)
	}

	payload, err := connector.Download(context.Background(), engine.Item{
		Title:       "Example.Release",
		DownloadURL: server.URL + "/files/abc.torrent",
	})
	if err != nil {
		t.Fatalf("Download direct: %v", err)
	}
	if string(payload.Body) != "torrent-direct" || payload.Filename != "Example.Release.torrent" {
		t.Errorf("payload = %q/%q", payload.Body, payload.Filename)
	}

	payload, err = connector.Download(context.Background(), engine.Item{
		Title:       "Example.Release",
		DownloadURL: server.URL + "/magnet-redirect",
	})
	if err != nil {
		t.Fatalf("Download magnet redirect: %v", err)
	}
	if payload.Magnet != magnet {
		t.Errorf("magnet = %q", payload.Magnet)
	}

	payload, err = connector.Download(context.Background(), engine.Item{Title: "Example", Magnet: magnet})
	if err != nil {
		t.Fatalf("Download magnet: %v", err)
	}
	if payload.Magnet != magnet {
		t.Errorf("magnet = %q", payload.Magnet)
	}

	payload, err = connector.Download(context.Background(), engine.Item{
		Title:      "Example.Release",
		Attributes: map[string]string{"id": "203"},
	})
	if err != nil {
		t.Fatalf("Download by id: %v", err)
	}
	if string(payload.Body) != "torrent-derived" {
		t.Errorf("derived body = %q", payload.Body)
	}
}

func TestXbtitDownloadErrorRedaction(t *testing.T) {
	const passkey = "xb-super-secret"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "failed "+r.URL.String(), http.StatusBadGateway)
	}))
	defer server.Close()

	connector, err := NewXbtit(XbtitConfig{BaseURL: server.URL, Passkey: passkey, Transport: newTestTransport()})
	if err != nil {
		t.Fatalf("NewXbtit: %v", err)
	}
	_, err = connector.Download(context.Background(), engine.Item{
		Title:      "Example",
		Attributes: map[string]string{"id": "7"},
	})
	if err == nil {
		t.Fatal("Download succeeded, want an error")
	}
	if strings.Contains(err.Error(), passkey) {
		t.Fatalf("error leaked the passkey: %v", err)
	}
	if !strings.Contains(err.Error(), "502") {
		t.Errorf("error = %v, want the HTTP status", err)
	}
}

func TestXbtitTestPhases(t *testing.T) {
	loginFixture := readFixture(t, "xbtit_login.html")
	searchFixture := readFixture(t, "xbtit_search.html")
	var searchCalls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/index.php" && r.URL.Query().Get("page") == "login" && r.Method == http.MethodGet:
			_, _ = w.Write(loginFixture)
		case r.URL.Path == "/index.php" && r.URL.Query().Get("page") == "login" && r.Method == http.MethodPost:
			http.SetCookie(w, &http.Cookie{Name: "uid", Value: "99", Path: "/"})
			_, _ = io.WriteString(w, "welcome")
		case r.URL.Path == "/index.php" && r.URL.Query().Get("page") == "torrents":
			searchCalls.Add(1)
			_, _ = w.Write(searchFixture)
		default:
			_, _ = w.Write(loginFixture)
		}
	}))
	defer server.Close()

	connector, err := NewXbtit(XbtitConfig{
		BaseURL:   server.URL,
		Username:  "bob",
		Password:  "s3cret",
		Transport: newTestTransport(),
	})
	if err != nil {
		t.Fatalf("NewXbtit: %v", err)
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
	if searchCalls.Load() == 0 {
		t.Error("search phase did not query the site")
	}
	for _, phase := range result.Phases {
		if strings.Contains(phase.Message, "s3cret") {
			t.Fatalf("phase %q leaked the password: %s", phase.Name, phase.Message)
		}
	}
}

func TestXbtitFactoryDefaultsAndValidation(t *testing.T) {
	connector, err := XbtitFactory(engine.Config{
		Indexer: engine.IndexerRef{Slug: "xb"},
		Settings: map[string]string{
			"base_url":   "https://tracker.example",
			"username":   "bob",
			"password":   "s3cret",
			"passkey":    "pk",
			"categories": "10,11",
			"user_agent": "xb-agent",
		},
		Transport: newTestTransport(),
	})
	if err != nil {
		t.Fatalf("XbtitFactory: %v", err)
	}
	tracker, ok := connector.(*Xbtit)
	if !ok {
		t.Fatalf("factory returned %T", connector)
	}
	caps := tracker.Capabilities()
	if caps.Protocol != engine.ProtocolTorrent {
		t.Errorf("protocol = %q", caps.Protocol)
	}
	if len(caps.Categories) != 2 || caps.Categories[0] != 10 || caps.Categories[1] != 11 {
		t.Errorf("categories = %v", caps.Categories)
	}
	for _, mode := range []engine.Mode{engine.ModeSearch, engine.ModeTV, engine.ModeMovie} {
		if !caps.Supports(mode) {
			t.Errorf("mode %q not declared", mode)
		}
	}
	if _, err := XbtitFactory(engine.Config{
		Settings: map[string]string{"base_url": "https://tracker.example", "categories": "x"},
	}); err == nil {
		t.Error("invalid categories did not fail")
	}
	if _, err := NewXbtit(XbtitConfig{Transport: newTestTransport()}); err == nil {
		t.Error("missing base URL did not fail")
	}
	if _, err := NewXbtit(XbtitConfig{BaseURL: "ftp://tracker.example", Transport: newTestTransport()}); err == nil {
		t.Error("non-HTTP base URL did not fail")
	}
	if _, err := NewXbtit(XbtitConfig{BaseURL: "https://tracker.example", Password: "x", Transport: newTestTransport()}); err == nil {
		t.Error("half-configured credentials did not fail")
	}
	if _, err := NewXbtit(XbtitConfig{
		BaseURL:   "https://tracker.example",
		Transport: newTestTransport(),
		Profile:   XbtitProfile{RowsSelector: "tr["},
	}); err == nil {
		t.Error("invalid selector did not fail")
	}
}

func TestDefaultXbtitProfile(t *testing.T) {
	profile := DefaultXbtitProfile()
	if profile.LoginPath != "index.php?page=login" {
		t.Errorf("login path = %q", profile.LoginPath)
	}
	if profile.PageParam != "page" {
		t.Errorf("page param = %q", profile.PageParam)
	}
	if !strings.Contains(profile.SearchPath, "index.php?page=torrents") {
		t.Errorf("search path = %q", profile.SearchPath)
	}
	if !routeBindsPage(profile.SearchPath) {
		t.Error("default search path should bind page to the torrents route")
	}
	if routeBindsPage("torrents.php?search=...&page=...") {
		t.Error("placeholder page must not count as a route")
	}
	for name, value := range map[string]string{
		"rows":     profile.RowsSelector,
		"title":    profile.TitleSelector,
		"download": profile.DownloadSelector,
		"size":     profile.SizeSelector,
		"date":     profile.DateSelector,
		"category": profile.CategorySelector,
	} {
		if strings.TrimSpace(value) == "" {
			t.Errorf("default %s selector is empty", name)
		}
	}
}

func TestXbtitDateParsing(t *testing.T) {
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
