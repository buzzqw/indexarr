package web

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/buzzqw/indexarr/internal/store"
)

// The batched reads must produce exactly the same health view as the
// per-indexer path: the list page may not show a different colour than the
// detail page for the same source.
func TestHealthBatchMatchesPerIndexerReads(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "health.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if err := st.UpsertDefinition(ctx, store.Definition{ID: "test-def", State: "bozza", Origin: "local", Body: "x", Hash: "h"}); err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for i := 0; i < 2; i++ {
		id, err := st.CreateIndexer(ctx, store.Indexer{
			DefinitionID: "test-def",
			Protocol:     "torrent",
			Slug:         "test-" + string(rune('a'+i)),
			Name:         "Test " + string(rune('A'+i)),
			Enabled:      true,
		})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := st.UpsertIndexerState(ctx, store.IndexerState{
		IndexerID: ids[0], Circuit: "open", LastError: "timeout",
		UpdatedAt: time.Unix(1_700_000_000, 0),
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for _, row := range []store.IndexerHistory{
		{IndexerID: ids[0], Kind: "run", Status: "error", Error: "timeout", StartedAt: now.Add(-3 * time.Minute)},
		{IndexerID: ids[0], Kind: "search", Status: "ok", StartedAt: now.Add(-2 * time.Minute),
			Metrics: &store.RunMetrics{Seen: 4, FieldTotal: 10, FieldSize: 9}},
		{IndexerID: ids[0], Kind: "download", Status: "ok", StartedAt: now.Add(-time.Minute)},
		{IndexerID: ids[1], Kind: "run", Status: "ok", StartedAt: now,
			Metrics: &store.RunMetrics{Seen: 2, FieldTotal: 2}},
	} {
		if _, err := st.RecordIndexerHistory(ctx, row); err != nil {
			t.Fatal(err)
		}
	}

	srv := &Server{store: st}
	indexers, err := st.ListIndexers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	batch := srv.loadHealthBatch(ctx, indexers)
	if batch == nil {
		t.Fatal("loadHealthBatch returned nil on a working store")
	}
	for _, in := range indexers {
		got := srv.loadIndexerHealth(ctx, in, nil, batch)
		want := srv.loadIndexerHealth(ctx, in, nil, nil)
		if got.HasState != want.HasState || got.State.Circuit != want.State.Circuit ||
			got.State.LastError != want.State.LastError {
			t.Fatalf("indexer %d state differs: batch=%#v single=%#v", in.ID, got.State, want.State)
		}
		if !reflect.DeepEqual(got.Assessment, want.Assessment) {
			t.Fatalf("indexer %d assessment differs: batch=%#v single=%#v", in.ID, got.Assessment, want.Assessment)
		}
		if (got.LastRun == nil) != (want.LastRun == nil) {
			t.Fatalf("indexer %d last run presence differs", in.ID)
		}
		if got.LastRun != nil {
			if got.LastRun.ID != want.LastRun.ID || got.LastRun.Kind != want.LastRun.Kind ||
				got.LastRun.Status != want.LastRun.Status || got.LastRun.Error != want.LastRun.Error ||
				!got.LastRun.StartedAt.Equal(want.LastRun.StartedAt) {
				t.Fatalf("indexer %d last run differs: batch=%#v single=%#v", in.ID, got.LastRun, want.LastRun)
			}
		}
	}
}
