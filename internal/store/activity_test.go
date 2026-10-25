package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestActivityMetricsSampleAndPrune(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	mustCreateDefinition(t, st, "activity-def")
	indexerID := mustCreateIndexer(t, st, "activity-def", "activity-one", "Activity One")

	started := time.Now().Add(-time.Hour)
	metrics := &RunMetrics{
		Seen: 10, ItemsNew: 4, Updated: 3, Duplicates: 1, Discarded: 2,
		DiscardNoLink: 1, DiscardBlocked: 1, FieldTotal: 10,
		FieldSize: 8, FieldSeeders: 6, FieldCategory: 5, FieldDate: 4,
		FieldHash: 9,
	}
	historyID, err := st.RecordIndexerHistory(ctx, IndexerHistory{
		IndexerID: indexerID, Kind: "run", Query: "ubuntu", Status: "ok",
		StartedAt: started, Metrics: metrics,
	})
	if err != nil {
		t.Fatalf("RecordIndexerHistory: %v", err)
	}
	rows, err := st.ListRunMetrics(ctx, indexerID, 20)
	if err != nil {
		t.Fatalf("ListRunMetrics: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("run metrics = %d, want 1", len(rows))
	}
	got := rows[0]
	if got.ID != historyID || got.Metrics.Seen != 10 || got.Metrics.ItemsNew != 4 ||
		got.Metrics.DiscardNoLink != 1 || got.Metrics.DiscardBlocked != 1 {
		t.Fatalf("run metrics = %+v", got)
	}
	if got.Metrics.Percent(got.Metrics.FieldSeeders) != 60 {
		t.Errorf("seeders completeness = %d%%, want 60%%",
			got.Metrics.Percent(got.Metrics.FieldSeeders))
	}
	if got.Metrics.Percent(0) != 0 {
		t.Errorf("zero completeness = %d", got.Metrics.Percent(0))
	}

	// The history reader exposes the same metrics without a second query.
	history, err := st.ListIndexerHistory(ctx, indexerID, 10)
	if err != nil {
		t.Fatalf("ListIndexerHistory: %v", err)
	}
	if len(history) != 1 || history[0].Metrics == nil ||
		history[0].Metrics.Seen != 10 {
		t.Fatalf("history metrics = %+v", history)
	}

	series, err := st.RunSeries(ctx, indexerID, started.Add(-time.Hour))
	if err != nil {
		t.Fatalf("RunSeries: %v", err)
	}
	if len(series) != 1 || series[0].New != 4 || series[0].Discarded != 2 {
		t.Fatalf("series = %+v", series)
	}

	sample := IndexerSample{
		IndexerID: indexerID, RequestURL: "https://tracker.example/search?q=x",
		Method: "GET", Status: 200, LatencyMS: 12, Body: "<html>one</html>",
		BodyBytes: 16, Items: []SampleItem{{Title: "One", Size: 10}},
	}
	if err := st.PutIndexerSample(ctx, sample); err != nil {
		t.Fatalf("PutIndexerSample: %v", err)
	}
	sample.Body = "<html>two</html>"
	sample.Items = []SampleItem{{Title: "Two"}}
	if err := st.PutIndexerSample(ctx, sample); err != nil {
		t.Fatalf("PutIndexerSample second: %v", err)
	}
	stored, found, err := st.GetIndexerSample(ctx, indexerID)
	if err != nil || !found {
		t.Fatalf("GetIndexerSample: found=%t err=%v", found, err)
	}
	if stored.Body != "<html>two</html>" || len(stored.Items) != 1 ||
		stored.Items[0].Title != "Two" {
		t.Fatalf("sample was not overwritten: %+v", stored)
	}

	appID, err := st.RecordAppSearch(ctx, AppSearch{
		App: "sonarr", Mode: "tvsearch", Query: "show", IndexerIDs: []int64{indexerID},
		IndexerNames: []string{"Activity One"}, Results: map[string]int64{"Activity One": 3},
		TotalResults: 3, DurationMS: 40, CreatedAt: started,
	})
	if err != nil || appID == 0 {
		t.Fatalf("RecordAppSearch: id=%d err=%v", appID, err)
	}
	searches, err := st.ListAppSearches(ctx, 10, 0)
	if err != nil {
		t.Fatalf("ListAppSearches: %v", err)
	}
	if len(searches) != 1 || searches[0].App != "sonarr" ||
		searches[0].Results["Activity One"] != 3 || searches[0].IndexerIDs[0] != indexerID {
		t.Fatalf("searches = %+v", searches)
	}

	// Retention removes old rows from all three tables and keeps fresh ones.
	old := time.Now().AddDate(0, 0, -40)
	oldID, err := st.RecordIndexerHistory(ctx, IndexerHistory{
		IndexerID: indexerID, Kind: "run", Status: "ok", StartedAt: old,
	})
	if err != nil {
		t.Fatalf("old history: %v", err)
	}
	if oldID == 0 {
		t.Fatal("old history has no id")
	}
	if err := st.PutIndexerSample(ctx, IndexerSample{
		IndexerID: indexerID, FetchedAt: old, Body: "old",
	}); err != nil {
		t.Fatalf("old sample: %v", err)
	}
	if _, err := st.RecordAppSearch(ctx, AppSearch{App: "old", CreatedAt: old}); err != nil {
		t.Fatalf("old app search: %v", err)
	}
	pruned, err := st.PruneActivity(ctx, time.Now().AddDate(0, 0, -30))
	if err != nil {
		t.Fatalf("PruneActivity: %v", err)
	}
	if pruned.History != 1 || pruned.Samples != 1 || pruned.Searches != 1 {
		t.Fatalf("pruned = %+v, want 1/1/1", pruned)
	}
	if _, found, err := st.GetIndexerSample(ctx, indexerID); err != nil || found {
		t.Fatalf("old sample survived: found=%t err=%v", found, err)
	}
	searches, err = st.ListAppSearches(ctx, 10, 0)
	if err != nil || len(searches) != 1 || searches[0].App != "sonarr" {
		t.Fatalf("fresh app search removed: %+v %v", searches, err)
	}
}

func TestHistoryRetentionSettingDefaultsToThirtyDays(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	settings, err := st.LoadSettings(ctx)
	if err != nil {
		t.Fatalf("LoadSettings: %v", err)
	}
	if settings.HistoryRetentionDays != 30 {
		t.Fatalf("default history retention = %d, want 30", settings.HistoryRetentionDays)
	}
	settings.HistoryRetentionDays = 7
	if err := st.SaveSettings(ctx, settings); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}
	again, err := st.LoadSettings(ctx)
	if err != nil || again.HistoryRetentionDays != 7 {
		t.Fatalf("history retention = %d, %v", again.HistoryRetentionDays, err)
	}
}

func TestActivityMigrationFromPreviousSchema(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "previous.db")

	first, err := Open(path)
	if err != nil {
		t.Fatalf("Open first: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close first: %v", err)
	}

	// Rewind to the schema before WP17: keep a history row to prove the
	// migration is additive, then remove the new tables and its version row.
	raw, err := sql.Open("sqlite", "file:"+dsnPath(path))
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	for _, statement := range []string{
		`DELETE FROM schema_migrations WHERE version IN (` + strconv.Itoa(indexarr2ActivityVersion) + `, ` + strconv.Itoa(indexarr2LiveSearchVersion) + `)`,
		`DROP TABLE IF EXISTS indexer_history_metrics`,
		`DROP TABLE IF EXISTS indexer_samples`,
		`DROP TABLE IF EXISTS app_searches`,
		`INSERT INTO indexer_history(kind, status, started_at) VALUES('run', 'ok', 1)`,
	} {
		if _, err := raw.ExecContext(ctx, statement); err != nil {
			_ = raw.Close()
			t.Fatalf("%s: %v", statement, err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	migrated, err := Open(path)
	if err != nil {
		t.Fatalf("Open migrated: %v", err)
	}
	defer migrated.Close()
	version, err := migrated.schemaVersion(ctx)
	if err != nil || version != indexarr2NotifyVersion {
		t.Fatalf("schema version = %d, %v", version, err)
	}
	for _, table := range []string{"indexer_history_metrics", "indexer_samples", "app_searches"} {
		var count int
		if err := migrated.DB().QueryRowContext(ctx,
			`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Errorf("table %s is missing after migration", table)
		}
	}
	var historyRows int
	if err := migrated.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM indexer_history`).Scan(&historyRows); err != nil {
		t.Fatal(err)
	}
	if historyRows != 1 {
		t.Errorf("previous history rows = %d, want 1", historyRows)
	}
}

func TestFeedReleasesProvenanceAndFilter(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	mustCreateDefinition(t, st, "feed-def")
	first := mustCreateIndexer(t, st, "feed-def", "feed-one", "Feed One")
	second := mustCreateIndexer(t, st, "feed-def", "feed-two", "Feed Two")

	t1 := time.Unix(1_700_000_000, 0)
	firstRelease := mustUpsertProvenanceRelease(t, st, "feed-first", "Feed First", t1, t1, 5, 1)
	secondRelease := mustUpsertProvenanceRelease(t, st, "feed-second", "Feed Second", t1.Add(time.Minute), t1.Add(time.Minute), 4, 2)
	for _, source := range []ReleaseSource{
		{ReleaseID: firstRelease.ID, IndexerID: first, FirstSeenAt: t1, LastSeenAt: t1},
		{ReleaseID: firstRelease.ID, IndexerID: second, FirstSeenAt: t1, LastSeenAt: t1},
		{ReleaseID: secondRelease.ID, IndexerID: second, FirstSeenAt: t1, LastSeenAt: t1},
	} {
		if err := st.RecordReleaseSource(ctx, source); err != nil {
			t.Fatalf("RecordReleaseSource: %v", err)
		}
	}

	feed, total, err := st.ListFeedReleases(ctx, 0, 10, 0)
	if err != nil {
		t.Fatalf("ListFeedReleases: %v", err)
	}
	if total != 2 || len(feed) != 2 {
		t.Fatalf("feed total=%d rows=%d", total, len(feed))
	}
	if feed[0].Title != "Feed Second" || feed[0].IndexerCount != 1 {
		t.Fatalf("newest feed row = %+v", feed[0])
	}
	if feed[1].IndexerCount != 2 || len(feed[1].IndexerNames) != 2 ||
		feed[1].IndexerNames[0] != "Feed One" {
		t.Fatalf("provenance feed row = %+v", feed[1])
	}

	filtered, total, err := st.ListFeedReleases(ctx, first, 10, 0)
	if err != nil {
		t.Fatalf("ListFeedReleases filtered: %v", err)
	}
	if total != 1 || len(filtered) != 1 || filtered[0].ID != firstRelease.ID {
		t.Fatalf("filtered feed = %+v total=%d", filtered, total)
	}
	page, total, err := st.ListFeedReleases(ctx, 0, 1, 1)
	if err != nil {
		t.Fatalf("ListFeedReleases page: %v", err)
	}
	if total != 2 || len(page) != 1 || page[0].ID != firstRelease.ID {
		t.Fatalf("page = %+v total=%d", page, total)
	}
}

func TestAppSearchLiveFanOutRoundTrip(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	id, err := st.RecordAppSearch(ctx, AppSearch{
		App: "sonarr", Mode: "tvsearch", Query: "Show",
		IndexerIDs: []int64{1, 2}, IndexerNames: []string{"One", "Two"},
		Results: map[string]int64{"One": 2}, TotalResults: 2, DurationMS: 20,
		Live: []AppSearchLive{
			{IndexerID: 1, Name: "One", Mode: "tvsearch", Results: 2, Duration: 10},
			{IndexerID: 2, Name: "Two", Mode: "search", Fallback: true, TimedOut: true, Duration: 20},
		},
	})
	if err != nil {
		t.Fatalf("RecordAppSearch: %v", err)
	}
	if id <= 0 {
		t.Fatalf("id = %d", id)
	}
	rows, err := st.ListAppSearches(ctx, 10, 0)
	if err != nil {
		t.Fatalf("ListAppSearches: %v", err)
	}
	if len(rows) != 1 || len(rows[0].Live) != 2 {
		t.Fatalf("live rows = %+v", rows)
	}
	if rows[0].Live[1].Mode != "search" || !rows[0].Live[1].Fallback || !rows[0].Live[1].TimedOut {
		t.Errorf("fallback/timeout not preserved: %+v", rows[0].Live[1])
	}
}

func TestIndexerLatencyPercentiles(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	mustCreateDefinition(t, st, "latency-def")
	indexerID := mustCreateIndexer(t, st, "latency-def", "latency-one", "Latency One")
	for _, latency := range []int64{10, 20, 30, 40, 1000} {
		if _, err := st.RecordIndexerHistory(ctx, IndexerHistory{
			IndexerID: indexerID, Kind: "search", Status: "ok", LatencyMS: latency,
			StartedAt: time.Now(),
		}); err != nil {
			t.Fatalf("RecordIndexerHistory: %v", err)
		}
	}
	latency, found, err := st.IndexerLatency(ctx, indexerID, 50)
	if err != nil || !found {
		t.Fatalf("IndexerLatency: %v found=%v", err, found)
	}
	if latency.P50MS != 30 || latency.P95MS != 1000 || latency.Count != 5 {
		t.Errorf("latency = %+v, want p50 30 p95 1000", latency)
	}
}
