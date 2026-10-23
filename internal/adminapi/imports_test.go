package adminapi

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/buzzqw/indexarr/internal/legacyimport"
	"github.com/buzzqw/indexarr/internal/store"
)

const (
	importInputKey    = "prowlarr-INPUT-KEY-SECRET"
	importTrackerKey  = "tracker-API-KEY-SECRET"
	importTrackerPass = "tracker-PASSWORD-SECRET"
	importTrackerUser = "tracker-USERNAME-SECRET"
	importAppKey      = "sonarr-API-KEY-SECRET"
	importURLPass     = "url-PASSWORD-SECRET"
	importURLKey      = "url-API-KEY-SECRET"
	importJackettKey  = "jackett-API-KEY-SECRET"
)

const importIndexersJSON = `[
  {
    "id": 1,
    "name": "Alpha",
    "implementation": "Torznab",
    "protocol": "torrent",
    "priority": 10,
    "enable": true,
    "tags": ["hd"],
    "fields": [
      {"name": "baseUrl", "value": "https://user:` + importURLPass + `@alpha.example/api?apikey=` + importURLKey + `"},
      {"name": "apiKey", "value": "` + importTrackerKey + `"},
      {"name": "username", "value": "` + importTrackerUser + `"},
      {"name": "password", "value": "` + importTrackerPass + `"}
    ],
    "capabilities": {
      "categories": [{"id": 5000}, {"id": 2000}]
    }
  }
]`

const importApplicationsJSON = `[
  {
    "id": 1,
    "name": "Sonarr",
    "implementation": "Sonarr",
    "syncLevel": "full",
    "fields": [
      {"name": "baseUrl", "value": "http://sonarr:8989"},
      {"name": "apiKey", "value": "` + importAppKey + `"}
    ]
  }
]`

func newImportProwlarrServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	handler := func(body string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-Api-Key") != importInputKey {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, body)
		}
	}
	mux.HandleFunc("/api/v1/indexer", handler(importIndexersJSON))
	mux.HandleFunc("/api/v1/applications", handler(importApplicationsJSON))
	return httptest.NewServer(mux)
}

func assertImportSecretsAbsent(t *testing.T, body []byte, secrets ...string) {
	t.Helper()
	for _, secret := range secrets {
		if strings.Contains(string(body), secret) {
			t.Fatalf("response leaked %q: %s", secret, body)
		}
	}
}

func TestImportProwlarr(t *testing.T) {
	e := newEnv(t, true, true)
	server := newImportProwlarrServer(t)
	defer server.Close()

	res := e.do(http.MethodPost, "/api/v2/import/prowlarr", jsonBody(map[string]string{
		"url":     server.URL,
		"api_key": importInputKey,
	}))
	requireStatus(t, res, http.StatusOK)

	body := decodeJSON[importResponse](t, res.body)
	if body.Source != "prowlarr" {
		t.Fatalf("source = %q", body.Source)
	}
	if len(body.Indexers) != 1 {
		t.Fatalf("indexers = %+v", body.Indexers)
	}
	indexer := body.Indexers[0]
	if indexer.Name != "Alpha" || indexer.Implementation != "Torznab" ||
		indexer.Protocol != "torrent" || indexer.DefinitionHint != "torznab-alpha" {
		t.Fatalf("indexer = %+v", indexer)
	}
	if indexer.BaseURL != "https://alpha.example/api" {
		t.Errorf("base URL = %q, want userinfo and apikey removed", indexer.BaseURL)
	}
	if !indexer.HasKey || !indexer.HasBasicAuth {
		t.Errorf("credential flags = %+v", indexer)
	}
	if len(indexer.Categories) != 2 || indexer.Categories[0] != 2000 ||
		indexer.Categories[1] != 5000 {
		t.Errorf("categories = %v", indexer.Categories)
	}
	if !indexer.Enabled || indexer.Priority != 10 {
		t.Errorf("indexer state = %+v", indexer)
	}

	if len(body.Applications) != 1 {
		t.Fatalf("applications = %+v", body.Applications)
	}
	app := body.Applications[0]
	if app.Kind != "sonarr" || app.Name != "Sonarr" || app.URL != "http://sonarr:8989" ||
		app.SyncLevel != "full" || !app.HasKey {
		t.Fatalf("application = %+v", app)
	}

	assertImportSecretsAbsent(t, res.body, importInputKey, importTrackerKey,
		importTrackerPass, importTrackerUser, importAppKey, importURLPass, importURLKey)
	if strings.Contains(string(res.body), `"api_key"`) {
		t.Fatalf("response has an api_key field: %s", res.body)
	}
	if strings.Contains(string(res.body), `"warnings":null`) {
		t.Fatalf("warnings must be an array: %s", res.body)
	}
}

func TestImportProwlarrValidation(t *testing.T) {
	e := newEnv(t, true, true)

	for _, test := range []struct {
		name string
		url  string
	}{
		{"empty", ""},
		{"relative", "not a url"},
		{"non-http", "ftp://prowlarr.example"},
		{"no host", "https://"},
	} {
		t.Run(test.name, func(t *testing.T) {
			res := e.do(http.MethodPost, "/api/v2/import/prowlarr", jsonBody(map[string]string{
				"url":     test.url,
				"api_key": importInputKey,
			}))
			requireStatus(t, res, http.StatusBadRequest)
			errBody := decodeJSON[map[string]string](t, res.body)
			if errBody["error"] == "" {
				t.Fatalf("no error message: %s", res.body)
			}
			assertImportSecretsAbsent(t, res.body, importInputKey)
		})
	}

	res := e.do(http.MethodPost, "/api/v2/import/prowlarr",
		`{"url":"https://prowlarr.example","api_key":"`+importInputKey+`","bogus":true}`)
	requireStatus(t, res, http.StatusBadRequest)
	assertImportSecretsAbsent(t, res.body, importInputKey)
}

func TestImportProwlarrFailureIsGeneric(t *testing.T) {
	e := newEnv(t, true, true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, "rejected "+importInputKey)
	}))
	defer server.Close()

	res := e.do(http.MethodPost, "/api/v2/import/prowlarr", jsonBody(map[string]string{
		"url":     server.URL,
		"api_key": importInputKey,
	}))
	requireStatus(t, res, http.StatusBadGateway)
	body := decodeJSON[map[string]string](t, res.body)
	if body["error"] != "prowlarr import failed" {
		t.Fatalf("error = %q, want a generic message", body["error"])
	}
	assertImportSecretsAbsent(t, res.body, importInputKey, "rejected")
}

func TestImportJackett(t *testing.T) {
	e := newEnv(t, true, true)
	dir := t.TempDir()
	writeImportFile(t, filepath.Join(dir, "ServerConfig.json"),
		`{"Port": 9117, "APIKey": "`+importJackettKey+`"}`)
	writeImportFile(t, filepath.Join(dir, "Indexers", "alpha.json"),
		`{"id": "alpha", "name": "Alpha", "site_link": "https://alpha.example/", "enabled": true, "categories": [2000, 5000]}`)

	res := e.do(http.MethodPost, "/api/v2/import/jackett", jsonBody(map[string]string{"path": dir}))
	requireStatus(t, res, http.StatusOK)
	body := decodeJSON[importResponse](t, res.body)
	if body.Source != "jackett" || len(body.Indexers) != 1 {
		t.Fatalf("response = %+v", body)
	}
	indexer := body.Indexers[0]
	if indexer.Name != "Alpha" || !indexer.HasKey {
		t.Fatalf("indexer = %+v", indexer)
	}
	if indexer.BaseURL != "http://127.0.0.1:9117/api/v2.0/indexers/alpha/results/torznab/" {
		t.Errorf("base URL = %q", indexer.BaseURL)
	}
	assertImportSecretsAbsent(t, res.body, importJackettKey)
}

func TestImportJackettValidation(t *testing.T) {
	e := newEnv(t, true, true)

	for _, path := range []string{"", "relative/path", ".."} {
		res := e.do(http.MethodPost, "/api/v2/import/jackett", jsonBody(map[string]string{"path": path}))
		requireStatus(t, res, http.StatusBadRequest)
		decodeJSON[map[string]string](t, res.body)
	}

	res := e.do(http.MethodPost, "/api/v2/import/jackett",
		jsonBody(map[string]string{"path": filepath.Join(t.TempDir(), "missing")}))
	requireStatus(t, res, http.StatusBadGateway)
	body := decodeJSON[map[string]string](t, res.body)
	if body["error"] != "jackett import failed" {
		t.Fatalf("error = %q, want a generic message", body["error"])
	}
}

func TestLegacyPreviewEndpoint(t *testing.T) {
	e := newEnv(t, true, true)
	ctx := context.Background()

	if _, err := e.store.CreateSource(ctx, store.Source{
		Name:     "Native Torznab",
		Kind:     "torznab",
		URL:      "https://tracker.example/api",
		APIKey:   "legacy-API-KEY-SECRET",
		Password: "legacy-PASSWORD-SECRET",
	}); err != nil {
		t.Fatalf("CreateSource torznab: %v", err)
	}
	if _, err := e.store.CreateSource(ctx, store.Source{
		Name: "RSS Feed",
		Kind: "rss",
		URL:  "https://feed.example/rss",
	}); err != nil {
		t.Fatalf("CreateSource rss: %v", err)
	}
	if _, err := e.store.BackfillLegacySources(ctx); err != nil {
		t.Fatalf("BackfillLegacySources: %v", err)
	}

	res := e.do(http.MethodGet, "/api/v2/legacy/preview", "")
	requireStatus(t, res, http.StatusOK)
	body := decodeJSON[struct {
		Candidates []legacyimport.Candidate `json:"candidates"`
		Counts     struct {
			Native  int `json:"native"`
			Skipped int `json:"skipped"`
		} `json:"counts"`
	}](t, res.body)
	if len(body.Candidates) != 2 {
		t.Fatalf("candidates = %+v", body.Candidates)
	}
	if body.Counts.Native != 2 || body.Counts.Skipped != 0 {
		t.Fatalf("counts = %+v", body.Counts)
	}
	for _, candidate := range body.Candidates {
		switch candidate.Kind {
		case "torznab":
			if !candidate.Native || candidate.Slug == "" || !candidate.HasAPIKey {
				t.Errorf("torznab candidate = %+v", candidate)
			}
		case "rss":
			// WP6: a plain RSS/Atom feed is a native candidate.
			if !candidate.Native || candidate.Reason != "" || candidate.Slug == "" {
				t.Errorf("rss candidate = %+v", candidate)
			}
		default:
			t.Errorf("unexpected candidate = %+v", candidate)
		}
	}
	assertImportSecretsAbsent(t, res.body, "legacy-API-KEY-SECRET", "legacy-PASSWORD-SECRET")
}

func writeImportFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile(%s): %v", path, err)
	}
}
