package web

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/buzzqw/indexarr/internal/catalog"
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
