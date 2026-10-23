package feed

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestEndpointResolution(t *testing.T) {
	cases := []struct {
		base    string
		manager string
		want    string
	}{
		{"http://host:5001", ManagerProwlarr, "http://host:5001/api/v1/search"},
		{"http://host:5001", ManagerJackett, "http://host:5001/api/v2.0/indexers/all/results/torznab/api"},
		{"http://host:5001", ManagerTorznab, "http://host:5001/api"},
		{"http://host:5001", ManagerMirCrew, "http://host:5001/api"},
		{"http://host:5001", ManagerAuto, "http://host:5001/api"},
		{"http://prowlarr:9696", ManagerAuto, "http://prowlarr:9696/api/v1/search"},
		{"http://jackett:9117", ManagerAuto, "http://jackett:9117/api/v2.0/indexers/all/results/torznab/api"},
		{"http://host:9696/1/api", ManagerAuto, "http://host:9696/1/api"},
		{"http://host:5001/torznab/api", ManagerAuto, "http://host:5001/torznab/api"},
		{"http://torznab.local:5001", ManagerAuto, "http://torznab.local:5001/api"},
		{"http://host:5001/", ManagerTorznab, "http://host:5001/api"},
	}
	for _, tc := range cases {
		if got := Endpoint(tc.base, tc.manager); got != tc.want {
			t.Errorf("Endpoint(%q, %q) = %q, want %q", tc.base, tc.manager, got, tc.want)
		}
	}
	if got := Endpoint("", ManagerAuto); got != "" {
		t.Errorf("Endpoint(empty) = %q", got)
	}
}

func TestFetchTorznabXML(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api" {
			t.Errorf("path = %q", r.URL.Path)
		}
		query := r.URL.Query()
		if query.Get("t") != "search" {
			t.Errorf("t = %q", query.Get("t"))
		}
		if query.Get("q") != "Show S01E02" {
			t.Errorf("q = %q", query.Get("q"))
		}
		if query.Get("apikey") != "secret" {
			t.Errorf("apikey = %q", query.Get("apikey"))
		}
		if query.Get("cat") != "5000,2000" {
			t.Errorf("cat = %q", query.Get("cat"))
		}
		if query.Get("season") != "1" || query.Get("ep") != "2" {
			t.Errorf("season/ep = %q/%q", query.Get("season"), query.Get("ep"))
		}
		if query.Get("limit") != "25" {
			t.Errorf("limit = %q", query.Get("limit"))
		}
		if r.Header.Get("User-Agent") != "test-agent" {
			t.Errorf("User-Agent = %q", r.Header.Get("User-Agent"))
		}
		w.Header().Set("Content-Type", "application/xml")
		_, _ = io.WriteString(w, torznabResponse)
	}))
	defer server.Close()

	client := NewClient(ClientConfig{
		Name: "test", URL: server.URL, Manager: ManagerTorznab, APIKey: "secret",
		Categories: []int{5000, 2000}, UserAgent: "test-agent", Timeout: 5 * time.Second,
	})
	items, err := client.Fetch(context.Background(), "Show S01E02", 25)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("items = %d, want 3", len(items))
	}
	// The enclosure is the most precise download URL: the parser keeps it.
	if items[1].TorrentURL != "http://indexer.test/download/2.torrent" {
		t.Errorf("TorrentURL = %q", items[1].TorrentURL)
	}
}

func TestFetchTorznabErrorElement(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `<rss><error code="203" description="tmdbid is not supported"/></rss>`)
	}))
	defer server.Close()
	client := NewClient(ClientConfig{URL: server.URL, Manager: ManagerTorznab, APIKey: "k"})
	_, err := client.Fetch(context.Background(), "x", 10)
	if err == nil || !strings.Contains(err.Error(), "203") {
		t.Fatalf("error = %v, want the Torznab 203 error", err)
	}
}

func TestTorznabError(t *testing.T) {
	body := []byte(`<rss><channel><error code="100" description="bad request"/></channel></rss>`)
	if got := TorznabError(body); got != "Torznab 100: bad request" {
		t.Errorf("TorznabError = %q", got)
	}
	if got := TorznabError([]byte(`<rss></rss>`)); got != "" {
		t.Errorf("TorznabError(clean) = %q", got)
	}
}

func TestClientTestCaps(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/status" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("t") != "caps" {
			t.Errorf("t = %q", r.URL.Query().Get("t"))
		}
		if r.URL.Query().Get("apikey") != "secret" {
			t.Errorf("apikey = %q", r.URL.Query().Get("apikey"))
		}
		_, _ = io.WriteString(w, `<caps><server version="1.0" title="MyIndexer"/><searching/></caps>`)
	}))
	defer server.Close()
	client := NewClient(ClientConfig{URL: server.URL, Manager: ManagerTorznab, APIKey: "secret"})
	message, err := client.Test(context.Background())
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	if message != "Torznab: MyIndexer 1.0" {
		t.Errorf("message = %q", message)
	}
}

func TestProwlarrJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/search" {
			t.Errorf("path = %q", r.URL.Path)
		}
		query := r.URL.Query()
		if query.Get("query") != "Show" {
			t.Errorf("query = %q", query.Get("query"))
		}
		if query.Get("type") != "search" {
			t.Errorf("type = %q", query.Get("type"))
		}
		if query.Get("limit") != "40" {
			t.Errorf("limit = %q", query.Get("limit"))
		}
		if query.Get("apikey") != "secret" {
			t.Errorf("apikey = %q", query.Get("apikey"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `[
			{"title":"Show.S01E01.1080p","guid":"g1","protocol":"torrent","size":123,
			 "seeders":5,"leechers":2,"infoHash":"0123456789abcdef0123456789abcdef01234567",
			 "magnetUrl":"magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567",
			 "downloadUrl":"http://prowlarr.test/1/download?apikey=secret",
			 "publishDate":"2026-01-02T03:04:05Z","categories":[{"id":5040,"name":"TV/HD"}],
			 "imdbId":"tt0133093"},
			{"title":"Usenet.Only","protocol":"usenet","size":5}
		]`)
	}))
	defer server.Close()

	client := NewClient(ClientConfig{URL: server.URL, Manager: ManagerProwlarr, APIKey: "secret"})
	items, err := client.Fetch(context.Background(), "Show", 40)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1 (usenet discarded)", len(items))
	}
	item := items[0]
	if item.Title != "Show.S01E01.1080p" || item.Seeders != 5 || item.Leechers != 2 {
		t.Errorf("item = %#v", item)
	}
	if item.Category != "5040" || item.IMDbID != "0133093" {
		t.Errorf("category/imdb = %q/%q", item.Category, item.IMDbID)
	}
	if item.Magnet == "" || item.InfoHash != "0123456789abcdef0123456789abcdef01234567" {
		t.Errorf("magnet/infohash = %q/%q", item.Magnet, item.InfoHash)
	}
	if item.TorrentURL != "http://prowlarr.test/1/download?apikey=secret" {
		t.Errorf("TorrentURL = %q", item.TorrentURL)
	}
}

func TestProwlarrLatestUsesPerIndexerTorznab(t *testing.T) {
	var asked []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.Path)
		switch r.URL.Path {
		case "/api/v1/indexer":
			_, _ = io.WriteString(w, `[
				{"id":1,"name":"A","enable":true,"protocol":"torrent"},
				{"id":2,"name":"B","enable":false,"protocol":"torrent"}
			]`)
		case "/1/api":
			if r.URL.Query().Get("q") != "" {
				t.Errorf("q = %q", r.URL.Query().Get("q"))
			}
			_, _ = io.WriteString(w, `<rss xmlns:torznab="http://torznab.com/schemas/2015/feed"><channel>
				<item><title>Latest.Release</title><link>magnet:?xt=urn:btih:fedcba9876543210fedcba9876543210fedcba98</link>
				<torznab:attr name="seeders" value="4"/></item></channel></rss>`)
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	}))
	defer server.Close()

	client := NewClient(ClientConfig{URL: server.URL, Manager: ManagerProwlarr, APIKey: "secret"})
	items, err := client.Fetch(context.Background(), "", 25)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(items) != 1 || items[0].Title != "Latest.Release" {
		t.Fatalf("items = %#v", items)
	}
	for _, path := range asked {
		if path == "/2/api" {
			t.Error("a disabled indexer was queried")
		}
	}
}

func TestProwlarrTestStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/system/status" {
			t.Errorf("path = %q", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"version":"1.2.3"}`)
	}))
	defer server.Close()
	client := NewClient(ClientConfig{URL: server.URL, Manager: ManagerProwlarr, APIKey: "secret"})
	message, err := client.Test(context.Background())
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	if message != "Prowlarr 1.2.3" {
		t.Errorf("message = %q", message)
	}
}

func TestDedupKeyIsStable(t *testing.T) {
	hash := "0123456789abcdef0123456789abcdef01234567"
	item := Item{Title: "A", InfoHash: hash, GUID: "g1"}
	if got := DedupKey(item, "src"); got != hash {
		t.Errorf("DedupKey with hash = %q", got)
	}
	byGUID := Item{Title: "A", GUID: "g1"}
	first := DedupKey(byGUID, "src")
	second := DedupKey(byGUID, "src")
	if first != second || !strings.HasPrefix(first, "guid:") {
		t.Errorf("guid key = %q / %q", first, second)
	}
	if other := DedupKey(byGUID, "other"); other == first {
		t.Error("the same GUID on two sources must not collide")
	}
	byTitle := Item{Title: "A", Size: 10}
	if !strings.HasPrefix(DedupKey(byTitle, "src"), "title:") {
		t.Error("title fallback key missing")
	}
}

func TestJoinInts(t *testing.T) {
	if got := joinInts([]int{5000, 0, 2000, -1}); got != "5000,2000" {
		t.Errorf("joinInts = %q", got)
	}
}
