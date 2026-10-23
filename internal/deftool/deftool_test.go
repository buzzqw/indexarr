package deftool

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/buzzqw/indexarr/internal/definition"
)

const validDefinition = `schema: 1
# a comment that formatting does not preserve
meta:
  id: sample
  name: Sample
  type: public
  protocol: torrent
  urls: [https://sample.example/]
settings:
  - { name: apikey, type: password, required: true }
categories:
  - { site: "5", standard: 5040, desc: "TV HD" }
capabilities:
  search: [q]
search:
  request:
    get: /search
    params:
      q: "{{ query.keywords }}"
  response:
    type: html
    rows: { selector: "table.results tr" }
    fields:
      title: { selector: "a.title", filters: [trim] }
      download: { selector: "a.download", attr: href, type: url }
      size: { selector: "td.size", type: size }
    required: [title, download]
`

const invalidDefinition = `schema: 1
meta:
  id: Broken ID
  name: Broken
  type: public
  protocol: torrent
  urls: ["not a url"]
`

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile(%s): %v", path, err)
	}
}

func TestValidateFileValidAndInvalid(t *testing.T) {
	dir := t.TempDir()
	validPath := filepath.Join(dir, "valid.yaml")
	invalidPath := filepath.Join(dir, "invalid.yml")
	writeFile(t, validPath, validDefinition)
	writeFile(t, invalidPath, invalidDefinition)

	valid := ValidateFile(validPath)
	if !valid.OK || valid.Err != nil {
		t.Fatalf("valid result = %+v, err = %v", valid, valid.Err)
	}
	if valid.ID != "sample" {
		t.Errorf("ID = %q, want sample", valid.ID)
	}

	invalid := ValidateFile(invalidPath)
	if invalid.OK || invalid.Err == nil {
		t.Fatalf("invalid result = %+v, want a failure", invalid)
	}
	if !strings.Contains(invalid.Err.Error(), "meta.id") {
		t.Errorf("error = %q, want a meta.id problem", invalid.Err)
	}

	missing := ValidateFile(filepath.Join(dir, "missing.yaml"))
	if missing.OK || missing.Err == nil {
		t.Fatalf("missing result = %+v, want a read failure", missing)
	}
}

func TestValidatePathsDirectoryOrderAndHiddenDirs(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "b.yaml"), validDefinition)
	writeFile(t, filepath.Join(root, "a.yml"), strings.ReplaceAll(validDefinition, "sample", "sample-a"))
	writeFile(t, filepath.Join(root, "sub", "c.yaml"), strings.ReplaceAll(validDefinition, "sample", "sample-c"))
	writeFile(t, filepath.Join(root, "sub", "deep", "d.yml"), strings.ReplaceAll(validDefinition, "sample", "sample-d"))
	writeFile(t, filepath.Join(root, ".hidden", "h.yaml"), validDefinition)
	writeFile(t, filepath.Join(root, "notes.txt"), "not a definition")

	results := ValidatePaths([]string{root})
	if len(results) != 4 {
		t.Fatalf("got %d results, want 4: %+v", len(results), results)
	}
	want := []string{
		filepath.Join(root, "a.yml"),
		filepath.Join(root, "b.yaml"),
		filepath.Join(root, "sub", "c.yaml"),
		filepath.Join(root, "sub", "deep", "d.yml"),
	}
	for i, result := range results {
		if result.Path != want[i] {
			t.Errorf("result[%d].Path = %q, want %q", i, result.Path, want[i])
		}
		if !result.OK {
			t.Errorf("result[%d] failed: %v", i, result.Err)
		}
	}
}

func TestValidatePathsReportsInvalidFilesInDirectory(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "good.yaml"), validDefinition)
	writeFile(t, filepath.Join(root, "testdata", "invalid", "bad.yaml"), invalidDefinition)

	results := ValidatePaths([]string{root})
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
	if !results[0].OK || results[1].OK {
		t.Fatalf("results = %+v, want one valid and one invalid", results)
	}
}

func TestValidateOptionsRejectDerived(t *testing.T) {
	dir := t.TempDir()
	replaceMeta := func(extra string) string {
		t.Helper()
		updated := strings.Replace(validDefinition, "  urls: [https://sample.example/]\n",
			"  urls: [https://sample.example/]\n"+extra, 1)
		if updated == validDefinition {
			t.Fatal("could not extend meta in the test definition")
		}
		return updated
	}

	derivedPath := filepath.Join(dir, "derived.yaml")
	authorPath := filepath.Join(dir, "author.yaml")
	writeFile(t, derivedPath, replaceMeta("  derived_from: cardigann\n"))
	writeFile(t, authorPath, replaceMeta("  author: Cardigann-Import\n"))

	// Normal validation still accepts local derived drafts: the converter
	// documents `indexarr-def validate ./native`.
	for _, result := range ValidatePaths([]string{dir}) {
		if !result.OK {
			t.Errorf("normal validate rejected %s: %v", result.Path, result.Err)
		}
	}

	results := ValidatePathsWithOptions([]string{dir}, ValidateOptions{RejectDerived: true})
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
	for _, result := range results {
		if result.OK {
			t.Errorf("RejectDerived accepted %s", result.Path)
			continue
		}
		if result.ID == "" {
			t.Errorf("%s: ID is empty after a policy failure", result.Path)
		}
		if !strings.Contains(result.Err.Error(), "derived draft") {
			t.Errorf("%s: error = %v, want a derived-draft explanation", result.Path, result.Err)
		}
	}

	if result := ValidateFileWithOptions(derivedPath, ValidateOptions{RejectDerived: true}); result.OK || result.Err == nil {
		t.Errorf("ValidateFileWithOptions accepted a derived draft: %+v", result)
	}
}

func TestResultsAreJSONable(t *testing.T) {
	results := []Result{
		{Path: "good.yaml", ID: "sample", OK: true},
		{Path: "bad.yaml", Err: os.ErrNotExist},
	}
	body, err := json.Marshal(results)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	var decoded []struct {
		Path  string `json:"path"`
		OK    bool   `json:"ok"`
		ID    string `json:"id"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if len(decoded) != 2 {
		t.Fatalf("decoded %d results, want 2", len(decoded))
	}
	if decoded[0].Path != "good.yaml" || !decoded[0].OK || decoded[0].ID != "sample" || decoded[0].Error != "" {
		t.Errorf("decoded[0] = %+v", decoded[0])
	}
	if decoded[1].OK || decoded[1].Error == "" {
		t.Errorf("decoded[1] = %+v, want a failure with an error string", decoded[1])
	}
}

func TestFormatRoundTrip(t *testing.T) {
	formatted, err := Format([]byte(validDefinition))
	if err != nil {
		t.Fatalf("Format: %v", err)
	}
	if strings.Contains(string(formatted), "# a comment") {
		t.Error("formatted output kept a YAML comment")
	}

	def, err := definition.Parse(formatted)
	if err != nil {
		t.Fatalf("parse formatted output: %v\n%s", err, formatted)
	}
	if def.Meta.ID != "sample" {
		t.Errorf("ID = %q, want sample", def.Meta.ID)
	}

	again, err := Format(formatted)
	if err != nil {
		t.Fatalf("Format(formatted): %v", err)
	}
	if string(again) != string(formatted) {
		t.Errorf("formatting is not idempotent:\nfirst:\n%s\nsecond:\n%s", formatted, again)
	}

	if _, err := Format([]byte(invalidDefinition)); err == nil {
		t.Error("Format(invalid) returned no error")
	}
}

func TestFingerprintStability(t *testing.T) {
	body := []byte(validDefinition)
	first := definition.Fingerprint(body)
	if len(first) != 64 {
		t.Fatalf("fingerprint length = %d, want 64", len(first))
	}
	if second := definition.Fingerprint(body); second != first {
		t.Errorf("fingerprint changed for identical bytes: %s != %s", first, second)
	}

	formatted, err := Format(body)
	if err != nil {
		t.Fatalf("Format: %v", err)
	}
	formattedHash := definition.Fingerprint(formatted)
	again, err := Format(formatted)
	if err != nil {
		t.Fatalf("Format(formatted): %v", err)
	}
	if definition.Fingerprint(again) != formattedHash {
		t.Error("fingerprint of canonical output is not stable")
	}
	if definition.Fingerprint([]byte(validDefinition+"\n")) == first {
		t.Error("fingerprint ignored a byte change")
	}
}

func TestCheckFixtures(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "fixtures", "ok.html"), "<html>ok</html>")
	writeFile(t, filepath.Join(dir, "fixtures", "empty.html"), "")

	body := `schema: 1
meta:
  id: fixtures
  name: Fixtures
  type: public
  protocol: torrent
  urls: [https://fixtures.example/]
capabilities: {search: [q]}
search:
  request: {get: /search}
  response:
    type: html
    rows: { selector: "table tr" }
    fields:
      title: { selector: "a.title" }
tests:
  - name: ok
    fixture: fixtures/ok.html
  - name: missing
    fixture: fixtures/missing.html
  - name: empty
    fixture: fixtures/empty.html
`
	path := filepath.Join(dir, "fixtures.yaml")
	writeFile(t, path, body)

	results, err := CheckFixtures(path)
	if err != nil {
		t.Fatalf("CheckFixtures: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("got %d results, want 3", len(results))
	}
	if !results[0].OK || results[0].Size == 0 {
		t.Errorf("existing fixture = %+v, want ok with a size", results[0])
	}
	if results[0].Path != filepath.Join(dir, "fixtures", "ok.html") {
		t.Errorf("fixture path = %q, want it resolved relative to the definition", results[0].Path)
	}
	if results[1].OK || results[1].Err == nil {
		t.Errorf("missing fixture = %+v, want a failure", results[1])
	}
	if results[2].OK || results[2].Size != 0 || results[2].Err == nil {
		t.Errorf("empty fixture = %+v, want an empty failure", results[2])
	}
}

func TestCheckFixturesRejectsParentTraversal(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "outside.html"), "secret")
	body := strings.Replace(validDefinition, "capabilities:", `tests:
  - name: escape
    fixture: ../outside.html

capabilities:`, 1)
	path := filepath.Join(dir, "escape.yaml")
	writeFile(t, path, body)

	if _, err := CheckFixtures(path); err == nil {
		t.Fatal("CheckFixtures accepted a ../ fixture")
	}
}

func TestCheckFixturesWithoutTests(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "plain.yaml")
	writeFile(t, path, validDefinition)

	results, err := CheckFixtures(path)
	if err != nil {
		t.Fatalf("CheckFixtures: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("got %d results, want 0", len(results))
	}
}
