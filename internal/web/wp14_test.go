package web

import (
	"context"
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

// TestDashboardSingleErrorSource covers U8: the home status is one value, so
// it cannot say "everything works" while a current indexer error exists. A
// failed run that has since recovered is not a problem anymore: it stays in
// Activity, not on Home.
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
		IndexerID: indexerID, Circuit: "closed", LastError: "HTTP 429: too many requests",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := env.store.RecordIndexerHistory(ctx, store.IndexerHistory{
		IndexerID: indexerID, Kind: "run", Status: "error", StartedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	body := get(t, env.handler, "/").Body.String()
	if strings.Contains(body, "All indexers look healthy now") {
		t.Error("the home page claims there are no errors while a current error exists")
	}
	for _, want := range []string{"indexers have problems", "Broken source", "limiting requests", "Needs attention"} {
		if !strings.Contains(body, want) {
			t.Errorf("home status missing %q", want)
		}
	}

	// A later successful run clears the current error. The old failed run is
	// still in Activity, but Home must go back to green instead of counting it.
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
	for _, want := range []string{"Everything works", "1 indexers active"} {
		if !strings.Contains(body, want) {
			t.Errorf("home status missing %q", want)
		}
	}
	if strings.Contains(body, "indexers have problems") {
		t.Error("the home page still reports problems after the indexer recovered")
	}
}

// TestDashboardPublicURLWarning covers U12: without public_url the Home shows
// the address this browser is using and a one-click way to adopt it.
func TestDashboardPublicURLWarning(t *testing.T) {
	env := newTestEnv(t)

	body := get(t, env.handler, "/").Body.String()
	for _, want := range []string{
		"Use this address",
		"http://example.com/api",
		"host.docker.internal",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing suggested address %q", want)
		}
	}

	token, cookie := csrfFrom(t, env.handler, "/")
	recorder := postForm(t, env.handler, "/settings/public-url", url.Values{
		"csrf":       {token},
		"public_url": {"http://example.com"},
	}, cookie)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("POST public-url = %d: %s", recorder.Code, recorder.Body.String())
	}

	body = get(t, env.handler, "/").Body.String()
	if strings.Contains(body, "Use this address") {
		t.Error("the home page still proposes an address after public_url is set")
	}
	for _, want := range []string{
		"http://example.com/api",
		"Public address: http://example.com",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("home missing configured endpoint %q", want)
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
		"application_form", "activity", "activity_feed",
		"activity_searches", "indexer_data", "indexer_try", "runs", "logs",
		"error", "components", "setup", "settings", "settings_definitions",
		"download_client_form",
	}
	allowed := map[string]bool{
		"indexarr": true,
		"v":        true,
		"torrent":  true,
		"usenet":   true,
		"ms":       true,
		"WAL":      true,
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
