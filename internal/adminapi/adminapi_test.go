package adminapi

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/buzzqw/indexarr/internal/indexer"
	"github.com/buzzqw/indexarr/internal/store"
	"github.com/buzzqw/indexarr/internal/vault"
)

const (
	testKey      = "primary-test-key"
	testSecret   = "sekret-api-key-value"
	testPassword = "hunter2-password"
	testNewToken = "brand-new-token-value"
	testTracker  = "https://tracker.example/"
)

type env struct {
	t       *testing.T
	server  *Server
	store   *store.Store
	vault   *vault.Vault
	handler http.Handler
	key     string
}

// newEnv opens a temporary store and builds the API server. The vault is
// always available unless withVault is false.
func newEnv(t *testing.T, withKey, withVault bool) *env {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "indexarr.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	var v *vault.Vault
	if withVault {
		v, err = vault.New(bytes.Repeat([]byte{0x42}, 32))
		if err != nil {
			t.Fatalf("vault.New: %v", err)
		}
	}
	key := ""
	if withKey {
		key = testKey
		if err := st.SetSetting(context.Background(), store.KeyAPIKey, key); err != nil {
			t.Fatalf("SetSetting: %v", err)
		}
	}
	srv, err := New(Config{
		Store:  st,
		Vault:  v,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("adminapi.New: %v", err)
	}
	return &env{
		t:       t,
		server:  srv,
		store:   st,
		vault:   v,
		handler: srv.Handler(),
		key:     key,
	}
}

type result struct {
	status int
	body   []byte
	header http.Header
}

func (e *env) call(method, target, body, key string) result {
	e.t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, reader)
	if key != "" {
		req.Header.Set("X-Api-Key", key)
	}
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	return result{status: rec.Code, body: rec.Body.Bytes(), header: rec.Header()}
}

func (e *env) do(method, target, body string) result {
	e.t.Helper()
	return e.call(method, target, body, e.key)
}

func requireStatus(t *testing.T, res result, want int) {
	t.Helper()
	if res.status != want {
		t.Fatalf("status = %d, want %d; body = %s", res.status, want, res.body)
	}
	if got := res.header.Get("Content-Type"); got != "" && got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
}

func decodeJSON[T any](t *testing.T, data []byte) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("decode JSON %q: %v", data, err)
	}
	return out
}

func jsonBody(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(data)
}

func definitionYAML(id, name string) string {
	return fmt.Sprintf(`schema: 1
connector: definition
meta:
  id: %s
  name: %s
  type: public
  protocol: torrent
  urls:
    - %s
`, id, name, testTracker)
}

func definitionBody(yaml string) string {
	return jsonBody(map[string]string{"body": yaml})
}

func (e *env) createDefinition(id, name string) {
	e.t.Helper()
	res := e.do(http.MethodPost, "/api/v2/definitions", definitionBody(definitionYAML(id, name)))
	requireStatus(e.t, res, http.StatusCreated)
}

func (e *env) createIndexer(payload map[string]any) indexerResponse {
	e.t.Helper()
	res := e.do(http.MethodPost, "/api/v2/indexers", jsonBody(payload))
	requireStatus(e.t, res, http.StatusCreated)
	return decodeJSON[indexerResponse](e.t, res.body)
}

func TestAuth(t *testing.T) {
	t.Run("no key configured", func(t *testing.T) {
		e := newEnv(t, false, true)
		res := e.call(http.MethodGet, "/api/v2/health", "", "anything")
		requireStatus(t, res, http.StatusServiceUnavailable)
		errBody := decodeJSON[map[string]string](t, res.body)
		if errBody["error"] == "" {
			t.Fatalf("missing error message: %s", res.body)
		}
	})

	t.Run("all key sources", func(t *testing.T) {
		e := newEnv(t, true, true)

		res := e.call(http.MethodGet, "/api/v2/health", "", "")
		requireStatus(t, res, http.StatusUnauthorized)
		if got := decodeJSON[map[string]string](t, res.body)["error"]; got != "unauthorized" {
			t.Fatalf("error = %q, want unauthorized", got)
		}

		res = e.call(http.MethodGet, "/api/v2/health", "", "wrong-key")
		requireStatus(t, res, http.StatusUnauthorized)

		// X-Api-Key header.
		res = e.do(http.MethodGet, "/api/v2/health", "")
		requireStatus(t, res, http.StatusOK)
		if health := decodeJSON[healthResponse](t, res.body); health.Status != "ok" {
			t.Fatalf("health = %+v", health)
		}

		// apikey query parameter.
		res = e.call(http.MethodGet, "/api/v2/health?apikey="+url.QueryEscape(e.key), "", "")
		requireStatus(t, res, http.StatusOK)

		// Authorization: Bearer.
		req := httptest.NewRequest(http.MethodGet, "/api/v2/health", nil)
		req.Header.Set("Authorization", "Bearer "+e.key)
		rec := httptest.NewRecorder()
		e.handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("bearer status = %d, body = %s", rec.Code, rec.Body)
		}

		// Secondary, revocable key.
		secondary, err := e.store.AddAPIKey(context.Background(), "test-client")
		if err != nil {
			t.Fatalf("AddAPIKey: %v", err)
		}
		res = e.call(http.MethodGet, "/api/v2/health", "", secondary.Key)
		requireStatus(t, res, http.StatusOK)
		keys, err := e.store.ListAPIKeys(context.Background())
		if err != nil {
			t.Fatalf("ListAPIKeys: %v", err)
		}
		if len(keys) != 1 || keys[0].Requests != 1 {
			t.Fatalf("secondary key not touched: %+v", keys)
		}
	})
}

func TestDefinitionsLifecycle(t *testing.T) {
	e := newEnv(t, true, true)
	valid := definitionYAML("alpha", "Alpha Tracker")

	res := e.do(http.MethodPost, "/api/v2/definitions", definitionBody(valid))
	requireStatus(t, res, http.StatusCreated)
	created := decodeJSON[definitionMeta](t, res.body)
	if created.ID != "alpha" || created.SchemaVersion != 1 {
		t.Fatalf("created = %+v", created)
	}
	if created.State != "bozza" || created.Origin != "local" || created.Author != "api" {
		t.Fatalf("created metadata = %+v", created)
	}
	if created.Hash == "" || len(created.Hash) != 64 {
		t.Fatalf("hash = %q", created.Hash)
	}

	// List has no body.
	res = e.do(http.MethodGet, "/api/v2/definitions", "")
	requireStatus(t, res, http.StatusOK)
	list := decodeJSON[struct {
		Definitions []map[string]any `json:"definitions"`
	}](t, res.body)
	if len(list.Definitions) != 1 {
		t.Fatalf("definitions = %d, want 1", len(list.Definitions))
	}
	if _, ok := list.Definitions[0]["body"]; ok {
		t.Fatalf("list leaked definition body: %s", res.body)
	}

	// Detail includes the exact YAML.
	res = e.do(http.MethodGet, "/api/v2/definitions/alpha", "")
	requireStatus(t, res, http.StatusOK)
	detail := decodeJSON[definitionDetail](t, res.body)
	if detail.Body != valid || detail.ID != "alpha" {
		t.Fatalf("detail = %+v", detail)
	}

	// Duplicate without overwrite.
	res = e.do(http.MethodPost, "/api/v2/definitions", definitionBody(valid))
	requireStatus(t, res, http.StatusConflict)

	// Duplicate with overwrite.
	res = e.do(http.MethodPost, "/api/v2/definitions?overwrite=1", definitionBody(valid))
	requireStatus(t, res, http.StatusCreated)

	// Validate success.
	res = e.do(http.MethodPost, "/api/v2/definitions/validate", definitionBody(valid))
	requireStatus(t, res, http.StatusOK)
	validated := decodeJSON[struct {
		Valid bool   `json:"valid"`
		ID    string `json:"id"`
		Name  string `json:"name"`
	}](t, res.body)
	if !validated.Valid || validated.ID != "alpha" || validated.Name != "Alpha Tracker" {
		t.Fatalf("validated = %+v", validated)
	}

	// Invalid YAML is a 422 with details.
	bad := e.do(http.MethodPost, "/api/v2/definitions", definitionBody("schema: [\n"))
	requireStatus(t, bad, http.StatusUnprocessableEntity)
	badBody := decodeJSON[struct {
		Error  string   `json:"error"`
		Errors []string `json:"errors"`
	}](t, bad.body)
	if len(badBody.Errors) == 0 {
		t.Fatalf("no validation details: %s", bad.body)
	}

	// Validate reports the reason instead of failing.
	res = e.do(http.MethodPost, "/api/v2/definitions/validate", definitionBody("schema: [\n"))
	requireStatus(t, res, http.StatusUnprocessableEntity)
	invalid := decodeJSON[struct {
		Valid  bool     `json:"valid"`
		Errors []string `json:"errors"`
	}](t, res.body)
	if invalid.Valid || len(invalid.Errors) == 0 {
		t.Fatalf("invalid validation = %s", res.body)
	}

	// Update requires the body id to match the path.
	res = e.do(http.MethodPut, "/api/v2/definitions/alpha",
		definitionBody(definitionYAML("beta", "Beta Tracker")))
	requireStatus(t, res, http.StatusBadRequest)

	// Update an existing definition.
	updatedYAML := definitionYAML("alpha", "Alpha Renamed")
	res = e.do(http.MethodPut, "/api/v2/definitions/alpha", definitionBody(updatedYAML))
	requireStatus(t, res, http.StatusOK)
	res = e.do(http.MethodGet, "/api/v2/definitions/alpha", "")
	requireStatus(t, res, http.StatusOK)
	if detail := decodeJSON[definitionDetail](t, res.body); detail.Body != updatedYAML {
		t.Fatalf("body after update = %q", detail.Body)
	}

	res = e.do(http.MethodPut, "/api/v2/definitions/missing",
		definitionBody(definitionYAML("missing", "Missing")))
	requireStatus(t, res, http.StatusNotFound)

	// A referenced definition cannot be deleted.
	e.createDefinition("beta", "Beta Tracker")
	in := e.createIndexer(map[string]any{
		"definition_id": "beta",
		"name":          "Beta Indexer",
		"settings":      map[string]any{},
	})
	res = e.do(http.MethodDelete, "/api/v2/definitions/beta", "")
	requireStatus(t, res, http.StatusConflict)

	res = e.do(http.MethodDelete, fmt.Sprintf("/api/v2/indexers/%d", in.ID), "")
	requireStatus(t, res, http.StatusNoContent)

	res = e.do(http.MethodDelete, "/api/v2/definitions/beta", "")
	requireStatus(t, res, http.StatusNoContent)
	res = e.do(http.MethodGet, "/api/v2/definitions/beta", "")
	requireStatus(t, res, http.StatusNotFound)

	res = e.do(http.MethodDelete, "/api/v2/definitions/beta", "")
	requireStatus(t, res, http.StatusNotFound)
}

func TestCatalog(t *testing.T) {
	e := newEnv(t, true, true)

	res := e.do(http.MethodGet, "/api/v2/catalog", "")
	requireStatus(t, res, http.StatusOK)
	before := decodeJSON[struct {
		Catalog []catalogEntry `json:"catalog"`
	}](t, res.body)
	if len(before.Catalog) == 0 {
		t.Fatal("catalog is empty")
	}
	for _, entry := range before.Catalog {
		if entry.Installed {
			t.Fatalf("entry %s unexpectedly installed", entry.ID)
		}
	}

	res = e.do(http.MethodPost, "/api/v2/catalog/sync", "")
	requireStatus(t, res, http.StatusOK)
	synced := decodeJSON[struct {
		Written int `json:"written"`
	}](t, res.body)
	if synced.Written != len(before.Catalog) {
		t.Fatalf("written = %d, want %d", synced.Written, len(before.Catalog))
	}

	res = e.do(http.MethodGet, "/api/v2/catalog", "")
	requireStatus(t, res, http.StatusOK)
	after := decodeJSON[struct {
		Catalog []catalogEntry `json:"catalog"`
	}](t, res.body)
	for _, entry := range after.Catalog {
		if !entry.Installed {
			t.Fatalf("entry %s not installed after sync", entry.ID)
		}
	}
}

func TestIndexers(t *testing.T) {
	e := newEnv(t, true, true)
	ctx := context.Background()
	e.createDefinition("alpha", "Alpha Tracker")

	payload := map[string]any{
		"definition_id": "alpha",
		"name":          "My Tracker!",
		"tags":          []string{"movies", "hd"},
		"settings": map[string]any{
			"url":        testTracker,
			"categories": []int{5000, 2000},
			"query":      "test",
			"api_key":    testSecret,
			"password":   testPassword,
		},
	}
	res := e.do(http.MethodPost, "/api/v2/indexers", jsonBody(payload))
	requireStatus(t, res, http.StatusCreated)
	created := decodeJSON[indexerResponse](t, res.body)
	if created.Slug != "my-tracker" {
		t.Fatalf("slug = %q, want my-tracker", created.Slug)
	}
	if !created.Enabled || created.Priority != 25 {
		t.Fatalf("defaults = %+v", created)
	}
	if !created.HasSecrets {
		t.Fatalf("has_secrets = false")
	}
	if len(created.Tags) != 2 || created.Tags[0] != "movies" {
		t.Fatalf("tags = %+v", created.Tags)
	}
	assertNoLeak(t, res.body)

	// Vault verification: the stored value decrypts correctly.
	secret, err := e.store.GetSecret(ctx, created.ID, "api_key")
	if err != nil {
		t.Fatalf("GetSecret(api_key): %v", err)
	}
	value, err := e.vault.Open(secret.Nonce, secret.Ciphertext)
	if err != nil {
		t.Fatalf("open secret: %v", err)
	}
	if value != testSecret {
		t.Fatalf("decrypted secret = %q", value)
	}
	if _, err := e.store.GetSecret(ctx, created.ID, "password"); err != nil {
		t.Fatalf("GetSecret(password): %v", err)
	}

	// Public configuration is stored, secrets are not.
	row, err := e.store.GetIndexer(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetIndexer: %v", err)
	}
	config, err := decodeConfig(row.ConfigJSON)
	if err != nil {
		t.Fatalf("decodeConfig: %v", err)
	}
	if config["url"] != testTracker || config["query"] != "test" {
		t.Fatalf("public config = %+v", config)
	}
	if _, ok := config["api_key"]; ok {
		t.Fatalf("secret in config_json: %s", row.ConfigJSON)
	}
	if _, ok := config["password"]; ok {
		t.Fatalf("secret in config_json: %s", row.ConfigJSON)
	}

	// List and get never leak secrets or the tracker URL (config is omitted).
	res = e.do(http.MethodGet, "/api/v2/indexers", "")
	requireStatus(t, res, http.StatusOK)
	assertNoLeak(t, res.body)
	list := decodeJSON[struct {
		Indexers []indexerResponse `json:"indexers"`
	}](t, res.body)
	if len(list.Indexers) != 1 || !list.Indexers[0].HasSecrets {
		t.Fatalf("list = %+v", list.Indexers)
	}

	res = e.do(http.MethodGet, fmt.Sprintf("/api/v2/indexers/%d", created.ID), "")
	requireStatus(t, res, http.StatusOK)
	assertNoLeak(t, res.body)

	// Slug conflict.
	res = e.do(http.MethodPost, "/api/v2/indexers", jsonBody(map[string]any{
		"definition_id": "alpha",
		"name":          "Other",
		"slug":          "my-tracker",
	}))
	requireStatus(t, res, http.StatusConflict)

	// Slug derived from the name.
	auto := e.createIndexer(map[string]any{
		"definition_id": "alpha",
		"name":          "Auto Slug",
	})
	if auto.Slug != "auto-slug" {
		t.Fatalf("derived slug = %q", auto.Slug)
	}

	// Partial update: rename, add one secret, delete another, keep the rest.
	res = e.do(http.MethodPut, fmt.Sprintf("/api/v2/indexers/%d", created.ID), jsonBody(map[string]any{
		"name": "Renamed Tracker",
		"settings": map[string]any{
			"password": "",
			"token":    testNewToken,
		},
	}))
	requireStatus(t, res, http.StatusOK)
	updated := decodeJSON[indexerResponse](t, res.body)
	if updated.Name != "Renamed Tracker" {
		t.Fatalf("name = %q", updated.Name)
	}
	if !updated.HasSecrets {
		t.Fatal("has_secrets = false after update")
	}
	assertNoLeak(t, res.body)

	if _, err := e.store.GetSecret(ctx, created.ID, "password"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("password still present: %v", err)
	}
	if _, err := e.store.GetSecret(ctx, created.ID, "api_key"); err != nil {
		t.Fatalf("api_key was removed: %v", err)
	}
	token, err := e.store.GetSecret(ctx, created.ID, "token")
	if err != nil {
		t.Fatalf("GetSecret(token): %v", err)
	}
	tokenValue, err := e.vault.Open(token.Nonce, token.Ciphertext)
	if err != nil {
		t.Fatalf("open token: %v", err)
	}
	if tokenValue != testNewToken {
		t.Fatalf("token = %q", tokenValue)
	}

	res = e.do(http.MethodPut, "/api/v2/indexers/999999", jsonBody(map[string]any{"name": "Nope"}))
	requireStatus(t, res, http.StatusNotFound)

	// Delete and verify children cascade.
	res = e.do(http.MethodDelete, fmt.Sprintf("/api/v2/indexers/%d", created.ID), "")
	requireStatus(t, res, http.StatusNoContent)
	res = e.do(http.MethodGet, fmt.Sprintf("/api/v2/indexers/%d", created.ID), "")
	requireStatus(t, res, http.StatusNotFound)
	if _, err := e.store.GetSecret(ctx, created.ID, "api_key"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("secret survived indexer delete: %v", err)
	}

	res = e.do(http.MethodDelete, fmt.Sprintf("/api/v2/indexers/%d", auto.ID), "")
	requireStatus(t, res, http.StatusNoContent)
}

func assertNoLeak(t *testing.T, body []byte) {
	t.Helper()
	for _, value := range []string{testSecret, testPassword, testNewToken, testTracker} {
		if bytes.Contains(body, []byte(value)) {
			t.Fatalf("response contains %q: %s", value, body)
		}
	}
}

func TestManagerEndpoints(t *testing.T) {
	e := newEnv(t, true, true)
	e.createDefinition("alpha", "Alpha Tracker")
	in := e.createIndexer(map[string]any{
		"definition_id": "alpha",
		"name":          "Manager Test",
	})

	// Without a manager the endpoints are unavailable.
	for _, target := range []string{"test", "search", "run"} {
		res := e.do(http.MethodPost, fmt.Sprintf("/api/v2/indexers/%d/%s", in.ID, target), "")
		requireStatus(t, res, http.StatusServiceUnavailable)
	}

	// A real manager (no HTTP is reached on these paths) enables validation
	// and 404 handling.
	mgr, err := indexer.New(indexer.Config{Store: e.store, Vault: e.vault})
	if err != nil {
		t.Fatalf("indexer.New: %v", err)
	}
	e.server.manager = mgr

	res := e.do(http.MethodPost, "/api/v2/indexers/999999/test", "")
	requireStatus(t, res, http.StatusNotFound)

	res = e.do(http.MethodPost, fmt.Sprintf("/api/v2/indexers/%d/search", in.ID), jsonBody(map[string]any{
		"mode": "bogus",
	}))
	requireStatus(t, res, http.StatusBadRequest)

	res = e.do(http.MethodPost, fmt.Sprintf("/api/v2/indexers/%d/search", in.ID), jsonBody(map[string]any{
		"mode":  "search",
		"query": "x",
		"bogus": true,
	}))
	requireStatus(t, res, http.StatusBadRequest)

	res = e.do(http.MethodPost, "/api/v2/indexers/999999/run", "")
	requireStatus(t, res, http.StatusNotFound)
}

func TestBodyLimitAndUnknownFields(t *testing.T) {
	e := newEnv(t, true, true)
	e.createDefinition("alpha", "Alpha Tracker")

	res := e.do(http.MethodPost, "/api/v2/definitions",
		`{"body":"schema: 1","bogus":true}`)
	requireStatus(t, res, http.StatusBadRequest)

	res = e.do(http.MethodPost, "/api/v2/indexers",
		`{"definition_id":"alpha","name":"x","bogus":true}`)
	requireStatus(t, res, http.StatusBadRequest)

	// One byte over the 1 MiB limit.
	large := `{"body":"` + strings.Repeat("a", maxBodyBytes+1) + `"}`
	res = e.do(http.MethodPost, "/api/v2/definitions", large)
	requireStatus(t, res, http.StatusBadRequest)
	body := decodeJSON[map[string]string](t, res.body)
	if body["error"] == "" {
		t.Fatalf("missing error: %s", res.body)
	}
}

func TestVaultRequiredForSecrets(t *testing.T) {
	e := newEnv(t, true, false)
	e.createDefinition("alpha", "Alpha Tracker")

	res := e.do(http.MethodPost, "/api/v2/indexers", jsonBody(map[string]any{
		"definition_id": "alpha",
		"name":          "No Vault",
		"settings":      map[string]any{"api_key": testSecret},
	}))
	requireStatus(t, res, http.StatusServiceUnavailable)
	assertNoLeak(t, res.body)

	res = e.do(http.MethodPost, "/api/v2/indexers", jsonBody(map[string]any{
		"definition_id": "alpha",
		"name":          "No Vault Public",
		"settings":      map[string]any{"url": testTracker},
	}))
	requireStatus(t, res, http.StatusCreated)
}
