package web

import (
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/buzzqw/indexarr/internal/catalog"
	"github.com/buzzqw/indexarr/internal/indexersvc"
	"github.com/buzzqw/indexarr/internal/store"
)

func TestPagesAreCompressedAndStaticAssetsRevalidate(t *testing.T) {
	env := newTestEnv(t)

	request := httptest.NewRequest(http.MethodGet, "/indexers", nil)
	request.Header.Set("Accept-Encoding", "gzip")
	recorder := httptest.NewRecorder()
	env.handler.ServeHTTP(recorder, request)
	if got := recorder.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", got)
	}
	if got := recorder.Header().Get("Vary"); !strings.Contains(got, "Accept-Encoding") {
		t.Fatalf("Vary = %q, want Accept-Encoding", got)
	}
	reader, err := gzip.NewReader(recorder.Body)
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	body, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read gzip body: %v", err)
	}
	if !strings.Contains(string(body), "Indexers") {
		t.Error("the compressed page does not contain the indexer list")
	}

	plain := get(t, env.handler, "/static/app.css")
	etag := plain.Header().Get("ETag")
	if etag == "" {
		t.Fatal("static asset has no ETag")
	}
	if got := plain.Header().Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("Cache-Control = %q, want no-cache", got)
	}
	revalidate := httptest.NewRequest(http.MethodGet, "/static/app.css", nil)
	revalidate.Header.Set("If-None-Match", etag)
	again := httptest.NewRecorder()
	env.handler.ServeHTTP(again, revalidate)
	if again.Code != http.StatusNotModified {
		t.Fatalf("revalidation = %d, want 304", again.Code)
	}
}

func TestDeleteReturnsToTheCurrentFilter(t *testing.T) {
	env := newNativeEnv(t)
	seedDefinition(t, env.store, "delete-def", "", `schema: 1
meta:
  id: delete-def
  name: Delete Site
  type: public
  protocol: torrent
  urls: [https://delete.example/]
search:
  request:
    get: /
  response:
    type: html
`)
	id := seedPhase2Indexer(t, env.store, "delete-def", "delete-site", false, `{}`)

	token, cookie := csrfFrom(t, env.handler, "/indexers?state=disabled")
	recorder := postForm(t, env.handler, "/indexers/"+strconvI(id)+"/delete", url.Values{
		"csrf":   {token},
		"return": {"/indexers?state=disabled"},
	}, cookie)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("delete = %d: %s", recorder.Code, recorder.Body.String())
	}
	location := recorder.Header().Get("Location")
	if !strings.HasPrefix(location, "/indexers?") || !strings.Contains(location, "state=disabled") {
		t.Fatalf("Location = %q, want the disabled filter", location)
	}

	// A crafted return path must not turn the form into an open redirect.
	if got := sanitizeIndexerReturn("https://evil.example/"); got != "" {
		t.Fatalf("sanitizeIndexerReturn(absolute) = %q", got)
	}
	if got := sanitizeIndexerReturn("/applications"); got != "" {
		t.Fatalf("sanitizeIndexerReturn(other page) = %q", got)
	}
}

func TestAddGuideKeepsSeveralNeedsActive(t *testing.T) {
	env := newNativeEnv(t)
	index := []catalog.IndexEntry{
		{ID: "movies-site", Name: "MoviesSite", Source: "native", Type: "public", Protocol: "torrent", State: "verificata", Categories: []string{"movies"}},
		{ID: "books-site", Name: "BooksSite", Source: "native", Type: "public", Protocol: "torrent", State: "verificata", Categories: []string{"books"}},
		{ID: "music-site", Name: "MusicSite", Source: "native", Type: "public", Protocol: "torrent", State: "verificata", Categories: []string{"music"}},
	}
	request := httptest.NewRequest(http.MethodGet, "/indexers/new?need=movies,books", nil)
	guide := env.server.buildAddGuide(request, index, map[string]store.Indexer{}, []string{"movies", "books"})
	if len(guide.Recommended) != 2 {
		t.Fatalf("recommended = %d, want the union of movies and books", len(guide.Recommended))
	}
	names := map[string]bool{}
	for _, site := range guide.Recommended {
		names[site.ID] = true
	}
	if !names["movies-site"] || !names["books-site"] || names["music-site"] {
		t.Fatalf("recommended = %v, want movies and books only", names)
	}
	active := 0
	for _, need := range guide.Needs {
		if need.Active {
			active++
		}
	}
	if active != 2 {
		t.Fatalf("active need cards = %d, want 2", active)
	}
}

func TestIndexerFormPutsCredentialsFirst(t *testing.T) {
	env := newNativeEnv(t)
	request := httptest.NewRequest(http.MethodGet, "/indexers/new?definition=torrentleech", nil)
	form, err := env.server.buildCompactIndexerForm(request, "torrentleech", nil, nil, nil, "")
	if err != nil {
		t.Fatalf("buildCompactIndexerForm: %v", err)
	}
	names := map[string]bool{}
	for _, field := range form.AccessFields {
		names[field.Name] = true
	}
	if !names["username"] || !names["password"] {
		t.Fatalf("access fields = %v, want username and password", names)
	}
	for _, field := range form.RequiredFields {
		if field.Name == "username" || field.Name == "password" {
			t.Fatalf("credential %q is still mixed with the ordinary required fields", field.Name)
		}
	}

	body := stripDetails(get(t, env.handler, "/indexers/new?definition=torrentleech").Body.String())
	for _, want := range []string{`name="setting_username"`, `name="setting_password"`, "Site access"} {
		if !strings.Contains(body, want) {
			t.Errorf("credential form missing %q outside Advanced", want)
		}
	}
}

func TestIndexerDetailShowsHealthAddressAndAccess(t *testing.T) {
	env := newNativeEnv(t)
	entry, ok := catalog.Get("torrentleech")
	if !ok {
		t.Fatal("torrentleech is not in the catalog")
	}
	seedDefinition(t, env.store, "torrentleech", "", string(entry.Body))
	id := seedPhase2Indexer(t, env.store, "torrentleech", "torrentleech-test", true, `{"url":"https://www.torrentleech.org/"}`)

	body := get(t, env.handler, "/indexers/"+strconvI(id)).Body.String()
	visible := stripDetails(body)
	for _, want := range []string{
		"Source health", "Source log", "https://www.torrentleech.org/",
		"Site access", "Enter credentials", "Missing", "Access is missing:",
	} {
		if !strings.Contains(visible, want) {
			t.Errorf("detail page missing %q outside Advanced", want)
		}
	}
}

const testSmartfeedURL = "https://mircrew-releases.org/smartfeed/feed?u=3923&e=Y658PJFFGOWo3l4l7b6HvF2zUmki9YhXUS3M8d73g&f=26&f=28&t=2&x=200"

func TestSmartfeedRSSIsDetectedAndAddressIsShown(t *testing.T) {
	env := newNativeEnv(t)
	entry, ok := catalog.Get("rss")
	if !ok {
		t.Fatal("rss is not in the catalog")
	}
	seedDefinition(t, env.store, "rss", "", string(entry.Body))
	priority := 25
	enabled := true
	created, err := env.server.svc.Create(context.Background(), indexersvc.Input{
		DefinitionID: "rss",
		Name:         "Mircrew feed",
		Slug:         "mircrew-feed",
		Protocol:     "torrent",
		Priority:     &priority,
		Enabled:      &enabled,
		Settings:     map[string]string{"url": testSmartfeedURL},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	body := get(t, env.handler, "/indexers/"+strconvI(created.ID)).Body.String()
	if strings.Contains(body, "example.invalid") {
		t.Error("the overview still shows the definition placeholder instead of the stored URL")
	}
	if !strings.Contains(body, "mircrew-releases.org") {
		t.Error("the overview does not show the stored feed address")
	}
	if !strings.Contains(body, "Switch to Smartfeed") {
		t.Error("the overview does not offer the Smartfeed conversion")
	}

	recorder := get(t, env.handler, "/indexers/"+strconvI(created.ID)+"/smartfeed")
	if recorder.Code != http.StatusFound {
		t.Fatalf("smartfeed conversion = %d, want 302", recorder.Code)
	}
	if !strings.HasPrefix(recorder.Header().Get("Location"), "/indexers/smartfeed?plan=") {
		t.Fatalf("Location = %q", recorder.Header().Get("Location"))
	}
}

func TestRSSCreateRedirectsSmartfeedURL(t *testing.T) {
	env := newNativeEnv(t)
	token, cookie := csrfFrom(t, env.handler, "/indexers/new?definition=rss")
	recorder := postForm(t, env.handler, "/indexers", url.Values{
		"csrf":          {token},
		"definition_id": {"rss"},
		"name":          {"Mircrew feed"},
		"url":           {testSmartfeedURL},
	}, cookie)
	if recorder.Code != http.StatusFound {
		t.Fatalf("create = %d: %s", recorder.Code, recorder.Body.String())
	}
	if !strings.HasPrefix(recorder.Header().Get("Location"), "/indexers/smartfeed?plan=") {
		t.Fatalf("Location = %q, want the Smartfeed form", recorder.Header().Get("Location"))
	}
}

func TestAddGuideSearchIsAtTheTopAndFormsGiveFeedback(t *testing.T) {
	env := newNativeEnv(t)
	body := get(t, env.handler, "/indexers/new").Body.String()
	search := strings.Index(body, "guide-search")
	needs := strings.Index(body, "need-grid")
	if search < 0 || needs < 0 {
		t.Fatalf("guide markers missing: search=%d needs=%d", search, needs)
	}
	if search > needs {
		t.Error("the catalogue search is below the need cards; it belongs at the top")
	}
	if !strings.Contains(body, `data-working-label="Working…"`) {
		t.Error("the layout does not carry the working label for submit feedback")
	}
	if !strings.Contains(body, `data-secret-ready=`) {
		t.Error("the layout does not carry the secret-ready hint")
	}
}

func TestIndexerUpdateSavesNewPassword(t *testing.T) {
	env := newNativeEnv(t)
	entry, ok := catalog.Get("mircrew")
	if !ok {
		t.Fatal("mircrew is not in the catalog")
	}
	seedDefinition(t, env.store, "mircrew", "mircrew", string(entry.Body))
	priority := 25
	enabled := true
	created, err := env.server.svc.Create(context.Background(), indexersvc.Input{
		DefinitionID: "mircrew",
		Name:         "MirCrew test",
		Slug:         "mircrew-test",
		Protocol:     "torrent",
		Priority:     &priority,
		Enabled:      &enabled,
		Settings:     map[string]string{"username": "buzzqw", "password": "old-password"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	token, cookie := csrfFrom(t, env.handler, "/indexers/"+strconvI(created.ID)+"/edit")
	edit := get(t, env.handler, "/indexers/"+strconvI(created.ID)+"/edit").Body.String()
	if !strings.Contains(edit, "data-secret-save") {
		t.Error("the edit form has no Save button next to the password field")
	}
	recorder := postForm(t, env.handler, "/indexers/"+strconvI(created.ID), url.Values{
		"csrf":                 {token},
		"definition_id":        {"mircrew"},
		"name":                 {"MirCrew test"},
		"setting_username":     {"buzzqw"},
		"setting_password":     {"new-password"},
		"setting_flaresolverr": {""},
	}, cookie)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("update = %d: %s", recorder.Code, recorder.Body.String())
	}
	value, ok := env.server.indexerSecretValue(context.Background(), created.ID, "password")
	if !ok || value != "new-password" {
		t.Fatalf("stored password = %q (found %v), want the new value", value, ok)
	}
}

func TestProblemFilterOffersTestAllProblems(t *testing.T) {
	env := newNativeEnv(t)
	seedDefinition(t, env.store, "problem-def", "", `schema: 1
meta:
  id: problem-def
  name: Problem Site
  type: public
  protocol: torrent
  urls: [https://problem.example/]
search:
  request:
    get: /
  response:
    type: html
`)
	id := seedPhase2Indexer(t, env.store, "problem-def", "problem-site", true, `{}`)
	ctx := context.Background()
	if err := env.store.UpsertIndexerState(ctx, store.IndexerState{
		IndexerID: id, Circuit: "closed", LastError: "HTTP 403: forbidden",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := env.store.RecordIndexerHistory(ctx, store.IndexerHistory{
		IndexerID: id, Kind: "run", Status: "error", Error: "HTTP 403: forbidden",
		StartedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	problem := get(t, env.handler, "/indexers?state=problem").Body.String()
	if !strings.Contains(problem, `action="/indexers/test-problems"`) {
		t.Error("the Problems view has no Test problems button")
	}
	all := get(t, env.handler, "/indexers").Body.String()
	if strings.Contains(all, `action="/indexers/test-problems"`) {
		t.Error("the Test problems button is shown outside the Problems view")
	}
}

func TestIndexerDetailShowsPausedRetry(t *testing.T) {
	env := newNativeEnv(t)
	seedDefinition(t, env.store, "retry-def", "", `schema: 1
meta:
  id: retry-def
  name: Retry Site
  type: public
  protocol: torrent
  urls: [https://retry.example/]
search:
  request:
    get: /
  response:
    type: html
`)
	id := seedPhase2Indexer(t, env.store, "retry-def", "retry-site", true, `{}`)
	ctx := context.Background()
	base := time.Now().Add(time.Minute)
	for i := 0; i < 9; i++ {
		if _, err := env.store.RecordIndexerHistory(ctx, store.IndexerHistory{
			IndexerID: id, Kind: "run", Status: "error", Error: "still broken",
			StartedAt: base.Add(time.Duration(i) * time.Hour),
		}); err != nil {
			t.Fatal(err)
		}
	}
	body := get(t, env.handler, "/indexers/"+strconvI(id)).Body.String()
	for _, want := range []string{"Automatic retry", "paused: fix it and press Test", "Automatic retry stopped after 9 failed attempts"} {
		if !strings.Contains(body, want) {
			t.Errorf("detail page missing %q", want)
		}
	}
}

func TestMaintenanceShowsUsefulSources(t *testing.T) {
	env := newNativeEnv(t)
	for _, id := range []string{"useful-a", "useful-b"} {
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
	a := seedPhase2Indexer(t, env.store, "useful-a", "useful-a", true, `{}`)
	seedPhase2Indexer(t, env.store, "useful-b", "useful-b", true, `{}`)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if _, err := env.store.RecordIndexerHistory(ctx, store.IndexerHistory{
			IndexerID: a, Kind: "grab", Status: "ok", StartedAt: time.Now(),
		}); err != nil {
			t.Fatal(err)
		}
	}
	body := get(t, env.handler, "/settings?section=maintenance&lang=it").Body.String()
	for _, want := range []string{"Fonti più utili", "useful-a", "3 scarichi", "Totale: 3 scarichi", "Web"} {
		if !strings.Contains(body, want) {
			t.Errorf("maintenance page missing %q", want)
		}
	}
}
