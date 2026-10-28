package store

import (
	"context"
	"testing"
	"time"
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

func TestRunRetryStatesCountsFailuresAndResetsOnSuccess(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	if err := st.UpsertDefinition(ctx, testDefinition("retry-site")); err != nil {
		t.Fatalf("UpsertDefinition: %v", err)
	}
	id, err := st.CreateIndexer(ctx, Indexer{
		DefinitionID: "retry-site", Slug: "retry-site", Name: "Retry", Enabled: true,
	})
	if err != nil {
		t.Fatalf("CreateIndexer: %v", err)
	}
	base := time.Now().Add(-4 * time.Hour)
	for i := 0; i < 3; i++ {
		if _, err := st.RecordIndexerHistory(ctx, IndexerHistory{
			IndexerID: id, Kind: "run", Status: "error",
			StartedAt: base.Add(time.Duration(i) * time.Hour),
		}); err != nil {
			t.Fatal(err)
		}
	}
	states, err := st.RunRetryStates(ctx)
	if err != nil {
		t.Fatalf("RunRetryStates: %v", err)
	}
	if got := states[id].Failures; got != 3 {
		t.Fatalf("failures = %d, want 3", got)
	}

	// A successful test after the last failed run resets the streak.
	if _, err := st.RecordIndexerHistory(ctx, IndexerHistory{
		IndexerID: id, Kind: "test", Status: "ok",
		StartedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	states, err = st.RunRetryStates(ctx)
	if err != nil {
		t.Fatalf("RunRetryStates after success: %v", err)
	}
	if got := states[id].Failures; got != 0 {
		t.Fatalf("failures after success = %d, want 0", got)
	}
}

// TestRunRetryStatesIgnoresPausedRows keeps the escalating backoff intact: a
// row the breaker refused is transparent, so the streak of the real failures
// before it survives and a dead site still reaches the last step.
func TestRunRetryStatesIgnoresPausedRows(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	if err := st.UpsertDefinition(ctx, testDefinition("paused-site")); err != nil {
		t.Fatalf("UpsertDefinition: %v", err)
	}
	id, err := st.CreateIndexer(ctx, Indexer{
		DefinitionID: "paused-site", Slug: "paused-site", Name: "Paused", Enabled: true,
	})
	if err != nil {
		t.Fatalf("CreateIndexer: %v", err)
	}
	base := time.Now().Add(-3 * time.Hour)
	for i, status := range []string{"error", "error", "paused"} {
		if _, err := st.RecordIndexerHistory(ctx, IndexerHistory{
			IndexerID: id, Kind: "run", Status: status,
			StartedAt: base.Add(time.Duration(i) * time.Hour),
		}); err != nil {
			t.Fatal(err)
		}
	}
	states, err := st.RunRetryStates(ctx)
	if err != nil {
		t.Fatalf("RunRetryStates: %v", err)
	}
	if got := states[id].Failures; got != 2 {
		t.Fatalf("failures = %d, want 2 (the pause must be transparent)", got)
	}
}
