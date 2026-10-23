package web

import (
	"html"
	"strings"
	"sync"
	"testing"

	"github.com/buzzqw/indexarr/internal/i18n"
)

// TestTemplatesRenderInEveryLanguage exercises the full handler path for each
// catalog. Before the cache became lazy these sets were compiled at startup;
// now the first request per language compiles it, and this test proves the T
// binding still follows the request language.
func TestTemplatesRenderInEveryLanguage(t *testing.T) {
	env := newTestEnv(t)
	for _, lang := range i18n.Supported() {
		recorder := get(t, env.handler, "/?lang="+string(lang))
		if recorder.Code != 200 {
			t.Fatalf("GET /?lang=%s = %d: %s", lang, recorder.Code, recorder.Body.String())
		}
		body := recorder.Body.String()
		if !strings.Contains(body, `<html lang="`+lang.HTML()+`" dir="ltr">`) {
			t.Errorf("%s page has the wrong document language", lang)
		}
		for _, key := range []string{"shell.home", "shell.search", "shell.searchLabel"} {
			want := html.EscapeString(i18n.T(lang, key))
			if want == key {
				t.Fatalf("%s is missing the catalog key %s", lang, key)
			}
			if !strings.Contains(body, want) {
				t.Errorf("%s page missing %q (%s)", lang, want, key)
			}
		}
	}
}

// TestTemplateCacheFallsBackToEnglish checks the unknown-tag path shared by the
// render helpers: an unsupported language returns the eager English set.
func TestTemplateCacheFallsBackToEnglish(t *testing.T) {
	english := templateSets.get(i18n.EN)
	if english == nil {
		t.Fatal("the English template set was not compiled")
	}
	if got := templateSets.get(i18n.Language("xx")); got != english {
		t.Error("an unknown language did not fall back to the English template set")
	}
}

// TestTemplateCacheCompilesLanguagesConcurrently uses a fresh cache so the
// first-use path really runs under the race detector. Each language must be
// compiled once and every caller must receive the same set.
func TestTemplateCacheCompilesLanguagesConcurrently(t *testing.T) {
	cache := newTemplateCache()
	langs := i18n.Supported()
	const workers = 8

	start := make(chan struct{})
	results := make([]*templateSet, len(langs)*workers)
	var wg sync.WaitGroup
	for i := range results {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results[i] = cache.get(langs[i%len(langs)])
		}()
	}
	close(start)
	wg.Wait()

	expected := make(map[i18n.Language]*templateSet, len(langs))
	for i, set := range results {
		lang := langs[i%len(langs)]
		if set == nil {
			t.Fatalf("%s returned a nil template set", lang)
		}
		if first, ok := expected[lang]; ok && first != set {
			t.Errorf("%s was compiled more than once", lang)
		}
		expected[lang] = set
		if len(set.pages) != len(pageNames) {
			t.Errorf("%s has %d pages, want %d", lang, len(set.pages), len(pageNames))
		}
	}
	for _, lang := range langs {
		if expected[lang] != cache.get(lang) {
			t.Errorf("%s was not cached", lang)
		}
	}
}
