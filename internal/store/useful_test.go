package store

import (
	"context"
	"testing"
	"time"
)

func TestSourceDownloadStatsCountsGrabs(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	for _, id := range []string{"useful-a", "useful-b"} {
		if err := st.UpsertDefinition(ctx, testDefinition(id)); err != nil {
			t.Fatalf("UpsertDefinition(%s): %v", id, err)
		}
	}
	a, err := st.CreateIndexer(ctx, Indexer{DefinitionID: "useful-a", Slug: "useful-a", Name: "Useful A", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.CreateIndexer(ctx, Indexer{DefinitionID: "useful-b", Slug: "useful-b", Name: "Useful B", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for i := 0; i < 3; i++ {
		if _, err := st.RecordIndexerHistory(ctx, IndexerHistory{
			IndexerID: a, Kind: "grab", Status: "ok", StartedAt: now.Add(-time.Duration(i) * time.Minute),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.RecordIndexerHistory(ctx, IndexerHistory{
		IndexerID: b, Kind: "grab", Status: "magnet", StartedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	// An internal .torrent fetch (infohash recovery) is not a user download.
	if _, err := st.RecordIndexerHistory(ctx, IndexerHistory{
		IndexerID: b, Kind: "download", Status: "ok", StartedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	// An old row outside the window is ignored.
	if _, err := st.RecordIndexerHistory(ctx, IndexerHistory{
		IndexerID: b, Kind: "grab", Status: "ok", StartedAt: now.AddDate(0, 0, -60),
	}); err != nil {
		t.Fatal(err)
	}
	stats, err := st.SourceDownloadStats(ctx, now.AddDate(0, 0, -30))
	if err != nil {
		t.Fatalf("SourceDownloadStats: %v", err)
	}
	if len(stats) != 2 || stats[0].IndexerID != a || stats[0].Downloads != 3 || stats[1].Downloads != 1 {
		t.Fatalf("stats = %+v", stats)
	}

	grabs, err := st.GrabStatsByIndexer(ctx, now.AddDate(0, 0, -30))
	if err != nil {
		t.Fatalf("GrabStatsByIndexer: %v", err)
	}
	if len(grabs) != 2 || grabs[0].Torrents != 3 || grabs[1].Magnets != 1 || grabs[1].Total() != 1 {
		t.Fatalf("grabs = %+v", grabs)
	}
	list, err := st.ListGrabs(ctx, now.AddDate(0, 0, -30), 10, 0)
	if err != nil || len(list) != 4 || list[0].IndexerName == "" {
		t.Fatalf("ListGrabs = %+v, %v", list, err)
	}
	if count, err := st.CountGrabs(ctx, time.Time{}); err != nil || count != 5 {
		t.Fatalf("CountGrabs(all) = %d, %v", count, err)
	}
}
