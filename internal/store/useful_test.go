package store

import (
	"context"
	"database/sql"
	"errors"
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
	if _, err := st.RecordIndexerHistory(ctx, IndexerHistory{
		IndexerID: b, Kind: "grab", Status: "ed2k", StartedAt: now,
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
	if len(stats) != 2 || stats[0].IndexerID != a || stats[0].Downloads != 3 || stats[1].Downloads != 2 {
		t.Fatalf("stats = %+v", stats)
	}

	grabs, err := st.GrabStatsByIndexer(ctx, now.AddDate(0, 0, -30))
	if err != nil {
		t.Fatalf("GrabStatsByIndexer: %v", err)
	}
	if len(grabs) != 2 || grabs[0].Torrents != 3 || grabs[1].Magnets != 1 || grabs[1].Ed2k != 1 || grabs[1].Total() != 2 {
		t.Fatalf("grabs = %+v", grabs)
	}
	list, err := st.ListGrabs(ctx, now.AddDate(0, 0, -30), 0, 10, 0)
	if err != nil || len(list) != 5 || list[0].IndexerName == "" {
		t.Fatalf("ListGrabs = %+v, %v", list, err)
	}
	filtered, err := st.ListGrabs(ctx, now.AddDate(0, 0, -30), b, 10, 0)
	if err != nil || len(filtered) != 2 {
		t.Fatalf("ListGrabs(source b) = %+v, %v", filtered, err)
	}
	if count, err := st.CountGrabs(ctx, time.Time{}, 0); err != nil || count != 6 {
		t.Fatalf("CountGrabs(all) = %d, %v", count, err)
	}
	if count, err := st.CountGrabs(ctx, time.Time{}, b); err != nil || count != 3 {
		t.Fatalf("CountGrabs(source b) = %d, %v", count, err)
	}
	sources, err := st.GrabSources(ctx)
	if err != nil || len(sources) != 2 {
		t.Fatalf("GrabSources = %+v, %v", sources, err)
	}
}

// A grab row carries the release it delivered so the Download tab can copy the
// link; the stored magnet is filled only when it was missing.
func TestListGrabsCarriesTheReleaseLink(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	if err := st.UpsertDefinition(ctx, testDefinition("grab-link-def")); err != nil {
		t.Fatalf("UpsertDefinition: %v", err)
	}
	indexerID, err := st.CreateIndexer(ctx, Indexer{
		DefinitionID: "grab-link-def", Slug: "grab-link", Name: "Grab Link", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	release := Release{
		DedupKey: "grab-link", Title: "Linked Show S01E01", Protocol: "torrent",
		FirstSeenAt: time.Now(), LastSeenAt: time.Now(),
	}
	if _, err := st.UpsertRelease(ctx, &release); err != nil {
		t.Fatalf("UpsertRelease: %v", err)
	}
	const magnet = "magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567"
	if err := st.SetReleaseMagnet(ctx, release.ID, magnet); err != nil {
		t.Fatalf("SetReleaseMagnet: %v", err)
	}
	// A second call must not replace the stored link.
	if err := st.SetReleaseMagnet(ctx, release.ID, "magnet:?xt=urn:btih:ffff"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("SetReleaseMagnet second = %v, want sql.ErrNoRows", err)
	}
	if _, err := st.RecordIndexerHistory(ctx, IndexerHistory{
		IndexerID: indexerID, ReleaseID: release.ID, Kind: "grab",
		Status: "magnet", Query: release.Title, StartedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	grabs, err := st.ListGrabs(ctx, time.Time{}, 0, 10, 0)
	if err != nil || len(grabs) != 1 {
		t.Fatalf("ListGrabs = %+v, %v", grabs, err)
	}
	if grabs[0].ReleaseID != release.ID || grabs[0].Magnet != magnet {
		t.Errorf("grab = %+v, want release %d with the magnet", grabs[0], release.ID)
	}
}
