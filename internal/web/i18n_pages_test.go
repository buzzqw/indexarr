package web

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/buzzqw/indexarr/internal/store"
)

func TestLegacyPagesRenderAllLanguages(t *testing.T) {
	env := newTestEnv(t)
	rel := store.Release{
		DedupKey: "i18n-page", Title: "I18N Page Release", SearchTitle: "i18n page release",
		Category: "5040", Protocol: "torrent", PublishedAt: time.Now(),
		FirstSeenAt: time.Now(), LastSeenAt: time.Now(),
	}
	if _, err := env.store.UpsertRelease(context.Background(), &rel); err != nil {
		t.Fatal(err)
	}
	paths := []string{
		"/releases/" + strconv.FormatInt(rel.ID, 10),
		"/rules",
		"/settings?section=general",
		"/settings?section=maintenance",
		"/indexers/new",
	}
	languages := []string{"en", "it", "de", "fr", "es", "pl", "zh"}
	for _, lang := range languages {
		for _, path := range paths {
			sep := "?"
			if strings.Contains(path, "?") {
				sep = "&"
			}
			body := get(t, env.handler, path+sep+"lang="+lang).Body.String()
			for _, key := range []string{"rules.", "maintenance.", "release.", "settings.", "idx.generic"} {
				if strings.Contains(body, ">"+key) {
					t.Errorf("%s %s: unresolved i18n key %q", lang, path, key)
				}
			}
			if lang != "it" && strings.Contains(body, "Manutenzione") {
				t.Errorf("%s %s: Italian hard-coded text", lang, path)
			}
			if lang != "en" && lang != "fr" && strings.Contains(body, ">Maintenance<") {
				t.Errorf("%s %s: English hard-coded Maintenance", lang, path)
			}
			if lang != "en" && strings.Contains(body, ">Delete<") {
				t.Errorf("%s %s: English hard-coded Delete", lang, path)
			}
			if lang != "en" && strings.Contains(body, ">Save<") {
				t.Errorf("%s %s: English hard-coded Save", lang, path)
			}
		}
	}
}

func TestMaintenanceFormsHaveHelp(t *testing.T) {
	env := newTestEnv(t)
	actions := []string{"integrity", "quick_check", "foreign_keys", "vacuum", "optimize", "checkpoint", "analyze", "reindex", "reset_errors", "dedupe", "clear_torrents", "prune", "purge_days", "purge_indexer", "purge_no_links", "purge_hidden", "clear_runs", "clear_runs_days", "reset_api_usage", "delete_all"}
	for _, lang := range []string{"en", "it", "de", "fr", "es", "pl", "zh"} {
		body := get(t, env.handler, "/settings?section=maintenance&lang="+lang).Body.String()
		for _, action := range actions {
			marker := `name="action" value="` + action + `"`
			if !strings.Contains(body, marker) {
				t.Errorf("%s: maintenance action %s has no form", lang, action)
				continue
			}
			pos := strings.Index(body, marker)
			start := strings.LastIndex(body[:pos], "<form")
			end := strings.Index(body[pos:], "</form>")
			if start < 0 || end < 0 {
				t.Errorf("%s: cannot inspect form for %s", lang, action)
				continue
			}
			block := body[start : pos+end]
			if !strings.Contains(block, `<p class="help">`) || strings.Contains(block, `class="help"></p>`) {
				t.Errorf("%s: form %s has no help text", lang, action)
			}
		}
	}
}
