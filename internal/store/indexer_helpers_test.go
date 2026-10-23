package store

import (
	"context"
	"testing"
	"time"
)

func TestCountIndexerHistory(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	mustCreateDefinition(t, st, "count-def")
	indexerID := mustCreateIndexer(t, st, "count-def", "count-site", "Count Site")
	otherID := mustCreateIndexer(t, st, "count-def", "count-other", "Count Other")

	base := time.Unix(1_700_000_000, 0)
	records := []IndexerHistory{
		{IndexerID: indexerID, Kind: "search", StartedAt: base},
		{IndexerID: indexerID, Kind: "search", StartedAt: base.Add(time.Hour)},
		{IndexerID: indexerID, Kind: "latest", StartedAt: base.Add(2 * time.Hour)},
		{IndexerID: indexerID, Kind: "run", StartedAt: base.Add(3 * time.Hour)},
		{IndexerID: indexerID, Kind: "grab", StartedAt: base.Add(4 * time.Hour)},
		{IndexerID: indexerID, Kind: "search", StartedAt: base.Add(-time.Hour)},
		{IndexerID: otherID, Kind: "search", StartedAt: base.Add(time.Hour)},
	}
	for _, record := range records {
		if _, err := st.RecordIndexerHistory(ctx, record); err != nil {
			t.Fatalf("RecordIndexerHistory(%s): %v", record.Kind, err)
		}
	}

	since := base.Add(30 * time.Minute)
	tests := []struct {
		name  string
		kinds []string
		want  int
	}{
		{name: "all kinds", kinds: nil, want: 4},
		{name: "single kind", kinds: []string{"search"}, want: 1},
		{name: "several kinds", kinds: []string{"search", "latest"}, want: 2},
		{name: "unknown kind", kinds: []string{"download"}, want: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := st.CountIndexerHistory(ctx, indexerID, test.kinds, since)
			if err != nil {
				t.Fatalf("CountIndexerHistory: %v", err)
			}
			if got != test.want {
				t.Fatalf("count = %d, want %d", got, test.want)
			}
		})
	}

	// A zero time counts every row of the indexer, including the one before
	// the window used above.
	got, err := st.CountIndexerHistory(ctx, indexerID, []string{"search"}, time.Time{})
	if err != nil {
		t.Fatalf("CountIndexerHistory zero since: %v", err)
	}
	if got != 3 {
		t.Fatalf("count with zero since = %d, want 3", got)
	}
}

func TestIndexerForRelease(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	mustCreateDefinition(t, st, "for-release-def")
	first := mustCreateIndexer(t, st, "for-release-def", "first-site", "First Site")
	second := mustCreateIndexer(t, st, "for-release-def", "second-site", "Second Site")

	now := time.Now()
	release := Release{
		DedupKey: "indexer-for-release", Title: "Provenance Release",
		SearchTitle: "provenance release", FirstSeenAt: now, LastSeenAt: now,
	}
	if _, err := st.UpsertRelease(ctx, &release); err != nil {
		t.Fatalf("UpsertRelease: %v", err)
	}

	if _, found, err := st.IndexerForRelease(ctx, release.ID); err != nil {
		t.Fatalf("IndexerForRelease without provenance: %v", err)
	} else if found {
		t.Fatal("IndexerForRelease found an indexer for a release without provenance")
	}

	if err := st.RecordReleaseSource(ctx, ReleaseSource{
		ReleaseID: release.ID, IndexerID: first,
		FirstSeenAt: now.Add(-time.Hour), LastSeenAt: now.Add(-time.Hour),
	}); err != nil {
		t.Fatalf("RecordReleaseSource first: %v", err)
	}
	if err := st.RecordReleaseSource(ctx, ReleaseSource{
		ReleaseID: release.ID, IndexerID: second,
		FirstSeenAt: now.Add(-2 * time.Hour), LastSeenAt: now,
	}); err != nil {
		t.Fatalf("RecordReleaseSource second: %v", err)
	}

	indexer, found, err := st.IndexerForRelease(ctx, release.ID)
	if err != nil {
		t.Fatalf("IndexerForRelease: %v", err)
	}
	if !found {
		t.Fatal("IndexerForRelease did not find the provenance indexer")
	}
	if indexer.ID != second {
		t.Fatalf("indexer = %d, want the most recently seen %d", indexer.ID, second)
	}
	if indexer.Slug != "second-site" {
		t.Fatalf("slug = %q", indexer.Slug)
	}

	if _, found, err := st.IndexerForRelease(ctx, release.ID+1000); err != nil {
		t.Fatalf("IndexerForRelease missing release: %v", err)
	} else if found {
		t.Fatal("IndexerForRelease found an indexer for a missing release")
	}
}
