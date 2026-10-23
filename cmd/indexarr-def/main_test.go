package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const plainDefinition = `schema: 1
connector: torznab
meta:
  id: plain
  name: Plain
  type: public
  protocol: torrent
  urls: [https://plain.example/]
  author: indexarr
`

const derivedDefinition = `schema: 1
connector: torznab
meta:
  id: derived
  name: Derived
  type: public
  protocol: torrent
  urls: [https://derived.example/]
  author: indexarr
  derived_from: cardigann
`

func writeDefinition(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

func TestValidateCatalogRejectsDerived(t *testing.T) {
	dir := t.TempDir()
	writeDefinition(t, dir, "plain.yaml", plainDefinition)
	derivedPath := writeDefinition(t, dir, "derived.yaml", derivedDefinition)

	var stdout, stderr bytes.Buffer
	if code := run([]string{"validate", dir}, &stdout, &stderr); code != 0 {
		t.Fatalf("normal validate exit = %d, want 0\nstdout: %s\nstderr: %s", code, stdout.String(), stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"validate", "-catalog", dir}, &stdout, &stderr); code != 1 {
		t.Fatalf("validate -catalog exit = %d, want 1\nstdout: %s\nstderr: %s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "FAIL") || !strings.Contains(stdout.String(), "derived draft") {
		t.Errorf("validate -catalog output does not explain the failure:\n%s", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"validate", "-reject-derived", derivedPath}, &stdout, &stderr); code != 1 {
		t.Fatalf("validate -reject-derived exit = %d, want 1\nstdout: %s\nstderr: %s", code, stdout.String(), stderr.String())
	}
}

func TestValidateCatalogAcceptsCleanDirectory(t *testing.T) {
	dir := t.TempDir()
	writeDefinition(t, dir, "plain.yaml", plainDefinition)

	var stdout, stderr bytes.Buffer
	if code := run([]string{"validate", "-catalog", dir}, &stdout, &stderr); code != 0 {
		t.Fatalf("validate -catalog exit = %d, want 0\nstdout: %s\nstderr: %s", code, stdout.String(), stderr.String())
	}
}

func TestHelpDescribesWritingAid(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"help"}, &stdout, &stderr); code != 0 {
		t.Fatalf("help exit = %d, want 0", code)
	}
	help := stdout.String()
	for _, want := range []string{"writing aid", "not a migration", "derived_from", "-catalog"} {
		if !strings.Contains(help, want) {
			t.Errorf("help does not mention %q:\n%s", want, help)
		}
	}
}
