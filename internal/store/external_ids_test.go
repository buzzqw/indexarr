package store

import (
	"context"
	"testing"
	"time"
)

func TestTitleForExternalID(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	release := Release{
		DedupKey:    "id-title",
		Title:       "Series A S01E01 1080p ITA-ENG X265-GROUP.mkv",
		SearchTitle: NormalizeSearch("Series A S01E01 1080p ITA-ENG X265-GROUP.mkv"),
		IMDbID:      "1234567",
		FirstSeenAt: time.Now().Add(-time.Hour),
		LastSeenAt:  time.Now().Add(-time.Hour),
	}
	if _, err := st.UpsertRelease(ctx, &release); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name       string
		imdb, tmdb string
		want       string
		ok         bool
	}{
		{"imdb", "tt1234567", "", "Series A S01E01 1080p ITA-ENG X265-GROUP.mkv", true},
		{"imdb with prefix", "  TT1234567 ", "", "Series A S01E01 1080p ITA-ENG X265-GROUP.mkv", true},
		{"unknown id", "tt9999999", "", "", false},
		{"no id", "", "", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok, err := st.TitleForExternalID(ctx, tc.imdb, tc.tmdb, "")
			if err != nil {
				t.Fatal(err)
			}
			if ok != tc.ok || got != tc.want {
				t.Errorf("TitleForExternalID = %q, %v; want %q, %v", got, ok, tc.want, tc.ok)
			}
		})
	}
}
