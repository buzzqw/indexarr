package adminapi

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/buzzqw/indexarr/internal/store"
)

func TestProwlarrCompatibilityReadOnly(t *testing.T) {
	e := newEnv(t, true, true)
	body := `schema: 1
meta:
  id: prowlarr-test
  name: Test site
  type: public
  protocol: torrent
  urls:
    - https://example.invalid/
categories:
  - { site: "1", standard: 2000, desc: Movies }
capabilities:
  search: [q]
search:
  request:
    get: /
  response:
    type: html
    rows: { selector: "tr" }
    fields:
      title: { selector: "a" }
health:
  query: test
  min_rows: 0
`
	if err := e.store.UpsertDefinition(context.Background(), store.Definition{
		ID: "prowlarr-test", SchemaVersion: 1, State: "verificata", Origin: "local",
		Author: "test", Body: body, Hash: "prowlarr-test-hash",
	}); err != nil {
		t.Fatalf("UpsertDefinition: %v", err)
	}
	id, err := e.store.CreateIndexer(context.Background(), store.Indexer{
		DefinitionID: "prowlarr-test", Protocol: "torrent", Slug: "test-site",
		Name: "Test site", Enabled: true, Priority: 7,
		ConfigJSON: `{"categories":"2000"}`, Tags: "",
	})
	if err != nil {
		t.Fatalf("CreateIndexer: %v", err)
	}

	status := e.do(http.MethodGet, "/api/v1/system/status", "")
	requireStatus(t, status, http.StatusOK)
	if !strings.Contains(string(status.body), `"appName":"indexarr"`) {
		t.Fatalf("status = %s", status.body)
	}

	health := e.do(http.MethodGet, "/api/v1/health", "")
	requireStatus(t, health, http.StatusOK)
	if strings.TrimSpace(string(health.body)) != "[]" {
		t.Fatalf("health = %s", health.body)
	}

	list := e.do(http.MethodGet, "/api/v1/indexer", "")
	requireStatus(t, list, http.StatusOK)
	rows := decodeJSON[[]prowlarrIndexer](t, list.body)
	if len(rows) != 1 || rows[0].Name != "Test site" || rows[0].Priority != 7 {
		t.Fatalf("indexers = %+v", rows)
	}
	if rows[0].Implementation != "Torznab" || !rows[0].Enable {
		t.Fatalf("indexer = %+v", rows[0])
	}
	var baseURL string
	for _, field := range rows[0].Fields {
		if field.Name == "baseUrl" {
			baseURL, _ = field.Value.(string)
		}
	}
	if !strings.Contains(baseURL, "/torznab/test-site") {
		t.Fatalf("baseUrl = %q", baseURL)
	}
	if len(rows[0].Capabilities.Categories) != 1 || rows[0].Capabilities.Categories[0].ID != 2000 {
		t.Fatalf("categories = %+v", rows[0].Capabilities.Categories)
	}

	one := e.do(http.MethodGet, "/api/v1/indexer/"+itoa64(id), "")
	requireStatus(t, one, http.StatusOK)
	if !strings.Contains(string(one.body), `"name":"Test site"`) {
		t.Fatalf("indexer get = %s", one.body)
	}

	unauthorized := e.call(http.MethodGet, "/api/v1/indexer", "", "")
	if unauthorized.status != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", unauthorized.status)
	}
}

// TestProwlarrIndexerStatusListsOnlyFailures mirrors Prowlarr's
// /api/v1/indexerstatus: a healthy source is not listed, a source whose last
// run failed is listed with the message and the time it will be retried, so a
// dashboard (or gextto) can skip it instead of waiting for a timeout.
func TestProwlarrIndexerStatusListsOnlyFailures(t *testing.T) {
	e := newEnv(t, true, true)
	ctx := context.Background()

	body := `schema: 1
meta:
  id: status-test
  name: Status site
  type: public
  protocol: torrent
  urls:
    - https://example.invalid/
capabilities:
  search: [q]
search:
  request:
    get: /
  response:
    type: html
    rows: { selector: "tr" }
    fields:
      title: { selector: "a" }
`
	if err := e.store.UpsertDefinition(ctx, store.Definition{
		ID: "status-test", SchemaVersion: 1, State: "verificata", Origin: "local",
		Author: "test", Body: body, Hash: "status-test-hash",
	}); err != nil {
		t.Fatalf("UpsertDefinition: %v", err)
	}
	healthy, err := e.store.CreateIndexer(ctx, store.Indexer{
		DefinitionID: "status-test", Protocol: "torrent", Slug: "healthy",
		Name: "Healthy", Enabled: true, ConfigJSON: `{}`,
	})
	if err != nil {
		t.Fatalf("CreateIndexer healthy: %v", err)
	}
	failing, err := e.store.CreateIndexer(ctx, store.Indexer{
		DefinitionID: "status-test", Protocol: "torrent", Slug: "failing",
		Name: "Failing", Enabled: true, ConfigJSON: `{}`,
	})
	if err != nil {
		t.Fatalf("CreateIndexer failing: %v", err)
	}
	until := time.Date(2026, time.November, 1, 12, 30, 0, 0, time.UTC)
	if err := e.store.UpsertIndexerState(ctx, store.IndexerState{
		IndexerID:     failing,
		DisabledUntil: until,
		LastError:     "search returned HTTP 503",
	}); err != nil {
		t.Fatalf("SetIndexerState: %v", err)
	}

	res := e.do(http.MethodGet, "/api/v1/indexerstatus", "")
	requireStatus(t, res, http.StatusOK)
	rows := decodeJSON[[]prowlarrIndexerStatus](t, res.body)
	if len(rows) != 1 {
		t.Fatalf("indexerstatus = %+v, want only the failing source", rows)
	}
	if rows[0].IndexerID != failing {
		t.Fatalf("indexerId = %d, want %d (healthy = %d)", rows[0].IndexerID, failing, healthy)
	}
	if rows[0].Message != "search returned HTTP 503" {
		t.Errorf("message = %q", rows[0].Message)
	}
	// Prowlarr's resource keeps timestamps in these two fields, so a client
	// that parses them as dates must not receive a sentence.
	if _, err := time.Parse(time.RFC3339, rows[0].MostRecentFailure); err != nil {
		t.Errorf("mostRecentFailure = %q, want a timestamp", rows[0].MostRecentFailure)
	}
	if rows[0].DisabledTill != until.Format(time.RFC3339) {
		t.Errorf("disabledTill = %q, want %q", rows[0].DisabledTill, until.Format(time.RFC3339))
	}

	// A successful run clears the message, and the source leaves the list.
	if err := e.store.UpsertIndexerState(ctx, store.IndexerState{IndexerID: failing}); err != nil {
		t.Fatalf("SetIndexerState clear: %v", err)
	}
	res = e.do(http.MethodGet, "/api/v1/indexerstatus", "")
	requireStatus(t, res, http.StatusOK)
	if rows := decodeJSON[[]prowlarrIndexerStatus](t, res.body); len(rows) != 0 {
		t.Fatalf("indexerstatus after recovery = %+v, want none", rows)
	}
}
