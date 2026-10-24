package torznabapi

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/buzzqw/indexarr/internal/engine"
	"github.com/buzzqw/indexarr/internal/indexer"
	"github.com/buzzqw/indexarr/internal/store"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newAPI(t *testing.T) (*Server, *store.Store) {
	t.Helper()
	st, err := store.Open(t.TempDir() + "/api.db")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	settings, err := st.LoadSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	settings.APIKey = "secret"
	if err := st.SaveSettings(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	return New(st, testLogger(), "test"), st
}

func insertRelease(t *testing.T, st *store.Store, title, category, infohash, magnet string, torrent []byte) int64 {
	t.Helper()
	if magnet == "" && len(torrent) == 0 {
		magnet = "magnet:?xt=urn:btih:" + infohash
	}
	release := store.Release{
		DedupKey:    infohash + "-" + title,
		InfoHash:    infohash,
		Title:       title,
		SearchTitle: store.NormalizeSearch(title),
		Size:        1234,
		Seeders:     7,
		Leechers:    2,
		Category:    category,
		PublishedAt: time.Now().Add(-time.Hour),
		Magnet:      magnet,
		Torrent:     torrent,
		FirstSeenAt: time.Now().Add(-time.Hour),
		LastSeenAt:  time.Now(),
	}
	if _, err := st.UpsertRelease(context.Background(), &release); err != nil {
		t.Fatalf("UpsertRelease: %v", err)
	}
	return release.ID
}

func do(t *testing.T, server *Server, target string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, target, nil)
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	return recorder
}

func TestCaps(t *testing.T) {
	api, _ := newAPI(t)
	recorder := do(t, api, "/api?t=caps")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	body := recorder.Body.String()
	for _, want := range []string{`<caps>`, `<category id="5000"`, `<subcat id="5040"`, `<tv-search available="yes"`, `<limits max="100"`} {
		if !strings.Contains(body, want) {
			t.Errorf("caps missing %q", want)
		}
	}
	if contentType := recorder.Header().Get("Content-Type"); !strings.Contains(contentType, "xml") {
		t.Errorf("content type = %q", contentType)
	}
}

func TestSearchAndDownload(t *testing.T) {
	api, st := newAPI(t)
	torrent := []byte("d4:infod4:name4:testee")
	hash := "0123456789abcdef0123456789abcdef01234567"
	magnet := "magnet:?xt=urn:btih:" + hash + "&dn=Show"
	id := insertRelease(t, st, "Show S01E02 1080p", "5040", hash, magnet, torrent)

	recorder := do(t, api, "/api?t=search&apikey=secret&q=Show")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	for _, want := range []string{
		`xmlns:torznab="http://torznab.com/schemas/2015/feed"`,
		`<newznab:response offset="0" total="1"/>`,
		`<torznab:attr name="seeders" value="7"/>`,
		`<torznab:attr name="leechers" value="2"/>`,
		`<torznab:attr name="infohash" value="` + hash + `"`,
		`http://example.com/dl/` + itoa(id) + `.torrent?apikey=secret`,
		`<title>Show S01E02 1080p</title>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q\n%s", want, body)
		}
	}

	download := do(t, api, "/dl/"+itoa(id)+".torrent?apikey=secret")
	if download.Code != http.StatusOK {
		t.Fatalf("download status = %d", download.Code)
	}
	if download.Body.String() != string(torrent) {
		t.Error("download body differs")
	}
	if contentType := download.Header().Get("Content-Type"); contentType != "application/x-bittorrent" {
		t.Errorf("download content type = %q", contentType)
	}

	byGet := do(t, api, "/api?t=get&id="+itoa(id)+"&apikey=secret")
	if byGet.Code != http.StatusOK || byGet.Body.String() != string(torrent) {
		t.Errorf("t=get failed: %d", byGet.Code)
	}
	// Some clients call t=get with the item GUID (the infohash here) instead
	// of indexarr's numeric row id.
	byGUID := do(t, api, "/api?t=get&id="+hash+"&apikey=secret")
	if byGUID.Code != http.StatusOK || byGUID.Body.String() != string(torrent) {
		t.Errorf("t=get by guid failed: %d", byGUID.Code)
	}
	if missing := do(t, api, "/api?t=get&id=does-not-exist&apikey=secret"); missing.Code != http.StatusNotFound {
		t.Errorf("t=get missing id status = %d", missing.Code)
	}
}

func TestAuthentication(t *testing.T) {
	api, st := newAPI(t)
	id := insertRelease(t, st, "Show", "5000", "abcdefabcdefabcdefabcdefabcdefabcdefabcd", "magnet:?xt=urn:btih:abcdefabcdefabcdefabcdefabcdefabcdefabcd", nil)

	if recorder := do(t, api, "/api?t=search&apikey=wrong"); recorder.Code != http.StatusUnauthorized {
		t.Errorf("wrong key status = %d", recorder.Code)
	}
	if recorder := do(t, api, "/api?t=search"); recorder.Code != http.StatusUnauthorized {
		t.Errorf("missing key status = %d", recorder.Code)
	}
	if recorder := do(t, api, "/dl/"+itoa(id)+".torrent"); recorder.Code != http.StatusUnauthorized {
		t.Errorf("download without key status = %d", recorder.Code)
	}
	request := httptest.NewRequest(http.MethodGet, "/api?t=search", nil)
	request.Header.Set("X-Api-Key", "secret")
	recorder := httptest.NewRecorder()
	api.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Errorf("header key status = %d", recorder.Code)
	}
}

func TestAdditionalAPIKeyAndRateLimit(t *testing.T) {
	api, st := newAPI(t)
	ctx := context.Background()
	key, err := st.AddAPIKey(ctx, "Sonarr")
	if err != nil {
		t.Fatal(err)
	}
	hash := "9999999999999999999999999999999999999999"
	id := insertRelease(t, st, "Key Show 1080p", "5040", hash, "magnet:?xt=urn:btih:"+hash, []byte("d4:infod4:name4:testee"))

	recorder := do(t, api, "/api?t=search&apikey="+key.Key+"&q=Key")
	if recorder.Code != http.StatusOK {
		t.Fatalf("additional key status = %d: %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "apikey="+key.Key) {
		t.Errorf("torrent link does not use the request key:\n%s", body)
	}
	if !strings.Contains(body, "/dl/"+itoa(id)+".torrent") {
		t.Error("release missing")
	}
	download := do(t, api, "/dl/"+itoa(id)+".torrent?apikey="+key.Key)
	if download.Code != http.StatusOK {
		t.Errorf("download with additional key = %d", download.Code)
	}

	settings, _ := st.LoadSettings(ctx)
	settings.APIRateLimit = 1
	if err := st.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	second, err := st.AddAPIKey(ctx, "limited")
	if err != nil {
		t.Fatal(err)
	}
	first := do(t, api, "/api?t=search&apikey="+second.Key)
	if first.Code != http.StatusOK {
		t.Fatalf("first limited request = %d", first.Code)
	}
	limited := do(t, api, "/api?t=search&apikey="+second.Key)
	if limited.Code != http.StatusTooManyRequests {
		t.Errorf("second limited request = %d, want 429", limited.Code)
	}
	if limited.Header().Get("Retry-After") == "" {
		t.Error("Retry-After missing")
	}
}

func TestLiveSearchRunsOnEveryQuery(t *testing.T) {
	api, st := newAPI(t)
	ctx := context.Background()
	settings, _ := st.LoadSettings(ctx)
	settings.TorznabLiveSearch = true
	settings.TorznabLiveTimeout = 5
	if err := st.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	calls := 0
	var modes []string
	api.SetLiveSearch(func(ctx context.Context, req indexer.LiveQuery) (indexer.LiveResult, error) {
		calls++
		modes = append(modes, req.Mode)
		hash := "7777777777777777777777777777777777777777"
		insertRelease(t, st, "Live Result 1080p", "5040", hash, "magnet:?xt=urn:btih:"+hash, nil)
		return indexer.LiveResult{
			Stored: 1,
			Indexers: []indexer.LiveIndexerResult{{
				IndexerID: 1, Name: "Fake", Mode: req.Mode, Results: 1,
			}},
		}, nil
	})
	first := do(t, api, "/api?t=search&apikey=secret&q=Live")
	if first.Code != http.StatusOK || !strings.Contains(first.Body.String(), "Live Result 1080p") {
		t.Fatalf("live response = %d: %s", first.Code, first.Body.String())
	}
	if calls != 1 {
		t.Errorf("live callback calls = %d", calls)
	}
	// The cache now already has the result, and the live fan-out still runs:
	// the cache is merged with the live answer, not used as a short-circuit.
	second := do(t, api, "/api?t=search&apikey=secret&q=Live")
	if second.Code != http.StatusOK || !strings.Contains(second.Body.String(), "Live Result 1080p") {
		t.Fatalf("cached response = %d", second.Code)
	}
	if calls != 2 {
		t.Errorf("live callback calls on a cache hit = %d, want 2", calls)
	}

	settings.TorznabLiveSearch = false
	if err := st.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	miss := do(t, api, "/api?t=search&apikey=secret&q=nothing-here")
	if miss.Code != http.StatusOK || strings.Contains(miss.Body.String(), "Live Result") {
		t.Errorf("disabled live search response = %d", miss.Code)
	}
	if calls != 2 {
		t.Errorf("disabled live search called the callback: %d", calls)
	}
}

func TestTVSearchFilterAndPagination(t *testing.T) {
	api, st := newAPI(t)
	insertRelease(t, st, "Show S01E02 1080p", "5040", "1111111111111111111111111111111111111111", "", nil)
	insertRelease(t, st, "Show S02E01 720p", "5030", "2222222222222222222222222222222222222222", "", nil)
	insertRelease(t, st, "Movie 2024 1080p", "2040", "3333333333333333333333333333333333333333", "", nil)

	season := do(t, api, "/api?t=tvsearch&apikey=secret&q=Show&season=1&ep=2")
	if season.Code != http.StatusOK {
		t.Fatalf("status = %d", season.Code)
	}
	if !strings.Contains(season.Body.String(), `total="1"`) {
		t.Errorf("season filter body:\n%s", season.Body.String())
	}

	limited := do(t, api, "/api?t=search&apikey=secret&limit=1")
	body := limited.Body.String()
	if !strings.Contains(body, `total="3"`) {
		t.Errorf("pagination total:\n%s", body)
	}
	if got := strings.Count(body, "<item>"); got != 1 {
		t.Errorf("items = %d, want 1", got)
	}

	offset := do(t, api, "/api?t=search&apikey=secret&limit=1&offset=2")
	if !strings.Contains(offset.Body.String(), `offset="2"`) {
		t.Errorf("offset header missing:\n%s", offset.Body.String())
	}
}

func TestPublicURLAndCategoryFilter(t *testing.T) {
	api, st := newAPI(t)
	insertRelease(t, st, "Show 1080p", "5040", "4444444444444444444444444444444444444444", "", []byte("d4:infod4:name4:testee"))
	insertRelease(t, st, "Movie 1080p", "2040", "5555555555555555555555555555555555555555", "", nil)

	settings, _ := st.LoadSettings(context.Background())
	settings.PublicURL = "https://idx.example/"
	if err := st.SaveSettings(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	recorder := do(t, api, "/api?t=search&apikey=secret&cat=5000")
	body := recorder.Body.String()
	if !strings.Contains(body, `https://idx.example/dl/`) {
		t.Errorf("public URL not used:\n%s", body)
	}
	if !strings.Contains(body, `total="1"`) {
		t.Errorf("category filter:\n%s", body)
	}
	if strings.Contains(body, "Movie 1080p") {
		t.Error("the movie should be filtered out")
	}
}

func TestTorrentFilename(t *testing.T) {
	if got := TorrentFilename(`Some/Movie: 2024?`); got != "Some_Movie_ 2024_.torrent" {
		t.Errorf("TorrentFilename = %q", got)
	}
}

func itoa(value int64) string {
	return strconv.FormatInt(value, 10)
}

func TestTorznabAliasAndFunctions(t *testing.T) {
	api, st := newAPI(t)
	hash := "abcdefabcdefabcdefabcdefabcdefabcdefabcd"
	insertRelease(t, st, "Show S01E02 1080p", "5040", hash, "", []byte("d4:infod4:name4:testee"))

	for _, target := range []string{
		"/torznab/api?t=search&apikey=secret&q=Show",
		"/torznab/api?t=tvsearch&apikey=secret&q=Show&season=1&ep=2",
		"/torznab/api?t=movie&apikey=secret&q=Show",
	} {
		recorder := do(t, api, target)
		if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "Show S01E02 1080p") {
			t.Errorf("%s = %d\n%s", target, recorder.Code, recorder.Body.String())
		}
	}
	if recorder := do(t, api, "/torznab/api?t=caps"); recorder.Code != http.StatusOK ||
		!strings.Contains(recorder.Body.String(), "<caps>") {
		t.Errorf("alias caps = %d", recorder.Code)
	}
	request := httptest.NewRequest(http.MethodHead, "/dl/1.torrent?apikey=secret", nil)
	recorder := httptest.NewRecorder()
	api.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || recorder.Body.Len() != 0 {
		t.Errorf("HEAD download = %d, body %d", recorder.Code, recorder.Body.Len())
	}
}

func TestRSSRequestsNeverSynthesizeAQuery(t *testing.T) {
	api, st := newAPI(t)
	ctx := context.Background()
	settings, _ := st.LoadSettings(ctx)
	settings.TorznabLiveSearch = true
	if err := st.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	calls := 0
	api.SetLiveSearch(func(ctx context.Context, req indexer.LiveQuery) (indexer.LiveResult, error) {
		calls++
		if req.Query == "test" {
			t.Errorf("synthetic query %q reached the fan-out", req.Query)
		}
		return indexer.LiveResult{}, nil
	})
	for _, path := range []string{
		"/api?t=search&apikey=secret",
		"/api?t=tvsearch&apikey=secret&cat=5000",
	} {
		rec := do(t, api, path)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s = %d", path, rec.Code)
		}
	}
	if calls != 0 {
		t.Errorf("RSS requests called the live fan-out %d times, want 0", calls)
	}
	do(t, api, "/api?t=search&apikey=secret&q=real")
	if calls != 1 {
		t.Errorf("query search calls = %d, want 1", calls)
	}
}

func TestTorznabCapabilitiesAdvertiseTVAndMovieForPlainSearch(t *testing.T) {
	caps := torznabCapabilities(engine.Capabilities{
		Modes:  []engine.Mode{engine.ModeSearch},
		Params: map[engine.Mode][]string{engine.ModeSearch: {engine.ParamQuery}},
	})
	for _, mode := range []engine.Mode{engine.ModeTV, engine.ModeMovie} {
		if !caps.Supports(mode) {
			t.Errorf("%s not advertised", mode)
		}
	}
	if got := caps.Params[engine.ModeTV]; len(got) != 3 {
		t.Errorf("tv params = %v, want q,season,ep", got)
	}
	if got := caps.Params[engine.ModeMovie]; len(got) != 1 || got[0] != engine.ParamQuery {
		t.Errorf("movie params = %v, want [q]", got)
	}
	// A site with no plain search must not gain TV/movie modes.
	caps = torznabCapabilities(engine.Capabilities{Modes: []engine.Mode{engine.ModeTV},
		Params: map[engine.Mode][]string{engine.ModeTV: {engine.ParamSeason}}})
	if caps.Supports(engine.ModeMovie) {
		t.Error("movie mode invented for a site without a plain search")
	}
}

// TestPerSiteRSSIgnoresLatestFilter: Servarr validates a per-site indexer with
// an empty tvsearch/movie request. Search-only sites have no latest feed but do
// have cached releases, and must still answer that per-site request (Blocco C).
func TestPerSiteRSSIgnoresLatestFilter(t *testing.T) {
	api, st := newAPI(t)
	hash := "7777777777777777777777777777777777777777"
	releaseID := insertRelease(t, st, "Per Site Show S01E01 1080p", "5040", hash,
		"magnet:?xt=urn:btih:"+hash, nil)
	indexerID := provenanceIndexer(t, st, "perse", "Per Site")
	if err := st.RecordReleaseSource(context.Background(), store.ReleaseSource{
		ReleaseID: releaseID, IndexerID: indexerID,
		FirstSeenAt: time.Now(), LastSeenAt: time.Now(),
	}); err != nil {
		t.Fatalf("RecordReleaseSource: %v", err)
	}
	api.SetLatestIndexers(func(context.Context) ([]int64, error) { return nil, nil })

	aggregate := do(t, api, "/api?t=tvsearch&apikey=secret&cat=5000").Body.String()
	if strings.Contains(aggregate, "Per Site Show") {
		t.Errorf("aggregate RSS returned a search-only site:\n%s", aggregate)
	}
	perSite := do(t, api, "/torznab/prov-indexer-perse/api?t=tvsearch&apikey=secret&cat=5000").Body.String()
	if !strings.Contains(perSite, "Per Site Show") {
		t.Errorf("per-site RSS returned no cached releases:\n%s", perSite)
	}
}

func TestCapsUseRealIndexerCapabilities(t *testing.T) {
	api, st := newAPI(t)
	ctx := context.Background()
	if err := st.UpsertDefinition(ctx, store.Definition{
		ID: "fake", SchemaVersion: 1, Family: "html", State: "verificata",
		Origin: "local", Author: "test", Body: "schema: 1", Hash: "hash",
	}); err != nil {
		t.Fatal(err)
	}
	id, err := st.CreateIndexer(ctx, store.Indexer{
		DefinitionID: "fake", Slug: "tracker", Name: "Tracker", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	api.SetCapabilities(func(context.Context, []int64) (map[int64]engine.Capabilities, error) {
		return map[int64]engine.Capabilities{
			id: {
				Modes: []engine.Mode{engine.ModeSearch, engine.ModeTV},
				Params: map[engine.Mode][]string{
					engine.ModeSearch: {engine.ParamQuery},
					engine.ModeTV:     {engine.ParamQuery, engine.ParamSeason, engine.ParamEpisode},
				},
				Categories: []int{5000, 5040, 2000},
			},
		}, nil
	})

	check := func(path string) {
		body := do(t, api, path).Body.String()
		for _, want := range []string{
			`<search available="yes" supportedParams="q"/>`,
			`<tv-search available="yes" supportedParams="q,season,ep"/>`,
			// A plain keyword search also answers movie queries, so Servarr
			// can build its RSS request for search-only sites.
			`<movie-search available="yes" supportedParams="q"/>`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("%s missing %q\n%s", path, want, body)
			}
		}
		if strings.Contains(body, `imdbid`) {
			t.Errorf("%s still advertises imdbid", path)
		}
	}
	check("/api?t=caps")
	check("/api/v2.0/indexers/tracker/results/torznab/api?t=caps")
}

func TestSanitizedParamsRemovesCredentials(t *testing.T) {
	r := httptest.NewRequest("GET", "/api?t=tvsearch&q=Show&season=1&ep=2&apikey=SECRET&api_key=SECRET2", nil)
	got := sanitizedParams(r)
	for _, want := range []string{"t=tvsearch", "q=Show", "season=1", "ep=2"} {
		if !strings.Contains(got, want) {
			t.Errorf("params %q missing %q", got, want)
		}
	}
	if strings.Contains(got, "SECRET") {
		t.Fatalf("credentials leaked into params: %q", got)
	}
}

func TestIndexersFunctionIsJackettCompatible(t *testing.T) {
	api, st := newAPI(t)
	ctx := context.Background()
	if err := st.UpsertDefinition(ctx, store.Definition{
		ID: "fake-indexers", SchemaVersion: 1, Family: "html", State: "verificata",
		Origin: "local", Author: "test", Body: "schema: 1", Hash: "hash",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateIndexer(ctx, store.Indexer{
		DefinitionID: "fake-indexers", Slug: "tracker", Name: "Tracker",
		Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	body := do(t, api, "/api?t=indexers&configured=true&apikey=secret").Body.String()
	for _, want := range []string{
		`<indexer id="tracker" configured="true">`,
		`<title>Tracker</title>`,
		`/api/v2.0/indexers/tracker/results/torznab/api`,
		`<caps><searching>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("t=indexers missing %q\n%s", want, body)
		}
	}
	if strings.Contains(body, "secret") {
		t.Fatalf("indexer list leaked the API key")
	}
}
