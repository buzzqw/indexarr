package family

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/buzzqw/indexarr/internal/engine"
)

func TestParseTorznabCaps(t *testing.T) {
	caps, err := ParseTorznabCaps(readFixture(t, "torznab_caps.xml"))
	if err != nil {
		t.Fatalf("ParseTorznabCaps: %v", err)
	}
	wantModes := []engine.Mode{engine.ModeSearch, engine.ModeTV, engine.ModeMovie, engine.ModeMusic}
	if len(caps.Modes) != len(wantModes) {
		t.Fatalf("modes = %v, want %v", caps.Modes, wantModes)
	}
	for i, mode := range wantModes {
		if caps.Modes[i] != mode {
			t.Errorf("mode %d = %q, want %q", i, caps.Modes[i], mode)
		}
	}
	if capsSupports(caps, engine.ModeBook) {
		t.Error("caps reported book search as available")
	}
	wantTVParams := []string{
		engine.ParamQuery,
		engine.ParamSeason,
		engine.ParamEpisode,
		engine.ParamIMDbID,
		engine.ParamTVDbID,
	}
	if got := caps.Params[engine.ModeTV]; strings.Join(got, ",") != strings.Join(wantTVParams, ",") {
		t.Errorf("tv params = %v, want %v", got, wantTVParams)
	}
	if caps.Limits.Max != 100 || caps.Limits.Default != 50 {
		t.Errorf("limits = %+v", caps.Limits)
	}
	if len(caps.Categories) != 2 || caps.Categories[0].ID != 2000 ||
		len(caps.Categories[0].Subcategories) != 2 {
		t.Errorf("categories = %+v", caps.Categories)
	}

	for _, body := range []string{"", "not xml", "<rss></rss>", "<caps><searching>"} {
		if _, err := ParseTorznabCaps([]byte(body)); err == nil {
			t.Errorf("ParseTorznabCaps(%q) succeeded, want error", body)
		}
	}
}

// capsSupports is a local helper because Caps does not carry the engine's
// Supports method.
func capsSupports(caps *Caps, mode engine.Mode) bool {
	for _, declared := range caps.Modes {
		if declared == mode {
			return true
		}
	}
	return false
}

func TestTorznabSearchParsingAndParams(t *testing.T) {
	fixture := readFixture(t, "torznab_search.xml")
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

	const apiKey = "secret-key"
	connector, err := NewTorznab(TorznabConfig{
		Indexer:   engine.IndexerRef{Slug: "tz"},
		BaseURL:   server.URL + "/api",
		APIKey:    apiKey,
		Transport: newTestTransport(),
	})
	if err != nil {
		t.Fatalf("NewTorznab: %v", err)
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
	if len(items) != 3 {
		t.Fatalf("got %d items, want 3 (malformed rows must be skipped)", len(items))
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
		"apikey":   apiKey,
	}
	for key, want := range checks {
		if got := query.Get(key); got != want {
			t.Errorf("query %s = %q, want %q", key, got, want)
		}
	}

	first := items[0]
	if first.Protocol != engine.ProtocolTorrent {
		t.Errorf("protocol = %q", first.Protocol)
	}
	if first.Title != "Magnet.Release.2026.1080p.WEB-DL" || first.GUID != "magnet-guid-1" {
		t.Errorf("title/guid = %q/%q", first.Title, first.GUID)
	}
	if first.Magnet != "magnet:?xt=urn:btih:ABCDEF0123456789ABCDEF0123456789ABCDEF01&dn=Magnet.Release" {
		t.Errorf("magnet = %q", first.Magnet)
	}
	if first.DownloadURL != "" {
		t.Errorf("download URL = %q, want empty when a magnet is present", first.DownloadURL)
	}
	if first.InfoHash != "abcdef0123456789abcdef0123456789abcdef01" {
		t.Errorf("infohash = %q, want normalized lowercase hex", first.InfoHash)
	}
	if first.DetailsURL != "https://tracker.example/details/magnet-release#comments" {
		t.Errorf("details URL = %q", first.DetailsURL)
	}
	if first.Size != 1234567890 || first.Seeders != 12 || first.Leechers != 3 {
		t.Errorf("size/seeds/leech = %d/%d/%d", first.Size, first.Seeders, first.Leechers)
	}
	if len(first.Categories) != 1 || first.Categories[0] != 2040 {
		t.Errorf("categories = %v, want [2040]", first.Categories)
	}
	if first.Published.Year() != 2024 {
		t.Errorf("published = %v", first.Published)
	}
	if first.IMDbID != "tt1234567" || first.TMDbID != "456" || first.TVDbID != "789" {
		t.Errorf("ids = %q/%q/%q", first.IMDbID, first.TMDbID, first.TVDbID)
	}
	if first.Attributes["category"] != "2040" || first.Attributes["seeders"] != "12" {
		t.Errorf("attributes = %v", first.Attributes)
	}

	second := items[1]
	if second.DownloadURL != server.URL+"/api?t=get&id=enclosure-guid-2" {
		t.Errorf("enclosure download URL = %q", second.DownloadURL)
	}
	if second.Magnet != "" {
		t.Errorf("second magnet = %q, want empty", second.Magnet)
	}
	if second.InfoHash != "0123456789abcdef0123456789abcdef01234567" {
		t.Errorf("second infohash = %q", second.InfoHash)
	}
	if second.Size != 987654321 {
		t.Errorf("second size = %d, want enclosure length", second.Size)
	}
	if len(second.Categories) != 1 || second.Categories[0] != 2000 {
		t.Errorf("second categories = %v", second.Categories)
	}

	third := items[2]
	if third.DownloadURL != server.URL+"/download/3.torrent" {
		t.Errorf("link download URL = %q", third.DownloadURL)
	}
	if third.Seeders != 42 || third.Leechers != 7 {
		t.Errorf("third seeds/leech = %d/%d", third.Seeders, third.Leechers)
	}
	if len(third.Categories) != 1 || third.Categories[0] != 5040 {
		t.Errorf("third categories = %v", third.Categories)
	}
}

func TestTorznabSearchModes(t *testing.T) {
	recorder := &queryRecorder{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder.add(r.URL.Query())
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = io.WriteString(w, `<rss version="2.0"><channel></channel></rss>`)
	}))
	defer server.Close()

	connector, err := NewTorznab(TorznabConfig{
		BaseURL:   server.URL,
		APIKey:    "secret-key",
		Transport: newTestTransport(),
	})
	if err != nil {
		t.Fatalf("NewTorznab: %v", err)
	}

	cases := []struct {
		name  string
		req   engine.SearchRequest
		check map[string]string
	}{
		{
			name: "search",
			req:  engine.SearchRequest{Mode: engine.ModeSearch, Query: "keywords"},
			check: map[string]string{
				"t": "search", "q": "keywords",
			},
		},
		{
			name: "tv",
			req: engine.SearchRequest{
				Mode: engine.ModeTV, Query: "show", Season: 4, Episode: 5,
				IMDbID: "tt100", TVDbID: "101",
			},
			check: map[string]string{
				"t": "tvsearch", "q": "show", "season": "4", "ep": "5",
				"imdbid": "tt100", "tvdbid": "101",
			},
		},
		{
			name: "movie",
			req: engine.SearchRequest{
				Mode: engine.ModeMovie, IMDbID: "tt200", TMDbID: "201",
			},
			check: map[string]string{
				"t": "movie", "imdbid": "tt200", "tmdbid": "201",
			},
		},
		{
			name: "music",
			req: engine.SearchRequest{
				Mode: engine.ModeMusic, Artist: "Artist", Album: "Album", RID: "1234",
			},
			check: map[string]string{
				"t": "music", "artist": "Artist", "album": "Album", "rid": "1234",
			},
		},
		{
			name: "book",
			req: engine.SearchRequest{
				Mode: engine.ModeBook, Author: "Author", Title: "Title",
			},
			check: map[string]string{
				"t": "book", "author": "Author", "title": "Title",
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
			if got := query.Get("extended"); got != "1" {
				t.Errorf("extended = %q, want 1", got)
			}
		})
	}

	if _, err := connector.Search(context.Background(), engine.SearchRequest{Mode: "bogus"}); err == nil {
		t.Error("invalid mode did not fail")
	}
}

func TestTorznabSearchErrorNeverLeaksAPIKey(t *testing.T) {
	const apiKey = "secret-key"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		_, _ = io.WriteString(w, `<error code="100" description="Invalid API key `+apiKey+`"/>`)
	}))
	defer server.Close()

	connector, err := NewTorznab(TorznabConfig{
		BaseURL:   server.URL,
		APIKey:    apiKey,
		Transport: newTestTransport(),
	})
	if err != nil {
		t.Fatalf("NewTorznab: %v", err)
	}
	_, err = connector.Search(context.Background(), engine.SearchRequest{Query: "test"})
	if err == nil {
		t.Fatal("Search succeeded, want API error")
	}
	if strings.Contains(err.Error(), apiKey) {
		t.Fatalf("error leaked API key: %v", err)
	}
	if !strings.Contains(err.Error(), "100") {
		t.Errorf("error = %v, want the Torznab error code", err)
	}
}

func TestTorznabCapsCache(t *testing.T) {
	fixture := readFixture(t, "torznab_caps.xml")
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

	connector, err := NewTorznab(TorznabConfig{
		BaseURL:   server.URL,
		APIKey:    "secret-key",
		Transport: newTestTransport(),
	})
	if err != nil {
		t.Fatalf("NewTorznab: %v", err)
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
		t.Errorf("caps calls = %d, want 1 (cache hit)", got)
	}
	if first.Limits.Max != second.Limits.Max || capsSupports(second, engine.ModeBook) {
		t.Errorf("cached caps = %+v", second)
	}

	connector.capsMu.Lock()
	connector.capsAt = time.Now().Add(-2 * torznabCapsTTL)
	connector.capsMu.Unlock()
	if _, err := connector.Caps(context.Background()); err != nil {
		t.Fatalf("Caps after expiry: %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("caps calls = %d, want 2 after expiry", got)
	}

	fail.Store(true)
	connector.capsMu.Lock()
	connector.capsAt = time.Now().Add(-2 * torznabCapsTTL)
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
}

func TestTorznabCapabilitiesUsesCaps(t *testing.T) {
	fixture := readFixture(t, "torznab_caps.xml")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(fixture)
	}))
	defer server.Close()

	connector, err := NewTorznab(TorznabConfig{
		BaseURL:    server.URL,
		APIKey:     "secret-key",
		Transport:  newTestTransport(),
		Categories: []int{111},
	})
	if err != nil {
		t.Fatalf("NewTorznab: %v", err)
	}

	before := connector.Capabilities()
	if before.Protocol != engine.ProtocolTorrent {
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
	if after.Supports(engine.ModeBook) {
		t.Error("caps said book was unavailable, but it is advertised")
	}
	if !after.Supports(engine.ModeMusic) || !after.SupportsParam(engine.ModeTV, engine.ParamSeason) {
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

func TestTorznabTestPhases(t *testing.T) {
	const apiKey = "secret-key"
	fixture := readFixture(t, "torznab_caps.xml")
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

	connector, err := NewTorznab(TorznabConfig{
		BaseURL:   server.URL,
		APIKey:    apiKey,
		Transport: newTestTransport(),
	})
	if err != nil {
		t.Fatalf("NewTorznab: %v", err)
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

func TestTorznabDownload(t *testing.T) {
	const apiKey = "secret-key"
	const torrentBody = "d4:infod4:name4:teste"

	mux := http.NewServeMux()
	mux.HandleFunc("/api", func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		if query.Get("t") != "get" || query.Get("id") != "abc123guid" || query.Get("apikey") != apiKey {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/x-bittorrent")
		_, _ = io.WriteString(w, torrentBody)
	})
	mux.HandleFunc("/direct.torrent", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("apikey") != apiKey {
			http.Error(w, "missing key", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/x-bittorrent")
		_, _ = io.WriteString(w, torrentBody)
	})
	mux.HandleFunc("/redirect", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "magnet:?xt=urn:btih:ABCDEF0123456789ABCDEF0123456789ABCDEF01")
		w.WriteHeader(http.StatusFound)
	})
	mux.HandleFunc("/missing", func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	connector, err := NewTorznab(TorznabConfig{
		BaseURL:   server.URL + "/api",
		APIKey:    apiKey,
		Transport: newTestTransport(),
	})
	if err != nil {
		t.Fatalf("NewTorznab: %v", err)
	}

	t.Run("magnet", func(t *testing.T) {
		payload, err := connector.Download(context.Background(), engine.Item{
			Title:  "Magnet",
			Magnet: "magnet:?xt=urn:btih:abcdef",
		})
		if err != nil {
			t.Fatalf("Download: %v", err)
		}
		if payload.Magnet != "magnet:?xt=urn:btih:abcdef" || len(payload.Body) != 0 {
			t.Fatalf("payload = %+v", payload)
		}
	})

	t.Run("magnet as download URL", func(t *testing.T) {
		payload, err := connector.Download(context.Background(), engine.Item{
			Title:       "Magnet URL",
			DownloadURL: "magnet:?xt=urn:btih:abcdef",
		})
		if err != nil {
			t.Fatalf("Download: %v", err)
		}
		if payload.Magnet != "magnet:?xt=urn:btih:abcdef" {
			t.Fatalf("payload = %+v", payload)
		}
	})

	t.Run("t get", func(t *testing.T) {
		payload, err := connector.Download(context.Background(), engine.Item{
			Title:       "Example/Release.Name",
			GUID:        "abc123guid",
			DownloadURL: server.URL + "/direct.torrent",
		})
		if err != nil {
			t.Fatalf("Download: %v", err)
		}
		if string(payload.Body) != torrentBody {
			t.Errorf("body = %q", payload.Body)
		}
		if payload.ContentType != "application/x-bittorrent" {
			t.Errorf("content type = %q", payload.ContentType)
		}
		if payload.Filename != "Example_Release.Name.torrent" {
			t.Errorf("filename = %q", payload.Filename)
		}
	})

	t.Run("url guid uses the release download link", func(t *testing.T) {
		// Prowlarr returns the tracker download page as <guid>. Sending it as
		// t=get&id= makes Prowlarr answer 400; the <link> is the real download.
		payload, err := connector.Download(context.Background(), engine.Item{
			Title:       "Prowlarr Release",
			GUID:        "https://tracker.example/download/123/Release.torrent",
			DownloadURL: server.URL + "/direct.torrent",
			Attributes: map[string]string{
				"guid": "https://tracker.example/download/123/Release.torrent",
			},
		})
		if err != nil {
			t.Fatalf("Download: %v", err)
		}
		if string(payload.Body) != torrentBody {
			t.Errorf("body = %q", payload.Body)
		}
	})

	t.Run("enclosure URL with API key", func(t *testing.T) {
		payload, err := connector.Download(context.Background(), engine.Item{
			Title:       "Enclosure",
			DownloadURL: server.URL + "/direct.torrent",
		})
		if err != nil {
			t.Fatalf("Download: %v", err)
		}
		if string(payload.Body) != torrentBody {
			t.Errorf("body = %q", payload.Body)
		}
	})

	t.Run("magnet redirect", func(t *testing.T) {
		payload, err := connector.Download(context.Background(), engine.Item{
			Title:       "Redirect",
			DownloadURL: server.URL + "/redirect",
		})
		if err != nil {
			t.Fatalf("Download: %v", err)
		}
		if payload.Magnet != "magnet:?xt=urn:btih:ABCDEF0123456789ABCDEF0123456789ABCDEF01" {
			t.Fatalf("payload = %+v", payload)
		}
	})

	t.Run("http error", func(t *testing.T) {
		_, err := connector.Download(context.Background(), engine.Item{
			Title:       "Missing",
			DownloadURL: server.URL + "/missing",
		})
		if err == nil {
			t.Fatal("Download succeeded, want HTTP error")
		}
		if strings.Contains(err.Error(), apiKey) {
			t.Fatalf("download error leaked API key: %v", err)
		}
		if !strings.Contains(err.Error(), strconv.Itoa(http.StatusNotFound)) {
			t.Errorf("error = %v, want HTTP status", err)
		}
	})
}

func TestNewTorznabValidation(t *testing.T) {
	transport := newTestTransport()
	if _, err := NewTorznab(TorznabConfig{Transport: transport}); err == nil {
		t.Error("missing base URL did not fail")
	}
	if _, err := NewTorznab(TorznabConfig{BaseURL: "ftp://indexer.example", Transport: transport}); err == nil {
		t.Error("non-HTTP base URL did not fail")
	}
}

func TestNormalizeInfoHash(t *testing.T) {
	cases := map[string]string{
		"": "",
		"ABCDEF0123456789ABCDEF0123456789ABCDEF01":            "abcdef0123456789abcdef0123456789abcdef01",
		" urn:btih:ABCDEF0123456789ABCDEF0123456789ABCDEF01 ": "abcdef0123456789abcdef0123456789abcdef01",
		"not-a-hash": "not-a-hash",
	}
	for input, want := range cases {
		if got := normalizeInfoHash(input); got != want {
			t.Errorf("normalizeInfoHash(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestTorznabGrabCarriesSeasonEpisodeAndToken(t *testing.T) {
	const feed = `<rss version="2.0" xmlns:torznab="http://torznab.com/schemas/2015/feed"><channel><item><title>Show S02E05 1080p</title><guid>grab-1</guid><link>https://tracker.example/details/1</link><enclosure url="https://tracker.example/dl/1.torrent" type="application/x-bittorrent"/></item></channel></rss>`
	var grabQuery url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("t") == "get" {
			grabQuery = r.URL.Query()
			_, _ = io.WriteString(w, "d4:infod4:name4:testee")
			return
		}
		_, _ = io.WriteString(w, feed)
	}))
	defer server.Close()
	connector, err := NewTorznab(TorznabConfig{
		BaseURL:   server.URL + "/api?k=KEEPME",
		Transport: newTestTransport(),
	})
	if err != nil {
		t.Fatal(err)
	}
	items, err := connector.Search(context.Background(), engine.SearchRequest{
		Mode: engine.ModeTV, Query: "Show", Season: 2, Episode: 5,
	})
	if err != nil || len(items) != 1 {
		t.Fatalf("Search = %d, %v", len(items), err)
	}
	if items[0].Attributes["season"] != "2" || items[0].Attributes["episode"] != "5" {
		t.Fatalf("search attributes = %v", items[0].Attributes)
	}
	if _, err := connector.Download(context.Background(), items[0]); err != nil {
		t.Fatalf("Download: %v", err)
	}
	if grabQuery.Get("season") != "2" || grabQuery.Get("ep") != "5" || grabQuery.Get("k") != "KEEPME" {
		t.Fatalf("grab query = %v", grabQuery)
	}
}
