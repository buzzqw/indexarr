package family

import (
	"strings"
	"testing"

	"github.com/buzzqw/indexarr/internal/engine"
)

func TestRegister(t *testing.T) {
	registry := engine.NewRegistry()
	if err := Register(registry); err != nil {
		t.Fatalf("Register: %v", err)
	}
	for _, name := range []string{ConnectorUnit3D, ConnectorNewznab, ConnectorTorznab} {
		if _, ok := registry.Factory(name); !ok {
			t.Errorf("factory %q was not registered", name)
		}
	}

	unit3d, err := registry.NewConnector(ConnectorUnit3D, engine.Config{
		Indexer: engine.IndexerRef{Slug: "tracker"},
		Settings: map[string]string{
			"base_url":      "https://tracker.example",
			"api_key":       "unit3d-key",
			"api_token":     "ignored-when-api-key-is-set",
			"categories":    "5000, 5040",
			"user_agent":    "family-test",
			"allow_private": "false",
		},
	})
	if err != nil {
		t.Fatalf("NewConnector(unit3d): %v", err)
	}
	unit3dCaps := unit3d.Capabilities()
	if unit3dCaps.Protocol != engine.ProtocolTorrent {
		t.Errorf("unit3d protocol = %q", unit3dCaps.Protocol)
	}
	if len(unit3dCaps.Categories) != 2 || unit3dCaps.Categories[0] != 5000 || unit3dCaps.Categories[1] != 5040 {
		t.Errorf("unit3d categories = %v", unit3dCaps.Categories)
	}

	newznab, err := registry.NewConnector(ConnectorNewznab, engine.Config{
		Indexer: engine.IndexerRef{Slug: "usenet"},
		Settings: map[string]string{
			"url":        "https://indexer.example/api",
			"api_key":    "nzb-key",
			"categories": "2040",
		},
	})
	if err != nil {
		t.Fatalf("NewConnector(newznab): %v", err)
	}
	newznabCaps := newznab.Capabilities()
	if newznabCaps.Protocol != engine.ProtocolUsenet {
		t.Errorf("newznab protocol = %q", newznabCaps.Protocol)
	}
	if len(newznabCaps.Categories) != 1 || newznabCaps.Categories[0] != 2040 {
		t.Errorf("newznab categories = %v", newznabCaps.Categories)
	}

	torznab, err := registry.NewConnector(ConnectorTorznab, engine.Config{
		Indexer: engine.IndexerRef{Slug: "torrents"},
		Settings: map[string]string{
			"url":        "https://indexer.example/api",
			"api_token":  "torznab-key",
			"categories": "5000, 5040",
		},
	})
	if err != nil {
		t.Fatalf("NewConnector(torznab): %v", err)
	}
	torznabCaps := torznab.Capabilities()
	if torznabCaps.Protocol != engine.ProtocolTorrent {
		t.Errorf("torznab protocol = %q", torznabCaps.Protocol)
	}
	if len(torznabCaps.Categories) != 2 || torznabCaps.Categories[0] != 5000 || torznabCaps.Categories[1] != 5040 {
		t.Errorf("torznab categories = %v", torznabCaps.Categories)
	}

	if err := Register(registry); err == nil {
		t.Fatal("duplicate Register succeeded, want error")
	}
	if err := Register(nil); err == nil {
		t.Fatal("Register(nil) succeeded, want error")
	}
}

func TestRegisterFactoryErrors(t *testing.T) {
	registry := engine.NewRegistry()
	if err := Register(registry); err != nil {
		t.Fatalf("Register: %v", err)
	}

	_, err := registry.NewConnector(ConnectorUnit3D, engine.Config{
		Settings: map[string]string{"categories": "not-a-number"},
	})
	if err == nil || !strings.Contains(err.Error(), "categories") {
		t.Errorf("invalid categories error = %v", err)
	}

	_, err = registry.NewConnector(ConnectorNewznab, engine.Config{
		Settings: map[string]string{"categories": "2040", "api_key": "secret"},
	})
	if err == nil {
		t.Error("missing base URL did not fail")
	}

	_, err = registry.NewConnector(ConnectorTorznab, engine.Config{
		Settings: map[string]string{"categories": "not-a-number"},
	})
	if err == nil || !strings.Contains(err.Error(), "categories") {
		t.Errorf("invalid torznab categories error = %v", err)
	}

	_, err = registry.NewConnector(ConnectorTorznab, engine.Config{
		Settings: map[string]string{"categories": "5000", "api_key": "secret"},
	})
	if err == nil {
		t.Error("missing Torznab base URL did not fail")
	}
}
