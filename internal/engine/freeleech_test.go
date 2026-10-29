package engine

import (
	"testing"

	"github.com/buzzqw/indexarr/internal/definition"
)

func TestParseFreeleechMarker(t *testing.T) {
	for raw, want := range map[string]bool{
		"":          false,
		"0":         false,
		"false":     false,
		"no":        false,
		"off":       false,
		"FREE":      true,
		"Freeleech": true,
		"1":         true,
		"yes":       true,
	} {
		if got := parseFreeleech(raw); got != want {
			t.Errorf("parseFreeleech(%q) = %v, want %v", raw, got, want)
		}
	}
}

func TestFreeleechOnlyDropsRowsWithoutTheMarker(t *testing.T) {
	def := loadTestDefinition(t, "pagination.yaml", "http://127.0.0.1:1")
	def.Search.Response.Fields["freeleech"] = definition.Field{Selector: definition.Selector{".free"}}
	connector, err := NewDefinitionConnector(DefinitionConfig{
		Definition: def,
		Settings:   map[string]string{"freeleech": "true"},
	})
	if err != nil {
		t.Fatal(err)
	}

	free, ok, err := connector.normalizeRow(map[string]string{
		"title":     "Free release",
		"details":   "/details/1",
		"download":  "/download/1.torrent",
		"freeleech": "FREE",
	}, nil)
	if err != nil || !ok || !free.Freeleech {
		t.Fatalf("free row = %+v, ok=%v, err=%v", free, ok, err)
	}
	paid, ok, err := connector.normalizeRow(map[string]string{
		"title":    "Paid release",
		"details":  "/details/2",
		"download": "/download/2.torrent",
	}, nil)
	if err != nil || !ok || paid.Freeleech {
		t.Fatalf("paid row = %+v, ok=%v, err=%v", paid, ok, err)
	}
	if !connector.keepsFreeleech(free, def.Search.Response.Fields) {
		t.Error("free release dropped")
	}
	if connector.keepsFreeleech(paid, def.Search.Response.Fields) {
		t.Error("paid release kept with freeleech-only on")
	}

	// A definition that filters on the site side (no row marker) is never
	// filtered again, otherwise every row would disappear.
	connector.settings["freeleech"] = "false"
	if !connector.keepsFreeleech(paid, def.Search.Response.Fields) {
		t.Error("paid release dropped with the option off")
	}
	delete(def.Search.Response.Fields, "freeleech")
	connector.settings["freeleech"] = "true"
	if !connector.keepsFreeleech(paid, def.Search.Response.Fields) {
		t.Error("site-side freeleech filter must not drop rows")
	}
}
