package web

import (
	"os"
	"strings"
	"testing"

	"github.com/buzzqw/indexarr/internal/i18n"
)

// TestEveryPageHelpKeyExistsInEveryLanguage keeps the explanatory paragraph of
// §2 complete. The map lists the keys pageHelpKey can return; a new page must
// add its key here and in all seven catalogs.
func TestEveryPageHelpKeyExistsInEveryLanguage(t *testing.T) {
	keys := []string{
		"home", "search", "release", "indexers", "indexer_add", "indexer_detail",
		"indexer_form", "app", "app_form", "client_form", "activity", "settings",
		"import", "setup", "definitions", "definition", "definition_form",
		"maintenance", "rules", "run", "discover",
	}
	for _, lang := range i18n.Supported() {
		if got := i18n.T(lang, "pageHelp.title"); got == "" || got == "pageHelp.title" {
			t.Errorf("%s: pageHelp.title is missing", lang)
		}
		for _, key := range keys {
			value := i18n.T(lang, "pageHelp."+key)
			if strings.TrimSpace(value) == "" || value == "pageHelp."+key {
				t.Errorf("%s: pageHelp.%s is missing", lang, key)
			}
		}
	}
}

// TestEveryTooltipKeyExistsInEveryLanguage guards the tooltips added across the
// app. The prefix is enough: the keys are the contract the templates use.
func TestEveryTooltipKeyExistsInEveryLanguage(t *testing.T) {
	english := i18n.Keys()
	var tips []string
	for _, key := range english {
		if strings.HasPrefix(key, "tip.") {
			tips = append(tips, key)
		}
	}
	if len(tips) < 20 {
		t.Fatalf("only %d tooltip keys found, expected the full set", len(tips))
	}
	for _, lang := range i18n.Supported() {
		for _, key := range tips {
			value := i18n.T(lang, key)
			if strings.TrimSpace(value) == "" || value == key {
				t.Errorf("%s: %s is missing", lang, key)
			}
		}
	}
}

// TestAllTemplatesUseKnownCatalogKeys extends the existing scan to every
// template. A literal T key outside the catalog would silently render its own
// name, and a missing translation would do the same.
func TestAllTemplatesUseKnownCatalogKeys(t *testing.T) {
	known := map[string]bool{}
	for _, key := range i18n.Keys() {
		known[key] = true
	}
	entries, err := os.ReadDir("templates")
	if err != nil {
		t.Fatalf("read templates: %v", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".html") {
			continue
		}
		data, err := os.ReadFile("templates/" + entry.Name())
		if err != nil {
			t.Fatalf("read %s: %v", entry.Name(), err)
		}
		for _, match := range templateKeyAction.FindAllStringSubmatch(string(data), -1) {
			if !known[match[1]] {
				t.Errorf("%s uses missing catalog key %q", entry.Name(), match[1])
			}
		}
	}
}

// TestGuidedAddAndFullCatalogue covers §2 point 1: the first screen starts
// from needs, says what a site needs/gives and keeps "Show all sites".
func TestGuidedAddAndFullCatalogue(t *testing.T) {
	env := newNativeEnv(t)
	body := get(t, env.handler, "/indexers/new").Body.String()
	for _, want := range []string{
		`class="site-guide"`, "What do you want to find?", "Try before adding",
		"What it needs", "What it gives", "Reliability", "Show all sites",
		`action="/indexers/catalog/try"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("guided add missing %q", want)
		}
	}
	if strings.Contains(body, `class="site-row catalog-row"`) {
		t.Error("the full catalogue is still the first thing on the page")
	}
	full := get(t, env.handler, "/indexers/new?all=1").Body.String()
	for _, want := range []string{"Supported sites", `class="site-row catalog-row"`} {
		if !strings.Contains(full, want) {
			t.Errorf("full catalogue missing %q", want)
		}
	}
}

// TestAppPageExplainsTheThreeThings covers §2 point 3.
func TestAppPageExplainsTheThreeThings(t *testing.T) {
	env := newTestEnv(t)
	body := get(t, env.handler, "/applications").Body.String()
	for _, want := range []string{
		"Connect Sonarr, Radarr, Lidarr…",
		"What indexarr does after you connect",
		"Three different things, do not mix them up",
		"Use the Torznab address by hand",
		"Add a download client",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("applications page missing %q", want)
		}
	}
	form := get(t, env.handler, "/applications/new").Body.String()
	for _, want := range []string{
		`id="url"`, `id="api_key"`, "Where to find it",
		`class="advanced"`, `id="sync_level"`,
	} {
		if !strings.Contains(form, want) {
			t.Errorf("application form missing %q", want)
		}
	}
}

// TestPagesRenderTheHelpParagraph checks the component is actually present on
// the main routes in Italian and English, not only in the catalog.
func TestPagesRenderTheHelpParagraph(t *testing.T) {
	env := newTestEnv(t)
	for _, lang := range []string{"en", "it"} {
		for _, path := range []string{"/", "/releases", "/indexers", "/indexers/new", "/applications", "/applications/new", "/activity", "/settings", "/import"} {
			body := get(t, env.handler, path+"?lang="+lang).Body.String()
			if !strings.Contains(body, `class="page-help"`) {
				t.Errorf("%s?lang=%s has no explanatory paragraph", path, lang)
			}
			if !strings.Contains(body, `data-page-help-storage=`) {
				t.Errorf("%s?lang=%s paragraph is not remembered", path, lang)
			}
		}
	}
}
