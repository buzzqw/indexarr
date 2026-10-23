package legacyimport

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/buzzqw/indexarr/internal/definition"
	"github.com/buzzqw/indexarr/internal/store"
	"github.com/buzzqw/indexarr/internal/vault"
)

func newVault(t *testing.T) *vault.Vault {
	t.Helper()
	v, err := vault.New([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("vault.New: %v", err)
	}
	return v
}

func bridgedIndexer(t *testing.T, st *store.Store, sourceID int64) store.Indexer {
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

func requireDefinitionMissing(t *testing.T, st *store.Store, id string) {
	t.Helper()
	if _, err := st.GetDefinition(context.Background(), id); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("definition %q exists, want missing (err = %v)", id, err)
	}
}

func TestApplyAndRevert(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	v := newVault(t)

	sourceID := createSource(t, st, store.Source{
		Name:       "Alpha Native",
		Kind:       "torznab",
		Manager:    "jackett",
		URL:        "https://tracker.example/api?apikey=" + urlSecret + "&cat=5000",
		APIKey:     sourceAPIKey,
		Categories: "5000,2000",
		Query:      "docs",
		Enabled:    true,
	})
	if _, err := st.BackfillLegacySources(ctx); err != nil {
		t.Fatalf("BackfillLegacySources: %v", err)
	}
	bridge := bridgedIndexer(t, st, sourceID)
	bridge.Tags = "hd,4k"
	bridge.Priority = 10
	bridge.QueryLimit = 3
	bridge.GrabLimit = 4
	if err := st.UpdateIndexer(ctx, bridge); err != nil {
		t.Fatalf("UpdateIndexer tags: %v", err)
	}
	bridge = bridgedIndexer(t, st, sourceID)
	nativeID := "native-" + itoa(sourceID)

	// Dry run only reports the plan.
	beforeDefs, err := st.ListDefinitions(ctx)
	if err != nil {
		t.Fatalf("ListDefinitions: %v", err)
	}
	dry, err := Apply(ctx, st, v, ApplyOptions{SourceIDs: []int64{sourceID}, DryRun: true})
	if err != nil {
		t.Fatalf("Apply dry run: %v", err)
	}
	if len(dry) != 1 {
		t.Fatalf("dry results = %+v", dry)
	}
	if dry[0].Applied || dry[0].Skipped || dry[0].DefinitionID != nativeID ||
		dry[0].IndexerID != bridge.ID || !strings.Contains(dry[0].Reason, "dry run") {
		t.Fatalf("dry result = %+v", dry[0])
	}
	requireDefinitionMissing(t, st, nativeID)
	if got := bridgedIndexer(t, st, sourceID); got.DefinitionID != bridge.DefinitionID || got.Slug != bridge.Slug {
		t.Fatalf("dry run changed the bridge: %+v", got)
	}
	if source, err := st.GetSource(ctx, sourceID); err != nil || !source.Enabled {
		t.Fatalf("dry run changed the source: %+v, %v", source, err)
	}
	afterDefs, err := st.ListDefinitions(ctx)
	if err != nil {
		t.Fatalf("ListDefinitions after dry run: %v", err)
	}
	if len(beforeDefs) != len(afterDefs) {
		t.Fatalf("dry run wrote definitions: %d -> %d", len(beforeDefs), len(afterDefs))
	}

	// Apply.
	results, err := Apply(ctx, st, v, ApplyOptions{SourceIDs: []int64{sourceID}})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("apply results = %+v", results)
	}
	applied := results[0]
	if !applied.Applied || applied.Skipped || applied.Err != nil {
		t.Fatalf("apply result = %+v", applied)
	}
	if applied.SourceID != sourceID || applied.Name != "Alpha Native" ||
		applied.DefinitionID != nativeID || applied.IndexerID != bridge.ID {
		t.Fatalf("apply identity = %+v", applied)
	}

	// Definition.
	def, err := st.GetDefinition(ctx, nativeID)
	if err != nil {
		t.Fatalf("GetDefinition(%s): %v", nativeID, err)
	}
	if def.SchemaVersion != 1 || def.Family != "torznab" || def.State != "verificata" ||
		def.Origin != "local" || def.Author != "migration" {
		t.Fatalf("definition metadata = %+v", def)
	}
	if def.Hash != definition.Fingerprint([]byte(def.Body)) {
		t.Fatalf("definition hash = %q", def.Hash)
	}
	parsed, err := definition.Parse([]byte(def.Body))
	if err != nil {
		t.Fatalf("definition.Parse: %v\n%s", err, def.Body)
	}
	if parsed.Connector != "torznab" || parsed.Meta.ID != nativeID ||
		parsed.Meta.Name != "Alpha Native" || parsed.Meta.Protocol != "torrent" ||
		parsed.Meta.Type != "private" || len(parsed.Meta.URLs) != 1 ||
		parsed.Meta.URLs[0] != "https://tracker.example/api?cat=5000" {
		t.Fatalf("parsed definition = %+v", parsed)
	}
	if len(parsed.Settings) != 3 || parsed.Settings[0].Name != "apikey" ||
		parsed.Settings[1].Name != "username" || parsed.Settings[2].Name != "password" {
		t.Fatalf("definition settings = %+v", parsed.Settings)
	}
	if len(parsed.Categories) != 2 || parsed.Categories[0].Standard != 5000 ||
		parsed.Categories[1].Standard != 2000 ||
		parsed.Categories[0].Site != "5000" || parsed.Categories[0].Desc != "5000" {
		t.Fatalf("definition categories = %+v", parsed.Categories)
	}
	if !reflect.DeepEqual(parsed.Capabilities.Search, []string{"q"}) ||
		!reflect.DeepEqual(parsed.Capabilities.TVSearch, []string{"q", "season", "ep", "imdbid", "tvdbid", "tmdbid"}) ||
		!reflect.DeepEqual(parsed.Capabilities.MovieSearch, []string{"q", "imdbid", "tmdbid"}) {
		t.Fatalf("definition capabilities = %+v", parsed.Capabilities)
	}

	// Indexer keeps id, slug, tags, priority and limits.
	updated, err := st.GetIndexer(ctx, bridge.ID)
	if err != nil {
		t.Fatalf("GetIndexer: %v", err)
	}
	if updated.DefinitionID != nativeID || updated.LegacySourceID != 0 {
		t.Fatalf("indexer definition/legacy = %+v", updated)
	}
	if updated.Slug != bridge.Slug || updated.Name != bridge.Name || updated.Protocol != "torrent" ||
		updated.Tags != "hd,4k" || updated.Priority != 10 ||
		updated.QueryLimit != 3 || updated.GrabLimit != 4 {
		t.Fatalf("indexer fields = %+v", updated)
	}
	var config map[string]any
	if err := json.Unmarshal([]byte(updated.ConfigJSON), &config); err != nil {
		t.Fatalf("config_json: %v", err)
	}
	if config["base_url"] != "https://tracker.example/api?cat=5000" ||
		config["url"] != "https://tracker.example/api?cat=5000" ||
		config["query"] != "docs" {
		t.Fatalf("config = %v", config)
	}
	if _, exists := config["kind"]; exists {
		t.Fatalf("native config kept kind: %v", config)
	}
	categories, ok := config["categories"].([]any)
	if !ok || len(categories) != 2 {
		t.Fatalf("config categories = %v", config["categories"])
	}

	// Secrets are sealed, never plaintext.
	secrets, err := st.ListSecrets(ctx, bridge.ID)
	if err != nil {
		t.Fatalf("ListSecrets: %v", err)
	}
	if len(secrets) != 1 || secrets[0].Key != "apikey" {
		t.Fatalf("secrets = %+v", secrets)
	}
	plaintext, err := v.Open(secrets[0].Nonce, secrets[0].Ciphertext)
	if err != nil {
		t.Fatalf("vault.Open: %v", err)
	}
	if plaintext != sourceAPIKey {
		t.Fatalf("sealed secret = %q", plaintext)
	}
	for _, secret := range []string{sourceAPIKey, urlSecret, sourcePassword, urlPassword} {
		if strings.Contains(def.Body, secret) || strings.Contains(updated.ConfigJSON, secret) {
			t.Fatalf("apply leaked %q", secret)
		}
	}

	// Source disabled only after everything else.
	if source, err := st.GetSource(ctx, sourceID); err != nil || source.Enabled {
		t.Fatalf("source enabled = %+v, err = %v", source, err)
	}

	// Applying again is a strict no-op.
	sourceBefore, _ := st.GetSource(ctx, sourceID)
	defBefore, _ := st.GetDefinition(ctx, nativeID)
	indexerBefore, _ := st.GetIndexer(ctx, bridge.ID)
	again, err := Apply(ctx, st, v, ApplyOptions{SourceIDs: []int64{sourceID}})
	if err != nil {
		t.Fatalf("Apply again: %v", err)
	}
	if len(again) != 1 || again[0].Applied || !again[0].Skipped ||
		again[0].Reason != "already native" || again[0].DefinitionID != nativeID ||
		again[0].IndexerID != bridge.ID {
		t.Fatalf("second apply = %+v", again)
	}
	if sourceAfter, _ := st.GetSource(ctx, sourceID); !reflect.DeepEqual(sourceBefore, sourceAfter) {
		t.Fatalf("second apply changed the source")
	}
	if defAfter, _ := st.GetDefinition(ctx, nativeID); !reflect.DeepEqual(defBefore, defAfter) {
		t.Fatalf("second apply changed the definition")
	}
	if indexerAfter, _ := st.GetIndexer(ctx, bridge.ID); !reflect.DeepEqual(indexerBefore, indexerAfter) {
		t.Fatalf("second apply changed the indexer")
	}

	// Revert restores the bridge and enables the source.
	reverted, err := Revert(ctx, st, []int64{sourceID})
	if err != nil {
		t.Fatalf("Revert: %v", err)
	}
	if len(reverted) != 1 || !reverted[0].Applied || reverted[0].Skipped || reverted[0].Err != nil {
		t.Fatalf("revert result = %+v", reverted)
	}
	if reverted[0].DefinitionID != "legacy-"+itoa(sourceID) || reverted[0].IndexerID != bridge.ID {
		t.Fatalf("revert identity = %+v", reverted[0])
	}
	restored, err := st.GetIndexer(ctx, bridge.ID)
	if err != nil {
		t.Fatalf("GetIndexer after revert: %v", err)
	}
	if restored.DefinitionID != "legacy-"+itoa(sourceID) || restored.LegacySourceID != sourceID ||
		restored.Slug != bridge.Slug || restored.Tags != "hd,4k" {
		t.Fatalf("restored indexer = %+v", restored)
	}
	if source, err := st.GetSource(ctx, sourceID); err != nil || !source.Enabled {
		t.Fatalf("source after revert = %+v, err = %v", source, err)
	}
	requireDefinitionMissing(t, st, nativeID)
	legacyDef, err := st.GetDefinition(ctx, "legacy-"+itoa(sourceID))
	if err != nil {
		t.Fatalf("legacy definition: %v", err)
	}
	if _, err := definition.Parse([]byte(legacyDef.Body)); err != nil {
		t.Fatalf("legacy definition invalid: %v\n%s", err, legacyDef.Body)
	}

	// Reverting twice is a no-op.
	revertAgain, err := Revert(ctx, st, []int64{sourceID})
	if err != nil {
		t.Fatalf("Revert again: %v", err)
	}
	if len(revertAgain) != 1 || revertAgain[0].Applied || !revertAgain[0].Skipped ||
		revertAgain[0].Reason != "already bridged" {
		t.Fatalf("second revert = %+v", revertAgain)
	}
}

func TestApplyRequiresVaultForSecrets(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	sourceID := createSource(t, st, store.Source{
		Name:    "No Vault",
		Kind:    "torznab",
		URL:     "https://tracker.example/api",
		APIKey:  sourceAPIKey,
		Enabled: true,
	})
	if _, err := st.BackfillLegacySources(ctx); err != nil {
		t.Fatalf("BackfillLegacySources: %v", err)
	}
	bridge := bridgedIndexer(t, st, sourceID)

	results, err := Apply(ctx, st, nil, ApplyOptions{SourceIDs: []int64{sourceID}})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(results) != 1 || !results[0].Skipped || results[0].Applied || results[0].Err == nil {
		t.Fatalf("result = %+v", results[0])
	}
	if !errors.Is(results[0].Err, ErrVaultRequired) {
		t.Fatalf("error = %v, want ErrVaultRequired", results[0].Err)
	}
	for _, text := range []string{results[0].Reason, results[0].Err.Error()} {
		if !strings.Contains(text, "vault") || strings.Contains(text, sourceAPIKey) {
			t.Fatalf("bad error text %q", text)
		}
	}
	requireDefinitionMissing(t, st, "native-"+itoa(sourceID))
	kept, err := st.GetIndexer(ctx, bridge.ID)
	if err != nil {
		t.Fatalf("GetIndexer: %v", err)
	}
	if kept.DefinitionID != bridge.DefinitionID || kept.LegacySourceID != sourceID {
		t.Fatalf("bridge changed: %+v", kept)
	}
	if source, err := st.GetSource(ctx, sourceID); err != nil || !source.Enabled {
		t.Fatalf("source changed: %+v, %v", source, err)
	}
	secrets, err := st.ListSecrets(ctx, bridge.ID)
	if err != nil {
		t.Fatalf("ListSecrets: %v", err)
	}
	if len(secrets) != 0 {
		t.Fatalf("wrote secrets without a vault: %+v", secrets)
	}
}

func TestApplySkipsBasicAuth(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	v := newVault(t)

	sourceID := createSource(t, st, store.Source{
		Name:     "Basic Source",
		Kind:     "torznab",
		URL:      "https://tracker.example/api",
		Username: "user",
		Password: sourcePassword,
		Enabled:  true,
	})
	urlSourceID := createSource(t, st, store.Source{
		Name:    "URL Basic Source",
		Kind:    "torznab",
		URL:     "https://user:" + urlPassword + "@tracker.example/api",
		Enabled: true,
	})
	if _, err := st.BackfillLegacySources(ctx); err != nil {
		t.Fatalf("BackfillLegacySources: %v", err)
	}

	results, err := Apply(ctx, st, v, ApplyOptions{SourceIDs: []int64{sourceID, urlSourceID}})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %+v", results)
	}
	for _, result := range results {
		if !result.Skipped || result.Applied || result.Err != nil {
			t.Fatalf("basic result = %+v", result)
		}
		if !strings.Contains(strings.ToLower(result.Reason), "basic") {
			t.Fatalf("basic reason = %q", result.Reason)
		}
		if strings.Contains(result.Reason, sourcePassword) || strings.Contains(result.Reason, urlPassword) {
			t.Fatalf("reason leaked credentials: %q", result.Reason)
		}
		requireDefinitionMissing(t, st, "native-"+itoa(result.SourceID))
		bridge := bridgedIndexer(t, st, result.SourceID)
		if bridge.LegacySourceID != result.SourceID {
			t.Fatalf("bridge changed: %+v", bridge)
		}
		if source, err := st.GetSource(ctx, result.SourceID); err != nil || !source.Enabled {
			t.Fatalf("source changed: %+v, %v", source, err)
		}
	}
}

func TestApplySkipsIneligibleSources(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	v := newVault(t)

	rssID := createSource(t, st, store.Source{Name: "Feed", Kind: "rss", URL: "https://feed.example/rss", Enabled: true})
	ftpID := createSource(t, st, store.Source{Name: "FTP", Kind: "torznab", URL: "ftp://tracker.example/api", Enabled: true})
	if _, err := st.BackfillLegacySources(ctx); err != nil {
		t.Fatalf("BackfillLegacySources: %v", err)
	}
	unbridgedID := createSource(t, st, store.Source{Name: "New", Kind: "torznab", URL: "https://new.example/api", Enabled: true})

	results, err := Apply(ctx, st, v, ApplyOptions{})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("results = %+v", results)
	}
	byID := make(map[int64]ApplyResult, len(results))
	for _, result := range results {
		byID[result.SourceID] = result
		if result.Applied || !result.Skipped || result.Err != nil {
			t.Fatalf("ineligible result = %+v", result)
		}
		if result.Reason == "" {
			t.Fatalf("missing reason for source %d", result.SourceID)
		}
	}
	if !strings.Contains(strings.ToLower(byID[rssID].Reason), "torznab") {
		t.Errorf("rss reason = %q", byID[rssID].Reason)
	}
	if !strings.Contains(byID[ftpID].Reason, "http or https") {
		t.Errorf("ftp reason = %q", byID[ftpID].Reason)
	}
	if !strings.Contains(byID[unbridgedID].Reason, "bridge") {
		t.Errorf("unbridged reason = %q", byID[unbridgedID].Reason)
	}

	missing, err := Apply(ctx, st, v, ApplyOptions{SourceIDs: []int64{99999}})
	if err != nil {
		t.Fatalf("Apply unknown: %v", err)
	}
	if len(missing) != 1 || missing[0].SourceID != 99999 || !missing[0].Skipped ||
		missing[0].Err == nil || missing[0].Reason != "source not found" {
		t.Fatalf("unknown source result = %+v", missing)
	}
}

func TestApplyInvalidDefinitionLeavesBridge(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	v := newVault(t)

	sourceID := createSource(t, st, store.Source{
		Name:    "", // meta.name is required, so definition.Parse rejects the body
		Kind:    "torznab",
		URL:     "https://tracker.example/api",
		APIKey:  sourceAPIKey,
		Enabled: true,
	})
	if _, err := st.BackfillLegacySources(ctx); err != nil {
		t.Fatalf("BackfillLegacySources: %v", err)
	}
	bridge := bridgedIndexer(t, st, sourceID)

	results, err := Apply(ctx, st, v, ApplyOptions{SourceIDs: []int64{sourceID}})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(results) != 1 || results[0].Applied || !results[0].Skipped || results[0].Err == nil {
		t.Fatalf("result = %+v", results[0])
	}
	requireDefinitionMissing(t, st, "native-"+itoa(sourceID))
	kept, err := st.GetIndexer(ctx, bridge.ID)
	if err != nil {
		t.Fatalf("GetIndexer: %v", err)
	}
	if kept.DefinitionID != bridge.DefinitionID || kept.LegacySourceID != sourceID {
		t.Fatalf("bridge changed: %+v", kept)
	}
	if source, err := st.GetSource(ctx, sourceID); err != nil || !source.Enabled {
		t.Fatalf("source changed: %+v, %v", source, err)
	}
}

func TestApplyRollsBackOnIndexerFailure(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	v := newVault(t)

	sourceID := createSource(t, st, store.Source{
		Name:    "Rollback",
		Kind:    "torznab",
		URL:     "https://tracker.example/api",
		APIKey:  sourceAPIKey,
		Enabled: true,
	})
	if _, err := st.BackfillLegacySources(ctx); err != nil {
		t.Fatalf("BackfillLegacySources: %v", err)
	}
	bridge := bridgedIndexer(t, st, sourceID)

	// Force the in-place indexer update to fail after the definition was
	// written, so the compensation path runs.
	if _, err := st.DB().ExecContext(ctx, `
		CREATE TRIGGER fail_native_update BEFORE UPDATE ON indexers
		WHEN NEW.definition_id LIKE 'native-%'
		BEGIN
			SELECT RAISE(ABORT, 'forced native update failure');
		END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	t.Cleanup(func() {
		_, _ = st.DB().ExecContext(context.Background(), `DROP TRIGGER IF EXISTS fail_native_update`)
	})

	results, err := Apply(ctx, st, v, ApplyOptions{SourceIDs: []int64{sourceID}})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(results) != 1 || results[0].Applied || !results[0].Skipped || results[0].Err == nil {
		t.Fatalf("result = %+v", results[0])
	}
	if !strings.Contains(results[0].Reason, "forced native update failure") {
		t.Fatalf("reason = %q", results[0].Reason)
	}
	requireDefinitionMissing(t, st, "native-"+itoa(sourceID))
	kept, err := st.GetIndexer(ctx, bridge.ID)
	if err != nil {
		t.Fatalf("GetIndexer: %v", err)
	}
	if kept.DefinitionID != bridge.DefinitionID || kept.LegacySourceID != sourceID ||
		kept.ConfigJSON != bridge.ConfigJSON {
		t.Fatalf("bridge was not restored: %+v", kept)
	}
	if source, err := st.GetSource(ctx, sourceID); err != nil || !source.Enabled {
		t.Fatalf("source was not restored: %+v, %v", source, err)
	}
	secrets, err := st.ListSecrets(ctx, bridge.ID)
	if err != nil || len(secrets) != 0 {
		t.Fatalf("rollback left secrets: %+v, %v", secrets, err)
	}
}

func TestRevertCreatesMissingLegacyDefinition(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	v := newVault(t)

	sourceID := createSource(t, st, store.Source{
		Name:    "Revert Me",
		Kind:    "torznab",
		URL:     "https://tracker.example/api?apikey=" + urlSecret,
		APIKey:  sourceAPIKey,
		Enabled: true,
	})
	if _, err := st.BackfillLegacySources(ctx); err != nil {
		t.Fatalf("BackfillLegacySources: %v", err)
	}
	if _, err := Apply(ctx, st, v, ApplyOptions{SourceIDs: []int64{sourceID}}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	// The store exposes no DeleteDefinition method; the test removes the row
	// to exercise the recreate path of Revert.
	if _, err := st.DB().ExecContext(ctx, `DELETE FROM definitions WHERE id = ?`, "legacy-"+itoa(sourceID)); err != nil {
		t.Fatalf("delete legacy definition: %v", err)
	}

	results, err := Revert(ctx, st, []int64{sourceID})
	if err != nil {
		t.Fatalf("Revert: %v", err)
	}
	if len(results) != 1 || !results[0].Applied {
		t.Fatalf("revert = %+v", results)
	}
	def, err := st.GetDefinition(ctx, "legacy-"+itoa(sourceID))
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	parsed, err := definition.Parse([]byte(def.Body))
	if err != nil {
		t.Fatalf("definition.Parse: %v\n%s", err, def.Body)
	}
	if parsed.Connector != "torznab" || parsed.Meta.ID != "legacy-"+itoa(sourceID) {
		t.Fatalf("legacy definition = %+v", parsed)
	}
	if strings.Contains(def.Body, urlSecret) {
		t.Fatalf("recreated legacy definition leaked the URL key")
	}
}

func TestApplyRequiresStore(t *testing.T) {
	if _, err := Apply(context.Background(), nil, nil, ApplyOptions{}); err == nil {
		t.Fatal("Apply(nil) succeeded, want error")
	}
	if _, err := Revert(context.Background(), nil, []int64{1}); err == nil {
		t.Fatal("Revert(nil) succeeded, want error")
	}
	empty, err := Revert(context.Background(), newStore(t), nil)
	if err != nil {
		t.Fatalf("Revert(empty): %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("Revert(empty) = %+v", empty)
	}
}

func TestRedactSecrets(t *testing.T) {
	text := redactSecrets("failed with "+sourceAPIKey+" and "+url.QueryEscape(sourcePassword), []string{
		sourceAPIKey, sourcePassword, "ab",
	})
	if strings.Contains(text, sourceAPIKey) || strings.Contains(text, sourcePassword) {
		t.Fatalf("secrets survived: %q", text)
	}
	if !strings.Contains(text, "[redacted]") {
		t.Fatalf("no redaction marker: %q", text)
	}
	if got := redactSecrets("ab is short", []string{"ab"}); got != "ab is short" {
		t.Fatalf("short secret was replaced: %q", got)
	}
}

func itoa(value int64) string {
	return strconv.FormatInt(value, 10)
}
