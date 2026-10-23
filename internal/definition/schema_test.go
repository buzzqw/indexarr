package definition

import (
	"encoding/json"
	"testing"
)

func TestJSONSchema(t *testing.T) {
	body := JSONSchema()
	if len(body) == 0 {
		t.Fatal("JSONSchema returned an empty document")
	}
	if !json.Valid(body) {
		t.Fatal("JSONSchema is not valid JSON")
	}

	var doc struct {
		Schema   string         `json:"$schema"`
		ID       string         `json:"$id"`
		Title    string         `json:"title"`
		Type     string         `json:"type"`
		Required []string       `json:"required"`
		Defs     map[string]any `json:"$defs"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if doc.Schema != "https://json-schema.org/draft/2020-12/schema" {
		t.Errorf("$schema = %q", doc.Schema)
	}
	if doc.ID == "" {
		t.Error("$id is missing")
	}
	if doc.Title == "" {
		t.Error("title is missing")
	}
	if doc.Type != "object" {
		t.Errorf("type = %q, want object", doc.Type)
	}
	if len(doc.Required) == 0 {
		t.Error("top-level required is empty")
	}
	for _, name := range []string{"meta", "setting", "auth", "search", "download", "downloadTarget", "field", "filter", "rows", "selector"} {
		if _, ok := doc.Defs[name]; !ok {
			t.Errorf("$defs is missing %q", name)
		}
	}

	// The returned slice must be a copy.
	body[0] = 'x'
	if JSONSchema()[0] == 'x' {
		t.Error("JSONSchema returned a mutable shared buffer")
	}
}
