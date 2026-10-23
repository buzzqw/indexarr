package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestDBReportAndChecks(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	sourceID, err := st.CreateSource(ctx, testSource("Report"))
	if err != nil {
		t.Fatalf("CreateSource: %v", err)
	}
	now := time.Now()
	releases := []Release{
		{
			DedupKey: "with-torrent", Title: "With torrent", SearchTitle: "with torrent",
			Torrent: []byte("d4ta"), SourceID: sourceID, SourceName: "Report",
			FirstSeenAt: now, LastSeenAt: now,
		},
		{
			DedupKey: "with-magnet", Title: "With magnet", SearchTitle: "with magnet",
			Magnet:   "magnet:?xt=urn:btih:" + strings.Repeat("a", 40),
			SourceID: sourceID, SourceName: "Report",
			FirstSeenAt: now, LastSeenAt: now,
		},
		{
			DedupKey: "dead", Title: "Dead", SearchTitle: "dead",
			SourceID: sourceID, SourceName: "Report",
			FirstSeenAt: now, LastSeenAt: now,
		},
	}
	for i := range releases {
		if _, err := st.UpsertRelease(ctx, &releases[i]); err != nil {
			t.Fatalf("UpsertRelease: %v", err)
		}
	}
	if _, err := st.SetReleaseFlags(ctx, releases[2].ID, false, true); err != nil {
		t.Fatalf("SetReleaseFlags: %v", err)
	}

	report, err := st.LoadDBReport(ctx)
	if err != nil {
		t.Fatalf("LoadDBReport: %v", err)
	}
	if report.PageSize <= 0 || report.PageCount <= 0 {
		t.Errorf("page info = size %d, count %d", report.PageSize, report.PageCount)
	}
	if report.FreeBytes() < 0 {
		t.Errorf("free bytes = %d", report.FreeBytes())
	}
	if report.JournalMode != "wal" {
		t.Errorf("journal mode = %q", report.JournalMode)
	}
	if report.Releases != 3 || report.Sources != 1 {
		t.Errorf("row counts = %#v", report)
	}
	if report.ReleasesTorrent != 1 || report.ReleasesMagnet != 1 {
		t.Errorf("link counts = %#v", report)
	}
	if report.ReleasesNoLink != 1 {
		t.Errorf("releases without links = %d", report.ReleasesNoLink)
	}
	if report.Hidden != 1 || report.Favorites != 0 {
		t.Errorf("flags = hidden %d, favorites %d", report.Hidden, report.Favorites)
	}

	if verdict, err := st.QuickCheck(ctx); err != nil || !strings.EqualFold(verdict, "ok") {
		t.Fatalf("QuickCheck = %q, %v", verdict, err)
	}
	if violations, err := st.ForeignKeyCheck(ctx); err != nil || violations != 0 {
		t.Fatalf("ForeignKeyCheck = %d, %v", violations, err)
	}
	if err := st.Analyze(ctx); err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	busy, _, _, err := st.Checkpoint(ctx)
	if err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	if busy != 0 {
		t.Errorf("checkpoint busy = %d", busy)
	}
}

func TestMaintenanceCleanup(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	a, err := st.CreateSource(ctx, testSource("Source A"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.CreateSource(ctx, testSource("Source B"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	old := now.AddDate(0, 0, -10)
	insert := func(rel Release) Release {
		t.Helper()
		if _, err := st.UpsertRelease(ctx, &rel); err != nil {
			t.Fatalf("UpsertRelease: %v", err)
		}
		return rel
	}
	insert(Release{
		DedupKey: "a-magnet", Title: "A magnet", SearchTitle: "a magnet",
		Magnet:   "magnet:?xt=urn:btih:" + strings.Repeat("b", 40),
		SourceID: a, SourceName: "Source A", FirstSeenAt: now, LastSeenAt: now,
	})
	insert(Release{
		DedupKey: "a-dead", Title: "A dead", SearchTitle: "a dead",
		SourceID: a, SourceName: "Source A", FirstSeenAt: now, LastSeenAt: now,
	})
	hidden := insert(Release{
		DedupKey: "b-hidden", Title: "B hidden", SearchTitle: "b hidden",
		Magnet:   "magnet:?xt=urn:btih:" + strings.Repeat("c", 40),
		SourceID: b, SourceName: "Source B", FirstSeenAt: now, LastSeenAt: now,
	})
	if _, err := st.SetReleaseFlags(ctx, hidden.ID, false, true); err != nil {
		t.Fatal(err)
	}
	insert(Release{
		DedupKey: "b-torrent", Title: "B torrent", SearchTitle: "b torrent",
		Torrent: []byte("x"), SourceID: b, SourceName: "Source B",
		FirstSeenAt: now, LastSeenAt: now,
	})
	insert(Release{
		DedupKey: "a-old", Title: "A old", SearchTitle: "a old",
		Magnet:   "magnet:?xt=urn:btih:" + strings.Repeat("d", 40),
		SourceID: a, SourceName: "Source A", FirstSeenAt: old, LastSeenAt: old,
	})

	if n, err := st.DeleteReleasesWithoutLinks(ctx); err != nil || n != 1 {
		t.Fatalf("DeleteReleasesWithoutLinks = %d, %v", n, err)
	}
	if n, err := st.DeleteHiddenReleases(ctx); err != nil || n != 1 {
		t.Fatalf("DeleteHiddenReleases = %d, %v", n, err)
	}
	if n, err := st.PruneReleases(ctx, now.AddDate(0, 0, -5)); err != nil || n != 1 {
		t.Fatalf("PruneReleases = %d, %v", n, err)
	}
	if n, err := st.DeleteReleasesBySource(ctx, a); err != nil || n != 1 {
		t.Fatalf("DeleteReleasesBySource = %d, %v", n, err)
	}
	if _, err := st.DeleteReleasesBySource(ctx, 0); err == nil {
		t.Error("DeleteReleasesBySource(0) must fail")
	}

	if _, err := st.DB().ExecContext(ctx, `UPDATE sources SET total_items = 999`); err != nil {
		t.Fatal(err)
	}
	if n, err := st.RecalculateSourceCounts(ctx); err != nil || n != 2 {
		t.Fatalf("RecalculateSourceCounts = %d, %v", n, err)
	}
	sourceA, _ := st.GetSource(ctx, a)
	sourceB, _ := st.GetSource(ctx, b)
	if sourceA.TotalItems != 0 || sourceB.TotalItems != 1 {
		t.Errorf("recounted totals = A %d, B %d", sourceA.TotalItems, sourceB.TotalItems)
	}

	key, err := st.AddAPIKey(ctx, "QA client")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().ExecContext(ctx,
		`UPDATE api_keys SET requests = 8, last_used_at = ? WHERE id = ?`, now.Unix(), key.ID); err != nil {
		t.Fatal(err)
	}
	if n, err := st.ResetAPIUsage(ctx); err != nil || n != 1 {
		t.Fatalf("ResetAPIUsage = %d, %v", n, err)
	}
	keys, err := st.ListAPIKeys(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range keys {
		if key.Requests != 0 || !key.LastUsedAt.IsZero() {
			t.Errorf("API key after reset = %#v", key)
		}
	}
}

func TestForeignKeyCheckDetectsOrphans(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	if _, err := st.DB().ExecContext(ctx, "PRAGMA foreign_keys=OFF"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().ExecContext(ctx,
		`INSERT INTO releases(dedup_key, title, source_id) VALUES('orphan', 'Orphan', 9999)`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().ExecContext(ctx, "PRAGMA foreign_keys=ON"); err != nil {
		t.Fatal(err)
	}
	violations, err := st.ForeignKeyCheck(ctx)
	if err != nil {
		t.Fatalf("ForeignKeyCheck: %v", err)
	}
	if violations != 1 {
		t.Errorf("violations = %d, want 1", violations)
	}
}

func TestMaintenanceHelpers(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	id, err := st.CreateSource(ctx, testSource("Broken"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().ExecContext(ctx,
		`UPDATE sources SET last_error = 'boom', consecutive_errors = 3 WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	reset, err := st.ResetSourceErrors(ctx)
	if err != nil || reset != 1 {
		t.Fatalf("ResetSourceErrors = %d, %v", reset, err)
	}
	source, err := st.GetSource(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if source.LastError != "" || source.ConsecutiveErrors != 0 {
		t.Errorf("source after reset = %#v", source)
	}
	if err := st.Optimize(ctx); err != nil {
		t.Fatalf("Optimize: %v", err)
	}
	if err := st.Reindex(ctx); err != nil {
		t.Fatalf("Reindex: %v", err)
	}
}
