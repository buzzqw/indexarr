package indexer

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/buzzqw/indexarr/internal/store"
)

// warmLiveStats gives one indexer a history of slow, empty answers so the
// early-finish rule can classify it.
func warmLiveStats(m *Manager, fastID, slowID int64) {
	for i := 0; i < liveStatMinSamples; i++ {
		m.observeLiveStat(LiveIndexerResult{IndexerID: fastID, Duration: 100 * time.Millisecond, Results: 2})
		m.observeLiveStat(LiveIndexerResult{IndexerID: slowID, Duration: 4 * time.Second})
	}
}

// warmProductiveStats gives a late indexer a history of useful answers, so it
// must never be treated as a site that only times out.
func warmProductiveStats(m *Manager, indexerID int64) {
	for i := 0; i < liveStatMinSamples; i++ {
		m.observeLiveStat(LiveIndexerResult{IndexerID: indexerID, Duration: 4 * time.Second, Results: 3})
	}
}

func TestLiveEarlyFinishSkipsSlackIndexer(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/fast/search", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(liveHTML(liveRow("Fast Show S01E01", "/details/1", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"))))
	})
	mux.HandleFunc("/slow/search", func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(4 * time.Second)
		_, _ = w.Write([]byte(liveHTML(liveRow("Slow Show S01E01", "/details/2", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"))))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	st := newTestStore(t)
	manager := newTestManager(t, st, newTestVault(t, 3))
	seedDefinition(t, st, "early-fast", "", liveDefinition("early-fast", server.URL+"/fast", "  search: [q]", ""))
	seedDefinition(t, st, "early-slow", "", liveDefinition("early-slow", server.URL+"/slow", "  search: [q]", ""))
	fastID := createIndexer(t, st, "early-fast", "early-fast", "Fast", "torrent", fmt.Sprintf(`{"url":%q}`, server.URL+"/fast"))
	slowID := createIndexer(t, st, "early-slow", "early-slow", "Slow", "torrent", fmt.Sprintf(`{"url":%q}`, server.URL+"/slow"))
	warmLiveStats(manager, fastID, slowID)

	variant := LiveQuery{Mode: "tvsearch", Query: "Show", Season: 1, Episode: 1, Limit: 50, App: "sonarr"}
	// The first variant of an episode keeps the full window; this call only
	// registers the group.
	manager.beginEpisodeVariant(variant)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	started := time.Now()
	result, err := manager.SearchLive(ctx, variant)
	elapsed := time.Since(started)
	if err != nil {
		t.Fatalf("SearchLive: %v", err)
	}
	if elapsed >= 3500*time.Millisecond {
		t.Fatalf("early finish waited %s, want the grace window not the full slow answer", elapsed)
	}
	var slow LiveIndexerResult
	for _, entry := range result.Indexers {
		if entry.IndexerID == slowID {
			slow = entry
		}
	}
	if !slow.TimedOut {
		t.Fatalf("slow outcome = %+v, want TimedOut after the early finish", slow)
	}

	// The detached worker still stores its rows for the next request.
	deadline := time.Now().Add(7 * time.Second)
	for time.Now().Before(deadline) {
		if _, total, lookupErr := st.SearchReleases(context.Background(), store.Filter{Query: "Slow", Limit: 10}); lookupErr == nil && total > 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("the detached slow worker did not store its result")
}

func TestLiveEarlyFinishWaitsForProductiveSite(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/fast/search", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(liveHTML(liveRow("Fast Show S01E01", "/details/1", "cccccccccccccccccccccccccccccccccccccccc"))))
	})
	mux.HandleFunc("/late/search", func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
		_, _ = w.Write([]byte(liveHTML(liveRow("Late Show S01E01", "/details/2", "dddddddddddddddddddddddddddddddddddddddd"))))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	st := newTestStore(t)
	manager := newTestManager(t, st, newTestVault(t, 3))
	seedDefinition(t, st, "late-fast", "", liveDefinition("late-fast", server.URL+"/fast", "  search: [q]", ""))
	seedDefinition(t, st, "late-good", "", liveDefinition("late-good", server.URL+"/late", "  search: [q]", ""))
	fastID := createIndexer(t, st, "late-fast", "late-fast", "Fast", "torrent", fmt.Sprintf(`{"url":%q}`, server.URL+"/fast"))
	lateID := createIndexer(t, st, "late-good", "late-good", "Late good", "torrent", fmt.Sprintf(`{"url":%q}`, server.URL+"/late"))
	warmLiveStats(manager, fastID, lateID)
	warmProductiveStats(manager, lateID)

	variant := LiveQuery{Mode: "tvsearch", Query: "Show", Season: 1, Episode: 1, Limit: 50, App: "sonarr"}
	manager.beginEpisodeVariant(variant)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := manager.SearchLive(ctx, variant)
	if err != nil {
		t.Fatalf("SearchLive: %v", err)
	}
	for _, entry := range result.Indexers {
		if entry.IndexerID == lateID {
			if entry.TimedOut || entry.Results == 0 {
				t.Fatalf("productive late site = %+v, want its rows in the response", entry)
			}
			return
		}
	}
	t.Fatal("the late productive site is missing from the outcome")
}

func TestLiveStatProfile(t *testing.T) {
	var series liveStatSeries
	for i := 1; i <= 10; i++ {
		series.add(time.Duration(i)*time.Millisecond, 0)
	}
	p90, success, samples := series.profile()
	if samples != 10 || p90 != 9*time.Millisecond || success != 0 {
		t.Fatalf("profile = p90 %s success %d samples %d", p90, success, samples)
	}
	for i := 0; i < 4; i++ {
		series.add(3*time.Second, 5)
	}
	if _, success, samples := series.profile(); success != 4*100/14 || samples != 14 {
		t.Fatalf("success after four productive answers = %d/%d, want %d/14", success, samples, 4*100/14)
	}
	// A fresh series proves the percentage on its own: four answers out of
	// four is 100%, regardless of the previous ring.
	var productive liveStatSeries
	for i := 0; i < 4; i++ {
		productive.add(3*time.Second, 5)
	}
	if _, success, _ := productive.profile(); success != 100 {
		t.Fatalf("productive success = %d, want 100", success)
	}
}

func TestLiveEarlyFinishCutsOverdueProductiveSite(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/fast/search", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(liveHTML(liveRow("Fast Show S01E02", "/details/1", "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"))))
	})
	mux.HandleFunc("/hang/search", func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(3 * time.Second)
		_, _ = w.Write([]byte(liveHTML(liveRow("Hang Show S01E02", "/details/2", "ffffffffffffffffffffffffffffffffffffffff"))))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	st := newTestStore(t)
	manager := newTestManager(t, st, newTestVault(t, 3))
	seedDefinition(t, st, "hang-fast", "", liveDefinition("hang-fast", server.URL+"/fast", "  search: [q]", ""))
	seedDefinition(t, st, "hang-site", "", liveDefinition("hang-site", server.URL+"/hang", "  search: [q]", ""))
	fastID := createIndexer(t, st, "hang-fast", "hang-fast", "Fast", "torrent", fmt.Sprintf(`{"url":%q}`, server.URL+"/fast"))
	hangID := createIndexer(t, st, "hang-site", "hang-site", "Hang", "torrent", fmt.Sprintf(`{"url":%q}`, server.URL+"/hang"))
	// Productive but normally fast: p90 around one second.
	for i := 0; i < liveStatMinSamples; i++ {
		manager.observeLiveStat(LiveIndexerResult{IndexerID: fastID, Duration: 100 * time.Millisecond, Results: 2})
		manager.observeLiveStat(LiveIndexerResult{IndexerID: hangID, Duration: time.Second, Results: 2})
	}

	variant := LiveQuery{Mode: "tvsearch", Query: "Show", Season: 1, Episode: 2, Limit: 50, App: "sonarr"}
	manager.beginEpisodeVariant(variant)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	started := time.Now()
	result, err := manager.SearchLive(ctx, variant)
	elapsed := time.Since(started)
	if err != nil {
		t.Fatalf("SearchLive: %v", err)
	}
	if elapsed >= 2500*time.Millisecond {
		t.Fatalf("overdue productive site waited %s, want a short grace", elapsed)
	}
	for _, entry := range result.Indexers {
		if entry.IndexerID == hangID && !entry.TimedOut {
			t.Fatalf("hang outcome = %+v, want TimedOut", entry)
		}
	}
}

func TestWaitLiveWaitsForRunningWorkers(t *testing.T) {
	manager := &Manager{liveRuns: make(map[string]*liveRun)}
	req := LiveQuery{Mode: "tvsearch", Query: "Show", Season: 1, Episode: 2, Limit: 100}
	done := make(chan struct{})
	manager.registerLiveRun(liveQueryKey(req), done)
	finished := make(chan struct{})
	go func() {
		manager.WaitLive(context.Background(), req, 5*time.Second)
		close(finished)
	}()
	select {
	case <-finished:
		t.Fatal("WaitLive returned while the workers were still running")
	case <-time.After(100 * time.Millisecond):
	}
	close(done)
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("WaitLive did not return after the workers finished")
	}

	// With no worker for the query it returns immediately.
	manager.WaitLive(context.Background(), LiveQuery{Mode: "search", Query: "Other"}, 5*time.Second)

	// A budget already spent by the first page leaves nothing to wait for.
	manager.registerLiveRun(liveQueryKey(req), make(chan struct{}))
	started := time.Now()
	manager.WaitLive(context.Background(), req, time.Millisecond)
	if time.Since(started) > 500*time.Millisecond {
		t.Fatal("WaitLive ignored the spent budget")
	}
}

func TestLiveNonServarrKeepsFullWindow(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/fast/search", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(liveHTML(liveRow("Fast Other S01E01", "/details/1", "1111111111111111111111111111111111111112"))))
	})
	mux.HandleFunc("/slow/search", func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
		_, _ = w.Write([]byte(liveHTML(liveRow("Slow Other S01E01", "/details/2", "1111111111111111111111111111111111111113"))))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	st := newTestStore(t)
	manager := newTestManager(t, st, newTestVault(t, 3))
	seedDefinition(t, st, "other-fast", "", liveDefinition("other-fast", server.URL+"/fast", "  search: [q]", ""))
	seedDefinition(t, st, "other-slow", "", liveDefinition("other-slow", server.URL+"/slow", "  search: [q]", ""))
	fastID := createIndexer(t, st, "other-fast", "other-fast", "Fast", "torrent", fmt.Sprintf(`{"url":%q}`, server.URL+"/fast"))
	slowID := createIndexer(t, st, "other-slow", "other-slow", "Slow", "torrent", fmt.Sprintf(`{"url":%q}`, server.URL+"/slow"))
	warmLiveStats(manager, fastID, slowID)

	// A client that is not a Servarr sends one query, so it must keep the
	// full window even when the same episode is requested again.
	req := LiveQuery{Mode: "tvsearch", Query: "Show", Season: 1, Episode: 1, Limit: 50, App: "python-urllib"}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := manager.SearchLive(ctx, req)
	if err != nil {
		t.Fatalf("SearchLive: %v", err)
	}
	found := false
	for _, entry := range result.Indexers {
		if entry.IndexerID == slowID {
			found = true
			if entry.TimedOut || entry.Results == 0 {
				t.Fatalf("non-Servarr slow site = %+v, want its rows", entry)
			}
		}
	}
	if !found {
		t.Fatal("the slow site is missing from the outcome")
	}
}

func TestEpisodeVariantBudget(t *testing.T) {
	manager := &Manager{liveEpisodes: make(map[string]time.Time)}
	req := LiveQuery{Mode: "tvsearch", Query: "Severance", Season: 2, Episode: 3,
		Categories: []int{5000, 2000}, App: "sonarr"}
	if reused, _ := manager.beginEpisodeVariant(req); reused {
		t.Fatal("the first variant must keep the full window")
	}
	second := req
	second.Query = "Scissione"
	reused, remaining := manager.beginEpisodeVariant(second)
	if !reused {
		t.Fatal("the second variant must share the episode budget")
	}
	if remaining <= 0 || remaining > liveEpisodeBudget {
		t.Fatalf("remaining budget = %s", remaining)
	}
	if reused, _ := manager.beginEpisodeVariant(LiveQuery{Mode: "tvsearch", Query: "Show", Season: 1, Episode: 1, App: "python-urllib"}); reused {
		t.Fatal("a non-Servarr client must not share the budget")
	}
	if reused, _ := manager.beginEpisodeVariant(LiveQuery{Mode: "search", Query: "Show", Season: 1, App: "sonarr"}); reused {
		t.Fatal("a plain search must not share the budget")
	}
}

func TestEpisodeGroupKey(t *testing.T) {
	base := LiveQuery{Mode: "tvsearch", Season: 2, Episode: 3, App: "sonarr", Categories: []int{5000, 2000}}
	other := base
	other.Query = "Another title"
	if episodeGroupKey(base) == "" || episodeGroupKey(base) != episodeGroupKey(other) {
		t.Fatal("the title must not change the episode group")
	}
	different := base
	different.Episode = 4
	if episodeGroupKey(base) == episodeGroupKey(different) {
		t.Fatal("a different episode must have a different group")
	}
	if episodeGroupKey(LiveQuery{Mode: "search", Season: 1}) != "" {
		t.Fatal("a plain search must not be grouped")
	}
	if episodeGroupKey(LiveQuery{Mode: "tvsearch"}) != "" {
		t.Fatal("a tvsearch without a season must not be grouped")
	}
}

func TestLivePartialCacheRefreshesWhenWorkerFinishes(t *testing.T) {
	manager := &Manager{liveCache: make(map[string]liveCacheEntry)}
	const key = "query"
	manager.cacheLive(key, time.Minute, LiveResult{
		Indexers: []LiveIndexerResult{
			{IndexerID: 1, Name: "Fast", Results: 2},
			{IndexerID: 2, Name: "Slow", TimedOut: true},
		},
		Stored: 2,
	})
	manager.refreshLiveCache(key, LiveIndexerResult{IndexerID: 2, Name: "Slow", Results: 3})
	cached, ok := manager.cachedLive(key)
	if !ok {
		t.Fatal("the partial answer is no longer cached")
	}
	if cached.Stored != 5 {
		t.Fatalf("stored = %d, want 5 after the late worker", cached.Stored)
	}
	for _, entry := range cached.Indexers {
		if entry.IndexerID == 2 && (entry.TimedOut || entry.Results != 3) {
			t.Fatalf("slow entry = %+v, want the late result", entry)
		}
	}
}
