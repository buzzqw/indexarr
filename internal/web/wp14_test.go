package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
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

const legacyDefinitionBody = `schema: 1
connector: newznab
meta:
  id: legacy-42
  name: Legacy 42
  type: public
  protocol: usenet
  urls:
    - https://legacy.example/
`

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

// TestIndexerDefinitionSelectorsExcludeLegacy covers U3: bridge definitions
// must never appear in the create or edit selectors, and the form must expose a
// single definition chooser.
func TestIndexerDefinitionSelectorsExcludeLegacy(t *testing.T) {
	env := newNativeEnv(t)
	seedDefinition(t, env.store, "legacy-42", "newznab", legacyDefinitionBody)

	form := get(t, env.handler, "/indexers/new?definition=test-native").Body.String()
	if strings.Contains(form, "legacy-42") || strings.Contains(form, "Legacy 42") {
		t.Error("the create form lists a legacy bridge definition")
	}
	if !strings.Contains(form, `value="test-native" selected`) {
		t.Error("the GET ?definition= flow no longer preselects the requested definition")
	}
	if strings.Count(form, `name="definition_id"`) != 1 {
		t.Errorf("the create form has %d definition_id selectors, want 1", strings.Count(form, `name="definition_id"`))
	}
	if strings.Contains(form, "definition_choice") {
		t.Error("the duplicate GET definition chooser is still rendered")
	}

	created, err := env.svc.Create(context.Background(), indexersvc.Input{
		DefinitionID: "test-native",
		Name:         "Selector indexer",
		Slug:         "selector-indexer",
		Settings:     map[string]string{"url": "https://example.invalid/api", "apikey": "test-key"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	edit := get(t, env.handler, "/indexers/"+strconv.FormatInt(created.ID, 10)+"/edit").Body.String()
	if strings.Contains(edit, "legacy-42") || strings.Contains(edit, "Legacy 42") {
		t.Error("the edit form lists a legacy bridge definition")
	}
	if strings.Count(edit, `name="definition_id"`) != 1 {
		t.Errorf("the edit form has %d definition_id selectors, want 1", strings.Count(edit, `name="definition_id"`))
	}
	if strings.Contains(edit, "definition_choice") {
		t.Error("the duplicate GET definition chooser is still rendered on edit")
	}
	if !strings.Contains(edit, `value="test-native" selected`) {
		t.Error("the edit form does not preselect the current definition")
	}
}

func TestIndexerCreateRejectsLegacyDefinition(t *testing.T) {
	env := newNativeEnv(t)
	seedDefinition(t, env.store, "legacy-42", "newznab", legacyDefinitionBody)
	token, cookie := csrfFrom(t, env.handler, "/indexers/new")
	recorder := postForm(t, env.handler, "/indexers", url.Values{
		"csrf":          {token},
		"definition_id": {"legacy-42"},
		"name":          {"Bridge copy"},
		"protocol":      {"usenet"},
		"enabled":       {"on"},
	}, cookie)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("create with legacy definition = %d: %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "Legacy bridge definitions") {
		t.Error("the rejection does not explain why a bridge definition cannot be used")
	}
}

// TestDerivedDefinitionWarning keeps converted definitions selectable for local
// use, but labels them as a derived draft that must be rewritten.
func TestDerivedDefinitionWarning(t *testing.T) {
	env := newNativeEnv(t)
	seedDefinition(t, env.store, "derived-draft", "newznab", derivedDefinitionBody)

	form := get(t, env.handler, "/indexers/new?definition=derived-draft").Body.String()
	if !strings.Contains(form, `value="derived-draft" selected`) {
		t.Error("the derived definition is not selectable")
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
	sourceID, err := env.store.CreateSource(ctx, store.Source{
		Name: "Broken source", Kind: "torznab", Manager: "auto",
		URL: "http://broken.example/api", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	runID, started, err := env.store.BeginSourceRun(ctx, sourceID)
	if err != nil {
		t.Fatal(err)
	}
	source, _ := env.store.GetSource(ctx, sourceID)
	if err := env.store.FinishSourceRun(ctx, runID, source, started, store.RunResult{}, errors.New("boom")); err != nil {
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
	runID, started, err = env.store.BeginSourceRun(ctx, sourceID)
	if err != nil {
		t.Fatal(err)
	}
	source, _ = env.store.GetSource(ctx, sourceID)
	if err := env.store.FinishSourceRun(ctx, runID, source, started, store.RunResult{Seen: 1}, nil); err != nil {
		t.Fatal(err)
	}

	body = get(t, env.handler, "/").Body.String()
	if strings.Contains(body, "all without errors") || strings.Contains(body, ">no errors<") {
		t.Error("the dashboard claims there are no errors while a run failed in the last 24h")
	}
	for _, want := range []string{"All indexers and sources look healthy now", "1 failed run(s) of 2"} {
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
		"Next step", "Connect application", "Manage applications",
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
