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
	for _, name := range []string{
		ConnectorUnit3D,
		ConnectorNewznab,
		ConnectorTorznab,
		ConnectorGazelle,
		ConnectorNexusPHP,
	} {
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

	gazelle, err := registry.NewConnector(ConnectorGazelle, engine.Config{
		Indexer: engine.IndexerRef{Slug: "gazelle"},
		Settings: map[string]string{
			"base_url":   "https://gazelle.example",
			"apikey":     "gazelle-key",
			"categories": "3000",
		},
	})
	if err != nil {
		t.Fatalf("NewConnector(gazelle): %v", err)
	}
	gazelleCaps := gazelle.Capabilities()
	if gazelleCaps.Protocol != engine.ProtocolTorrent {
		t.Errorf("gazelle protocol = %q", gazelleCaps.Protocol)
	}
	if len(gazelleCaps.Categories) != 1 || gazelleCaps.Categories[0] != 3000 {
		t.Errorf("gazelle categories = %v", gazelleCaps.Categories)
	}

	nexus, err := registry.NewConnector(ConnectorNexusPHP, engine.Config{
		Indexer: engine.IndexerRef{Slug: "nexus"},
		Settings: map[string]string{
			"url":        "https://nexus.example",
			"username":   "user",
			"password":   "pass",
			"passkey":    "pk",
			"categories": "401, 402",
		},
	})
	if err != nil {
		t.Fatalf("NewConnector(nexusphp): %v", err)
	}
	nexusCaps := nexus.Capabilities()
	if nexusCaps.Protocol != engine.ProtocolTorrent {
		t.Errorf("nexusphp protocol = %q", nexusCaps.Protocol)
	}
	if len(nexusCaps.Categories) != 2 || nexusCaps.Categories[0] != 401 || nexusCaps.Categories[1] != 402 {
		t.Errorf("nexusphp categories = %v", nexusCaps.Categories)
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

	_, err = registry.NewConnector(ConnectorGazelle, engine.Config{
		Settings: map[string]string{"categories": "not-a-number"},
	})
	if err == nil || !strings.Contains(err.Error(), "categories") {
		t.Errorf("invalid gazelle categories error = %v", err)
	}
	_, err = registry.NewConnector(ConnectorGazelle, engine.Config{
		Settings: map[string]string{"base_url": "https://gazelle.example"},
	})
	if err == nil {
		t.Error("missing gazelle API key did not fail")
	}

	_, err = registry.NewConnector(ConnectorNexusPHP, engine.Config{
		Settings: map[string]string{"categories": "not-a-number"},
	})
	if err == nil || !strings.Contains(err.Error(), "categories") {
		t.Errorf("invalid nexusphp categories error = %v", err)
	}
	_, err = registry.NewConnector(ConnectorNexusPHP, engine.Config{
		Settings: map[string]string{"username": "user", "password": "pass"},
	})
	if err == nil {
		t.Error("missing NexusPHP base URL did not fail")
	}
}
