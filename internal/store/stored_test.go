package store

import (
	"context"
	"testing"
)

// TestLinkedAppCategoryRootsFollowsTheGenericKind proves a generic servarr
// application narrows a stored source through the categories the operator
// chose, instead of contributing nothing.
func TestLinkedAppCategoryRootsFollowsTheGenericKind(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	if err := st.UpsertDefinition(ctx, Definition{
		ID: "alpha", SchemaVersion: 1, Family: "torznab", State: "verificata",
		Origin: "local", Author: "test", Body: "schema: 1\nconnector: torznab\n",
	}); err != nil {
		t.Fatalf("UpsertDefinition: %v", err)
	}
	indexerID, err := st.CreateIndexer(ctx, Indexer{
		DefinitionID: "alpha", Protocol: "torrent", Slug: "alpha", Name: "Alpha",
		Enabled: true, ConfigJSON: `{}`,
	})
	if err != nil {
		t.Fatalf("CreateIndexer: %v", err)
	}
	link := func(kind, categories string) {
		appID, err := st.CreateApplication(ctx, Application{
			Kind: kind, Name: kind, URL: "http://" + kind + ".test", SyncLevel: "full",
			Categories: categories,
		})
		if err != nil {
			t.Fatalf("CreateApplication(%s): %v", kind, err)
		}
		if err := st.UpsertAppIndexer(ctx, AppIndexer{ApplicationID: appID, IndexerID: indexerID, RemoteID: "1"}); err != nil {
			t.Fatalf("UpsertAppIndexer: %v", err)
		}
	}

	link("sonarr", "5000,5070")
	link("servarr", "7030,1000")

	roots, err := st.LinkedAppCategoryRoots(ctx, indexerID)
	if err != nil {
		t.Fatalf("LinkedAppCategoryRoots: %v", err)
	}
	want := []int{1000, 5000, 7000}
	if len(roots) != len(want) {
		t.Fatalf("roots = %v, want %v", roots, want)
	}
	for i := range want {
		if roots[i] != want[i] {
			t.Fatalf("roots = %v, want %v", roots, want)
		}
	}
}
