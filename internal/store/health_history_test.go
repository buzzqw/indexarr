package store

import (
	"context"
	"testing"
	"time"
)

func TestIndexerStatesReturnsEveryState(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	ids := dedupFixture(t, st)

	for i, id := range ids {
		if err := st.UpsertIndexerState(ctx, IndexerState{
			IndexerID: id,
			Circuit:   "open",
			LastError: "boom",
			UpdatedAt: time.Unix(1_700_000_000+int64(i), 0),
		}); err != nil {
			t.Fatalf("UpsertIndexerState(%d): %v", id, err)
		}
	}
	states, err := st.IndexerStates(ctx)
	if err != nil {
		t.Fatalf("IndexerStates: %v", err)
	}
	if len(states) < len(ids) {
		t.Fatalf("IndexerStates returned %d rows, want at least %d", len(states), len(ids))
	}
	for _, id := range ids {
		state, ok := states[id]
		if !ok {
			t.Fatalf("IndexerStates is missing indexer %d", id)
		}
		if state.Circuit != "open" || state.LastError != "boom" {
			t.Fatalf("indexer %d state = %#v, want circuit open and last error boom", id, state)
		}
		if state.UpdatedAt.Unix() != 1_700_000_000 && state.UpdatedAt.Unix() != 1_700_000_001 {
			t.Fatalf("indexer %d updated_at = %v", id, state.UpdatedAt)
		}
	}
}

func TestListIndexerHealthHistoryGroupsByIndexerAndKeepsMetrics(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	ids := dedupFixture(t, st)
	now := time.Now()

	rows := []IndexerHistory{
		{IndexerID: ids[0], Kind: "run", Status: "error", Error: "timeout", StartedAt: now.Add(-3 * time.Minute)},
		{IndexerID: ids[0], Kind: "search", Status: "ok", StartedAt: now.Add(-2 * time.Minute),
			Metrics: &RunMetrics{Seen: 7, FieldTotal: 10, FieldSize: 9, FieldSeeders: 8,
				FieldCategory: 7, FieldDate: 6, FieldHash: 5}},
		{IndexerID: ids[0], Kind: "download", Status: "ok", StartedAt: now.Add(-time.Minute)},
		{IndexerID: ids[1], Kind: "run", Status: "ok", StartedAt: now.Add(-time.Minute),
			Metrics: &RunMetrics{Seen: 2, FieldTotal: 4}},
	}
	for _, row := range rows {
		if _, err := st.RecordIndexerHistory(ctx, row); err != nil {
			t.Fatalf("RecordIndexerHistory(%s): %v", row.Kind, err)
		}
	}

	got, err := st.ListIndexerHealthHistory(ctx, ids, 2)
	if err != nil {
		t.Fatalf("ListIndexerHealthHistory: %v", err)
	}
	if len(got[ids[0]]) != 2 {
		t.Fatalf("indexer %d got %d rows, want the limit of 2", ids[0], len(got[ids[0]]))
	}
	if got[ids[0]][0].Kind != "download" || got[ids[0]][1].Kind != "search" {
		t.Fatalf("indexer %d order = %q, %q; want newest first", ids[0],
			got[ids[0]][0].Kind, got[ids[0]][1].Kind)
	}
	if got[ids[0]][1].Metrics == nil || got[ids[0]][1].Metrics.FieldSize != 9 {
		t.Fatalf("metrics not carried: %#v", got[ids[0]][1].Metrics)
	}
	if got[ids[0]][0].Metrics != nil {
		t.Fatalf("download row has metrics: %#v", got[ids[0]][0].Metrics)
	}
	if len(got[ids[1]]) != 1 || got[ids[1]][0].Metrics == nil || got[ids[1]][0].Metrics.Seen != 2 {
		t.Fatalf("indexer %d = %#v", ids[1], got[ids[1]])
	}

	empty, err := st.ListIndexerHealthHistory(ctx, nil, 50)
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty call = %#v, %v; want an empty map and no error", empty, err)
	}
}

func TestListIndexerHealthHistoryMatchesFullHistoryFields(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	ids := dedupFixture(t, st)
	now := time.Now()
	for i := 0; i < 4; i++ {
		if _, err := st.RecordIndexerHistory(ctx, IndexerHistory{
			IndexerID: ids[0],
			Kind:      []string{"run", "search", "download", "health"}[i],
			Status:    []string{"ok", "error", "ok", "ok"}[i],
			Error:     []string{"", "boom", "", ""}[i],
			StartedAt: now.Add(-time.Duration(i) * time.Minute),
			Metrics:   &RunMetrics{Seen: int64(i), FieldTotal: int64(i + 1)},
		}); err != nil {
			t.Fatal(err)
		}
	}
	full, err := st.ListIndexerHistory(ctx, ids[0], 50)
	if err != nil {
		t.Fatal(err)
	}
	slim, err := st.ListIndexerHealthHistory(ctx, []int64{ids[0]}, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(full) != len(slim[ids[0]]) {
		t.Fatalf("len full=%d slim=%d", len(full), len(slim[ids[0]]))
	}
	for i := range full {
		a, b := full[i], slim[ids[0]][i]
		if a.ID != b.ID || a.Kind != b.Kind || a.Status != b.Status ||
			a.Error != b.Error || !a.StartedAt.Equal(b.StartedAt) {
			t.Fatalf("row %d differs: full=%#v slim=%#v", i, a, b)
		}
		if (a.Metrics == nil) != (b.Metrics == nil) {
			t.Fatalf("row %d metrics presence differs", i)
		}
		if a.Metrics != nil && (a.Metrics.Seen != b.Metrics.Seen || a.Metrics.FieldTotal != b.Metrics.FieldTotal) {
			t.Fatalf("row %d metrics differ: full=%#v slim=%#v", i, a.Metrics, b.Metrics)
		}
	}
}
