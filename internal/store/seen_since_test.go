package store

import (
	"context"
	"testing"
	"time"
)

// A manual live search reads only releases touched after it started: the
// SeenSince predicate must not mix the older cache rows into the table.
func TestSearchReleasesSeenSince(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	old := addFixtureRelease(t, st, "old", "Old Release", "hash-old", "site", 1000, false)
	fresh := addFixtureRelease(t, st, "fresh", "Fresh Release", "hash-fresh", "site", 2000, false)

	cutoff := time.Now()
	oldSeen := cutoff.Add(-2 * time.Minute).Unix()
	if _, err := st.DB().ExecContext(ctx,
		`UPDATE releases SET last_seen_at = ? WHERE id = ?`, oldSeen, old.ID); err != nil {
		t.Fatalf("age old release: %v", err)
	}
	if _, err := st.DB().ExecContext(ctx,
		`UPDATE releases SET last_seen_at = ? WHERE id = ?`, cutoff.Add(time.Second).Unix(), fresh.ID); err != nil {
		t.Fatalf("touch fresh release: %v", err)
	}

	releases, total, err := st.SearchReleases(ctx, Filter{SeenSince: cutoff, Limit: 10})
	if err != nil {
		t.Fatalf("SearchReleases: %v", err)
	}
	if total != 1 || len(releases) != 1 || releases[0].ID != fresh.ID {
		t.Fatalf("SeenSince returned total=%d rows=%+v, want only %d", total, releases, fresh.ID)
	}

	all, total, err := st.SearchReleases(ctx, Filter{Limit: 10})
	if err != nil || total != 2 || len(all) != 2 {
		t.Fatalf("without SeenSince total=%d rows=%d err=%v", total, len(all), err)
	}
}
