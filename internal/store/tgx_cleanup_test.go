package store

import (
	"context"
	"testing"
	"time"
)

func TestCleanupTorrentGalaxyHashlessKeepsHashedRow(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	mustCreateDefinition(t, st, "torrentgalaxy")
	indexerID := mustCreateIndexer(t, st, "torrentgalaxy", "tgx-one", "TGx One")
	now := time.Now()
	hashless := Release{
		DedupKey: "tgx-hashless", Title: "Same Title 1080p", SearchTitle: "same title 1080p",
		Size: 1500, FirstSeenAt: now, LastSeenAt: now,
	}
	if _, err := st.UpsertRelease(ctx, &hashless); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordReleaseSource(ctx, ReleaseSource{ReleaseID: hashless.ID, IndexerID: indexerID}); err != nil {
		t.Fatal(err)
	}
	hashed := Release{
		DedupKey: "tgx-hashed", InfoHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Title: "Same Title 1080p", SearchTitle: "same title 1080p",
		Size: 1500, FirstSeenAt: now, LastSeenAt: now,
	}
	if _, err := st.UpsertRelease(ctx, &hashed); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordReleaseSource(ctx, ReleaseSource{ReleaseID: hashed.ID, IndexerID: indexerID}); err != nil {
		t.Fatal(err)
	}

	n, err := st.CleanupTorrentGalaxyHashless(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("removed = %d, want 1", n)
	}
	if _, found, err := st.ReleaseByKey(ctx, "tgx-hashless"); err != nil || found {
		t.Fatalf("hashless row survived: found=%v err=%v", found, err)
	}
	if _, found, err := st.ReleaseByKey(ctx, "tgx-hashed"); err != nil || !found {
		t.Fatalf("hashed row missing: found=%v err=%v", found, err)
	}
	if n, err := st.CleanupTorrentGalaxyHashless(ctx); err != nil || n != 0 {
		t.Fatalf("second cleanup = %d, %v, want 0", n, err)
	}
}
