package family

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/buzzqw/indexarr/internal/engine"
)

func TestGazelleSearchParsingAndParams(t *testing.T) {
	fixture := readFixture(t, "gazelle_search.json")
	recorder := &queryRecorder{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ajax.php" {
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("Authorization"); got != "token secret-gazelle-token" {
			http.Error(w, "bad authorization: "+got, http.StatusUnauthorized)
			return
		}
		if r.Header.Get("User-Agent") != "gazelle-test-agent" {
			http.Error(w, "bad user agent", http.StatusBadRequest)
			return
		}
		recorder.add(r.URL.Query())
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(fixture)
	}))
	defer server.Close()

	connector, err := NewGazelle(GazelleConfig{
		Indexer:    engine.IndexerRef{Slug: "gazelle-tracker"},
		BaseURL:    server.URL,
		APIKey:     "secret-gazelle-token",
		Transport:  newTestTransport(),
		Categories: []int{3000},
		UserAgent:  "gazelle-test-agent",
	})
	if err != nil {
		t.Fatalf("NewGazelle: %v", err)
	}

	items, err := connector.Search(context.Background(), engine.SearchRequest{
		Mode:   engine.ModeMusic,
		Artist: "Artist",
		Album:  "Album",
		Limit:  25,
		Offset: 50,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) != 4 {
		t.Fatalf("got %d items, want 4 (malformed torrents are skipped)", len(items))
	}

	query := recorder.last()
	checks := map[string]string{
		"action":    "browse",
		"searchstr": "Artist Album",
		"order_by":  "time",
		"order_way": "desc",
		"page":      "3",
		"perpage":   "25",
	}
	for key, want := range checks {
		if got := query.Get(key); got != want {
			t.Errorf("query %s = %q, want %q", key, got, want)
		}
	}

	first := items[0]
	if first.Title != "Artist - Album 01 - Track.flac" {
		t.Errorf("title = %q", first.Title)
	}
	if first.GUID != "gazelle:1001" {
		t.Errorf("guid = %q", first.GUID)
	}
	if first.InfoHash != "0123456789abcdef0123456789abcdef01234567" {
		t.Errorf("infohash = %q", first.InfoHash)
	}
	if first.Size != 524288000 || first.Seeders != 12 || first.Leechers != 3 {
		t.Errorf("size/seeds/leech = %d/%d/%d", first.Size, first.Seeders, first.Leechers)
	}
	if len(first.Categories) != 1 || first.Categories[0] != 3000 {
		t.Errorf("categories = %v, want [3000]", first.Categories)
	}
	if first.Published.Unix() != 1700000100 {
		t.Errorf("published = %v (%d)", first.Published, first.Published.Unix())
	}
	wantDownload := server.URL + "/torrents.php?action=download&id=1001"
	if first.DownloadURL != wantDownload {
		t.Errorf("download = %q, want %q", first.DownloadURL, wantDownload)
	}
	if first.Attributes["format"] != "FLAC" || first.Attributes["group_id"] != "101" {
		t.Errorf("attributes = %v", first.Attributes)
	}

	second := items[1]
	if second.Title != "Artist - Album MP3" {
		t.Errorf("second title = %q", second.Title)
	}
	if second.GUID != "gazelle:1002" || second.InfoHash != "abcdefabcdefabcdefabcdefabcdefabcdefabcd" {
		t.Errorf("second guid/hash = %q/%q", second.GUID, second.InfoHash)
	}
	if second.Size != 1048576 || second.Seeders != 4 || second.Leechers != 1 {
		t.Errorf("second size/seeds/leech = %d/%d/%d", second.Size, second.Seeders, second.Leechers)
	}
	if want := server.URL + "/torrents.php?action=download&id=1002"; second.DownloadURL != want {
		t.Errorf("second download = %q, want %q", second.DownloadURL, want)
	}

	movie := items[2]
	if movie.Title != "Some.Movie.2023.1080p" {
		t.Errorf("movie title = %q", movie.Title)
	}
	if len(movie.Categories) != 1 || movie.Categories[0] != 4000 {
		t.Errorf("movie categories = %v, want [4000]", movie.Categories)
	}
	if movie.Published.Year() != 2024 || movie.Published.Month() != 2 {
		t.Errorf("movie published = %v", movie.Published)
	}

	object := items[3]
	if object.Title != "Object.Group MKV" {
		t.Errorf("object title = %q", object.Title)
	}
	if len(object.Categories) != 1 || object.Categories[0] != 5070 {
		t.Errorf("object categories = %v, want [5070]", object.Categories)
	}
	if object.Size != 1<<30 {
		t.Errorf("object size = %d", object.Size)
	}

	for _, item := range items {
		if strings.Contains(item.GUID, "secret-gazelle-token") {
			t.Fatal("GUID leaked the API token")
		}
		for key, value := range item.Attributes {
			if strings.Contains(value, "secret-gazelle-token") {
				t.Fatalf("attribute %q leaked the API token", key)
			}
		}
	}
}

func TestGazelleCookieAuthAndPairDownload(t *testing.T) {
	var mu sync.Mutex
	var cookies []string
	var downloadQuery map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ajax.php":
			mu.Lock()
			cookies = append(cookies, r.Header.Get("Cookie"))
			mu.Unlock()
			if r.Header.Get("Authorization") != "" {
				http.Error(w, "cookie auth must not use a bearer token", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"status":"success","response":{"results":[]}}`)
		case "/torrents.php":
			if r.URL.Query().Get("action") != "download" {
				http.NotFound(w, r)
				return
			}
			mu.Lock()
			downloadQuery = map[string]string{
				"id":           r.URL.Query().Get("id"),
				"authkey":      r.URL.Query().Get("authkey"),
				"torrent_pass": r.URL.Query().Get("torrent_pass"),
			}
			mu.Unlock()
			w.Header().Set("Content-Type", "application/x-bittorrent")
			_, _ = io.WriteString(w, "d8:announce5:https")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	cookieConnector, err := NewGazelle(GazelleConfig{
		BaseURL:   server.URL,
		APIKey:    "session=abc123",
		Transport: newTestTransport(),
	})
	if err != nil {
		t.Fatalf("NewGazelle(cookie): %v", err)
	}
	if _, err := cookieConnector.Search(context.Background(), engine.SearchRequest{Query: "test"}); err != nil {
		t.Fatalf("cookie Search: %v", err)
	}
	mu.Lock()
	if len(cookies) != 1 || cookies[0] != "session=abc123" {
		t.Errorf("cookie header = %v", cookies)
	}
	mu.Unlock()

	pairConnector, err := NewGazelle(GazelleConfig{
		BaseURL:   server.URL,
		APIKey:    "auth-key-123:pass-key-456",
		Transport: newTestTransport(),
	})
	if err != nil {
		t.Fatalf("NewGazelle(pair): %v", err)
	}
	payload, err := pairConnector.Download(context.Background(), engine.Item{
		Title:      "Example.Release",
		Attributes: map[string]string{"torrent_id": "1001"},
	})
	if err != nil {
		t.Fatalf("pair Download: %v", err)
	}
	if string(payload.Body) != "d8:announce5:https" {
		t.Errorf("body = %q", payload.Body)
	}
	if payload.ContentType != "application/x-bittorrent" || payload.Filename != "Example.Release.torrent" {
		t.Errorf("content type/filename = %q/%q", payload.ContentType, payload.Filename)
	}
	mu.Lock()
	want := map[string]string{"id": "1001", "authkey": "auth-key-123", "torrent_pass": "pass-key-456"}
	for key, value := range want {
		if downloadQuery[key] != value {
			t.Errorf("download query %s = %q, want %q", key, downloadQuery[key], value)
		}
	}
	mu.Unlock()
}

func TestGazelleDownloadMagnetRedirect(t *testing.T) {
	const magnet = "magnet:?xt=urn:btih:0123456789abcdef&dn=Example"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/torrents.php" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Location", magnet)
		w.WriteHeader(http.StatusFound)
	}))
	defer server.Close()

	connector, err := NewGazelle(GazelleConfig{
		BaseURL:   server.URL,
		APIKey:    "secret-token",
		Transport: newTestTransport(),
	})
	if err != nil {
		t.Fatalf("NewGazelle: %v", err)
	}
	payload, err := connector.Download(context.Background(), engine.Item{
		Title:      "Example.Release",
		Attributes: map[string]string{"torrent_id": "9"},
	})
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if payload.Magnet != magnet {
		t.Errorf("magnet = %q, want %q", payload.Magnet, magnet)
	}
	if len(payload.Body) != 0 {
		t.Errorf("magnet redirect returned a body: %q", payload.Body)
	}
}

func TestGazelleLatestModesAndCapabilities(t *testing.T) {
	recorder := &queryRecorder{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder.add(r.URL.Query())
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"status":"success","response":{"results":[]}}`)
	}))
	defer server.Close()

	connector, err := NewGazelle(GazelleConfig{
		BaseURL:    server.URL,
		APIKey:     "secret-token",
		Transport:  newTestTransport(),
		Categories: []int{3000, 5000},
	})
	if err != nil {
		t.Fatalf("NewGazelle: %v", err)
	}

	if _, err := connector.Latest(context.Background(), engine.LatestRequest{Limit: 10}); err != nil {
		t.Fatalf("Latest: %v", err)
	}
	latest := recorder.last()
	if latest.Get("searchstr") != "" {
		t.Errorf("latest sent a search term: %v", latest)
	}
	if latest.Get("page") != "1" || latest.Get("perpage") != "10" {
		t.Errorf("latest pagination = page %q perpage %q", latest.Get("page"), latest.Get("perpage"))
	}

	if _, err := connector.Search(context.Background(), engine.SearchRequest{
		Mode: engine.ModeTV, Query: "The Show", Season: 2, Episode: 3,
	}); err != nil {
		t.Fatalf("TV Search: %v", err)
	}
	if got := recorder.last().Get("searchstr"); got != "The Show S02E03" {
		t.Errorf("TV searchstr = %q", got)
	}

	if _, err := connector.Search(context.Background(), engine.SearchRequest{
		Mode: engine.ModeBook, Query: "Dune",
	}); err != nil {
		t.Fatalf("Book Search: %v", err)
	}
	if got := recorder.last().Get("searchstr"); got != "Dune" {
		t.Errorf("book searchstr = %q", got)
	}

	if _, err := connector.Search(context.Background(), engine.SearchRequest{Mode: "bogus"}); err == nil {
		t.Fatal("unsupported mode did not fail")
	}

	caps := connector.Capabilities()
	if caps.Protocol != engine.ProtocolTorrent {
		t.Errorf("protocol = %q", caps.Protocol)
	}
	for _, mode := range []engine.Mode{engine.ModeSearch, engine.ModeTV, engine.ModeMovie, engine.ModeMusic, engine.ModeBook} {
		if !caps.Supports(mode) {
			t.Errorf("mode %q not declared", mode)
		}
	}
	if !caps.SupportsParam(engine.ModeMusic, engine.ParamArtist) ||
		!caps.SupportsParam(engine.ModeMusic, engine.ParamAlbum) ||
		!caps.SupportsParam(engine.ModeTV, engine.ParamSeason) {
		t.Errorf("params = %v", caps.Params)
	}
	if len(caps.Categories) != 2 || caps.Categories[0] != 3000 || caps.Categories[1] != 5000 {
		t.Errorf("categories = %v", caps.Categories)
	}
}

func TestGazelleTestPhasesAndFailureRedaction(t *testing.T) {
	const apiKey = "super-secret-gazelle-key"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("searchstr") != "" {
			_, _ = io.WriteString(w, `{"status":"error","error":"token `+apiKey+` is invalid"}`)
			return
		}
		_, _ = io.WriteString(w, `{"status":"success","response":{"results":[]}}`)
	}))
	defer server.Close()

	connector, err := NewGazelle(GazelleConfig{
		BaseURL:   server.URL,
		APIKey:    apiKey,
		Transport: newTestTransport(),
	})
	if err != nil {
		t.Fatalf("NewGazelle: %v", err)
	}
	result, err := connector.Test(context.Background())
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	if result.OK {
		t.Fatal("Test reported OK with an invalid token")
	}
	names := make([]string, 0, len(result.Phases))
	for _, phase := range result.Phases {
		names = append(names, phase.Name)
	}
	if strings.Join(names, ",") != "reach,search" {
		t.Errorf("phases = %v", names)
	}
	byName := map[string]engine.TestPhase{}
	for _, phase := range result.Phases {
		byName[phase.Name] = phase
		if strings.Contains(phase.Message, apiKey) {
			t.Fatalf("phase %q leaked the API key: %s", phase.Name, phase.Message)
		}
	}
	if !byName["reach"].OK {
		t.Errorf("reach phase = %+v", byName["reach"])
	}
	if byName["search"].OK {
		t.Errorf("search phase = %+v, want failure", byName["search"])
	}
}

func TestGazelleCustomPathsFlatResponseAndCategoryOverride(t *testing.T) {
	const hash = "3333333333333333333333333333333333333333"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/browse.php":
			if r.URL.Query().Get("action") != "browse" || r.URL.Query().Get("searchstr") != "Flat" {
				http.Error(w, "bad browse query", http.StatusBadRequest)
				return
			}
			if r.Header.Get("Authorization") != "token flat-token" {
				http.Error(w, "bad token", http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"results":[{"groupId":"9","groupName":"Flat.Group","categoryId":"1","torrents":[{"torrentId":"90","infoHash":"`+hash+`","size":1000,"seeders":2,"leechers":0,"format":"MP3"}]}]}`)
		case "/api/download.php":
			if r.URL.Query().Get("id") != "90" || r.URL.Query().Get("action") != "download" {
				http.Error(w, "bad download query", http.StatusBadRequest)
				return
			}
			_, _ = io.WriteString(w, "d8:announce5:https")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	connector, err := NewGazelle(GazelleConfig{
		BaseURL:      server.URL,
		APIKey:       "flat-token",
		Transport:    newTestTransport(),
		BrowsePath:   "api/browse.php",
		DownloadPath: "/api/download.php",
		CategoryMap:  map[int]int{1: 3060},
	})
	if err != nil {
		t.Fatalf("NewGazelle: %v", err)
	}
	items, err := connector.Search(context.Background(), engine.SearchRequest{Query: "Flat"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}
	if items[0].Title != "Flat.Group MP3" || items[0].GUID != "gazelle:90" {
		t.Errorf("title/guid = %q/%q", items[0].Title, items[0].GUID)
	}
	if len(items[0].Categories) != 1 || items[0].Categories[0] != 3060 {
		t.Errorf("categories = %v, want the configured [3060]", items[0].Categories)
	}
	if want := server.URL + "/api/download.php?action=download&id=90"; items[0].DownloadURL != want {
		t.Errorf("download = %q, want %q", items[0].DownloadURL, want)
	}

	payload, err := connector.Download(context.Background(), engine.Item{
		Title:      "Flat.Release",
		Attributes: map[string]string{"torrent_id": "90"},
	})
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if string(payload.Body) != "d8:announce5:https" || payload.ContentType != "application/x-bittorrent" {
		t.Errorf("payload = %q/%q", payload.Body, payload.ContentType)
	}
}

func TestDecodeGazelleResultsShapes(t *testing.T) {
	results, err := decodeGazelleResults([]byte(`{"results":[{"groupId":1}]}`))
	if err != nil || len(results) != 1 {
		t.Fatalf("flat results = %d/%v", len(results), err)
	}
	results, err = decodeGazelleResults([]byte(`{"response":[{"groupId":1},{"groupId":2}]}`))
	if err != nil || len(results) != 2 {
		t.Fatalf("array response = %d/%v", len(results), err)
	}
	if _, err := decodeGazelleResults([]byte(`not json`)); err == nil {
		t.Error("malformed JSON did not fail")
	}
	if _, err := decodeGazelleResults([]byte(`{"status":"failure","error":"nope"}`)); err == nil {
		t.Error("failure status did not fail")
	}
	if _, err := decodeGazelleResults([]byte(`{"status":"success"}`)); err == nil {
		t.Error("missing results did not fail")
	}
}

func TestNewGazelleValidation(t *testing.T) {
	transport := newTestTransport()
	if _, err := NewGazelle(GazelleConfig{APIKey: "x", Transport: transport}); err == nil {
		t.Error("missing base URL did not fail")
	}
	if _, err := NewGazelle(GazelleConfig{BaseURL: "ftp://tracker.example", APIKey: "x", Transport: transport}); err == nil {
		t.Error("non-HTTP base URL did not fail")
	}
	if _, err := NewGazelle(GazelleConfig{BaseURL: "https://tracker.example", Transport: transport}); err == nil {
		t.Error("missing API key did not fail")
	}
}
