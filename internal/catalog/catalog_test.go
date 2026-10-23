package catalog

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/buzzqw/indexarr/internal/definition"
	"github.com/buzzqw/indexarr/internal/store"
)

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "catalog-test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestBuiltinsValid(t *testing.T) {
	entries, err := Builtins()
	if err != nil {
		t.Fatalf("Builtins: %v", err)
	}
	if len(entries) != 7 {
		t.Fatalf("len(Builtins()) = %d, want 7", len(entries))
	}

	seen := make(map[string]bool, len(entries))
	for _, entry := range entries {
		if seen[entry.ID] {
			t.Errorf("duplicate id %q", entry.ID)
		}
		seen[entry.ID] = true

		if entry.Definition == nil {
			t.Fatalf("%s: nil Definition", entry.ID)
		}
		if entry.Definition.Meta.ID != entry.ID {
			t.Errorf("%s: meta.id = %q", entry.ID, entry.Definition.Meta.ID)
		}
		if entry.State != "bozza" {
			t.Errorf("%s: state = %q, want bozza", entry.ID, entry.State)
		}
		if entry.Author == "" {
			t.Errorf("%s: author is empty", entry.ID)
		}
		if entry.Definition.Schema != definition.CurrentSchema {
			t.Errorf("%s: schema = %d", entry.ID, entry.Definition.Schema)
		}

		// The hash is the SHA-256 fingerprint of the embedded body and must be
		// lowercase hexadecimal.
		if len(entry.Hash) != 64 {
			t.Errorf("%s: hash length = %d, want 64", entry.ID, len(entry.Hash))
		}
		if _, err := hex.DecodeString(entry.Hash); err != nil {
			t.Errorf("%s: hash is not hexadecimal: %v", entry.ID, err)
		}
		if want := definition.Fingerprint(entry.Body); entry.Hash != want {
			t.Errorf("%s: hash = %q, want %q", entry.ID, entry.Hash, want)
		}
		if strings.ToLower(entry.Hash) != entry.Hash {
			t.Errorf("%s: hash is not lowercase", entry.ID)
		}

		if _, err := definition.Parse(entry.Body); err != nil {
			t.Errorf("%s: embedded body does not parse: %v", entry.ID, err)
		}
		// Every definition must be loadable from its own id.
		if found, ok := Get(entry.ID); !ok || found.Hash != entry.Hash {
			t.Errorf("%s: Get did not return the embedded entry", entry.ID)
		}
	}

	if _, ok := Get("does-not-exist"); ok {
		t.Error("Get returned an entry for an unknown id")
	}
}

func TestBuiltinsManifestOrder(t *testing.T) {
	entries, err := Builtins()
	if err != nil {
		t.Fatalf("Builtins: %v", err)
	}
	want := []string{"example-html", "unit3d-generic", "newznab-generic", "gazelle-generic", "nexusphp-generic", "torrenttrader-generic", "xbtit-generic"}
	if len(entries) != len(want) {
		t.Fatalf("len(Builtins()) = %d, want %d", len(entries), len(want))
	}
	for i, id := range want {
		if entries[i].ID != id {
			t.Errorf("entries[%d].ID = %q, want %q", i, entries[i].ID, id)
		}
	}
}

func TestManifestIDsMatchFiles(t *testing.T) {
	body, err := builtinFS.ReadFile(manifestFile)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	items, err := parseManifest(body)
	if err != nil {
		t.Fatalf("parseManifest: %v", err)
	}
	if len(items) != 7 {
		t.Fatalf("manifest has %d definitions, want 7", len(items))
	}
	for _, item := range items {
		base := strings.TrimSuffix(path.Base(item.File), ".yaml")
		if base != item.ID {
			t.Errorf("%s: file base %q does not match id %q", item.File, base, item.ID)
		}
	}
}

func TestEveryDefinitionHasLanguage(t *testing.T) {
	// Catalog hygiene: every definition must declare meta.language so the UI
	// and search defaults can pick the right locale. Family definitions are
	// no exception.
	entries, err := Builtins()
	if err != nil {
		t.Fatalf("Builtins: %v", err)
	}
	for _, entry := range entries {
		if strings.TrimSpace(entry.Definition.Meta.Language) == "" {
			t.Errorf("%s: meta.language is required", entry.ID)
		}
	}
}

func TestSyncFreshThenIdempotent(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	entries, err := Builtins()
	if err != nil {
		t.Fatalf("Builtins: %v", err)
	}

	n, err := Sync(ctx, st)
	if err != nil {
		t.Fatalf("Sync (fresh): %v", err)
	}
	if n != len(entries) {
		t.Fatalf("Sync (fresh) wrote %d, want %d", n, len(entries))
	}

	for _, entry := range entries {
		got, err := st.GetDefinition(ctx, entry.ID)
		if err != nil {
			t.Fatalf("GetDefinition(%s): %v", entry.ID, err)
		}
		if got.Origin != OriginBuiltin {
			t.Errorf("%s: origin = %q, want %q", entry.ID, got.Origin, OriginBuiltin)
		}
		if got.State != entry.State {
			t.Errorf("%s: state = %q, want %q", entry.ID, got.State, entry.State)
		}
		if got.Author != entry.Author {
			t.Errorf("%s: author = %q, want %q", entry.ID, got.Author, entry.Author)
		}
		if got.Hash != entry.Hash {
			t.Errorf("%s: hash = %q, want %q", entry.ID, got.Hash, entry.Hash)
		}
		if got.Body != string(entry.Body) {
			t.Errorf("%s: stored body differs from embedded body", entry.ID)
		}
		if got.SchemaVersion != entry.Definition.Schema {
			t.Errorf("%s: schema_version = %d, want %d", entry.ID, got.SchemaVersion, entry.Definition.Schema)
		}
		if got.Family != entry.Definition.Connector {
			t.Errorf("%s: family = %q, want %q", entry.ID, got.Family, entry.Definition.Connector)
		}
	}

	n, err = Sync(ctx, st)
	if err != nil {
		t.Fatalf("Sync (second): %v", err)
	}
	if n != 0 {
		t.Fatalf("Sync (second) wrote %d, want 0", n)
	}
}

func TestSyncUpdatesChangedBuiltin(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	entries, err := Builtins()
	if err != nil {
		t.Fatalf("Builtins: %v", err)
	}
	if _, err := Sync(ctx, st); err != nil {
		t.Fatalf("Sync (fresh): %v", err)
	}

	target := entries[0]
	changed, err := st.GetDefinition(ctx, target.ID)
	if err != nil {
		t.Fatalf("GetDefinition(%s): %v", target.ID, err)
	}
	changed.Body += "\n# simulated local corruption\n"
	changed.Hash = definition.Fingerprint([]byte(changed.Body))
	changed.Origin = OriginBuiltin
	if err := st.UpsertDefinition(ctx, changed); err != nil {
		t.Fatalf("UpsertDefinition: %v", err)
	}

	n, err := Sync(ctx, st)
	if err != nil {
		t.Fatalf("Sync (changed): %v", err)
	}
	if n != 1 {
		t.Fatalf("Sync (changed) wrote %d, want 1", n)
	}

	got, err := st.GetDefinition(ctx, target.ID)
	if err != nil {
		t.Fatalf("GetDefinition(%s): %v", target.ID, err)
	}
	if got.Hash != target.Hash {
		t.Errorf("hash = %q, want embedded %q", got.Hash, target.Hash)
	}
	if got.Body != string(target.Body) {
		t.Errorf("body was not restored to the embedded definition")
	}
}

func TestSyncNeverOverwritesLocal(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	local := store.Definition{
		ID:            "unit3d-generic",
		SchemaVersion: 1,
		Family:        "unit3d",
		State:         "verificata",
		Origin:        OriginLocal,
		Author:        "user",
		Body:          "schema: 1\n# edited locally\n",
		Hash:          definition.Fingerprint([]byte("schema: 1\n# edited locally\n")),
	}
	if err := st.UpsertDefinition(ctx, local); err != nil {
		t.Fatalf("UpsertDefinition(local): %v", err)
	}

	n, err := Sync(ctx, st)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if n != 6 {
		t.Fatalf("Sync wrote %d, want 6 (the local entry must be skipped)", n)
	}

	got, err := st.GetDefinition(ctx, local.ID)
	if err != nil {
		t.Fatalf("GetDefinition(%s): %v", local.ID, err)
	}
	if got.Origin != OriginLocal || got.Body != local.Body || got.Hash != local.Hash {
		t.Errorf("Sync overwrote the local definition: %+v", got)
	}
}

func TestParseManifestRejectsInvalid(t *testing.T) {
	valid := "schema: 1\ndefinitions:\n  - id: example-html\n    file: definitions/example-html.yaml\n    state: bozza\n    author: indexarr\n"
	tests := []struct {
		name string
		body string
	}{
		{"empty", ""},
		{"malformed yaml", "schema: [\n"},
		{"missing schema", "definitions:\n  - id: a\n    file: definitions/a.yaml\n    state: bozza\n    author: indexarr\n"},
		{"wrong schema", "schema: 2\ndefinitions:\n  - id: a\n    file: definitions/a.yaml\n    state: bozza\n    author: indexarr\n"},
		{"no definitions", "schema: 1\ndefinitions: []\n"},
		{"unknown key", valid + "extra: true\n"},
		{"duplicate id", valid + "  - id: example-html\n    file: definitions/other.yaml\n    state: bozza\n    author: indexarr\n"},
		{"bad id", "schema: 1\ndefinitions:\n  - id: Bad ID\n    file: definitions/bad.yaml\n    state: bozza\n    author: indexarr\n"},
		{"absolute file", "schema: 1\ndefinitions:\n  - id: bad\n    file: /tmp/bad.yaml\n    state: bozza\n    author: indexarr\n"},
		{"parent segment", "schema: 1\ndefinitions:\n  - id: bad\n    file: definitions/../bad.yaml\n    state: bozza\n    author: indexarr\n"},
		{"outside definitions dir", "schema: 1\ndefinitions:\n  - id: bad\n    file: bad.yaml\n    state: bozza\n    author: indexarr\n"},
		{"unknown state", "schema: 1\ndefinitions:\n  - id: bad\n    file: definitions/bad.yaml\n    state: pronto\n    author: indexarr\n"},
		{"missing author", "schema: 1\ndefinitions:\n  - id: bad\n    file: definitions/bad.yaml\n    state: bozza\n    author: \"\"\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseManifest([]byte(tc.body)); err == nil {
				t.Fatal("parseManifest accepted an invalid manifest")
			}
		})
	}

	if _, err := parseManifest([]byte(valid)); err != nil {
		t.Fatalf("parseManifest rejected a valid manifest: %v", err)
	}
}

func TestLoadRejectsInvalidDefinition(t *testing.T) {
	manifestBody := "schema: 1\ndefinitions:\n  - id: broken\n    file: definitions/broken.yaml\n    state: bozza\n    author: indexarr\n"
	tests := []struct {
		name string
		def  string
	}{
		{"invalid yaml", "schema: 1\nmeta: {}\n"},
		{"id mismatch", "schema: 1\nmeta:\n  id: other\n  name: Other\n  type: public\n  protocol: torrent\n  urls: [https://example.test/]\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fsys := fstest.MapFS{
				manifestFile:              &fstest.MapFile{Data: []byte(manifestBody)},
				"definitions/broken.yaml": &fstest.MapFile{Data: []byte(tc.def)},
			}
			if _, err := load(fsys); err == nil {
				t.Fatal("load accepted an invalid definition")
			}
		})
	}
}

func TestJSONSchemaAndDefinitions(t *testing.T) {
	schema := definition.JSONSchema()
	if len(schema) == 0 {
		t.Fatal("JSONSchema returned an empty document")
	}
	if !json.Valid(schema) {
		t.Fatal("JSONSchema is not valid JSON")
	}
	var doc struct {
		Schema string `json:"$schema"`
		ID     string `json:"$id"`
		Type   string `json:"type"`
	}
	if err := json.Unmarshal(schema, &doc); err != nil {
		t.Fatalf("Unmarshal(schema): %v", err)
	}
	if doc.Schema != "https://json-schema.org/draft/2020-12/schema" {
		t.Errorf("$schema = %q", doc.Schema)
	}
	if doc.ID == "" {
		t.Error("$id is missing")
	}
	if doc.Type != "object" {
		t.Errorf("type = %q, want object", doc.Type)
	}

	entries, err := Builtins()
	if err != nil {
		t.Fatalf("Builtins: %v", err)
	}
	for _, entry := range entries {
		if err := entry.Definition.Validate(); err != nil {
			t.Errorf("%s: definition.Validate: %v", entry.ID, err)
		}
	}
}
