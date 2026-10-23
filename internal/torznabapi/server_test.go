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
