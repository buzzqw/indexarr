package web

import (
	"context"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/buzzqw/indexarr/internal/i18n"
	"github.com/buzzqw/indexarr/internal/indexersvc"
	"github.com/buzzqw/indexarr/internal/store"
)

const derivedDefinitionBody = `schema: 1
connector: newznab
meta:
  id: derived-draft
  name: Derived Draft
  type: public
  protocol: torrent
  derived_from: cardigann
  urls:
    - https://derived.example/
`

func seedDefinition(t *testing.T, st *store.Store, id, family, body string) {
	t.Helper()
	if err := st.UpsertDefinition(context.Background(), store.Definition{
		ID:            id,
		SchemaVersion: 1,
		Family:        family,
		State:         "bozza",
		Origin:        "local",
		Author:        "test",
		Body:          body,
		Hash:          "hash-" + id,
		UpdatedAt:     time.Now().UTC(),
	}); err != nil {
		t.Fatalf("UpsertDefinition(%s): %v", id, err)
	}
}

// TestDerivedDefinitionWarning keeps converted definitions selectable for local
// use, but labels them as a derived draft that must be rewritten.
func TestDerivedDefinitionWarning(t *testing.T) {
	env := newNativeEnv(t)
	seedDefinition(t, env.store, "derived-draft", "newznab", derivedDefinitionBody)

	form := get(t, env.handler, "/indexers/new?definition=derived-draft").Body.String()
	if !strings.Contains(form, `name="definition_id" value="derived-draft"`) {
		t.Error("the derived definition is not selected")
	}
	if !strings.Contains(form, "Derived draft: rewrite it") {
		t.Error("the create form does not warn about a derived draft")
	}
	detail := get(t, env.handler, "/definitions/derived-draft").Body.String()
	if !strings.Contains(detail, "Derived draft: rewrite it") {
		t.Error("the definition detail does not warn about a derived draft")
	}
}

// TestDashboardSingleErrorSource covers U8: the overview must never say that
// everything is error-free while the 24h run counter reports failures.
func TestDashboardSingleErrorSource(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	if err := env.store.UpsertDefinition(ctx, store.Definition{
		ID: "broken-def", SchemaVersion: 1, Family: "torznab", State: "verificata",
		Origin: "local", Author: "test", Body: "schema: 1\nconnector: torznab\n", Hash: strings.Repeat("a", 64),
	}); err != nil {
		t.Fatal(err)
	}
	indexerID, err := env.store.CreateIndexer(ctx, store.Indexer{
		DefinitionID: "broken-def", Protocol: "torrent", Slug: "broken",
		Name: "Broken source", Enabled: true, ConfigJSON: `{}`,
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := env.store.UpsertIndexerState(ctx, store.IndexerState{
		IndexerID: indexerID, Circuit: "closed", LastError: "boom",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := env.store.RecordIndexerHistory(ctx, store.IndexerHistory{
		IndexerID: indexerID, Kind: "run", Status: "error", StartedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	body := get(t, env.handler, "/").Body.String()
	if strings.Contains(body, "all without errors") || strings.Contains(body, ">no errors<") {
		t.Error("the dashboard claims there are no errors while a current error exists")
	}
	for _, want := range []string{"Overall status", "1 problem(s)", "failed run"} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard status missing %q", want)
		}
	}

	// A later successful run clears the entity error but leaves the failed run
	// in the 24h window: the summary must mention it instead of going back to
	// "no errors".
	if err := env.store.UpsertIndexerState(ctx, store.IndexerState{
		IndexerID: indexerID, Circuit: "closed", LastError: "",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := env.store.RecordIndexerHistory(ctx, store.IndexerHistory{
		IndexerID: indexerID, Kind: "run", Status: "ok", StartedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	body = get(t, env.handler, "/").Body.String()
	if strings.Contains(body, "all without errors") || strings.Contains(body, ">no errors<") {
		t.Error("the dashboard claims there are no errors while a run failed in the last 24h")
	}
	for _, want := range []string{"All indexers look healthy now", "1 failed run(s) of 2"} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard status missing %q", want)
		}
	}
}

// TestDashboardPublicURLWarning covers U12: the dashboard shows the configured
// public URL and links to Settings when public_url is empty.
func TestDashboardPublicURLWarning(t *testing.T) {
	env := newTestEnv(t)

	body := get(t, env.handler, "/").Body.String()
	for _, want := range []string{
		"public_url is not set",
		`href="/settings"`,
		"Set the public URL",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing public_url warning %q", want)
		}
	}

	ctx := context.Background()
	settings, err := env.store.LoadSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	settings.PublicURL = "https://indexarr.example"
	if err := env.store.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}

	body = get(t, env.handler, "/").Body.String()
	if strings.Contains(body, "public_url is not set") {
		t.Error("the dashboard still warns after public_url is configured")
	}
	for _, want := range []string{
		"https://indexarr.example/api",
		"Public URL: https://indexarr.example",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard missing configured endpoint %q", want)
		}
	}
}

func TestIndexerDetailNextStep(t *testing.T) {
	env := newNativeEnv(t)
	created, err := env.svc.Create(context.Background(), indexersvc.Input{
		DefinitionID: "test-native",
		Name:         "Next step indexer",
		Slug:         "next-step-indexer",
		Settings:     map[string]string{"url": "https://example.invalid/api", "apikey": "test-key"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	body := get(t, env.handler, "/indexers/"+strconv.FormatInt(created.ID, 10)).Body.String()
	for _, want := range []string{
		"Next step", "Connect an app",
		`href="/applications/new"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("indexer detail missing next step %q", want)
		}
	}
}

var templateTextAction = regexp.MustCompile(`(?s)\{\{.*?\}\}`)
var templateKeyAction = regexp.MustCompile(`\bT\s+"([^"]+)"`)
var templateToken = regexp.MustCompile(`\p{L}+`)
var handlerKeyPattern = regexp.MustCompile(`(?:translate|newBaseT)\(r, "([^"]+)"`)

// TestOwnedTemplatesUseCatalog guards U9 for the pages owned by WP14 phase 1:
// every T key exists in the English catalog and no user-visible literal text is
// left in the template outside it. Technical identifiers are explicitly allowed.
func TestOwnedTemplatesUseCatalog(t *testing.T) {
	files := []string{
		"layout", "dashboard", "indexers", "indexer", "indexer_form",
		"definitions", "definition", "definition_form", "applications",
		"application_form", "import", "activity", "runs", "logs", "error",
		"components",
	}
	allowed := map[string]bool{
		"indexarr": true,
		"v":        true,
		"torrent":  true,
		"usenet":   true,
		"ms":       true,
	}
	known := make(map[string]bool, len(i18n.Keys()))
	for _, key := range i18n.Keys() {
		known[key] = true
	}

	for _, name := range files {
		path := "templates/" + name + ".html"
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		content := string(data)

		for _, match := range templateKeyAction.FindAllStringSubmatch(content, -1) {
			if !known[match[1]] {
				t.Errorf("%s uses missing catalog key %q", path, match[1])
			}
		}

		// Ignore executable blocks and comments: only the text nodes rendered
		// to the user matter for localization.
		text := regexp.MustCompile(`(?s)<script.*?</script>`).ReplaceAllString(content, "")
		text = regexp.MustCompile(`(?s)<style.*?</style>`).ReplaceAllString(text, "")
		text = regexp.MustCompile(`(?s)<!--.*?-->`).ReplaceAllString(text, "")
		text = templateTextAction.ReplaceAllString(text, "")
		text = regexp.MustCompile(`<[^>]+>`).ReplaceAllString(text, "\n")

		var literal []string
		for _, token := range templateToken.FindAllString(text, -1) {
			if allowed[token] {
				continue
			}
			literal = append(literal, token)
		}
		if len(literal) > 0 {
			sort.Strings(literal)
			t.Errorf("%s has user-visible text outside the catalog: %v", path, literal)
		}
	}
}

// TestOwnedHandlerTranslationsExist checks the flash messages and page titles
// passed from handlers, which the template scan cannot see.
func TestOwnedHandlerTranslationsExist(t *testing.T) {
	known := make(map[string]bool, len(i18n.Keys()))
	for _, key := range i18n.Keys() {
		known[key] = true
	}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for _, match := range handlerKeyPattern.FindAllStringSubmatch(string(data), -1) {
			if !known[match[1]] {
				t.Errorf("%s uses missing catalog key %q", name, match[1])
			}
		}
	}
}
