package web

import (
	"context"
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

// visibleMain returns the <main> block with every <details> block removed and
// all tags stripped. It is the text a sighted user reads without opening
// "Advanced", which is exactly the perimeter of UI-GUIDE rule 1.
func visibleMain(t *testing.T, body string) string {
	t.Helper()
	start := strings.Index(body, `<main `)
	end := strings.LastIndex(body, `</main>`)
	if start < 0 || end < start {
		t.Fatalf("no <main> block in page")
	}
	main := body[start : end+len("</main>")]
	main = regexp.MustCompile(`(?s)<details.*?</details>`).ReplaceAllString(main, "")
	main = regexp.MustCompile(`<[^>]+>`).ReplaceAllString(main, " ")
	return main
}

func stripDetails(body string) string {
	return regexp.MustCompile(`(?s)<details.*?</details>`).ReplaceAllString(body, "")
}

func phase2Definition(id, connector, typ string, urls []string, settings, categories string) string {
	var b strings.Builder
	b.WriteString("schema: 1\n")
	if connector != "" {
		b.WriteString("connector: " + connector + "\n")
	}
	b.WriteString("meta:\n  id: " + id + "\n  name: " + id + "\n  type: " + typ + "\n  protocol: torrent\n  urls:\n")
	for _, raw := range urls {
		b.WriteString("    - " + raw + "\n")
	}
	if settings != "" {
		b.WriteString("settings:\n" + settings)
	}
	if categories != "" {
		b.WriteString("categories:\n" + categories)
	}
	if connector == "" {
		b.WriteString("search:\n  request:\n    get: /\n  response:\n    type: html\n")
	}
	return b.String()
}

func seedPhase2Indexer(t *testing.T, st *store.Store, defID, slug string, enabled bool, config string) int64 {
	t.Helper()
	id, err := st.CreateIndexer(context.Background(), store.Indexer{
		DefinitionID: defID,
		Protocol:     "torrent",
		Slug:         slug,
		Name:         slug,
		Enabled:      enabled,
		Priority:     25,
		ConfigJSON:   config,
	})
	if err != nil {
		t.Fatalf("CreateIndexer(%s): %v", slug, err)
	}
	return id
}

func TestAddCorsaroShowsOnlyNameAndCategories(t *testing.T) {
	env := newNativeEnv(t)
	body := get(t, env.handler, "/indexers/new?definition=corsaro").Body.String()
	visible := stripDetails(body)

	for _, want := range []string{`name="name"`, `name="categories"`, "Categories", "Movies", "Music", "Other"} {
		if !strings.Contains(visible, want) {
			t.Errorf("Corsaro form missing %q outside Advanced", want)
		}
	}
	for _, unwanted := range []string{
		`name="protocol"`, `name="slug"`, `name="priority"`, `name="tags"`,
		`name="user_agent"`, `name="allow_private"`, `name="setting_category"`,
		"Protocol", "Priority", "User agent", "Allow private",
	} {
		if strings.Contains(visible, unwanted) {
			t.Errorf("Corsaro form shows %q outside Advanced", unwanted)
		}
	}
	if !strings.Contains(body, `<details class="advanced">`) {
		t.Error("the Advanced block is not rendered as a closed detail")
	}
	if strings.Contains(body, "legacy-") {
		t.Error("a legacy definition reached the add page")
	}
}

func TestForbiddenWordsStayInsideAdvanced(t *testing.T) {
	env := newNativeEnv(t)
	id := seedPhase2Indexer(t, env.store, "test-native", "visible-words", true, `{"url":"https://example.invalid/api"}`)

	paths := []string{"/indexers", "/indexers/new", "/indexers/new?definition=corsaro", "/indexers/" + strconv.FormatInt(id, 10)}
	for _, lang := range []string{"en", "it"} {
		for _, path := range paths {
			withLang := path
			if lang == "it" {
				separator := "?"
				if strings.Contains(path, "?") {
					separator = "&"
				}
				withLang = path + separator + "lang=it"
			}
			body := get(t, env.handler, withLang).Body.String()
			visible := strings.ToLower(visibleMain(t, body))
			for _, word := range []string{"definizione", "definition", "famiglia", "family", "connettore", "connector", "ponte", "bridge", "legacy", "slug", "ssrf"} {
				if strings.Contains(visible, word) {
					t.Errorf("%s shows forbidden word %q outside Advanced", withLang, word)
				}
			}
		}
	}
}

func TestPasteExtToURLOpensExtTo(t *testing.T) {
	env := newNativeEnv(t)
	pasted := "/indexers/new?q=" + url.QueryEscape("https://extto.org/browse/")
	recorder := get(t, env.handler, pasted)
	if recorder.Code != http.StatusFound {
		t.Fatalf("pasting ExtTo = %d, want 302: %s", recorder.Code, recorder.Body.String())
	}
	location := recorder.Header().Get("Location")
	if !strings.HasPrefix(location, "/indexers/new?definition=extto") {
		t.Fatalf("pasting ExtTo redirected to %q", location)
	}
	form := get(t, env.handler, location).Body.String()
	for _, want := range []string{"ExtTo", `name="name"`, `name="categories"`, `name="setting_filter"`} {
		if !strings.Contains(form, want) {
			t.Errorf("ExtTo form missing %q", want)
		}
	}
	if !strings.Contains(form, `name="definition_id" value="extto"`) {
		t.Error("the ExtTo form lost the selected site")
	}
	if !strings.Contains(form, `name="setting_filter"`) || !strings.Contains(form, `value="c=TV"`) {
		t.Error("the ExtTo required filter is not prefilled with its default")
	}
}

func TestSiteChooserListsStatesAndLocalizedCategories(t *testing.T) {
	env := newNativeEnv(t)
	body := get(t, env.handler, "/indexers/new").Body.String()
	for _, want := range []string{"Supported sites", "Verified", "Testing", "ExtTo", "Corsaro", "Torznab (generic)"} {
		if !strings.Contains(body, want) {
			t.Errorf("site chooser missing %q", want)
		}
	}
	italian := get(t, env.handler, "/indexers/new?definition=corsaro&lang=it").Body.String()
	for _, want := range []string{"Film", "Musica", "Altro"} {
		if !strings.Contains(italian, want) {
			t.Errorf("Italian Corsaro form missing localized category %q", want)
		}
	}
	if strings.Contains(italian, ">Movies<") {
		t.Error("the Italian form still shows an English category name")
	}
}

func TestIndexerStatusBadgesAndProblems(t *testing.T) {
	env := newNativeEnv(t)

	defs := []struct {
		id, connector, typ, urls, categories string
	}{
		{"html-public", "", "public", "https://public.example/\n", `  - { site: "1", standard: 5000, desc: "TV" }` + "\n"},
		{"html-private", "", "private", "https://private.example/\n", ""},
		{"bridge", "torznab", "private", "https://bridge.example/\n", ""},
		{"feed", "rss", "public", "https://feed.example/rss\n", ""},
	}
	for _, def := range defs {
		seedDefinition(t, env.store, def.id, def.connector, phase2Definition(def.id, def.connector, def.typ, []string{strings.TrimSpace(def.urls)}, "", def.categories))
	}

	okID := seedPhase2Indexer(t, env.store, "html-public", "extto", true, `{"url":"https://public.example/","filter":"c=TV"}`)
	seedPhase2Indexer(t, env.store, "html-private", "privato", true, `{"url":"https://private.example/"}`)
	seedPhase2Indexer(t, env.store, "bridge", "via-jackett", true, `{"url":"http://jackett:9117/api/v2.0/indexers/x/results/torznab/"}`)
	seedPhase2Indexer(t, env.store, "bridge", "via-prowlarr", true, `{"url":"http://prowlarr:9696/16/api"}`)
	disabledID := seedPhase2Indexer(t, env.store, "html-public", "spento", false, `{"url":"https://public.example/"}`)
	seedPhase2Indexer(t, env.store, "feed", "feed-knaben", true, `{"url":"https://feed.example/rss"}`)

	ctx := context.Background()
	if _, err := env.store.RecordIndexerHistory(ctx, store.IndexerHistory{
		IndexerID: okID, Kind: "run", Status: "ok", StartedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := env.store.UpsertIndexerState(ctx, store.IndexerState{
		IndexerID: 2, Circuit: "closed", LastError: "HTTP 429: too many requests",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := env.store.RecordIndexerHistory(ctx, store.IndexerHistory{
		IndexerID: disabledID, Kind: "run", Status: "ok", StartedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	body := get(t, env.handler, "/indexers").Body.String()
	for _, want := range []string{
		`class="status-dot status-ok"`, `class="status-dot status-problem"`,
		`class="status-dot status-disabled"`, `class="status-dot status-testing"`,
		"Public", "Private", "Via Jackett", "Via Prowlarr", "Feed",
		"The site is limiting requests.", "Reduce the frequency",
		"1 filter",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("indexer list missing %q", want)
		}
	}
}

func TestBulkActionsAppearOnlyAfterSelection(t *testing.T) {
	env := newNativeEnv(t)
	seedDefinition(t, env.store, "bulk-def", "", `schema: 1
meta:
  id: bulk-def
  name: Bulk
  type: public
  protocol: torrent
  urls: [https://bulk.example/]
search:
  request:
    get: /
  response:
    type: html
`)
	id := seedPhase2Indexer(t, env.store, "bulk-def", "bulk-one", true, `{}`)

	body := get(t, env.handler, "/indexers").Body.String()
	if !strings.Contains(body, `<div class="bulk-bar" data-bulk-actions hidden>`) {
		t.Error("the bulk bar is visible before any selection")
	}
	if !strings.Contains(body, `name="ids" value="`+strconv.FormatInt(id, 10)+`"`) {
		t.Error("the row has no selection checkbox")
	}

	token, cookie := csrfFrom(t, env.handler, "/indexers")
	recorder := postForm(t, env.handler, "/indexers/bulk", url.Values{
		"csrf":   {token},
		"ids":    {strconv.FormatInt(id, 10)},
		"action": {"disable"},
	}, cookie)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("bulk disable = %d: %s", recorder.Code, recorder.Body.String())
	}
	updated, err := env.store.GetIndexer(context.Background(), id)
	if err != nil || updated.Enabled {
		t.Fatalf("bulk disable left enabled=%v err=%v", updated.Enabled, err)
	}
}

func TestIndexerDetailWorkPage(t *testing.T) {
	env := newNativeEnv(t)
	seedDefinition(t, env.store, "detail-def", "", `schema: 1
meta:
  id: detail-def
  name: Detail Site
  type: public
  protocol: torrent
  urls: [https://detail.example/]
settings:
  - { name: uploader, type: text, label: Uploader, default: anonymous }
categories:
  - { site: "1", standard: 5000, desc: "TV" }
  - { site: "2", standard: 2000, desc: "Movies" }
search:
  request:
    get: /
  response:
    type: html
`)
	id := seedPhase2Indexer(t, env.store, "detail-def", "detail-site", true, `{"url":"https://detail.example/","uploader":"EZTV"}`)
	ctx := context.Background()
	if err := env.store.UpsertIndexerState(ctx, store.IndexerState{
		IndexerID: id, Circuit: "closed", LastError: "HTTP 403: forbidden",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := env.store.RecordIndexerHistory(ctx, store.IndexerHistory{
		IndexerID: id, Kind: "run", Status: "error", StartedAt: time.Now().Add(-time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := env.store.RecordIndexerHistory(ctx, store.IndexerHistory{
		IndexerID: id, Kind: "search", Status: "ok", LatencyMS: 800, StartedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := env.store.RecordIndexerHistory(ctx, store.IndexerHistory{
		IndexerID: id, Kind: "grab", Status: "ok", StartedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	rel := store.Release{
		DedupKey: "detail-release-1", Title: "Detail.Show.S01E01.1080p", SearchTitle: "detail show s01e01 1080p",
		Size: 1024 * 1024 * 100, Seeders: 12, Category: "5040", Protocol: "torrent",
		PublishedAt: time.Now().Add(-2 * time.Hour), FirstSeenAt: time.Now().Add(-2 * time.Hour),
	}
	if _, err := env.store.UpsertRelease(ctx, &rel); err != nil {
		t.Fatal(err)
	}
	if err := env.store.RecordReleaseSource(ctx, store.ReleaseSource{ReleaseID: rel.ID, IndexerID: id}); err != nil {
		t.Fatal(err)
	}

	body := get(t, env.handler, "/indexers/"+strconv.FormatInt(id, 10)).Body.String()
	for _, want := range []string{
		"Detail Site", "Problem", "The site refuses requests.",
		"Last update", "Next update", "App searches (24 h)", "Average time", "800 ms", "Grabs",
		"Filters", "uploader", "EZTV", "TV", "Movies",
		"Latest releases", "Detail.Show.S01E01.1080p",
		"Advanced and diagnostics", "Detail Site", "Detail Site", "Full last error",
		"HTTP 403: forbidden", "Site support", "detail-def", "Connector",
		"Requests per host", "Connect an app", `href="/applications/new"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("indexer detail missing %q", want)
		}
	}
	visible := stripDetails(body)
	if strings.Contains(visible, "Full last error") || strings.Contains(visible, "Requests per host") {
		t.Error("the diagnostics content is visible outside Advanced")
	}
}

func TestComponentsRender(t *testing.T) {
	tmpl := templateSets[i18n.EN].pages["indexer"]
	render := func(name string, data any) string {
		t.Helper()
		var b strings.Builder
		if err := tmpl.ExecuteTemplate(&b, name, data); err != nil {
			t.Fatalf("component %s: %v", name, err)
		}
		return b.String()
	}

	cases := []struct {
		name string
		data any
		want []string
	}{
		{"status-dot", statusDot{State: "ok", Label: "Working"}, []string{"status-dot status-ok", "Working"}},
		{"problem", uiProblem{Text: "Rate limited.", ActionLabel: "Reduce", ActionURL: "/settings"}, []string{"Rate limited.", "Reduce", `href="/settings"`}},
		{"empty", uiEmpty{Title: "Nothing", Text: "Add one.", ActionLabel: "Add", ActionURL: "/new"}, []string{"Nothing", "Add one.", `href="/new"`}},
		{"next-step", uiNextStep{Text: "Next.", ActionLabel: "Go", ActionURL: "/go"}, []string{"next-step", "Next.", "Go"}},
		{"category-picker", struct {
			Categories []categoryChoice
			Help       string
		}{Categories: []categoryChoice{{ID: "5000", Label: "TV", Checked: true}}, Help: "Pick"}, []string{"category-picker", "TV", "checked"}},
		{"advanced-open", advancedView{Title: "Advanced tools"}, []string{`<details class="advanced">`, "Advanced tools"}},
		{"confirm", struct {
			Action, Message, CSRF, Class, Label string
		}{Action: "/delete", Message: "Sure?", CSRF: "token", Class: "danger", Label: "Delete"}, []string{`data-confirm="Sure?"`, "Delete", `name="csrf" value="token"`}},
		{"secret-field", struct {
			URL, ID, Name string
			HasSecret     bool
			Required      bool
		}{URL: "/secret", ID: "key", Name: "key", HasSecret: true}, []string{"secret-field", "configured"}},
	}
	for _, tc := range cases {
		out := render(tc.name, tc.data)
		for _, want := range tc.want {
			if !strings.Contains(out, want) {
				t.Errorf("%s output missing %q: %s", tc.name, want, out)
			}
		}
	}
	if out := render("advanced-close", advancedView{}); !strings.Contains(out, "</details>") {
		t.Error("advanced-close does not close the block")
	}
}

func TestSettingLabelsExistInAllLocales(t *testing.T) {
	names := []string{
		"username", "password", "apikey", "api_key", "passkey", "cookie",
		"uploader", "language", "url", "query", "default_category",
		"default-category", "categories", "user_agent", "allow_private",
		"filter", "category", "proxy",
	}
	for _, lang := range i18n.Supported() {
		for _, name := range names {
			key := "setting." + name + ".label"
			if got := i18n.T(lang, key); got == key {
				t.Errorf("%s is missing %s", lang, key)
			}
		}
	}
}
