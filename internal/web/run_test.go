package web

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/buzzqw/indexarr/internal/store"
)

func TestMetricsEndpoint(t *testing.T) {
	env := newTestEnv(t)
	recorder := get(t, env.handler, "/metrics")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	body := recorder.Body.String()
	for _, metric := range []string{
		"indexarr_up 1",
		"indexarr_releases ",
		"indexarr_indexers ",
		"indexarr_blocklist_rules ",
		"indexarr_uptime_seconds ",
	} {
		if !strings.Contains(body, metric) {
			t.Errorf("metric missing: %s", metric)
		}
	}
	if contentType := recorder.Header().Get("Content-Type"); !strings.Contains(contentType, "text/plain") {
		t.Errorf("content type = %q", contentType)
	}
}

func TestRunDetail(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	if err := env.store.UpsertDefinition(ctx, store.Definition{
		ID: "run-def", SchemaVersion: 1, Family: "torznab", State: "verificata",
		Origin: "local", Author: "test", Body: "schema: 1\nconnector: torznab\n", Hash: strings.Repeat("a", 64),
	}); err != nil {
		t.Fatal(err)
	}
	indexerID, err := env.store.CreateIndexer(ctx, store.Indexer{
		DefinitionID: "run-def", Protocol: "torrent", Slug: "run",
		Name: "Run source", Enabled: true, ConfigJSON: `{}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	runID, err := env.store.RecordIndexerHistory(ctx, store.IndexerHistory{
		IndexerID: indexerID, Kind: "run", Status: "ok", Query: "show", StartedAt: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	recorder := get(t, env.handler, "/runs/"+strconvI(runID))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	body := recorder.Body.String()
	for _, want := range []string{"Run source", "show", "ok"} {
		if !strings.Contains(body, want) {
			t.Errorf("run page missing %q", want)
		}
	}
}
