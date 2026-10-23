package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/buzzqw/indexarr/internal/indexer"
	"github.com/buzzqw/indexarr/internal/indexersvc"
	"github.com/buzzqw/indexarr/internal/store"
	"github.com/buzzqw/indexarr/internal/vault"
)

const nativeCapsXML = `<?xml version="1.0" encoding="UTF-8"?>
<caps>
  <server version="1.0" title="Example Indexer"/>
  <limits max="10000" default="50"/>
  <searching>
    <search available="yes" supportedParams="q,group"/>
    <tv-search available="yes" supportedParams="q,season,ep,imdbid,tvdbid,tvmazeid,cat"/>
    <movie-search available="yes" supportedParams="q,imdbid,tmdbid"/>
    <book-search available="yes" supportedParams="q,author,title"/>
  </searching>
  <categories>
    <category id="5000" name="TV">
      <subcat id="5040" name="TV/HD"/>
    </category>
  </categories>
</caps>`

const nativeSearchXML = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:newznab="http://www.newznab.com/DTD/2010/feeds/attributes/">
  <channel>
    <title>Example Indexer</title>
    <item>
      <title>Example.Show.S02E03.1080p.WEB-DL</title>
      <guid isPermaLink="false">abc123guid</guid>
      <link>https://indexer.example/details/abc123</link>
      <pubDate>Mon, 06 May 2024 07:08:09 +0000</pubDate>
      <category>TV &gt; HD</category>
      <enclosure url="https://indexer.example/api?t=get&amp;id=abc123guid" length="1234567890" type="application/x-nzb"/>
      <newznab:attr name="size" value="1234567890"/>
      <newznab:attr name="seeders" value="12"/>
      <newznab:attr name="category" value="5040"/>
    </item>
    <item>
      <title>Example.Movie.2024.2160p</title>
      <guid>def456guid</guid>
      <link>https://indexer.example/details/def456</link>
      <pubDate>Tue, 07 May 2024 10:11:12 GMT</pubDate>
      <category>2040</category>
      <enclosure url="/api?t=get&amp;id=def456guid" length="987654321" type="application/x-nzb"/>
      <newznab:attr name="category" value="2040"/>
    </item>
  </channel>
</rss>`

const nativeDefinition = `schema: 1
connector: newznab
meta:
  id: test-native
  name: Test Native
  type: public
  protocol: usenet
  urls:
    - https://example.invalid/
settings:
  - {name: url, type: text, label: URL API, required: true}
  - {name: apikey, type: password, label: API key, required: true}
  - {name: flavour, type: select, label: Flavore, options: [a, b]}
  - {name: extra-flag, type: checkbox, label: Flag extra}
`

type nativeEnv struct {
	store   *store.Store
	vault   *vault.Vault
	svc     *indexersvc.Service
	manager *indexer.Manager
	server  *Server
	handler http.Handler
	apiURL  string
}

func newNativeEnv(t *testing.T) *nativeEnv {
	t.Helper()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("t") {
		case "caps":
			w.Header().Set("Content-Type", "application/xml")
			_, _ = io.WriteString(w, nativeCapsXML)
		case "get":
			w.Header().Set("Content-Type", "application/x-nzb")
			_, _ = io.WriteString(w, "nzb-payload")
		default:
			w.Header().Set("Content-Type", "application/rss+xml")
			_, _ = io.WriteString(w, nativeSearchXML)
		}
	}))
	t.Cleanup(upstream.Close)

	st, err := store.Open(t.TempDir() + "/native.db")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	v, err := vault.New(key)
	if err != nil {
		t.Fatalf("vault.New: %v", err)
	}
	manager, err := indexer.New(indexer.Config{Store: st, Vault: v})
	if err != nil {
		t.Fatalf("indexer.New: %v", err)
	}
	svc, err := indexersvc.New(indexersvc.Config{Store: st, Vault: v, Manager: manager})
	if err != nil {
		t.Fatalf("indexersvc.New: %v", err)
	}
	if err := st.UpsertDefinition(context.Background(), store.Definition{
		ID:            "test-native",
		SchemaVersion: 1,
		Family:        "newznab",
		State:         "bozza",
		Origin:        "local",
		Author:        "test",
		Body:          nativeDefinition,
		Hash:          "native-hash",
	}); err != nil {
		t.Fatalf("UpsertDefinition: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := New(Config{
		Store:    st,
		Logger:   logger,
		Version:  "test",
		Started:  time.Now(),
		Native:   manager,
		Indexers: svc,
	})
	return &nativeEnv{
		store:   st,
		vault:   v,
		svc:     svc,
		manager: manager,
		server:  server,
		handler: server.Handler(),
		apiURL:  upstream.URL + "/api",
	}
}

func TestNativePagesRender(t *testing.T) {
	env := newNativeEnv(t)

	for _, path := range []string{"/definitions", "/definitions/test-native", "/definitions/new", "/indexers", "/indexers/new?definition=test-native"} {
		recorder := get(t, env.handler, path)
		if recorder.Code != http.StatusOK {
			t.Fatalf("GET %s = %d: %s", path, recorder.Code, recorder.Body.String())
		}
	}
	definitions := get(t, env.handler, "/definitions").Body.String()
	for _, want := range []string{"Definizioni", "Test Native", "test-native", "Indexer nativi"} {
		if !strings.Contains(definitions, want) {
			t.Errorf("definitions page missing %q", want)
		}
	}
	detail := get(t, env.handler, "/definitions/test-native").Body.String()
	for _, want := range []string{"Test Native", "connector: newznab", "bozza", "valida"} {
		if !strings.Contains(detail, want) {
			t.Errorf("definition detail missing %q", want)
		}
	}
	form := get(t, env.handler, "/indexers/new?definition=test-native").Body.String()
	for _, want := range []string{"Nuovo indexer nativo", "API key", "Flavore", "Consenti indirizzi privati"} {
		if !strings.Contains(form, want) {
			t.Errorf("indexer form missing %q", want)
		}
	}
}

func TestNativeIndexerLifecycle(t *testing.T) {
	env := newNativeEnv(t)
	token, cookie := csrfFrom(t, env.handler, "/indexers/new?definition=test-native")
	values := url.Values{
		"csrf":               {token},
		"definition_id":      {"test-native"},
		"name":               {"Native One"},
		"slug":               {"native-one"},
		"protocol":           {"usenet"},
		"priority":           {"5"},
		"tags":               {"film, hd"},
		"enabled":            {"on"},
		"url":                {env.apiURL},
		"categories":         {"5000"},
		"query":              {""},
		"user_agent":         {""},
		"default_category":   {""},
		"allow_private":      {"on"},
		"setting_apikey":     {"topsecretvalue"},
		"setting_flavour":    {"b"},
		"setting_extra-flag": {"on"},
	}
	recorder := postForm(t, env.handler, "/indexers", values, cookie)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("create status = %d: %s", recorder.Code, recorder.Body.String())
	}
	indexers, err := env.store.ListIndexers(context.Background())
	if err != nil || len(indexers) != 1 {
		t.Fatalf("indexers = %#v, %v", indexers, err)
	}
	created := indexers[0]
	if created.Name != "Native One" || created.Slug != "native-one" || created.Priority != 5 ||
		created.Tags != "film,hd" || created.Protocol != "usenet" {
		t.Errorf("created = %#v", created)
	}
	if strings.Contains(created.ConfigJSON, "topsecretvalue") {
		t.Errorf("secret in config_json: %s", created.ConfigJSON)
	}

	list := get(t, env.handler, "/indexers").Body.String()
	if !strings.Contains(list, "Native One") || !strings.Contains(list, "native-one") {
		t.Errorf("indexers page does not list the indexer:\n%s", list)
	}

	editPath := "/indexers/" + strconv.FormatInt(created.ID, 10) + "/edit"
	edit := get(t, env.handler, editPath).Body.String()
	if strings.Contains(edit, "topsecretvalue") {
		t.Fatal("the edit form leaked a secret value")
	}
	if !strings.Contains(edit, "configurato") || !strings.Contains(edit, "Rimuovi il segreto") {
		t.Errorf("edit form does not show the secret key state")
	}

	detailPath := "/indexers/" + strconv.FormatInt(created.ID, 10)
	detail := get(t, env.handler, detailPath).Body.String()
	if strings.Contains(detail, "topsecretvalue") {
		t.Fatal("the indexer page leaked a secret value")
	}
	if !strings.Contains(detail, "apikey") || !strings.Contains(detail, "Configurazione pubblica") {
		t.Errorf("detail does not show the public configuration")
	}

	// Update keeps the secret when the password field is empty.
	token, cookie = csrfFrom(t, env.handler, editPath)
	values.Set("csrf", token)
	values.Set("name", "Native Renamed")
	values.Set("setting_apikey", "")
	recorder = postForm(t, env.handler, "/indexers/"+strconv.FormatInt(created.ID, 10), values, cookie)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("update status = %d: %s", recorder.Code, recorder.Body.String())
	}
	updated, err := env.store.GetIndexer(context.Background(), created.ID)
	if err != nil || updated.Name != "Native Renamed" {
		t.Fatalf("updated = %#v, %v", updated, err)
	}
	secret, err := env.store.GetSecret(context.Background(), created.ID, "apikey")
	if err != nil {
		t.Fatalf("secret was not kept: %v", err)
	}
	plaintext, err := env.vault.Open(secret.Nonce, secret.Ciphertext)
	if err != nil || plaintext != "topsecretvalue" {
		t.Fatalf("secret = %q, %v", plaintext, err)
	}
	if body := get(t, env.handler, detailPath).Body.String(); !strings.Contains(body, "Native Renamed") {
		t.Error("the detail page does not show the updated name")
	}

	// Delete through the UI.
	token, cookie = csrfFrom(t, env.handler, detailPath)
	recorder = postForm(t, env.handler, "/indexers/"+strconv.FormatInt(created.ID, 10)+"/delete",
		url.Values{"csrf": {token}}, cookie)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("delete status = %d: %s", recorder.Code, recorder.Body.String())
	}
	if remaining, err := env.store.ListIndexers(context.Background()); err != nil || len(remaining) != 0 {
		t.Fatalf("indexers after delete = %#v, %v", remaining, err)
	}
}

func TestNativeTestAndRun(t *testing.T) {
	env := newNativeEnv(t)
	created, err := env.svc.Create(context.Background(), indexersvc.Input{
		DefinitionID: "test-native",
		Name:         "Action indexer",
		Settings: map[string]string{
			"url":           env.apiURL,
			"apikey":        "topsecretvalue",
			"allow_private": "true",
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	path := "/indexers/" + strconv.FormatInt(created.ID, 10)
	token, cookie := csrfFrom(t, env.handler, path)

	testRecorder := postForm(t, env.handler, path+"/test", url.Values{"csrf": {token}}, cookie)
	if testRecorder.Code != http.StatusOK {
		t.Fatalf("test status = %d: %s", testRecorder.Code, testRecorder.Body.String())
	}
	testBody := testRecorder.Body.String()
	for _, want := range []string{"Risultato del test", "test superato", "reach", "caps", "search", "Durata"} {
		if !strings.Contains(testBody, want) {
			t.Errorf("test result missing %q:\n%s", want, testBody)
		}
	}
	if strings.Contains(testBody, "topsecretvalue") {
		t.Error("the test result leaked a secret value")
	}

	runRecorder := postForm(t, env.handler, path+"/run", url.Values{"csrf": {token}}, cookie)
	if runRecorder.Code != http.StatusOK {
		t.Fatalf("run status = %d: %s", runRecorder.Code, runRecorder.Body.String())
	}
	runBody := runRecorder.Body.String()
	for _, want := range []string{"Risultato dell'esecuzione", "Visti", "Nuovi", "Example.Show.S02E03.1080p", "5040"} {
		if !strings.Contains(runBody, want) {
			t.Errorf("run result missing %q:\n%s", want, runBody)
		}
	}
	if strings.Contains(runBody, "topsecretvalue") {
		t.Error("the run result leaked a secret value")
	}
}

func TestDefinitionCreateFromUI(t *testing.T) {
	env := newNativeEnv(t)
	token, cookie := csrfFrom(t, env.handler, "/definitions/new")
	body := `schema: 1
connector: newznab
meta:
  id: ui-defined
  name: UI Defined
  type: public
  protocol: usenet
  urls:
    - https://example.invalid/
`
	recorder := postForm(t, env.handler, "/definitions", url.Values{
		"csrf": {token},
		"body": {body},
	}, cookie)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("definition create status = %d: %s", recorder.Code, recorder.Body.String())
	}
	def, err := env.store.GetDefinition(context.Background(), "ui-defined")
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	if def.Origin != "local" || def.State != "bozza" || def.Author != "ui" {
		t.Errorf("stored definition = %#v", def)
	}

	// A second submission without the overwrite checkbox is rejected.
	token, cookie = csrfFrom(t, env.handler, "/definitions/new")
	recorder = postForm(t, env.handler, "/definitions", url.Values{
		"csrf": {token},
		"body": {body},
	}, cookie)
	if recorder.Code != http.StatusBadRequest ||
		!strings.Contains(recorder.Body.String(), "Esiste già una definizione") {
		t.Fatalf("duplicate definition = %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestNativePagesDegradeWithoutService(t *testing.T) {
	env := newTestEnv(t)

	page := get(t, env.handler, "/indexers")
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "non è configurato") {
		t.Fatalf("indexers without service = %d: %s", page.Code, page.Body.String())
	}
	form := get(t, env.handler, "/indexers/new")
	if form.Code != http.StatusServiceUnavailable || !strings.Contains(form.Body.String(), "non è configurato") {
		t.Fatalf("new indexer without service = %d: %s", form.Code, form.Body.String())
	}
	definitions := get(t, env.handler, "/definitions")
	if definitions.Code != http.StatusOK || !strings.Contains(definitions.Body.String(), "Definizioni") {
		t.Fatalf("definitions without service = %d: %s", definitions.Code, definitions.Body.String())
	}
}
