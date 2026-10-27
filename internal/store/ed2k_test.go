package store

import (
	"context"
	"testing"
	"time"
)

func ed2kRelease(title, link, hash string) *Release {
	return &Release{
		DedupKey:    "ed2k:" + hash,
		Title:       title,
		SearchTitle: NormalizeSearch(title),
		Size:        1234,
		Category:    "2000",
		PublishedAt: time.Unix(1_793_000_000, 0).UTC(),
		Ed2k:        link,
		Ed2kHash:    hash,
		Protocol:    "ed2k",
	}
}

func TestEd2kReleaseRoundTrip(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	link := "ed2k://|file|Rare%20Movie.mkv|1234|0123456789abcdef0123456789abcdef|/"
	rel := ed2kRelease("Rare Movie", link, "0123456789abcdef0123456789abcdef")
	if _, err := st.UpsertRelease(ctx, rel); err != nil {
		t.Fatalf("UpsertRelease: %v", err)
	}
	got, found, err := st.ReleaseByEd2kHash(ctx, "0123456789abcdef0123456789abcdef")
	if err != nil || !found {
		t.Fatalf("ReleaseByEd2kHash: found=%v err=%v", found, err)
	}
	if got.Ed2k != link || got.Ed2kHash != rel.Ed2kHash || got.Protocol != "ed2k" {
		t.Errorf("release = %+v", got)
	}

	// The same MD4 hash seen through another key must update the same row.
	again := ed2kRelease("Rare Movie (another title)", link, rel.Ed2kHash)
	again.DedupKey = "ed2k:other-key"
	if _, err := st.UpsertRelease(ctx, again); err != nil {
		t.Fatalf("second UpsertRelease: %v", err)
	}
	list, err := st.ListRecentReleases(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("releases = %d, want 1", len(list))
	}
}

func TestIndexerProtocolAcceptsEd2k(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	def := Definition{
		ID: "ddunlimited", SchemaVersion: 1, State: "verificata", Origin: "local",
		Author: "test",
		Body:   "schema: 1\nmeta:\n  id: ddunlimited\n  name: D\n  type: private\n  protocol: ed2k\n  urls:\n    - https://example.invalid/\n",
		Hash:   "hash",
	}
	if err := st.UpsertDefinition(ctx, def); err != nil {
		t.Fatalf("SaveDefinition: %v", err)
	}
	id, err := st.CreateIndexer(ctx, Indexer{
		DefinitionID: "ddunlimited", Protocol: "ed2k", Slug: "ddunlimited",
		Name: "DDUnlimited", Enabled: true,
	})
	if err != nil {
		t.Fatalf("CreateIndexer: %v", err)
	}
	got, err := st.GetIndexer(ctx, id)
	if err != nil {
		t.Fatalf("GetIndexer: %v", err)
	}
	if got.Protocol != "ed2k" {
		t.Errorf("protocol = %q", got.Protocol)
	}
}
