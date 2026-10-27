package web

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/buzzqw/indexarr/internal/logbuffer"
	"github.com/buzzqw/indexarr/internal/store"
)

func TestLogsSectionTabsAndCounters(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	now := time.Now()

	env.logs.Add(logbuffer.Entry{Time: now, Level: 0, Message: "internal marker line"})

	for _, in := range []store.AppSearch{
		{
			App: "sonarr", Mode: "tvsearch", Query: "good query", TotalResults: 7,
			DurationMS: 250, CreatedAt: now.Add(-time.Minute),
			Live: []store.AppSearchLive{{Name: "1337x", Results: 7, Duration: 200}},
		},
		{
			App: "radarr", Mode: "movie", Query: "bad query", CreatedAt: now.Add(-2 * time.Minute),
			Error: "request failed", Live: []store.AppSearchLive{
				{Name: "1337x", Results: 0, Duration: 300, TimedOut: true},
			},
		},
	} {
		if _, err := env.store.RecordAppSearch(ctx, in); err != nil {
			t.Fatalf("RecordAppSearch: %v", err)
		}
	}

	// Internal log tab.
	page := get(t, env.handler, "/logs")
	if page.Code != http.StatusOK {
		t.Fatalf("GET /logs = %d, want 200", page.Code)
	}
	body := page.Body.String()
	for _, want := range []string{"internal marker line", `href="/logs?tab=requests"`, `href="/logs?tab=stats"`, "Where to start"} {
		if !strings.Contains(body, want) {
			t.Errorf("internal tab missing %q", want)
		}
	}

	// Requests tab: counters, both rows, and the outcome column.
	page = get(t, env.handler, "/logs?tab=requests")
	if page.Code != http.StatusOK {
		t.Fatalf("GET /logs?tab=requests = %d, want 200", page.Code)
	}
	body = page.Body.String()
	for _, want := range []string{"good query", "bad query", "1337x", "Success rate", "50.0%"} {
		if !strings.Contains(body, want) {
			t.Errorf("requests tab missing %q", want)
		}
	}

	// Errors filter hides the answered request.
	page = get(t, env.handler, "/logs?tab=requests&status=errors")
	body = page.Body.String()
	if strings.Contains(body, "good query") || !strings.Contains(body, "bad query") {
		t.Error("errors filter did not narrow the table")
	}

	// Window selector changes the counters: the rows are 1 minute old.
	page = get(t, env.handler, "/logs?tab=requests&window=all")
	if !strings.Contains(page.Body.String(), "2") {
		t.Error("all-history window lost the counters")
	}

	// Statistics tab: per app and per indexer.
	page = get(t, env.handler, "/logs?tab=stats")
	if page.Code != http.StatusOK {
		t.Fatalf("GET /logs?tab=stats = %d, want 200", page.Code)
	}
	body = page.Body.String()
	for _, want := range []string{"Per app", "Per indexer", "sonarr", "radarr", "1337x", "50.0%"} {
		if !strings.Contains(body, want) {
			t.Errorf("stats tab missing %q", want)
		}
	}
}

func TestLogsSectionEmptyAndFilters(t *testing.T) {
	env := newTestEnv(t)

	page := get(t, env.handler, "/logs?tab=requests&status=errors")
	if page.Code != http.StatusOK {
		t.Fatalf("GET /logs = %d", page.Code)
	}
	if !strings.Contains(page.Body.String(), "No requests") {
		t.Error("empty state missing")
	}

	// The internal filter form submits to the Logs page, not to Activity.
	page = get(t, env.handler, "/logs?tab=internal&q=needle")
	body := page.Body.String()
	if !strings.Contains(body, `action="/logs"`) {
		t.Error("internal filter form lost its Logs action")
	}
	if !strings.Contains(body, `name="tab" value="internal"`) {
		t.Error("internal filter form lost its tab")
	}
	if !strings.Contains(body, `value="needle"`) {
		t.Error("internal filter form lost the query")
	}
}

func TestLogsDownloadsTabAndPlainRequests(t *testing.T) {
	env := newNativeEnv(t)
	ctx := context.Background()
	now := time.Now()
	seedDefinition(t, env.store, "dl-src", "", `schema: 1
meta:
  id: dl-src
  name: dl-src
  type: public
  protocol: torrent
  urls: [https://dl-src.example/]
search:
  request:
    get: /
  response:
    type: html
`)
	id := seedPhase2Indexer(t, env.store, "dl-src", "dl-src", true, `{}`)
	for _, via := range []string{"ok", "magnet", "magnet"} {
		if _, err := env.store.RecordIndexerHistory(ctx, store.IndexerHistory{
			IndexerID: id, Kind: "grab", Status: via, Query: "Grabbed Show S01E01", App: "sonarr", StartedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := env.store.RecordAppSearch(ctx, store.AppSearch{
		App: "sonarr", Mode: "tvsearch", Query: "slow show", TotalResults: 3, DurationMS: 5000, CreatedAt: now,
		Live: []store.AppSearchLive{
			{Name: "Fast", Results: 3, Duration: 900},
			{Name: "Slow", TimedOut: true},
			{Name: "Feed", Skipped: true, Error: "feed-only indexer: cache only"},
		},
	}); err != nil {
		t.Fatal(err)
	}

	body := get(t, env.handler, "/logs?tab=downloads").Body.String()
	for _, want := range []string{"Grabbed Show S01E01", "dl-src", `action="/logs"`, `name="window"`, "Direct magnets are not counted."} {
		if !strings.Contains(body, want) {
			t.Errorf("downloads tab missing %q", want)
		}
	}

	body = get(t, env.handler, "/logs?tab=requests").Body.String()
	for _, want := range []string{"1 in time", "1 in background", "1 skipped", "TV series", "Feed-only source"} {
		if !strings.Contains(body, want) {
			t.Errorf("requests tab missing %q", want)
		}
	}
	if strings.Contains(body, `chip danger">over timeout`) {
		t.Error("a late source is still shown as a red timeout")
	}

	body = get(t, env.handler, "/logs?tab=stats").Body.String()
	if !strings.Contains(body, "In background") || !strings.Contains(body, "<td><strong>dl-src</strong></td>") || !strings.Contains(body, "<td><strong>3</strong></td>") {
		t.Errorf("stats tab misses the late column or the downloads count")
	}
}
