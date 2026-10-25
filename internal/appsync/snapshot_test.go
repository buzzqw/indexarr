package appsync

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSnapshotSpecsKeepsOnlyManagedIndexers(t *testing.T) {
	payload := []byte(`[
		{"id":1,"name":"indexarr · Tracker","implementation":"Torznab",
		 "configContract":"TorznabSettings","protocol":"torrent","priority":40,
		 "tags":[3,1],"enableRss":true,"enableAutomaticSearch":true,
		 "enableInteractiveSearch":false,
		 "fields":[{"name":"baseUrl","value":"https://indexarr.example/torznab/a/"},
		           {"name":"apiKey","value":"k1"},
		           {"name":"categories","value":[5000,5070]}]},
		{"id":2,"name":"Manual tracker","implementation":"Torznab",
		 "configContract":"TorznabSettings","protocol":"torrent","priority":25,
		 "tags":[],"fields":[{"name":"baseUrl","value":"https://manual.example"}]},
		{"id":3,"name":"indexarr · Usenet","implementation":"Newznab",
		 "configContract":"NewznabSettings","protocol":"usenet","priority":10,
		 "tags":[],"fields":[{"name":"baseUrl","value":"https://indexarr.example/torznab/b"},
		           {"name":"apiKey","value":"k2"},
		           {"name":"categories","value":[7000]}]}
	]`)

	specs, err := SnapshotSpecs(payload)
	if err != nil {
		t.Fatalf("SnapshotSpecs: %v", err)
	}
	if len(specs) != 2 {
		t.Fatalf("specs = %d, want 2: %+v", len(specs), specs)
	}
	first := specs[0]
	if first.Name != ManagedPrefix+"Tracker" || first.BaseURL != "https://indexarr.example/torznab/a/" ||
		first.APIKey != "k1" || first.Priority != 40 || first.Protocol != "torrent" ||
		len(first.Categories) != 2 || first.Categories[0] != 5000 ||
		len(first.Tags) != 2 || first.EnableInteractiveSearch {
		t.Fatalf("first spec = %#v", first)
	}
	if specs[1].Implementation != "Newznab" || specs[1].Protocol != "usenet" {
		t.Fatalf("second spec = %#v", specs[1])
	}

	empty, err := SnapshotSpecs([]byte("[]"))
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty snapshot = %#v, %v", empty, err)
	}
	if _, err := SnapshotSpecs([]byte("{not json")); err == nil {
		t.Fatal("SnapshotSpecs accepted a malformed payload")
	}
}

func TestCountSnapshotIndexers(t *testing.T) {
	if got := CountSnapshotIndexers([]byte(`[{"name":"a"},{"name":"b"}]`)); got != 2 {
		t.Fatalf("count = %d, want 2", got)
	}
	if got := CountSnapshotIndexers([]byte(`not-json`)); got != 0 {
		t.Fatalf("invalid count = %d, want 0", got)
	}
}

// TestIndexerSnapshotRedactsAppKey proves the snapshot is the raw list but
// never contains the application API key, even if a mock echoes it.
func TestIndexerSnapshotRedactsAppKey(t *testing.T) {
	t.Parallel()

	const appKey = "app-secret-key"
	var snapshot []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/system/status":
			_, _ = w.Write([]byte(`{"appName":"Sonarr","version":"4.0"}`))
		case "/api/v3/indexer":
			_, _ = w.Write([]byte(`[{"id":1,"name":"indexarr · A","fields":[
				{"name":"baseUrl","value":"https://indexarr.example/torznab/a"},
				{"name":"note","value":"` + appKey + `"}]}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := New(Config{Kind: KindSonarr, URL: server.URL, APIKey: appKey})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	snapshotter, ok := client.(Snapshotter)
	if !ok {
		t.Fatal("Sonarr client does not implement Snapshotter")
	}
	snapshot, err = snapshotter.IndexerSnapshot(context.Background())
	if err != nil {
		t.Fatalf("IndexerSnapshot: %v", err)
	}
	if strings.Contains(string(snapshot), appKey) {
		t.Fatalf("snapshot contains the application key: %s", snapshot)
	}
	var entries []map[string]any
	if err := json.Unmarshal(snapshot, &entries); err != nil || len(entries) != 1 {
		t.Fatalf("snapshot = %s, %v", snapshot, err)
	}
}
