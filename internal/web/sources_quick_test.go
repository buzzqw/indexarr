package web

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestQuickAddDetectsKind(t *testing.T) {
	env := newTestEnv(t)
	token, cookie := csrfFrom(t, env.handler, "/sources")

	recorder := postForm(t, env.handler, "/sources/quick", url.Values{
		"csrf": {token},
		"url":  {"https://extto.org/browse/?filter=u=test"},
	}, cookie)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("quick add status = %d: %s", recorder.Code, recorder.Body.String())
	}
	sources, _ := env.store.ListSources(context.Background())
	if len(sources) != 1 || sources[0].Kind != "rss" || sources[0].Name != "extto.org" {
		t.Fatalf("sources = %#v", sources)
	}
	duplicate := postForm(t, env.handler, "/sources/quick", url.Values{
		"csrf": {token},
		"url":  {"https://extto.org/browse/?filter=u=test"},
	}, cookie)
	if duplicate.Code != http.StatusSeeOther || !strings.Contains(duplicate.Header().Get("Location"), "err=") {
		t.Fatalf("duplicate quick add = %d %q", duplicate.Code, duplicate.Header().Get("Location"))
	}
	sources, _ = env.store.ListSources(context.Background())
	if len(sources) != 1 {
		t.Fatalf("duplicate added a source: %#v", sources)
	}

	recorder = postForm(t, env.handler, "/sources/quick", url.Values{
		"csrf": {token},
		"url":  {"http://indexer.example/api"},
		"name": {"Indexer"},
	}, cookie)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("quick add torznab status = %d", recorder.Code)
	}
	sources, _ = env.store.ListSources(context.Background())
	if len(sources) != 2 {
		t.Fatalf("sources = %#v", sources)
	}
	found := false
	for _, source := range sources {
		if source.Name == "Indexer" && source.Kind == "torznab" {
			found = true
		}
	}
	if !found {
		t.Fatalf("torznab source = %#v", sources)
	}
}

func TestDiscoverFeedsPage(t *testing.T) {
	env := newTestEnv(t)
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `<html><head>
			<link rel="alternate" type="application/atom+xml" title="News feed" href="/atom.xml">
		</head></html>`)
	}))
	defer page.Close()
	token, cookie := csrfFrom(t, env.handler, "/sources")
	discover := get(t, env.handler, "/sources/discover?url="+url.QueryEscape(page.URL))
	if discover.Code != http.StatusOK || !strings.Contains(discover.Body.String(), page.URL+"/atom.xml") {
		t.Fatalf("discover = %d: %s", discover.Code, discover.Body.String())
	}
	recorder := postForm(t, env.handler, "/sources/quick", url.Values{
		"csrf": {token},
		"url":  {page.URL + "/atom.xml"},
		"kind": {"rss"},
		"name": {"News feed"},
	}, cookie)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("add discovered feed = %d", recorder.Code)
	}
	sources, _ := env.store.ListSources(context.Background())
	if len(sources) != 1 || sources[0].Name != "News feed" || sources[0].Kind != "rss" {
		t.Errorf("sources = %#v", sources)
	}
}
