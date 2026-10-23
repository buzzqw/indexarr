// Package catalog ships the built-in indexer definitions with the binary.
//
// The definitions are embedded at compile time, parsed and validated whenever
// the catalog is loaded, and can be synchronised into the store. Synchronising
// never overwrites a definition the user edited locally: entries with origin
// "local" are left untouched, and built-in entries are only rewritten when
// their content hash changes.
//
// Definitions in state "verificata" were written from real public pages or
// feeds captured into internal/catalog/fixtures/ and pass the offline
// equivalence test against the legacy parsers. Entries still in state
// "bozza" are drafts and must not be presented as production ready.
//
// Definitions produced by a conversion tool (meta.derived_from, or the
// Cardigann converter's author) are rejected: derived drafts are local writing
// aids and never enter the distributed catalog.
package catalog

import (
	"bytes"
	"context"
	"embed"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/buzzqw/indexarr/internal/definition"
	"github.com/buzzqw/indexarr/internal/store"
)

//go:embed catalog.yaml definitions/*.yaml
var builtinFS embed.FS

const (
	manifestFile   = "catalog.yaml"
	manifestSchema = 1

	// OriginBuiltin marks a definition written by the embedded catalog.
	OriginBuiltin = "builtin"
	// OriginLocal marks a definition created or edited by the user.
	OriginLocal = "local"
)

var (
	slugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	validStates = map[string]bool{
		"bozza":      true,
		"verificata": true,
		"stabile":    true,
		"degradata":  true,
	}
)

// Entry is one validated built-in definition together with its manifest
// metadata and content hash.
type Entry struct {
	ID         string
	State      string
	Author     string
	Body       []byte
	Definition *definition.Definition
	Hash       string
}

type manifest struct {
	Schema      int             `yaml:"schema"`
	Definitions []manifestEntry `yaml:"definitions"`
}

type manifestEntry struct {
	ID     string `yaml:"id"`
	File   string `yaml:"file"`
	State  string `yaml:"state"`
	Author string `yaml:"author"`
}

// Builtins parses the embedded manifest and definitions, validates every
// definition and returns the entries in manifest order.
func Builtins() ([]Entry, error) {
	return load(builtinFS)
}

// Get returns the built-in entry with the given id.
func Get(id string) (Entry, bool) {
	entries, err := Builtins()
	if err != nil {
		return Entry{}, false
	}
	for _, entry := range entries {
		if entry.ID == id {
			return entry, true
		}
	}
	return Entry{}, false
}

// ValidateAll parses and validates every embedded definition. It is a compile
// and start-up guard: an invalid built-in must never reach the store.
func ValidateAll() error {
	if _, err := Builtins(); err != nil {
		return err
	}
	return nil
}

// Sync writes the built-in definitions into st. Definitions with origin
// "local" are never touched; built-in definitions are updated only when their
// stored hash differs from the embedded one. It returns the number of rows
// written, in manifest order.
func Sync(ctx context.Context, st *store.Store) (int, error) {
	if st == nil {
		return 0, errors.New("catalog: nil store")
	}
	entries, err := Builtins()
	if err != nil {
		return 0, err
	}
	existing, err := st.ListDefinitions(ctx)
	if err != nil {
		return 0, fmt.Errorf("catalog: list definitions: %w", err)
	}
	byID := make(map[string]store.Definition, len(existing))
	for _, def := range existing {
		byID[def.ID] = def
	}

	written := 0
	for _, entry := range entries {
		if current, ok := byID[entry.ID]; ok {
			if current.Origin == OriginLocal {
				continue
			}
			if current.Hash == entry.Hash {
				continue
			}
		}
		err := st.UpsertDefinition(ctx, store.Definition{
			ID:            entry.ID,
			SchemaVersion: entry.Definition.Schema,
			Family:        entry.Definition.Connector,
			State:         entry.State,
			Origin:        OriginBuiltin,
			Author:        entry.Author,
			Body:          string(entry.Body),
			Hash:          entry.Hash,
		})
		if err != nil {
			return written, fmt.Errorf("catalog: sync %s: %w", entry.ID, err)
		}
		written++
	}
	return written, nil
}

// load reads the manifest and definitions from fsys. Tests use it with an
// in-memory filesystem to cover invalid manifests and definitions.
func load(fsys fs.FS) ([]Entry, error) {
	body, err := fs.ReadFile(fsys, manifestFile)
	if err != nil {
		return nil, fmt.Errorf("catalog: read manifest: %w", err)
	}
	items, err := parseManifest(body)
	if err != nil {
		return nil, err
	}

	entries := make([]Entry, 0, len(items))
	for _, item := range items {
		defBody, err := fs.ReadFile(fsys, item.File)
		if err != nil {
			return nil, fmt.Errorf("catalog: read %s: %w", item.File, err)
		}
		def, err := definition.Parse(defBody)
		if err != nil {
			return nil, fmt.Errorf("catalog: %s: %w", item.File, err)
		}
		if def.IsDerived() {
			return nil, fmt.Errorf("catalog: %s: derived drafts (derived_from=%q, author=%q) cannot enter the catalog; rewrite the definition from observed fixtures",
				item.File, def.Meta.DerivedFrom, def.Meta.Author)
		}
		if def.Meta.ID != item.ID {
			return nil, fmt.Errorf("catalog: %s: meta.id %q does not match manifest id %q",
				item.File, def.Meta.ID, item.ID)
		}
		if base := strings.TrimSuffix(path.Base(item.File), ".yaml"); base != item.ID {
			return nil, fmt.Errorf("catalog: %s: file name %q does not match id %q",
				item.File, base, item.ID)
		}
		entries = append(entries, Entry{
			ID:         item.ID,
			State:      item.State,
			Author:     item.Author,
			Body:       defBody,
			Definition: def,
			Hash:       definition.Fingerprint(defBody),
		})
	}
	return entries, nil
}

// parseManifest decodes and validates the catalog manifest. Unknown keys,
// duplicate ids, unsafe file paths and unknown states are rejected.
func parseManifest(body []byte) ([]manifestEntry, error) {
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, errors.New("catalog: empty manifest")
	}

	dec := yaml.NewDecoder(bytes.NewReader(body))
	dec.KnownFields(true)
	var m manifest
	if err := dec.Decode(&m); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errors.New("catalog: empty manifest")
		}
		return nil, fmt.Errorf("catalog: manifest YAML: %w", err)
	}

	var extra yaml.Node
	err := dec.Decode(&extra)
	switch {
	case err == nil:
		return nil, fmt.Errorf("catalog: multiple YAML documents are not supported (second document at line %d)", extra.Line)
	case !errors.Is(err, io.EOF):
		return nil, fmt.Errorf("catalog: manifest YAML: %w", err)
	}

	if m.Schema != manifestSchema {
		return nil, fmt.Errorf("catalog: manifest schema must be %d (got %d)", manifestSchema, m.Schema)
	}
	if len(m.Definitions) == 0 {
		return nil, errors.New("catalog: manifest has no definitions")
	}

	seen := make(map[string]int, len(m.Definitions))
	for i, item := range m.Definitions {
		if !slugPattern.MatchString(item.ID) {
			return nil, fmt.Errorf("catalog: definitions[%d].id: must be a slug matching %s", i, slugPattern.String())
		}
		if first, ok := seen[item.ID]; ok {
			return nil, fmt.Errorf("catalog: definitions[%d].id: duplicate of definitions[%d].id (%q)", i, first, item.ID)
		}
		seen[item.ID] = i
		if !validDefinitionFile(item.File) {
			return nil, fmt.Errorf("catalog: definitions[%d].file: must be a clean relative path under definitions/ ending in .yaml", i)
		}
		if !validStates[item.State] {
			return nil, fmt.Errorf("catalog: definitions[%d].state: must be one of bozza, verificata, stabile, degradata", i)
		}
		if strings.TrimSpace(item.Author) == "" {
			return nil, fmt.Errorf("catalog: definitions[%d].author: is required", i)
		}
	}
	return m.Definitions, nil
}

func validDefinitionFile(raw string) bool {
	if raw == "" || strings.Contains(raw, `\`) || path.IsAbs(raw) {
		return false
	}
	clean := path.Clean(raw)
	if clean != raw || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return false
	}
	if !strings.HasPrefix(clean, "definitions/") || !strings.HasSuffix(clean, ".yaml") {
		return false
	}
	return true
}
