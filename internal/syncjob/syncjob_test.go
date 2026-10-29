package syncjob

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/buzzqw/indexarr/internal/appsync"
	"github.com/buzzqw/indexarr/internal/store"
	"github.com/buzzqw/indexarr/internal/vault"
)

const (
	testIndexarrKey = "indexarr-primary-key"
	testAppKey      = "servarr-application-key"
	testBaseURL     = "https://indexarr.example/"
)

// newSyncStore opens a temporary store. When withAPIKey is true the primary
// indexarr Torznab key is configured.
func newSyncStore(t *testing.T, withAPIKey bool) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "indexarr.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if withAPIKey {
		if err := st.SetSetting(context.Background(), store.KeyAPIKey, testIndexarrKey); err != nil {
			t.Fatalf("SetSetting: %v", err)
		}
	}
	return st
}

func newSyncVault(t *testing.T) *vault.Vault {
	t.Helper()
	v, err := vault.New(bytes.Repeat([]byte{0x17}, 32))
	if err != nil {
		t.Fatalf("vault.New: %v", err)
	}
	return v
}

func newSyncService(t *testing.T, st *store.Store, v *vault.Vault, baseURL string) *Service {
	t.Helper()
	svc, err := New(Config{
		Store:   st,
		Vault:   v,
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		BaseURL: baseURL,
	})
	if err != nil {
		t.Fatalf("syncjob.New: %v", err)
	}
	return svc
}

func testDefinition(id, protocol string, categories ...int) string {
	var b strings.Builder
	fmt.Fprintf(&b, `schema: 1
connector: definition
meta:
  id: %s
  name: %s
  type: public
  protocol: %s
  urls:
    - https://tracker.example/
categories:
`, id, id, protocol)
	for _, category := range categories {
		fmt.Fprintf(&b, "  - site: \"%d\"\n    standard: %d\n", category, category)
	}
	return b.String()
}

func createTestDefinition(t *testing.T, st *store.Store, id, protocol string, categories ...int) {
	t.Helper()
	if err := st.UpsertDefinition(context.Background(), store.Definition{
		ID:            id,
		SchemaVersion: 1,
		Family:        "definition",
		State:         "verificata",
		Origin:        "local",
		Author:        "test",
		Body:          testDefinition(id, protocol, categories...),
		Hash:          "hash-" + id,
	}); err != nil {
		t.Fatalf("UpsertDefinition(%s): %v", id, err)
	}
}

func createTestIndexer(t *testing.T, st *store.Store, definitionID, protocol, slug, name string, enabled bool, priority int) store.Indexer {
	t.Helper()
	id, err := st.CreateIndexer(context.Background(), store.Indexer{
		DefinitionID: definitionID,
		Protocol:     protocol,
		Slug:         slug,
		Name:         name,
		Enabled:      enabled,
		Priority:     priority,
	})
	if err != nil {
		t.Fatalf("CreateIndexer(%s): %v", slug, err)
	}
	in, err := st.GetIndexer(context.Background(), id)
	if err != nil {
		t.Fatalf("GetIndexer(%d): %v", id, err)
	}
	return in
}

func createTestApplication(t *testing.T, st *store.Store, v *vault.Vault, kind, name, appURL, apiKey, level string) int64 {
	t.Helper()
	sealed, err := v.SealString(apiKey)
	if err != nil {
		t.Fatalf("SealString: %v", err)
	}
	id, err := st.CreateApplication(context.Background(), store.Application{
		Kind:                    kind,
		Name:                    name,
		URL:                     appURL,
		APIKeyEnc:               []byte(sealed),
		SyncLevel:               level,
		EnableRSS:               true,
		EnableAutomaticSearch:   true,
		EnableInteractiveSearch: true,
	})
	if err != nil {
		t.Fatalf("CreateApplication(%s): %v", name, err)
	}
	return id
}

// fakeServarr is a minimal Servarr v3 API implementing the subset appsync
// uses. It keeps indexers in memory and can be made to fail indexer requests.
type fakeServarr struct {
	t      *testing.T
	server *httptest.Server
	apiKey string

	mu        sync.Mutex
	nextID    int
	items     map[int]map[string]any
	tags      map[string]int
	nextTag   int
	listCalls int
	fail      bool
	failBody  string
}

func newFakeServarr(t *testing.T, apiKey string) *fakeServarr {
	t.Helper()
	f := &fakeServarr{
		t:       t,
		apiKey:  apiKey,
		nextID:  1,
		items:   map[int]map[string]any{},
		tags:    map[string]int{"tv": 1},
		nextTag: 2,
	}
	f.server = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeServarr) handle(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-Api-Key") != f.apiKey {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	switch {
	case r.URL.Path == "/api/v3/system/status":
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"appName":"Sonarr","version":"4.0.1"}`))
	case r.URL.Path == "/api/v3/tag":
		f.handleTags(w, r)
	case r.URL.Path == "/api/v3/indexer/schema":
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
			{"id":0,"implementation":"Torznab","configContract":"TorznabSettings",
			 "fields":[{"name":"baseUrl","value":""},{"name":"apiKey","value":""},
			 {"name":"categories","value":[]}]},
			{"id":0,"implementation":"Newznab","configContract":"NewznabSettings",
			 "fields":[{"name":"baseUrl","value":""},{"name":"apiKey","value":""}]}
		]`))
	case r.URL.Path == "/api/v3/indexer":
		f.handleCollection(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/v3/indexer/"):
		f.handleItem(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeServarr) handleTags(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		f.mu.Lock()
		out := make([]map[string]any, 0, len(f.tags))
		for label, id := range f.tags {
			out = append(out, map[string]any{"id": id, "label": label})
		}
		f.mu.Unlock()
		sort.Slice(out, func(i, j int) bool { return out[i]["id"].(int) < out[j]["id"].(int) })
		writeFakeJSON(w, out)
	case http.MethodPost:
		var payload struct {
			Label string `json:"label"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, "bad payload", http.StatusBadRequest)
			return
		}
		label := strings.ToLower(strings.TrimSpace(payload.Label))
		f.mu.Lock()
		id, ok := f.tags[label]
		if !ok {
			id = f.nextTag
			f.nextTag++
			f.tags[label] = id
		}
		f.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		writeFakeJSON(w, map[string]any{"id": id, "label": payload.Label})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (f *fakeServarr) handleCollection(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	failing := f.fail
	message := f.failBody
	f.mu.Unlock()
	if failing {
		if message == "" {
			message = "indexer backend down"
		}
		http.Error(w, message, http.StatusInternalServerError)
		return
	}

	switch r.Method {
	case http.MethodGet:
		f.mu.Lock()
		f.listCalls++
		ids := make([]int, 0, len(f.items))
		for id := range f.items {
			ids = append(ids, id)
		}
		sort.Ints(ids)
		out := make([]map[string]any, 0, len(ids))
		for _, id := range ids {
			out = append(out, f.items[id])
		}
		f.mu.Unlock()
		writeFakeJSON(w, out)
	case http.MethodPost:
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, "bad payload", http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		id := f.nextID
		f.nextID++
		payload["id"] = id
		f.items[id] = payload
		f.mu.Unlock()
		writeFakeJSON(w, payload)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (f *fakeServarr) handleItem(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/api/v3/indexer/"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	f.mu.Lock()
	failing := f.fail
	message := f.failBody
	_, exists := f.items[id]
	f.mu.Unlock()
	if failing {
		if message == "" {
			message = "indexer backend down"
		}
		http.Error(w, message, http.StatusInternalServerError)
		return
	}
	if !exists {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	switch r.Method {
	case http.MethodGet:
		item := f.getByID(id)
		writeFakeJSON(w, item)
	case http.MethodPut:
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, "bad payload", http.StatusBadRequest)
			return
		}
		payload["id"] = id
		f.mu.Lock()
		f.items[id] = payload
		f.mu.Unlock()
		writeFakeJSON(w, payload)
	case http.MethodDelete:
		f.mu.Lock()
		delete(f.items, id)
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func writeFakeJSON(w http.ResponseWriter, payload any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(payload)
}

func (f *fakeServarr) getByID(id int) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return cloneMap(f.items[id])
}

func (f *fakeServarr) find(name string) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, item := range f.items {
		if item["name"] == name {
			return cloneMap(item)
		}
	}
	return nil
}

func (f *fakeServarr) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.items)
}

func (f *fakeServarr) listCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.listCalls
}

func (f *fakeServarr) setFail(fail bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail = fail
}

// addManual inserts an indexer as if the user created it by hand. It has no
// managed prefix and must never be touched by the reconciler.
func (f *fakeServarr) addManual(name, baseURL string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := f.nextID
	f.nextID++
	f.items[id] = map[string]any{
		"id":                      id,
		"name":                    name,
		"implementation":          "Torznab",
		"configContract":          "TorznabSettings",
		"priority":                10,
		"tags":                    []any{},
		"enableRss":               false,
		"enableAutomaticSearch":   false,
		"enableInteractiveSearch": false,
		"fields": []any{
			map[string]any{"name": "baseUrl", "value": baseURL},
			map[string]any{"name": "apiKey", "value": "manual-key"},
			map[string]any{"name": "categories", "value": []any{}},
		},
	}
}

func cloneMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	encoded, err := json.Marshal(in)
	if err != nil {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal(encoded, &out); err != nil {
		return nil
	}
	return out
}

func fakeField(item map[string]any, name string) any {
	fields, ok := item["fields"].([]any)
	if !ok {
		return nil
	}
	for _, raw := range fields {
		field, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if field["name"] == name {
			return field["value"]
		}
	}
	return nil
}

func fakeCategories(t *testing.T, item map[string]any) []int {
	t.Helper()
	raw, ok := fakeField(item, "categories").([]any)
	if !ok {
		return nil
	}
	out := make([]int, 0, len(raw))
	for _, value := range raw {
		number, ok := value.(float64)
		if !ok {
			t.Fatalf("category %v is not a number", value)
		}
		out = append(out, int(number))
	}
	return out
}

func fakeTags(t *testing.T, item map[string]any) []int {
	t.Helper()
	raw, ok := item["tags"].([]any)
	if !ok {
		return nil
	}
	out := make([]int, 0, len(raw))
	for _, value := range raw {
		number, ok := value.(float64)
		if !ok {
			t.Fatalf("tag %v is not a number", value)
		}
		out = append(out, int(number))
	}
	return out
}

func fakeRemoteID(t *testing.T, item map[string]any) int {
	t.Helper()
	number, ok := item["id"].(float64)
	if !ok {
		t.Fatalf("remote id %v is not a number", item["id"])
	}
	return int(number)
}

func TestSyncLifecycle(t *testing.T) {
	ctx := context.Background()
	st := newSyncStore(t, true)
	v := newSyncVault(t)
	fake := newFakeServarr(t, testAppKey)

	createTestDefinition(t, st, "torrent-def", "torrent", 5000, 2000, 2000, 1000)
	createTestDefinition(t, st, "usenet-def", "usenet", 7000, 2000)
	alpha := createTestIndexer(t, st, "torrent-def", "torrent", "alpha", "Alpha Tracker", true, 25)
	beta := createTestIndexer(t, st, "usenet-def", "usenet", "beta", "Beta Usenet", true, 10)
	// A disabled indexer is not desired and must be removed if it was synced
	// previously; it is created here only to prove it is absent from the app.
	createTestIndexer(t, st, "torrent-def", "torrent", "off", "Disabled Tracker", false, 25)

	appID := createTestApplication(t, st, v, "sonarr", "Sonarr", fake.server.URL, testAppKey, "full")
	svc := newSyncService(t, st, v, testBaseURL)

	result, err := svc.SyncApp(ctx, appID)
	if err != nil {
		t.Fatalf("SyncApp: %v", err)
	}
	if result.Name != "Sonarr" || result.Added != 2 || result.Updated != 0 ||
		result.Deleted != 0 || result.Skipped != 0 {
		t.Fatalf("first sync result = %+v", result)
	}
	if fake.count() != 2 {
		t.Fatalf("remote indexers = %d, want 2", fake.count())
	}

	alphaItem := fake.find(appsync.ManagedPrefix + "Alpha Tracker")
	if alphaItem == nil {
		t.Fatal("alpha was not added")
	}
	if got := alphaItem["implementation"]; got != "Torznab" {
		t.Errorf("alpha implementation = %v", got)
	}
	if got := alphaItem["configContract"]; got != "TorznabSettings" {
		t.Errorf("alpha configContract = %v", got)
	}
	if got := alphaItem["priority"]; got != float64(25) {
		t.Errorf("alpha priority = %v", got)
	}
	if got := fakeField(alphaItem, "baseUrl"); got != "https://indexarr.example/torznab/alpha" {
		t.Errorf("alpha baseUrl = %v", got)
	}
	if got := fakeField(alphaItem, "apiKey"); got != testIndexarrKey {
		t.Errorf("alpha apiKey = %v, want the indexarr primary key", got)
	}
	if got := fakeCategories(t, alphaItem); fmt.Sprint(got) != fmt.Sprint([]int{5000}) {
		t.Errorf("alpha categories = %v", got)
	}
	for _, flag := range []string{"enableRss", "enableAutomaticSearch", "enableInteractiveSearch"} {
		if got := alphaItem[flag]; got != true {
			t.Errorf("alpha %s = %v, want true", flag, got)
		}
	}
	if got := fakeField(alphaItem, "apiKey"); got == testAppKey {
		t.Error("the application API key leaked into the indexer payload")
	}

	betaItem := fake.find(appsync.ManagedPrefix + "Beta Usenet")
	if betaItem == nil {
		t.Fatal("beta was not added")
	}
	if got := betaItem["implementation"]; got != "Newznab" {
		t.Errorf("beta implementation = %v", got)
	}
	if got := betaItem["configContract"]; got != "NewznabSettings" {
		t.Errorf("beta configContract = %v", got)
	}
	if got := betaItem["priority"]; got != float64(10) {
		t.Errorf("beta priority = %v", got)
	}
	if fake.find(appsync.ManagedPrefix+"Disabled Tracker") != nil {
		t.Error("disabled indexer was synced")
	}

	links, err := st.ListAppIndexers(ctx, appID)
	if err != nil {
		t.Fatalf("ListAppIndexers: %v", err)
	}
	if len(links) != 2 {
		t.Fatalf("app_indexers rows = %d, want 2", len(links))
	}
	wantAlphaHash := appsync.SpecHash(appsync.IndexerSpec{
		Name: appsync.ManagedPrefix + "Alpha Tracker", Implementation: "Torznab",
		ConfigContract: "TorznabSettings",
		BaseURL:        "https://indexarr.example/torznab/alpha", APIKey: testIndexarrKey,
		Categories: []int{5000}, Priority: 25,
		EnableRSS: true, EnableAutomaticSearch: true, EnableInteractiveSearch: true,
		Protocol: "torrent",
	})
	linkByIndexer := map[int64]store.AppIndexer{}
	for _, link := range links {
		linkByIndexer[link.IndexerID] = link
	}
	if link, ok := linkByIndexer[alpha.ID]; !ok {
		t.Error("missing app_indexers row for alpha")
	} else {
		if want := strconv.Itoa(fakeRemoteID(t, alphaItem)); link.RemoteID != want {
			t.Errorf("alpha remote id = %q, want %q", link.RemoteID, want)
		}
		if link.SyncedHash != wantAlphaHash {
			t.Errorf("alpha synced hash = %q, want %q", link.SyncedHash, wantAlphaHash)
		}
	}
	if link, ok := linkByIndexer[beta.ID]; !ok {
		t.Error("missing app_indexers row for beta")
	} else if link.RemoteID != strconv.Itoa(fakeRemoteID(t, betaItem)) {
		t.Errorf("beta remote id = %q", link.RemoteID)
	}

	app, err := st.GetApplication(ctx, appID)
	if err != nil {
		t.Fatalf("GetApplication: %v", err)
	}
	if app.LastSyncAt.IsZero() {
		t.Error("last_sync_at was not recorded")
	}
	if app.LastError != "" {
		t.Errorf("last_error = %q, want empty", app.LastError)
	}

	// Second run: the desired state is already present, so nothing changes.
	creates := fake.count()
	result, err = svc.SyncApp(ctx, appID)
	if err != nil {
		t.Fatalf("second SyncApp: %v", err)
	}
	if result.Added != 0 || result.Updated != 0 || result.Deleted != 0 {
		t.Fatalf("second sync was not a no-op: %+v", result)
	}
	if fake.count() != creates {
		t.Fatalf("second sync changed the remote indexers: %d", fake.count())
	}

	// A manually created indexer is left alone.
	fake.addManual("Manual tracker", "https://manual.example/api")
	result, err = svc.SyncApp(ctx, appID)
	if err != nil {
		t.Fatalf("sync with manual indexer: %v", err)
	}
	if result.Added != 0 || result.Updated != 0 || result.Deleted != 0 {
		t.Fatalf("sync with manual indexer changed state: %+v", result)
	}
	if fake.find("Manual tracker") == nil {
		t.Fatal("unmanaged indexer was deleted")
	}

	// A changed priority is an update in full mode.
	alpha.Priority = 40
	if err := st.UpdateIndexer(ctx, alpha); err != nil {
		t.Fatalf("UpdateIndexer: %v", err)
	}
	result, err = svc.SyncApp(ctx, appID)
	if err != nil {
		t.Fatalf("sync after priority change: %v", err)
	}
	if result.Updated != 1 || result.Added != 0 || result.Deleted != 0 {
		t.Fatalf("priority change result = %+v", result)
	}
	alphaItem = fake.find(appsync.ManagedPrefix + "Alpha Tracker")
	if got := alphaItem["priority"]; got != float64(40) {
		t.Errorf("alpha priority after update = %v", got)
	}
	links, err = st.ListAppIndexers(ctx, appID)
	if err != nil {
		t.Fatalf("ListAppIndexers after update: %v", err)
	}
	for _, link := range links {
		if link.IndexerID != alpha.ID {
			continue
		}
		want := appsync.SpecHash(appsync.IndexerSpec{
			Name: appsync.ManagedPrefix + "Alpha Tracker", Implementation: "Torznab",
			ConfigContract: "TorznabSettings",
			BaseURL:        "https://indexarr.example/torznab/alpha", APIKey: testIndexarrKey,
			Categories: []int{5000}, Priority: 40,
			EnableRSS: true, EnableAutomaticSearch: true, EnableInteractiveSearch: true,
			Protocol: "torrent",
		})
		if link.SyncedHash != want {
			t.Errorf("alpha synced hash after update = %q, want %q", link.SyncedHash, want)
		}
	}

	// Disabling an indexer removes only that managed indexer and its link.
	alpha.Enabled = false
	if err := st.UpdateIndexer(ctx, alpha); err != nil {
		t.Fatalf("UpdateIndexer disable: %v", err)
	}
	result, err = svc.SyncApp(ctx, appID)
	if err != nil {
		t.Fatalf("sync after disable: %v", err)
	}
	if result.Deleted != 1 || result.Added != 0 || result.Updated != 0 {
		t.Fatalf("disable result = %+v", result)
	}
	if fake.find(appsync.ManagedPrefix+"Alpha Tracker") != nil {
		t.Error("disabled indexer still exists in the application")
	}
	if fake.find("Manual tracker") == nil {
		t.Error("unmanaged indexer was deleted")
	}
	links, err = st.ListAppIndexers(ctx, appID)
	if err != nil {
		t.Fatalf("ListAppIndexers after disable: %v", err)
	}
	if len(links) != 1 || links[0].IndexerID != beta.ID {
		t.Fatalf("app_indexers after disable = %+v", links)
	}
}

// TestSyncUsenetUsesNewznabAndExcludesEd2k covers every Servarr application:
// a Usenet indexer is pushed as Newznab/NewznabSettings with protocol usenet,
// while an eD2k indexer is never sent to an app that would hand its link to a
// torrent client.
func TestSyncUsenetUsesNewznabAndExcludesEd2k(t *testing.T) {
	for _, kind := range []string{"sonarr", "radarr", "lidarr", "readarr", "whisparr"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			st := newSyncStore(t, true)
			v := newSyncVault(t)
			fake := newFakeServarr(t, testAppKey)

			// The same definition advertises every category root, so each app
			// can filter it to the categories it consumes.
			createTestDefinition(t, st, "usenet-def", "usenet", 2000, 3000, 5000, 6000, 7000)
			createTestDefinition(t, st, "ed2k-def", "ed2k", 5000)
			usenet := createTestIndexer(t, st, "usenet-def", "usenet", "news", "News Source", true, 10)
			createTestIndexer(t, st, "ed2k-def", "ed2k", "emule", "eMule Source", true, 20)

			appID := createTestApplication(t, st, v, kind, kind, fake.server.URL, testAppKey, "full")
			svc := newSyncService(t, st, v, testBaseURL)

			result, err := svc.SyncApp(ctx, appID)
			if err != nil {
				t.Fatalf("SyncApp: %v", err)
			}
			if result.Added != 1 || result.Deleted != 0 || result.Updated != 0 {
				t.Fatalf("sync result = %+v, want one added indexer", result)
			}
			if fake.count() != 1 {
				t.Fatalf("remote indexers = %d, want 1", fake.count())
			}

			item := fake.find(appsync.ManagedPrefix + "News Source")
			if item == nil {
				t.Fatal("the usenet indexer was not pushed")
			}
			if got := item["implementation"]; got != "Newznab" {
				t.Errorf("implementation = %v, want Newznab", got)
			}
			if got := item["configContract"]; got != "NewznabSettings" {
				t.Errorf("configContract = %v, want NewznabSettings", got)
			}
			if got, _ := item["protocol"].(string); got != "usenet" {
				t.Errorf("protocol = %q, want usenet", got)
			}
			if got := fakeField(item, "baseUrl"); got != "https://indexarr.example/torznab/"+usenet.Slug {
				t.Errorf("baseUrl = %v", got)
			}
			if fake.find(appsync.ManagedPrefix+"eMule Source") != nil {
				t.Error("the eD2k indexer was pushed to the application")
			}
		})
	}
}

// TestSyncCarriesTags maps indexarr indexer labels to the application's tag
// IDs, creates the missing ones once and follows later label changes.
func TestSyncCarriesTags(t *testing.T) {
	ctx := context.Background()
	st := newSyncStore(t, true)
	v := newSyncVault(t)
	fake := newFakeServarr(t, testAppKey)

	createTestDefinition(t, st, "tv-def", "torrent", 5000, 2000)
	alpha := createTestIndexer(t, st, "tv-def", "torrent", "alpha", "Alpha Tracker", true, 25)
	alpha.Tags = "TV, Anime"
	if err := st.UpdateIndexer(ctx, alpha); err != nil {
		t.Fatalf("UpdateIndexer tags: %v", err)
	}
	appID := createTestApplication(t, st, v, "sonarr", "Sonarr", fake.server.URL, testAppKey, "full")
	svc := newSyncService(t, st, v, testBaseURL)

	if result, err := svc.SyncApp(ctx, appID); err != nil || result.Added != 1 {
		t.Fatalf("first sync = %+v, %v", result, err)
	}
	item := fake.find(appsync.ManagedPrefix + "Alpha Tracker")
	if item == nil {
		t.Fatal("alpha was not added")
	}
	if got := fakeTags(t, item); fmt.Sprint(got) != "[1 2]" {
		t.Errorf("tags = %v, want [1 2] (tv existing, anime created)", got)
	}
	if got := fakeCategories(t, item); fmt.Sprint(got) != "[5000]" {
		t.Errorf("Sonarr categories = %v, want only TV", got)
	}

	// Two consecutive syncs must not duplicate indexers or tags.
	if result, err := svc.SyncApp(ctx, appID); err != nil || result.Added != 0 || result.Updated != 0 {
		t.Fatalf("second sync = %+v, %v", result, err)
	}
	if fake.count() != 1 {
		t.Errorf("indexers after two syncs = %d, want 1", fake.count())
	}

	// Dropping a label updates the indexer's tag list.
	alpha.Tags = "TV"
	if err := st.UpdateIndexer(ctx, alpha); err != nil {
		t.Fatalf("UpdateIndexer tags removal: %v", err)
	}
	if result, err := svc.SyncApp(ctx, appID); err != nil || result.Updated != 1 {
		t.Fatalf("tag update = %+v, %v", result, err)
	}
	item = fake.find(appsync.ManagedPrefix + "Alpha Tracker")
	if got := fakeTags(t, item); fmt.Sprint(got) != "[1]" {
		t.Errorf("tags after removal = %v, want [1]", got)
	}
}

func TestSyncLevels(t *testing.T) {
	ctx := context.Background()
	st := newSyncStore(t, true)
	v := newSyncVault(t)
	fake := newFakeServarr(t, testAppKey)

	createTestDefinition(t, st, "torrent-def", "torrent", 5000)
	indexer := createTestIndexer(t, st, "torrent-def", "torrent", "alpha", "Alpha", true, 25)
	appID := createTestApplication(t, st, v, "sonarr", "Sonarr", fake.server.URL, testAppKey, "add")
	svc := newSyncService(t, st, v, testBaseURL)

	// "add" maps to add-remove: additions and deletions run, updates do not.
	result, err := svc.SyncApp(ctx, appID)
	if err != nil {
		t.Fatalf("SyncApp: %v", err)
	}
	if result.Added != 1 {
		t.Fatalf("add level first sync = %+v", result)
	}

	indexer.Priority = 40
	if err := st.UpdateIndexer(ctx, indexer); err != nil {
		t.Fatalf("UpdateIndexer: %v", err)
	}
	result, err = svc.SyncApp(ctx, appID)
	if err != nil {
		t.Fatalf("SyncApp after priority change: %v", err)
	}
	if result.Updated != 0 || result.Skipped != 1 {
		t.Fatalf("add level update result = %+v, want update=0 skipped=1", result)
	}
	item := fake.find(appsync.ManagedPrefix + "Alpha")
	if got := item["priority"]; got != float64(25) {
		t.Errorf("add level changed priority to %v", got)
	}

	// Full mode applies the pending update.
	app, err := st.GetApplication(ctx, appID)
	if err != nil {
		t.Fatalf("GetApplication: %v", err)
	}
	app.SyncLevel = "full"
	if err := st.UpdateApplication(ctx, app); err != nil {
		t.Fatalf("UpdateApplication: %v", err)
	}
	result, err = svc.SyncApp(ctx, appID)
	if err != nil {
		t.Fatalf("full SyncApp: %v", err)
	}
	if result.Updated != 1 || result.Skipped != 0 {
		t.Fatalf("full level result = %+v", result)
	}
	item = fake.find(appsync.ManagedPrefix + "Alpha")
	if got := item["priority"]; got != float64(40) {
		t.Errorf("full level priority = %v, want 40", got)
	}
}

func TestSyncDisabledDoesNothing(t *testing.T) {
	ctx := context.Background()
	st := newSyncStore(t, true)
	v := newSyncVault(t)
	fake := newFakeServarr(t, testAppKey)

	createTestDefinition(t, st, "torrent-def", "torrent", 5000)
	createTestIndexer(t, st, "torrent-def", "torrent", "alpha", "Alpha", true, 25)
	appID := createTestApplication(t, st, v, "sonarr", "Sonarr", fake.server.URL, testAppKey, "disabled")

	// A managed indexer already present in the application must survive a
	// disabled sync.
	fake.addManual(appsync.ManagedPrefix+"Old", "https://old.example/api")

	svc := newSyncService(t, st, v, testBaseURL)
	result, err := svc.SyncApp(ctx, appID)
	if err != nil {
		t.Fatalf("SyncApp: %v", err)
	}
	if result.Added != 0 || result.Updated != 0 || result.Deleted != 0 || result.Skipped != 0 {
		t.Fatalf("disabled sync result = %+v", result)
	}
	if fake.listCount() != 0 {
		t.Errorf("disabled sync contacted the application (%d list calls)", fake.listCount())
	}
	if fake.find(appsync.ManagedPrefix+"Old") == nil {
		t.Error("disabled sync deleted a remote indexer")
	}
	app, err := st.GetApplication(ctx, appID)
	if err != nil {
		t.Fatalf("GetApplication: %v", err)
	}
	if !app.LastSyncAt.IsZero() || app.LastError != "" {
		t.Errorf("disabled sync recorded state: %+v", app)
	}
}

func TestSyncAllContinuesAfterFailure(t *testing.T) {
	ctx := context.Background()
	st := newSyncStore(t, true)
	v := newSyncVault(t)

	bad := newFakeServarr(t, testAppKey)
	bad.setFail(true)
	// A real application may echo the submitted fields in its error body. The
	// indexarr Torznab key must never reach last_error.
	bad.mu.Lock()
	bad.failBody = "validation failed for apiKey " + testIndexarrKey
	bad.mu.Unlock()
	good := newFakeServarr(t, testAppKey)

	createTestDefinition(t, st, "torrent-def", "torrent", 5000)
	createTestIndexer(t, st, "torrent-def", "torrent", "alpha", "Alpha", true, 25)
	badID := createTestApplication(t, st, v, "sonarr", "A Bad", bad.server.URL, testAppKey, "full")
	goodID := createTestApplication(t, st, v, "radarr", "B Good", good.server.URL, testAppKey, "full")

	svc := newSyncService(t, st, v, testBaseURL)
	results := svc.SyncAll(ctx)
	if len(results) != 2 {
		t.Fatalf("SyncAll returned %d results, want 2", len(results))
	}
	if results[0].ApplicationID != badID || results[0].Err == nil {
		t.Fatalf("failed application result = %+v", results[0])
	}
	if strings.Contains(results[0].Err.Error(), testIndexarrKey) {
		t.Errorf("sync result leaks the indexarr API key: %v", results[0].Err)
	}
	if results[1].ApplicationID != goodID || results[1].Err != nil || results[1].Added != 1 {
		t.Fatalf("successful application result = %+v", results[1])
	}

	badApp, err := st.GetApplication(ctx, badID)
	if err != nil {
		t.Fatalf("GetApplication(bad): %v", err)
	}
	if badApp.LastError == "" || badApp.LastSyncAt.IsZero() {
		t.Errorf("failed application did not record last_error/last_sync_at: %+v", badApp)
	}
	if strings.Contains(badApp.LastError, testAppKey) {
		t.Error("last_error leaks the application API key")
	}
	if strings.Contains(badApp.LastError, testIndexarrKey) {
		t.Error("last_error leaks the indexarr API key")
	}
	goodApp, err := st.GetApplication(ctx, goodID)
	if err != nil {
		t.Fatalf("GetApplication(good): %v", err)
	}
	if goodApp.LastError != "" || goodApp.LastSyncAt.IsZero() {
		t.Errorf("successful application state = %+v", goodApp)
	}
	if good.count() != 1 {
		t.Errorf("good application indexers = %d, want 1", good.count())
	}
}

func TestSyncErrors(t *testing.T) {
	ctx := context.Background()

	t.Run("vault missing", func(t *testing.T) {
		st := newSyncStore(t, true)
		v := newSyncVault(t)
		fake := newFakeServarr(t, testAppKey)
		createTestDefinition(t, st, "torrent-def", "torrent", 5000)
		createTestIndexer(t, st, "torrent-def", "torrent", "alpha", "Alpha", true, 25)
		appID := createTestApplication(t, st, v, "sonarr", "Sonarr", fake.server.URL, testAppKey, "full")

		svc := newSyncService(t, st, nil, testBaseURL)
		_, err := svc.SyncApp(ctx, appID)
		if err == nil || !strings.Contains(err.Error(), "vault") {
			t.Fatalf("vault missing error = %v", err)
		}
	})

	t.Run("base URL missing", func(t *testing.T) {
		st := newSyncStore(t, true)
		v := newSyncVault(t)
		fake := newFakeServarr(t, testAppKey)
		createTestDefinition(t, st, "torrent-def", "torrent", 5000)
		createTestIndexer(t, st, "torrent-def", "torrent", "alpha", "Alpha", true, 25)
		appID := createTestApplication(t, st, v, "sonarr", "Sonarr", fake.server.URL, testAppKey, "full")

		svc := newSyncService(t, st, v, "")
		_, err := svc.SyncApp(ctx, appID)
		if err == nil || !strings.Contains(err.Error(), "base URL") {
			t.Fatalf("base URL missing error = %v", err)
		}
	})

	t.Run("indexarr key missing", func(t *testing.T) {
		st := newSyncStore(t, false)
		v := newSyncVault(t)
		fake := newFakeServarr(t, testAppKey)
		createTestDefinition(t, st, "torrent-def", "torrent", 5000)
		createTestIndexer(t, st, "torrent-def", "torrent", "alpha", "Alpha", true, 25)
		appID := createTestApplication(t, st, v, "sonarr", "Sonarr", fake.server.URL, testAppKey, "full")

		svc := newSyncService(t, st, v, testBaseURL)
		_, err := svc.SyncApp(ctx, appID)
		if err == nil || !strings.Contains(err.Error(), "indexarr API key") {
			t.Fatalf("missing indexarr key error = %v", err)
		}
	})

	t.Run("application key missing", func(t *testing.T) {
		st := newSyncStore(t, true)
		v := newSyncVault(t)
		fake := newFakeServarr(t, testAppKey)
		createTestDefinition(t, st, "torrent-def", "torrent", 5000)
		createTestIndexer(t, st, "torrent-def", "torrent", "alpha", "Alpha", true, 25)
		appID, err := st.CreateApplication(ctx, store.Application{
			Kind: "sonarr", Name: "Sonarr", URL: fake.server.URL, SyncLevel: "full",
		})
		if err != nil {
			t.Fatalf("CreateApplication: %v", err)
		}

		svc := newSyncService(t, st, v, testBaseURL)
		_, err = svc.SyncApp(ctx, appID)
		if err == nil || !strings.Contains(err.Error(), "no API key") {
			t.Fatalf("missing application key error = %v", err)
		}
	})
}

func TestStartWaitCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	st := newSyncStore(t, true)
	v := newSyncVault(t)
	fake := newFakeServarr(t, testAppKey)
	createTestDefinition(t, st, "torrent-def", "torrent", 5000)
	createTestIndexer(t, st, "torrent-def", "torrent", "alpha", "Alpha", true, 25)
	appID := createTestApplication(t, st, v, "sonarr", "Sonarr", fake.server.URL, testAppKey, "full")

	svc := newSyncService(t, st, v, testBaseURL)
	svc.initialDelay = 10 * time.Millisecond
	svc.interval = 20 * time.Millisecond
	svc.Start(ctx)

	// Wait for the initial sync plus at least one tick. The sync reads the
	// indexer list twice (the snapshot reads it too), so last_sync_at is the
	// reliable signal that a sync completed.
	var (
		app store.Application
		err error
	)
	deadline := time.Now().Add(3 * time.Second)
	for {
		app, err = st.GetApplication(ctx, appID)
		if err == nil && !app.LastSyncAt.IsZero() && fake.listCount() >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("automatic sync did not run (list calls = %d)", fake.listCount())
		}
		time.Sleep(5 * time.Millisecond)
	}
	if app.LastSyncAt.IsZero() {
		t.Error("automatic sync did not record last_sync_at")
	}
	if fake.count() != 1 {
		t.Errorf("automatic sync created %d indexers, want 1", fake.count())
	}

	cancel()
	done := make(chan struct{})
	go func() {
		svc.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Wait did not return after cancellation")
	}
}

// TestScrubErrorRedactsPartialCredentialQuery: an application may echo only a
// prefix of the indexerr key, so exact-secret replacement is not sufficient.
func TestScrubErrorRedactsPartialCredentialQuery(t *testing.T) {
	t.Parallel()

	err := fmt.Errorf("appsync: add: HTTP 400: GET http://host/torznab/x/api?t=caps&apikey=e81ef323bc5770")
	scrubbed := scrubError(err, "a-completely-different-key")
	if strings.Contains(scrubbed.Error(), "e81ef323bc5770") {
		t.Fatalf("partial key leaked: %s", scrubbed.Error())
	}
	if !strings.Contains(scrubbed.Error(), "apikey=[REDACTED]") {
		t.Fatalf("credential query not redacted: %s", scrubbed.Error())
	}
}

func TestClampPriority(t *testing.T) {
	tests := map[int]int{-5: 1, 0: 25, 1: 1, 25: 25, 50: 50, 51: 50}
	for in, want := range tests {
		if got := clampPriority(in); got != want {
			t.Errorf("clampPriority(%d) = %d, want %d", in, got, want)
		}
	}
}

func (f *fakeServarr) edit(id int, fn func(map[string]any)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if item, ok := f.items[id]; ok && item != nil {
		fn(item)
	}
}

func (f *fakeServarr) remove(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for id, item := range f.items {
		if item["name"] == name {
			delete(f.items, id)
			return
		}
	}
}

// TestSyncSnapshotsBeforeEveryChange proves the safety net: a sync that
// modifies the application stores the previous list first, while a no-op sync
// stores nothing.
func TestSyncSnapshotsBeforeEveryChange(t *testing.T) {
	ctx := context.Background()
	st := newSyncStore(t, true)
	v := newSyncVault(t)
	fake := newFakeServarr(t, testAppKey)

	createTestDefinition(t, st, "snap-def", "torrent", 5000)
	alpha := createTestIndexer(t, st, "snap-def", "torrent", "alpha", "Alpha", true, 25)
	createTestIndexer(t, st, "snap-def", "torrent", "beta", "Beta", true, 25)
	appID := createTestApplication(t, st, v, "sonarr", "Sonarr", fake.server.URL, testAppKey, "full")
	svc := newSyncService(t, st, v, testBaseURL)

	if _, err := svc.SyncApp(ctx, appID); err != nil {
		t.Fatalf("first SyncApp: %v", err)
	}
	snapshots, err := st.ListAppSnapshots(ctx, appID)
	if err != nil {
		t.Fatalf("ListAppSnapshots: %v", err)
	}
	if len(snapshots) != 1 {
		t.Fatalf("snapshots after first sync = %d, want 1", len(snapshots))
	}
	if snapshots[0].Reason != store.SnapshotReasonSync || snapshots[0].IndexerCount != 0 {
		t.Fatalf("first snapshot = %#v", snapshots[0])
	}
	if strings.Contains(snapshots[0].Payload, testAppKey) {
		t.Fatal("the snapshot contains the application API key")
	}

	// A no-op sync does not create a snapshot.
	if _, err := svc.SyncApp(ctx, appID); err != nil {
		t.Fatalf("no-op SyncApp: %v", err)
	}
	snapshots, _ = st.ListAppSnapshots(ctx, appID)
	if len(snapshots) != 1 {
		t.Fatalf("a no-op sync created a snapshot: %d", len(snapshots))
	}

	// A real change stores the state from before the change.
	alpha.Priority = 40
	if err := st.UpdateIndexer(ctx, alpha); err != nil {
		t.Fatalf("UpdateIndexer: %v", err)
	}
	if _, err := svc.SyncApp(ctx, appID); err != nil {
		t.Fatalf("changing SyncApp: %v", err)
	}
	snapshots, _ = st.ListAppSnapshots(ctx, appID)
	if len(snapshots) != 2 {
		t.Fatalf("snapshots after change = %d, want 2", len(snapshots))
	}
	if snapshots[0].IndexerCount != 2 {
		t.Errorf("snapshot indexer count = %d, want 2", snapshots[0].IndexerCount)
	}
	for _, name := range []string{appsync.ManagedPrefix + "Alpha", appsync.ManagedPrefix + "Beta"} {
		if !strings.Contains(snapshots[0].Payload, name) {
			t.Errorf("snapshot payload misses %q", name)
		}
	}
}

// TestSyncApplicationProfile covers the per-application Prowlarr sync
// profile: search flags, the seeder floor and category filtering.
func TestSyncApplicationProfile(t *testing.T) {
	ctx := context.Background()
	st := newSyncStore(t, true)
	v := newSyncVault(t)
	fake := newFakeServarr(t, testAppKey)

	createTestDefinition(t, st, "tv-def", "torrent", 5000, 2000)
	createTestDefinition(t, st, "movie-def", "torrent", 2000)
	createTestDefinition(t, st, "nocat-def", "torrent")
	alpha := createTestIndexer(t, st, "tv-def", "torrent", "alpha", "Alpha", true, 25)
	createTestIndexer(t, st, "movie-def", "torrent", "beta", "Beta", true, 25)
	gamma := createTestIndexer(t, st, "nocat-def", "torrent", "gamma", "Gamma", true, 25)
	// Gamma already uses the same floor as the first application profile, so
	// the second sync below only updates alpha.
	gamma.MinSeeders = 9
	if err := st.UpdateIndexer(ctx, gamma); err != nil {
		t.Fatalf("UpdateIndexer gamma: %v", err)
	}

	appID := createTestApplication(t, st, v, "sonarr", "Sonarr", fake.server.URL, testAppKey, "full")
	app, err := st.GetApplication(ctx, appID)
	if err != nil {
		t.Fatalf("GetApplication: %v", err)
	}
	app.EnableRSS = false
	app.EnableAutomaticSearch = true
	app.EnableInteractiveSearch = false
	app.MinSeeders = 9
	app.Categories = "5000"
	if err := st.UpdateApplication(ctx, app); err != nil {
		t.Fatalf("UpdateApplication: %v", err)
	}
	svc := newSyncService(t, st, v, testBaseURL)

	result, err := svc.SyncApp(ctx, appID)
	if err != nil {
		t.Fatalf("SyncApp: %v", err)
	}
	if result.Added != 2 || result.Updated != 0 || result.Deleted != 0 {
		t.Fatalf("profile sync result = %+v", result)
	}
	if fake.find(appsync.ManagedPrefix+"Beta") != nil {
		t.Error("an indexer with no category overlap was synced")
	}

	alphaItem := fake.find(appsync.ManagedPrefix + "Alpha")
	if alphaItem == nil {
		t.Fatal("alpha was not added")
	}
	if got := fakeCategories(t, alphaItem); fmt.Sprint(got) != "[5000]" {
		t.Errorf("alpha categories = %v, want the selected TV category", got)
	}
	for flag, want := range map[string]bool{
		"enableRss":               false,
		"enableAutomaticSearch":   true,
		"enableInteractiveSearch": false,
	} {
		if got := alphaItem[flag]; got != want {
			t.Errorf("alpha %s = %v, want %v", flag, got, want)
		}
	}
	if got := fakeField(alphaItem, "minimumSeeders"); got != float64(9) {
		t.Errorf("alpha minimumSeeders = %v, want the application override 9", got)
	}

	gammaItem := fake.find(appsync.ManagedPrefix + "Gamma")
	if gammaItem == nil {
		t.Fatal("an indexer without categories did not use the application selection")
	}
	if got := fakeCategories(t, gammaItem); fmt.Sprint(got) != "[5000]" {
		t.Errorf("gamma categories = %v, want the application selection", got)
	}

	// Zero on the application keeps the indexer's own seeder floor.
	alpha.MinSeeders = 3
	if err := st.UpdateIndexer(ctx, alpha); err != nil {
		t.Fatalf("UpdateIndexer: %v", err)
	}
	app, err = st.GetApplication(ctx, appID)
	if err != nil {
		t.Fatalf("GetApplication: %v", err)
	}
	app.MinSeeders = 0
	if err := st.UpdateApplication(ctx, app); err != nil {
		t.Fatalf("UpdateApplication zero seeders: %v", err)
	}
	result, err = svc.SyncApp(ctx, appID)
	if err != nil {
		t.Fatalf("SyncApp without override: %v", err)
	}
	if result.Updated != 1 {
		t.Fatalf("seeder change result = %+v, want one update", result)
	}
	alphaItem = fake.find(appsync.ManagedPrefix + "Alpha")
	if got := fakeField(alphaItem, "minimumSeeders"); got != float64(3) {
		t.Errorf("alpha minimumSeeders = %v, want the indexer value 3", got)
	}
}

// TestSyncTagFilter proves that an application only receives the indexerr
// indexers whose labels match its filter, and that changing the filter both
// adds and removes managed indexers.
func TestSyncTagFilter(t *testing.T) {
	ctx := context.Background()
	st := newSyncStore(t, true)
	v := newSyncVault(t)
	fake := newFakeServarr(t, testAppKey)

	createTestDefinition(t, st, "filter-def", "torrent", 5000)
	alpha := createTestIndexer(t, st, "filter-def", "torrent", "alpha", "Alpha", true, 25)
	alpha.Tags = "TV, Anime"
	if err := st.UpdateIndexer(ctx, alpha); err != nil {
		t.Fatalf("UpdateIndexer alpha: %v", err)
	}
	beta := createTestIndexer(t, st, "filter-def", "torrent", "beta", "Beta", true, 25)
	beta.Tags = "Movies"
	if err := st.UpdateIndexer(ctx, beta); err != nil {
		t.Fatalf("UpdateIndexer beta: %v", err)
	}

	appID := createTestApplication(t, st, v, "sonarr", "Sonarr", fake.server.URL, testAppKey, "full")
	app, err := st.GetApplication(ctx, appID)
	if err != nil {
		t.Fatalf("GetApplication: %v", err)
	}
	app.TagFilter = "tv"
	if err := st.UpdateApplication(ctx, app); err != nil {
		t.Fatalf("UpdateApplication filter: %v", err)
	}
	svc := newSyncService(t, st, v, testBaseURL)

	if _, err := svc.SyncApp(ctx, appID); err != nil {
		t.Fatalf("filtered SyncApp: %v", err)
	}
	if fake.find(appsync.ManagedPrefix+"Alpha") == nil {
		t.Error("the matching indexer was not added")
	}
	if fake.find(appsync.ManagedPrefix+"Beta") != nil {
		t.Error("an indexer outside the tag filter was added")
	}
	if fake.count() != 1 {
		t.Fatalf("remote indexers = %d, want 1", fake.count())
	}

	// Clearing the filter includes every indexer.
	app.TagFilter = ""
	if err := st.UpdateApplication(ctx, app); err != nil {
		t.Fatalf("UpdateApplication clear: %v", err)
	}
	if _, err := svc.SyncApp(ctx, appID); err != nil {
		t.Fatalf("cleared SyncApp: %v", err)
	}
	if fake.count() != 2 {
		t.Fatalf("remote indexers after clearing = %d, want 2", fake.count())
	}

	// A narrower filter removes only the managed indexer that no longer
	// matches.
	fake.addManual("Manual tracker", "https://manual.example/api")
	app.TagFilter = "movies"
	if err := st.UpdateApplication(ctx, app); err != nil {
		t.Fatalf("UpdateApplication movies: %v", err)
	}
	result, err := svc.SyncApp(ctx, appID)
	if err != nil {
		t.Fatalf("movies SyncApp: %v", err)
	}
	if result.Deleted != 1 {
		t.Fatalf("movies result = %+v", result)
	}
	if fake.find(appsync.ManagedPrefix+"Alpha") != nil {
		t.Error("the filtered-out indexer is still in the app")
	}
	if fake.find(appsync.ManagedPrefix+"Beta") == nil {
		t.Error("the matching indexer was removed")
	}
	if fake.find("Manual tracker") == nil {
		t.Error("the manual indexer was touched")
	}
}

// TestRestoreRecreatesUpdatesAndDeletesOnlyManaged covers the whole restore
// contract against a fake Servarr.
func TestRestoreRecreatesUpdatesAndDeletesOnlyManaged(t *testing.T) {
	ctx := context.Background()
	st := newSyncStore(t, true)
	v := newSyncVault(t)
	fake := newFakeServarr(t, testAppKey)

	createTestDefinition(t, st, "restore-def", "torrent", 5000)
	createTestIndexer(t, st, "restore-def", "torrent", "alpha", "Alpha", true, 25)
	createTestIndexer(t, st, "restore-def", "torrent", "beta", "Beta", true, 25)
	appID := createTestApplication(t, st, v, "sonarr", "Sonarr", fake.server.URL, testAppKey, "full")
	svc := newSyncService(t, st, v, testBaseURL)

	if _, err := svc.SyncApp(ctx, appID); err != nil {
		t.Fatalf("SyncApp: %v", err)
	}
	alphaItem := fake.find(appsync.ManagedPrefix + "Alpha")
	betaItem := fake.find(appsync.ManagedPrefix + "Beta")
	if alphaItem == nil || betaItem == nil {
		t.Fatal("the initial sync did not add both indexers")
	}
	alphaID := fakeRemoteID(t, alphaItem)
	betaID := fakeRemoteID(t, betaItem)

	// The operator asks for a checkpoint of the healthy state.
	checkpoint, err := svc.SnapshotApp(ctx, appID)
	if err != nil {
		t.Fatalf("SnapshotApp: %v", err)
	}

	// The operator then breaks the app by hand: an indexer is edited, one is
	// deleted, an extra managed indexer is added and a manual one appears.
	fake.edit(alphaID, func(item map[string]any) {
		item["priority"] = float64(5)
		item["name"] = appsync.ManagedPrefix + "Alpha renamed"
	})
	fake.remove(appsync.ManagedPrefix + "Beta")
	fake.addManual(appsync.ManagedPrefix+"Extra", "https://indexarr.example/torznab/extra")
	fake.addManual("Manual tracker", "https://manual.example/api")

	result, err := svc.Restore(ctx, appID, checkpoint)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if result.Updated != 1 || result.Added != 1 || result.Deleted != 1 {
		t.Fatalf("restore result = %+v", result)
	}

	restoredAlpha := fake.find(appsync.ManagedPrefix + "Alpha")
	if restoredAlpha == nil {
		t.Fatal("the edited indexer was not restored under its old name")
	}
	if got := restoredAlpha["priority"]; got != float64(25) {
		t.Errorf("restored priority = %v, want 25", got)
	}
	if got := fakeField(restoredAlpha, "apiKey"); got != testIndexarrKey {
		t.Errorf("restored apiKey = %v, want the current indexarr key", got)
	}
	if fake.find(appsync.ManagedPrefix+"Beta") == nil {
		t.Error("the deleted indexer was not recreated")
	}
	if fake.find(appsync.ManagedPrefix+"Extra") != nil {
		t.Error("the extra managed indexer was not removed")
	}
	if fake.find("Manual tracker") == nil {
		t.Error("the manual indexer was deleted by the restore")
	}
	_ = betaID

	// A restore stores the state it found, so it is itself reversible.
	snapshots, err := st.ListAppSnapshots(ctx, appID)
	if err != nil {
		t.Fatalf("ListAppSnapshots: %v", err)
	}
	if snapshots[0].Reason != store.SnapshotReasonBeforeRestore {
		t.Fatalf("newest snapshot reason = %q, want before-restore", snapshots[0].Reason)
	}
	if snapshots[0].IndexerCount != 3 {
		t.Errorf("before-restore count = %d, want 3", snapshots[0].IndexerCount)
	}

	links, err := st.ListAppIndexers(ctx, appID)
	if err != nil {
		t.Fatalf("ListAppIndexers: %v", err)
	}
	if len(links) != 2 {
		t.Fatalf("app_indexers after restore = %d, want 2: %+v", len(links), links)
	}
}

// TestSnapshotRetentionTwenty keeps the newest 20 snapshots for an app.
func TestSnapshotRetentionTwenty(t *testing.T) {
	ctx := context.Background()
	st := newSyncStore(t, true)
	v := newSyncVault(t)
	fake := newFakeServarr(t, testAppKey)

	createTestDefinition(t, st, "retention-def", "torrent", 5000)
	createTestIndexer(t, st, "retention-def", "torrent", "alpha", "Alpha", true, 25)
	appID := createTestApplication(t, st, v, "sonarr", "Sonarr", fake.server.URL, testAppKey, "full")
	svc := newSyncService(t, st, v, testBaseURL)

	for i := 0; i < store.AppSnapshotRetention+2; i++ {
		if _, err := svc.SnapshotApp(ctx, appID); err != nil {
			t.Fatalf("SnapshotApp %d: %v", i, err)
		}
	}
	snapshots, err := st.ListAppSnapshots(ctx, appID)
	if err != nil {
		t.Fatalf("ListAppSnapshots: %v", err)
	}
	if len(snapshots) != store.AppSnapshotRetention {
		t.Fatalf("snapshots = %d, want %d", len(snapshots), store.AppSnapshotRetention)
	}
	for _, snapshot := range snapshots {
		if snapshot.Reason != store.SnapshotReasonManual {
			t.Errorf("manual snapshot reason = %q", snapshot.Reason)
		}
	}
}
