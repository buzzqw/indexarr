package web

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/buzzqw/indexarr/internal/store"
)

// The indexer page of a forum with a topic history offers the History tab with
// the counters and the manual page read; a source without a catalog does not
// show the tab at all.
func TestIndexerHistoryTabShowsTheCounters(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	if err := env.store.UpsertDefinition(ctx, store.Definition{
		ID: "forum-def", SchemaVersion: 1, Family: "native", State: "verificata",
		Origin: "local", Author: "test",
		Body: `schema: 1
meta: { id: forum-def, name: Forum, type: private, protocol: torrent, urls: ["https://forum.example/"] }
capabilities: { search: [q] }
search:
  request: { get: /search.php }
  response:
    type: html
    rows: { selector: "li" }
    fields:
      title: { selector: "a" }
    required: [title]
catalog:
  forums:
    - { id: "52", standard: 5040 }
  request: { get: /viewforum.php }
  forum_param: f
  start_param: start
  page_size: 25
  response:
    type: html
    rows: { selector: 'li:not([class*="sticky"])' }
    fields:
      title: { selector: "a" }
      details: { selector: "a", attr: href, type: url }
    required: [title, details]
`,
		Hash: strings.Repeat("b", 64),
	}); err != nil {
		t.Fatalf("UpsertDefinition: %v", err)
	}
	id, err := env.store.CreateIndexer(ctx, store.Indexer{
		DefinitionID: "forum-def", Protocol: "torrent", Slug: "forum", Name: "Forum",
		Enabled: true, Priority: 25, ConfigJSON: `{}`,
	})
	if err != nil {
		t.Fatalf("CreateIndexer: %v", err)
	}
	now := time.Now()
	if _, err := env.store.UpsertTopics(ctx, id, []store.Topic{
		{ForumID: "52", Title: "Dark Matter S01 1080p", URL: "https://forum.example/viewtopic.php?t=1", Category: 5040, FirstSeenAt: now, LastSeenAt: now},
		{ForumID: "52", Title: "Dark Matter S02 1080p", URL: "https://forum.example/viewtopic.php?t=2", Category: 5040, FirstSeenAt: now, LastSeenAt: now},
	}); err != nil {
		t.Fatalf("UpsertTopics: %v", err)
	}
	if err := env.store.SaveTopicCursor(ctx, store.TopicCursor{
		IndexerID: id, ForumID: "52", NextStart: 25, LastRunAt: now,
	}); err != nil {
		t.Fatalf("SaveTopicCursor: %v", err)
	}

	body := get(t, env.handler, "/indexers/"+itoa(id)+"?tab=history&lang=en").Body.String()
	for _, want := range []string{
		"Topic history",
		"2 topics stored",
		"Read a page now",
		"Next row",
		"action=\"/indexers/" + itoa(id) + "/history/run\"",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the history tab is missing %q", want)
		}
	}
	// The tab is offered and marked as current.
	if !strings.Contains(body, "?tab=history\" aria-current=\"page\"") {
		t.Error("the History tab is not the active one")
	}
}

// A source without a catalog has no topic history and no tab.
func TestIndexerWithoutCatalogHasNoHistoryTab(t *testing.T) {
	env := newTestEnv(t)
	id := seedDataIndexer(t, env, "plain", "Plain")
	body := get(t, env.handler, "/indexers/"+itoa(id)+"?lang=en").Body.String()
	if strings.Contains(body, "?tab=history") {
		t.Error("a source without a catalog shows the History tab")
	}
	if strings.Contains(body, "/history/run") {
		t.Error("a source without a catalog shows the history read form")
	}
}
