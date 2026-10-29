package adminapi

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/buzzqw/indexarr/internal/syncjob"
)

func createTestApplication(t *testing.T, e *env, payload map[string]any) applicationResponse {
	t.Helper()
	res := e.do(http.MethodPost, "/api/v2/applications", jsonBody(payload))
	requireStatus(t, res, http.StatusCreated)
	return decodeJSON[applicationResponse](t, res.body)
}

func TestApplicationsCRUD(t *testing.T) {
	e := newEnv(t, true, true)
	const secret = "sonarr-secret-api-key-value"

	created := createTestApplication(t, e, map[string]any{
		"kind":       "sonarr",
		"name":       "Sonarr",
		"url":        "http://sonarr.local:8989/",
		"api_key":    secret,
		"sync_level": "full",
	})
	if created.ID <= 0 || created.Kind != "sonarr" || created.Name != "Sonarr" {
		t.Fatalf("created application = %+v", created)
	}
	if created.URL != "http://sonarr.local:8989" {
		t.Errorf("URL = %q, want trailing slash removed", created.URL)
	}
	if created.SyncLevel != "full" || !created.HasAPIKey {
		t.Errorf("created application flags = %+v", created)
	}

	// The API key is sealed in the store and never appears in a response.
	stored, err := e.store.GetApplication(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("GetApplication: %v", err)
	}
	if len(stored.APIKeyEnc) == 0 {
		t.Fatal("api_key_enc was not stored")
	}
	opened, err := e.vault.OpenString(string(stored.APIKeyEnc))
	if err != nil {
		t.Fatalf("OpenString: %v", err)
	}
	if opened != secret {
		t.Errorf("decrypted API key = %q, want the original", opened)
	}

	// List, get: no plaintext and no sealed blob in the JSON.
	listRes := e.do(http.MethodGet, "/api/v2/applications", "")
	requireStatus(t, listRes, http.StatusOK)
	assertNoSecret(t, listRes.body, secret)
	list := decodeJSON[struct {
		Applications []applicationResponse `json:"applications"`
	}](t, listRes.body)
	if len(list.Applications) != 1 || list.Applications[0].ID != created.ID {
		t.Fatalf("applications list = %+v", list.Applications)
	}
	if !list.Applications[0].HasAPIKey {
		t.Error("has_api_key = false, want true")
	}

	getRes := e.do(http.MethodGet, fmt.Sprintf("/api/v2/applications/%d", created.ID), "")
	requireStatus(t, getRes, http.StatusOK)
	assertNoSecret(t, getRes.body, secret)
	if got := decodeJSON[applicationResponse](t, getRes.body); got.ID != created.ID {
		t.Fatalf("get application = %+v", got)
	}

	// Update the name; an empty api_key keeps the stored key.
	updateRes := e.do(http.MethodPut, fmt.Sprintf("/api/v2/applications/%d", created.ID),
		jsonBody(map[string]any{"name": "Sonarr 2", "api_key": ""}))
	requireStatus(t, updateRes, http.StatusOK)
	assertNoSecret(t, updateRes.body, secret)
	updated := decodeJSON[applicationResponse](t, updateRes.body)
	if updated.Name != "Sonarr 2" || !updated.HasAPIKey {
		t.Fatalf("updated application = %+v", updated)
	}
	stored, err = e.store.GetApplication(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("GetApplication after name update: %v", err)
	}
	opened, err = e.vault.OpenString(string(stored.APIKeyEnc))
	if err != nil || opened != secret {
		t.Fatalf("API key changed after empty update: %q, %v", opened, err)
	}

	// A non-empty api_key replaces the secret.
	const replacement = "replacement-api-key-value"
	updateRes = e.do(http.MethodPut, fmt.Sprintf("/api/v2/applications/%d", created.ID),
		jsonBody(map[string]any{"api_key": replacement, "sync_level": "add"}))
	requireStatus(t, updateRes, http.StatusOK)
	assertNoSecret(t, updateRes.body, replacement)
	updated = decodeJSON[applicationResponse](t, updateRes.body)
	if updated.SyncLevel != "add" {
		t.Errorf("sync_level = %q, want add", updated.SyncLevel)
	}
	stored, err = e.store.GetApplication(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("GetApplication after key update: %v", err)
	}
	opened, err = e.vault.OpenString(string(stored.APIKeyEnc))
	if err != nil || opened != replacement {
		t.Fatalf("API key was not replaced: %q, %v", opened, err)
	}

	// Delete and confirm the row is gone.
	deleteRes := e.do(http.MethodDelete, fmt.Sprintf("/api/v2/applications/%d", created.ID), "")
	requireStatus(t, deleteRes, http.StatusNoContent)
	missingRes := e.do(http.MethodGet, fmt.Sprintf("/api/v2/applications/%d", created.ID), "")
	requireStatus(t, missingRes, http.StatusNotFound)
	if _, err := e.store.GetApplication(context.Background(), created.ID); err == nil {
		t.Error("application still exists after delete")
	}
}

func TestApplicationSyncProfileRoundTrip(t *testing.T) {
	e := newEnv(t, true, true)

	created := createTestApplication(t, e, map[string]any{
		"kind": "sonarr", "name": "Sonarr", "url": "http://sonarr.local:8989",
		"api_key": "key-value", "sync_level": "full",
	})
	if !created.EnableRSS || !created.EnableAutomaticSearch || !created.EnableInteractiveSearch {
		t.Fatalf("create defaults = %+v", created)
	}
	if created.MinSeeders != 0 || len(created.Categories) != 0 {
		t.Fatalf("create profile = %+v", created)
	}

	res := e.do(http.MethodPut, fmt.Sprintf("/api/v2/applications/%d", created.ID),
		jsonBody(map[string]any{
			"enable_rss":                false,
			"enable_automatic_search":   true,
			"enable_interactive_search": false,
			"min_seeders":               7,
			"categories":                []int{5000, 2000, 5000},
		}))
	requireStatus(t, res, http.StatusOK)
	updated := decodeJSON[applicationResponse](t, res.body)
	if updated.EnableRSS || !updated.EnableAutomaticSearch || updated.EnableInteractiveSearch {
		t.Fatalf("updated flags = %+v", updated)
	}
	if updated.MinSeeders != 7 || fmt.Sprint(updated.Categories) != "[2000 5000]" {
		t.Fatalf("updated profile = %+v", updated)
	}
	stored, err := e.store.GetApplication(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("GetApplication: %v", err)
	}
	if stored.Categories != "2000,5000" || stored.MinSeeders != 7 ||
		stored.EnableRSS || stored.EnableInteractiveSearch {
		t.Fatalf("stored profile = %+v", stored)
	}

	// The string form used by the web form is accepted too, and an omitted
	// field on update keeps the current value.
	res = e.do(http.MethodPut, fmt.Sprintf("/api/v2/applications/%d", created.ID),
		jsonBody(map[string]any{"categories": "5000"}))
	requireStatus(t, res, http.StatusOK)
	updated = decodeJSON[applicationResponse](t, res.body)
	if fmt.Sprint(updated.Categories) != "[5000]" || updated.MinSeeders != 7 || updated.EnableRSS {
		t.Fatalf("string categories update = %+v", updated)
	}

	// An empty category list clears the selection.
	res = e.do(http.MethodPut, fmt.Sprintf("/api/v2/applications/%d", created.ID),
		jsonBody(map[string]any{"categories": []int{}}))
	requireStatus(t, res, http.StatusOK)
	if got := decodeJSON[applicationResponse](t, res.body); len(got.Categories) != 0 {
		t.Fatalf("categories after clear = %v", got.Categories)
	}
}

func TestApplicationSyncProfileValidation(t *testing.T) {
	e := newEnv(t, true, true)
	created := createTestApplication(t, e, map[string]any{
		"kind": "sonarr", "name": "Sonarr", "url": "http://sonarr.local:8989",
		"api_key": "key-value", "sync_level": "full",
	})
	cases := []map[string]any{
		{"min_seeders": -1},
		{"categories": []int{5000, -1}},
		{"categories": "5000,nope"},
	}
	for _, payload := range cases {
		res := e.do(http.MethodPut, fmt.Sprintf("/api/v2/applications/%d", created.ID),
			jsonBody(payload))
		requireStatus(t, res, http.StatusBadRequest)
	}
	res := e.do(http.MethodPost, "/api/v2/applications", jsonBody(map[string]any{
		"kind": "sonarr", "name": "Bad", "url": "http://sonarr.local:8989",
		"api_key": "key-value", "min_seeders": -2,
	}))
	requireStatus(t, res, http.StatusBadRequest)
}

func TestApplicationValidation(t *testing.T) {
	e := newEnv(t, true, true)

	valid := map[string]any{
		"kind": "sonarr", "name": "Sonarr", "url": "http://sonarr.local:8989",
		"api_key": "key-value", "sync_level": "full",
	}
	tests := []struct {
		name    string
		mutate  func(map[string]any)
		message string
	}{
		{"unknown kind", func(m map[string]any) { m["kind"] = "jackett" }, "kind"},
		{"missing name", func(m map[string]any) { m["name"] = " " }, "name"},
		{"missing url", func(m map[string]any) { m["url"] = "" }, "url"},
		{"bad url scheme", func(m map[string]any) { m["url"] = "ftp://sonarr.local" }, "url"},
		{"missing api key", func(m map[string]any) { m["api_key"] = "" }, "api_key"},
		{"bad sync level", func(m map[string]any) { m["sync_level"] = "sometimes" }, "sync_level"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			payload := make(map[string]any, len(valid))
			for key, value := range valid {
				payload[key] = value
			}
			test.mutate(payload)
			res := e.do(http.MethodPost, "/api/v2/applications", jsonBody(payload))
			requireStatus(t, res, http.StatusBadRequest)
			if got := decodeJSON[map[string]string](t, res.body)["error"]; !strings.Contains(got, test.message) {
				t.Errorf("error = %q, want it to mention %q", got, test.message)
			}
		})
	}

	created := createTestApplication(t, e, valid)
	// Unknown sync_level and unknown kind on update are rejected.
	res := e.do(http.MethodPut, fmt.Sprintf("/api/v2/applications/%d", created.ID),
		jsonBody(map[string]any{"kind": "nope"}))
	requireStatus(t, res, http.StatusBadRequest)
	res = e.do(http.MethodPut, fmt.Sprintf("/api/v2/applications/%d", created.ID),
		jsonBody(map[string]any{"sync_level": "all"}))
	requireStatus(t, res, http.StatusBadRequest)
	// Empty update fields leave the application unchanged.
	res = e.do(http.MethodPut, fmt.Sprintf("/api/v2/applications/%d", created.ID), jsonBody(map[string]any{}))
	requireStatus(t, res, http.StatusOK)
	if got := decodeJSON[applicationResponse](t, res.body); got.Name != "Sonarr" {
		t.Errorf("empty update changed the name to %q", got.Name)
	}
	// Unknown id.
	res = e.do(http.MethodGet, "/api/v2/applications/9999", "")
	requireStatus(t, res, http.StatusNotFound)
}

func TestApplicationCreateRequiresVault(t *testing.T) {
	e := newEnv(t, true, false)
	res := e.do(http.MethodPost, "/api/v2/applications", jsonBody(map[string]any{
		"kind": "sonarr", "name": "Sonarr", "url": "http://sonarr.local:8989",
		"api_key": "key-value", "sync_level": "full",
	}))
	requireStatus(t, res, http.StatusServiceUnavailable)
}

func TestApplicationSyncRequiresService(t *testing.T) {
	e := newEnv(t, true, true)
	created := createTestApplication(t, e, map[string]any{
		"kind": "sonarr", "name": "Sonarr", "url": "http://sonarr.local:8989",
		"api_key": "key-value", "sync_level": "full",
	})

	res := e.do(http.MethodPost, fmt.Sprintf("/api/v2/applications/%d/sync", created.ID), "")
	requireStatus(t, res, http.StatusServiceUnavailable)
	if got := decodeJSON[map[string]string](t, res.body)["error"]; !strings.Contains(got, "sync service") {
		t.Errorf("error = %q", got)
	}

	res = e.do(http.MethodPost, "/api/v2/applications/sync", "")
	requireStatus(t, res, http.StatusServiceUnavailable)

	// The application still exists and is unchanged after the failed call.
	stored, err := e.store.GetApplication(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("GetApplication: %v", err)
	}
	if stored.LastError != "" || !stored.LastSyncAt.IsZero() {
		t.Errorf("sync endpoint changed the application: %+v", stored)
	}
}

// TestApplicationSyncAllEmpty wires a real syncjob.Service with no
// applications to prove the optional Config.Sync field is honoured.
func TestApplicationSyncAllEmpty(t *testing.T) {
	e := newEnv(t, true, true)
	svc, err := syncjob.New(syncjob.Config{
		Store:   e.store,
		Vault:   e.vault,
		Logger:  e.server.logger,
		BaseURL: "https://indexarr.example",
	})
	if err != nil {
		t.Fatalf("syncjob.New: %v", err)
	}
	srv, err := New(Config{
		Store:  e.store,
		Vault:  e.vault,
		Sync:   svc,
		Logger: e.server.logger,
	})
	if err != nil {
		t.Fatalf("adminapi.New: %v", err)
	}
	e.handler = srv.Handler()

	res := e.do(http.MethodPost, "/api/v2/applications/sync", "")
	requireStatus(t, res, http.StatusOK)
	body := decodeJSON[struct {
		Results []applicationSyncAllResult `json:"results"`
	}](t, res.body)
	if len(body.Results) != 0 {
		t.Fatalf("results = %+v, want none", body.Results)
	}
}

// assertNoSecret checks that neither the plaintext key nor the sealed column
// name appears in a response body.
func assertNoSecret(t *testing.T, body []byte, secret string) {
	t.Helper()
	text := string(body)
	if strings.Contains(text, secret) {
		t.Errorf("response leaks the API key: %s", text)
	}
	if strings.Contains(text, "api_key_enc") {
		t.Errorf("response leaks the sealed API key: %s", text)
	}
}

// TestApplicationResponseShape pins the public JSON keys.
func TestApplicationResponseShape(t *testing.T) {
	e := newEnv(t, true, true)
	created := createTestApplication(t, e, map[string]any{
		"kind": "radarr", "name": "Radarr", "url": "http://radarr.local:7878",
		"api_key": "another-key", "sync_level": "add-remove",
	})
	res := e.do(http.MethodGet, fmt.Sprintf("/api/v2/applications/%d", created.ID), "")
	requireStatus(t, res, http.StatusOK)
	body := decodeJSON[map[string]any](t, res.body)
	for _, key := range []string{"id", "kind", "name", "url", "sync_level",
		"enable_rss", "enable_automatic_search", "enable_interactive_search",
		"min_seeders", "categories",
		"last_sync_at", "last_error", "has_api_key"} {
		if _, ok := body[key]; !ok {
			t.Errorf("response is missing %q: %s", key, res.body)
		}
	}
	if _, ok := body["api_key"]; ok {
		t.Error("response contains api_key")
	}
	// Confirm the store row is what the response claims.
	stored, err := e.store.GetApplication(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("GetApplication: %v", err)
	}
	if stored.Kind != "radarr" || stored.SyncLevel != "add-remove" ||
		len(stored.APIKeyEnc) == 0 {
		t.Fatalf("stored application = %+v", stored)
	}
}
