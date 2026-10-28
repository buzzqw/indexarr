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
		"Show S04 E02 1080p",
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
	if err != nil || total != 7 {
		t.Fatalf("season 4 = %d, %v", total, err)
	}
}

func TestFreeTextEpisodeTokenMatchesEveryEpisodeSpelling(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	now := time.Now()
	for i, title := range []string{
		"Bones S06E12 1080p",
		"Bones - 6x12 - L'amore ha un cuore solo.mkv",
		"Bones S06 E12 720p",
		"Bones S06E13 1080p",
		"Other Show S06E12 1080p",
	} {
		rel := Release{DedupKey: title, Title: title, SearchTitle: NormalizeSearch(title),
			Category: "5040", FirstSeenAt: now, LastSeenAt: now}
		if _, err := st.UpsertRelease(ctx, &rel); err != nil {
			t.Fatalf("upsert %d: %v", i, err)
		}
	}
	for _, query := range []string{"Bones S06E12", "Bones 6x12"} {
		releases, total, err := st.SearchReleases(ctx, Filter{Query: query})
		if err != nil {
			t.Fatal(err)
		}
		if total != 3 || len(releases) != 3 {
			var got []string
			for _, rel := range releases {
				got = append(got, rel.Title)
			}
			t.Errorf("%q = %d %v, want the three spellings of Bones 6x12", query, total, got)
		}
	}
	// Gextto's episode search sends the token in q and season/ep too.
	if _, total, err := st.SearchReleases(ctx, Filter{Query: "Bones S06E12", Season: 6, Episode: 12}); err != nil || total != 3 {
		t.Errorf("Bones S06E12 with season/ep = %d, %v; want 3", total, err)
	}
	// A token that disagrees with season/ep stays text: nothing is invented.
	if _, total, err := st.SearchReleases(ctx, Filter{Query: "Bones S06E12", Season: 6, Episode: 13}); err != nil || total != 0 {
		t.Errorf("Bones S06E12 with ep 13 = %d, %v; want 0", total, err)
	}
	// A bare episode token has no title to scope it: it stays a text search.
	if _, total, err := st.SearchReleases(ctx, Filter{Query: "S06E12"}); err != nil || total != 2 {
		t.Errorf("bare S06E12 = %d, %v; want the two literal matches", total, err)
	}
}

func TestSplitEpisodeToken(t *testing.T) {
	tokens, season, episode := splitEpisodeToken([]string{"bones", "s06e12"})
	if len(tokens) != 1 || tokens[0] != "bones" || season != 6 || episode != 12 {
		t.Errorf("got %v %d %d", tokens, season, episode)
	}
	if tokens, season, _ := splitEpisodeToken([]string{"show", "s01e01", "s01e02"}); len(tokens) != 3 || season != 0 {
		t.Errorf("two episode tokens must stay text: %v %d", tokens, season)
	}
}

// A marker matches only at the start of a word: "1x01" is not "11x01" and
// "Season 1" is not "Season 12".
func TestSeasonMarkersDoNotMatchInsideALongerNumber(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	now := time.Now()
	titles := []string{
		"I misteri di Murdoch 1x01 Quale futuro",
		"I misteri di Murdoch 11x01 Risorto dalle ceneri",
		"I misteri di Murdoch 21x01 Altro",
		"I misteri di Murdoch Season 1 Complete",
		"I misteri di Murdoch Season 12 Complete",
		"I misteri di Murdoch S01 Complete",
	}
	for i, title := range titles {
		rel := Release{DedupKey: title, Title: title, SearchTitle: NormalizeSearch(title), Category: "5030", FirstSeenAt: now, LastSeenAt: now}
		if _, err := st.UpsertRelease(ctx, &rel); err != nil {
			t.Fatalf("upsert %d: %v", i, err)
		}
	}
	for _, tc := range []struct {
		filter Filter
		want   []string
	}{
		{Filter{Season: 1, Episode: 1}, []string{"I misteri di Murdoch 1x01 Quale futuro", "I misteri di Murdoch Season 1 Complete", "I misteri di Murdoch S01 Complete"}},
		{Filter{Season: 1}, []string{"I misteri di Murdoch 1x01 Quale futuro", "I misteri di Murdoch Season 1 Complete", "I misteri di Murdoch S01 Complete"}},
		{Filter{Season: 11, Episode: 1}, []string{"I misteri di Murdoch 11x01 Risorto dalle ceneri"}},
	} {
		releases, _, err := st.SearchReleases(ctx, tc.filter)
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]bool{}
		for _, rel := range releases {
			got[rel.Title] = true
		}
		if len(got) != len(tc.want) {
			t.Errorf("season %d episode %d = %v, want %v", tc.filter.Season, tc.filter.Episode, got, tc.want)
			continue
		}
		for _, title := range tc.want {
			if !got[title] {
				t.Errorf("season %d episode %d misses %q (got %v)", tc.filter.Season, tc.filter.Episode, title, got)
			}
		}
	}
}
