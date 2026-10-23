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

func TestLegacyURLsRedirect(t *testing.T) {
	env := newTestEnv(t)

	cases := []struct {
		path     string
		tab      string
		preserve map[string]string
	}{
		{"/sources", "legacy", nil},
		{"/sources?msg=ciao", "legacy", map[string]string{"msg": "ciao"}},
		{"/runs", "runs", nil},
		{"/runs?source=3&errors=1", "runs", map[string]string{"source": "3", "errors": "1"}},
		{"/logs", "logs", nil},
		{"/logs?level=error&q=timeout", "logs", map[string]string{"level": "error", "q": "timeout"}},
	}
	for _, tc := range cases {
		recorder := get(t, env.handler, tc.path)
		if recorder.Code != http.StatusFound {
			t.Fatalf("GET %s = %d, want 302", tc.path, recorder.Code)
		}
		parsed, values := locationQuery(t, recorder.Header().Get("Location"))
		wantPath := "/activity"
		if tc.tab == "legacy" {
			wantPath = "/indexers"
		}
		if parsed.Path != wantPath || values.Get("tab") != tc.tab {
			t.Errorf("GET %s -> %s, want %s?tab=%s", tc.path, parsed, wantPath, tc.tab)
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
	sourceID, err := env.store.CreateSource(ctx, store.Source{
		Name: "Activity source", Kind: "torznab", Manager: "auto",
		URL: "http://activity.example/api", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	runID, started, err := env.store.BeginSourceRun(ctx, sourceID)
	if err != nil {
		t.Fatal(err)
	}
	source, _ := env.store.GetSource(ctx, sourceID)
	if err := env.store.FinishSourceRun(ctx, runID, source, started,
		store.RunResult{Seen: 4, New: 1}, nil); err != nil {
		t.Fatal(err)
	}
	env.logs.Add(logbuffer.Entry{Time: time.Now(), Level: slog.LevelInfo, Message: "activity log line"})

	runs := get(t, env.handler, "/activity")
	if runs.Code != http.StatusOK {
		t.Fatalf("GET /activity = %d: %s", runs.Code, runs.Body.String())
	}
	body := runs.Body.String()
	for _, want := range []string{"Activity", "Runs", "Logs", "Activity source", `class="tab active" href="/activity?tab=runs"`} {
		if !strings.Contains(body, want) {
			t.Errorf("runs tab missing %q", want)
		}
	}
	if strings.Contains(body, "activity log line") {
		t.Error("the runs tab rendered the log buffer")
	}

	logs := get(t, env.handler, "/activity?tab=logs")
	if logs.Code != http.StatusOK {
		t.Fatalf("GET /activity?tab=logs = %d: %s", logs.Code, logs.Body.String())
	}
	logBody := logs.Body.String()
	for _, want := range []string{"Activity", "activity log line", "Showing 1 of 1", `class="tab active" href="/activity?tab=logs"`} {
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

func TestUnifiedIndexersTabs(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	if _, err := env.store.CreateSource(ctx, store.Source{
		Name: "Legacy Jackett", Kind: "torznab", Manager: "jackett",
		URL: "http://jackett:9117", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}

	native := get(t, env.handler, "/indexers")
	if native.Code != http.StatusOK {
		t.Fatalf("GET /indexers = %d: %s", native.Code, native.Body.String())
	}
	nativeBody := native.Body.String()
	for _, want := range []string{
		"Native indexers use indexarr definitions and connectors",
		"New native indexer",
		"Native",
		"Legacy bridge",
		`class="tab active" href="/indexers?tab=native"`,
	} {
		if !strings.Contains(nativeBody, want) {
			t.Errorf("native tab missing %q", want)
		}
	}
	if strings.Contains(nativeBody, "Legacy Jackett") {
		t.Error("the native tab rendered the legacy source list")
	}

	legacy := get(t, env.handler, "/indexers?tab=legacy")
	if legacy.Code != http.StatusOK {
		t.Fatalf("GET /indexers?tab=legacy = %d: %s", legacy.Code, legacy.Body.String())
	}
	legacyBody := legacy.Body.String()
	for _, want := range []string{
		"Legacy Jackett",
		"Add legacy source",
		`class="tab active" href="/indexers?tab=legacy"`,
	} {
		if !strings.Contains(legacyBody, want) {
			t.Errorf("legacy tab missing %q", want)
		}
	}
	if strings.Contains(legacyBody, "New native indexer") {
		t.Error("the legacy tab still shows the native CTA")
	}

	sources, err := env.store.ListSources(ctx)
	if err != nil || len(sources) != 1 {
		t.Fatalf("sources = %#v, %v", sources, err)
	}
	for _, path := range []string{
		"/sources/new",
		"/sources/bulk",
		"/sources/" + strconvI(sources[0].ID) + "/edit",
	} {
		recorder := get(t, env.handler, path)
		if recorder.Code != http.StatusOK {
			t.Errorf("GET %s = %d: %s", path, recorder.Code, recorder.Body.String())
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
		"Configuration", "Status", "Native indexers", "Legacy sources",
		"Applications", "Errors (24h)", "Database",
		"New native indexer", `href="/indexers/new"`,
		"Add legacy source", `href="/sources/new"`,
		"Import from Prowlarr/Jackett", `href="/import"`,
		"Connect application", `href="/applications/new"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard missing %q", want)
		}
	}
}

func TestDefinitionsSectionsAndIndexerCTA(t *testing.T) {
	env := newTestEnv(t)
	recorder := get(t, env.handler, "/definitions")
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /definitions = %d", recorder.Code)
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
		"Overview", "Releases", "Indexers", "Definitions", "Applications",
		"Import", "Activity", "Rules", "Settings", "Maintenance",
		"Manage", "System",
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

func TestOldPOSTRoutesStillWork(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	token, cookie := csrfFrom(t, env.handler, "/indexers?tab=legacy")
	created := postForm(t, env.handler, "/sources/quick", url.Values{
		"csrf": {token},
		"url":  {"https://legacy.example/feed.xml"},
		"kind": {"rss"},
		"name": {"Legacy POST feed"},
	}, cookie)
	if created.Code != http.StatusSeeOther {
		t.Fatalf("POST /sources/quick = %d: %s", created.Code, created.Body.String())
	}
	sources, err := env.store.ListSources(ctx)
	if err != nil || len(sources) != 1 {
		t.Fatalf("sources = %#v, %v", sources, err)
	}

	token, cookie = csrfFrom(t, env.handler, "/activity?tab=logs")
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

	token, cookie = csrfFrom(t, env.handler, "/activity?tab=logs")
	clearedLogs := postForm(t, env.handler, "/logs/clear", url.Values{"csrf": {token}}, cookie)
	if clearedLogs.Code != http.StatusSeeOther {
		t.Fatalf("POST /logs/clear = %d: %s", clearedLogs.Code, clearedLogs.Body.String())
	}
}
