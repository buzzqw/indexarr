package appsync

import (
	"context"
	"errors"
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

func TestSimpleClientsUnsupportedIndexerOperations(t *testing.T) {
	t.Parallel()

	for _, kind := range []Kind{KindMylar3, KindLazyLibrarian} {
		client, err := New(Config{Kind: kind, URL: "http://127.0.0.1:1", APIKey: "key"})
		if err != nil {
			t.Fatalf("New(%s): %v", kind, err)
		}
		ctx := context.Background()

		if _, err := client.ListIndexers(ctx); !errors.Is(err, ErrUnsupported) {
			t.Errorf("%s ListIndexers error = %v, want ErrUnsupported", kind, err)
		}
		if _, err := client.AddIndexer(ctx, IndexerSpec{Name: "x"}); !errors.Is(err, ErrUnsupported) {
			t.Errorf("%s AddIndexer error = %v, want ErrUnsupported", kind, err)
		}
		if _, err := client.UpdateIndexer(ctx, 1, IndexerSpec{Name: "x"}); !errors.Is(err, ErrUnsupported) {
			t.Errorf("%s UpdateIndexer error = %v, want ErrUnsupported", kind, err)
		}
		if err := client.DeleteIndexer(ctx, 1); !errors.Is(err, ErrUnsupported) {
			t.Errorf("%s DeleteIndexer error = %v, want ErrUnsupported", kind, err)
		}
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
