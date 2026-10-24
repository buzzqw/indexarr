package store

import (
	"context"
	"testing"
	"time"
)

func TestSeedersCandidatesUseDedicatedTimestampAndRecentWindow(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	now := time.Now()
	if err := st.UpsertDefinition(ctx, Definition{
		ID: "seed-def", SchemaVersion: 1, Family: "torznab", State: "verificata",
		Origin: "local", Author: "test", Body: "schema: 1", Hash: "hash",
	}); err != nil {
		t.Fatal(err)
	}
	id, err := st.CreateIndexer(ctx, Indexer{
		DefinitionID: "seed-def", Slug: "seed", Name: "Seed", Protocol: "torrent", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	mk := func(key string, published time.Time) int64 {
		rel := Release{DedupKey: key, Title: key, SearchTitle: key, Category: "5040",
			PublishedAt: published, FirstSeenAt: published, LastSeenAt: published}
		if _, err := st.UpsertRelease(ctx, &rel); err != nil {
			t.Fatal(err)
		}
		if err := st.RecordReleaseSource(ctx, ReleaseSource{ReleaseID: rel.ID, IndexerID: id,
			FirstSeenAt: published, LastSeenAt: published}); err != nil {
			t.Fatal(err)
		}
		return rel.ID
	}
	recent := mk("recent-release", now.Add(-24*time.Hour))
	mk("old-release", now.Add(-30*24*time.Hour))

	got, err := st.SeedersCandidates(ctx, now, 6*time.Hour, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ReleaseID != recent {
		t.Fatalf("candidates = %+v, want only the recent release", got)
	}
	if err := st.MarkSeedersChecked(ctx, recent, id, now); err != nil {
		t.Fatal(err)
	}
	again, err := st.SeedersCandidates(ctx, now.Add(time.Hour), 6*time.Hour, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 0 {
		t.Fatalf("same row returned before TTL: %+v", again)
	}
}
