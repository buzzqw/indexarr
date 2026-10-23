package adminapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/buzzqw/indexarr/internal/definition"
	"github.com/buzzqw/indexarr/internal/store"
)

const (
	legacyAdminKey      = "legacy-admin-API-KEY-SECRET"
	legacyAdminPassword = "legacy-admin-PASSWORD-SECRET"
	legacyAdminURLKey   = "legacy-admin-URL-KEY-SECRET"
)

type legacyActionResult struct {
	SourceID     int64  `json:"source_id"`
	Name         string `json:"name"`
	DefinitionID string `json:"definition_id"`
	IndexerID    int64  `json:"indexer_id"`
	Applied      bool   `json:"applied"`
	Skipped      bool   `json:"skipped"`
	Reason       string `json:"reason"`
	Error        string `json:"error"`
}

type legacyActionTestResponse struct {
	DryRun  bool                 `json:"dry_run"`
	Results []legacyActionResult `json:"results"`
}

func newLegacyEnv(t *testing.T, withVault bool) (*env, int64) {
	t.Helper()
	e := newEnv(t, true, withVault)
	id, err := e.store.CreateSource(context.Background(), store.Source{
		Name:       "Alpha Legacy",
		Kind:       "torznab",
		Manager:    "jackett",
		URL:        "https://tracker.example/api?apikey=" + legacyAdminURLKey,
		APIKey:     legacyAdminKey,
		Categories: "5000,2000",
		Query:      "docs",
		Enabled:    true,
	})
	if err != nil {
		t.Fatalf("CreateSource: %v", err)
	}
	if _, err := e.store.BackfillLegacySources(context.Background()); err != nil {
		t.Fatalf("BackfillLegacySources: %v", err)
	}
	return e, id
}

func legacyIndexerFor(t *testing.T, st *store.Store, sourceID int64) store.Indexer {
	t.Helper()
	indexers, err := st.ListIndexers(context.Background())
	if err != nil {
		t.Fatalf("ListIndexers: %v", err)
	}
	for _, in := range indexers {
		if in.LegacySourceID == sourceID {
			return in
		}
	}
	t.Fatalf("no bridged indexer for source %d", sourceID)
	return store.Indexer{}
}

func assertNoDefinition(t *testing.T, st *store.Store, id string) {
	t.Helper()
	if _, err := st.GetDefinition(context.Background(), id); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("definition %q exists, want missing (err = %v)", id, err)
	}
}

func TestLegacyApplyAndRevertEndpoints(t *testing.T) {
	e, sourceID := newLegacyEnv(t, true)
	ctx := context.Background()
	bridge := legacyIndexerFor(t, e.store, sourceID)
	nativeID := "native-" + strconv.FormatInt(sourceID, 10)
	legacyID := "legacy-" + strconv.FormatInt(sourceID, 10)

	var bodies [][]byte

	// Dry run.
	res := e.do(http.MethodPost, "/api/v2/legacy/apply",
		jsonBody(map[string]any{"source_ids": []int64{sourceID}, "dry_run": true}))
	requireStatus(t, res, http.StatusOK)
	bodies = append(bodies, res.body)
	dry := decodeJSON[legacyActionTestResponse](t, res.body)
	if !dry.DryRun || len(dry.Results) != 1 {
		t.Fatalf("dry response = %+v", dry)
	}
	if dry.Results[0].Applied || dry.Results[0].Skipped ||
		dry.Results[0].DefinitionID != nativeID || dry.Results[0].IndexerID != bridge.ID {
		t.Fatalf("dry result = %+v", dry.Results[0])
	}
	assertNoDefinition(t, e.store, nativeID)
	if source, err := e.store.GetSource(ctx, sourceID); err != nil || !source.Enabled {
		t.Fatalf("dry run changed the source: %+v, %v", source, err)
	}

	// Apply.
	res = e.do(http.MethodPost, "/api/v2/legacy/apply",
		jsonBody(map[string]any{"source_ids": []int64{sourceID}}))
	requireStatus(t, res, http.StatusOK)
	bodies = append(bodies, res.body)
	applied := decodeJSON[legacyActionTestResponse](t, res.body)
	if applied.DryRun || len(applied.Results) != 1 {
		t.Fatalf("apply response = %+v", applied)
	}
	result := applied.Results[0]
	if !result.Applied || result.Skipped || result.Error != "" ||
		result.DefinitionID != nativeID || result.IndexerID != bridge.ID {
		t.Fatalf("apply result = %+v", result)
	}

	def, err := e.store.GetDefinition(ctx, nativeID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	if _, err := definition.Parse([]byte(def.Body)); err != nil {
		t.Fatalf("stored definition is invalid: %v", err)
	}
	updated, err := e.store.GetIndexer(ctx, bridge.ID)
	if err != nil {
		t.Fatalf("GetIndexer: %v", err)
	}
	if updated.DefinitionID != nativeID || updated.LegacySourceID != 0 {
		t.Fatalf("indexer = %+v", updated)
	}
	if source, err := e.store.GetSource(ctx, sourceID); err != nil || source.Enabled {
		t.Fatalf("source after apply = %+v, %v", source, err)
	}
	secrets, err := e.store.ListSecrets(ctx, bridge.ID)
	if err != nil || len(secrets) != 1 {
		t.Fatalf("secrets = %+v, %v", secrets, err)
	}
	plaintext, err := e.vault.Open(secrets[0].Nonce, secrets[0].Ciphertext)
	if err != nil || plaintext != legacyAdminKey {
		t.Fatalf("sealed secret = %q, %v", plaintext, err)
	}

	// Second apply is a no-op.
	res = e.do(http.MethodPost, "/api/v2/legacy/apply",
		jsonBody(map[string]any{"source_ids": []int64{sourceID}}))
	requireStatus(t, res, http.StatusOK)
	bodies = append(bodies, res.body)
	again := decodeJSON[legacyActionTestResponse](t, res.body)
	if len(again.Results) != 1 || again.Results[0].Applied || !again.Results[0].Skipped ||
		again.Results[0].Reason != "already native" {
		t.Fatalf("second apply = %+v", again)
	}

	// Preview keeps working.
	res = e.do(http.MethodGet, "/api/v2/legacy/preview", "")
	requireStatus(t, res, http.StatusOK)
	bodies = append(bodies, res.body)

	// Revert.
	res = e.do(http.MethodPost, "/api/v2/legacy/revert",
		jsonBody(map[string]any{"source_ids": []int64{sourceID}}))
	requireStatus(t, res, http.StatusOK)
	bodies = append(bodies, res.body)
	reverted := decodeJSON[legacyActionTestResponse](t, res.body)
	if len(reverted.Results) != 1 || !reverted.Results[0].Applied ||
		reverted.Results[0].DefinitionID != legacyID {
		t.Fatalf("revert response = %+v", reverted)
	}
	restored, err := e.store.GetIndexer(ctx, bridge.ID)
	if err != nil {
		t.Fatalf("GetIndexer after revert: %v", err)
	}
	if restored.DefinitionID != legacyID || restored.LegacySourceID != sourceID {
		t.Fatalf("restored indexer = %+v", restored)
	}
	if source, err := e.store.GetSource(ctx, sourceID); err != nil || !source.Enabled {
		t.Fatalf("source after revert = %+v, %v", source, err)
	}
	assertNoDefinition(t, e.store, nativeID)

	// Revert again is a no-op.
	res = e.do(http.MethodPost, "/api/v2/legacy/revert",
		jsonBody(map[string]any{"source_ids": []int64{sourceID}}))
	requireStatus(t, res, http.StatusOK)
	bodies = append(bodies, res.body)
	revertAgain := decodeJSON[legacyActionTestResponse](t, res.body)
	if len(revertAgain.Results) != 1 || revertAgain.Results[0].Applied ||
		!revertAgain.Results[0].Skipped {
		t.Fatalf("second revert = %+v", revertAgain)
	}

	for _, body := range bodies {
		for _, secret := range []string{legacyAdminKey, legacyAdminPassword, legacyAdminURLKey} {
			if strings.Contains(string(body), secret) {
				t.Fatalf("endpoint leaked %q: %s", secret, body)
			}
		}
	}
}

func TestLegacyApplyEndpointWithoutVault(t *testing.T) {
	e, sourceID := newLegacyEnv(t, false)
	ctx := context.Background()
	bridge := legacyIndexerFor(t, e.store, sourceID)

	res := e.do(http.MethodPost, "/api/v2/legacy/apply",
		jsonBody(map[string]any{"source_ids": []int64{sourceID}}))
	requireStatus(t, res, http.StatusServiceUnavailable)
	body := decodeJSON[map[string]string](t, res.body)
	if !strings.Contains(body["error"], "vault") {
		t.Fatalf("response = %+v", body)
	}
	assertNoDefinition(t, e.store, "native-"+strconv.FormatInt(sourceID, 10))
	kept, err := e.store.GetIndexer(ctx, bridge.ID)
	if err != nil {
		t.Fatalf("GetIndexer: %v", err)
	}
	if kept.DefinitionID != bridge.DefinitionID || kept.LegacySourceID != sourceID {
		t.Fatalf("bridge changed: %+v", kept)
	}
	if source, err := e.store.GetSource(ctx, sourceID); err != nil || !source.Enabled {
		t.Fatalf("source changed: %+v, %v", source, err)
	}
	secrets, err := e.store.ListSecrets(ctx, bridge.ID)
	if err != nil || len(secrets) != 0 {
		t.Fatalf("secrets = %+v, %v", secrets, err)
	}
	if strings.Contains(string(res.body), legacyAdminKey) {
		t.Fatalf("503 body leaked the API key: %s", res.body)
	}
}

func TestLegacyApplyAppliesEverySourceAndReportsSkips(t *testing.T) {
	e, sourceID := newLegacyEnv(t, true)
	rssID, err := e.store.CreateSource(context.Background(), store.Source{
		Name: "Feed", Kind: "rss", URL: "https://feed.example/rss", Enabled: true,
	})
	if err != nil {
		t.Fatalf("CreateSource rss: %v", err)
	}
	if _, err := e.store.BackfillLegacySources(context.Background()); err != nil {
		t.Fatalf("BackfillLegacySources: %v", err)
	}

	res := e.do(http.MethodPost, "/api/v2/legacy/apply", jsonBody(map[string]any{}))
	requireStatus(t, res, http.StatusOK)
	body := decodeJSON[legacyActionTestResponse](t, res.body)
	if len(body.Results) != 2 {
		t.Fatalf("results = %+v", body.Results)
	}
	byID := make(map[int64]legacyActionResult, len(body.Results))
	for _, result := range body.Results {
		byID[result.SourceID] = result
	}
	if !byID[sourceID].Applied {
		t.Errorf("torznab result = %+v", byID[sourceID])
	}
	if !byID[rssID].Skipped || byID[rssID].Applied || byID[rssID].Reason == "" {
		t.Errorf("rss result = %+v", byID[rssID])
	}
}

func TestLegacyActionBadRequests(t *testing.T) {
	e, sourceID := newLegacyEnv(t, true)

	cases := []struct {
		name   string
		path   string
		body   string
		status int
	}{
		{"apply invalid json", "/api/v2/legacy/apply", "{", http.StatusBadRequest},
		{"apply unknown field", "/api/v2/legacy/apply", `{"nope":true}`, http.StatusBadRequest},
		{"apply zero id", "/api/v2/legacy/apply", `{"source_ids":[0]}`, http.StatusBadRequest},
		{"apply negative id", "/api/v2/legacy/apply", `{"source_ids":[-3]}`, http.StatusBadRequest},
		{"revert missing ids", "/api/v2/legacy/revert", `{}`, http.StatusBadRequest},
		{"revert empty ids", "/api/v2/legacy/revert", `{"source_ids":[]}`, http.StatusBadRequest},
		{"revert zero id", "/api/v2/legacy/revert", `{"source_ids":[0]}`, http.StatusBadRequest},
		{"revert invalid json", "/api/v2/legacy/revert", "not-json", http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := e.do(http.MethodPost, tc.path, tc.body)
			requireStatus(t, res, tc.status)
			if body := decodeJSON[map[string]string](t, res.body); body["error"] == "" {
				t.Fatalf("missing error message: %s", res.body)
			}
		})
	}

	// A valid apply body still works after the rejected requests.
	res := e.do(http.MethodPost, "/api/v2/legacy/apply",
		jsonBody(map[string]any{"source_ids": []int64{sourceID}, "dry_run": true}))
	requireStatus(t, res, http.StatusOK)
}

func TestLegacyApplyUnknownSourceEndpoint(t *testing.T) {
	e, _ := newLegacyEnv(t, true)
	res := e.do(http.MethodPost, "/api/v2/legacy/apply",
		jsonBody(map[string]any{"source_ids": []int64{424242}}))
	requireStatus(t, res, http.StatusOK)
	body := decodeJSON[legacyActionTestResponse](t, res.body)
	if len(body.Results) != 1 || body.Results[0].SourceID != 424242 ||
		body.Results[0].Applied || body.Results[0].Error == "" {
		t.Fatalf("response = %+v", body)
	}
	// JSON must always be valid, including the unknown-source error path.
	var raw map[string]any
	if err := json.Unmarshal(res.body, &raw); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
}
