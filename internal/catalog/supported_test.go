package catalog

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSupportedDocsMatchesCatalog fails when a definition is added, removed or
// changes state without regenerating docs/SUPPORTED.md. The rule is in
// AGENTS.md: supporting another provider updates the document that lists them.
// The fix is one command:
//
//	go generate ./internal/catalog
func TestSupportedDocsMatchesCatalog(t *testing.T) {
	entries, err := Builtins()
	if err != nil {
		t.Fatalf("Builtins: %v", err)
	}
	want := RenderSupportedDocs(entries)
	path := filepath.Join("..", "..", "docs", "SUPPORTED.md")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v (run go generate ./internal/catalog)", path, err)
	}
	// A Windows checkout can give the file CRLF; the generated document is LF.
	got := bytes.ReplaceAll(body, []byte("\r\n"), []byte("\n"))
	if !bytes.Equal(got, want) {
		t.Fatalf("docs/SUPPORTED.md is stale: run go generate ./internal/catalog")
	}
}

// TestSupportedDocsListsEverySiteDefinition guards the purpose of the
// document: a new definition that the generator skips (because it is
// misclassified as generic, for example) would silently disappear from the
// list.
func TestSupportedDocsListsEverySiteDefinition(t *testing.T) {
	entries, err := Builtins()
	if err != nil {
		t.Fatalf("Builtins: %v", err)
	}
	doc := string(RenderSupportedDocs(entries))
	for _, entry := range entries {
		if isGeneric(entry) {
			continue
		}
		name := entry.Definition.Meta.Name
		if !strings.Contains(doc, "| "+name+" |") && !strings.Contains(doc, "["+name+"](") {
			t.Errorf("%s (%s) is not in the supported list", entry.ID, name)
		}
	}
}

// TestSupportedDocsKeepsKnownSites spots an accidental exclusion of the
// best-known public sites, which are the ones the README names.
func TestSupportedDocsKeepsKnownSites(t *testing.T) {
	entries, err := Builtins()
	if err != nil {
		t.Fatalf("Builtins: %v", err)
	}
	doc := string(RenderSupportedDocs(entries))
	for _, name := range []string{"The Pirate Bay", "1337x", "YTS", "EZTV", "Nyaa", "TorrentGalaxy", "MirCrew", "IPTorrents"} {
		if !strings.Contains(doc, name) {
			t.Errorf("%q is missing from docs/SUPPORTED.md", name)
		}
	}
}
