package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

var importTokenPattern = regexp.MustCompile(`name="token" value="([A-Za-z0-9_-]+)"`)

const webProwlarrIndexersJSON = `[
  {
    "id": 1,
    "name": "Alpha",
    "implementation": "Torznab",
    "protocol": "torrent",
    "enable": true,
    "fields": [
      {"name": "baseUrl", "value": "https://alpha.example.invalid/"},
      {"name": "apiKey", "value": "alpha-key-SECRET"}
    ],
    "capabilities": {"categories": [{"id": 5000}, {"id": 2000}]}
  },
  {
    "id": 2,
    "name": "Bravo",
    "implementation": "Newznab",
    "protocol": "usenet",
    "enable": false,
    "fields": [
      {"name": "baseUrl", "value": "https://bravo.example.invalid/"},
      {"name": "apiKey", "value": "bravo-key-SECRET"}
    ],
    "capabilities": {"categories": [{"id": 5040}]}
  }
]`

const webProwlarrApplicationsJSON = `[
  {
    "id": 1,
    "name": "Sonarr",
    "implementation": "Sonarr",
    "syncLevel": "full",
    "fields": [
      {"name": "baseUrl", "value": "http://sonarr:8989"},
      {"name": "apiKey", "value": "sonarr-key-SECRET"}
    ]
  },
  {
    "id": 2,
    "name": "Radarr",
    "implementation": "Radarr",
    "syncLevel": "add-remove",
    "fields": [
      {"name": "baseUrl", "value": "http://radarr:7878"},
      {"name": "apiKey", "value": "radarr-key-SECRET"}
    ]
  }
]`

func newWebProwlarr(t *testing.T, apiKey string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	write := func(body string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-Api-Key") != apiKey {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, body)
		}
	}
	mux.HandleFunc("/api/v1/indexer", write(webProwlarrIndexersJSON))
	mux.HandleFunc("/api/v1/applications", write(webProwlarrApplicationsJSON))
	return httptest.NewServer(mux)
}

func TestImportPageRenders(t *testing.T) {
	env := newTestEnv(t)
	recorder := get(t, env.handler, "/import")
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /import = %d: %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	for _, want := range []string{"Import", "Prowlarr", "Jackett", "Preview, no changes", "does not create definitions"} {
		if !strings.Contains(body, want) {
			t.Errorf("import page missing %q", want)
		}
	}
}

func TestImportProwlarrRendersPlanWithoutSecrets(t *testing.T) {
	env := newTestEnv(t)
	const apiKey = "prowlarr-key-SECRET"
	upstream := newWebProwlarr(t, apiKey)
	defer upstream.Close()

	token, cookie := csrfFrom(t, env.handler, "/import")
	recorder := postForm(t, env.handler, "/import/prowlarr", url.Values{
		"csrf":     {token},
		"base_url": {upstream.URL},
		"api_key":  {apiKey},
	}, cookie)
	if recorder.Code != http.StatusOK {
		t.Fatalf("import status = %d: %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	for _, want := range []string{
		"Plan from prowlarr", "Alpha", "Bravo", "Torznab", "Newznab",
		"torrent", "usenet", "Sonarr", "Radarr", "Add/remove only",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("plan missing %q", want)
		}
	}
	for _, secret := range []string{
		apiKey, "alpha-key-SECRET", "bravo-key-SECRET",
		"sonarr-key-SECRET", "radarr-key-SECRET",
	} {
		if strings.Contains(body, secret) {
			t.Fatalf("the plan leaked secret %q", secret)
		}
	}
}

func TestImportProwlarrErrorDoesNotLeakKey(t *testing.T) {
	env := newTestEnv(t)
	const apiKey = "prowlarr-key-SECRET"
	upstream := newWebProwlarr(t, "another-key")
	defer upstream.Close()

	token, cookie := csrfFrom(t, env.handler, "/import")
	recorder := postForm(t, env.handler, "/import/prowlarr", url.Values{
		"csrf":     {token},
		"base_url": {upstream.URL},
		"api_key":  {apiKey},
	}, cookie)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "Prowlarr import failed") {
		t.Errorf("missing Italian error: %s", body)
	}
	if strings.Contains(body, apiKey) {
		t.Fatal("the error leaked the submitted API key")
	}
}

func TestImportJackettRendersPlan(t *testing.T) {
	env := newTestEnv(t)
	const apiKey = "jackett-key-SECRET"
	dir := t.TempDir()
	writeImportTestFile(t, filepath.Join(dir, "ServerConfig.json"),
		`{"Port": 9117, "LocalBindAddress": "127.0.0.1", "APIKey": "`+apiKey+`"}`)
	if err := os.Mkdir(filepath.Join(dir, "Indexers"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeImportTestFile(t, filepath.Join(dir, "Indexers", "alpha.json"),
		`{"id": "alpha", "name": "Alpha Jackett", "enabled": true, "torrentEnabled": true,
		  "site_link": "https://alpha.example.invalid/", "categories": [{"id": 2000}]}`)

	token, cookie := csrfFrom(t, env.handler, "/import")
	recorder := postForm(t, env.handler, "/import/jackett", url.Values{
		"csrf": {token},
		"path": {dir},
	}, cookie)
	if recorder.Code != http.StatusOK {
		t.Fatalf("import status = %d: %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	for _, want := range []string{"Plan from jackett", "Alpha Jackett", "Cardigann", "torrent", "2000"} {
		if !strings.Contains(body, want) {
			t.Errorf("plan missing %q", want)
		}
	}
	if strings.Contains(body, apiKey) {
		t.Fatal("the plan leaked the Jackett API key")
	}
}

func TestImportJackettInvalidPath(t *testing.T) {
	env := newTestEnv(t)
	token, cookie := csrfFrom(t, env.handler, "/import")
	recorder := postForm(t, env.handler, "/import/jackett", url.Values{
		"csrf": {token},
		"path": {filepath.Join(t.TempDir(), "missing")},
	}, cookie)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "Jackett import failed") {
		t.Errorf("missing Italian error: %s", recorder.Body.String())
	}
}

func writeImportTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile(%s): %v", path, err)
	}
}

func importPlanToken(t *testing.T, body string) string {
	t.Helper()
	match := importTokenPattern.FindStringSubmatch(body)
	if match == nil {
		t.Fatalf("no plan token in preview: %s", body)
	}
	return match[1]
}

func TestImportApplyImportsOnlySelectedSites(t *testing.T) {
	env := newTestEnv(t)
	const apiKey = "prowlarr-key-SECRET"
	upstream := newWebProwlarr(t, apiKey)
	defer upstream.Close()

	token, cookie := csrfFrom(t, env.handler, "/import")
	preview := postForm(t, env.handler, "/import/prowlarr", url.Values{
		"csrf":     {token},
		"base_url": {upstream.URL},
		"api_key":  {apiKey},
	}, cookie)
	if preview.Code != http.StatusOK {
		t.Fatalf("preview = %d: %s", preview.Code, preview.Body.String())
	}
	planToken := importPlanToken(t, preview.Body.String())
	if strings.Contains(preview.Body.String(), apiKey) ||
		strings.Contains(preview.Body.String(), "alpha-key-SECRET") {
		t.Fatal("the preview leaked a secret")
	}

	// Only Alpha is selected. Bravo stays out even though it was in the plan.
	applied := postForm(t, env.handler, "/import/apply", url.Values{
		"csrf":  {token},
		"token": {planToken},
		"site":  {"0"},
	}, cookie)
	if applied.Code != http.StatusOK {
		t.Fatalf("apply = %d: %s", applied.Code, applied.Body.String())
	}
	body := applied.Body.String()
	for _, want := range []string{
		"Import result", "Added: 1", "Updated: 0", "Skipped: 0", "Errors: 0",
		"Alpha", "via Prowlarr", "Connect an app",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("summary missing %q", want)
		}
	}
	for _, secret := range []string{apiKey, "alpha-key-SECRET", "bravo-key-SECRET"} {
		if strings.Contains(body, secret) {
			t.Fatalf("the apply result leaked secret %q", secret)
		}
	}

	rows, err := env.store.ListIndexers(t.Context())
	if err != nil {
		t.Fatalf("ListIndexers: %v", err)
	}
	if len(rows) != 1 || rows[0].Name != "Alpha" {
		t.Fatalf("imported rows = %+v, want only Alpha", rows)
	}
	if rows[0].DefinitionID != "torznab-generic" {
		t.Fatalf("Alpha definition = %q", rows[0].DefinitionID)
	}
	var sources int
	if err := env.store.DB().QueryRowContext(t.Context(),
		`SELECT count(*) FROM sources`).Scan(&sources); err != nil {
		t.Fatalf("count sources: %v", err)
	}
	if sources != 0 {
		t.Fatalf("apply wrote %d legacy sources", sources)
	}

	// The token is one-shot.
	replay := postForm(t, env.handler, "/import/apply", url.Values{
		"csrf":  {token},
		"token": {planToken},
		"site":  {"0"},
	}, cookie)
	if replay.Code != http.StatusGone {
		t.Fatalf("replayed apply = %d, want 410: %s", replay.Code, replay.Body.String())
	}
}

func TestImportApplyUnknownToken(t *testing.T) {
	env := newTestEnv(t)
	token, cookie := csrfFrom(t, env.handler, "/import")
	recorder := postForm(t, env.handler, "/import/apply", url.Values{
		"csrf":  {token},
		"token": {"not-a-plan"},
		"site":  {"0"},
	}, cookie)
	if recorder.Code != http.StatusGone {
		t.Fatalf("status = %d, want 410: %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "expired or is unknown") {
		t.Errorf("missing expiry message: %s", recorder.Body.String())
	}
}

func TestImportApplyRequiresSelection(t *testing.T) {
	env := newTestEnv(t)
	const apiKey = "prowlarr-key-SECRET"
	upstream := newWebProwlarr(t, apiKey)
	defer upstream.Close()

	token, cookie := csrfFrom(t, env.handler, "/import")
	preview := postForm(t, env.handler, "/import/prowlarr", url.Values{
		"csrf":     {token},
		"base_url": {upstream.URL},
		"api_key":  {apiKey},
	}, cookie)
	planToken := importPlanToken(t, preview.Body.String())

	applied := postForm(t, env.handler, "/import/apply", url.Values{
		"csrf":  {token},
		"token": {planToken},
	}, cookie)
	if applied.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", applied.Code, applied.Body.String())
	}
	if !strings.Contains(applied.Body.String(), "at least one site") {
		t.Errorf("missing selection message: %s", applied.Body.String())
	}
}

func TestImportPlanStoreExpiresTokens(t *testing.T) {
	plans := newImportPlanStore()
	base := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	plans.now = func() time.Time { return base }

	token, err := plans.put(nil)
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if _, ok := plans.get(token); !ok {
		t.Fatal("fresh token was not found")
	}
	if _, ok := plans.get("unknown-token"); ok {
		t.Fatal("unknown token was accepted")
	}

	plans.now = func() time.Time { return base.Add(importPlanTTL) }
	if _, ok := plans.get(token); !ok {
		t.Fatal("token expired exactly at the TTL boundary")
	}
	plans.now = func() time.Time { return base.Add(importPlanTTL + time.Second) }
	if _, ok := plans.get(token); ok {
		t.Fatal("expired token was accepted")
	}
}

func TestImportPagePrefillsPublicURL(t *testing.T) {
	env := newTestEnv(t)
	recorder := get(t, env.handler, "/import")
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /import = %d", recorder.Code)
	}
	body := recorder.Body.String()
	for _, want := range []string{
		"http://example.com", "No public URL is configured yet",
		"Verify that your apps can reach it", `href="/settings"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("import page missing %q", want)
		}
	}
}
