package store

import (
	"context"
	"testing"
)

func TestReleaseFilesAreSearchable(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	rel := addFixtureRelease(t, st, "file-1", "Show S01 1080p", "hash-file-1", "site", 1000, false)
	if err := st.ReplaceReleaseFiles(ctx, rel.ID, []ReleaseFile{
		{Path: "Show/Season 1/S01E01.mkv", Size: 900},
		{Path: "Show/Season 1/readme.txt", Size: 1},
	}); err != nil {
		t.Fatalf("ReplaceReleaseFiles: %v", err)
	}
	if count, err := st.ReleaseFileCount(ctx, rel.ID); err != nil || count != 2 {
		t.Fatalf("file count = %d, %v", count, err)
	}

	for query, want := range map[string]int64{
		"S01E01":  1,
		"readme":  1,
		"missing": 0,
	} {
		_, total, err := st.SearchReleases(ctx, Filter{FilesOnly: true, Query: query, Limit: 10})
		if err != nil {
			t.Fatalf("search %q: %v", query, err)
		}
		if total != want {
			t.Errorf("search %q total = %d, want %d", query, total, want)
		}
	}

	// Replacing the list removes the old paths from the index.
	if err := st.ReplaceReleaseFiles(ctx, rel.ID, []ReleaseFile{{Path: "other/file.nfo", Size: 2}}); err != nil {
		t.Fatalf("replace: %v", err)
	}
	_, total, err := st.SearchReleases(ctx, Filter{FilesOnly: true, Query: "S01E01", Limit: 10})
	if err != nil || total != 0 {
		t.Fatalf("old file still found: %d, %v", total, err)
	}
	_, total, err = st.SearchReleases(ctx, Filter{FilesOnly: true, Query: "file.nfo", Limit: 10})
	if err != nil || total != 1 {
		t.Fatalf("new file not found: %d, %v", total, err)
	}
}

func TestExcludeAdultFilter(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	addFixtureRelease(t, st, "family", "Family Movie", "hash-family", "site", 1000, false)
	adult := addFixtureRelease(t, st, "adult", "Adult Movie", "hash-adult", "site", 1000, false)
	if _, err := st.DB().ExecContext(ctx, `UPDATE releases SET category = '6000' WHERE id = ?`, adult.ID); err != nil {
		t.Fatal(err)
	}
	_, total, err := st.SearchReleases(ctx, Filter{ExcludeAdult: true, Limit: 10})
	if err != nil || total != 1 {
		t.Fatalf("exclude adult total = %d, %v", total, err)
	}
	_, total, err = st.SearchReleases(ctx, Filter{Limit: 10})
	if err != nil || total != 2 {
		t.Fatalf("without filter total = %d, %v", total, err)
	}
}

func TestPopularAndGrabsSort(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	first := addFixtureRelease(t, st, "first", "First", "hash-first", "site", 1000, false)
	second := addFixtureRelease(t, st, "second", "Second", "hash-second", "site", 1000, false)
	if _, err := st.DB().ExecContext(ctx, `UPDATE releases SET times_seen = ? WHERE id = ?`, 9, second.ID); err != nil {
		t.Fatal(err)
	}
	releases, _, err := st.SearchReleases(ctx, Filter{Sort: "popular", Limit: 10})
	if err != nil || len(releases) < 2 || releases[0].ID != second.ID {
		t.Fatalf("popular order = %+v, %v", releases, err)
	}
	_ = first
}

func TestReleasesWithoutFilesBackfillMarker(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	rel := addFixtureRelease(t, st, "backfill", "Backfill", "hash-backfill", "site", 1000, false)
	if _, err := st.DB().ExecContext(ctx, `UPDATE releases SET torrent = ? WHERE id = ?`,
		[]byte("not-a-real-torrent"), rel.ID); err != nil {
		t.Fatal(err)
	}
	items, err := st.ReleasesWithoutFiles(ctx, 10)
	if err != nil || len(items) != 1 || items[0].ID != rel.ID {
		t.Fatalf("pending = %+v, %v", items, err)
	}
	if err := st.ReplaceReleaseFiles(ctx, rel.ID, nil); err != nil {
		t.Fatalf("mark parsed: %v", err)
	}
	items, err = st.ReleasesWithoutFiles(ctx, 10)
	if err != nil || len(items) != 0 {
		t.Fatalf("a marked release is still pending: %+v, %v", items, err)
	}
}
