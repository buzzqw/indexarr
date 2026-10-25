package store

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// TestVisibleCountMatchesFilteredCount pins the cheap visible-release counter
// used to size the text-search prefix against the predicate it replaced: the
// value must equal what "WHERE hidden = 0" counts, whatever the curation
// ratio is, including the empty and the all-hidden stores.
func TestVisibleCountMatchesFilteredCount(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	assertVisible := func(t *testing.T, want int64) {
		t.Helper()
		var filtered int64
		if err := st.DB().QueryRowContext(ctx,
			`SELECT COUNT(*) FROM releases WHERE hidden = 0`).Scan(&filtered); err != nil {
			t.Fatalf("filtered count: %v", err)
		}
		if filtered != want {
			t.Fatalf("reference count = %d, want %d", filtered, want)
		}
		got, err := st.visibleCount(ctx)
		if err != nil {
			t.Fatalf("visibleCount: %v", err)
		}
		if got != want {
			t.Fatalf("visibleCount = %d, want %d", got, want)
		}
	}

	assertVisible(t, 0)

	const total = 60
	now := time.Now()
	ids := make([]int64, 0, total)
	for i := 0; i < total; i++ {
		title := fmt.Sprintf("Visible Release %d 1080p WEB-DL", i)
		rel := Release{
			DedupKey:    fmt.Sprintf("visible-%d", i),
			Title:       title,
			SearchTitle: NormalizeSearch(title),
			Size:        int64(i+1) * 1_000_000,
			Category:    "2040",
			PublishedAt: now.Add(-time.Duration(i) * time.Minute),
			GUID:        fmt.Sprintf("guid-visible-%d", i),
			Magnet:      fmt.Sprintf("magnet:?xt=urn:btih:%040x", i),
			FirstSeenAt: now,
			LastSeenAt:  now,
		}
		if _, err := st.UpsertRelease(ctx, &rel); err != nil {
			t.Fatalf("UpsertRelease(%d): %v", i, err)
		}
		ids = append(ids, rel.ID)
	}
	assertVisible(t, total)

	for _, hidden := range []int{0, 1, 12, total - 1, total} {
		t.Run(fmt.Sprintf("hidden_%d", hidden), func(t *testing.T) {
			for i, id := range ids {
				if _, err := st.SetReleaseFlags(ctx, id, false, i < hidden); err != nil {
					t.Fatalf("SetReleaseFlags(%d): %v", id, err)
				}
			}
			assertVisible(t, int64(total-hidden))
		})
	}
}
