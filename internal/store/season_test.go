package store

import (
	"context"
	"testing"
	"time"
)

func TestEpisodeVariantsAndSeasonPacks(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	now := time.Now()
	titles := []string{
		"Show S04E01 1080p",
		"Show S04E01E02 1080p",
		"Show 4x01 1080p",
		"Show S04 E01 1080p",
		"Show Season 4 Complete 1080p",
		"Show S04 Complete 1080p",
		"Show S03E01 1080p",
	}
	for i, title := range titles {
		rel := Release{
			DedupKey:    title,
			Title:       title,
			SearchTitle: NormalizeSearch(title),
			Category:    "5040",
			FirstSeenAt: now,
			LastSeenAt:  now,
		}
		if _, err := st.UpsertRelease(ctx, &rel); err != nil {
			t.Fatalf("upsert %d: %v", i, err)
		}
	}

	releases, total, err := st.SearchReleases(ctx, Filter{Season: 4, Episode: 1})
	if err != nil {
		t.Fatal(err)
	}
	if total != 6 || len(releases) != 6 {
		t.Fatalf("season 4 episode 1 = %d/%d, want 6/6", total, len(releases))
	}
	for _, rel := range releases {
		if rel.Title == "Show S03E01 1080p" {
			t.Error("season 3 release leaked into season 4 results")
		}
	}

	_, total, err = st.SearchReleases(ctx, Filter{Season: 4})
	if err != nil || total != 6 {
		t.Fatalf("season 4 = %d, %v", total, err)
	}
}
