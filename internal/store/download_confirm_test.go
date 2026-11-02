package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestConfirmDownloadMarksTheGrab(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	mustCreateDefinition(t, st, "confirm-def")
	indexerID := mustCreateIndexer(t, st, "confirm-def", "confirm-one", "Confirm One")

	release := Release{
		DedupKey: "confirm-torrent",
		Title:    "Confirm Show S01E01",
		InfoHash: "0123456789abcdef0123456789abcdef01234567",
		Protocol: "torrent",
	}
	if _, err := st.UpsertRelease(ctx, &release); err != nil {
		t.Fatalf("UpsertRelease: %v", err)
	}
	started := time.Now().Add(-2 * time.Minute)
	grabID, err := st.RecordIndexerHistory(ctx, IndexerHistory{
		IndexerID: indexerID, ReleaseID: release.ID, Kind: "grab",
		Query: release.Title, Status: "magnet", App: "gextto", StartedAt: started,
	})
	if err != nil {
		t.Fatalf("RecordIndexerHistory: %v", err)
	}

	matched, err := st.ConfirmDownload(ctx, DownloadConfirmation{
		Protocol:    "torrent",
		Hash:        strings.ToUpper(release.InfoHash),
		Title:       release.Title,
		Client:      "gextto",
		CompletedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("ConfirmDownload: %v", err)
	}
	if matched != grabID {
		t.Fatalf("confirmed grab = %d, want %d", matched, grabID)
	}

	grabs, err := st.ListGrabs(ctx, time.Now().Add(-time.Hour), 0, 50, 0)
	if err != nil {
		t.Fatalf("ListGrabs: %v", err)
	}
	if len(grabs) != 1 || grabs[0].ConfirmedAt.IsZero() || grabs[0].ConfirmedBy != "gextto" {
		t.Fatalf("grab = %+v, want a confirmation from gextto", grabs)
	}
	stats, err := st.GrabStatsByIndexer(ctx, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("GrabStatsByIndexer: %v", err)
	}
	if len(stats) != 1 || stats[0].Total() != 1 || stats[0].Confirmed != 1 {
		t.Fatalf("stats = %+v, want 1 total and 1 confirmed", stats)
	}

	// A second report is idempotent and does not touch an already confirmed row.
	again, err := st.ConfirmDownload(ctx, DownloadConfirmation{
		Protocol: "torrent", Hash: release.InfoHash, Client: "gextto",
	})
	if err != nil {
		t.Fatalf("second ConfirmDownload: %v", err)
	}
	if again != 0 {
		t.Fatalf("second confirmation matched grab %d, want none", again)
	}
}

func TestConfirmDownloadUsesTheNewestGrabAndTheTitleFallback(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	mustCreateDefinition(t, st, "confirm-title-def")
	indexerID := mustCreateIndexer(t, st, "confirm-title-def", "confirm-two", "Confirm Two")

	release := Release{
		DedupKey: "confirm-title",
		Title:    "Confirm Show S02E01",
		Protocol: "torrent",
	}
	if _, err := st.UpsertRelease(ctx, &release); err != nil {
		t.Fatalf("UpsertRelease: %v", err)
	}
	if err := st.MarkReleaseDownloaded(ctx, release.ID); err != nil {
		t.Fatalf("MarkReleaseDownloaded: %v", err)
	}
	oldGrab, err := st.RecordIndexerHistory(ctx, IndexerHistory{
		IndexerID: indexerID, ReleaseID: release.ID, Kind: "grab",
		Query: release.Title, Status: "ok", App: "sonarr", StartedAt: time.Now().Add(-3 * time.Hour),
	})
	if err != nil {
		t.Fatalf("old RecordIndexerHistory: %v", err)
	}
	newGrab, err := st.RecordIndexerHistory(ctx, IndexerHistory{
		IndexerID: indexerID, ReleaseID: release.ID, Kind: "grab",
		Query: release.Title, Status: "ok", App: "sonarr", StartedAt: time.Now().Add(-time.Hour),
	})
	if err != nil {
		t.Fatalf("new RecordIndexerHistory: %v", err)
	}

	// The hash is unknown to indexarr: the title has to find the release and the
	// confirmation stores the hash for the next report.
	hash := "abcdefabcdefabcdefabcdefabcdefabcdefabcd"
	matched, err := st.ConfirmDownload(ctx, DownloadConfirmation{
		Protocol: "torrent", Hash: hash, Title: release.Title,
		Client: "gextto", CompletedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("ConfirmDownload: %v", err)
	}
	if matched != newGrab {
		t.Fatalf("confirmed grab = %d, want the newest %d (old was %d)", matched, newGrab, oldGrab)
	}
	stored, found, err := st.ReleaseByInfoHash(ctx, hash)
	if err != nil {
		t.Fatalf("ReleaseByInfoHash: %v", err)
	}
	if !found || stored.ID != release.ID {
		t.Fatalf("release by hash = %+v, found %v, want id %d", stored, found, release.ID)
	}
	grabs, err := st.ListGrabs(ctx, time.Now().Add(-6*time.Hour), 0, 50, 0)
	if err != nil {
		t.Fatalf("ListGrabs: %v", err)
	}
	for _, grab := range grabs {
		if grab.ID == oldGrab && !grab.ConfirmedAt.IsZero() {
			t.Errorf("the older grab was confirmed too")
		}
		if grab.ID == newGrab && grab.ConfirmedAt.IsZero() {
			t.Errorf("the newest grab was not confirmed")
		}
	}
}
