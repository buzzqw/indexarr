package web

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/buzzqw/indexarr/internal/logbuffer"
	"github.com/buzzqw/indexarr/internal/store"
)

func locationQuery(t *testing.T, location string) (*url.URL, url.Values) {
	t.Helper()
	parsed, err := url.Parse(location)
	if err != nil {
		t.Fatalf("Location %q: %v", location, err)
	}
	return parsed, parsed.Query()
}

func TestLogsSectionKeepsLegacyActivityTab(t *testing.T) {
	env := newTestEnv(t)

	// The old Activity → Logs tab now leads to the Log section, filters
	// included.
	recorder := get(t, env.handler, "/activity?tab=logs&level=error&q=timeout")
	if recorder.Code != http.StatusFound {
		t.Fatalf("GET /activity?tab=logs = %d, want 302", recorder.Code)
	}
	parsed, values := locationQuery(t, recorder.Header().Get("Location"))
	if parsed.Path != "/logs" || values.Get("tab") != "" || values.Get("level") != "error" || values.Get("q") != "timeout" {
		t.Errorf("GET /activity?tab=logs -> %s", parsed)
	}

	recorder = get(t, env.handler, "/logs?level=error&q=timeout")
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /logs = %d, want 200", recorder.Code)
	}
	body := recorder.Body.String()
	if strings.Contains(body, `name="tab"`) {
		t.Error("the Log filter carries a tab on a single-tab page")
	}
	if !strings.Contains(body, `value="timeout"`) {
		t.Error("the Log filter lost the query")
	}

	// The tabs that moved to Statistics keep their old links.
	for _, tab := range []string{"requests", "stats", "downloads"} {
		recorder = get(t, env.handler, "/logs?tab="+tab+"&window=7d")
		if recorder.Code != http.StatusFound {
			t.Fatalf("GET /logs?tab=%s = %d, want 302", tab, recorder.Code)
		}
		parsed, values = locationQuery(t, recorder.Header().Get("Location"))
		if parsed.Path != "/activity" || values.Get("tab") != tab || values.Get("window") != "7d" {
			t.Errorf("GET /logs?tab=%s -> %s", tab, parsed)
		}
	}
	recorder = get(t, env.handler, "/activity?tab=searches&page=2")
	if recorder.Code != http.StatusFound {
		t.Fatalf("GET /activity?tab=searches = %d, want 302", recorder.Code)
	}
	if parsed, values = locationQuery(t, recorder.Header().Get("Location")); values.Get("tab") != "requests" || values.Get("page") != "2" {
		t.Errorf("GET /activity?tab=searches -> %s", parsed)
	}
}

func TestLegacyURLsRedirect(t *testing.T) {
	env := newTestEnv(t)

	cases := []struct {
		path     string
		tab      string
		preserve map[string]string
	}{
		{"/sources", "", nil},
		{"/sources?msg=ciao", "", map[string]string{"msg": "ciao"}},
		{"/runs", "runs", nil},
		{"/runs?source=3&errors=1", "runs", map[string]string{"source": "3", "errors": "1"}},
	}
	for _, tc := range cases {
		recorder := get(t, env.handler, tc.path)
		if recorder.Code != http.StatusFound {
			t.Fatalf("GET %s = %d, want 302", tc.path, recorder.Code)
		}
		parsed, values := locationQuery(t, recorder.Header().Get("Location"))
		wantPath := "/activity"
		if tc.tab == "" {
			wantPath = "/indexers"
		}
		if parsed.Path != wantPath {
			t.Errorf("GET %s -> %s, want %s", tc.path, parsed, wantPath)
		}
		if tc.tab != "" && values.Get("tab") != tc.tab {
			t.Errorf("GET %s lost tab: got %q want %q", tc.path, values.Get("tab"), tc.tab)
		}
		for key, want := range tc.preserve {
			if got := values.Get(key); got != want {
				t.Errorf("GET %s lost %s: got %q want %q", tc.path, key, got, want)
			}
		}
	}
}

func TestActivityTabsReuseRunsAndLogs(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	if err := env.store.UpsertDefinition(ctx, store.Definition{
		ID: "activity-def", SchemaVersion: 1, Family: "torznab", State: "verificata",
		Origin: "local", Author: "test", Body: "schema: 1\nconnector: torznab\n", Hash: strings.Repeat("a", 64),
	}); err != nil {
		t.Fatal(err)
	}
	indexerID, err := env.store.CreateIndexer(ctx, store.Indexer{
		DefinitionID: "activity-def", Protocol: "torrent", Slug: "activity",
		Name: "Activity source", Enabled: true, ConfigJSON: `{}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	runID, err := env.store.RecordIndexerHistory(ctx, store.IndexerHistory{
		IndexerID: indexerID, Kind: "run", Status: "ok", StartedAt: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	env.logs.Add(logbuffer.Entry{Time: time.Now(), Level: slog.LevelInfo, Message: "activity log line"})

	runs := get(t, env.handler, "/activity?tab=runs")
	if runs.Code != http.StatusOK {
		t.Fatalf("GET /activity = %d: %s", runs.Code, runs.Body.String())
	}
	body := runs.Body.String()
	for _, want := range []string{"Statistics", "Runs", "Logs", "Activity source", `class="tab active" href="/activity?tab=runs"`} {
		if !strings.Contains(body, want) {
			t.Errorf("runs tab missing %q", want)
		}
	}
	if strings.Contains(body, "activity log line") {
		t.Error("the runs tab rendered the log buffer")
	}

	logs := get(t, env.handler, "/logs")
	if logs.Code != http.StatusOK {
		t.Fatalf("GET /logs = %d: %s", logs.Code, logs.Body.String())
	}
	logBody := logs.Body.String()
	for _, want := range []string{"System log", "activity log line", "Showing 1 of 1", `href="/logs" class="active"`} {
		if !strings.Contains(logBody, want) {
			t.Errorf("logs tab missing %q", want)
		}
	}
	if strings.Contains(logBody, "Activity source") {
		t.Error("the logs tab rendered the runs table")
	}

	detail := get(t, env.handler, "/runs/"+strconvI(runID))
	if detail.Code != http.StatusOK || !strings.Contains(detail.Body.String(), "Activity source") {
		t.Errorf("run detail = %d", detail.Code)
	}
}

func TestUnifiedIndexersPage(t *testing.T) {
	env := newTestEnv(t)

	native := get(t, env.handler, "/indexers")
	if native.Code != http.StatusOK {
		t.Fatalf("GET /indexers = %d: %s", native.Code, native.Body.String())
	}
	nativeBody := native.Body.String()
	for _, want := range []string{"Add indexer", "Import", "/indexers/export"} {
		if !strings.Contains(nativeBody, want) {
			t.Errorf("indexers page missing %q", want)
		}
	}
	for _, unwanted := range []string{"Legacy bridge", "Add legacy source", "/sources"} {
		if strings.Contains(nativeBody, unwanted) {
			t.Errorf("indexers page still contains %q", unwanted)
		}
	}

	legacy := get(t, env.handler, "/indexers?tab=legacy")
	if legacy.Code != http.StatusFound || legacy.Header().Get("Location") != "/indexers" {
		t.Fatalf("GET /indexers?tab=legacy = %d %q", legacy.Code, legacy.Header().Get("Location"))
	}

	for _, path := range []string{"/sources/new", "/sources/bulk", "/sources/1/edit"} {
		recorder := get(t, env.handler, path)
		if recorder.Code != http.StatusFound || recorder.Header().Get("Location") != "/indexers" {
			t.Errorf("GET %s = %d %q, want 302 /indexers", path, recorder.Code, recorder.Header().Get("Location"))
		}
	}
}

func TestDashboardQuickActions(t *testing.T) {
	env := newTestEnv(t)
	recorder := get(t, env.handler, "/")
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET / = %d", recorder.Code)
	}
	body := recorder.Body.String()
	for _, want := range []string{
		"Status", "Add indexer", `href="/indexers/new"`,
		"Connect your apps", "Connect an app", `href="/applications/new"`,
		"Last 24 hours", "Next step",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("home missing %q", want)
		}
	}
	for _, unwanted := range []string{"Errors (24h)", "Database size"} {
		if strings.Contains(body, unwanted) {
			t.Errorf("home still shows the technical counter %q", unwanted)
		}
	}
}

func TestDefinitionsSectionsAndIndexerCTA(t *testing.T) {
	env := newTestEnv(t)
	recorder := get(t, env.handler, "/settings?section=advanced")
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /settings?section=advanced = %d", recorder.Code)
	}
	body := recorder.Body.String()
	for _, want := range []string{
		"Built-in catalog", "Local definitions",
		"Create indexer",
		`href="/indexers/new?definition=`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("definitions page missing %q", want)
		}
	}
}

func TestNavigationEnglishGroupedAndNoSourcesDuplicate(t *testing.T) {
	env := newTestEnv(t)
	body := get(t, env.handler, "/").Body.String()
	if !strings.Contains(body, `<html lang="en" dir="ltr">`) {
		t.Error("the document is not marked as English")
	}
	if !strings.Contains(body, "Skip to content") {
		t.Error("the skip link is not in English")
	}

	asideStart := strings.Index(body, `<aside class="sidebar"`)
	asideEnd := strings.Index(body, "</aside>")
	if asideStart < 0 || asideEnd < asideStart {
		t.Fatal("no sidebar in the page")
	}
	sidebar := body[asideStart:asideEnd]
	for _, want := range []string{
		"Home", "Search", "Indexers", "App", "Statistics", "Logs", "Settings",
	} {
		if !strings.Contains(sidebar, want) {
			t.Errorf("navigation missing %q", want)
		}
	}
	if count := strings.Count(sidebar, `href="/indexers"`); count != 1 {
		t.Errorf("sidebar has %d Indexer links, want 1", count)
	}
	if strings.Contains(sidebar, `href="/sources"`) {
		t.Error("the navigation still has a Sources entry")
	}
	if strings.Contains(sidebar, ">Indexer nativi<") {
		t.Error("the navigation still has a separate Indexer nativi entry")
	}
}

func TestQuickAddCreatesNativeIndexer(t *testing.T) {
	env := newNativeEnv(t)
	token, cookie := csrfFrom(t, env.handler, "/indexers")
	created := postForm(t, env.handler, "/indexers/quick", url.Values{
		"csrf": {token},
		"url":  {"https://feed.example/feed.xml"},
		"name": {"POST feed"},
	}, cookie)
	if created.Code != http.StatusSeeOther {
		t.Fatalf("POST /indexers/quick = %d: %s", created.Code, created.Body.String())
	}
	indexers, err := env.store.ListIndexers(context.Background())
	if err != nil || len(indexers) != 1 {
		t.Fatalf("indexers = %#v, %v; location=%q", indexers, err, created.Header().Get("Location"))
	}
}

func TestModernPOSTRoutesStillWork(t *testing.T) {
	env := newTestEnv(t)

	token, cookie := csrfFrom(t, env.handler, "/logs")
	level := postForm(t, env.handler, "/logs/level", url.Values{
		"csrf":          {token},
		"runtime_level": {"debug"},
	}, cookie)
	if level.Code != http.StatusSeeOther || env.level.Level() != slog.LevelDebug {
		t.Fatalf("POST /logs/level = %d, level = %v", level.Code, env.level.Level())
	}

	token, cookie = csrfFrom(t, env.handler, "/activity?tab=runs")
	cleared := postForm(t, env.handler, "/runs/clear", url.Values{"csrf": {token}}, cookie)
	if cleared.Code != http.StatusSeeOther {
		t.Fatalf("POST /runs/clear = %d: %s", cleared.Code, cleared.Body.String())
	}

	token, cookie = csrfFrom(t, env.handler, "/logs")
	clearedLogs := postForm(t, env.handler, "/logs/clear", url.Values{"csrf": {token}}, cookie)
	if clearedLogs.Code != http.StatusSeeOther {
		t.Fatalf("POST /logs/clear = %d: %s", clearedLogs.Code, clearedLogs.Body.String())
	}
}
