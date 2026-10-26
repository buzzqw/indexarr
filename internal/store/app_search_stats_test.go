package store

import (
	"context"
	"testing"
	"time"
)

func TestAppSearchStatsAndFilters(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	now := time.Now()

	insert := func(in AppSearch) {
		t.Helper()
		if _, err := st.RecordAppSearch(ctx, in); err != nil {
			t.Fatalf("RecordAppSearch: %v", err)
		}
	}
	insert(AppSearch{
		App: "sonarr", Mode: "tvsearch", Query: "show a", TotalResults: 10,
		DurationMS: 200, CreatedAt: now.Add(-time.Hour),
		Live: []AppSearchLive{
			{Name: "1337x", Results: 6, Duration: 120},
			{Name: "TorrentLeech", Results: 4, Duration: 300, Error: "timeout"},
		},
	})
	insert(AppSearch{
		App: "radarr", Mode: "movie", Query: "movie b", TotalResults: 5,
		DurationMS: 400, CreatedAt: now.Add(-2 * time.Hour),
		Live: []AppSearchLive{
			{Name: "1337x", Results: 5, Duration: 180},
		},
	})
	insert(AppSearch{
		App: "sonarr", Mode: "tvsearch", Query: "broken", DurationMS: 600,
		Error: "request failed", CreatedAt: now.Add(-3 * time.Hour),
		Live: []AppSearchLive{
			{Name: "TorrentLeech", Results: 0, Duration: 50, TimedOut: true},
		},
	})
	// Outside the 24h window: must not be counted there.
	insert(AppSearch{
		App: "sonarr", Mode: "tvsearch", Query: "old", TotalResults: 99,
		DurationMS: 10, CreatedAt: now.AddDate(0, 0, -10),
	})

	day := now.Add(-24 * time.Hour)
	totals, err := st.AppSearchTotals(ctx, day)
	if err != nil {
		t.Fatalf("AppSearchTotals: %v", err)
	}
	if totals.Requests != 3 || totals.Errors != 1 || totals.Results != 15 {
		t.Fatalf("totals = %+v, want 3 requests / 1 error / 15 results", totals)
	}

	all, err := st.AppSearchTotals(ctx, time.Time{})
	if err != nil {
		t.Fatalf("AppSearchTotals all: %v", err)
	}
	if all.Requests != 4 {
		t.Fatalf("all-time requests = %d, want 4", all.Requests)
	}

	apps, err := st.AppSearchStatsByApp(ctx, day)
	if err != nil {
		t.Fatalf("AppSearchStatsByApp: %v", err)
	}
	if len(apps) != 2 || apps[0].App != "sonarr" || apps[0].Requests != 2 || apps[0].Errors != 1 {
		t.Fatalf("apps = %+v", apps)
	}

	indexers, err := st.AppSearchStatsByIndexer(ctx, day, 10)
	if err != nil {
		t.Fatalf("AppSearchStatsByIndexer: %v", err)
	}
	if len(indexers) != 2 {
		t.Fatalf("indexers = %+v, want 2", indexers)
	}
	byName := map[string]AppSearchIndexerStat{}
	for _, item := range indexers {
		byName[item.Name] = item
	}
	if got := byName["1337x"]; got.Requests != 2 || got.Failures != 0 || got.Results != 11 {
		t.Errorf("1337x = %+v", got)
	}
	if got := byName["TorrentLeech"]; got.Requests != 2 || got.Failures != 2 {
		t.Errorf("TorrentLeech = %+v", got)
	}

	countAll, err := st.CountAppSearchesFiltered(ctx, false)
	if err != nil || countAll != 4 {
		t.Fatalf("count all = %d, %v", countAll, err)
	}
	countErrors, err := st.CountAppSearchesFiltered(ctx, true)
	if err != nil || countErrors != 1 {
		t.Fatalf("count errors = %d, %v", countErrors, err)
	}
	onlyErrors, err := st.ListAppSearchesFiltered(ctx, true, 10, 0)
	if err != nil {
		t.Fatalf("ListAppSearchesFiltered: %v", err)
	}
	if len(onlyErrors) != 1 || onlyErrors[0].Query != "broken" {
		t.Fatalf("only errors = %+v", onlyErrors)
	}
}
