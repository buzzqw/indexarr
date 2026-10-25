package store

import (
	"context"
	"testing"
	"time"
)

func TestCachedStatsAndHomeActivityRespectInvalidation(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	if err := st.UpsertDefinition(ctx, Definition{
		ID: "cached", SchemaVersion: 1, Family: "torznab", State: "verificata",
		Origin: "local", Author: "test", Body: "schema: 1\nconnector: torznab\n",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateIndexer(ctx, Indexer{
		DefinitionID: "cached", Slug: "cached", Name: "Cached", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	stats, err := st.CachedStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Indexers != 1 {
		t.Fatalf("indexers = %d, want 1", stats.Indexers)
	}

	// A second write bumps the revision, so the next read is fresh.
	if _, err := st.CreateIndexer(ctx, Indexer{
		DefinitionID: "cached", Slug: "cached-2", Name: "Cached 2", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	fresh, err := st.CachedStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Indexers != 2 {
		t.Fatalf("indexers after invalidation = %d, want 2", fresh.Indexers)
	}

	activity, err := st.CachedHomeActivity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if activity.AppSearches != 0 {
		t.Fatalf("app searches = %d, want 0", activity.AppSearches)
	}
	if _, err := st.RecordAppSearch(ctx, AppSearch{
		App: "sonarr", Mode: "tvsearch", Query: "Show", CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	activity, err = st.CachedHomeActivity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if activity.AppSearches != 1 {
		t.Fatalf("app searches after a recorded search = %d, want 1", activity.AppSearches)
	}
}

func TestRecentLiveIndexerSamples(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	if _, err := st.RecordAppSearch(ctx, AppSearch{
		App: "sonarr", Mode: "tvsearch", Query: "Show", CreatedAt: time.Now(),
		Live: []AppSearchLive{
			{IndexerID: 1, Name: "One", Duration: 120, Results: 2},
			{IndexerID: 2, Name: "Two", Duration: 5000, TimedOut: true},
			{IndexerID: 3, Name: "Feed", Skipped: true},
		},
	}); err != nil {
		t.Fatal(err)
	}
	samples, err := st.RecentLiveIndexerSamples(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(samples) != 3 {
		t.Fatalf("samples = %d, want 3", len(samples))
	}
	if samples[0].IndexerID != 1 || samples[0].Duration != 120 || samples[0].Results != 2 {
		t.Fatalf("first sample = %+v", samples[0])
	}
	if !samples[1].TimedOut {
		t.Fatalf("second sample = %+v, want timed out", samples[1])
	}
}
