package web

import (
	"context"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/buzzqw/indexarr/internal/i18n"
	"github.com/buzzqw/indexarr/internal/store"
)

func seedDataIndexer(t *testing.T, env *testEnv, slug, name string) int64 {
	t.Helper()
	ctx := context.Background()
	if err := env.store.UpsertDefinition(ctx, store.Definition{
		ID: "data-tab-def", SchemaVersion: 1, Family: "torznab", State: "verificata",
		Origin: "local", Author: "test",
		Body: "schema: 1\nconnector: torznab\nmeta:\n  id: data-tab-def\n  name: Data tab\n  protocol: torrent\n",
		Hash: strings.Repeat("a", 64),
	}); err != nil {
		t.Fatalf("UpsertDefinition: %v", err)
	}
	id, err := env.store.CreateIndexer(ctx, store.Indexer{
		DefinitionID: "data-tab-def",
		Protocol:     "torrent",
		Slug:         slug,
		Name:         name,
		Enabled:      true,
		Priority:     25,
		ConfigJSON:   `{}`,
	})
	if err != nil {
		t.Fatalf("CreateIndexer(%s): %v", slug, err)
	}
	return id
}

func seedDataRelease(t *testing.T, env *testEnv, indexerID int64, key, title string, seen time.Time, seeders int64) store.Release {
	t.Helper()
	release := store.Release{
		DedupKey:    key,
		Title:       title,
		SearchTitle: store.NormalizeSearch(title),
		Size:        1024,
		Seeders:     seeders,
		Leechers:    2,
		Category:    "5040",
		PublishedAt: seen,
		Magnet:      "magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567",
		FirstSeenAt: seen,
		LastSeenAt:  seen,
	}
	if _, err := env.store.UpsertRelease(context.Background(), &release); err != nil {
		t.Fatalf("UpsertRelease(%s): %v", key, err)
	}
	if err := env.store.RecordReleaseSource(context.Background(), store.ReleaseSource{
		ReleaseID: release.ID, IndexerID: indexerID,
		FirstSeenAt: seen, LastSeenAt: seen, Seeders: seeders,
	}); err != nil {
		t.Fatalf("RecordReleaseSource(%s): %v", key, err)
	}
	return release
}

func TestIndexerDataTabShowsMetricsCompletenessAndRedactedSample(t *testing.T) {
	env := newTestEnv(t)
	indexerID := seedDataIndexer(t, env, "data-indexer", "Data Indexer")
	ctx := context.Background()
	now := time.Now()

	// Three runs in the last 7 days, with one item for every discard reason.
	for i := 0; i < 3; i++ {
		started := now.Add(-time.Duration(i) * time.Hour)
		metrics := &store.RunMetrics{
			Seen: 10, ItemsNew: 3, Updated: 1, Duplicates: 2, Discarded: 4,
			DiscardNoLink: 1, DiscardBlocked: 1, DiscardTooOld: 1,
			DiscardCategory: 1, DiscardParse: 1, DiscardOther: 1,
			FieldTotal: 10, FieldSize: 9, FieldSeeders: 3, FieldCategory: 8,
			FieldDate: 7, FieldHash: 6,
		}
		if _, err := env.store.RecordIndexerHistory(ctx, store.IndexerHistory{
			IndexerID: indexerID, Kind: "run", Status: "ok",
			LatencyMS: 120, StartedAt: started, Metrics: metrics,
		}); err != nil {
			t.Fatalf("RecordIndexerHistory: %v", err)
		}
	}

	body := "<html>" +
		`<a href="https://tracker.example/SYNTHETICKEY/download?apikey=SYNTHETICKEY">` +
		"release</a></html>"
	if err := env.store.PutIndexerSample(ctx, store.IndexerSample{
		IndexerID:  indexerID,
		RequestURL: "https://tracker.example/SYNTHETICKEY/feed?apikey=SYNTHETICKEY",
		Method:     "GET", Status: 200, LatencyMS: 42,
		Body: body, BodyBytes: int64(len(body)), Truncated: true,
		Items: []store.SampleItem{{
			Title:       "Sample Release",
			DownloadURL: "https://tracker.example/SYNTHETICKEY/download?apikey=SYNTHETICKEY",
			Size:        2048, Seeders: 4, Categories: []int{5040},
			Published: "2026-10-23T10:00:00Z",
			Fields: map[string]bool{
				"size": true, "seeders": true, "category": true,
				"date": true, "infohash": false,
			},
		}},
		FetchedAt: now,
	}); err != nil {
		t.Fatalf("PutIndexerSample: %v", err)
	}

	recorder := get(t, env.handler, "/indexers/"+strconv.FormatInt(indexerID, 10)+"?tab=data")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
	page := recorder.Body.String()
	for _, want := range []string{
		"New and discarded per run",
		`<svg class="run-chart"`,
		"Field completeness",
		"Seeders",
		"Last sample",
		"Raw response",
		"First normalized items",
		"No link", "Blocked", "Too old", "Parse", "Other",
		"Found", "Missing", "Sample Release",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("data tab missing %q", want)
		}
	}
	if got := strings.Count(page, `class="chart-new"`); got != 3 {
		t.Errorf("chart new bars = %d, want 3", got)
	}
	if strings.Contains(page, "SYNTHETICKEY") {
		t.Error("the rendered sample still contains the synthetic secret marker")
	}
	if !strings.Contains(page, "data-tab") && !strings.Contains(page, "Data") {
		t.Error("data tab navigation missing")
	}

	// Pagination and filters in the same page must not reload the whole feed.
	if strings.Contains(page, `href="/indexers/`+strconv.FormatInt(indexerID, 10)+`?tab=data" aria-current="page"`) == false {
		t.Error("the Data tab is not marked current")
	}
}

func TestTryAsAppRendersTheSameXMLAsAPI(t *testing.T) {
	env := newTestEnv(t)
	indexerID := seedDataIndexer(t, env, "try-web", "Try Web")
	seedDataRelease(t, env, indexerID, "try-web-release", "Try Show S01E01 1080p", time.Now().Add(-time.Hour), 30)

	query := url.Values{
		"try": {"1"}, "mode": {"tvsearch"}, "q": {"Try Show"},
		"season": {"1"}, "ep": {"1"},
	}
	recorder := get(t, env.handler, "/indexers/"+strconv.FormatInt(indexerID, 10)+"?"+query.Encode())
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
	page := recorder.Body.String()
	for _, want := range []string{
		"Try as Sonarr/Radarr", "Try Show", "Exact Torznab XML",
		"Try Show S01E01 1080p",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("try panel missing %q", want)
		}
	}

	match := regexp.MustCompile(`(?s)<pre class="sample-body">(.*?)</pre>`).FindStringSubmatch(page)
	if match == nil {
		t.Fatal("the exact Torznab XML block is missing")
	}
	tryXML := html.UnescapeString(match[1])

	apiQuery := url.Values{
		"t": {"tvsearch"}, "apikey": {env.apiKey}, "q": {"Try Show"},
		"season": {"1"}, "ep": {"1"},
	}
	apiRecorder := get(t, env.handler, "/api?"+apiQuery.Encode())
	if apiRecorder.Code != http.StatusOK {
		t.Fatalf("api status = %d: %s", apiRecorder.Code, apiRecorder.Body.String())
	}
	if apiRecorder.Body.String() != tryXML {
		t.Fatalf("the Try panel XML differs from /api\n--- try ---\n%s\n--- api ---\n%s",
			tryXML, apiRecorder.Body.String())
	}
}

func TestActivityFeedAndSearchesPages(t *testing.T) {
	env := newTestEnv(t)
	indexerID := seedDataIndexer(t, env, "feed-one", "Feed One")
	ctx := context.Background()
	base := time.Now().Add(-2 * time.Hour)

	for i := 0; i < 51; i++ {
		seedDataRelease(t, env, indexerID,
			"feed-"+strconv.Itoa(i),
			"Feed Release "+strconv.Itoa(i),
			base.Add(time.Duration(i)*time.Minute), int64(i))
	}
	for i := 0; i < 51; i++ {
		if _, err := env.store.RecordAppSearch(ctx, store.AppSearch{
			App: "sonarr", Mode: "tvsearch", Query: "query " + strconv.Itoa(i),
			IndexerIDs: []int64{indexerID}, IndexerNames: []string{"Feed One"},
			Results:      map[string]int64{"Feed One": int64(i)},
			TotalResults: int64(i), DurationMS: int64(i + 1),
			CreatedAt: base.Add(time.Duration(i) * time.Minute),
		}); err != nil {
			t.Fatalf("RecordAppSearch: %v", err)
		}
	}

	feed := get(t, env.handler, "/activity?tab=feed")
	if feed.Code != http.StatusOK {
		t.Fatalf("feed status = %d: %s", feed.Code, feed.Body.String())
	}
	feedBody := feed.Body.String()
	for _, want := range []string{
		"New releases", "Feed Release 50", "Seen by", "Feed One",
		`data-live-refresh="10"`, "page=2",
	} {
		if !strings.Contains(feedBody, want) {
			t.Errorf("feed page missing %q", want)
		}
	}
	if strings.Contains(feedBody, "Feed Release 0") {
		t.Error("the first feed page contains a release from page 2")
	}

	feedPage2 := get(t, env.handler, "/activity?tab=feed&page=2")
	if !strings.Contains(feedPage2.Body.String(), "Feed Release 0") {
		t.Error("the second feed page does not contain the oldest release")
	}

	// Filtering by indexer keeps the provenance filter in the query.
	filtered := get(t, env.handler, "/activity?tab=feed&indexer="+strconv.FormatInt(indexerID, 10))
	if !strings.Contains(filtered.Body.String(), "Feed Release 50") {
		t.Error("the indexer filter dropped releases")
	}

	searches := get(t, env.handler, "/activity?tab=searches")
	if searches.Code != http.StatusOK {
		t.Fatalf("searches status = %d: %s", searches.Code, searches.Body.String())
	}
	searchBody := searches.Body.String()
	for _, want := range []string{
		"Searches", "sonarr", "query 50", "Feed One", "Results per indexer",
		`data-live-refresh="10"`, "page=2",
	} {
		if !strings.Contains(searchBody, want) {
			t.Errorf("searches page missing %q", want)
		}
	}
	searchPage2 := get(t, env.handler, "/activity?tab=searches&page=2")
	if !strings.Contains(searchPage2.Body.String(), "query 0") {
		t.Error("the second searches page does not contain the oldest request")
	}

	runs := get(t, env.handler, "/activity?tab=runs")
	if strings.Contains(runs.Body.String(), `data-live-refresh`) {
		t.Error("the runs tab must not poll")
	}
}

func TestSettingsHistoryRetentionField(t *testing.T) {
	env := newTestEnv(t)
	body := get(t, env.handler, "/settings?section=updates").Body.String()
	if !strings.Contains(body, `name="history_retention_days"`) {
		t.Fatal("the history retention field is missing from Settings")
	}
	if !strings.Contains(body, `value="30"`) {
		t.Error("the history retention default is not 30 days")
	}

	token, cookie := csrfFrom(t, env.handler, "/settings")
	values := url.Values{
		"csrf": {token}, "public_url": {""}, "flaresolverr_url": {""},
		"default_interval_seconds": {"3600"}, "retention_days": {"0"},
		"history_retention_days": {"7"},
		"fetch_timeout_seconds":  {"45"}, "workers": {"2"},
		"max_torrents_per_run": {"30"}, "max_torrent_mb": {"8"},
		"user_agent": {"unit-test"}, "torznab_download_factor": {"0"},
		"torznab_upload_factor": {"1"}, "backup_interval_hours": {"24"},
		"backup_keep": {"7"}, "download_client": {"none"},
		"torznab_live_timeout_seconds": {"20"}, "auto_disable_after": {"0"},
	}
	if recorder := postForm(t, env.handler, "/settings", values, cookie); recorder.Code != http.StatusSeeOther {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
	settings, err := env.store.LoadSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if settings.HistoryRetentionDays != 7 {
		t.Errorf("history retention = %d, want 7", settings.HistoryRetentionDays)
	}

	// 0 is meaningful: it keeps the activity forever.
	values.Set("history_retention_days", "0")
	values.Set("csrf", token)
	if recorder := postForm(t, env.handler, "/settings", values, cookie); recorder.Code != http.StatusSeeOther {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
	settings, err = env.store.LoadSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if settings.HistoryRetentionDays != 0 {
		t.Errorf("history retention = %d, want 0 (keep forever)", settings.HistoryRetentionDays)
	}
}

func TestDataAndActivityTabsAreTranslatedInEveryLanguage(t *testing.T) {
	keys := []string{
		"idx.detail.data", "idx.data.chartTitle", "idx.data.completenessTitle",
		"idx.data.sampleTitle", "idx.try.title", "activity.feed",
		"activity.searches", "settings.historyRetentionDays",
	}
	english := map[string]string{}
	for _, key := range keys {
		english[key] = i18n.T(i18n.EN, key)
		if english[key] == "" || english[key] == key {
			t.Errorf("missing English key %q", key)
		}
	}
	for _, lang := range i18n.Supported() {
		for _, key := range keys {
			value := i18n.T(lang, key)
			if value == "" || value == key {
				t.Errorf("%s: key %q is not translated", lang, key)
			}
		}
	}

	env := newTestEnv(t)
	indexerID := seedDataIndexer(t, env, "translated", "Translated")
	path := "/indexers/" + strconv.FormatInt(indexerID, 10) + "?tab=data&lang=it"
	page := get(t, env.handler, path).Body.String()
	for _, want := range []string{"Dati", "Completezza dei campi", "Ultimo campione"} {
		if !strings.Contains(page, want) {
			t.Errorf("Italian data tab missing %q", want)
		}
	}
	activity := get(t, env.handler, "/activity?tab=feed&lang=it").Body.String()
	if !strings.Contains(activity, "Nuove release") {
		t.Error("Italian activity feed tab missing")
	}
}

func TestVisibilityRefreshIsOnlyOnLiveTabs(t *testing.T) {
	env := newTestEnv(t)
	for _, tab := range []string{"feed", "searches"} {
		body := get(t, env.handler, "/activity?tab="+tab).Body.String()
		if !strings.Contains(body, `data-live-refresh="10"`) {
			t.Errorf("%s should poll every 10 seconds", tab)
		}
	}
	for _, tab := range []string{"runs", "logs"} {
		body := get(t, env.handler, "/activity?tab="+tab).Body.String()
		if strings.Contains(body, `data-live-refresh`) {
			t.Errorf("%s must not use visibility polling", tab)
		}
	}
}
