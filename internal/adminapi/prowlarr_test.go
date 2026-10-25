package adminapi

import (
	"context"
	"net/http"
	"strings"
	"testing"

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
