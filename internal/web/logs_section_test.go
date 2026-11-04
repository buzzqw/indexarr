package web

import (
	"context"
	"net/http"
	"strconv"
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
	for _, want := range []string{"internal marker line", `href="/activity?tab=requests"`, "Where to start", "System log"} {
		if !strings.Contains(body, want) {
			t.Errorf("internal tab missing %q", want)
		}
	}

	// Requests tab: counters, both rows, and the outcome column.
	page = get(t, env.handler, "/activity?tab=requests")
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
	page = get(t, env.handler, "/activity?tab=requests&status=errors")
	body = page.Body.String()
	if strings.Contains(body, "good query") || !strings.Contains(body, "bad query") {
		t.Error("errors filter did not narrow the table")
	}

	// Window selector changes the counters: the rows are 1 minute old.
	page = get(t, env.handler, "/activity?tab=requests&window=all")
	if !strings.Contains(page.Body.String(), "2") {
		t.Error("all-history window lost the counters")
	}

	// Statistics tab: per app and per indexer.
	page = get(t, env.handler, "/activity?tab=stats")
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

	page := get(t, env.handler, "/activity?tab=requests&status=errors")
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
	if strings.Contains(body, `name="tab"`) {
		t.Error("the single-tab Log page must not carry a tab in its filter form")
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
	for _, via := range []string{"ok", "magnet", "magnet", "ed2k"} {
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

	body := get(t, env.handler, "/activity?tab=downloads").Body.String()
	for _, want := range []string{"Grabbed Show S01E01", "dl-src", `action="/activity"`, `name="window"`, `name="source"`, "All sources", "Direct magnets are not counted.", "queued in aMule", "eD2k link"} {
		if !strings.Contains(body, want) {
			t.Errorf("downloads tab missing %q", want)
		}
	}

	body = get(t, env.handler, "/activity?tab=requests").Body.String()
	for _, want := range []string{"1 in time", "1 in background", "1 skipped", "TV series", "Feed-only source"} {
		if !strings.Contains(body, want) {
			t.Errorf("requests tab missing %q", want)
		}
	}
	if strings.Contains(body, `chip danger">over timeout`) {
		t.Error("a late source is still shown as a red timeout")
	}

	body = get(t, env.handler, "/activity?tab=stats").Body.String()
	if !strings.Contains(body, "In background") || !strings.Contains(body, "<td><strong>dl-src</strong></td>") || !strings.Contains(body, "<td><strong>4</strong></td>") {
		t.Errorf("stats tab misses the late column or the downloads count")
	}
}

// The source filter narrows the latest table and the counters, and the
// "delivered as" chip copies the magnet or the .torrent link of that release.
func TestLogsDownloadsSourceFilterAndCopyLinks(t *testing.T) {
	env := newNativeEnv(t)
	ctx := context.Background()
	now := time.Now()
	for _, id := range []string{"copy-a", "copy-b"} {
		seedDefinition(t, env.store, id, "", `schema: 1
meta:
  id: `+id+`
  name: `+id+`
  type: public
  protocol: torrent
  urls: [https://`+id+`.example/]
search:
  request:
    get: /
  response:
    type: html
`)
	}
	first := seedPhase2Indexer(t, env.store, "copy-a", "copy-a", true, `{}`)
	second := seedPhase2Indexer(t, env.store, "copy-b", "copy-b", true, `{}`)

	magnetRelease := store.Release{
		DedupKey:    "copy-magnet",
		Title:       "Magnet Show S01E01",
		Magnet:      "magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567",
		Protocol:    "torrent",
		FirstSeenAt: now,
		LastSeenAt:  now,
		PublishedAt: now,
	}
	if _, err := env.store.UpsertRelease(ctx, &magnetRelease); err != nil {
		t.Fatalf("UpsertRelease(magnet): %v", err)
	}
	torrentRelease := store.Release{
		DedupKey:    "copy-torrent",
		Title:       "Torrent Show S02E02",
		Torrent:     []byte("d4:infod4:name4:teste"),
		Protocol:    "torrent",
		FirstSeenAt: now,
		LastSeenAt:  now,
		PublishedAt: now,
	}
	if _, err := env.store.UpsertRelease(ctx, &torrentRelease); err != nil {
		t.Fatalf("UpsertRelease(torrent): %v", err)
	}
	for _, row := range []store.IndexerHistory{
		{IndexerID: first, ReleaseID: magnetRelease.ID, Kind: "grab", Status: "magnet", Query: magnetRelease.Title, App: "sonarr", StartedAt: now},
		{IndexerID: second, ReleaseID: torrentRelease.ID, Kind: "grab", Status: "ok", Query: torrentRelease.Title, App: "radarr", StartedAt: now},
	} {
		if _, err := env.store.RecordIndexerHistory(ctx, row); err != nil {
			t.Fatal(err)
		}
	}

	body := get(t, env.handler, "/activity?tab=downloads&window=all").Body.String()
	for _, want := range []string{
		"Magnet Show S01E01",
		"Torrent Show S02E02",
		`data-copy="magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567"`,
		"http://example.com/releases/" + strconv.FormatInt(torrentRelease.ID, 10) + "/torrent",
		"copy-a",
		"copy-b",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("downloads tab missing %q", want)
		}
	}

	filtered := get(t, env.handler, "/activity?tab=downloads&window=all&source="+strconv.FormatInt(first, 10)).Body.String()
	if !strings.Contains(filtered, "Magnet Show S01E01") {
		t.Error("the source filter hid the selected source")
	}
	if strings.Contains(filtered, "Torrent Show S02E02") {
		t.Error("the source filter kept another source's download")
	}
}

// The eD2k tile is always visible: a zero says that no eD2k grab passed through
// indexarr, which is information too. It used to disappear with no rows.
func TestLogsDownloadsShowsTheEd2kTileWithoutGrabs(t *testing.T) {
	env := newNativeEnv(t)
	body := get(t, env.handler, "/activity?tab=downloads").Body.String()
	if !strings.Contains(body, `<strong>0</strong><span class="hint">queued in aMule</span>`) {
		t.Errorf("the eD2k tile is missing or not zero\n%s", body)
	}
}

// A grab a client confirmed as completed is shown as downloaded; one nobody
// confirmed stays "delivered only". The counter follows the same evidence.
func TestLogsDownloadsShowsTheConfirmedOutcome(t *testing.T) {
	env := newNativeEnv(t)
	ctx := context.Background()
	now := time.Now()
	seedDefinition(t, env.store, "confirm-ui", "", `schema: 1
meta:
  id: confirm-ui
  name: confirm-ui
  type: public
  protocol: torrent
  urls: [https://confirm-ui.example/]
search:
  request:
    get: /
  response:
    type: html
`)
	id := seedPhase2Indexer(t, env.store, "confirm-ui", "confirm-ui", true, `{}`)
	release := store.Release{
		DedupKey: "confirm-ui-release",
		Title:    "Confirmed Show S01E01",
		InfoHash: "0123456789abcdef0123456789abcdef01234567",
		Protocol: "torrent",
	}
	if _, err := env.store.UpsertRelease(ctx, &release); err != nil {
		t.Fatalf("UpsertRelease: %v", err)
	}
	if err := env.store.MarkReleaseDownloaded(ctx, release.ID); err != nil {
		t.Fatalf("MarkReleaseDownloaded: %v", err)
	}
	if _, err := env.store.RecordIndexerHistory(ctx, store.IndexerHistory{
		IndexerID: id, ReleaseID: release.ID, Kind: "grab",
		Query: release.Title, Status: "magnet", App: "gextto",
		StartedAt: now.Add(-2 * time.Minute),
	}); err != nil {
		t.Fatalf("RecordIndexerHistory: %v", err)
	}
	if _, err := env.store.RecordIndexerHistory(ctx, store.IndexerHistory{
		IndexerID: id, Kind: "grab", Query: "Delivered Only S01E02",
		Status: "ok", App: "sonarr", StartedAt: now.Add(-time.Minute),
	}); err != nil {
		t.Fatalf("RecordIndexerHistory: %v", err)
	}
	if matched, err := env.store.ConfirmDownload(ctx, store.DownloadConfirmation{
		Protocol: "torrent", Hash: release.InfoHash, Title: release.Title,
		Client: "gextto", CompletedAt: now,
	}); err != nil || matched == 0 {
		t.Fatalf("ConfirmDownload = %d, %v", matched, err)
	}

	body := get(t, env.handler, "/activity?tab=downloads").Body.String()
	for _, want := range []string{
		"Confirmed", "completed by the client",
		"downloaded", "delivered only", "Confirmed by gextto",
		"Outcome", "/api/v2/downloads/confirm",
		`name="outcome"`, "All outcomes", "outcome=confirmed",
		"outcome=confirmed&amp;source=",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("downloads tab missing %q", want)
		}
	}

	// The outcome filter narrows the list: the two grabs stay in the window,
	// but each selection shows only its own.
	confirmedOnly := get(t, env.handler, "/activity?tab=downloads&window=all&outcome=confirmed").Body.String()
	if !strings.Contains(confirmedOnly, "Confirmed Show S01E01") {
		t.Error("the confirmed filter hid the confirmed download")
	}
	if strings.Contains(confirmedOnly, "Delivered Only S01E02") {
		t.Error("the confirmed filter kept a delivered-only download")
	}
	deliveredOnly := get(t, env.handler, "/activity?tab=downloads&window=all&outcome=delivered").Body.String()
	if !strings.Contains(deliveredOnly, "Delivered Only S01E02") {
		t.Error("the delivered filter hid the delivered-only download")
	}
	if strings.Contains(deliveredOnly, "Confirmed Show S01E01") {
		t.Error("the delivered filter kept a confirmed download")
	}

	// The form keeps the outcome selected, so changing the window keeps it.
	if !strings.Contains(confirmedOnly, `<option value="confirmed" selected>`) {
		t.Error("the outcome select lost the active filter")
	}
}

func TestStatisticsHoldsTheTabsAndLogOnlyTheProcessLog(t *testing.T) {
	env := newTestEnv(t)

	body := get(t, env.handler, "/activity?lang=en").Body.String()
	for _, tab := range []string{"requests", "stats", "downloads", "runs", "feed", "network"} {
		if !strings.Contains(body, `href="/activity?tab=`+tab+`"`) {
			t.Errorf("Statistics is missing the %s tab", tab)
		}
	}
	if !strings.Contains(body, `class="tab active" href="/activity?tab=requests"`) {
		t.Error("Statistics does not open on Requests")
	}
	if !strings.Contains(body, `<a href="/logs">`) {
		t.Error("the Statistics guide does not lead to the system log")
	}

	logs := get(t, env.handler, "/logs?lang=en").Body.String()
	for _, moved := range []string{`href="/logs?tab=requests"`, `href="/logs?tab=stats"`, `href="/logs?tab=downloads"`, `class="tabs"`} {
		if strings.Contains(logs, moved) {
			t.Errorf("the Log page still has %s", moved)
		}
	}
	for _, want := range []string{`action="/logs/level"`, `action="/logs/clear"`, `href="/logs/download?`, "Statistics → Requests"} {
		if !strings.Contains(logs, want) {
			t.Errorf("the Log page lost %s", want)
		}
	}
}
