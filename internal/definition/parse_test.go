package definition

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestParseExample(t *testing.T) {
	def, err := Load("testdata/example.yaml")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if def.Schema != CurrentSchema {
		t.Errorf("schema = %d, want %d", def.Schema, CurrentSchema)
	}
	if def.Meta.ID != "example-tracker" || def.Meta.Name != "Example Tracker" {
		t.Errorf("unexpected meta: %+v", def.Meta)
	}
	if def.Meta.Protocol != "torrent" || def.Meta.Type != "private" {
		t.Errorf("unexpected meta type/protocol: %+v", def.Meta)
	}
	if len(def.Meta.URLs) != 2 {
		t.Errorf("urls = %v, want 2 entries", def.Meta.URLs)
	}
	if len(def.Settings) != 2 || def.Settings[0].Name != "username" {
		t.Errorf("unexpected settings: %+v", def.Settings)
	}
	if def.Auth == nil {
		t.Fatal("auth is nil")
	}
	if def.Auth.Strategy != "form" {
		t.Errorf("auth.strategy = %q, want form", def.Auth.Strategy)
	}
	if got := def.Auth.RefreshAfter.Duration(); got != 6*time.Hour {
		t.Errorf("refresh_after = %v, want 6h", got)
	}
	if len(def.Auth.Steps) != 2 {
		t.Fatalf("auth steps = %d, want 2", len(def.Auth.Steps))
	}
	if def.Auth.Steps[0].Get != "/login.php" || def.Auth.Steps[1].Post != "/login.php" {
		t.Errorf("unexpected auth steps: %+v", def.Auth.Steps)
	}

	if got := def.Search.Response.Rows.Selector; !reflect.DeepEqual(got, Selector{"table.torrents > tbody > tr"}) {
		t.Errorf("rows selector = %v", got)
	}
	title := def.Search.Response.Fields["title"]
	if !reflect.DeepEqual(title.Selector, Selector{"a.title", "td.name a"}) {
		t.Errorf("title selector = %v", title.Selector)
	}
	if len(title.Filters) != 1 || title.Filters[0].Name != "trim" || title.Filters[0].Arg != "" {
		t.Errorf("title filters = %+v", title.Filters)
	}
	category := def.Search.Response.Fields["category"]
	if len(category.Filters) != 1 || category.Filters[0].Name != "query_param" || category.Filters[0].Arg != "cat" {
		t.Errorf("category filters = %+v", category.Filters)
	}
	if !reflect.DeepEqual(def.Search.Response.Required, []string{"title", "download", "size"}) {
		t.Errorf("required = %v", def.Search.Response.Required)
	}
}

func TestSelectorUnmarshal(t *testing.T) {
	var doc struct {
		Selector Selector `yaml:"selector"`
	}
	cases := []struct {
		name string
		yaml string
		want Selector
	}{
		{"scalar", "selector: a.title\n", Selector{"a.title"}},
		{"sequence", "selector:\n  - a.title\n  - td.name a\n", Selector{"a.title", "td.name a"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc.Selector = nil
			if err := yaml.Unmarshal([]byte(tc.yaml), &doc); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if !reflect.DeepEqual(doc.Selector, tc.want) {
				t.Errorf("selector = %#v, want %#v", doc.Selector, tc.want)
			}
		})
	}

	if err := yaml.Unmarshal([]byte("selector: {a: b}\n"), &doc); err == nil {
		t.Fatal("expected an error for a mapping selector")
	}
}

func TestFilterUnmarshal(t *testing.T) {
	var doc struct {
		Filter Filter `yaml:"filter"`
	}
	cases := []struct {
		name string
		yaml string
		want Filter
	}{
		{"scalar", "filter: trim\n", Filter{Name: "trim"}},
		{"mapping", "filter: {query_param: cat}\n", Filter{Name: "query_param", Arg: "cat"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc.Filter = Filter{}
			if err := yaml.Unmarshal([]byte(tc.yaml), &doc); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if doc.Filter != tc.want {
				t.Errorf("filter = %+v, want %+v", doc.Filter, tc.want)
			}
		})
	}

	for _, body := range []string{
		"filter: {a: b, c: d}\n",
		"filter: [a, b]\n",
	} {
		if err := yaml.Unmarshal([]byte(body), &doc); err == nil {
			t.Errorf("expected an error for %q", body)
		}
	}
}

func TestRowSelectorUnmarshal(t *testing.T) {
	var doc struct {
		Rows RowSelector `yaml:"rows"`
	}
	cases := []struct {
		name string
		yaml string
		want RowSelector
	}{
		{"selector mapping", "rows: {selector: 'table tr'}\n", RowSelector{Selector: Selector{"table tr"}}},
		{"path mapping", "rows: {path: '$.results'}\n", RowSelector{Path: "$.results"}},
		{
			"selector and path",
			"rows: {selector: 'table tr', path: '$.results'}\n",
			RowSelector{Selector: Selector{"table tr"}, Path: "$.results"},
		},
		{"scalar shorthand", "rows: 'table tr'\n", RowSelector{Selector: Selector{"table tr"}}},
		{"sequence shorthand", "rows: ['table tr', 'tbody tr']\n", RowSelector{Selector: Selector{"table tr", "tbody tr"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc.Rows = RowSelector{}
			if err := yaml.Unmarshal([]byte(tc.yaml), &doc); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if !reflect.DeepEqual(doc.Rows, tc.want) {
				t.Errorf("rows = %+v, want %+v", doc.Rows, tc.want)
			}
		})
	}

	if err := yaml.Unmarshal([]byte("rows: {bogus: x}\n"), &doc); err == nil {
		t.Fatal("expected an error for an unknown rows key")
	}
}

func TestDurationUnmarshal(t *testing.T) {
	var doc struct {
		Refresh Duration `yaml:"refresh_after"`
	}
	cases := []struct {
		name string
		yaml string
		want time.Duration
	}{
		{"hours", "refresh_after: 6h\n", 6 * time.Hour},
		{"compound", "refresh_after: 1h30m\n", 90 * time.Minute},
		{"empty string", "refresh_after: \"\"\n", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc.Refresh = -1
			if err := yaml.Unmarshal([]byte(tc.yaml), &doc); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if doc.Refresh.Duration() != tc.want {
				t.Errorf("duration = %v, want %v", doc.Refresh.Duration(), tc.want)
			}
		})
	}

	if err := yaml.Unmarshal([]byte("refresh_after: soon\n"), &doc); err == nil {
		t.Fatal("expected an error for an invalid duration")
	}

	var fresh struct {
		Refresh Duration `yaml:"refresh_after"`
	}
	if err := yaml.Unmarshal([]byte("refresh_after:\n"), &fresh); err != nil {
		t.Fatalf("empty duration should be accepted: %v", err)
	}
	if fresh.Refresh != 0 {
		t.Errorf("empty duration = %v, want 0", fresh.Refresh.Duration())
	}
}

func TestParseStrictUnknownField(t *testing.T) {
	base := `schema: 1
meta:
  id: strict
  name: Strict
  type: public
  protocol: torrent
  urls: [https://strict.example/]
`
	cases := []struct {
		name string
		body string
	}{
		{"top level", base + "bogus: true\n"},
		{"nested meta", `schema: 1
meta:
  id: strict
  name: Strict
  type: public
  protocol: torrent
  urls: [https://strict.example/]
  bogus: true
`},
		{"nested search response", base + `search:
  request:
    get: /search
  response:
    type: html
    rows: "table tr"
    bogus: true
`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.body))
			if err == nil {
				t.Fatal("expected strict decoding to fail")
			}
			if !strings.Contains(err.Error(), "bogus") {
				t.Errorf("error does not mention the unknown key: %v", err)
			}
			if !strings.Contains(err.Error(), "line") {
				t.Errorf("error does not carry line context: %v", err)
			}
		})
	}
}

func TestParseRejectsMultipleDocuments(t *testing.T) {
	body := `schema: 1
meta:
  id: one
  name: One
  type: public
  protocol: torrent
  urls: [https://one.example/]
---
schema: 1
meta:
  id: two
  name: Two
  type: public
  protocol: torrent
  urls: [https://two.example/]
`
	if _, err := Parse([]byte(body)); err == nil || !strings.Contains(err.Error(), "multiple YAML documents") {
		t.Fatalf("expected multiple-document error, got %v", err)
	}
}

func TestFingerprint(t *testing.T) {
	const want = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	if got := Fingerprint([]byte("abc")); got != want {
		t.Errorf("Fingerprint(abc) = %q, want %q", got, want)
	}

	body := []byte("schema: 1\n")
	if Fingerprint(body) != Fingerprint(body) {
		t.Error("fingerprint is not stable")
	}
	if Fingerprint(body) == Fingerprint([]byte("schema: 1\n\n")) {
		t.Error("fingerprint did not change with content")
	}
}

func TestAllTestdataDefinitionsValidate(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("testdata", "*.yaml"))
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}
	if len(files) < 3 {
		t.Fatalf("expected several testdata definitions, got %d", len(files))
	}

	for _, file := range files {
		t.Run(filepath.Base(file), func(t *testing.T) {
			if _, err := Load(file); err != nil {
				t.Errorf("Load(%s): %v", file, err)
			}
		})
	}
}
