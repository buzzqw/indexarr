package store

import (
	"context"
	"reflect"
	"testing"
)

func TestPendingSearchesRoundTrip(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	mustCreateDefinition(t, st, "pending-def")
	enabled := mustCreateIndexer(t, st, "pending-def", "pending-on", "Pending On")
	disabled := mustCreateIndexer(t, st, "pending-def", "pending-off", "Pending Off")
	deleted := mustCreateIndexer(t, st, "pending-def", "pending-gone", "Pending Gone")

	queue := `[{"request":{"Query":"one"},"page":1}]`
	for _, id := range []int64{enabled, disabled, deleted} {
		if err := st.SetPendingSearches(ctx, id, queue); err != nil {
			t.Fatalf("SetPendingSearches(%d): %v", id, err)
		}
	}
	if got, err := st.PendingSearches(ctx, enabled); err != nil || got != queue {
		t.Fatalf("PendingSearches = %q, %v; want %q", got, err, queue)
	}

	off, err := st.GetIndexer(ctx, disabled)
	if err != nil {
		t.Fatalf("GetIndexer: %v", err)
	}
	off.Enabled = false
	if err := st.UpdateIndexer(ctx, off); err != nil {
		t.Fatalf("UpdateIndexer: %v", err)
	}
	if err := st.DeleteIndexer(ctx, deleted); err != nil {
		t.Fatalf("DeleteIndexer: %v", err)
	}
	// A deleted indexer takes its queue with it; a disabled one keeps it but
	// is not resumed at startup.
	if got, err := st.PendingSearches(ctx, deleted); err != nil || got != "" {
		t.Fatalf("PendingSearches(deleted) = %q, %v; want empty", got, err)
	}
	ids, err := st.IndexersWithPendingSearches(ctx)
	if err != nil {
		t.Fatalf("IndexersWithPendingSearches: %v", err)
	}
	if !reflect.DeepEqual(ids, []int64{enabled}) {
		t.Fatalf("IndexersWithPendingSearches = %v, want [%d]", ids, enabled)
	}

	// An empty queue removes the row.
	if err := st.SetPendingSearches(ctx, enabled, ""); err != nil {
		t.Fatalf("SetPendingSearches(empty): %v", err)
	}
	if got, err := st.PendingSearches(ctx, enabled); err != nil || got != "" {
		t.Fatalf("PendingSearches after clearing = %q, %v; want empty", got, err)
	}
}
