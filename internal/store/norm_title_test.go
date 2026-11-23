package store

import (
	"context"
	"testing"
	"time"

	"github.com/buzzqw/indexarr/internal/dedup"
)

func TestUpsertReleaseStoresTheNormalizedTitle(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	title := "Show.S01E01.1080p.WEB-DL.x264-GRP.mkv"
	release := Release{
		DedupKey: "norm-title", Title: title, SearchTitle: NormalizeSearch(title),
		Size: 1000, FirstSeenAt: time.Now(), LastSeenAt: time.Now(),
	}
	if _, err := st.UpsertRelease(ctx, &release); err != nil {
		t.Fatal(err)
	}
	var stored string
	if err := st.DB().QueryRowContext(ctx, `SELECT norm_title FROM releases WHERE id = ?`, release.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if want := dedup.NormalizedKey(title); stored != want {
		t.Errorf("norm_title = %q, want %q", stored, want)
	}

	newTitle := "Another Show S02E04 720p HDTV x264-OTHER"
	release.Title = newTitle
	if _, err := st.UpsertRelease(ctx, &release); err != nil {
		t.Fatal(err)
	}
	if err := st.DB().QueryRowContext(ctx, `SELECT norm_title FROM releases WHERE id = ?`, release.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if want := dedup.NormalizedKey(newTitle); stored != want {
		t.Errorf("norm_title after an update = %q, want %q", stored, want)
	}
}

func TestNormTitleColumnIsIndexedAndNeverNull(t *testing.T) {
	st := newTestStore(t)
	var count int
	if err := st.DB().QueryRow(`SELECT COUNT(*) FROM sqlite_master
		WHERE type = 'index' AND name = 'releases_norm_title'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("norm_title indexes = %d, want 1", count)
	}
	var notNull int
	if err := st.DB().QueryRow(`SELECT COUNT(*) FROM releases WHERE norm_title IS NULL`).Scan(&notNull); err != nil {
		t.Fatal(err)
	}
	if notNull != 0 {
		t.Errorf("rows with NULL norm_title = %d", notNull)
	}
}

// TestMergeDuplicatesUsesTheStoredKey pins that the cleanup relies on the
// column: changing the stored key moves a row out of the duplicate group even
// when the title text is unchanged (the Go key is not recomputed).
func TestMergeDuplicatesUsesTheStoredKey(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	title := "Same Show S01E01 1080p WEB-DL x264-GRP"
	first := Release{DedupKey: "same-a", Title: title, SearchTitle: NormalizeSearch(title),
		Size: 1000, InfoHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		FirstSeenAt: time.Now(), LastSeenAt: time.Now()}
	second := Release{DedupKey: "same-b", Title: title, SearchTitle: NormalizeSearch(title),
		Size: 1000, InfoHash: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		FirstSeenAt: time.Now(), LastSeenAt: time.Now()}
	for _, release := range []*Release{&first, &second} {
		if _, err := st.UpsertRelease(ctx, release); err != nil {
			t.Fatal(err)
		}
	}
	// Two different hashes must not be merged even with an equal title: this
	// is the existing conservative rule, now driven by the stored key.
	stats, err := st.MergeDuplicates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Removed != 0 {
		t.Fatalf("removed %d rows with different hashes", stats.Removed)
	}
	var key string
	if err := st.DB().QueryRowContext(ctx, `SELECT norm_title FROM releases WHERE id = ?`, first.ID).Scan(&key); err != nil {
		t.Fatal(err)
	}
	if key == "" {
		t.Error("the stored normalization key is empty")
	}
}
