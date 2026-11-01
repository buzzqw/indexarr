package catalog_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/buzzqw/indexarr/internal/catalog"
	"github.com/buzzqw/indexarr/internal/store"
)

// remoteDefinition is a minimal but valid native definition.
func remoteDefinition(id, name string) string {
	return fmt.Sprintf(`schema: 1
meta:
  id: %s
  name: %s
  type: public
  protocol: torrent
  urls:
    - https://%s.example/
capabilities:
  search: [q]
search:
  request:
    get: /
  response:
    type: html
    rows: { selector: "tr" }
    fields:
      title: { selector: "a" }
`, id, name, id)
}

// remoteCatalog serves index.json and definitions/ from a map.
func remoteCatalog(t *testing.T, entries string, docs map[string]string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/catalog/index.json" {
			_, _ = w.Write([]byte(entries))
			return
		}
		if id, ok := trimDefinitionPath(r.URL.Path); ok {
			body, found := docs[id]
			if !found {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write([]byte(body))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)
	return server
}

func trimDefinitionPath(path string) (string, bool) {
	const prefix = "/catalog/definitions/"
	if len(path) <= len(prefix)+len(".yaml") || path[:len(prefix)] != prefix {
		return "", false
	}
	rest := path[len(prefix):]
	if rest[len(rest)-5:] != ".yaml" {
		return "", false
	}
	return rest[:len(rest)-5], true
}

// TestImportRemoteCatalogWritesDefinitions is the "a site fix without a
// release" path: the manifest and the YAML are read from an address, every
// document is validated, and the origin marks it so the embedded catalog
// leaves it alone.
func TestImportRemoteCatalogWritesDefinitions(t *testing.T) {
	ctx := context.Background()
	st := newCatalogStore(t)

	entries := `[
	  {"id":"alpha","name":"Alpha","state":"verificata"},
	  {"id":"beta","name":"Beta","state":"degradata"},
	  {"id":"dead","name":"Dead","state":"morta"},
	  {"id":"broken","name":"Broken","state":"verificata"},
	  {"id":"mismatch","name":"Mismatch","state":"verificata"}
	]`
	docs := map[string]string{
		"alpha": remoteDefinition("alpha", "Alpha"),
		"beta":  remoteDefinition("beta", "Beta"),
		"dead":  remoteDefinition("dead", "Dead"),
		// Not YAML at all: must be reported, not stored.
		"broken": "schema: [",
		// Declares another id: must be rejected.
		"mismatch": remoteDefinition("other", "Other"),
	}
	server := remoteCatalog(t, entries, docs)

	result, err := catalog.Import(ctx, st, server.URL+"/catalog")
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if result.Entries != 5 || result.Written != 2 || result.Skipped != 1 {
		t.Fatalf("result = %+v, want 5 entries, 2 written and the dead site skipped", result)
	}
	if len(result.Problems) != 2 {
		t.Fatalf("problems = %v, want the two rejected entries", result.Problems)
	}

	alpha, err := st.GetDefinition(ctx, "alpha")
	if err != nil {
		t.Fatalf("GetDefinition(alpha): %v", err)
	}
	if alpha.Origin != catalog.OriginRemote || alpha.State != "verificata" {
		t.Fatalf("alpha = origin %q state %q", alpha.Origin, alpha.State)
	}
	beta, err := st.GetDefinition(ctx, "beta")
	if err != nil {
		t.Fatalf("GetDefinition(beta): %v", err)
	}
	if beta.State != "degradata" {
		t.Errorf("beta state = %q, want the manifest value", beta.State)
	}
	if _, err := st.GetDefinition(ctx, "dead"); err == nil {
		t.Error("the dead site was installed")
	}

	// A second run finds nothing to write.
	result, err = catalog.Import(ctx, st, server.URL+"/catalog")
	if err != nil {
		t.Fatalf("Import again: %v", err)
	}
	if result.Written != 0 || result.Unchanged != 2 {
		t.Fatalf("second run = %+v, want 0 written and 2 unchanged", result)
	}

	// The embedded catalog must not undo the import.
	if _, err := catalog.Sync(ctx, st); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	alpha, err = st.GetDefinition(ctx, "alpha")
	if err != nil {
		t.Fatalf("GetDefinition after Sync: %v", err)
	}
	if alpha.Origin != catalog.OriginRemote {
		t.Fatalf("Sync replaced the remote definition: origin %q", alpha.Origin)
	}
}

// TestImportKeepsLocalDefinitions proves the operator always wins: a
// definition created or edited by hand is counted and left untouched.
func TestImportKeepsLocalDefinitions(t *testing.T) {
	ctx := context.Background()
	st := newCatalogStore(t)
	if err := st.UpsertDefinition(ctx, store.Definition{
		ID: "alpha", SchemaVersion: 1, State: "verificata", Origin: catalog.OriginLocal,
		Author: "me", Body: remoteDefinition("alpha", "Mine"), Hash: "local-hash",
	}); err != nil {
		t.Fatalf("UpsertDefinition: %v", err)
	}

	server := remoteCatalog(t, `[{"id":"alpha","name":"Alpha","state":"verificata"}]`,
		map[string]string{"alpha": remoteDefinition("alpha", "Theirs")})

	result, err := catalog.Import(ctx, st, server.URL+"/catalog")
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if result.Local != 1 || result.Written != 0 {
		t.Fatalf("result = %+v, want the local entry counted and skipped", result)
	}
	stored, err := st.GetDefinition(ctx, "alpha")
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	if stored.Origin != catalog.OriginLocal || stored.Author != "me" {
		t.Fatalf("the local definition changed: %+v", stored)
	}
}

// TestImportRejectsUnsafeIDs keeps the manifest from reaching outside
// definitions/: the id becomes a path segment.
func TestImportRejectsUnsafeIDs(t *testing.T) {
	ctx := context.Background()
	st := newCatalogStore(t)
	server := remoteCatalog(t,
		`[{"id":"../etc/passwd","name":"x","state":"verificata"},{"id":"../secret","name":"y","state":"verificata"}]`,
		map[string]string{})

	result, err := catalog.Import(ctx, st, server.URL+"/catalog")
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if result.Written != 0 || len(result.Problems) != 2 {
		t.Fatalf("result = %+v, want both ids rejected", result)
	}
}

// TestImportRejectsBadURL is the basic input check of the endpoint.
func TestImportRejectsBadURL(t *testing.T) {
	st := newCatalogStore(t)
	for _, raw := range []string{"", "ftp://example/catalog", "not a url"} {
		if _, err := catalog.Import(context.Background(), st, raw); err == nil {
			t.Errorf("Import(%q) accepted the address", raw)
		}
	}
}

// newCatalogStore opens a temporary store. The catalog tests already have a
// store helper for the embedded catalog; this keeps the remote tests
// independent of it.
func newCatalogStore(t *testing.T) *store.Store {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "indexarr.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() {
		_ = st.Close()
		_ = os.RemoveAll(dir)
	})
	return st
}
