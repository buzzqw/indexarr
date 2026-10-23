package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/buzzqw/indexarr/internal/store"
)

func TestParseBulkEntriesLines(t *testing.T) {
	text := `
# comment
https://a.example/rss

My Feed | https://b.example/feed.xml
https://c.example/rss | Other Name
not-a-url
`
	entries, problems := parseBulkEntries(text)
	if len(entries) != 3 {
		t.Fatalf("entries = %d, want 3: %#v", len(entries), entries)
	}
	if entries[0].Name != "a.example" || entries[0].URL != "https://a.example/rss" || entries[0].Line != 3 {
		t.Errorf("first entry = %#v", entries[0])
	}
	if entries[1].Name != "My Feed" || entries[1].URL != "https://b.example/feed.xml" {
		t.Errorf("second entry = %#v", entries[1])
	}
	if entries[2].Name != "Other Name" || entries[2].URL != "https://c.example/rss" {
		t.Errorf("URL-first line = %#v", entries[2])
	}
	if len(problems) != 1 || !strings.Contains(problems[0], "line 7") || !strings.Contains(problems[0], "not-a-url") {
		t.Errorf("problems = %#v", problems)
	}
}

func TestParseBulkEntriesOPML(t *testing.T) {
	const opml = `<?xml version="1.0" encoding="UTF-8"?>
<opml version="2.0">
  <body>
    <outline text="Group">
      <outline text="Child feed" xmlUrl="https://c.example/rss"/>
      <outline title="Duplicate" xmlurl="https://c.example/rss"/>
    </outline>
    <outline title="Title feed" xmlUrl="https://d.example/rss"/>
    <outline xmlUrl="https://e.example/rss"/>
    <outline text="No URL here"/>
    <outline text="Broken" xmlUrl="not-a-url"/>
  </body>
</opml>`
	entries, problems := parseBulkEntries(opml)
	if len(entries) != 3 {
		t.Fatalf("entries = %d, want 3: %#v", len(entries), entries)
	}
	if entries[0].Name != "Child feed" || entries[0].URL != "https://c.example/rss" {
		t.Errorf("nested OPML entry = %#v", entries[0])
	}
	if entries[1].Name != "Title feed" || entries[1].URL != "https://d.example/rss" {
		t.Errorf("title OPML entry = %#v", entries[1])
	}
	if entries[2].Name != "e.example" || entries[2].URL != "https://e.example/rss" {
		t.Errorf("auto-named OPML entry = %#v", entries[2])
	}
	if len(problems) != 1 || !strings.Contains(problems[0], "OPML") {
		t.Errorf("problems = %#v", problems)
	}
}

func TestParseBulkEntriesEmpty(t *testing.T) {
	if _, problems := parseBulkEntries(""); len(problems) != 1 {
		t.Errorf("empty input problems = %#v", problems)
	}
	if _, problems := parseBulkEntries("# only a comment\n\n"); len(problems) != 1 {
		t.Errorf("comment-only input problems = %#v", problems)
	}
}

func TestUniqueSourceName(t *testing.T) {
	taken := map[string]bool{"example.org": true, "example.org (2)": true, "my feed": true}
	if got := uniqueSourceName("example.org", taken); got != "example.org (3)" {
		t.Errorf("got %q", got)
	}
	if got := uniqueSourceName("My Feed", taken); got != "My Feed (2)" {
		t.Errorf("case-insensitive duplicate = %q", got)
	}
}

func TestAutoFeedName(t *testing.T) {
	if got := autoFeedName("https://www.example.org/feed.xml"); got != "example.org" {
		t.Errorf("got %q", got)
	}
	if got := autoFeedName("not a url"); got != "RSS feed" {
		t.Errorf("got %q", got)
	}
}

func TestBulkAddFeeds(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	if _, err := env.store.CreateSource(ctx, store.Source{
		Name: "Existing", Kind: "rss", URL: "https://a.example/rss", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}

	token, cookie := csrfFrom(t, env.handler, "/sources/bulk")
	values := url.Values{
		"csrf":             {token},
		"feeds":            {"https://a.example/rss\nNew feed | https://b.example/feed.xml\nhttps://c.example/rss\nnot-a-url"},
		"interval_seconds": {"900"},
		"enabled":          {"on"},
	}
	recorder := postForm(t, env.handler, "/sources/bulk", values, cookie)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	for _, want := range []string{
		"2 feed(s) added", "1 already present", "1 not added",
		"invalid feed URL", "New feed",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("report missing %q", want)
		}
	}

	sources, err := env.store.ListSources(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 3 {
		t.Fatalf("sources = %d, want 3: %#v", len(sources), sources)
	}
	byName := map[string]store.Source{}
	for _, source := range sources {
		byName[source.Name] = source
	}
	if source, ok := byName["New feed"]; !ok || source.IntervalSeconds != 900 || !source.Enabled || source.Kind != "rss" {
		t.Errorf("New feed = %#v", source)
	}
	if _, ok := byName["c.example"]; !ok {
		t.Errorf("auto name missing: %#v", byName)
	}
	if _, ok := byName["Existing"]; !ok {
		t.Error("the pre-existing source was modified or removed")
	}
}

func TestBulkAddFeedsRedirectsOnSuccess(t *testing.T) {
	env := newTestEnv(t)
	token, cookie := csrfFrom(t, env.handler, "/sources/bulk")
	recorder := postForm(t, env.handler, "/sources/bulk", url.Values{
		"csrf":             {token},
		"feeds":            {"https://x.example/rss\nhttps://y.example/rss"},
		"interval_seconds": {"0"},
		"enabled":          {"on"},
	}, cookie)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
	if location := recorder.Header().Get("Location"); !strings.HasPrefix(location, "/sources?msg=") {
		t.Errorf("location = %q", location)
	}
	sources, err := env.store.ListSources(context.Background())
	if err != nil || len(sources) != 2 {
		t.Fatalf("sources = %d, %v", len(sources), err)
	}
}

func TestBulkAddOPMLThroughHandler(t *testing.T) {
	env := newTestEnv(t)
	token, cookie := csrfFrom(t, env.handler, "/sources/bulk")
	const opml = `<opml version="2.0"><body>
		<outline text="OPML A" xmlUrl="https://opml-a.example/rss"/>
		<outline text="OPML B" xmlUrl="https://opml-b.example/rss"/>
	</body></opml>`
	recorder := postForm(t, env.handler, "/sources/bulk", url.Values{
		"csrf":  {token},
		"feeds": {opml},
	}, cookie)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
	sources, _ := env.store.ListSources(context.Background())
	if len(sources) != 2 {
		t.Fatalf("sources = %d", len(sources))
	}
}

func TestBulkInvalidInterval(t *testing.T) {
	env := newTestEnv(t)
	token, cookie := csrfFrom(t, env.handler, "/sources/bulk")
	recorder := postForm(t, env.handler, "/sources/bulk", url.Values{
		"csrf":             {token},
		"feeds":            {"https://x.example/rss"},
		"interval_seconds": {"30"},
		"enabled":          {"on"},
	}, cookie)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "at least 60 seconds") {
		t.Error("interval error missing")
	}
	if sources, _ := env.store.ListSources(context.Background()); len(sources) != 0 {
		t.Errorf("sources = %d, want 0", len(sources))
	}
}
