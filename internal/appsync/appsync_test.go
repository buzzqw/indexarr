package appsync

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
)

func TestKindValidAndUsesServarrAPI(t *testing.T) {
	t.Parallel()

	tests := []struct {
		kind    Kind
		valid   bool
		servarr bool
	}{
		{KindSonarr, true, true},
		{KindRadarr, true, true},
		{KindLidarr, true, true},
		{KindReadarr, true, true},
		{KindWhisparr, true, true},
		{KindMylar3, true, false},
		{KindLazyLibrarian, true, false},
		{Kind("prowlarr"), false, false},
		{Kind(""), false, false},
	}
	for _, test := range tests {
		if got := test.kind.Valid(); got != test.valid {
			t.Errorf("%q.Valid() = %v, want %v", test.kind, got, test.valid)
		}
		if got := test.kind.UsesServarrAPI(); got != test.servarr {
			t.Errorf("%q.UsesServarrAPI() = %v, want %v", test.kind, got, test.servarr)
		}
	}
}

// TestCategoriesForKind is the Blocco C acceptance rule: Sonarr only gets TV
// categories and Radarr only movie categories, whatever the site declares.
func TestCategoriesForKind(t *testing.T) {
	t.Parallel()

	all := []int{8000, 5000, 5070, 2000, 2040, 3000, 7000, 6000}
	cases := []struct {
		kind Kind
		want []int
	}{
		{KindSonarr, []int{5000, 5070}},
		{KindRadarr, []int{2000, 2040}},
		{KindLidarr, []int{3000}},
		{KindReadarr, []int{7000}},
		{KindWhisparr, []int{6000}},
		{Kind("other"), []int{2000, 2040, 3000, 5000, 5070, 6000, 7000, 8000}},
	}
	for _, test := range cases {
		if got := CategoriesForKind(test.kind, all); !slices.Equal(got, test.want) {
			t.Errorf("CategoriesForKind(%s) = %v, want %v", test.kind, got, test.want)
		}
	}
	if got := CategoriesForKind(KindSonarr, []int{2000, 3000}); len(got) != 0 {
		t.Errorf("CategoriesForKind(Sonarr, movie+audio) = %v, want empty", got)
	}
	// The input is not mutated.
	if all[0] != 8000 || len(all) != 8 {
		t.Errorf("input slice was mutated: %v", all)
	}
}

func TestNewDispatchAndValidation(t *testing.T) {
	t.Parallel()

	for _, kind := range []Kind{KindSonarr, KindRadarr, KindLidarr, KindReadarr,
		KindWhisparr, KindMylar3, KindLazyLibrarian} {
		client, err := New(Config{Kind: kind, URL: "http://127.0.0.1:8080", APIKey: "key"})
		if err != nil {
			t.Fatalf("New(%s) returned error: %v", kind, err)
		}
		if client == nil {
			t.Fatalf("New(%s) returned a nil client", kind)
		}
	}

	if _, err := New(Config{Kind: "prowlarr", URL: "http://localhost"}); err == nil {
		t.Fatal("New accepted an unknown kind")
	}
	if _, err := New(Config{Kind: KindSonarr, URL: ""}); err == nil {
		t.Fatal("New accepted an empty URL")
	}
	if _, err := New(Config{Kind: KindSonarr, URL: "ftp://localhost"}); err == nil {
		t.Fatal("New accepted a non-HTTP URL")
	}
	if _, err := New(Config{Kind: KindSonarr, URL: "http://localhost", SyncLevel: "sometimes"}); err == nil {
		t.Fatal("New accepted an invalid sync level")
	}
}

func TestSpecHashStableAndSensitive(t *testing.T) {
	t.Parallel()

	base := IndexerSpec{
		Name:                    "indexarr · Tracker",
		Implementation:          "Torznab",
		ConfigContract:          "TorznabSettings",
		BaseURL:                 "https://tracker.example/api/",
		APIKey:                  "indexer-key",
		Categories:              []int{5000, 2000},
		Priority:                25,
		Tags:                    []int{3, 1},
		EnableRSS:               true,
		EnableAutomaticSearch:   true,
		EnableInteractiveSearch: true,
		Protocol:                "torrent",
		AdditionalFields:        map[string]any{"additionalParameters": "a=1", "other": 2},
	}

	reordered := base
	reordered.Categories = []int{2000, 5000}
	reordered.Tags = []int{1, 3}
	reordered.BaseURL = "https://tracker.example/api"
	reordered.AdditionalFields = map[string]any{"other": 2, "additionalParameters": "a=1"}

	if got, want := SpecHash(reordered), SpecHash(base); got != want {
		t.Fatalf("SpecHash is not order-insensitive: %s != %s", got, want)
	}

	changes := map[string]IndexerSpec{
		"name":     func() IndexerSpec { s := base; s.Name = "other"; return s }(),
		"baseURL":  func() IndexerSpec { s := base; s.BaseURL = "https://other.example/api"; return s }(),
		"apiKey":   func() IndexerSpec { s := base; s.APIKey = "rotated"; return s }(),
		"priority": func() IndexerSpec { s := base; s.Priority = 10; return s }(),
		"tags":     func() IndexerSpec { s := base; s.Tags = []int{3}; return s }(),
		"rss":      func() IndexerSpec { s := base; s.EnableRSS = false; return s }(),
	}
	for name, changed := range changes {
		if SpecHash(changed) == SpecHash(base) {
			t.Errorf("SpecHash did not change when %s changed", name)
		}
	}

	// The implementation is derived from the protocol when omitted.
	usenet := IndexerSpec{Name: "n", BaseURL: "https://n.example", Protocol: "usenet"}
	explicit := IndexerSpec{
		Name: "n", BaseURL: "https://n.example",
		Implementation: "Newznab", ConfigContract: "NewznabSettings", Protocol: "usenet",
	}
	if SpecHash(usenet) != SpecHash(explicit) {
		t.Fatal("SpecHash did not canonicalize Newznab identity")
	}
}

func TestSimpleClientsTest(t *testing.T) {
	t.Parallel()

	tests := []struct {
		kind Kind
		body string
		want string
	}{
		{KindMylar3, `{"success":true,"data":{"version":"0.7.4"}}`, "Mylar3 0.7.4"},
		{KindLazyLibrarian, `{"result":"1.2.3"}`, "LazyLibrarian 1.2.3"},
		{KindLazyLibrarian, `{"version":"9.9.9"}`, "LazyLibrarian 9.9.9"},
	}

	for _, test := range tests {
		test := test
		t.Run(string(test.kind)+"/"+test.want, func(t *testing.T) {
			t.Parallel()
			var mu sync.Mutex
			var gotAPIKey, gotCmd string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api" {
					t.Errorf("path = %q, want /api", r.URL.Path)
				}
				mu.Lock()
				gotAPIKey = r.URL.Query().Get("apikey")
				gotCmd = r.URL.Query().Get("cmd")
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()

			client, err := New(Config{Kind: test.kind, URL: server.URL, APIKey: "simple-key"})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			got, err := client.Test(context.Background())
			if err != nil {
				t.Fatalf("Test: %v", err)
			}
			if got != test.want {
				t.Fatalf("Test = %q, want %q", got, test.want)
			}
			mu.Lock()
			defer mu.Unlock()
			if gotCmd != "getVersion" {
				t.Errorf("cmd = %q, want getVersion", gotCmd)
			}
			if gotAPIKey != "simple-key" {
				t.Errorf("apikey = %q, want simple-key", gotAPIKey)
			}
		})
	}
}

func TestSimpleClientsVersionError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":false,"error":"not logged in"}`))
	}))
	defer server.Close()

	client, err := New(Config{Kind: KindMylar3, URL: server.URL, APIKey: "key"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := client.Test(context.Background()); err == nil {
		t.Fatal("Test accepted a response without a version")
	}
}

func TestSimpleClientsManageProviders(t *testing.T) {
	t.Parallel()

	for _, kind := range []Kind{KindMylar3, KindLazyLibrarian} {
		t.Run(string(kind), func(t *testing.T) {
			var mu sync.Mutex
			seen := map[string]url.Values{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				query := r.URL.Query()
				mu.Lock()
				seen[query.Get("cmd")] = query
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				switch query.Get("cmd") {
				case "getVersion":
					_, _ = w.Write([]byte(`{"version":"1.2.3"}`))
				case "listProviders", "listNabProviders":
					_, _ = w.Write([]byte(`{"success":true,"data":{"torznabs":[{"name":"indexarr · Site","host":"http://indexarr:8788/torznab/site","apikey":"key","categories":"2000,5000","enabled":true}],"newznabs":[]}}`))
				default:
					_, _ = w.Write([]byte(`{"success":true}`))
				}
			}))
			defer server.Close()

			client, err := New(Config{Kind: kind, URL: server.URL, APIKey: "app-key"})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			ctx := context.Background()
			if version, err := client.Test(ctx); err != nil || !strings.Contains(version, "1.2.3") {
				t.Fatalf("Test = %q, %v", version, err)
			}

			remote, err := client.ListIndexers(ctx)
			if err != nil || len(remote) != 1 {
				t.Fatalf("ListIndexers = %+v, %v", remote, err)
			}
			if remote[0].Name != "indexarr · Site" || remote[0].ID == 0 {
				t.Fatalf("remote = %+v", remote[0])
			}
			snapshotter, ok := client.(Snapshotter)
			if !ok {
				t.Fatal("simple client does not implement Snapshotter")
			}
			payload, err := snapshotter.IndexerSnapshot(ctx)
			if err != nil {
				t.Fatalf("IndexerSnapshot: %v", err)
			}
			specs, err := SnapshotSpecs(payload)
			if err != nil || len(specs) != 1 || specs[0].Name != remote[0].Name {
				t.Fatalf("snapshot specs = %+v, %v", specs, err)
			}

			if remote[0].RawHash != SpecHash(IndexerSpec{
				Name: "indexarr · Site", BaseURL: "http://indexarr:8788/torznab/site",
				APIKey: "key", Categories: []int{2000, 5000},
				Implementation: "Torznab", ConfigContract: "TorznabSettings",
				Protocol: "torrent", EnableRSS: true,
				EnableAutomaticSearch: true, EnableInteractiveSearch: true, Priority: 25,
			}) {
				t.Fatal("remote hash does not match the parsed provider")
			}

			spec := IndexerSpec{
				Name: "indexarr · Site", BaseURL: "http://indexarr:8788/torznab/site",
				APIKey: "key", Categories: []int{2000, 5000}, Protocol: "torrent",
				Priority: 10, MinSeeders: 5, SeedRatio: 1.5, SeedTimeMinutes: 60,
			}
			if _, err := client.AddIndexer(ctx, spec); err != nil {
				t.Fatalf("AddIndexer: %v", err)
			}
			if _, err := client.UpdateIndexer(ctx, remote[0].ID, spec); err != nil {
				t.Fatalf("UpdateIndexer: %v", err)
			}
			if err := client.DeleteIndexer(ctx, remote[0].ID); err != nil {
				t.Fatalf("DeleteIndexer: %v", err)
			}

			mu.Lock()
			defer mu.Unlock()
			for _, command := range []string{"addProvider", "changeProvider", "delProvider"} {
				query, ok := seen[command]
				if !ok {
					t.Fatalf("%s was not called", command)
				}
				if query.Get("apikey") != "app-key" {
					t.Errorf("%s lost the app API key", command)
				}
				if command != "delProvider" {
					if query.Get("host") != spec.BaseURL || query.Get("prov_apikey") != spec.APIKey {
						t.Errorf("%s provider params = %v", command, query)
					}
					if query.Get("categories") != "2000,5000" {
						t.Errorf("%s categories = %q", command, query.Get("categories"))
					}
				}
			}
			if kind == KindLazyLibrarian {
				add := seen["addProvider"]
				if add.Get("seeders") != "5" || add.Get("seed_ratio") != "1.5" || add.Get("seed_duration") != "60" {
					t.Errorf("Lazy seed params = %v", add)
				}
				if add.Get("dlpriority") != "41" {
					t.Errorf("Lazy priority = %q, want 41", add.Get("dlpriority"))
				}
			}
			if kind == KindMylar3 && seen["addProvider"].Get("prio") != "" {
				t.Errorf("Mylar received an unsupported priority")
			}
		})
	}
}

func TestSimpleClientCrossHostRedirectStripsQueryKey(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var gotKey string
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotKey = r.URL.Query().Get("apikey")
		mu.Unlock()
		_, _ = w.Write([]byte(`{"version":"1.0.0"}`))
	}))
	defer target.Close()

	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/api?cmd=getVersion&apikey=query-secret", http.StatusFound)
	}))
	defer source.Close()

	client, err := New(Config{Kind: KindLazyLibrarian, URL: source.URL, APIKey: "query-secret"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := client.Test(context.Background()); err != nil {
		t.Fatalf("Test: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if gotKey != "" {
		t.Fatalf("cross-host redirect leaked apikey %q", gotKey)
	}
}

// TestRedactCredentialQuery covers Radarr echoing a truncated key inside a
// URL: matching the full secret is not enough, the query value itself must go.
func TestRedactCredentialQuery(t *testing.T) {
	t.Parallel()

	text := redact(`GET http://host:18541/torznab/x/api?t=caps&apikey=e81ef323bc5770 failed`, "unrelated")
	if strings.Contains(text, "e81ef323bc5770") {
		t.Errorf("partial key survived: %s", text)
	}
	if !strings.Contains(text, "apikey=[REDACTED]") {
		t.Errorf("credential query was not redacted: %s", text)
	}
}

func TestSimpleClientErrorRedactsKey(t *testing.T) {
	t.Parallel()

	const key = "simple-secret"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad key "+key+" (escaped "+url.QueryEscape(key)+")", http.StatusUnauthorized)
	}))
	defer server.Close()

	client, err := New(Config{Kind: KindMylar3, URL: server.URL, APIKey: key})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = client.Test(context.Background())
	if err == nil {
		t.Fatal("Test returned no error")
	}
	if strings.Contains(err.Error(), key) || strings.Contains(err.Error(), url.QueryEscape(key)) {
		t.Fatalf("error leaked the API key: %v", err)
	}
	if !strings.Contains(err.Error(), "401") {
		t.Fatalf("error does not mention the HTTP status: %v", err)
	}
}

func TestDesiredFieldsCarrySeedCriteria(t *testing.T) {
	spec := IndexerSpec{
		BaseURL: "https://indexarr.example/torznab/site", APIKey: "key",
		Protocol: "torrent", MinSeeders: 5, SeedRatio: 1.5,
		SeedTimeMinutes: 60, SeasonPackSeedTimeMinutes: 90,
	}
	fields := desiredFields(spec)
	if fields["minimumSeeders"] != 5 || fields["seedRatio"] != 1.5 ||
		fields["seedTime"] != 60 || fields["seasonPackSeedTime"] != 90 {
		t.Fatalf("seed fields = %#v", fields)
	}

	// Zero is sent for torrents so a cleared value converges.
	zero := desiredFields(IndexerSpec{BaseURL: "https://x", Protocol: "torrent"})
	for _, key := range []string{"minimumSeeders", "seedRatio", "seedTime", "seasonPackSeedTime"} {
		if _, ok := zero[key]; !ok {
			t.Errorf("zero value missing for %s", key)
		}
	}
	// Usenet has no seed criteria.
	usenet := desiredFields(IndexerSpec{BaseURL: "https://x", Protocol: "usenet", MinSeeders: 5})
	if _, ok := usenet["minimumSeeders"]; ok {
		t.Error("seed criteria sent for a usenet indexer")
	}
}
