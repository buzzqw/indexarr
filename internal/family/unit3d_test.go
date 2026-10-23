package family

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/buzzqw/indexarr/internal/engine"
)

func newTestTransport() *engine.Transport {
	return engine.NewTransport(engine.TransportConfig{AllowPrivate: true, UserAgent: "family-test"})
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return body
}

type queryRecorder struct {
	mu     sync.Mutex
	values []url.Values
}

func (r *queryRecorder) add(values url.Values) {
	clone := make(url.Values, len(values))
	for key, list := range values {
		clone[key] = append([]string(nil), list...)
	}
	r.mu.Lock()
	r.values = append(r.values, clone)
	r.mu.Unlock()
}

func (r *queryRecorder) at(index int) url.Values {
	r.mu.Lock()
	defer r.mu.Unlock()
	if index < 0 || index >= len(r.values) {
		return nil
	}
	return r.values[index]
}

func (r *queryRecorder) last() url.Values {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.values) == 0 {
		return nil
	}
	return r.values[len(r.values)-1]
}

func TestUnit3DSearchParsingAndParams(t *testing.T) {
	fixture := readFixture(t, "unit3d_search.json")
	recorder := &queryRecorder{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/torrents/filter" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Accept") != "application/json" {
			http.Error(w, "missing accept", http.StatusBadRequest)
			return
		}
		if r.Header.Get("User-Agent") != "unit3d-test-agent" {
			http.Error(w, "missing user agent", http.StatusBadRequest)
			return
		}
		recorder.add(r.URL.Query())
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(fixture)
	}))
	defer server.Close()

	connector, err := NewUnit3D(Unit3DConfig{
		Indexer:    engine.IndexerRef{Slug: "tracker"},
		BaseURL:    server.URL,
		APIKey:     "secret-token",
		Transport:  newTestTransport(),
		Categories: []int{5000},
		UserAgent:  "unit3d-test-agent",
	})
	if err != nil {
		t.Fatalf("NewUnit3D: %v", err)
	}

	items, err := connector.Search(context.Background(), engine.SearchRequest{
		Mode:       engine.ModeTV,
		Query:      "The Show",
		Season:     2,
		Episode:    3,
		IMDbID:     "tt123",
		TMDbID:     "456",
		TVDbID:     "789",
		Categories: []int{5000, 5040},
		Limit:      25,
		Offset:     50,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2 (malformed rows must be skipped)", len(items))
	}

	query := recorder.last()
	checks := map[string]string{
		"api_token":     "secret-token",
		"search":        "The Show S02E03",
		"name":          "The Show S02E03",
		"perPage":       "25",
		"page":          "3",
		"sortField":     "created_at",
		"sortDirection": "desc",
		"imdbId":        "tt123",
		"tmdbId":        "456",
		"tvdbId":        "789",
	}
	for key, want := range checks {
		if got := query.Get(key); got != want {
			t.Errorf("query %s = %q, want %q", key, got, want)
		}
	}
	if got := query["categories[]"]; len(got) != 2 || got[0] != "5000" || got[1] != "5040" {
		t.Errorf("categories[] = %v, want [5000 5040]", got)
	}

	first := items[0]
	if first.Title != "Example.Movie.2024.1080p.WEB-DL.x264-GROUP" {
		t.Errorf("title = %q", first.Title)
	}
	if first.GUID != "1" || first.InfoHash != "ABC123" {
		t.Errorf("guid/infohash = %q/%q", first.GUID, first.InfoHash)
	}
	if first.Size != 1234567890 || first.Seeders != 12 || first.Leechers != 3 {
		t.Errorf("size/seeds/leech = %d/%d/%d", first.Size, first.Seeders, first.Leechers)
	}
	if len(first.Categories) != 1 || first.Categories[0] != 2040 {
		t.Errorf("categories = %v, want [2040]", first.Categories)
	}
	if first.IMDbID != "67890" || first.TMDbID != "12345" || first.TVDbID != "" {
		t.Errorf("ids = %q/%q/%q", first.IMDbID, first.TMDbID, first.TVDbID)
	}
	if first.Published.IsZero() || first.Published.Year() != 2024 {
		t.Errorf("published = %v", first.Published)
	}
	if first.Attributes["category"] != "Movies/HD" {
		t.Errorf("attribute category = %q", first.Attributes["category"])
	}
	wantDownload := server.URL + "/dl/1.torrent"
	if first.DownloadURL != wantDownload {
		t.Errorf("download URL = %q, want %q", first.DownloadURL, wantDownload)
	}

	second := items[1]
	if second.GUID != "9" || second.InfoHash != "DEADBEEF" {
		t.Errorf("guid/infohash = %q/%q", second.GUID, second.InfoHash)
	}
	if second.Magnet == "" || second.DownloadURL != server.URL+"/dl/9.torrent" {
		t.Errorf("magnet/download = %q/%q", second.Magnet, second.DownloadURL)
	}
	if len(second.Categories) != 1 || second.Categories[0] != 5040 {
		t.Errorf("categories = %v, want [5040]", second.Categories)
	}

	for _, item := range items {
		for key, value := range item.Attributes {
			if strings.Contains(value, "secret-token") {
				t.Fatalf("attribute %q leaked the API token", key)
			}
		}
		if strings.Contains(item.GUID, "secret-token") {
			t.Fatal("GUID leaked the API token")
		}
	}
}

func TestUnit3DLatest(t *testing.T) {
	recorder := &queryRecorder{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder.add(r.URL.Query())
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[],"meta":{"current_page":1,"last_page":1}}`)
	}))
	defer server.Close()

	connector, err := NewUnit3D(Unit3DConfig{
		Indexer:    engine.IndexerRef{Slug: "tracker"},
		BaseURL:    server.URL,
		APIKey:     "secret-token",
		Transport:  newTestTransport(),
		Categories: []int{5000},
	})
	if err != nil {
		t.Fatalf("NewUnit3D: %v", err)
	}
	items, err := connector.Latest(context.Background(), engine.LatestRequest{Limit: 10})
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("got %d items, want 0", len(items))
	}
	query := recorder.last()
	if query.Get("page") != "1" || query.Get("perPage") != "10" {
		t.Errorf("pagination = page %q perPage %q", query.Get("page"), query.Get("perPage"))
	}
	if query.Get("search") != "" || query.Get("name") != "" {
		t.Errorf("latest must not send a search term: %v", query)
	}
	if cats := query["categories[]"]; len(cats) != 1 || cats[0] != "5000" {
		t.Errorf("categories[] = %v", cats)
	}
}

func TestUnit3DDownloadTorrent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/files/release.torrent" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/x-bittorrent")
		_, _ = io.WriteString(w, "d8:announce5:https")
	}))
	defer server.Close()

	connector, err := NewUnit3D(Unit3DConfig{
		BaseURL:   server.URL,
		APIKey:    "secret-token",
		Transport: newTestTransport(),
	})
	if err != nil {
		t.Fatalf("NewUnit3D: %v", err)
	}
	payload, err := connector.Download(context.Background(), engine.Item{
		Title:       "Example.Release",
		DownloadURL: server.URL + "/files/release.torrent",
	})
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if string(payload.Body) != "d8:announce5:https" {
		t.Errorf("body = %q", payload.Body)
	}
	if payload.ContentType != "application/x-bittorrent" {
		t.Errorf("content type = %q", payload.ContentType)
	}
	if payload.Filename != "Example.Release.torrent" {
		t.Errorf("filename = %q", payload.Filename)
	}
}

func TestUnit3DDownloadMagnetRedirect(t *testing.T) {
	const magnet = "magnet:?xt=urn:btih:0123456789abcdef&dn=Example"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", magnet)
		w.WriteHeader(http.StatusFound)
	}))
	defer server.Close()

	connector, err := NewUnit3D(Unit3DConfig{
		BaseURL:   server.URL,
		APIKey:    "secret-token",
		Transport: newTestTransport(),
	})
	if err != nil {
		t.Fatalf("NewUnit3D: %v", err)
	}
	payload, err := connector.Download(context.Background(), engine.Item{
		Title:       "Example.Release",
		DownloadURL: server.URL + "/download/9",
	})
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if payload.Magnet != magnet {
		t.Errorf("magnet = %q, want %q", payload.Magnet, magnet)
	}
	if len(payload.Body) != 0 {
		t.Errorf("magnet redirect should not return a body, got %q", payload.Body)
	}
}

func TestUnit3DDownloadFallbackNeverLeaksAPIKey(t *testing.T) {
	const apiKey = "super-secret-api-token"
	recorder := &queryRecorder{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder.add(r.URL.Query())
		if r.URL.Path != "/api/torrents/7/download" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("api_token") != apiKey {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		http.Error(w, "server exploded", http.StatusInternalServerError)
	}))
	defer server.Close()

	connector, err := NewUnit3D(Unit3DConfig{
		BaseURL:   server.URL,
		APIKey:    apiKey,
		Transport: newTestTransport(),
	})
	if err != nil {
		t.Fatalf("NewUnit3D: %v", err)
	}
	_, err = connector.Download(context.Background(), engine.Item{
		Title:      "Example.Release",
		Attributes: map[string]string{"id": "7"},
	})
	if err == nil {
		t.Fatal("Download succeeded, want an HTTP error")
	}
	if strings.Contains(err.Error(), apiKey) {
		t.Fatalf("error leaked API key: %v", err)
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error = %v, want the HTTP status", err)
	}
	if got := recorder.last().Get("api_token"); got != apiKey {
		t.Errorf("download api_token = %q", got)
	}
}

func TestUnit3DTestPhases(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/torrents/filter" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("api_token") != "secret-token" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"message":"unauthorized"}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[],"meta":{"current_page":1,"last_page":1}}`)
	}))
	defer server.Close()

	connector, err := NewUnit3D(Unit3DConfig{
		BaseURL:   server.URL,
		APIKey:    "secret-token",
		Transport: newTestTransport(),
	})
	if err != nil {
		t.Fatalf("NewUnit3D: %v", err)
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
	if strings.Join(names, ",") != "reach,auth,search" {
		t.Errorf("phases = %v", names)
	}
	for _, phase := range result.Phases {
		if strings.Contains(phase.Message, "secret-token") {
			t.Fatalf("phase %q leaked key: %s", phase.Name, phase.Message)
		}
	}
}

func TestUnit3DTestReportsAuthFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("api_token") != "secret-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Query().Get("search") == "test" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"data":[],"meta":{"current_page":1,"last_page":1}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"message":"bad token"}`)
	}))
	defer server.Close()

	connector, err := NewUnit3D(Unit3DConfig{
		BaseURL:   server.URL,
		APIKey:    "secret-token",
		Transport: newTestTransport(),
	})
	if err != nil {
		t.Fatalf("NewUnit3D: %v", err)
	}
	result, err := connector.Test(context.Background())
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	if result.OK {
		t.Fatal("Test reported OK with an invalid auth response")
	}
	byName := map[string]engine.TestPhase{}
	for _, phase := range result.Phases {
		byName[phase.Name] = phase
	}
	if !byName["reach"].OK {
		t.Errorf("reach phase = %+v", byName["reach"])
	}
	if byName["auth"].OK {
		t.Errorf("auth phase = %+v, want failure", byName["auth"])
	}
	if !byName["search"].OK {
		t.Errorf("search phase = %+v, want success", byName["search"])
	}
}

func TestUnit3DCapabilities(t *testing.T) {
	connector, err := NewUnit3D(Unit3DConfig{
		BaseURL:    "https://tracker.example",
		APIKey:     "secret-token",
		Transport:  newTestTransport(),
		Categories: []int{2040, 5040},
	})
	if err != nil {
		t.Fatalf("NewUnit3D: %v", err)
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
	if !caps.SupportsParam(engine.ModeTV, engine.ParamSeason) ||
		!caps.SupportsParam(engine.ModeTV, engine.ParamEpisode) ||
		!caps.SupportsParam(engine.ModeMovie, engine.ParamIMDbID) {
		t.Errorf("params = %v", caps.Params)
	}
	if len(caps.Categories) != 2 || caps.Categories[0] != 2040 || caps.Categories[1] != 5040 {
		t.Errorf("categories = %v", caps.Categories)
	}
}

func TestNewUnit3DValidation(t *testing.T) {
	transport := newTestTransport()
	if _, err := NewUnit3D(Unit3DConfig{APIKey: "x", Transport: transport}); err == nil {
		t.Error("missing base URL did not fail")
	}
	if _, err := NewUnit3D(Unit3DConfig{BaseURL: "ftp://tracker.example", APIKey: "x", Transport: transport}); err == nil {
		t.Error("non-HTTP base URL did not fail")
	}
	if _, err := NewUnit3D(Unit3DConfig{BaseURL: "https://tracker.example", Transport: transport}); err == nil {
		t.Error("missing API key did not fail")
	}
}
