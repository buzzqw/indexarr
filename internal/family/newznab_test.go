package family

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/buzzqw/indexarr/internal/engine"
)

func TestParseCaps(t *testing.T) {
	caps, err := ParseCaps(readFixture(t, "newznab_caps.xml"))
	if err != nil {
		t.Fatalf("ParseCaps: %v", err)
	}
	wantModes := []engine.Mode{engine.ModeSearch, engine.ModeTV, engine.ModeMovie, engine.ModeBook}
	if len(caps.Modes) != len(wantModes) {
		t.Fatalf("modes = %v, want %v", caps.Modes, wantModes)
	}
	for i, mode := range wantModes {
		if caps.Modes[i] != mode {
			t.Errorf("mode %d = %q, want %q", i, caps.Modes[i], mode)
		}
	}
	wantTVParams := []string{
		engine.ParamQuery,
		engine.ParamSeason,
		engine.ParamEpisode,
		engine.ParamIMDbID,
		engine.ParamTVDbID,
		engine.ParamTVMazeID,
	}
	if got := caps.Params[engine.ModeTV]; strings.Join(got, ",") != strings.Join(wantTVParams, ",") {
		t.Errorf("tv params = %v, want %v", got, wantTVParams)
	}
	if caps.Limits.Max != 10000 || caps.Limits.Default != 50 {
		t.Errorf("limits = %+v", caps.Limits)
	}
	if len(caps.Categories) != 2 {
		t.Fatalf("categories = %+v", caps.Categories)
	}
	if caps.Categories[0].ID != 2000 || caps.Categories[0].Name != "Movies" {
		t.Errorf("category[0] = %+v", caps.Categories[0])
	}
	if len(caps.Categories[0].Subcategories) != 2 ||
		caps.Categories[0].Subcategories[0].ID != 2040 ||
		caps.Categories[0].Subcategories[1].ID != 2045 {
		t.Errorf("subcategories = %+v", caps.Categories[0].Subcategories)
	}
	if len(caps.Categories[1].Subcategories) != 1 || caps.Categories[1].Subcategories[0].ID != 5040 {
		t.Errorf("tv subcategories = %+v", caps.Categories[1].Subcategories)
	}

	for _, body := range []string{"", "not xml", "<rss></rss>", "<caps><searching>"} {
		if _, err := ParseCaps([]byte(body)); err == nil {
			t.Errorf("ParseCaps(%q) succeeded, want error", body)
		}
	}
}

func TestNewznabSearchParsingAndParams(t *testing.T) {
	fixture := readFixture(t, "newznab_search.xml")
	recorder := &queryRecorder{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api" {
			http.NotFound(w, r)
			return
		}
		recorder.add(r.URL.Query())
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = w.Write(fixture)
	}))
	defer server.Close()

	connector, err := NewNewznab(NewznabConfig{
		Indexer:   engine.IndexerRef{Slug: "nzb"},
		BaseURL:   server.URL + "/api",
		APIKey:    "secret-key",
		Transport: newTestTransport(),
	})
	if err != nil {
		t.Fatalf("NewNewznab: %v", err)
	}

	items, err := connector.Search(context.Background(), engine.SearchRequest{
		Mode:       engine.ModeTV,
		Query:      "the show",
		Season:     2,
		Episode:    3,
		IMDbID:     "tt123",
		TMDbID:     "456",
		TVDbID:     "789",
		Categories: []int{2040, 5040},
		Limit:      50,
		Offset:     100,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2 (malformed rows must be skipped)", len(items))
	}

	query := recorder.last()
	checks := map[string]string{
		"t":        "tvsearch",
		"q":        "the show",
		"season":   "2",
		"ep":       "3",
		"imdbid":   "tt123",
		"tmdbid":   "456",
		"tvdbid":   "789",
		"cat":      "2040,5040",
		"limit":    "50",
		"offset":   "100",
		"extended": "1",
		"apikey":   "secret-key",
	}
	for key, want := range checks {
		if got := query.Get(key); got != want {
			t.Errorf("query %s = %q, want %q", key, got, want)
		}
	}

	first := items[0]
	if first.Protocol != engine.ProtocolUsenet {
		t.Errorf("protocol = %q", first.Protocol)
	}
	if first.Title != "Example.Show.S02E03.1080p.WEB-DL" || first.GUID != "abc123guid" {
		t.Errorf("title/guid = %q/%q", first.Title, first.GUID)
	}
	if first.DetailsURL != "https://indexer.example/details/abc123#comments" {
		t.Errorf("details URL = %q", first.DetailsURL)
	}
	if first.DownloadURL != "https://indexer.example/api?t=get&id=abc123guid&apikey=SECRET" {
		t.Errorf("download URL = %q", first.DownloadURL)
	}
	if first.Size != 1234567890 || first.Seeders != 12 || first.Leechers != 3 {
		t.Errorf("size/seeds/leech = %d/%d/%d", first.Size, first.Seeders, first.Leechers)
	}
	if len(first.Categories) != 1 || first.Categories[0] != 5040 {
		t.Errorf("categories = %v, want [5040]", first.Categories)
	}
	if first.Published.IsZero() || first.Published.Year() != 2024 {
		t.Errorf("published = %v", first.Published)
	}
	if first.IMDbID != "tt1234567" || first.TMDbID != "456" || first.TVDbID != "789" {
		t.Errorf("ids = %q/%q/%q", first.IMDbID, first.TMDbID, first.TVDbID)
	}
	if first.InfoHash != "" {
		t.Errorf("usenet item infohash = %q, want empty", first.InfoHash)
	}
	if first.Attributes["infohash"] != "should-not-be-used" {
		t.Errorf("infohash attribute was not preserved: %q", first.Attributes["infohash"])
	}

	second := items[1]
	if second.GUID != "def456guid" || second.Size != 987654321 {
		t.Errorf("second guid/size = %q/%d", second.GUID, second.Size)
	}
	if !strings.HasPrefix(second.DownloadURL, server.URL+"/api?") {
		t.Errorf("second download URL = %q", second.DownloadURL)
	}
	if len(second.Categories) != 1 || second.Categories[0] != 2040 {
		t.Errorf("second categories = %v", second.Categories)
	}
	if second.Published.Year() != 2024 {
		t.Errorf("second published = %v", second.Published)
	}
}

func TestNewznabSearchModes(t *testing.T) {
	recorder := &queryRecorder{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder.add(r.URL.Query())
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = io.WriteString(w, `<rss version="2.0"><channel></channel></rss>`)
	}))
	defer server.Close()

	connector, err := NewNewznab(NewznabConfig{
		BaseURL:   server.URL,
		APIKey:    "secret-key",
		Transport: newTestTransport(),
	})
	if err != nil {
		t.Fatalf("NewNewznab: %v", err)
	}

	cases := []struct {
		name  string
		req   engine.SearchRequest
		check map[string]string
	}{
		{
			name: "music",
			req: engine.SearchRequest{
				Mode:   engine.ModeMusic,
				Artist: "Artist Name",
				Album:  "Album Name",
				RID:    "1234",
			},
			check: map[string]string{
				"t": "music", "artist": "Artist Name", "album": "Album Name", "rid": "1234",
			},
		},
		{
			name: "book",
			req: engine.SearchRequest{
				Mode:   engine.ModeBook,
				Author: "Author Name",
				Title:  "Book Title",
			},
			check: map[string]string{
				"t": "book", "author": "Author Name", "title": "Book Title",
			},
		},
		{
			name: "movie",
			req: engine.SearchRequest{
				Mode:   engine.ModeMovie,
				IMDbID: "tt999",
				TMDbID: "777",
			},
			check: map[string]string{
				"t": "movie", "imdbid": "tt999", "tmdbid": "777",
			},
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if _, err := connector.Search(context.Background(), test.req); err != nil {
				t.Fatalf("Search: %v", err)
			}
			query := recorder.last()
			for key, want := range test.check {
				if got := query.Get(key); got != want {
					t.Errorf("query %s = %q, want %q", key, got, want)
				}
			}
		})
	}
}

func TestNewznabSearchErrorNeverLeaksAPIKey(t *testing.T) {
	const apiKey = "secret-key"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		_, _ = io.WriteString(w, `<error code="100" description="Invalid API key `+apiKey+`"/>`)
	}))
	defer server.Close()

	connector, err := NewNewznab(NewznabConfig{
		BaseURL:   server.URL,
		APIKey:    apiKey,
		Transport: newTestTransport(),
	})
	if err != nil {
		t.Fatalf("NewNewznab: %v", err)
	}
	_, err = connector.Search(context.Background(), engine.SearchRequest{Query: "test"})
	if err == nil {
		t.Fatal("Search succeeded, want API error")
	}
	if strings.Contains(err.Error(), apiKey) {
		t.Fatalf("error leaked API key: %v", err)
	}
	if !strings.Contains(err.Error(), "100") {
		t.Errorf("error = %v, want the Newznab error code", err)
	}
}

func TestParseNewznabRSSMalformed(t *testing.T) {
	for _, body := range []string{"", "not xml", "<rss></rss>", "<html></html>"} {
		if _, err := parseNewznabRSS([]byte(body)); err == nil {
			t.Errorf("parseNewznabRSS(%q) succeeded, want error", body)
		}
	}
	rows, err := parseNewznabRSS([]byte(`<rss><channel></channel></rss>`))
	if err != nil {
		t.Fatalf("empty feed: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("empty feed returned %d rows", len(rows))
	}
}

func TestNewznabCapsCache(t *testing.T) {
	fixture := readFixture(t, "newznab_caps.xml")
	var calls atomic.Int32
	var fail atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Query().Get("t") != "caps" {
			t.Errorf("caps request t = %q", r.URL.Query().Get("t"))
		}
		if fail.Load() {
			http.Error(w, "nope", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write(fixture)
	}))
	defer server.Close()

	connector, err := NewNewznab(NewznabConfig{
		BaseURL:   server.URL,
		APIKey:    "secret-key",
		Transport: newTestTransport(),
	})
	if err != nil {
		t.Fatalf("NewNewznab: %v", err)
	}

	first, err := connector.Caps(context.Background())
	if err != nil {
		t.Fatalf("Caps: %v", err)
	}
	second, err := connector.Caps(context.Background())
	if err != nil {
		t.Fatalf("Caps cached: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("caps calls = %d, want 1 (cache miss)", got)
	}
	if first.Limits.Max != second.Limits.Max || len(second.Modes) != 4 {
		t.Errorf("cached caps = %+v", second)
	}

	connector.capsMu.Lock()
	connector.capsAt = time.Now().Add(-2 * newznabCapsTTL)
	connector.capsMu.Unlock()
	if _, err := connector.Caps(context.Background()); err != nil {
		t.Fatalf("Caps after expiry: %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("caps calls = %d, want 2 after expiry", got)
	}

	fail.Store(true)
	connector.capsMu.Lock()
	connector.capsAt = time.Now().Add(-2 * newznabCapsTTL)
	connector.capsMu.Unlock()
	_, err = connector.Caps(context.Background())
	if err == nil {
		t.Fatal("Caps succeeded, want failure")
	}
	if strings.Contains(err.Error(), "secret-key") {
		t.Fatalf("caps error leaked key: %v", err)
	}
	connector.capsMu.Lock()
	cachedAfterError := connector.caps
	connector.capsMu.Unlock()
	if cachedAfterError != nil {
		t.Error("failed caps request left a cached document")
	}

	fail.Store(false)
	if _, err := connector.Caps(context.Background()); err != nil {
		t.Fatalf("Caps after recovery: %v", err)
	}
	if got := calls.Load(); got != 4 {
		t.Errorf("caps calls = %d, want 4", got)
	}
}

func TestNewznabCapabilitiesUsesCaps(t *testing.T) {
	fixture := readFixture(t, "newznab_caps.xml")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(fixture)
	}))
	defer server.Close()

	connector, err := NewNewznab(NewznabConfig{
		BaseURL:    server.URL,
		APIKey:     "secret-key",
		Transport:  newTestTransport(),
		Categories: []int{111},
	})
	if err != nil {
		t.Fatalf("NewNewznab: %v", err)
	}

	before := connector.Capabilities()
	if before.Protocol != engine.ProtocolUsenet {
		t.Errorf("protocol = %q", before.Protocol)
	}
	for _, mode := range []engine.Mode{
		engine.ModeSearch, engine.ModeTV, engine.ModeMovie, engine.ModeMusic, engine.ModeBook,
	} {
		if !before.Supports(mode) {
			t.Errorf("default capabilities missing mode %q", mode)
		}
	}
	if len(before.Categories) != 1 || before.Categories[0] != 111 {
		t.Errorf("default categories = %v", before.Categories)
	}

	if _, err := connector.Caps(context.Background()); err != nil {
		t.Fatalf("Caps: %v", err)
	}
	after := connector.Capabilities()
	if after.Supports(engine.ModeMusic) {
		t.Error("caps said music was unavailable, but it is advertised")
	}
	if !after.Supports(engine.ModeBook) || !after.SupportsParam(engine.ModeTV, engine.ParamSeason) {
		t.Errorf("caps capabilities = %+v", after)
	}
	wantCategories := []int{2000, 2040, 2045, 5000, 5040}
	if len(after.Categories) != len(wantCategories) {
		t.Fatalf("categories = %v, want %v", after.Categories, wantCategories)
	}
	for i, want := range wantCategories {
		if after.Categories[i] != want {
			t.Errorf("category %d = %d, want %d", i, after.Categories[i], want)
		}
	}
}

func TestNewznabTestPhases(t *testing.T) {
	const apiKey = "secret-key"
	fixture := readFixture(t, "newznab_caps.xml")
	var capsFail atomic.Bool
	var searchFail atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		switch query.Get("t") {
		case "caps":
			if capsFail.Load() && query.Get("apikey") != "" {
				http.Error(w, "caps exploded", http.StatusInternalServerError)
				return
			}
			_, _ = w.Write(fixture)
		case "search":
			if searchFail.Load() {
				http.Error(w, "search exploded", http.StatusInternalServerError)
				return
			}
			_, _ = io.WriteString(w, `<rss version="2.0"><channel></channel></rss>`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	connector, err := NewNewznab(NewznabConfig{
		BaseURL:   server.URL,
		APIKey:    apiKey,
		Transport: newTestTransport(),
	})
	if err != nil {
		t.Fatalf("NewNewznab: %v", err)
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
	if strings.Join(names, ",") != "reach,caps,search" {
		t.Errorf("phases = %v", names)
	}

	capsFail.Store(true)
	result, err = connector.Test(context.Background())
	if err != nil {
		t.Fatalf("Test with caps failure: %v", err)
	}
	if result.OK {
		t.Fatal("Test reported OK with failing caps")
	}
	byName := map[string]engine.TestPhase{}
	for _, phase := range result.Phases {
		byName[phase.Name] = phase
		if strings.Contains(phase.Message, apiKey) {
			t.Fatalf("phase %q leaked API key: %s", phase.Name, phase.Message)
		}
	}
	if !byName["reach"].OK {
		t.Errorf("reach = %+v, want success", byName["reach"])
	}
	if byName["caps"].OK {
		t.Errorf("caps = %+v, want failure", byName["caps"])
	}
	if !byName["search"].OK {
		t.Errorf("search = %+v, want success", byName["search"])
	}
	if !strings.Contains(byName["caps"].Message, "500") {
		t.Errorf("caps message = %q, want HTTP status", byName["caps"].Message)
	}

	capsFail.Store(false)
	searchFail.Store(true)
	result, err = connector.Test(context.Background())
	if err != nil {
		t.Fatalf("Test with search failure: %v", err)
	}
	if result.OK {
		t.Fatal("Test reported OK with failing search")
	}
	for _, phase := range result.Phases {
		if phase.Name == "search" && phase.OK {
			t.Errorf("search phase = %+v, want failure", phase)
		}
	}
}

func TestNewznabDownload(t *testing.T) {
	const apiKey = "secret-key"
	var fail atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		if query.Get("t") != "get" || query.Get("id") != "abc123guid" || query.Get("apikey") != apiKey {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if fail.Load() {
			http.Error(w, "missing", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/x-nzb")
		_, _ = io.WriteString(w, "<nzb></nzb>")
	}))
	defer server.Close()

	connector, err := NewNewznab(NewznabConfig{
		BaseURL:   server.URL,
		APIKey:    apiKey,
		Transport: newTestTransport(),
	})
	if err != nil {
		t.Fatalf("NewNewznab: %v", err)
	}
	payload, err := connector.Download(context.Background(), engine.Item{
		Title:       "Example/Release.Name",
		GUID:        "abc123guid",
		DownloadURL: "http://127.0.0.1:1/ignored",
	})
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if string(payload.Body) != "<nzb></nzb>" {
		t.Errorf("body = %q", payload.Body)
	}
	if payload.ContentType != "application/x-nzb" {
		t.Errorf("content type = %q", payload.ContentType)
	}
	if payload.Filename != "Example_Release.Name.nzb" {
		t.Errorf("filename = %q", payload.Filename)
	}

	fail.Store(true)
	_, err = connector.Download(context.Background(), engine.Item{Title: "Example", GUID: "abc123guid"})
	if err == nil {
		t.Fatal("Download succeeded, want HTTP error")
	}
	if strings.Contains(err.Error(), apiKey) {
		t.Fatalf("download error leaked API key: %v", err)
	}
	if !strings.Contains(err.Error(), strconv.Itoa(http.StatusNotFound)) {
		t.Errorf("error = %v, want HTTP status", err)
	}
}

func TestNewNewznabValidation(t *testing.T) {
	transport := newTestTransport()
	if _, err := NewNewznab(NewznabConfig{Transport: transport}); err == nil {
		t.Error("missing base URL did not fail")
	}
	if _, err := NewNewznab(NewznabConfig{BaseURL: "ftp://indexer.example", Transport: transport}); err == nil {
		t.Error("non-HTTP base URL did not fail")
	}
}
