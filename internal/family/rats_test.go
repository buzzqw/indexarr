package family

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/buzzqw/indexarr/internal/engine"
)

// ratsSearchJSON is the documented answer of search.torrents: the envelope the
// API.md of librats/rats-search describes, with rows in the Torrent Object
// Format. It is not a capture: the shape is the contract, and every field the
// connector reads is present here exactly once.
const ratsSearchJSON = `{
  "success": true,
  "requestId": "req-1",
  "data": [
    {
      "hash": "29EBE63FEB8BE91B6DCFF02BACC562D9A99EA864",
      "name": "Ubuntu 24.04.1 Desktop amd64",
      "size": 6291456000,
      "files": 3,
      "pieceLength": 262144,
      "added": 1735689600000,
      "contentType": "software",
      "seeders": 42,
      "leechers": 3,
      "completed": 910
    },
    {
      "hash": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
      "name": "Some Show S01E02 1080p WEB-DL",
      "size": 1500000000,
      "files": 1,
      "added": 1735689600000,
      "contentType": "video",
      "seeders": 0,
      "leechers": 0
    },
    {
      "hash": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
      "name": "A Book Collection (epub)",
      "size": 45000000,
      "files": 12,
      "added": "1735689600000",
      "contentType": "books",
      "seeders": "7"
    }
  ]
}`

// ratsConnector builds a connector pointed at server. The mutate hook lets a
// test turn on a setting the constructor reads.
func ratsConnector(t *testing.T, server *httptest.Server, mutate func(*RatsConfig)) *Rats {
	t.Helper()
	cfg := RatsConfig{
		Indexer:   engine.IndexerRef{ID: 1, Slug: "rats", Name: "Rats"},
		BaseURL:   server.URL,
		Transport: engine.NewTransport(engine.TransportConfig{AllowPrivate: true}),
	}
	if mutate != nil {
		mutate(&cfg)
	}
	connector, err := NewRats(cfg)
	if err != nil {
		t.Fatalf("NewRats: %v", err)
	}
	return connector
}
func TestRatsSearchTurnsTorrentsIntoReleases(t *testing.T) {
	var last url.URL
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		last = *r.URL
		if r.URL.Path != "/api/search.torrents" {
			t.Errorf("path = %q, want /api/search.torrents", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(ratsSearchJSON))
	}))
	t.Cleanup(server.Close)

	connector := ratsConnector(t, server, nil)
	items, err := connector.Search(context.Background(), engine.SearchRequest{
		Mode:  engine.ModeSearch,
		Query: "ubuntu",
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("items = %d, want 3", len(items))
	}

	// The infohash is lowercased and becomes the magnet and the GUID.
	first := items[0]
	if first.InfoHash != "29ebe63feb8be91b6dcff02bacc562d9a99ea864" {
		t.Errorf("infohash = %q", first.InfoHash)
	}
	if first.GUID != first.InfoHash {
		t.Errorf("guid = %q, want the infohash", first.GUID)
	}
	wantMagnet := "magnet:?xt=urn:btih:29ebe63feb8be91b6dcff02bacc562d9a99ea864&dn=Ubuntu+24.04.1+Desktop+amd64"
	if first.Magnet != wantMagnet {
		t.Errorf("magnet = %q, want %q", first.Magnet, wantMagnet)
	}
	if first.Protocol != engine.ProtocolTorrent {
		t.Errorf("protocol = %q", first.Protocol)
	}
	if first.Size != 6291456000 || first.Seeders != 42 || first.Leechers != 3 {
		t.Errorf("counters = size %d seeders %d leechers %d", first.Size, first.Seeders, first.Leechers)
	}
	if got := first.Published.UTC().Format(time.RFC3339); got != "2025-01-01T00:00:00Z" {
		t.Errorf("published = %q, want the documented millisecond timestamp", got)
	}
	if want := []int{4000}; !sameCategories(first.Categories, want) {
		t.Errorf("software categories = %v, want %v", first.Categories, want)
	}

	// A video row cannot be both: the node detects the file type, not the
	// format. A search that names no category stores it under Movies.
	if want := []int{2000}; !sameCategories(items[1].Categories, want) {
		t.Errorf("video categories = %v, want %v", items[1].Categories, want)
	}
	// A numeric string is read like a number: versions of the node differ.
	if items[2].Seeders != 7 || items[2].Size != 45000000 {
		t.Errorf("string counters = seeders %d size %d", items[2].Seeders, items[2].Size)
	}
	if want := []int{7000}; !sameCategories(items[2].Categories, want) {
		t.Errorf("books categories = %v, want %v", items[2].Categories, want)
	}

	values := last.Query()
	if values.Get("text") != "ubuntu" || values.Get("limit") != "10" {
		t.Errorf("query = %q", last.RawQuery)
	}
	if values.Get("type") != "" {
		t.Errorf("type = %q, want no type filter without categories", values.Get("type"))
	}
}

func TestRatsSearchSendsTheTypeAndSafeSearchOfTheRequest(t *testing.T) {
	var last url.URL
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		last = *r.URL
		_, _ = w.Write([]byte(ratsSearchJSON))
	}))
	t.Cleanup(server.Close)

	connector := ratsConnector(t, server, func(cfg *RatsConfig) { cfg.SafeSearch = true })
	_, err := connector.Search(context.Background(), engine.SearchRequest{
		Mode:       engine.ModeTV,
		Query:      "the show",
		Categories: []int{5000},
		Limit:      20,
		Offset:     40,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	values := last.Query()
	if values.Get("type") != "video" {
		t.Errorf("type = %q, want video", values.Get("type"))
	}
	if values.Get("safeSearch") != "true" {
		t.Errorf("safeSearch = %q, want true", values.Get("safeSearch"))
	}
	if values.Get("index") != "40" {
		t.Errorf("index = %q, want 40", values.Get("index"))
	}
}

func TestRatsSearchFoldsSeasonEpisodeAndYearIntoTheText(t *testing.T) {
	tests := []struct {
		name string
		req  engine.SearchRequest
		want string
	}{
		{
			name: "tv",
			req:  engine.SearchRequest{Mode: engine.ModeTV, Query: "The Show", Season: 2, Episode: 5},
			want: "The Show S02E05",
		},
		{
			name: "movie",
			req:  engine.SearchRequest{Mode: engine.ModeMovie, Query: "Dune", Year: 2021},
			want: "Dune 2021",
		},
		{
			name: "music without a query",
			req:  engine.SearchRequest{Mode: engine.ModeMusic, Artist: "Massive Attack", Album: "Mezzanine"},
			want: "Massive Attack Mezzanine",
		},
		{
			name: "book without a query",
			req:  engine.SearchRequest{Mode: engine.ModeBook, Author: "Ursula K. Le Guin", Title: "The Dispossessed"},
			want: "Ursula K. Le Guin The Dispossessed",
		},
		{
			name: "the marker is not repeated",
			req:  engine.SearchRequest{Mode: engine.ModeTV, Query: "The Show S02E05", Season: 2, Episode: 5},
			want: "The Show S02E05",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ratsText(tc.req); got != tc.want {
				t.Errorf("ratsText = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRatsTypeCoversTheWholeRequestOrNothing(t *testing.T) {
	tests := []struct {
		name       string
		categories []int
		want       string
	}{
		{"no categories", nil, ""},
		{"movies", []int{2000}, "video"},
		{"tv", []int{5000}, "video"},
		{"movies and tv", []int{2000, 5000}, "video"},
		{"a movie child category", []int{2040}, "video"},
		{"movies and audio", []int{2000, 3000}, ""},
		{"books", []int{7020}, "books"},
		{"console", []int{1000}, "games"},
		{"pc games", []int{4050}, "games"},
		{"pc and games", []int{4000, 4050}, "software, games"},
		{"other", []int{8000}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ratsTypeFor(tc.categories)
			// "software, games" is the mixed case: no single type covers it.
			if strings.Contains(tc.want, ",") {
				if got != "" {
					t.Errorf("ratsTypeFor(%v) = %q, want no single type", tc.categories, got)
				}
				return
			}
			if got != tc.want {
				t.Errorf("ratsTypeFor(%v) = %q, want %q", tc.categories, got, tc.want)
			}
		})
	}
}

// TestRatsVideoTakesTheSideTheSearchAsksFor pins the one ambiguous mapping: a
// stored release carries one category, so a TV search must store a video row
// under TV and a movie search under Movies, otherwise the other client would
// never find it in the cache.
func TestRatsVideoTakesTheSideTheSearchAsksFor(t *testing.T) {
	tests := []struct {
		name       string
		categories []int
		want       int
	}{
		{"tv", []int{5000}, 5000},
		{"a tv child", []int{5040}, 5000},
		{"movies", []int{2000}, 2000},
		{"both", []int{2000, 5000}, 2000},
		{"none", nil, 2000},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(ratsSearchJSON))
			}))
			t.Cleanup(server.Close)

			connector := ratsConnector(t, server, nil)
			items, err := connector.Search(context.Background(), engine.SearchRequest{
				Mode:       engine.ModeSearch,
				Query:      "the show",
				Categories: tc.categories,
			})
			if err != nil {
				t.Fatalf("Search: %v", err)
			}
			video, ok := itemByTitle(items, "Some Show S01E02 1080p WEB-DL")
			if !ok {
				t.Fatalf("the video row is missing from %d items", len(items))
			}
			if len(video.Categories) != 1 || video.Categories[0] != tc.want {
				t.Errorf("categories = %v, want [%d]", video.Categories, tc.want)
			}
		})
	}
}

func TestRatsSearchKeepsTheRequestedCategories(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(ratsSearchJSON))
	}))
	t.Cleanup(server.Close)

	connector := ratsConnector(t, server, nil)
	items, err := connector.Search(context.Background(), engine.SearchRequest{
		Mode:       engine.ModeSearch,
		Query:      "ubuntu",
		Categories: []int{7000},
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) != 1 || !strings.Contains(items[0].Title, "Book") {
		t.Fatalf("items = %+v, want the single books row", items)
	}

	// A child category of a requested parent matches through the tree.
	items, err = connector.Search(context.Background(), engine.SearchRequest{
		Mode:       engine.ModeSearch,
		Query:      "ubuntu",
		Categories: []int{4000},
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) != 1 || items[0].InfoHash != "29ebe63feb8be91b6dcff02bacc562d9a99ea864" {
		t.Fatalf("items = %+v, want the software row", items)
	}
}

func TestRatsSearchWithoutKeywordsReadsTheNewest(t *testing.T) {
	var last url.URL
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		last = *r.URL
		if r.URL.Path != "/api/search.recent" {
			t.Errorf("path = %q, want /api/search.recent", r.URL.Path)
		}
		_, _ = w.Write([]byte(ratsSearchJSON))
	}))
	t.Cleanup(server.Close)

	connector := ratsConnector(t, server, nil)
	items, err := connector.Search(context.Background(), engine.SearchRequest{Mode: engine.ModeSearch, Limit: 5})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) != 3 {
		t.Errorf("items = %d, want 3", len(items))
	}
	if last.Query().Get("limit") != "5" {
		t.Errorf("limit = %q, want 5", last.Query().Get("limit"))
	}
	if last.Query().Get("text") != "" {
		t.Errorf("text = %q, want none on the recent list", last.Query().Get("text"))
	}
}

func TestRatsLatestDropsRowsOlderThanTheMaxAge(t *testing.T) {
	now := time.Now()
	body := `{"success": true, "data": [
      {"hash": "cccccccccccccccccccccccccccccccccccccccc", "name": "Fresh", "size": 10, "added": ` +
		strconv.FormatInt(now.UnixMilli(), 10) + `, "contentType": "video"},
      {"hash": "dddddddddddddddddddddddddddddddddddddddd", "name": "Old", "size": 10, "added": ` +
		strconv.FormatInt(now.Add(-72*time.Hour).UnixMilli(), 10) + `, "contentType": "video"}
    ]}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)

	connector := ratsConnector(t, server, nil)
	items, err := connector.Latest(context.Background(), engine.LatestRequest{
		Categories: []int{5000},
		Limit:      100,
		MaxAge:     24 * time.Hour,
	})
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if len(items) != 1 || items[0].Title != "Fresh" {
		t.Fatalf("items = %+v, want only the fresh row", items)
	}
}

func TestRatsSearchReportsTheNodeFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success": false, "error": "Manticore is not running", "requestId": "x"}`))
	}))
	t.Cleanup(server.Close)

	connector := ratsConnector(t, server, nil)
	_, err := connector.Search(context.Background(), engine.SearchRequest{Mode: engine.ModeSearch, Query: "ubuntu"})
	if err == nil {
		t.Fatal("Search succeeded on a failed envelope")
	}
	if !strings.Contains(err.Error(), "Manticore is not running") {
		t.Errorf("error = %v, want the node's message", err)
	}
}

func TestRatsSearchRejectsBrokenResponses(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{"empty", "", "empty response body"},
		{"not json", "<html>nope</html>", "invalid JSON response"},
		{"data is an object", `{"success": true, "data": {"torrents": 1}}`, "not a list of torrents"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(tc.body))
			}))
			t.Cleanup(server.Close)

			connector := ratsConnector(t, server, nil)
			_, err := connector.Search(context.Background(), engine.SearchRequest{Mode: engine.ModeSearch, Query: "ubuntu"})
			if err == nil {
				t.Fatal("Search succeeded on a broken answer")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestRatsSkipsRowsWithoutAnInfohash(t *testing.T) {
	body := `{"success": true, "data": [
      {"hash": "", "name": "No hash", "size": 10},
      {"hash": "not-a-hash", "name": "Broken hash", "size": 10},
      {"hash": "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", "name": "", "size": 10},
      {"hash": "ffffffffffffffffffffffffffffffffffffffff", "name": "Kept", "size": 10}
    ]}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)

	connector := ratsConnector(t, server, nil)
	items, err := connector.Search(context.Background(), engine.SearchRequest{Mode: engine.ModeSearch, Query: "x"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) != 1 || items[0].Title != "Kept" {
		t.Fatalf("items = %+v, want only the row with a hash and a name", items)
	}
}

func TestRatsDownloadAnswersWithTheMagnet(t *testing.T) {
	connector := ratsConnector(t, httptest.NewServer(http.NotFoundHandler()), nil)

	payload, err := connector.Download(context.Background(), engine.Item{
		Title:  "Some Release",
		Magnet: "magnet:?xt=urn:btih:1111111111111111111111111111111111111111",
	})
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if payload.Magnet != "magnet:?xt=urn:btih:1111111111111111111111111111111111111111" {
		t.Errorf("magnet = %q", payload.Magnet)
	}

	// A row that lost its magnet is rebuilt from the infohash.
	payload, err = connector.Download(context.Background(), engine.Item{
		Title:    "Some Release",
		InfoHash: "2222222222222222222222222222222222222222",
	})
	if err != nil {
		t.Fatalf("Download from infohash: %v", err)
	}
	if !strings.Contains(payload.Magnet, "urn:btih:2222222222222222222222222222222222222222") {
		t.Errorf("magnet = %q", payload.Magnet)
	}

	if _, err := connector.Download(context.Background(), engine.Item{Title: "Nothing"}); err == nil {
		t.Error("Download without a magnet or an infohash succeeded")
	}
}

func TestRatsTestReportsTheNodeAndItsIndex(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/stats.get":
			_, _ = w.Write([]byte(`{"success": true, "data": {"torrents": 184320, "files": 921600, "size": 4398046511104, "peers": 8}}`))
		case "/api/search.torrents":
			_, _ = w.Write([]byte(ratsSearchJSON))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	connector := ratsConnector(t, server, nil)
	result, err := connector.Test(context.Background())
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	if !result.OK || len(result.Phases) != 2 {
		t.Fatalf("result = %+v, want two passing phases", result)
	}
	if result.Phases[0].Name != "reach" || !strings.Contains(result.Phases[0].Message, "184320") {
		t.Errorf("reach phase = %+v, want the node's index size", result.Phases[0])
	}
}

func TestRatsAcceptsTheNodeAddressWithOrWithoutAPI(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/search.torrents" {
			t.Errorf("path = %q, want /api/search.torrents", r.URL.Path)
		}
		_, _ = w.Write([]byte(ratsSearchJSON))
	}))
	t.Cleanup(server.Close)

	for _, base := range []string{server.URL, server.URL + "/", server.URL + "/api", server.URL + "/api/"} {
		connector, err := NewRats(RatsConfig{
			Indexer:   engine.IndexerRef{Slug: "rats"},
			BaseURL:   base,
			Transport: engine.NewTransport(engine.TransportConfig{AllowPrivate: true}),
		})
		if err != nil {
			t.Fatalf("NewRats(%q): %v", base, err)
		}
		if _, err := connector.Search(context.Background(), engine.SearchRequest{Mode: engine.ModeSearch, Query: "x"}); err != nil {
			t.Errorf("Search with base %q: %v", base, err)
		}
	}

	if _, err := NewRats(RatsConfig{BaseURL: ""}); err == nil {
		t.Error("NewRats accepted an empty address")
	}
	if _, err := NewRats(RatsConfig{BaseURL: "127.0.0.1:8095"}); err == nil {
		t.Error("NewRats accepted an address without a scheme")
	}
}

func TestRatsAddedReadsMillisecondsAndSeconds(t *testing.T) {
	if got := ratsAdded([]byte("1735689600000")); got.UTC().Format(time.RFC3339) != "2025-01-01T00:00:00Z" {
		t.Errorf("milliseconds = %v", got.UTC())
	}
	if got := ratsAdded([]byte("1735689600")); got.UTC().Format(time.RFC3339) != "2025-01-01T00:00:00Z" {
		t.Errorf("seconds = %v", got.UTC())
	}
	if got := ratsAdded(nil); !got.IsZero() {
		t.Errorf("missing = %v, want the zero time", got)
	}
}

func itemByTitle(items []engine.Item, title string) (engine.Item, bool) {
	for _, item := range items {
		if item.Title == title {
			return item, true
		}
	}
	return engine.Item{}, false
}

func sameCategories(got, want []int) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
