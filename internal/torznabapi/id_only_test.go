package torznabapi

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/buzzqw/indexarr/internal/indexer"
	"github.com/buzzqw/indexarr/internal/store"
)

func TestIDOnlyRequestLendsItsCachedTitleToTheFanOut(t *testing.T) {
	api, st := newAPI(t)
	ctx := context.Background()
	settings, err := st.LoadSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	settings.TorznabLiveSearch = true
	settings.TorznabLiveWait = 5
	if err := st.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}

	release := store.Release{
		DedupKey:    "id-only-title",
		Title:       "Series A S01E01 1080p ITA-ENG X265-GROUP.mkv",
		SearchTitle: store.NormalizeSearch("Series A S01E01 1080p ITA-ENG X265-GROUP.mkv"),
		IMDbID:      "1234567",
		Category:    "5040",
		PublishedAt: time.Now().Add(-time.Hour),
		Magnet:      "magnet:?xt=urn:btih:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		FirstSeenAt: time.Now().Add(-time.Hour),
		LastSeenAt:  time.Now(),
	}
	if _, err := st.UpsertRelease(ctx, &release); err != nil {
		t.Fatal(err)
	}

	var queries []string
	api.SetLiveSearch(func(_ context.Context, req indexer.LiveQuery) (indexer.LiveResult, error) {
		queries = append(queries, req.Query)
		return indexer.LiveResult{}, nil
	})

	recorder := do(t, api, "/api?t=search&apikey=secret&imdbid=tt1234567")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
	if len(queries) != 1 {
		t.Fatalf("live calls = %d, want 1", len(queries))
	}
	if !strings.Contains(strings.ToLower(queries[0]), "series a") {
		t.Errorf("live query = %q, want the title resolved from the cache", queries[0])
	}
	// The cached answer must still be the id-filtered one, not a text search.
	if !strings.Contains(recorder.Body.String(), release.Title) {
		t.Error("the cached row with that id is missing from the answer")
	}
	if !strings.Contains(recorder.Body.String(), `total="1"`) {
		t.Error("the id filter was replaced by the borrowed title")
	}
}

func TestIDOnlyRequestWithoutACachedTitleStaysLiveSilent(t *testing.T) {
	api, st := newAPI(t)
	ctx := context.Background()
	settings, _ := st.LoadSettings(ctx)
	settings.TorznabLiveSearch = true
	settings.TorznabLiveWait = 5
	if err := st.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	calls := 0
	api.SetLiveSearch(func(context.Context, indexer.LiveQuery) (indexer.LiveResult, error) {
		calls++
		return indexer.LiveResult{}, nil
	})
	recorder := do(t, api, "/api?t=search&apikey=secret&imdbid=tt9999999")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	if calls != 0 {
		t.Errorf("live calls = %d: an unknown id cannot invent a query", calls)
	}
}
