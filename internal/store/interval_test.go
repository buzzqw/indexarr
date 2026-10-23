package store

import (
	"context"
	"testing"
)

func TestIndexerIntervalRoundTrip(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	if err := st.UpsertDefinition(ctx, testDefinition("interval-site")); err != nil {
		t.Fatalf("UpsertDefinition: %v", err)
	}
	id, err := st.CreateIndexer(ctx, Indexer{
		DefinitionID:    "interval-site",
		Slug:            "interval-site",
		Name:            "Interval Site",
		Enabled:         true,
		IntervalSeconds: 900,
	})
	if err != nil {
		t.Fatalf("CreateIndexer: %v", err)
	}
	got, err := st.GetIndexer(ctx, id)
	if err != nil {
		t.Fatalf("GetIndexer: %v", err)
	}
	if got.IntervalSeconds != 900 {
		t.Fatalf("interval = %d, want 900", got.IntervalSeconds)
	}

	got.IntervalSeconds = 0
	if err := st.UpdateIndexer(ctx, got); err != nil {
		t.Fatalf("UpdateIndexer: %v", err)
	}
	again, err := st.GetIndexer(ctx, id)
	if err != nil {
		t.Fatalf("GetIndexer after update: %v", err)
	}
	if again.IntervalSeconds != 0 {
		t.Fatalf("interval after update = %d, want 0", again.IntervalSeconds)
	}
}

func TestLastRunTimes(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	if err := st.UpsertDefinition(ctx, testDefinition("last-run-site")); err != nil {
		t.Fatalf("UpsertDefinition: %v", err)
	}
	id, err := st.CreateIndexer(ctx, Indexer{
		DefinitionID: "last-run-site", Slug: "last-run-site", Name: "Last Run", Enabled: true,
	})
	if err != nil {
		t.Fatalf("CreateIndexer: %v", err)
	}
	if _, err := st.RecordIndexerHistory(ctx, IndexerHistory{
		IndexerID: id, Kind: "run", Status: "ok",
	}); err != nil {
		t.Fatalf("RecordIndexerHistory: %v", err)
	}
	if _, err := st.RecordIndexerHistory(ctx, IndexerHistory{
		IndexerID: id, Kind: "search", Status: "ok",
	}); err != nil {
		t.Fatalf("RecordIndexerHistory(search): %v", err)
	}
	runs, err := st.LastRunTimes(ctx)
	if err != nil {
		t.Fatalf("LastRunTimes: %v", err)
	}
	if _, ok := runs[id]; !ok {
		t.Fatal("LastRunTimes did not return the run row")
	}
}
