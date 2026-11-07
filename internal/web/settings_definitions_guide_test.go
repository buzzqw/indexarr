package web

import (
	"context"
	"strings"
	"testing"

	"github.com/buzzqw/indexarr/internal/store"
)

// The Definitions part of Settings > Advanced explains what a definition is,
// guides the common tasks, shows first the definitions behind the user's
// indexers and keeps the complete list (with every column) folded below.
func TestDefinitionsSectionGuidesBeforeTheFullList(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	if err := env.store.UpsertDefinition(ctx, store.Definition{
		ID: "guide-def", SchemaVersion: 1, Family: "torznab", State: "degradata",
		Origin: "local", Author: "test", Body: "schema: 1\nconnector: torznab\n", Hash: strings.Repeat("b", 64),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := env.store.CreateIndexer(ctx, store.Indexer{
		DefinitionID: "guide-def", Protocol: "torrent", Slug: "guide",
		Name: "Guide", Enabled: true, ConfigJSON: `{}`,
	}); err != nil {
		t.Fatal(err)
	}
	page := get(t, env.handler, "/settings?section=advanced&lang=en").Body.String()
	section := page[strings.Index(page, `id="definitions"`):]
	order := []string{
		"is the “recipe” indexarr uses", "What do you want to do?", `href="/indexers/new"`,
		"Used by your indexers", `class="definitions-table definitions-used"`, `href="/definitions/guide-def"`,
		`id="definitions-remote"`, `action="/definitions/import"`,
		`class="filter-advanced definitions-all"`, "Full list:", "Family / connector", "Hash",
		"Built-in catalog definitions update themselves",
	}
	at := 0
	for _, want := range order {
		next := strings.Index(section[at:], want)
		if next < 0 {
			t.Fatalf("definitions section missing %q after position %d", want, at)
		}
		at += next
	}
	used := section[strings.Index(section, "definitions-used"):strings.Index(section, `id="definitions-remote"`)]
	if !strings.Contains(used, ">degraded<") {
		t.Error("the state must be translated")
	}
	if strings.Contains(used, "Hash") || strings.Contains(used, "Family") {
		t.Error("the short table must leave the technical columns to the full list")
	}
}
