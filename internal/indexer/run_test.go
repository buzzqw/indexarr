package indexer

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/buzzqw/indexarr/internal/feed"
	"github.com/buzzqw/indexarr/internal/store"
)

func TestRunPersistsProvenanceHistoryAndState(t *testing.T) {
	st := newTestStore(t)
	v := newTestVault(t, 1)
	server := newSearchServer(t)
	manager := newTestManager(t, st, v)
	indexerID := seedNative(t, st, v, server.URL, true)
	ctx := context.Background()

	stats, err := manager.Run(ctx, indexerID)
	if err != nil {
		t.Fatalf("first Run: %v", err)
	}
	if stats.Seen != 2 {
		t.Errorf("Seen = %d, want 2", stats.Seen)
	}
	if stats.New != 2 {
		t.Errorf("New = %d, want 2", stats.New)
	}
	if stats.Updated != 0 {
		t.Errorf("Updated = %d, want 0", stats.Updated)
	}
	if stats.Torrents != 1 {
		t.Errorf("Torrents = %d, want 1", stats.Torrents)
	}
	if stats.Blocked != 0 || stats.Skipped != 0 {
		t.Errorf("Blocked = %d, Skipped = %d", stats.Blocked, stats.Skipped)
	}
	if len(stats.Items) != 2 {
		t.Errorf("Items = %d, want 2", len(stats.Items))
	}

	releases, total, err := st.SearchReleases(ctx, store.Filter{Limit: 10})
	if err != nil {
		t.Fatalf("SearchReleases: %v", err)
	}
	if total != 2 {
		t.Fatalf("releases = %d, want 2", total)
	}
	for _, release := range releases {
		if release.Category != "5040" {
			t.Errorf("release %d category = %q", release.ID, release.Category)
		}
		if release.SearchTitle == "" {
			t.Errorf("release %d has no search title", release.ID)
		}
		sources, err := st.ListReleaseSources(ctx, release.ID)
		if err != nil {
			t.Fatalf("ListReleaseSources: %v", err)
		}
		if len(sources) != 1 || sources[0].IndexerID != indexerID {
			t.Errorf("release %d sources = %+v", release.ID, sources)
		}
		if sources[0].DownloadRef != "" {
			t.Errorf("release %d download ref = %q, want empty", release.ID, sources[0].DownloadRef)
		}
	}

	hashRelease, ok, err := st.ReleaseByInfoHash(ctx, hashA)
	if err != nil || !ok {
		t.Fatalf("ReleaseByInfoHash(%s): ok=%t err=%v", hashA, ok, err)
	}
	if hashRelease.Magnet == "" {
		t.Error("infohash release has no magnet")
	}
	if len(hashRelease.Torrent) != 0 {
		t.Errorf("infohash release stored %d torrent bytes, want 0", len(hashRelease.Torrent))
	}

	history, err := st.ListIndexerHistory(ctx, indexerID, 10)
	if err != nil {
		t.Fatalf("ListIndexerHistory: %v", err)
	}
	if len(history) == 0 {
		t.Fatal("no history rows")
	}
	runRow := history[0]
	if runRow.Kind != "run" || runRow.Status != "ok" {
		t.Fatalf("run history = %+v", runRow)
	}
	if runRow.Query != "ubuntu" {
		t.Errorf("run query = %q", runRow.Query)
	}
	if runRow.Bytes != int64(len(torrentBody)) {
		t.Errorf("run bytes = %d, want %d", runRow.Bytes, len(torrentBody))
	}

	state, err := st.GetIndexerState(ctx, indexerID)
	if err != nil {
		t.Fatalf("GetIndexerState: %v", err)
	}
	if state.LastError != "" {
		t.Errorf("last error = %q", state.LastError)
	}
	if state.UpdatedAt.IsZero() {
		t.Error("state updated_at is zero")
	}
	if state.Circuit != "closed" {
		t.Errorf("circuit = %q, want closed", state.Circuit)
	}

	second, err := manager.Run(ctx, indexerID)
	if err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if second.New != 0 || second.Updated != 2 {
		t.Errorf("second run New = %d, Updated = %d", second.New, second.Updated)
	}
	_, total, err = st.SearchReleases(ctx, store.Filter{Limit: 10})
	if err != nil {
		t.Fatalf("SearchReleases after second run: %v", err)
	}
	if total != 2 {
		t.Errorf("releases after second run = %d, want 2", total)
	}
}

func TestRunFetchesTorrentForDownloadOnlyItem(t *testing.T) {
	st := newTestStore(t)
	v := newTestVault(t, 1)
	server := newSearchServer(t)
	manager := newTestManager(t, st, v)
	indexerID := seedNative(t, st, v, server.URL, true)
	ctx := context.Background()

	if _, err := manager.Run(ctx, indexerID); err != nil {
		t.Fatalf("Run: %v", err)
	}

	expected, err := feed.InfoHash(torrentBody)
	if err != nil {
		t.Fatalf("feed.InfoHash: %v", err)
	}
	release, ok, err := st.ReleaseByInfoHash(ctx, expected)
	if err != nil || !ok {
		t.Fatalf("ReleaseByInfoHash(%s): ok=%t err=%v", expected, ok, err)
	}
	if len(release.Torrent) != len(torrentBody) {
		t.Fatalf("torrent bytes = %d, want %d", len(release.Torrent), len(torrentBody))
	}
	if string(release.Torrent) != string(torrentBody) {
		t.Errorf("stored torrent differs from the downloaded one")
	}
	if release.TorrentFetchedAt.IsZero() {
		t.Error("torrent_fetched_at is zero")
	}
	if release.Magnet == "" {
		t.Error("download-only release has no magnet")
	}
	if release.Size != 2_000_000_000 {
		t.Errorf("size = %d, want 2 GB", release.Size)
	}
}

func TestRunBlocklistPreventsPersistence(t *testing.T) {
	st := newTestStore(t)
	v := newTestVault(t, 1)
	server := newSearchServer(t)
	manager := newTestManager(t, st, v)
	indexerID := seedNative(t, st, v, server.URL, true)
	ctx := context.Background()

	if _, err := st.AddBlocklistRule(ctx, store.BlockKindInfohash, hashA, "test rule"); err != nil {
		t.Fatalf("AddBlocklistRule: %v", err)
	}

	stats, err := manager.Run(ctx, indexerID)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.Blocked != 1 {
		t.Errorf("Blocked = %d, want 1", stats.Blocked)
	}
	if stats.New != 1 {
		t.Errorf("New = %d, want 1", stats.New)
	}

	releases, total, err := st.SearchReleases(ctx, store.Filter{Limit: 10})
	if err != nil {
		t.Fatalf("SearchReleases: %v", err)
	}
	if total != 1 {
		t.Fatalf("releases = %d, want 1", total)
	}
	if releases[0].InfoHash == hashA {
		t.Fatal("blocked release was persisted")
	}
}

func TestRunCanceledRecordsCanceled(t *testing.T) {
	st := newTestStore(t)
	v := newTestVault(t, 1)
	server := newSearchServer(t)
	manager := newTestManager(t, st, v)
	indexerID := seedNative(t, st, v, server.URL, true)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	started := time.Now()
	stats, err := manager.Run(ctx, indexerID)
	elapsed := time.Since(started)
	if stats == nil {
		t.Fatal("Run returned nil stats")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want context.Canceled", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("Run took %s after cancellation", elapsed)
	}

	history, historyErr := st.ListIndexerHistory(context.Background(), indexerID, 10)
	if historyErr != nil {
		t.Fatalf("ListIndexerHistory: %v", historyErr)
	}
	if len(history) == 0 {
		t.Fatal("canceled run wrote no history")
	}
	if history[0].Kind != "run" || history[0].Status != "canceled" {
		t.Fatalf("history[0] = %+v", history[0])
	}

	state, stateErr := st.GetIndexerState(context.Background(), indexerID)
	if stateErr != nil {
		t.Fatalf("GetIndexerState: %v", stateErr)
	}
	if state.Circuit != "closed" {
		t.Errorf("circuit = %q, want closed", state.Circuit)
	}
}
