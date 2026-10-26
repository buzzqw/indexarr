package store

import (
	"context"
	"testing"
)

func TestListIndexerReleasesUsesProvenanceAndVisibility(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	ids := dedupFixture(t, st)

	newer := addFixtureRelease(t, st, "key-newer", "Newer.Release.2024.1080p", "", "tgx", 1_000, false)
	older := addFixtureRelease(t, st, "key-older", "Older.Release.2024.720p", "", "tgx", 2_000, false)
	other := addFixtureRelease(t, st, "key-other", "Other.Release.2024.1080p", "", "extto", 3_000, false)
	hidden := addFixtureRelease(t, st, "key-hidden", "Hidden.Release.2024.1080p", "", "tgx", 4_000, false)

	for _, rel := range []Release{newer, older, hidden} {
		if err := st.RecordReleaseSource(ctx, ReleaseSource{ReleaseID: rel.ID, IndexerID: ids[0]}); err != nil {
			t.Fatalf("RecordReleaseSource(%d): %v", rel.ID, err)
		}
	}
	if err := st.RecordReleaseSource(ctx, ReleaseSource{ReleaseID: other.ID, IndexerID: ids[1]}); err != nil {
		t.Fatalf("RecordReleaseSource(other): %v", err)
	}
	if _, err := st.DB().ExecContext(ctx, `UPDATE releases SET last_seen_at = ? WHERE id = ?`, 1_000, older.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().ExecContext(ctx, `UPDATE releases SET last_seen_at = ? WHERE id = ?`, 2_000, newer.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().ExecContext(ctx, `UPDATE releases SET hidden = 1 WHERE id = ?`, hidden.ID); err != nil {
		t.Fatal(err)
	}

	got, err := st.ListIndexerReleases(ctx, ids[0], 10)
	if err != nil {
		t.Fatalf("ListIndexerReleases: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d releases, want the two visible ones from the first indexer", len(got))
	}
	if got[0].ID != newer.ID || got[1].ID != older.ID {
		t.Fatalf("order = [%d %d], want newest first [%d %d]", got[0].ID, got[1].ID, newer.ID, older.ID)
	}

	limited, err := st.ListIndexerReleases(ctx, ids[0], 1)
	if err != nil {
		t.Fatalf("ListIndexerReleases limit: %v", err)
	}
	if len(limited) != 1 || limited[0].ID != newer.ID {
		t.Fatalf("limited = %#v, want only the newest release", limited)
	}

	empty, err := st.ListIndexerReleases(ctx, 0, 10)
	if err != nil || empty != nil {
		t.Fatalf("ListIndexerReleases(0) = %v, %v; want nil, nil", empty, err)
	}
}
