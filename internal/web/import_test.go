package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
	for _, want := range []string{"Importa", "Prowlarr", "Jackett", "Anteprima, nessuna modifica", "crea definizioni"} {
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
		"Piano da prowlarr", "Alpha", "Bravo", "Torznab", "Newznab",
		"torrent", "usenet", "Sonarr", "Radarr", "add-remove",
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
	if !strings.Contains(body, "Import da Prowlarr non riuscito") {
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
	for _, want := range []string{"Piano da jackett", "Alpha Jackett", "Cardigann", "torrent", "2000"} {
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
	if !strings.Contains(recorder.Body.String(), "Import da Jackett non riuscito") {
		t.Errorf("missing Italian error: %s", recorder.Body.String())
	}
}

func writeImportTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile(%s): %v", path, err)
	}
}
