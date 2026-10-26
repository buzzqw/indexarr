package web

import (
	"path/filepath"
	"testing"

	"github.com/buzzqw/indexarr/internal/definition"
	"github.com/buzzqw/indexarr/internal/store"
)

func TestParsedDefinitionCacheKeysByHash(t *testing.T) {
	var cache parsedDefinitionCache
	first := &definition.Definition{Meta: definition.Meta{ID: "one"}}
	second := &definition.Definition{Meta: definition.Meta{ID: "two"}}

	cache.store("one", "hash-a", first)
	if got, ok := cache.lookup("one", "hash-a"); !ok || got != first {
		t.Fatalf("lookup same hash = %v, %v; want the cached pointer", got, ok)
	}
	if _, ok := cache.lookup("one", "hash-b"); ok {
		t.Fatal("lookup with a new hash returned a stale definition")
	}
	cache.store("one", "hash-b", second)
	if got, ok := cache.lookup("one", "hash-b"); !ok || got != second {
		t.Fatalf("lookup after replacement = %v, %v; want the new pointer", got, ok)
	}
}

func TestParsedDefinitionCacheIsBounded(t *testing.T) {
	var cache parsedDefinitionCache
	for i := 0; i < parsedDefinitionCacheMax+10; i++ {
		id := string(rune('a' + i%26))
		cache.store(id, "h", &definition.Definition{Meta: definition.Meta{ID: id}})
	}
	if got := len(cache.entries); got > parsedDefinitionCacheMax {
		t.Fatalf("cache holds %d entries, limit %d", got, parsedDefinitionCacheMax)
	}
	if got := len(cache.order); got > parsedDefinitionCacheMax {
		t.Fatalf("order holds %d ids, limit %d", got, parsedDefinitionCacheMax)
	}
}

func TestParseStoredDefinitionReusesCacheUntilHashChanges(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "definitions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	srv := &Server{store: st}

	body := "schema: 1\nconnector: rss\nmeta:\n  id: cache-test\n  name: Cache Test\n  type: public\n  protocol: torrent\n  urls:\n    - https://example.invalid/feed.xml\nsettings:\n  - { name: url, type: text, label: Feed URL, required: true, default: \"https://example.invalid/feed.xml\" }\ncapabilities:\n  search: [q]\n"
	row := store.Definition{ID: "cache-test", Body: body, Hash: definition.Fingerprint([]byte(body))}
	first, err := srv.parseStoredDefinition(row)
	if err != nil {
		t.Fatal(err)
	}
	again, err := srv.parseStoredDefinition(row)
	if err != nil {
		t.Fatal(err)
	}
	if first != again {
		t.Fatal("the second parse did not reuse the cached definition")
	}

	row.Body = body + "# edited\n" // a comment keeps the YAML valid
	row.Hash = definition.Fingerprint([]byte(row.Body))
	edited, err := srv.parseStoredDefinition(row)
	if err != nil {
		t.Fatal(err)
	}
	if edited == first {
		t.Fatal("a changed hash returned the old parsed definition")
	}
}
