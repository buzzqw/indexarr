package catalog_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/buzzqw/indexarr/internal/definition"
	"github.com/buzzqw/indexarr/internal/feed"
)

// TestSukebeiFeedFixture reads the real Sukebei feed captured on 2026-10-25
// and checks that the generic RSS parser turns every item's .torrent link into
// a TorrentURL. Sukebei has no magnet and no enclosure: the download URL only
// sits in <link>, which is exactly the path the rss connector relies on.
func TestSukebeiFeedFixture(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("definitions", "fixtures", "sukebei", "feed.xml"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	items, err := feed.ParseRSS(body)
	if err != nil {
		t.Fatalf("feed.ParseRSS: %v", err)
	}
	if len(items) < 4 {
		t.Fatalf("items = %d, want at least 4", len(items))
	}
	for i, item := range items {
		if strings.TrimSpace(item.Title) == "" {
			t.Errorf("items[%d]: empty title", i)
		}
		if !strings.HasSuffix(strings.ToLower(item.TorrentURL), ".torrent") {
			t.Errorf("items[%d] %q: TorrentURL = %q, want a .torrent link", i, item.Title, item.TorrentURL)
		}
	}
}

// TestSukebeiDefinition keeps the YAML aligned with the fixture: the rss
// connector, the adult default category 6000 and a test block pointing at the
// captured feed.
func TestSukebeiDefinition(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("definitions", "sukebei.yaml"))
	if err != nil {
		t.Fatalf("read definition: %v", err)
	}
	def, err := definition.Parse(body)
	if err != nil {
		t.Fatalf("definition.Parse: %v", err)
	}
	if def.Meta.ID != "sukebei" || def.Meta.Name != "Sukebei" {
		t.Errorf("meta = %q/%q, want sukebei/Sukebei", def.Meta.ID, def.Meta.Name)
	}
	if def.Connector != "rss" {
		t.Errorf("connector = %q, want rss", def.Connector)
	}
	var defaultCategory string
	for _, setting := range def.Settings {
		if setting.Name == "default-category" {
			defaultCategory = setting.Default
		}
	}
	if defaultCategory != "6000" {
		t.Errorf("default-category = %q, want 6000", defaultCategory)
	}
	if len(def.Tests) == 0 || def.Tests[0].Fixture != "fixtures/sukebei/feed.xml" {
		t.Errorf("tests = %+v, want the sukebei feed fixture", def.Tests)
	}
}
