package web

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/buzzqw/indexarr/internal/store"
)

func TestMaintenanceActions(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	if err := env.store.UpsertDefinition(ctx, store.Definition{
		ID: "maint-def", SchemaVersion: 1, Family: "torznab", State: "verificata",
		Origin: "local", Author: "test", Body: "schema: 1\nconnector: torznab\n", Hash: strings.Repeat("a", 64),
	}); err != nil {
		t.Fatal(err)
	}
	id, err := env.store.CreateIndexer(ctx, store.Indexer{
		DefinitionID: "maint-def", Protocol: "torrent", Slug: "broken",
		Name: "Broken", Enabled: true, ConfigJSON: `{}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := env.store.UpsertIndexerState(ctx, store.IndexerState{
		IndexerID: id, Circuit: "open", LastError: "HTTP 500",
	}); err != nil {
		t.Fatal(err)
	}
	token, cookie := csrfFrom(t, env.handler, "/maintenance")
	page := get(t, env.handler, "/maintenance").Body.String()
	for _, want := range []string{
		"Database report", "Cleanup and retention", "Purge by age",
		"Purge indexer", "Delete dead releases",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("maintenance page missing %q", want)
		}
	}
	for _, action := range []string{
		"integrity", "quick_check", "foreign_keys", "optimize",
		"checkpoint", "analyze", "reindex", "reset_errors",
	} {
		recorder := postForm(t, env.handler, "/maintenance", url.Values{
			"csrf":   {token},
			"action": {action},
		}, cookie)
		if recorder.Code != http.StatusSeeOther {
			t.Fatalf("%s status = %d: %s", action, recorder.Code, recorder.Body.String())
		}
	}
	state, _ := env.store.GetIndexerState(ctx, id)
	if state.LastError != "" || state.Circuit != "closed" {
		t.Errorf("state after reset = %#v", state)
	}
}

func TestServiceWorkerAndManifest(t *testing.T) {
	env := newTestEnv(t)
	sw := get(t, env.handler, "/sw.js")
	if sw.Code != http.StatusOK || !strings.Contains(sw.Body.String(), "indexarr-static") {
		t.Fatalf("sw = %d", sw.Code)
	}
	if contentType := sw.Header().Get("Content-Type"); !strings.Contains(contentType, "javascript") {
		t.Errorf("sw content type = %q", contentType)
	}
	manifest := get(t, env.handler, "/static/manifest.webmanifest")
	if manifest.Code != http.StatusOK || !strings.Contains(manifest.Body.String(), `"name": "indexarr"`) {
		t.Fatalf("manifest = %d", manifest.Code)
	}
	page := get(t, env.handler, "/").Body.String()
	if !strings.Contains(page, `rel="manifest"`) || !strings.Contains(page, `/static/icon.svg`) {
		t.Error("PWA links missing from layout")
	}
}

func TestMaintenanceCleanupActions(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	if err := env.store.UpsertDefinition(ctx, store.Definition{
		ID: "cleanup-def", SchemaVersion: 1, Family: "torznab", State: "verificata",
		Origin: "local", Author: "test", Body: "schema: 1\nconnector: torznab\n", Hash: strings.Repeat("a", 64),
	}); err != nil {
		t.Fatal(err)
	}
	indexerID, err := env.store.CreateIndexer(ctx, store.Indexer{
		DefinitionID: "cleanup-def", Protocol: "torrent", Slug: "cleanup",
		Name: "Cleanup", Enabled: true, ConfigJSON: `{}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	old := now.AddDate(0, 0, -10)
	makeRelease := func(key, title string, seen time.Time) store.Release {
		rel := store.Release{
			DedupKey: key, Title: title, SearchTitle: strings.ToLower(title),
			Magnet:      "magnet:?xt=urn:btih:" + strings.Repeat("a", 40),
			FirstSeenAt: seen, LastSeenAt: seen,
		}
		if _, err := env.store.UpsertRelease(ctx, &rel); err != nil {
			t.Fatalf("UpsertRelease: %v", err)
		}
		if err := env.store.RecordReleaseSource(ctx, store.ReleaseSource{
			ReleaseID: rel.ID, IndexerID: indexerID,
			FirstSeenAt: seen, LastSeenAt: seen,
		}); err != nil {
			t.Fatalf("RecordReleaseSource: %v", err)
		}
		return rel
	}
	makeRelease("current", "Current", now)
	makeRelease("old", "Old", old)
	hidden := makeRelease("hidden", "Hidden", now)
	if _, err := env.store.SetReleaseFlags(ctx, hidden.ID, false, true); err != nil {
		t.Fatal(err)
	}
	if _, err := env.store.UpsertRelease(ctx, &store.Release{
		DedupKey: "dead", Title: "Dead", SearchTitle: "dead",
		FirstSeenAt: now, LastSeenAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := env.store.RecordIndexerHistory(ctx, store.IndexerHistory{
		IndexerID: indexerID, Kind: "run", Status: "ok", StartedAt: old,
	}); err != nil {
		t.Fatal(err)
	}
	key, err := env.store.AddAPIKey(ctx, "QA client")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.store.DB().ExecContext(ctx,
		`UPDATE api_keys SET requests = 5, last_used_at = ? WHERE id = ?`, now.Unix(), key.ID); err != nil {
		t.Fatal(err)
	}

	token, cookie := csrfFrom(t, env.handler, "/maintenance")
	actions := []url.Values{
		{"csrf": {token}, "action": {"purge_no_links"}},
		{"csrf": {token}, "action": {"purge_hidden"}},
		{"csrf": {token}, "action": {"purge_days"}, "days": {"5"}},
		{"csrf": {token}, "action": {"purge_indexer"}, "indexer_id": {strconv.FormatInt(indexerID, 10)}},
		{"csrf": {token}, "action": {"clear_runs_days"}, "days": {"5"}},
		{"csrf": {token}, "action": {"reset_api_usage"}},
	}
	for _, values := range actions {
		recorder := postForm(t, env.handler, "/maintenance", values, cookie)
		if recorder.Code != http.StatusSeeOther {
			t.Fatalf("%s status = %d: %s", values.Get("action"), recorder.Code, recorder.Body.String())
		}
	}

	stats, err := env.store.LoadStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Releases != 0 {
		t.Errorf("releases after cleanup = %d", stats.Releases)
	}
	keys, err := env.store.ListAPIKeys(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 || keys[0].Requests != 0 || !keys[0].LastUsedAt.IsZero() {
		t.Errorf("API key after reset = %#v", keys)
	}
}

func TestMaintenanceRejectsInvalidCleanupInput(t *testing.T) {
	env := newTestEnv(t)
	token, cookie := csrfFrom(t, env.handler, "/maintenance")
	for _, values := range []url.Values{
		{"csrf": {token}, "action": {"purge_days"}, "days": {"0"}},
		{"csrf": {token}, "action": {"clear_runs_days"}, "days": {"-1"}},
		{"csrf": {token}, "action": {"purge_indexer"}, "indexer_id": {"0"}},
	} {
		recorder := postForm(t, env.handler, "/maintenance", values, cookie)
		if recorder.Code != http.StatusSeeOther {
			t.Fatalf("%s status = %d: %s", values.Get("action"), recorder.Code, recorder.Body.String())
		}
		location := recorder.Header().Get("Location")
		if !strings.Contains(location, "err=") {
			t.Errorf("%s invalid input accepted: %q", values.Get("action"), location)
		}
	}
}
