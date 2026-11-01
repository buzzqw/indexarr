package store

import (
	"context"
	"testing"
	"time"
)

// TestReleaseKeepsTheDeclaredLanguages proves the language list survives the
// round trip and the merge: a source that describes the same release again
// without the field must not erase what another source already said.
func TestReleaseKeepsTheDeclaredLanguages(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	now := time.Unix(1_700_000_000, 0).UTC()
	release := Release{
		DedupKey:    "dedup-languages",
		Title:       "Film.2024.ITA.ENG.1080p",
		SearchTitle: "film 2024 ita eng 1080p",
		Size:        1_000,
		Seeders:     3,
		Category:    "2040",
		Protocol:    "torrent",
		Languages:   "Italian,English",
		FirstSeenAt: now,
		LastSeenAt:  now,
	}
	if _, err := st.UpsertRelease(ctx, &release); err != nil {
		t.Fatalf("UpsertRelease: %v", err)
	}
	stored, ok, err := st.ReleaseByKey(ctx, "dedup-languages")
	if err != nil || !ok {
		t.Fatalf("ReleaseByDedupKey = %+v, %v, %v", stored, ok, err)
	}
	if stored.Languages != "Italian,English" {
		t.Fatalf("languages = %q", stored.Languages)
	}

	// The same release from a source that does not declare them: the stored
	// value stays.
	again := release
	again.Languages = ""
	again.LastSeenAt = now.Add(time.Hour)
	if _, err := st.UpsertRelease(ctx, &again); err != nil {
		t.Fatalf("UpsertRelease again: %v", err)
	}
	stored, _, err = st.ReleaseByKey(ctx, "dedup-languages")
	if err != nil {
		t.Fatalf("ReleaseByDedupKey: %v", err)
	}
	if stored.Languages != "Italian,English" {
		t.Fatalf("languages after the merge = %q, want the first value", stored.Languages)
	}

	// A source that does say replaces the list: it is newer information.
	third := release
	third.Languages = "Italian"
	if _, err := st.UpsertRelease(ctx, &third); err != nil {
		t.Fatalf("UpsertRelease third: %v", err)
	}
	stored, _, err = st.ReleaseByKey(ctx, "dedup-languages")
	if err != nil {
		t.Fatalf("ReleaseByDedupKey: %v", err)
	}
	if stored.Languages != "Italian" {
		t.Fatalf("languages after the replacement = %q", stored.Languages)
	}

	// The list query reads the same column.
	list, _, err := st.SearchReleases(ctx, Filter{Query: "film"})
	if err != nil {
		t.Fatalf("SearchReleases: %v", err)
	}
	if len(list) != 1 || list[0].Languages != "Italian" {
		t.Fatalf("SearchReleases = %+v", list)
	}
}
