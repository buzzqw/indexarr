package feed

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
)

// mircrewResponse is the exact XML produced by mircrew-indexer's
// torznab_search: the magnet is in <link> and in the enclosure (not in a
// magneturl attribute), there is no seeders attribute, and the date is the
// forum topic date.
const mircrewResponse = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:torznab="http://torznab.com/schemas/2015/feed"><channel><title>mircrew-indexer</title><response offset="0" total="1"/><item><title>FBI.Most.Wanted-1x02-Conseguenze.DLMux-1080p-x264-AC3.ITA-ENG.mkv</title><pubDate>Mon, 05 Oct 2026 10:17:44 +0000</pubDate><guid isPermaLink="false">0123456789abcdef0123456789abcdef01234567</guid><link>magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567&amp;dn=FBI.Most.Wanted-1x02</link><category>5000</category><enclosure url="magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567&amp;dn=FBI.Most.Wanted-1x02" length="0" type="application/x-bittorrent"/><torznab:attr name="category" value="5000"/><torznab:attr name="infohash" value="0123456789abcdef0123456789abcdef01234567"/><torznab:attr name="season" value="1"/><torznab:attr name="episode" value="2"/></item></channel></rss>`

func TestMirCrewDirectTorznab(t *testing.T) {
	var requestedPath, requestedQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestedPath = r.URL.Path
		requestedQuery = r.URL.Query().Get("q")
		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		_, _ = io.WriteString(w, mircrewResponse)
	}))
	defer server.Close()

	// A MirCrew source is configured with the base URL: /api is appended by
	// indexarr, exactly like Gextto asks for it.
	client := NewClient(ClientConfig{Name: "mircrew indexerr", URL: server.URL, APIKey: ""})
	items, err := client.Fetch(context.Background(), "FBI Most Wanted S01E02", 100)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if requestedPath != "/api" {
		t.Errorf("path = %q, want /api", requestedPath)
	}
	if requestedQuery != "FBI Most Wanted S01E02" {
		t.Errorf("q = %q", requestedQuery)
	}
	if len(items) != 1 {
		t.Fatalf("items = %d", len(items))
	}
	item := items[0]
	if item.InfoHash != "0123456789abcdef0123456789abcdef01234567" {
		t.Errorf("infohash = %q", item.InfoHash)
	}
	if !strings.HasPrefix(item.Magnet, "magnet:?xt=urn:btih:") {
		t.Errorf("magnet = %q", item.Magnet)
	}
	// MirCrew reports no seeders: unknown, not zero.
	if item.Seeders != -1 {
		t.Errorf("seeders = %d, want -1", item.Seeders)
	}
	if item.Category != "5000" {
		t.Errorf("category = %q", item.Category)
	}
	if item.Published.IsZero() || item.Published.Year() != 2026 {
		t.Errorf("published = %v", item.Published)
	}
	if item.TorrentURL != "" {
		t.Errorf("a magnet must not become a .torrent download: %q", item.TorrentURL)
	}
}

func TestMirCrewStatusReportsForumLogin(t *testing.T) {
	loggedIn := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api":
			_, _ = io.WriteString(w, `<caps><server title="mircrew-indexer"/></caps>`)
		case "/status":
			if loggedIn {
				_, _ = io.WriteString(w, `{"ok":true,"logged_in":true,"login_error":"","last_error":"old"}`)
			} else {
				_, _ = io.WriteString(w, `{"ok":true,"logged_in":false,"login_error":"wrong password"}`)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := NewClient(ClientConfig{URL: server.URL + "/api", Manager: ManagerTorznab})
	if _, err := client.Test(context.Background()); err == nil || !strings.Contains(err.Error(), "wrong password") {
		t.Fatalf("broken forum login not reported: %v", err)
	}

	loggedIn = true
	message, err := client.Test(context.Background())
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	if !strings.Contains(message, "forum login ok") {
		t.Errorf("message = %q", message)
	}
}

func TestServiceStatusDistinguishesStartingFromLost(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   string
		wantErr  bool
		wantText string
	}{
		{"starting", `{"ok":true,"logged_in":false,"login_error":"","last_run":""}`, false, "in progress"},
		{"lost", `{"ok":true,"logged_in":false,"login_error":"","last_run":"2026-10-08T11:17:51Z"}`, true, "login lost"},
		{"unhealthy", `{"ok":false,"last_error":"disk full"}`, true, "disk full"},
		{"not mircrew", `{"version":"1.0"}`, false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/status" {
					_, _ = io.WriteString(w, tc.status)
					return
				}
				_, _ = io.WriteString(w, `<caps><server title="svc"/></caps>`)
			}))
			defer server.Close()
			client := NewClient(ClientConfig{URL: server.URL + "/api", Manager: ManagerTorznab})
			message, err := client.Test(context.Background())
			if tc.wantErr {
				if err == nil || !strings.Contains(err.Error(), tc.wantText) {
					t.Fatalf("err = %v, want %q", err, tc.wantText)
				}
				return
			}
			if err != nil {
				t.Fatalf("Test: %v", err)
			}
			if tc.wantText != "" && !strings.Contains(message, tc.wantText) {
				t.Errorf("message = %q, want %q", message, tc.wantText)
			}
		})
	}
}

func TestBasicAuthAndAPIKeyHeader(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, password, ok := r.BasicAuth()
		if !ok || user != "user" || password != "pass" {
			t.Errorf("basic auth = %q/%q/%v", user, password, ok)
		}
		if r.Header.Get("X-Api-Key") != "secret" {
			t.Errorf("X-Api-Key = %q", r.Header.Get("X-Api-Key"))
		}
		_, _ = io.WriteString(w, mircrewResponse)
	}))
	defer server.Close()

	client := NewClient(ClientConfig{
		URL: server.URL, Manager: ManagerTorznab, APIKey: "secret",
		Username: "user", Password: "pass",
	})
	if _, err := client.Fetch(context.Background(), "x", 10); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
}

func TestWithQueryMergesExistingParameters(t *testing.T) {
	got := withQuery("http://host/api?apikey=old&x=1", url.Values{
		"t":      {"search"},
		"apikey": {"new"},
	})
	parsed, err := url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	if query.Get("t") != "search" || query.Get("apikey") != "new" || query.Get("x") != "1" {
		t.Errorf("query = %v", query)
	}
}

func TestSanitizeXML(t *testing.T) {
	feed := `<rss version="2.0"><channel><item>` +
		`<title>Café & Bar &nbsp; 2024 &unknown;</title>` +
		`<link>magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567&dn=Name</link>` +
		`</item></channel></rss>`
	items, err := ParseRSS([]byte(feed))
	if err != nil {
		t.Fatalf("ParseRSS: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %d", len(items))
	}
	if !strings.Contains(items[0].Title, "Café & Bar") {
		t.Errorf("title = %q", items[0].Title)
	}
	if !strings.Contains(items[0].Title, "&unknown;") {
		t.Errorf("unknown entity should stay literal, got %q", items[0].Title)
	}
	if items[0].InfoHash != "0123456789abcdef0123456789abcdef01234567" {
		t.Errorf("infohash = %q", items[0].InfoHash)
	}
}

// MirCrew coverage lives in mircrew_test.go.

func TestMirCrewIndexerOfflineFeed(t *testing.T) {
	body, err := os.ReadFile("testdata/mircrew_indexer_feed.xml")
	if err != nil {
		t.Fatal(err)
	}
	var gotQuery url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = w.Write(body)
	}))
	defer server.Close()

	client := NewClient(ClientConfig{URL: server.URL + "/api?k=KEEPME", Manager: ManagerTorznab})
	items, err := client.Fetch(context.Background(), "FBI Most Wanted S01E02", 100)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %d", len(items))
	}
	if gotQuery.Get("k") != "KEEPME" {
		t.Errorf("k token = %q, want KEEPME", gotQuery.Get("k"))
	}
	if gotQuery.Get("season") != "1" || gotQuery.Get("ep") != "2" {
		t.Errorf("season/ep = %q/%q", gotQuery.Get("season"), gotQuery.Get("ep"))
	}
	item := items[0]
	if !item.Published.IsZero() {
		t.Errorf("missing pubDate became %v, want zero for first-seen handling", item.Published)
	}
	if item.Seeders != -1 {
		t.Errorf("missing seeders = %d, want -1 (unknown)", item.Seeders)
	}
	if !strings.Contains(item.Title, "ITA") || !strings.Contains(item.Title, "ENG") {
		t.Errorf("languages lost from title: %q", item.Title)
	}
	if !strings.Contains(item.Magnet, "tr=udp") {
		t.Errorf("magnet lost its tracker: %q", item.Magnet)
	}
}
