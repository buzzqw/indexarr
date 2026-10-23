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
	id, err := env.store.CreateSource(context.Background(), store.Source{
		Name: "Broken", Kind: "torznab", Manager: "auto",
		URL: "http://broken.example/api", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.store.DB().ExecContext(context.Background(),
		`UPDATE sources SET last_error = 'HTTP 500', consecutive_errors = 2 WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	token, cookie := csrfFrom(t, env.handler, "/maintenance")
	page := get(t, env.handler, "/maintenance").Body.String()
	if !strings.Contains(page, "Sources needing attention") || !strings.Contains(page, "HTTP 500") {
		t.Fatal("failed source not shown")
	}
	for _, want := range []string{
		"Database report", "Cleanup and retention", "Purge by age",
		"Purge source", "Delete dead releases", "Recount source items",
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
	source, _ := env.store.GetSource(context.Background(), id)
	if source.LastError != "" || source.ConsecutiveErrors != 0 {
		t.Errorf("source after reset = %#v", source)
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
	sourceID, err := env.store.CreateSource(ctx, store.Source{
		Name: "Cleanup", Kind: "torznab", Manager: "auto",
		URL: "http://cleanup.example/api", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	old := now.AddDate(0, 0, -10)
	makeRelease := func(key, title string, seen time.Time) store.Release {
		rel := store.Release{
			DedupKey: key, Title: title, SearchTitle: strings.ToLower(title),
			Magnet:   "magnet:?xt=urn:btih:" + strings.Repeat("a", 40),
			SourceID: sourceID, SourceName: "Cleanup", FirstSeenAt: seen, LastSeenAt: seen,
		}
		if _, err := env.store.UpsertRelease(ctx, &rel); err != nil {
			t.Fatalf("UpsertRelease: %v", err)
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
		SourceID: sourceID, SourceName: "Cleanup", FirstSeenAt: now, LastSeenAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := env.store.DB().ExecContext(ctx, `
		INSERT INTO runs(source_id, source_name, started_at, finished_at, status)
		VALUES(?, 'Cleanup', ?, ?, 'ok')`, sourceID, old.Unix(), old.Unix()); err != nil {
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
		{"csrf": {token}, "action": {"purge_source"}, "source_id": {strconv.FormatInt(sourceID, 10)}},
		{"csrf": {token}, "action": {"clear_runs_days"}, "days": {"5"}},
		{"csrf": {token}, "action": {"reset_api_usage"}},
		{"csrf": {token}, "action": {"recount_sources"}},
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
	source, err := env.store.GetSource(ctx, sourceID)
	if err != nil {
		t.Fatal(err)
	}
	if source.TotalItems != 0 {
		t.Errorf("source total after recount = %d", source.TotalItems)
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
		{"csrf": {token}, "action": {"purge_source"}, "source_id": {"0"}},
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
