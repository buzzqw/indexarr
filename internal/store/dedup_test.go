package store

import (
	"context"
	"testing"
	"time"
)

func dedupFixture(t *testing.T, st *Store) (indexerIDs []int64) {
	t.Helper()
	ctx := context.Background()
	for i, slug := range []string{"tgx", "extto"} {
		_, err := st.DB().ExecContext(ctx, `
			INSERT INTO definitions(id, body, hash) VALUES(?, 'x', 'h')`, "def-"+slug)
		if err != nil {
			t.Fatalf("definition %s: %v", slug, err)
		}
		res, err := st.DB().ExecContext(ctx, `
			INSERT INTO indexers(definition_id, protocol, slug, name, priority)
			VALUES(?, 'torrent', ?, ?, ?)`, "def-"+slug, slug, slug, 10+i)
		if err != nil {
			t.Fatalf("indexer %s: %v", slug, err)
		}
		id, _ := res.LastInsertId()
		indexerIDs = append(indexerIDs, id)
	}
	return indexerIDs
}

func addFixtureRelease(t *testing.T, st *Store, key, title, hash, source string, size int64, favorite bool) Release {
	t.Helper()
	now := time.Now()
	rel := Release{
		DedupKey: key, InfoHash: hash, Title: title, SearchTitle: NormalizeSearch(title),
		Size: size, Seeders: 10, Category: "2000", PublishedAt: now,
		FirstSeenAt: now, LastSeenAt: now, Favorite: favorite,
		Magnet: "magnet:?xt=urn:btih:" + key,
	}
	if _, err := st.UpsertRelease(context.Background(), &rel); err != nil {
		t.Fatalf("UpsertRelease(%s): %v", key, err)
	}
	return rel
}

func TestMergeDuplicatesKeepsEverySourceAndFavorite(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	ids := dedupFixture(t, st)
	titleA := "The.Movie.2024.1080p.WEB-DL.DDP5.1.H.264-GROUP"
	titleB := "The Movie 2024 1080p WEB DL DDP5 1 H.264 GROUP"
	a := addFixtureRelease(t, st, "key-a", titleA, "", "tgx", 4_300_000_000, false)
	if _, err := st.DB().ExecContext(ctx, `UPDATE releases SET favorite = 1 WHERE id = ?`, a.ID); err != nil {
		t.Fatal(err)
	}
	// Force a second row: UpsertRelease now merges hashless duplicates itself,
	// so the cleanup job is tested against a row inserted directly.
	now := time.Now().Unix()
	if _, err := st.DB().ExecContext(ctx, `
		INSERT INTO releases(dedup_key, title, search_title, size, first_seen_at, last_seen_at, favorite)
		VALUES('key-b', ?, ?, ?, ?, ?, 0)`, titleB, NormalizeSearch(titleB), 4_300_000_000, now, now); err != nil {
		t.Fatalf("insert duplicate: %v", err)
	}
	var bID int64
	if err := st.DB().QueryRowContext(ctx, `SELECT id FROM releases WHERE dedup_key='key-b'`).Scan(&bID); err != nil {
		t.Fatal(err)
	}
	for i, id := range []int64{a.ID, bID} {
		if err := st.RecordReleaseSource(ctx, ReleaseSource{
			ReleaseID: id, IndexerID: ids[i], Seeders: int64(20 + i),
			DownloadRef: "ref-" + string(rune('a'+i)),
		}); err != nil {
			t.Fatalf("RecordReleaseSource: %v", err)
		}
	}

	stats, err := st.MergeDuplicates(ctx)
	if err != nil {
		t.Fatalf("MergeDuplicates: %v", err)
	}
	if stats.Removed != 1 || stats.After != 1 {
		t.Fatalf("stats = %#v, want one removed", stats)
	}
	var count int
	if err := st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM releases`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("releases = %d, want 1", count)
	}
	var favorite int
	if err := st.DB().QueryRowContext(ctx, `SELECT favorite FROM releases`).Scan(&favorite); err != nil {
		t.Fatal(err)
	}
	if favorite != 1 {
		t.Error("favorite was lost by the merge")
	}
	sources, err := st.ReleaseSourcesFor(ctx, a.ID)
	if err != nil {
		t.Fatalf("ReleaseSourcesFor: %v", err)
	}
	if len(sources) != 2 {
		t.Fatalf("sources = %d, want 2", len(sources))
	}
	for _, source := range sources {
		if source.DownloadRef == "" {
			t.Errorf("source %d lost its download reference", source.IndexerID)
		}
	}

	again, err := st.MergeDuplicates(ctx)
	if err != nil {
		t.Fatalf("second MergeDuplicates: %v", err)
	}
	if again.Removed != 0 {
		t.Errorf("second pass removed %d rows, want 0", again.Removed)
	}
}

func TestMergeDuplicatesNeverMergesDifferentTorrents(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	ids := dedupFixture(t, st)
	addFixtureRelease(t, st, "hash-a", "Movie.2024.1080p.WEB-DL.x264-GRP", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "", 4_000_000_000, false)
	addFixtureRelease(t, st, "hash-b", "Movie.2024.1080p.WEB-DL.x264-GRP", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "", 4_000_000_000, false)
	stats, err := st.MergeDuplicates(ctx)
	if err != nil {
		t.Fatalf("MergeDuplicates: %v", err)
	}
	if stats.Removed != 0 {
		t.Fatalf("removed %d rows for two different torrents", stats.Removed)
	}
	var count int
	if err := st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM releases`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("releases = %d, want 2", count)
	}
	_ = ids
}

func TestSetReleaseInfoHashMergesIntoExistingHashRow(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	ids := dedupFixture(t, st)
	hashed := addFixtureRelease(t, st, "hashed", "Show.S01E01.1080p.WEB-DL.x264-GRP", "cccccccccccccccccccccccccccccccccccccccc", "", 1_500_000_000, false)
	if err := st.RecordReleaseSource(ctx, ReleaseSource{ReleaseID: hashed.ID, IndexerID: ids[0]}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	if _, err := st.DB().ExecContext(ctx, `
		INSERT INTO releases(dedup_key, title, search_title, size, first_seen_at, last_seen_at)
		VALUES('nohash', 'Show S01E01 1080p WEB DL x264 GRP', ?, ?, ?, ?)`,
		NormalizeSearch("Show S01E01 1080p WEB DL x264 GRP"), 1_500_000_000, now, now); err != nil {
		t.Fatal(err)
	}
	var noHashID int64
	if err := st.DB().QueryRowContext(ctx, `SELECT id FROM releases WHERE dedup_key='nohash'`).Scan(&noHashID); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordReleaseSource(ctx, ReleaseSource{ReleaseID: noHashID, IndexerID: ids[1]}); err != nil {
		t.Fatal(err)
	}
	keeper, merged, err := st.SetReleaseInfoHash(ctx, noHashID, "cccccccccccccccccccccccccccccccccccccccc")
	if err != nil {
		t.Fatalf("SetReleaseInfoHash: %v", err)
	}
	if !merged || keeper != hashed.ID {
		t.Fatalf("merged=%v keeper=%d, want %d", merged, keeper, hashed.ID)
	}
	sources, err := st.ReleaseSourcesFor(ctx, hashed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 2 {
		t.Fatalf("sources = %d, want 2", len(sources))
	}
}

func TestFuseReleasesKeepsOneRowAndStrongestIdentity(t *testing.T) {
	now := time.Now()
	hashless := Release{ID: 1, Title: "Show.S01E01.1080p.WEB-DL.x264-GRP", SearchTitle: NormalizeSearch("Show.S01E01.1080p.WEB-DL.x264-GRP"), Size: 1_500_000_000, Magnet: "magnet:?xt=urn:btih:nohash", Seeders: 30, FirstSeenAt: now, LastSeenAt: now}
	hashed := Release{ID: 2, Title: "Show S01E01 1080p WEB DL x264 GRP", SearchTitle: NormalizeSearch("Show S01E01 1080p WEB DL x264 GRP"), Size: 1_500_000_000, InfoHash: "dddddddddddddddddddddddddddddddddddddddd", Seeders: 5, FirstSeenAt: now, LastSeenAt: now}
	fused := FuseReleases([]Release{hashless, hashed})
	if len(fused) != 1 {
		t.Fatalf("fused rows = %d, want 1", len(fused))
	}
	if fused[0].InfoHash == "" {
		t.Fatal("fused row lost the infohash")
	}
	if fused[0].Seeders != 30 {
		t.Fatalf("seeders = %d, want the healthiest copy's 30", fused[0].Seeders)
	}
}
