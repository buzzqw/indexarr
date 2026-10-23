package indexer

import (
	"testing"

	"github.com/buzzqw/indexarr/internal/store"
)

// TestApplyDefaultsAliasesAPIKey keeps the slug setting used by definitions
// ("apikey") and the canonical name read by the family connectors ("api_key")
// in sync. Without this, a torznab-generic indexer created from the UI sends an
// empty API key and the endpoint answers 401.
func TestApplyDefaultsAliasesAPIKey(t *testing.T) {
	settings := map[string]string{"apikey": "synthetic-key"}
	applyDefaults(store.Indexer{Name: "MirCrew", Slug: "mircrew", Protocol: "torrent"}, nil, settings)
	if settings["api_key"] != "synthetic-key" {
		t.Errorf("api_key = %q, want the apikey value", settings["api_key"])
	}

	canonical := map[string]string{"api_key": "canonical-key"}
	applyDefaults(store.Indexer{}, nil, canonical)
	if canonical["apikey"] != "canonical-key" {
		t.Errorf("apikey = %q, want the api_key value", canonical["apikey"])
	}
}
