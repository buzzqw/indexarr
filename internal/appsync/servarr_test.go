package appsync

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

const testAppKey = "app-secret-key"

func TestServarrLifecycle(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var addPayload map[string]any
	var addKey string
	var putPayload map[string]any

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v3/system/status", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != testAppKey {
			http.Error(w, "missing key", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"appName":"Sonarr","version":"4.0.1","instanceName":"test"}`))
	})
	mux.HandleFunc("/api/v3/indexer/schema", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[
			{"id":0,"implementation":"Torznab","configContract":"TorznabSettings","priority":25,
			 "downloadClientId":0,
			 "fields":[
				{"name":"baseUrl","value":""},
				{"name":"apiKey","value":""},
				{"name":"categories","value":[]},
				{"name":"apiPath","value":"/api"},
				{"name":"additionalParameters","value":""}
			 ]}
		]`))
	})
	mux.HandleFunc("/api/v3/indexer", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			_, _ = w.Write([]byte(`[
				{"id":3,"name":"indexarr · Alpha","implementation":"Torznab",
				 "configContract":"TorznabSettings","priority":25,"tags":[2],
				 "enableRss":true,"enableAutomaticSearch":true,"enableInteractiveSearch":false,
				 "fields":[
					{"name":"baseUrl","value":"https://alpha.example/api"},
					{"name":"apiKey","value":"alpha-key"},
					{"name":"categories","value":[5000,2000]},
					{"name":"extra","value":"keep"}
				 ]},
				{"id":4,"name":"Manual tracker","implementation":"Torznab",
				 "configContract":"TorznabSettings","priority":10,"tags":[],
				 "enableRss":false,"enableAutomaticSearch":false,"enableInteractiveSearch":false,
				 "fields":[{"name":"baseUrl","value":"https://manual.example/api"}]}
			]`))
		case http.MethodPost:
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Errorf("decode add payload: %v", err)
				http.Error(w, "bad payload", http.StatusBadRequest)
				return
			}
			payload["id"] = float64(9)
			mu.Lock()
			addPayload = payload
			addKey = r.Header.Get("X-Api-Key")
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(payload)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/api/v3/indexer/7", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			_, _ = w.Write([]byte(`{
				"id":7,"name":"indexarr · Tracker","implementation":"Torznab",
				"configContract":"TorznabSettings","priority":10,"tags":[1],
				"enableRss":true,"enableAutomaticSearch":false,"enableInteractiveSearch":true,
				"protocol":"torrent","unknownTop":{"a":1},
				"fields":[
					{"name":"baseUrl","value":"https://old.example/api"},
					{"name":"apiKey","value":"old-key"},
					{"name":"categories","value":[1000]},
					{"name":"customParam","value":"custom-value"},
					{"name":"apiPath","value":"/api"}
				]
			}`))
		case http.MethodPut:
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Errorf("decode update payload: %v", err)
				http.Error(w, "bad payload", http.StatusBadRequest)
				return
			}
			mu.Lock()
			putPayload = payload
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(payload)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/api/v3/indexer/42", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	client, err := New(Config{
		Kind: KindSonarr, Name: "Sonarr", URL: server.URL,
		APIKey: testAppKey, SyncLevel: "full",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()

	version, err := client.Test(ctx)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	if version != "Sonarr 4.0.1" {
		t.Fatalf("Test = %q, want Sonarr 4.0.1", version)
	}

	remote, err := client.ListIndexers(ctx)
	if err != nil {
		t.Fatalf("ListIndexers: %v", err)
	}
	if len(remote) != 2 {
		t.Fatalf("ListIndexers returned %d entries, want 2", len(remote))
	}
	alpha := remote[0]
	if alpha.ID != 3 || alpha.Name != "indexarr · Alpha" {
		t.Fatalf("unexpected first remote indexer: %+v", alpha)
	}
	if got := asString(alpha.Fields["baseUrl"]); got != "https://alpha.example/api" {
		t.Errorf("baseUrl = %q", got)
	}
	if got := asString(alpha.Fields["apiKey"]); got != "alpha-key" {
		t.Errorf("apiKey = %q", got)
	}
	if got := asIntSlice(alpha.Fields["categories"]); len(got) != 2 || got[0] != 5000 || got[1] != 2000 {
		t.Errorf("categories = %v", got)
	}
	wantHash := SpecHash(IndexerSpec{
		Name: "indexarr · Alpha", Implementation: "Torznab", ConfigContract: "TorznabSettings",
		BaseURL: "https://alpha.example/api", APIKey: "alpha-key",
		Categories: []int{5000, 2000}, Priority: 25, Tags: []int{2},
		EnableRSS: true, EnableAutomaticSearch: true, EnableInteractiveSearch: false,
	})
	if alpha.RawHash != wantHash {
		t.Errorf("RawHash = %s, want %s", alpha.RawHash, wantHash)
	}

	addSpec := IndexerSpec{
		Name: "indexarr · Added", Implementation: "Torznab", ConfigContract: "TorznabSettings",
		BaseURL: "https://added.example/api/", APIKey: "added-key",
		Categories: []int{5000}, Priority: 25, Tags: []int{1},
		EnableRSS: true, EnableAutomaticSearch: true, EnableInteractiveSearch: true,
		Protocol:         "torrent",
		AdditionalFields: map[string]any{"additionalParameters": "x=1"},
	}
	added, err := client.AddIndexer(ctx, addSpec)
	if err != nil {
		t.Fatalf("AddIndexer: %v", err)
	}
	if added.ID != 9 {
		t.Errorf("added ID = %d, want 9", added.ID)
	}

	mu.Lock()
	if addKey != testAppKey {
		t.Errorf("X-Api-Key = %q, want %q", addKey, testAppKey)
	}
	fields := fieldMap(addPayload["fields"])
	if got := asString(fields["baseUrl"]); got != "https://added.example/api" {
		t.Errorf("add baseUrl = %q", got)
	}
	if got := asString(fields["apiKey"]); got != "added-key" {
		t.Errorf("add apiKey = %q", got)
	}
	if got := asString(fields["additionalParameters"]); got != "x=1" {
		t.Errorf("add additionalParameters = %q", got)
	}
	if got := asString(fields["apiPath"]); got != "/api" {
		t.Errorf("schema default apiPath was not preserved: %q", got)
	}
	if _, ok := addPayload["downloadClientId"]; !ok {
		t.Error("schema default downloadClientId was dropped")
	}
	if got := asString(addPayload["implementation"]); got != "Torznab" {
		t.Errorf("implementation = %q", got)
	}
	if got := asString(addPayload["configContract"]); got != "TorznabSettings" {
		t.Errorf("configContract = %q", got)
	}
	if got := asString(addPayload["protocol"]); got != "torrent" {
		t.Errorf("protocol = %q", got)
	}
	if got := asInt(addPayload["priority"]); got != 25 {
		t.Errorf("priority = %d", got)
	}
	mu.Unlock()

	updateSpec := IndexerSpec{
		Name: "indexarr · Tracker", Implementation: "Torznab", ConfigContract: "TorznabSettings",
		BaseURL: "https://new.example/api", APIKey: "new-key",
		Categories: []int{5000}, Priority: 25, Tags: []int{1, 2},
		EnableRSS: true, EnableAutomaticSearch: true, EnableInteractiveSearch: true,
	}
	updated, err := client.UpdateIndexer(ctx, 7, updateSpec)
	if err != nil {
		t.Fatalf("UpdateIndexer: %v", err)
	}
	if updated.ID != 7 {
		t.Errorf("updated ID = %d, want 7", updated.ID)
	}

	mu.Lock()
	if putPayload == nil {
		t.Fatal("PUT payload was not captured")
	}
	if got := asInt(putPayload["id"]); got != 7 {
		t.Errorf("PUT id = %d, want 7", got)
	}
	unknown, ok := putPayload["unknownTop"].(map[string]any)
	if !ok || asInt(unknown["a"]) != 1 {
		t.Errorf("unknown top-level field was not preserved: %#v", putPayload["unknownTop"])
	}
	putFields := fieldMap(putPayload["fields"])
	if got := asString(putFields["baseUrl"]); got != "https://new.example/api" {
		t.Errorf("updated baseUrl = %q", got)
	}
	if got := asString(putFields["apiKey"]); got != "new-key" {
		t.Errorf("updated apiKey = %q", got)
	}
	if got := asString(putFields["customParam"]); got != "custom-value" {
		t.Errorf("unknown field customParam was not preserved: %q", got)
	}
	if got := asString(putFields["apiPath"]); got != "/api" {
		t.Errorf("field apiPath was not preserved: %q", got)
	}
	if got := asIntSlice(putFields["categories"]); len(got) != 1 || got[0] != 5000 {
		t.Errorf("updated categories = %v", got)
	}
	mu.Unlock()

	if err := client.DeleteIndexer(ctx, 42); err != nil {
		t.Fatalf("DeleteIndexer(404) = %v, want nil", err)
	}
	if err := client.DeleteIndexer(ctx, 42); err != nil {
		t.Fatalf("DeleteIndexer(404) again = %v, want nil", err)
	}
}

func TestServarrV3FallsBackToV1(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	paths := make([]string, 0, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()

		switch r.URL.Path {
		case "/api/v1/system/status":
			_, _ = w.Write([]byte(`{"appName":"Whisparr","version":"3.1.0"}`))
		case "/api/v1/indexer":
			_, _ = w.Write([]byte(`[]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := New(Config{Kind: KindWhisparr, URL: server.URL, APIKey: "key"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	version, err := client.Test(context.Background())
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	if version != "Whisparr 3.1.0" {
		t.Fatalf("Test = %q", version)
	}
	remote, err := client.ListIndexers(context.Background())
	if err != nil {
		t.Fatalf("ListIndexers: %v", err)
	}
	if len(remote) != 0 {
		t.Fatalf("ListIndexers returned %d entries, want 0", len(remote))
	}

	mu.Lock()
	defer mu.Unlock()
	var sawV1Status, sawV1List bool
	for _, path := range paths {
		switch path {
		case "/api/v1/system/status":
			sawV1Status = true
		case "/api/v1/indexer":
			sawV1List = true
		}
	}
	if !sawV1Status || !sawV1List {
		t.Fatalf("v1 fallback was not used, paths = %v", paths)
	}
}

func TestServarrErrorsRedactKeyAndTruncate(t *testing.T) {
	t.Parallel()

	const key = "echoed-secret-key"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := "auth failed for " + key + " " + strings.Repeat("noise ", 100) + key
		http.Error(w, body, http.StatusUnauthorized)
	}))
	defer server.Close()

	client, err := New(Config{Kind: KindRadarr, URL: server.URL, APIKey: key})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = client.Test(context.Background())
	if err == nil {
		t.Fatal("Test returned no error")
	}
	if strings.Contains(err.Error(), key) {
		t.Fatalf("error leaked the API key: %v", err)
	}
	if !strings.Contains(err.Error(), "401") {
		t.Fatalf("error does not mention HTTP 401: %v", err)
	}
	if len(err.Error()) > 400 {
		t.Fatalf("error is not truncated: length %d: %v", len(err.Error()), err)
	}
}

func TestServarrCrossHostRedirectStripsKey(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var targetKey, sourceKey string

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		targetKey = r.Header.Get("X-Api-Key")
		mu.Unlock()
		_, _ = w.Write([]byte(`{"appName":"Sonarr","version":"4.0.1"}`))
	}))
	defer target.Close()

	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		sourceKey = r.Header.Get("X-Api-Key")
		mu.Unlock()
		http.Redirect(w, r, target.URL+"/api/v3/system/status", http.StatusFound)
	}))
	defer source.Close()

	client, err := New(Config{Kind: KindSonarr, URL: source.URL, APIKey: testAppKey})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := client.Test(context.Background()); err != nil {
		t.Fatalf("Test: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if sourceKey != testAppKey {
		t.Errorf("source did not receive the key: %q", sourceKey)
	}
	if targetKey != "" {
		t.Fatalf("cross-host redirect leaked X-Api-Key %q", targetKey)
	}
}

// TestServarrResolveTags covers the label -> tag ID mapping that makes the
// indexer tags survive the sync into Sonarr/Radarr.
func TestServarrResolveTags(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	created := make([]string, 0)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v3/system/status", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"appName":"Sonarr","version":"4.0.1"}`))
	})
	mux.HandleFunc("/api/v3/tag", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			_, _ = w.Write([]byte(`[{"id":4,"label":"TV"},{"id":7,"label":"anime"}]`))
		case http.MethodPost:
			var payload struct {
				Label string `json:"label"`
			}
			_ = json.NewDecoder(r.Body).Decode(&payload)
			mu.Lock()
			created = append(created, payload.Label)
			mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 9, "label": payload.Label})
		}
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	client, err := New(Config{Kind: KindSonarr, URL: server.URL, APIKey: "key"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ids, err := client.(TagResolver).ResolveTags(context.Background(),
		[]string{"tv", "anime", "TV", "new-label"})
	if err != nil {
		t.Fatalf("ResolveTags: %v", err)
	}
	if len(ids) != 4 || ids[0] != 4 || ids[1] != 7 || ids[2] != 4 || ids[3] != 9 {
		t.Errorf("tag ids = %v, want [4 7 4 9]", ids)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(created) != 1 || created[0] != "new-label" {
		t.Errorf("created tags = %v, want only new-label", created)
	}
}

// TestServarrForceSave proves that add and update skip the blocking indexer
// test: a site with no current results must not abort the whole sync.
func TestServarrForceSave(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var addForce, putForce bool
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v3/system/status", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"appName":"Radarr","version":"5.0.1"}`))
	})
	mux.HandleFunc("/api/v3/indexer/schema", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"implementation":"Torznab","configContract":"TorznabSettings","fields":[]}]`))
	})
	mux.HandleFunc("/api/v3/indexer", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		addForce = r.URL.Query().Get("forceSave") == "true"
		mu.Unlock()
		_, _ = w.Write([]byte(`{"id":5,"name":"added"}`))
	})
	mux.HandleFunc("/api/v3/indexer/5", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"id":5,"name":"added","fields":[]}`))
			return
		}
		mu.Lock()
		putForce = r.URL.Query().Get("forceSave") == "true"
		mu.Unlock()
		_, _ = w.Write([]byte(`{"id":5,"name":"updated","fields":[]}`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	client, err := New(Config{Kind: KindRadarr, URL: server.URL, APIKey: "key"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	spec := IndexerSpec{Name: "indexarr · Test", Implementation: "Torznab",
		ConfigContract: "TorznabSettings", BaseURL: "https://indexarr.example/torznab/test/api",
		Categories: []int{2000}, Protocol: "torrent"}
	if _, err := client.AddIndexer(context.Background(), spec); err != nil {
		t.Fatalf("AddIndexer: %v", err)
	}
	if _, err := client.UpdateIndexer(context.Background(), 5, spec); err != nil {
		t.Fatalf("UpdateIndexer: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !addForce || !putForce {
		t.Errorf("forceSave add=%v update=%v, want both true", addForce, putForce)
	}
}

func TestServarrAddNewznab(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var payload map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v3/indexer/schema", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[
			{"implementation":"Torznab","configContract":"TorznabSettings","fields":[]},
			{"implementation":"Newznab","configContract":"NewznabSettings",
			 "fields":[{"name":"apiPath","value":"/api"}]}
		]`))
	})
	mux.HandleFunc("/api/v3/indexer", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		var captured map[string]any
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Errorf("decode: %v", err)
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		captured["id"] = float64(11)
		mu.Lock()
		payload = captured
		mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(captured)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	client, err := New(Config{Kind: KindReadarr, URL: server.URL, APIKey: "key"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	spec := IndexerSpec{
		Name: "indexarr · Usenet", Implementation: "Newznab", ConfigContract: "NewznabSettings",
		BaseURL: "https://usenet.example/api", APIKey: "nzb-key",
		Categories: []int{7000}, Priority: 25, Protocol: "usenet",
		EnableRSS: true, EnableAutomaticSearch: true, EnableInteractiveSearch: true,
	}
	if _, err := client.AddIndexer(context.Background(), spec); err != nil {
		t.Fatalf("AddIndexer: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if got := asString(payload["implementation"]); got != "Newznab" {
		t.Errorf("implementation = %q", got)
	}
	if got := asString(payload["configContract"]); got != "NewznabSettings" {
		t.Errorf("configContract = %q", got)
	}
	if got := asString(payload["protocol"]); got != "usenet" {
		t.Errorf("protocol = %q", got)
	}
	f := fieldMap(payload["fields"])
	if got := asString(f["baseUrl"]); got != "https://usenet.example/api" {
		t.Errorf("baseUrl = %q", got)
	}
	if got := asString(f["apiPath"]); got != "/api" {
		t.Errorf("apiPath from schema = %q", got)
	}
}

func TestServarrAddWithoutSchemaUsesMinimalPayload(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var payload map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v3/indexer/schema", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "schema unavailable", http.StatusInternalServerError)
	})
	mux.HandleFunc("/api/v3/indexer", func(w http.ResponseWriter, r *http.Request) {
		var captured map[string]any
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Errorf("decode: %v", err)
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		captured["id"] = float64(12)
		mu.Lock()
		payload = captured
		mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(captured)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	client, err := New(Config{Kind: KindLidarr, URL: server.URL, APIKey: "key"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	spec := IndexerSpec{
		Name: "indexarr · Minimal", Implementation: "Torznab", ConfigContract: "TorznabSettings",
		BaseURL: "https://minimal.example/api", APIKey: "minimal-key",
		Categories: []int{3000}, Protocol: "torrent",
		EnableRSS: true, EnableAutomaticSearch: true, EnableInteractiveSearch: true,
	}
	if _, err := client.AddIndexer(context.Background(), spec); err != nil {
		t.Fatalf("AddIndexer: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	f := fieldMap(payload["fields"])
	for _, name := range []string{"baseUrl", "apiKey", "categories"} {
		if _, ok := f[name]; !ok {
			t.Errorf("minimal payload is missing field %q", name)
		}
	}
	if got := asString(payload["name"]); got != spec.Name {
		t.Errorf("name = %q", got)
	}
	if got := asString(payload["implementation"]); got != "Torznab" {
		t.Errorf("implementation = %q", got)
	}
	if got := asInt(payload["priority"]); got != 25 {
		t.Errorf("priority = %d", got)
	}
}
