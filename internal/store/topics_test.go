package store

import (
	"context"
	"testing"
	"time"
)

// The topic history keeps one row per address: reading the same page again
// refreshes last_seen_at and never duplicates the thread or loses the day it
// was first seen.
func TestTopicsUpsertAndSearch(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	mustCreateDefinition(t, st, "forum-def")
	indexerID := mustCreateIndexer(t, st, "forum-def", "forum", "Forum")

	first := time.Unix(1_700_000_000, 0).UTC()
	added, err := st.UpsertTopics(ctx, indexerID, []Topic{
		{ForumID: "52", Title: "Dark Matter S01 1080p", URL: "https://forum.example/viewtopic.php?t=1", Category: 5040, FirstSeenAt: first, LastSeenAt: first},
		{ForumID: "52", Title: "Dark Matter S02 720p", URL: "https://forum.example/viewtopic.php?t=2", Category: 5040, FirstSeenAt: first, LastSeenAt: first},
		{ForumID: "26", Title: "Il Film 2024 ITA", URL: "https://forum.example/viewtopic.php?t=3", Category: 2040, FirstSeenAt: first, LastSeenAt: first},
	})
	if err != nil {
		t.Fatalf("UpsertTopics: %v", err)
	}
	if added != 3 {
		t.Fatalf("added = %d, want 3", added)
	}

	later := first.Add(24 * time.Hour)
	added, err = st.UpsertTopics(ctx, indexerID, []Topic{
		{ForumID: "52", Title: "Dark Matter S01 1080p", URL: "https://forum.example/viewtopic.php?t=1", Category: 5040, LastSeenAt: later},
	})
	if err != nil {
		t.Fatalf("UpsertTopics again: %v", err)
	}
	if added != 0 {
		t.Fatalf("added on the second pass = %d, want 0", added)
	}
	count, err := st.TopicCount(ctx, indexerID)
	if err != nil || count != 3 {
		t.Fatalf("TopicCount = %d, %v", count, err)
	}

	// The stored title is searchable by its words, and the order is the most
	// recent sighting first: the thread still published wins.
	found, err := st.SearchTopics(ctx, indexerID, []string{"dark", "matter"}, 10)
	if err != nil {
		t.Fatalf("SearchTopics: %v", err)
	}
	if len(found) != 2 {
		t.Fatalf("SearchTopics = %d rows, want 2", len(found))
	}
	if found[0].URL != "https://forum.example/viewtopic.php?t=1" {
		t.Errorf("first result = %q, want the one seen last", found[0].URL)
	}
	if !found[0].FirstSeenAt.Equal(first) {
		t.Errorf("first_seen_at = %v, want %v", found[0].FirstSeenAt, first)
	}
	if !found[0].LastSeenAt.Equal(later) {
		t.Errorf("last_seen_at = %v, want %v", found[0].LastSeenAt, later)
	}

	// A word that is not in any title narrows the result to nothing instead of
	// matching everything.
	found, err = st.SearchTopics(ctx, indexerID, []string{"matter", "klingon"}, 10)
	if err != nil {
		t.Fatalf("SearchTopics: %v", err)
	}
	if len(found) != 0 {
		t.Fatalf("SearchTopics with a missing word = %d rows, want 0", len(found))
	}

	// The history belongs to one indexer: another one does not see it.
	otherID := mustCreateIndexer(t, st, "forum-def", "other", "Other")
	found, err = st.SearchTopics(ctx, otherID, []string{"dark"}, 10)
	if err != nil {
		t.Fatalf("SearchTopics(other): %v", err)
	}
	if len(found) != 0 {
		t.Fatalf("another indexer sees %d topics, want 0", len(found))
	}
}

func TestTopicCursorsRoundTrip(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	mustCreateDefinition(t, st, "forum-def")
	indexerID := mustCreateIndexer(t, st, "forum-def", "forum", "Forum")

	cursors, err := st.TopicCursors(ctx, indexerID)
	if err != nil || len(cursors) != 0 {
		t.Fatalf("TopicCursors = %v, %v", cursors, err)
	}
	when := time.Unix(1_700_000_000, 0).UTC()
	if err := st.SaveTopicCursor(ctx, TopicCursor{IndexerID: indexerID, ForumID: "52", NextStart: 250, LastRunAt: when}); err != nil {
		t.Fatalf("SaveTopicCursor: %v", err)
	}
	cursors, err = st.TopicCursors(ctx, indexerID)
	if err != nil {
		t.Fatalf("TopicCursors: %v", err)
	}
	cursor := cursors["52"]
	if cursor.NextStart != 250 || !cursor.LastRunAt.Equal(when) {
		t.Fatalf("cursor = %+v, want start 250 at %v", cursor, when)
	}
	if err := st.DeleteTopics(ctx, indexerID); err != nil {
		t.Fatalf("DeleteTopics: %v", err)
	}
	cursors, _ = st.TopicCursors(ctx, indexerID)
	if len(cursors) != 0 {
		t.Fatalf("cursors after DeleteTopics = %v", cursors)
	}
}

// A catalogue page is a scheduled run: it must set the last run time and the
// retry schedule of its indexer, otherwise the source stays due at every tick
// and the forum is read once a minute.
func TestCatalogRunSetsTheRetryState(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	mustCreateDefinition(t, st, "forum-def")
	indexerID := mustCreateIndexer(t, st, "forum-def", "forum", "Forum")

	started := time.Now().Add(-2 * time.Hour)
	if _, err := st.RecordIndexerHistory(ctx, IndexerHistory{
		IndexerID: indexerID, Kind: "catalog", Status: "error",
		Query: "52 start=0", StartedAt: started,
	}); err != nil {
		t.Fatalf("RecordIndexerHistory: %v", err)
	}
	last, err := st.LastRunTimes(ctx)
	if err != nil {
		t.Fatalf("LastRunTimes: %v", err)
	}
	if got := last[indexerID]; got.IsZero() || !got.Equal(started.Truncate(time.Second)) {
		t.Fatalf("LastRunTimes = %v, want the catalogue run at %v", got, started.Truncate(time.Second))
	}
	states, err := st.RunRetryStates(ctx)
	if err != nil {
		t.Fatalf("RunRetryStates: %v", err)
	}
	if state := states[indexerID]; state.Failures != 1 {
		t.Fatalf("RunRetryStates failures = %d, want 1", state.Failures)
	}
}
