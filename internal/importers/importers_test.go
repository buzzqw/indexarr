package importers

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	prowlarrAPIKey = "prowlarr-api-key-SECRET"
	alphaAPIKey    = "alpha-key-SECRET"
	alphaPassword  = "alpha-pass-SECRET"
	bravoAPIKey    = "bravo-key-SECRET"
	charlieUser    = "charlie-user-SECRET"
	charliePass    = "charlie-pass-SECRET"
	sonarrAPIKey   = "sonarr-key-SECRET"
	aardvarkAPIKey = "aardvark-key-SECRET"
	urlPassword    = "url-pass-SECRET"

	jackettAPIKey    = "jackett-api-key-SECRET"
	jackettProxyUser = "proxy-user-SECRET"
	jackettProxyPass = "proxy-pass-SECRET"
)

const prowlarrIndexersJSON = `[
  {
    "id": 1,
    "name": "Alpha",
    "implementation": "Torznab",
    "protocol": "torrent",
    "priority": 10,
    "enable": true,
    "tags": [2, "hd"],
    "fields": [
      {"name": "baseUrl", "value": "https://alpha.example.invalid/"},
      {"name": "apiPath", "value": "/api"},
      {"name": "apiKey", "value": "alpha-key-SECRET"},
      {"name": "username", "value": "alpha-user-SECRET"},
      {"name": "password", "value": "alpha-pass-SECRET"}
    ],
    "capabilities": {
      "categories": [
        {"id": 5000, "name": "TV"},
        {"id": 2000, "name": "Movies"}
      ]
    }
  },
  {
    "id": 2,
    "name": "Bravo",
    "implementation": "Newznab",
    "protocol": "usenet",
    "priority": 20,
    "enable": true,
    "fields": [
      {"name": "baseUrl", "value": "https://bravo.example.invalid/"},
      {"name": "apiKey", "value": "bravo-key-SECRET"}
    ],
    "capabilities": {
      "categories": [{"id": 5040}, {"id": 5030}]
    }
  },
  {
    "id": 3,
    "name": "Charlie Tracker",
    "implementation": "Cardigann",
    "protocol": "torrent",
    "priority": 30,
    "enable": false,
    "fields": [
      {"name": "definitionId", "value": "charlie-def"},
      {"name": "baseUrl", "value": "https://charlie.example.invalid/"},
      {"name": "username", "value": "charlie-user-SECRET"},
      {"name": "password", "value": "charlie-pass-SECRET"},
      {"name": "categories", "value": "100, 200"}
    ]
  },
  null,
  {
    "id": 5,
    "name": "No Implementation",
    "protocol": "torrent"
  },
  {
    "id": 4,
    "name": "Aardvark",
    "implementation": "Torznab",
    "protocol": "torrent",
    "priority": 40,
    "enable": true,
    "fields": [
      {"name": "baseUrl", "value": "https://aardvark.example.invalid/"},
      {"name": "apiKey", "value": "aardvark-key-SECRET"}
    ]
  }
]`

const prowlarrApplicationsJSON = `[
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
    "syncLevel": "addOnly",
    "fields": [
      {"name": "baseUrl", "value": "http://radarr:7878"},
      {"name": "apiKey", "value": "radarr-key-SECRET"}
    ]
  },
  {
    "id": 3,
    "name": "Bazarr",
    "implementation": "Bazarr",
    "fields": [
      {"name": "baseUrl", "value": "http://bazarr:6767"}
    ]
  }
]`

func newProwlarrServer(t *testing.T, apiKey string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	writeJSON := func(body string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-Api-Key") != apiKey {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if accept := r.Header.Get("Accept"); accept != "application/json" {
				t.Errorf("Accept header = %q, want application/json", accept)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, body)
		}
	}
	mux.HandleFunc("/api/v1/indexer", writeJSON(prowlarrIndexersJSON))
	mux.HandleFunc("/api/v1/applications", writeJSON(prowlarrApplicationsJSON))
	return httptest.NewServer(mux)
}

func requireSecretsAbsent(t *testing.T, text string, secrets ...string) {
	t.Helper()
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		if strings.Contains(text, secret) {
			t.Fatalf("text leaks secret %q: %s", secret, text)
		}
	}
}

func TestFromProwlarr(t *testing.T) {
	server := newProwlarrServer(t, prowlarrAPIKey)
	defer server.Close()

	plan, err := FromProwlarr(context.Background(), server.URL, prowlarrAPIKey, nil)
	if err != nil {
		t.Fatalf("FromProwlarr: %v", err)
	}
	if plan.Source != "prowlarr" {
		t.Fatalf("Source = %q, want prowlarr", plan.Source)
	}

	if len(plan.Indexers) != 4 {
		t.Fatalf("got %d indexers, want 4: %+v", len(plan.Indexers), plan.Indexers)
	}
	names := []string{plan.Indexers[0].Name, plan.Indexers[1].Name,
		plan.Indexers[2].Name, plan.Indexers[3].Name}
	wantNames := []string{"Aardvark", "Alpha", "Bravo", "Charlie Tracker"}
	for i := range wantNames {
		if names[i] != wantNames[i] {
			t.Fatalf("indexer order = %v, want %v (sorted by name even though Aardvark is last in the API)", names, wantNames)
		}
	}
	if plan.Indexers[0].DefinitionHint != "torznab-aardvark" {
		t.Fatalf("aardvark DefinitionHint = %q", plan.Indexers[0].DefinitionHint)
	}

	alpha := plan.Indexers[1]
	if alpha.Implementation != "Torznab" || alpha.Protocol != ProtocolTorrent {
		t.Fatalf("alpha protocol/implementation = %q/%q", alpha.Protocol, alpha.Implementation)
	}
	if alpha.DefinitionHint != "torznab-alpha" {
		t.Fatalf("alpha DefinitionHint = %q", alpha.DefinitionHint)
	}
	if alpha.BaseURL != "https://alpha.example.invalid/" {
		t.Fatalf("alpha BaseURL = %q", alpha.BaseURL)
	}
	if alpha.APIKey != alphaAPIKey || alpha.Username != "alpha-user-SECRET" || alpha.Password != alphaPassword {
		t.Fatalf("alpha secrets were not mapped")
	}
	if len(alpha.Categories) != 2 || alpha.Categories[0] != 2000 || alpha.Categories[1] != 5000 {
		t.Fatalf("alpha Categories = %v", alpha.Categories)
	}
	if len(alpha.Tags) != 2 || alpha.Tags[0] != "2" || alpha.Tags[1] != "hd" {
		t.Fatalf("alpha Tags = %v", alpha.Tags)
	}
	if alpha.Priority != 10 || !alpha.Enabled {
		t.Fatalf("alpha priority/enabled = %d/%v", alpha.Priority, alpha.Enabled)
	}
	if alpha.Fields["apiPath"] != "/api" {
		t.Fatalf("alpha Fields = %v", alpha.Fields)
	}
	for _, secretName := range []string{"apiKey", "username", "password"} {
		if _, ok := alpha.Fields[secretName]; ok {
			t.Fatalf("alpha Fields contains secret field %q: %v", secretName, alpha.Fields)
		}
	}

	bravo := plan.Indexers[2]
	if bravo.Protocol != ProtocolUsenet || bravo.DefinitionHint != "newznab-bravo" {
		t.Fatalf("bravo = %+v", bravo)
	}
	if len(bravo.Categories) != 2 || bravo.Categories[0] != 5030 || bravo.Categories[1] != 5040 {
		t.Fatalf("bravo Categories = %v", bravo.Categories)
	}
	if bravo.APIKey != bravoAPIKey {
		t.Fatalf("bravo API key was not mapped")
	}

	charlie := plan.Indexers[3]
	if charlie.DefinitionHint != "charlie-def" {
		t.Fatalf("charlie DefinitionHint = %q", charlie.DefinitionHint)
	}
	if charlie.Enabled {
		t.Fatalf("charlie should be disabled")
	}
	if len(charlie.Categories) != 2 || charlie.Categories[0] != 100 || charlie.Categories[1] != 200 {
		t.Fatalf("charlie Categories = %v", charlie.Categories)
	}
	if charlie.Username != charlieUser || charlie.Password != charliePass {
		t.Fatalf("charlie credentials were not mapped")
	}

	if len(plan.Applications) != 2 {
		t.Fatalf("got %d applications, want 2: %+v", len(plan.Applications), plan.Applications)
	}
	if plan.Applications[0].Name != "Radarr" || plan.Applications[0].Kind != "radarr" ||
		plan.Applications[0].SyncLevel != "addOnly" {
		t.Fatalf("radarr = %+v", plan.Applications[0])
	}
	sonarr := plan.Applications[1]
	if sonarr.Name != "Sonarr" || sonarr.Kind != "sonarr" || sonarr.SyncLevel != "full" ||
		sonarr.URL != "http://sonarr:8989" || sonarr.APIKey != sonarrAPIKey {
		t.Fatalf("sonarr = %+v", sonarr)
	}

	if len(plan.Proxies) != 0 {
		t.Fatalf("got %d Prowlarr proxies, want 0: %+v", len(plan.Proxies), plan.Proxies)
	}

	joined := strings.Join(plan.Warnings, "\n")
	for _, want := range []string{"not a JSON object", "no implementation", "unsupported implementation"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("warnings miss %q:\n%s", want, joined)
		}
	}
	requireSecretsAbsent(t, joined, prowlarrAPIKey, alphaAPIKey, alphaPassword,
		bravoAPIKey, charlieUser, charliePass, sonarrAPIKey, aardvarkAPIKey)
}

func TestFromProwlarrOptionalEndpointsMissing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/indexer" {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, `[]`)
	}))
	defer server.Close()

	plan, err := FromProwlarr(context.Background(), server.URL+"/api/v1/", prowlarrAPIKey, server.Client())
	if err != nil {
		t.Fatalf("FromProwlarr: %v", err)
	}
	if len(plan.Indexers) != 0 {
		t.Fatalf("got %d indexers, want 0", len(plan.Indexers))
	}
	if len(plan.Warnings) != 0 {
		t.Fatalf("404 on optional endpoints should not warn: %v", plan.Warnings)
	}
}

func TestFromProwlarrErrorsDoNotLeakSecrets(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	baseWithCredentials := strings.Replace(server.URL, "http://", "http://prowlarr-user:"+urlPassword+"@", 1)
	_, err := FromProwlarr(context.Background(), baseWithCredentials, prowlarrAPIKey, server.Client())
	if err == nil {
		t.Fatal("expected an error for HTTP 401")
	}
	requireSecretsAbsent(t, err.Error(), prowlarrAPIKey, urlPassword, "prowlarr-user")
	if !strings.Contains(err.Error(), "401") {
		t.Fatalf("error does not mention the status: %v", err)
	}

	// A transport error must not leak the key either.
	closed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	closedURL := closed.URL
	closed.Close()
	_, err = FromProwlarr(context.Background(), closedURL, prowlarrAPIKey, closed.Client())
	if err == nil {
		t.Fatal("expected an error from the closed server")
	}
	requireSecretsAbsent(t, err.Error(), prowlarrAPIKey, urlPassword)
}

func TestFromProwlarrMalformedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"error":"not an array"}`)
	}))
	defer server.Close()

	_, err := FromProwlarr(context.Background(), server.URL, prowlarrAPIKey, server.Client())
	if err == nil {
		t.Fatal("expected a parse error")
	}
	requireSecretsAbsent(t, err.Error(), prowlarrAPIKey)
}

func TestFromProwlarrInvalidBaseURL(t *testing.T) {
	_, err := FromProwlarr(context.Background(), "ftp://example.invalid", prowlarrAPIKey, nil)
	if err == nil {
		t.Fatal("expected an error for a non-http base URL")
	}
	requireSecretsAbsent(t, err.Error(), prowlarrAPIKey)
}

func TestOverrideManagerEndpoint(t *testing.T) {
	plan := Plan{
		Source:        "jackett",
		ManagerURL:    "http://127.0.0.1:9117",
		ManagerAPIKey: "stale-key-SECRET",
		Indexers: []Indexer{
			{RemoteID: "limetorrents", BaseURL: "http://127.0.0.1:9117/api/v2.0/indexers/limetorrents/results/torznab/", APIKey: "stale-key-SECRET"},
			{RemoteID: "torrentgalaxy", BaseURL: "http://127.0.0.1:9117/api/v2.0/indexers/torrentgalaxy/results/torznab/", APIKey: "stale-key-SECRET"},
		},
	}
	updated, err := OverrideManagerEndpoint(plan, "http://192.168.1.161:9117/", "live-key-SECRET")
	if err != nil {
		t.Fatalf("OverrideManagerEndpoint: %v", err)
	}
	if updated.ManagerURL != "http://192.168.1.161:9117" {
		t.Fatalf("manager URL = %q", updated.ManagerURL)
	}
	if updated.ManagerAPIKey != "live-key-SECRET" {
		t.Fatalf("manager key was not replaced")
	}
	for _, indexer := range updated.Indexers {
		want := updated.ManagerURL + "/api/v2.0/indexers/" + indexer.RemoteID + "/results/torznab/"
		if indexer.BaseURL != want {
			t.Errorf("BaseURL for %s = %q, want %q", indexer.RemoteID, indexer.BaseURL, want)
		}
		if indexer.APIKey != "live-key-SECRET" {
			t.Errorf("APIKey for %s was not replaced", indexer.RemoteID)
		}
	}
}

func TestOverrideManagerEndpointKeepsURLWithoutOverride(t *testing.T) {
	plan := Plan{
		Source:        "jackett",
		ManagerURL:    "http://127.0.0.1:9117",
		ManagerAPIKey: "stale-key-SECRET",
		Indexers:      []Indexer{{RemoteID: "limetorrents", BaseURL: "http://127.0.0.1:9117/api/v2.0/indexers/limetorrents/results/torznab/"}},
	}
	updated, err := OverrideManagerEndpoint(plan, "", "live-key-SECRET")
	if err != nil {
		t.Fatalf("OverrideManagerEndpoint: %v", err)
	}
	if updated.ManagerURL != plan.ManagerURL {
		t.Fatalf("manager URL changed to %q", updated.ManagerURL)
	}
	if updated.Indexers[0].APIKey != "live-key-SECRET" {
		t.Fatal("the key was not replaced")
	}
}

func TestOverrideManagerEndpointRejectsBadURLWithoutLeakingKey(t *testing.T) {
	const secret = "never-print-this-key"
	_, err := OverrideManagerEndpoint(Plan{Source: "jackett"}, "ftp://example.invalid", secret)
	if err == nil {
		t.Fatal("expected an error for a non-http URL")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error leaked the key: %v", err)
	}
}

func TestFromJackettConfigDirectory(t *testing.T) {
	plan, err := FromJackettConfig(context.Background(), filepath.Join("testdata", "jackett"))
	if err != nil {
		t.Fatalf("FromJackettConfig: %v", err)
	}
	assertJackettPlan(t, plan)
}

func TestFromJackettConfigDirectoryVariants(t *testing.T) {
	paths := map[string]string{
		"jackett directory":  filepath.Join("testdata", "jackett", "Jackett"),
		"single server file": filepath.Join("testdata", "jackett", "Jackett", "ServerConfig.json"),
	}
	for name, path := range paths {
		t.Run(name, func(t *testing.T) {
			plan, err := FromJackettConfig(context.Background(), path)
			if err != nil {
				t.Fatalf("FromJackettConfig(%q): %v", path, err)
			}
			assertJackettPlan(t, plan)
		})
	}
}

func assertJackettPlan(t *testing.T, plan Plan) {
	t.Helper()
	if plan.Source != "jackett" {
		t.Fatalf("Source = %q, want jackett", plan.Source)
	}
	if len(plan.Indexers) != 2 {
		t.Fatalf("got %d indexers, want 2: %+v", len(plan.Indexers), plan.Indexers)
	}

	torrent := plan.Indexers[0]
	if torrent.Name != "1337x" || torrent.DefinitionHint != "1337x" {
		t.Fatalf("first indexer = %+v", torrent)
	}
	if torrent.Protocol != ProtocolTorrent || !torrent.Enabled {
		t.Fatalf("first indexer protocol/enabled = %q/%v", torrent.Protocol, torrent.Enabled)
	}
	if torrent.BaseURL != "http://127.0.0.1:9117/api/v2.0/indexers/1337x/results/torznab/" {
		t.Fatalf("first indexer URL = %q", torrent.BaseURL)
	}
	if torrent.APIKey != jackettAPIKey {
		t.Fatalf("first indexer API key was not mapped")
	}
	if len(torrent.Categories) != 2 || torrent.Categories[0] != 2000 || torrent.Categories[1] != 5000 {
		t.Fatalf("first indexer categories = %v", torrent.Categories)
	}
	if len(torrent.Tags) != 1 || torrent.Tags[0] != "movies" {
		t.Fatalf("first indexer tags = %v", torrent.Tags)
	}
	if torrent.Fields["type"] != "public" || torrent.Fields["site_link"] != "https://1337x.example.invalid/" {
		t.Fatalf("first indexer fields = %v", torrent.Fields)
	}

	usenet := plan.Indexers[1]
	if usenet.Name != "Test Usenet" || usenet.DefinitionHint != "test-usenet" {
		t.Fatalf("second indexer = %+v", usenet)
	}
	if usenet.Protocol != ProtocolUsenet || usenet.Enabled {
		t.Fatalf("second indexer protocol/enabled = %q/%v", usenet.Protocol, usenet.Enabled)
	}
	if usenet.BaseURL != "http://127.0.0.1:9117/api/v2.0/indexers/test-usenet/results/torznab/" {
		t.Fatalf("second indexer URL = %q", usenet.BaseURL)
	}
	if len(usenet.Categories) != 2 || usenet.Categories[0] != 5030 || usenet.Categories[1] != 5040 {
		t.Fatalf("second indexer categories = %v", usenet.Categories)
	}

	if len(plan.Proxies) != 1 {
		t.Fatalf("got %d proxies, want 1: %+v", len(plan.Proxies), plan.Proxies)
	}
	proxy := plan.Proxies[0]
	if proxy.Kind != "http" || proxy.URL != "http://proxy.example.invalid:8080" ||
		proxy.Username != jackettProxyUser || proxy.Password != jackettProxyPass {
		t.Fatalf("proxy = %+v", proxy)
	}

	if len(plan.Warnings) != 1 {
		t.Fatalf("got %d warnings, want 1: %v", len(plan.Warnings), plan.Warnings)
	}
	if !strings.Contains(plan.Warnings[0], "broken.json") {
		t.Fatalf("warning does not mention the broken file: %v", plan.Warnings)
	}
	requireSecretsAbsent(t, strings.Join(plan.Warnings, "\n"), jackettAPIKey, jackettProxyPass, jackettProxyUser)
}

func TestFromJackettConfigErrorsDoNotLeakSecrets(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "ServerConfig.json"),
		`{"Port": 9117, "APIKey": "`+jackettAPIKey+`",`)

	_, err := FromJackettConfig(context.Background(), dir)
	if err == nil {
		t.Fatal("expected an error for malformed ServerConfig.json")
	}
	requireSecretsAbsent(t, err.Error(), jackettAPIKey, jackettProxyPass)
}

func TestFromJackettConfigMissingPath(t *testing.T) {
	_, err := FromJackettConfig(context.Background(), filepath.Join(t.TempDir(), "missing"))
	if err == nil {
		t.Fatal("expected an error for a missing path")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("error does not wrap os.ErrNotExist: %v", err)
	}
	requireSecretsAbsent(t, err.Error(), jackettAPIKey)
}

func TestFromJackettConfigDirectoryWithoutServerConfig(t *testing.T) {
	dir := t.TempDir()
	_, err := FromJackettConfig(context.Background(), dir)
	if err == nil {
		t.Fatal("expected an error for a directory without ServerConfig.json")
	}
	if !strings.Contains(err.Error(), "ServerConfig.json") {
		t.Fatalf("error = %v, want it to mention ServerConfig.json", err)
	}
}

func TestFromJackettConfigWithoutIndexersDirectory(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "ServerConfig.json"), `{"Port": 9117, "APIKey": "`+jackettAPIKey+`"}`)

	plan, err := FromJackettConfig(context.Background(), dir)
	if err != nil {
		t.Fatalf("FromJackettConfig: %v", err)
	}
	if len(plan.Indexers) != 0 {
		t.Fatalf("got %d indexers, want 0", len(plan.Indexers))
	}
	if len(plan.Warnings) != 1 || !strings.Contains(plan.Warnings[0], "no Indexers directory") {
		t.Fatalf("warnings = %v", plan.Warnings)
	}
}

func TestFromJackettConfigDefaultsAndMissingAPIKey(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "ServerConfig.json"), `{"Port": "not-a-number", "LocalBindAddress": "*"}`)
	writeTestFile(t, filepath.Join(dir, "Indexers", "good.json"),
		`{"id": "good", "name": "Good", "site_link": "https://good.example.invalid/", "enabled": true}`)

	plan, err := FromJackettConfig(context.Background(), dir)
	if err != nil {
		t.Fatalf("FromJackettConfig: %v", err)
	}
	if len(plan.Indexers) != 1 {
		t.Fatalf("got %d indexers, want 1", len(plan.Indexers))
	}
	if plan.Indexers[0].BaseURL != "http://127.0.0.1:9117/api/v2.0/indexers/good/results/torznab/" {
		t.Fatalf("BaseURL = %q", plan.Indexers[0].BaseURL)
	}
	if plan.Indexers[0].APIKey != "" {
		t.Fatalf("API key = %q, want empty", plan.Indexers[0].APIKey)
	}
	joined := strings.Join(plan.Warnings, "\n")
	if !strings.Contains(joined, "invalid port") || !strings.Contains(joined, "no API key") {
		t.Fatalf("warnings = %v", plan.Warnings)
	}
}

func TestFromJackettConfigSkipsMissingURLOrID(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "ServerConfig.json"), `{"Port": 9117, "APIKey": "`+jackettAPIKey+`"}`)
	writeTestFile(t, filepath.Join(dir, "Indexers", "no-id.json"),
		`{"name": "No ID", "site_link": "https://no-id.example.invalid/"}`)
	writeTestFile(t, filepath.Join(dir, "Indexers", "no-url.json"),
		`{"id": "no-url", "name": "No URL"}`)
	writeTestFile(t, filepath.Join(dir, "Indexers", "good.json"),
		`{"id": "good", "name": "Good", "site_link": "https://good.example.invalid/"}`)

	plan, err := FromJackettConfig(context.Background(), dir)
	if err != nil {
		t.Fatalf("FromJackettConfig: %v", err)
	}
	if len(plan.Indexers) != 1 || plan.Indexers[0].Name != "Good" {
		t.Fatalf("indexers = %+v", plan.Indexers)
	}
	joined := strings.Join(plan.Warnings, "\n")
	if !strings.Contains(joined, "has no id") || !strings.Contains(joined, "has no URL") {
		t.Fatalf("warnings = %v", plan.Warnings)
	}
}

func TestFromJackettConfigTraversalSafety(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "ServerConfig.json"), `{"Port": 9117, "APIKey": "`+jackettAPIKey+`"}`)
	writeTestFile(t, filepath.Join(dir, "Indexers", "good.json"),
		`{"id": "good", "name": "Good", "site_link": "https://good.example.invalid/"}`)
	writeTestFile(t, filepath.Join(dir, "Indexers", "unsafe.json"),
		`{"id": "../../etc/passwd", "name": "Unsafe", "site_link": "https://unsafe.example.invalid/"}`)

	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "evil.json")
	writeTestFile(t, outsideFile,
		`{"id": "evil", "name": "Evil", "site_link": "https://evil.example.invalid/"}`)
	symlinked := true
	if err := os.Symlink(outsideFile, filepath.Join(dir, "Indexers", "link.json")); err != nil {
		symlinked = false
		t.Logf("symlink unavailable, skipping that assertion: %v", err)
	}

	plan, err := FromJackettConfig(context.Background(), dir)
	if err != nil {
		t.Fatalf("FromJackettConfig: %v", err)
	}
	if len(plan.Indexers) != 1 || plan.Indexers[0].Name != "Good" {
		t.Fatalf("indexers = %+v, want only Good", plan.Indexers)
	}
	joined := strings.Join(plan.Warnings, "\n")
	if !strings.Contains(joined, "unsafe id") {
		t.Fatalf("warnings do not mention the unsafe id: %v", plan.Warnings)
	}
	if symlinked && !strings.Contains(joined, "symbolic link") {
		t.Fatalf("warnings do not mention the symlink: %v", plan.Warnings)
	}
}

func TestJackettHost(t *testing.T) {
	cases := map[string]string{
		"":            "127.0.0.1",
		"*":           "127.0.0.1",
		"0.0.0.0":     "127.0.0.1",
		"::":          "127.0.0.1",
		"localhost":   "127.0.0.1",
		"127.0.0.1":   "127.0.0.1",
		"192.168.1.5": "192.168.1.5",
		"not-an-ip":   "127.0.0.1",
	}
	for bind, want := range cases {
		if got := jackettHost(bind); got != want {
			t.Fatalf("jackettHost(%q) = %q, want %q", bind, got, want)
		}
	}
}

func TestJackettProxyCredentialsFromURL(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "ServerConfig.json"), `{
		"Port": 9117,
		"APIKey": "`+jackettAPIKey+`",
		"ProxyType": "Http",
		"ProxyUrl": "http://embedded-user:embedded-pass-SECRET@proxy.example.invalid",
		"ProxyPort": 8080
	}`)

	plan, err := FromJackettConfig(context.Background(), dir)
	if err != nil {
		t.Fatalf("FromJackettConfig: %v", err)
	}
	if len(plan.Proxies) != 1 {
		t.Fatalf("got %d proxies, want 1", len(plan.Proxies))
	}
	proxy := plan.Proxies[0]
	if proxy.URL != "http://proxy.example.invalid:8080" {
		t.Fatalf("proxy URL = %q", proxy.URL)
	}
	if proxy.Username != "embedded-user" || proxy.Password != "embedded-pass-SECRET" {
		t.Fatalf("proxy credentials were not extracted: %+v", proxy)
	}
	requireSecretsAbsent(t, strings.Join(plan.Warnings, "\n"), jackettAPIKey, "embedded-pass-SECRET")
}

func TestNormalizeProxyURL(t *testing.T) {
	cases := []struct {
		host, port, kind, want string
	}{
		{"", "8080", "http", ""},
		{"proxy.local", "1080", "socks5", "socks5://proxy.local:1080"},
		{"proxy.local", "1080", "socks4", "socks4://proxy.local:1080"},
		{"proxy.local", "8080", "http", "http://proxy.local:8080"},
		{"http://proxy.local", "", "http", "http://proxy.local"},
		{"http://proxy.local", "8080", "http", "http://proxy.local:8080"},
		{"2001:db8::1", "8080", "http", "http://[2001:db8::1]:8080"},
	}
	for _, test := range cases {
		if got := normalizeProxyURL(test.host, test.port, test.kind); got != test.want {
			t.Fatalf("normalizeProxyURL(%q, %q, %q) = %q, want %q",
				test.host, test.port, test.kind, got, test.want)
		}
	}
}

func TestSortIndexerRecords(t *testing.T) {
	records := []indexerRecord{
		{indexer: Indexer{Name: "Beta", BaseURL: "id-10"}, sourceID: "10"},
		{indexer: Indexer{Name: "alpha", BaseURL: "id-2"}, sourceID: "2"},
		{indexer: Indexer{Name: "Beta", BaseURL: "id-2"}, sourceID: "2"},
		{indexer: Indexer{Name: "Beta", BaseURL: "id-1"}, sourceID: "1"},
	}
	sorted := sortIndexerRecords(records)
	wantNames := []string{"alpha", "Beta", "Beta", "Beta"}
	wantURLs := []string{"id-2", "id-1", "id-2", "id-10"}
	for i := range wantNames {
		if sorted[i].Name != wantNames[i] {
			t.Fatalf("index %d name = %q, want %q", i, sorted[i].Name, wantNames[i])
		}
		if sorted[i].BaseURL != wantURLs[i] {
			t.Fatalf("index %d URL = %q, want %q (numeric id order)", i, sorted[i].BaseURL, wantURLs[i])
		}
	}
}

func writeTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestFromJackettArrayConfig(t *testing.T) {
	plan, err := FromJackettConfig(context.Background(), "testdata/jackett-array")
	if err != nil {
		t.Fatalf("FromJackettConfig: %v", err)
	}
	if len(plan.Indexers) != 1 {
		t.Fatalf("indexers = %d, want 1", len(plan.Indexers))
	}
	indexer := plan.Indexers[0]
	if indexer.Name != "array-site" {
		t.Errorf("name = %q", indexer.Name)
	}
	if indexer.BaseURL != "http://127.0.0.1:9117/api/v2.0/indexers/array-site/results/torznab/" {
		t.Errorf("base URL = %q", indexer.BaseURL)
	}
	if len(indexer.Categories) == 0 || indexer.Categories[0] != 2000 {
		t.Errorf("categories = %v", indexer.Categories)
	}
	for _, warning := range plan.Warnings {
		if strings.Contains(warning, "proxy") {
			t.Errorf("unexpected proxy warning: %q", warning)
		}
	}
}
