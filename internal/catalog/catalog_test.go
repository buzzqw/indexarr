package catalog

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"os"
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
	if len(entries) != 63 {
		t.Fatalf("len(Builtins()) = %d, want 63", len(entries))
	}

	verified := map[string]bool{
		"extto":              true,
		"torrentgalaxy":      true,
		"corsaro":            true,
		"knaben":             true,
		"knaben-search":      true,
		"torrentgalaxyclone": true,
		"limetorrents":       true,
		"thepiratebay":       true,
		"torrentscsv":        true,
		"showrss":            true,
		"1337x":              true,
		"animetosho":         true,
		"kickasstorrents":    true,
		"nyaa":               true,
		"subsplease":         true,
		"tokyotosho":         true,
		"torrentdownloads":   true,
		"yts":                true,
		"acgnx":              true,
		"bangumimoe":         true,
		"mikan":              true,
		"torlock":            true,
		"torrentfunk":        true,
		"bt4g":               true,
		"solidtorrents":      true,
		"bitsearch":          true,
		"btdig":              true,
		"linuxtracker":       true,
		"academictorrents":   true,
		"shanaproject":       true,
		"anirena":            true,
		"audiobookbay":       true,
		"internetarchive":    true,
		"eztv":               true,
		"torrent9":           true,
		"librivox":           true,
		"fosstorrents":       true,
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
		wantState := "bozza"
		if verified[entry.ID] {
			wantState = "verificata"
		}
		if entry.State != wantState {
			t.Errorf("%s: state = %q, want %q", entry.ID, entry.State, wantState)
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

// TestIndexMatchesDefinitions fails when a definition or the manifest changed
// without regenerating index.json. The fix is one command:
//
//	go generate ./internal/catalog
func TestIndexMatchesDefinitions(t *testing.T) {
	entries, err := Builtins()
	if err != nil {
		t.Fatalf("Builtins: %v", err)
	}
	sites, err := Sites()
	if err != nil {
		t.Fatalf("Sites: %v", err)
	}
	want, err := json.MarshalIndent(BuildFullIndex(entries, sites), "", "  ")
	if err != nil {
		t.Fatalf("encode index: %v", err)
	}
	want = append(want, '\n')
	// The embedded index can be materialised with CRLF on a Windows
	// checkout; the generated document is always LF.
	gotIndex := bytes.ReplaceAll(indexJSON, []byte("\r\n"), []byte("\n"))
	if !bytes.Equal(gotIndex, want) {
		t.Fatalf("catalog index is stale: run go generate ./internal/catalog")
	}
}

func TestIndexShapeIsStable(t *testing.T) {
	index, err := Index()
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	entries, err := Builtins()
	if err != nil {
		t.Fatalf("Builtins: %v", err)
	}
	sites, err := Sites()
	if err != nil {
		t.Fatalf("Sites: %v", err)
	}
	if len(index) != len(entries)+len(sites) {
		t.Fatalf("len(Index()) = %d, want %d", len(index), len(entries)+len(sites))
	}
	for i := 1; i < len(index); i++ {
		if index[i-1].ID >= index[i].ID {
			t.Errorf("index is not sorted by id: %q before %q", index[i-1].ID, index[i].ID)
		}
	}
	byID := map[string]IndexEntry{}
	for _, entry := range index {
		if entry.Name == "" || entry.Type == "" || entry.Protocol == "" || entry.State == "" {
			t.Errorf("%s: incomplete index row: %+v", entry.ID, entry)
		}
		byID[entry.ID] = entry
	}
	extto, ok := byID["extto"]
	if !ok {
		t.Fatal("extto is missing from the index")
	}
	if extto.Language != "en-US" || extto.Type != "public" || len(extto.Hosts) == 0 {
		t.Errorf("extto index row = %+v", extto)
	}
	if len(extto.Required) != 1 || extto.Required[0] != "filter" {
		t.Errorf("extto required = %v, want [filter]", extto.Required)
	}
	corsaro, ok := byID["corsaro"]
	if !ok {
		t.Fatal("corsaro is missing from the index")
	}
	for _, group := range []string{"movies", "music"} {
		if !containsString(corsaro.Categories, group) {
			t.Errorf("corsaro categories = %v, want %q", corsaro.Categories, group)
		}
	}
	if index[0].ID != "1337x" {
		t.Errorf("first index id = %q, want 1337x", index[0].ID)
	}
	if last := index[len(index)-1].ID; last != "yts" {
		t.Errorf("last index id = %q, want yts", last)
	}
	if !extto.Italian || extto.Family != "custom" {
		t.Errorf("extto metadata = family %q italian %v", extto.Family, extto.Italian)
	}
	if !corsaro.Italian || corsaro.Family != "xbtit" {
		t.Errorf("corsaro metadata = family %q italian %v", corsaro.Family, corsaro.Italian)
	}
}

func containsString(list []string, want string) bool {
	for _, value := range list {
		if value == want {
			return true
		}
	}
	return false
}

func TestBuiltinsManifestOrder(t *testing.T) {
	entries, err := Builtins()
	if err != nil {
		t.Fatalf("Builtins: %v", err)
	}
	want := []string{
		"example-html",
		"unit3d-generic",
		"newznab-generic",
		"torznab-generic",
		"gazelle-generic",
		"nexusphp-generic",
		"torrenttrader-generic",
		"xbtit-generic",
		"extto",
		"torrentgalaxy",
		"corsaro",
		"rss",
		"corsaro-user",
		"knaben",
		"knaben-search",
		"torrentleech",
		"torrentleech-rss",
		"torrentgalaxyclone",
		"limetorrents",
		"thepiratebay",
		"torrentscsv",
		"showrss",
		"corsaro-account",
		"ildragonegro",
		"mircrew",
		"mircrew-smartfeed",
		"1337x",
		"animetosho",
		"kickasstorrents",
		"nyaa",
		"subsplease",
		"tokyotosho",
		"torrentdownloads",
		"yts",
		"acgnx",
		"bangumimoe",
		"mikan",
		"torlock",
		"torrentfunk",
		"bt4g",
		"solidtorrents",
		"bitsearch",
		"btdig",
		"linuxtracker",
		"academictorrents",
		"shanaproject",
		"anirena",
		"audiobookbay",
		"internetarchive",
		"eztv",
		"torrent9",
		"librivox",
		"fosstorrents",
		"acgrip",
		"crackingpatching",
		"dmhy",
		"mactorrentsdownload",
		"nipponsei",
		"plugintorrent",
		"pornrips",
		"sukebeinyaasi",
		"vstorrent",
		"vsttorrentz",
	}
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
	if len(items) != 63 {
		t.Fatalf("manifest has %d definitions, want 63", len(items))
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

func TestVerifiedEntriesHaveHealthAndTests(t *testing.T) {
	// A catalog entry may only be "verificata" when it declares a health query
	// and at least one offline test; the equivalence test exercises the
	// fixtures themselves.
	entries, err := Builtins()
	if err != nil {
		t.Fatalf("Builtins: %v", err)
	}
	verified := 0
	for _, entry := range entries {
		if entry.State != "verificata" {
			continue
		}
		verified++
		if entry.Definition.Health == nil {
			t.Errorf("%s: verificata but has no health section", entry.ID)
		}
		if len(entry.Definition.Tests) == 0 {
			t.Errorf("%s: verificata but declares no tests", entry.ID)
		}
	}
	if verified == 0 {
		t.Fatal("catalog has no verificata entry")
	}
}

func TestDeclaredFixturesExist(t *testing.T) {
	// tests[].fixture is relative to the definition file. The catalog keeps
	// fixtures in a real directory, internal/catalog/definitions/fixtures/, so
	// the tree also works on Windows checkouts (core.symlinks=false).
	entries, err := Builtins()
	if err != nil {
		t.Fatalf("Builtins: %v", err)
	}
	for _, entry := range entries {
		for _, test := range entry.Definition.Tests {
			name := filepath.Join("definitions", filepath.FromSlash(test.Fixture))
			info, err := os.Stat(name)
			if err != nil {
				t.Errorf("%s: fixture %q: %v", entry.ID, test.Fixture, err)
				continue
			}
			if !info.Mode().IsRegular() || info.Size() == 0 {
				t.Errorf("%s: fixture %q is not a non-empty regular file", entry.ID, test.Fixture)
			}
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

	entries, err := Builtins()
	if err != nil {
		t.Fatalf("Builtins: %v", err)
	}
	n, err := Sync(ctx, st)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if want := len(entries) - 1; n != want {
		t.Fatalf("Sync wrote %d, want %d (the local entry must be skipped)", n, want)
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

func TestBuiltinsAreNotDerived(t *testing.T) {
	entries, err := Builtins()
	if err != nil {
		t.Fatalf("Builtins: %v", err)
	}
	for _, entry := range entries {
		if entry.Definition.IsDerived() {
			t.Errorf("%s: built-in catalog entry is a derived draft (derived_from=%q, author=%q)",
				entry.ID, entry.Definition.Meta.DerivedFrom, entry.Definition.Meta.Author)
		}
	}
}

// TestLoadRejectsDerivedDraftInCatalog copies the real embedded catalog into a
// temporary directory, marks one definition as derived and checks that the
// loader refuses it. This is the same path catalog.Builtins uses.
func TestLoadRejectsDerivedDraftInCatalog(t *testing.T) {
	dir := t.TempDir()
	manifestBody, err := builtinFS.ReadFile(manifestFile)
	if err != nil {
		t.Fatalf("read embedded manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, manifestFile), manifestBody, 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "definitions"), 0o755); err != nil {
		t.Fatalf("mkdir definitions: %v", err)
	}
	files, err := fs.Glob(builtinFS, "definitions/*.yaml")
	if err != nil {
		t.Fatalf("glob definitions: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("embedded catalog has no definitions")
	}
	for _, name := range files {
		body, err := builtinFS.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(name)), body, 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	target := filepath.Join(dir, "definitions", "example-html.yaml")
	body, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read %s: %v", target, err)
	}
	// A Windows checkout may materialise the embedded fixture with CRLF.
	// Normalise before injecting the marker so the test exercises the policy
	// on every supported platform.
	body = bytes.ReplaceAll(body, []byte("\r\n"), []byte("\n"))
	derived := bytes.Replace(body, []byte("  author: indexarr\n"),
		[]byte("  author: indexarr\n  derived_from: cardigann\n"), 1)
	if bytes.Equal(derived, body) {
		t.Fatal("could not inject derived_from into the temp catalog fixture")
	}
	if err := os.WriteFile(target, derived, 0o644); err != nil {
		t.Fatalf("write %s: %v", target, err)
	}

	_, err = load(os.DirFS(dir))
	if err == nil {
		t.Fatal("load accepted a catalog entry with derived_from")
	}
	if !strings.Contains(err.Error(), "derived") || !strings.Contains(err.Error(), "derived_from") {
		t.Errorf("error does not explain the derived-draft rejection: %v", err)
	}
}

// TestLoadRejectsCardigannAuthor guards the second marker written by the
// converter: the author is compared case-insensitively and after trimming.
func TestLoadRejectsCardigannAuthor(t *testing.T) {
	manifestBody := "schema: 1\ndefinitions:\n  - id: draft\n    file: definitions/draft.yaml\n    state: bozza\n    author: indexarr\n"
	for _, author := range []string{"cardigann-import", "Cardigann-Import", "CARDIGANN-IMPORT", "  cardigann-import  "} {
		t.Run(strings.TrimSpace(author), func(t *testing.T) {
			defBody := "schema: 1\nconnector: torznab\nmeta:\n  id: draft\n  name: Draft\n  type: public\n  protocol: torrent\n  urls: [https://draft.example/]\n  author: \"" + author + "\"\n"
			fsys := fstest.MapFS{
				manifestFile:             &fstest.MapFile{Data: []byte(manifestBody)},
				"definitions/draft.yaml": &fstest.MapFile{Data: []byte(defBody)},
			}
			_, err := load(fsys)
			if err == nil {
				t.Fatalf("load accepted author %q", author)
			}
			if !strings.Contains(err.Error(), "derived") {
				t.Errorf("error does not explain the derived-draft rejection: %v", err)
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
