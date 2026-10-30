package web

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/buzzqw/indexarr/internal/definition"
	"github.com/buzzqw/indexarr/internal/store"
)

func TestSearchOnlyIndexerExplainsItself(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	body := `schema: 1
connector: torznab
meta:
  id: search-only
  name: Search Only
  type: public
  protocol: torrent
  urls: ["https://example.test/"]
  latest: false
`
	if _, err := definition.Parse([]byte(body)); err != nil {
		t.Fatalf("definition: %v", err)
	}
	if err := env.store.UpsertDefinition(ctx, store.Definition{
		ID: "search-only", SchemaVersion: definition.CurrentSchema,
		State: "verificata", Origin: "local", Author: "test",
		Body: body, Hash: definition.Fingerprint([]byte(body)), UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	id, err := env.store.CreateIndexer(ctx, store.Indexer{
		DefinitionID: "search-only", Protocol: "torrent", Slug: "search-only",
		Name: "Search Only", Enabled: true, ConfigJSON: `{"url":"https://example.test/"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	page := get(t, env.handler, "/indexers/"+strconvI(id)+"?lang=en").Body.String()
	if !strings.Contains(page, "no list of new releases") {
		t.Errorf("search-only explanation missing: %s", page)
	}
	if !strings.Contains(page, "Search Only") {
		t.Error("indexer name missing")
	}
	if strings.Contains(page, "Update now") {
		t.Error("a search-only indexer must not offer a scheduled update")
	}
}

func TestActivityShowsLiveFanOut(t *testing.T) {
	env := newTestEnv(t)
	if _, err := env.store.RecordAppSearch(context.Background(), store.AppSearch{
		App: "sonarr", Mode: "tvsearch", Query: "Show", TotalResults: 1,
		IndexerNames: []string{"Slow Site", "Fast Site"},
		Live: []store.AppSearchLive{
			{IndexerID: 1, Name: "Slow Site", Mode: "search", Fallback: true, TimedOut: true, Duration: 20000},
			{IndexerID: 2, Name: "Fast Site", Mode: "tvsearch", Results: 1, Duration: 120},
		},
	}); err != nil {
		t.Fatal(err)
	}
	page := get(t, env.handler, "/activity?tab=requests&lang=en").Body.String()
	for _, want := range []string{"Slow Site", "fallback", "in background", "Fast Site", "tvsearch"} {
		if !strings.Contains(page, want) {
			t.Errorf("activity page missing %q", want)
		}
	}
}
