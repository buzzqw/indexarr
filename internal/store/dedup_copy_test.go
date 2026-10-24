package store

import (
	"context"
	"os"
	"testing"
)

// TestDedupeDBCopy is an opt-in acceptance check. Point INDEXARR_DEDUP_DB at a
// disposable copy of a real database and it reports the row counts before and
// after the cleanup and verifies that every source is still reachable.
func TestDedupeDBCopy(t *testing.T) {
	path := os.Getenv("INDEXARR_DEDUP_DB")
	if path == "" {
		t.Skip("set INDEXARR_DEDUP_DB to a disposable database copy")
	}
	st, err := Open(path)
	if err != nil {
		t.Fatalf("Open(%s): %v", path, err)
	}
	defer st.Close()
	ctx := context.Background()
	var before, sourcesBefore int64
	if err := st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM releases`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM release_sources`).Scan(&sourcesBefore); err != nil {
		t.Fatal(err)
	}
	stats, err := st.MergeDuplicates(ctx)
	if err != nil {
		t.Fatalf("MergeDuplicates: %v", err)
	}
	var after, sourcesAfter, orphan int64
	if err := st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM releases`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if err := st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM release_sources`).Scan(&sourcesAfter); err != nil {
		t.Fatal(err)
	}
	if err := st.DB().QueryRowContext(ctx, `
		SELECT COUNT(*) FROM release_sources rs
		LEFT JOIN releases r ON r.id = rs.release_id WHERE r.id IS NULL`).Scan(&orphan); err != nil {
		t.Fatal(err)
	}
	t.Logf("releases before=%d after=%d removed=%d (hash=%d title=%d)",
		before, after, stats.Removed, stats.HashMerged, stats.TitleMerged)
	t.Logf("release_sources before=%d after=%d orphan=%d", sourcesBefore, sourcesAfter, orphan)
	if after != stats.After || stats.Removed != before-after {
		t.Fatalf("inconsistent stats: %#v before=%d after=%d", stats, before, after)
	}
	if orphan != 0 {
		t.Fatalf("orphan sources after cleanup: %d", orphan)
	}
	again, err := st.MergeDuplicates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if again.Removed != 0 {
		t.Fatalf("second pass removed %d rows", again.Removed)
	}
}
