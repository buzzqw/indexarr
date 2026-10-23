package compat

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/buzzqw/indexarr/internal/appsync"
	"github.com/buzzqw/indexarr/internal/simmock"
)

const servarrKey = "servarr-application-key"

type payloadCapture struct {
	mu       sync.Mutex
	payloads []map[string]any
}

func (c *payloadCapture) add(payload map[string]any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.payloads = append(c.payloads, payload)
}

func (c *payloadCapture) byName(name string) map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, payload := range c.payloads {
		if asString(payload["name"]) == name {
			return payload
		}
	}
	return nil
}

// newServarrStub implements the subset of the Servarr v3 API that appsync uses.
func newServarrStub(t *testing.T) (*payloadCapture, *httptest.Server) {
	t.Helper()
	capture := &payloadCapture{}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != servarrKey {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch {
		case r.URL.Path == "/api/v3/system/status":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"appName":"Servarr","version":"4.0.0"}`))
		case r.URL.Path == "/api/v3/indexer/schema":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[
				{"implementation":"Torznab","configContract":"TorznabSettings","protocol":"torrent",
				 "fields":[
					{"name":"baseUrl","value":""},
					{"name":"apiKey","value":""},
					{"name":"categories","value":[]},
					{"name":"apiPath","value":"/api"}
				 ]},
				{"implementation":"Newznab","configContract":"NewznabSettings","protocol":"usenet",
				 "fields":[
					{"name":"baseUrl","value":""},
					{"name":"apiKey","value":""},
					{"name":"categories","value":[]},
					{"name":"apiPath","value":"/api"}
				 ]}
			]`))
		case r.URL.Path == "/api/v3/indexer" && r.Method == http.MethodPost:
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				http.Error(w, "bad payload", http.StatusBadRequest)
				return
			}
			capture.add(payload)
			payload["id"] = float64(1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(payload)
		default:
			http.NotFound(w, r)
		}
	})
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return capture, server
}

func asString(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	return ""
}

func asInt(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case float64:
		return int(typed)
	case json.Number:
		number, _ := typed.Int64()
		return int(number)
	}
	return 0
}

func asBool(value any) bool {
	flag, _ := value.(bool)
	return flag
}

func asInts(value any) []int {
	list, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]int, 0, len(list))
	for _, item := range list {
		out = append(out, asInt(item))
	}
	return out
}

func fieldValues(payload map[string]any) map[string]any {
	out := map[string]any{}
	fields, _ := payload["fields"].([]any)
	for _, raw := range fields {
		field, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		out[asString(field["name"])] = field["value"]
	}
	return out
}

func TestServarrPayloadsForEveryArr(t *testing.T) {
	capture, server := newServarrStub(t)
	kinds := []appsync.Kind{
		appsync.KindSonarr,
		appsync.KindRadarr,
		appsync.KindLidarr,
		appsync.KindReadarr,
		appsync.KindWhisparr,
	}
	protocols := []struct {
		label      string
		impl       string
		contract   string
		protocol   string
		categories []int
	}{
		{"torznab-torrent", "Torznab", "TorznabSettings", "torrent", []int{5000, 2000}},
		{"newznab-usenet", "Newznab", "NewznabSettings", "usenet", []int{7000, 3000}},
	}

	for _, kind := range kinds {
		for _, protocol := range protocols {
			client, err := appsync.New(appsync.Config{
				Kind:      kind,
				Name:      string(kind),
				URL:       server.URL,
				APIKey:    servarrKey,
				SyncLevel: "full",
			})
			if err != nil {
				t.Fatalf("appsync.New(%s): %v", kind, err)
			}
			name := "indexarr " + string(kind) + " " + protocol.label
			spec := appsync.IndexerSpec{
				Name:                    name,
				Implementation:          protocol.impl,
				ConfigContract:          protocol.contract,
				BaseURL:                 "https://indexarr.example/torznab/" + string(kind) + "/api/",
				APIKey:                  primaryKey,
				Categories:              protocol.categories,
				Priority:                13,
				Tags:                    []int{7, 9},
				EnableRSS:               true,
				EnableAutomaticSearch:   true,
				EnableInteractiveSearch: true,
				Protocol:                protocol.protocol,
			}
			remote, err := client.AddIndexer(context.Background(), spec)
			if err != nil {
				t.Fatalf("%s AddIndexer: %v", name, err)
			}
			if remote.ID != 1 {
				t.Errorf("%s remote id = %d, want 1", name, remote.ID)
			}
		}
	}

	for _, kind := range kinds {
		for _, protocol := range protocols {
			name := "indexarr " + string(kind) + " " + protocol.label
			payload := capture.byName(name)
			if payload == nil {
				t.Fatalf("no POST payload captured for %q", name)
			}

			if got := asString(payload["implementation"]); got != protocol.impl {
				t.Errorf("%s implementation = %q, want %q", name, got, protocol.impl)
			}
			if got := asString(payload["configContract"]); got != protocol.contract {
				t.Errorf("%s configContract = %q, want %q", name, got, protocol.contract)
			}
			if got := asString(payload["protocol"]); got != protocol.protocol {
				t.Errorf("%s protocol = %q, want %q", name, got, protocol.protocol)
			}
			if !asBool(payload["enableRss"]) {
				t.Errorf("%s enableRss is not true", name)
			}
			if !asBool(payload["enableAutomaticSearch"]) {
				t.Errorf("%s enableAutomaticSearch is not true", name)
			}
			if !asBool(payload["enableInteractiveSearch"]) {
				t.Errorf("%s enableInteractiveSearch is not true", name)
			}
			if got := asInt(payload["priority"]); got != 13 {
				t.Errorf("%s priority = %d, want 13", name, got)
			}
			if got := asInts(payload["tags"]); len(got) != 2 || got[0] != 7 || got[1] != 9 {
				t.Errorf("%s tags = %v, want [7 9]", name, got)
			}

			fields := fieldValues(payload)
			if got := asString(fields["baseUrl"]); got != "https://indexarr.example/torznab/"+string(kind)+"/api" {
				t.Errorf("%s baseUrl = %q", name, got)
			}
			if got := asString(fields["apiKey"]); got != primaryKey {
				t.Errorf("%s apiKey = %q", name, got)
			}
			if got := asInts(fields["categories"]); len(got) != 2 ||
				got[0] != protocol.categories[0] || got[1] != protocol.categories[1] {
				t.Errorf("%s categories = %v, want %v", name, got, protocol.categories)
			}
			if got := asString(fields["apiPath"]); got != "/api" {
				t.Errorf("%s schema default apiPath = %q", name, got)
			}
		}
	}
}

func TestSimmockServarrAcceptsIndexarrPayloads(t *testing.T) {
	mock := simmock.New(simmock.Config{
		APIKey:    "demo-key",
		SonarrKey: "demo-sonarr",
		Logger:    log.New(io.Discard, "", 0),
	})
	server := httptest.NewServer(mock.Handler())
	defer server.Close()

	client, err := appsync.New(appsync.Config{
		Kind:      appsync.KindSonarr,
		URL:       server.URL + "/sonarr",
		APIKey:    "demo-sonarr",
		SyncLevel: "full",
	})
	if err != nil {
		t.Fatalf("appsync.New: %v", err)
	}
	specs := []appsync.IndexerSpec{
		{
			Name: "indexarr Torznab", Implementation: "Torznab", ConfigContract: "TorznabSettings",
			BaseURL: server.URL + "/torznab/alpha/api", APIKey: primaryKey,
			Categories: []int{5000}, Protocol: "torrent",
			EnableRSS: true, EnableAutomaticSearch: true, EnableInteractiveSearch: true,
		},
		{
			Name: "indexarr Newznab", Implementation: "Newznab", ConfigContract: "NewznabSettings",
			BaseURL: server.URL + "/torznab/beta/api", APIKey: primaryKey,
			Categories: []int{7000}, Protocol: "usenet",
			EnableRSS: true, EnableAutomaticSearch: true, EnableInteractiveSearch: true,
		},
	}
	for _, spec := range specs {
		if _, err := client.AddIndexer(context.Background(), spec); err != nil {
			t.Fatalf("AddIndexer(%s): %v", spec.Name, err)
		}
	}

	response, err := http.Get(server.URL + "/debug/state")
	if err != nil {
		t.Fatalf("GET /debug/state: %v", err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	var state simmock.State
	if err := json.Unmarshal(body, &state); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	if len(state.SonarrIndexers) != 2 {
		t.Fatalf("simmock Sonarr has %d indexers, want 2", len(state.SonarrIndexers))
	}
	for _, payload := range state.SonarrIndexers {
		name := asString(payload["name"])
		if !strings.HasPrefix(name, "indexarr ") {
			t.Errorf("unexpected indexer name %q", name)
		}
		fields := fieldValues(payload)
		if asString(fields["baseUrl"]) == "" || asString(fields["apiKey"]) == "" {
			t.Errorf("%s has incomplete fields: %v", name, fields)
		}
	}
}
