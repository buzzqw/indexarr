package indexer

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/buzzqw/indexarr/internal/engine"
	"github.com/buzzqw/indexarr/internal/store"
	"github.com/buzzqw/indexarr/internal/textutil"
)

const (
	liveHashSlow   = "1111111111111111111111111111111111111111"
	liveHashTV     = "2222222222222222222222222222222222222222"
	liveHashCached = "3333333333333333333333333333333333333333"
	liveHashDetail = "4444444444444444444444444444444444444444"
)

// liveRow is one normalized row of the fake sites.
func liveRow(title, details, hash string) string {
	hashCell := ""
	if hash != "" {
		hashCell = `<td class="hash">` + hash + `</td>`
	}
	return `<tr><td class="name"><a href="` + details + `">` + title + `</a></td>` +
		`<td class="size">1.5 GB</td><td class="seeders">10</td><td class="leechers">1</td>` +
		hashCell + `</tr>`
}

func liveHTML(rows ...string) string {
	return `<html><body><table class="results"><tbody>` +
		strings.Join(rows, "") + `</tbody></table></body></html>`
}

// liveDefinition builds a search definition bound to one fake server path.
// caps is the YAML block under capabilities (already indented); download is an
// optional extra block.
func liveDefinition(id, baseURL, caps, download string) string {
	return fmt.Sprintf(`schema: 1
meta:
  id: %s
  name: %s
  type: public
  protocol: torrent
  urls: ["%s/"]
capabilities:
%s
search:
  request:
    get: /search
    params:
      q: "{{ query.keywords }}"
  response:
    type: html
    rows: "table.results tr"
    fields:
      title: { selector: "td.name a", filters: [trim] }
      details: { selector: "td.name a", attr: href, type: url }
      infohash: { selector: "td.hash" }
      size: { selector: "td.size", type: size }
      seeders: { selector: "td.seeders", type: int }
      leechers: { selector: "td.leechers", type: int }
    required: [title, details]
%s`, id, id, baseURL, caps, download)
}

func TestSearchLiveFanOut(t *testing.T) {
	var tvQuery atomic.Value
	var detailCalls atomic.Int64

	mux := http.NewServeMux()
	mux.HandleFunc("/slow/search", func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
		_, _ = w.Write([]byte(liveHTML(liveRow("Slow Show S02E05", "/details/1", liveHashSlow))))
	})
	mux.HandleFunc("/error/search", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	mux.HandleFunc("/tv/search", func(w http.ResponseWriter, r *http.Request) {
		tvQuery.Store(r.URL.Query().Get("q"))
		_, _ = w.Write([]byte(liveHTML(liveRow("TV Show S02E05", "/details/2", liveHashTV))))
	})
	mux.HandleFunc("/feed/search", func(w http.ResponseWriter, r *http.Request) {
		t.Error("the feed-only indexer must not be queried")
	})
	mux.HandleFunc("/cached/search", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(liveHTML(liveRow("Cached Live Show S02E05", "/details/3", "5555555555555555555555555555555555555555"))))
	})
	mux.HandleFunc("/details/search", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(liveHTML(liveRow("Details Show S02E05", "/details/1", ""))))
	})
	mux.HandleFunc("/details/details/", func(w http.ResponseWriter, r *http.Request) {
		detailCalls.Add(1)
		_, _ = w.Write([]byte(liveHTML()))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	st := newTestStore(t)
	v := newTestVault(t, 3)
	manager := newTestManager(t, st, v)

	searchCaps := "  search: [q]"
	feedCaps := ""

	seedDefinition(t, st, "live-slow", "", liveDefinition("live-slow", server.URL+"/slow", searchCaps, ""))
	seedDefinition(t, st, "live-error", "", liveDefinition("live-error", server.URL+"/error", searchCaps, ""))
	seedDefinition(t, st, "live-tv", "", liveDefinition("live-tv", server.URL+"/tv", searchCaps, ""))
	seedDefinition(t, st, "live-feed", "", liveDefinition("live-feed", server.URL+"/feed", feedCaps, ""))
	seedDefinition(t, st, "live-cached", "", liveDefinition("live-cached", server.URL+"/cached", searchCaps, ""))
	seedDefinition(t, st, "live-details", "", liveDefinition("live-details", server.URL+"/details", searchCaps,
		`download:
  resolve_on: search
  from: details
  extract:
    - magnet: { selector: 'a[href^="magnet:"]', attr: href }
`))

	slowID := createIndexer(t, st, "live-slow", "live-slow", "Slow", "torrent", fmt.Sprintf(`{"url":%q}`, server.URL+"/slow"))
	errorID := createIndexer(t, st, "live-error", "live-error", "Error", "torrent", fmt.Sprintf(`{"url":%q}`, server.URL+"/error"))
	tvID := createIndexer(t, st, "live-tv", "live-tv", "TV", "torrent", fmt.Sprintf(`{"url":%q}`, server.URL+"/tv"))
	feedID := createIndexer(t, st, "live-feed", "live-feed", "Feed only", "torrent", fmt.Sprintf(`{"url":%q}`, server.URL+"/feed"))
	cachedID := createIndexer(t, st, "live-cached", "live-cached", "Cached", "torrent", fmt.Sprintf(`{"url":%q}`, server.URL+"/cached"))
	detailsID := createIndexer(t, st, "live-details", "live-details", "Details", "torrent", fmt.Sprintf(`{"url":%q}`, server.URL+"/details"))

	// One matching result is already cached for the "cached" indexer. The
	// fan-out must not replace it: the live answer is merged with it.
	now := time.Now()
	cached := &store.Release{
		DedupKey: liveHashCached, InfoHash: liveHashCached,
		Title: "Cached Show S02E05", SearchTitle: textutil.NormalizeTitle("Cached Show S02E05"),
		Category: "5040", Magnet: "magnet:?xt=urn:btih:" + liveHashCached,
		FirstSeenAt: now, LastSeenAt: now,
	}
	if _, err := st.UpsertRelease(context.Background(), cached); err != nil {
		t.Fatalf("cache release: %v", err)
	}
	if err := st.RecordReleaseSource(context.Background(), store.ReleaseSource{
		ReleaseID: cached.ID, IndexerID: cachedID, FirstSeenAt: now, LastSeenAt: now,
	}); err != nil {
		t.Fatalf("cache provenance: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result, err := manager.SearchLive(ctx, LiveQuery{
		Mode: "tvsearch", Query: "Show", Season: 2, Episode: 5, Limit: 50,
	})
	if err != nil {
		t.Fatalf("SearchLive: %v", err)
	}

	byID := make(map[int64]LiveIndexerResult, len(result.Indexers))
	for _, entry := range result.Indexers {
		byID[entry.IndexerID] = entry
	}
	if entry, ok := byID[slowID]; !ok || !entry.TimedOut {
		t.Errorf("slow outcome = %+v, want TimedOut", entry)
	}
	if entry := byID[errorID]; entry.Err == "" {
		t.Errorf("error outcome = %+v, want an error", entry)
	}
	if entry := byID[tvID]; entry.Err != "" || entry.Results != 1 {
		t.Errorf("tv outcome = %+v, want one stored result", entry)
	}
	if q, _ := tvQuery.Load().(string); q != "Show S02E05" {
		t.Errorf("tv fallback query = %q, want %q", q, "Show S02E05")
	}
	if entry := byID[feedID]; !entry.Skipped || entry.Err == "" {
		t.Errorf("feed-only outcome = %+v, want skipped", entry)
	}
	if entry := byID[cachedID]; entry.Err != "" {
		t.Errorf("cached outcome = %+v", entry)
	}
	if entry := byID[detailsID]; entry.Err != "" {
		t.Errorf("details outcome = %+v", entry)
	}
	if detailCalls.Load() != 0 {
		t.Errorf("live search opened %d details pages, want 0", detailCalls.Load())
	}

	// The cached result survived, and the live results were added to it.
	releases, total, err := st.SearchReleases(context.Background(), store.Filter{Query: "Show", Limit: 50})
	if err != nil {
		t.Fatalf("SearchReleases: %v", err)
	}
	if total < 3 {
		t.Fatalf("cached total = %d, want at least 3 (%d released)", total, len(releases))
	}
	titles := make(map[string]bool)
	for _, release := range releases {
		titles[release.Title] = true
	}
	if !titles["Cached Show S02E05"] {
		t.Error("the cached result was replaced by the live answer")
	}
	if !titles["TV Show S02E05"] {
		t.Error("the live TV result is missing")
	}

	// The slow indexer continues in the background and caches its result.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		_, total, err := st.SearchReleases(context.Background(), store.Filter{Query: "Slow", Limit: 10})
		if err == nil && total > 0 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if _, total, err := st.SearchReleases(context.Background(), store.Filter{Query: "Slow", Limit: 10}); err != nil || total == 0 {
		t.Fatalf("slow result was not cached in the background (total %d, err %v)", total, err)
	}

	// The details indexer stored an opaque download reference instead of
	// fetching the details page.
	detailReleases, _, err := st.SearchReleases(context.Background(), store.Filter{Query: "Details", Limit: 10})
	if err != nil || len(detailReleases) == 0 {
		t.Fatalf("details release = %d, err %v", len(detailReleases), err)
	}
	sources, err := st.ListReleaseSources(context.Background(), detailReleases[0].ID)
	if err != nil {
		t.Fatalf("ListReleaseSources: %v", err)
	}
	foundRef := false
	for _, source := range sources {
		if source.IndexerID == detailsID && strings.TrimSpace(source.DownloadRef) != "" {
			foundRef = true
			break
		}
	}
	if !foundRef {
		t.Error("the details item has no sealed download reference")
	}
}

func TestPlanLiveSearchFallbacks(t *testing.T) {
	searchOnly := engine.Capabilities{
		Modes: []engine.Mode{engine.ModeSearch},
		Params: map[engine.Mode][]string{
			engine.ModeSearch: {engine.ParamQuery},
		},
	}
	tvCaps := engine.Capabilities{
		Modes: []engine.Mode{engine.ModeSearch, engine.ModeTV},
		Params: map[engine.Mode][]string{
			engine.ModeSearch: {engine.ParamQuery},
			engine.ModeTV:     {engine.ParamQuery, engine.ParamSeason, engine.ParamEpisode},
		},
	}
	idCaps := engine.Capabilities{
		Modes:  []engine.Mode{engine.ModeMovie},
		Params: map[engine.Mode][]string{engine.ModeMovie: {engine.ParamIMDbID}},
	}

	tvPlan, ok, reason := planLiveSearch(searchOnly, LiveQuery{
		Mode: "tvsearch", Query: "Show", Season: 2, Episode: 5,
	})
	if !ok || !tvPlan.fallback || tvPlan.mode != engine.ModeSearch || tvPlan.request.Query != "Show S02E05" {
		t.Errorf("tv fallback = %+v ok=%v reason=%q", tvPlan, ok, reason)
	}
	seasonPlan, ok, _ := planLiveSearch(searchOnly, LiveQuery{Mode: "tvsearch", Query: "Show", Season: 3})
	if !ok || seasonPlan.request.Query != "Show S03" {
		t.Errorf("season fallback = %+v ok=%v", seasonPlan, ok)
	}
	moviePlan, ok, _ := planLiveSearch(searchOnly, LiveQuery{Mode: "movie", Query: "Dune", Year: 2021})
	if !ok || moviePlan.request.Query != "Dune 2021" {
		t.Errorf("movie fallback = %+v ok=%v", moviePlan, ok)
	}
	if _, ok, _ := planLiveSearch(searchOnly, LiveQuery{Mode: "movie", IMDbID: "tt123"}); ok {
		t.Error("an ID-only movie request must be skipped on a search-only site")
	}
	if _, ok, _ := planLiveSearch(idCaps, LiveQuery{Mode: "movie", IMDbID: "tt123"}); !ok {
		t.Error("an ID-only movie request must be accepted when the mode declares imdbid")
	}
	if plan, ok, _ := planLiveSearch(tvCaps, LiveQuery{Mode: "tvsearch", Query: "Show", Season: 2, Episode: 5}); !ok ||
		plan.mode != engine.ModeTV || plan.fallback {
		t.Errorf("declared tv-search = %+v ok=%v", plan, ok)
	}
	if _, ok, reason := planLiveSearch(engine.Capabilities{LocalOnly: true}, LiveQuery{Mode: "search", Query: "Show"}); ok || reason == "" {
		t.Errorf("feed-only indexer joined the live fan-out: ok=%v reason=%q", ok, reason)
	}
}

func TestSearchLiveResponseCache(t *testing.T) {
	var hits atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(liveHTML(liveRow("Cached Search Show S01E01", "/details/1", liveHashCached))))
	}))
	defer server.Close()

	st := newTestStore(t)
	manager := newTestManager(t, st, newTestVault(t, 3))
	seedDefinition(t, st, "cache", "", liveDefinition("cache", server.URL, "  search: [q]", ""))
	createIndexer(t, st, "cache", "cache", "Cache", "torrent", fmt.Sprintf(`{"url":%q}`, server.URL))

	ctx := context.Background()
	req := LiveQuery{Mode: "search", Query: "Show", Limit: 50}
	first, err := manager.SearchLive(ctx, req)
	if err != nil || first.Stored == 0 {
		t.Fatalf("first search = %+v, %v", first, err)
	}
	second, err := manager.SearchLive(ctx, req)
	if err != nil || second.Stored == 0 {
		t.Fatalf("second search = %+v, %v", second, err)
	}
	if hits.Load() != 1 {
		t.Fatalf("HTTP hits after warm request = %d, want 1", hits.Load())
	}

	settings, _ := st.LoadSettings(ctx)
	settings.TorznabLiveCache = 0
	if err := st.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.SearchLive(ctx, req); err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 2 {
		t.Fatalf("HTTP hits with cache disabled = %d, want 2", hits.Load())
	}
}

func TestLiveFirstPageOnlyDetectsUploaderLists(t *testing.T) {
	cases := map[string]bool{
		`{"uploader":"MIRCrewRS"}`:                          true,
		`{"filter":"u=EZTV"}`:                               true,
		`{"filter":"c=Movies","filter-extra":"tg=Italian"}`: false,
		`{"query":"MIRCrew","filter":"c=TV"}`:               false,
		``:                                                  false,
	}
	for config, want := range cases {
		if got := liveFirstPageOnly(config); got != want {
			t.Errorf("liveFirstPageOnly(%s) = %v, want %v", config, got, want)
		}
	}
}

func TestZeroIndexersUsesRecentSearchHistory(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	manager := newTestManager(t, st, newTestVault(t, 3))
	now := time.Now()
	for _, row := range []store.AppSearch{
		{
			App: "sonarr", Mode: "search", Query: "Severance", TotalResults: 4, CreatedAt: now.Add(-time.Hour),
			Live: []store.AppSearchLive{
				{IndexerID: 1, Results: 0},
				{IndexerID: 2, Results: 3},
				{IndexerID: 3, Results: 0, TimedOut: true},
				{IndexerID: 4, Results: 0, Skipped: true},
			},
		},
		{
			App: "sonarr", Mode: "search", Query: "Severance", CreatedAt: now.Add(-7 * time.Hour),
			Live: []store.AppSearchLive{{IndexerID: 5, Results: 0}},
		},
		{
			App: "sonarr", Mode: "search", Query: "Severance", TotalResults: 0, CreatedAt: now.Add(-time.Hour),
			Live: []store.AppSearchLive{{IndexerID: 6, TimedOut: true}},
		},
	} {
		if _, err := st.RecordAppSearch(ctx, row); err != nil {
			t.Fatal(err)
		}
	}
	got := manager.zeroIndexers(ctx, LiveQuery{Mode: "search", Query: "Severance"})
	if !got[1] || got[2] || got[3] || got[4] || got[5] || !got[6] {
		t.Fatalf("zero indexers = %v, want 1 and 6", got)
	}
}
