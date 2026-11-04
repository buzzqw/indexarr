package engine

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/buzzqw/indexarr/internal/definition"
)

const catalogDefinition = `schema: 1
meta:
  id: forum
  name: Forum
  type: private
  protocol: torrent
  urls: ["%s/"]
capabilities:
  search: [q]
search:
  request:
    get: /search.php
    params:
      keywords: "{{ query.keywords_raw }}"
  response:
    type: html
    rows: { selector: "ul.results > li" }
    fields:
      title: { selector: "a", filters: [trim] }
      details: { selector: "a", attr: href, type: url }
    required: [title, details]
catalog:
  forums:
    - { id: "52", standard: 5040 }
    - { id: "26", standard: 2040 }
  request:
    get: /viewforum.php
  forum_param: f
  start_param: start
  page_size: 2
  response:
    type: html
    rows: { selector: 'ul.topiclist.topics > li.row:not([class*="announce"]):not([class*="sticky"])' }
    fields:
      title: { selector: "a.topictitle", filters: [trim] }
      details: { selector: "a.topictitle", attr: href, type: url }
    required: [title, details]
`

func newCatalogConnector(t *testing.T, baseURL string) *DefinitionConnector {
	t.Helper()
	parsed, err := definition.Parse([]byte(strings.Replace(catalogDefinition, "%s", baseURL, 1)))
	if err != nil {
		t.Fatalf("parse definition: %v", err)
	}
	connector, err := NewDefinitionConnector(DefinitionConfig{
		Indexer:    IndexerRef{Slug: "forum", Name: "Forum"},
		Definition: parsed,
		Transport:  NewTransport(TransportConfig{AllowPrivate: true}),
	})
	if err != nil {
		t.Fatalf("NewDefinitionConnector: %v", err)
	}
	return connector
}

// The walk reads one forum page: the forum and the first row travel as query
// parameters, the title and the topic address come from the listing, and a
// page shorter than page_size marks the end of the forum.
func TestDefinitionCatalogReadsOnePage(t *testing.T) {
	var seen url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/viewforum.php" {
			http.NotFound(w, r)
			return
		}
		seen = r.URL.Query()
		writeBody(t, w, []byte(`<ul class="topiclist topics">
			<li class="row bg1 global-announce"><a class="topictitle" href="/viewtopic.php?t=7">Comunicazione</a></li>
			<li class="row"><a class="topictitle" href="/viewtopic.php?t=1">Serie A</a></li>
			<li class="row sticky"><a class="topictitle" href="/viewtopic.php?t=8">Regole</a></li>
			<li class="row"><a class="topictitle" href="/viewtopic.php?t=2">Serie B</a></li>
		</ul>`))
	}))
	defer server.Close()

	connector := newCatalogConnector(t, server.URL)
	result, err := connector.Catalog(context.Background(), CatalogRequest{Forum: "52", Start: 100})
	if err != nil {
		t.Fatalf("Catalog: %v", err)
	}
	if got := seen.Get("f"); got != "52" {
		t.Errorf("forum parameter = %q, want 52", got)
	}
	if got := seen.Get("start"); got != "100" {
		t.Errorf("start parameter = %q, want 100", got)
	}
	if len(result.Entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(result.Entries))
	}
	if result.Entries[0].Title != "Serie A" {
		t.Errorf("title = %q", result.Entries[0].Title)
	}
	wantURL := server.URL + "/viewtopic.php?t=1"
	if result.Entries[0].URL != wantURL {
		t.Errorf("url = %q, want %q", result.Entries[0].URL, wantURL)
	}
	if result.Entries[0].Forum != "52" || result.Entries[0].Standard != 5040 {
		t.Errorf("entry = %+v, want forum 52 category 5040", result.Entries[0])
	}
	if result.PageSize != 2 {
		t.Errorf("page size = %d, want 2", result.PageSize)
	}
	if !result.More {
		t.Error("a page with topics must say that more rows follow")
	}
	for _, entry := range result.Entries {
		if strings.Contains(entry.Title, "Comunicazione") || strings.Contains(entry.Title, "Regole") {
			t.Errorf("an announcement or a sticky topic was stored: %+v", entry)
		}
	}
}

func TestDefinitionCatalogEmptyPageEndsTheForum(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Past the last page the forum still shows the announcements and the
		// stickies, and no topic at all: the walk is over.
		writeBody(t, w, []byte(`<ul class="topiclist topics">
			<li class="row bg1 global-announce"><a class="topictitle" href="/viewtopic.php?t=7">Comunicazione</a></li>
			<li class="row sticky"><a class="topictitle" href="/viewtopic.php?t=8">Regole</a></li>
		</ul>`))
	}))
	defer server.Close()

	connector := newCatalogConnector(t, server.URL)
	result, err := connector.Catalog(context.Background(), CatalogRequest{Forum: "26", Start: 400})
	if err != nil {
		t.Fatalf("Catalog: %v", err)
	}
	if len(result.Entries) != 0 {
		t.Fatalf("entries = %d, want 0", len(result.Entries))
	}
	if result.More {
		t.Error("a page with no topic finishes the forum")
	}
}

func TestDefinitionCatalogRejectsEmptyForum(t *testing.T) {
	connector := newCatalogConnector(t, "http://127.0.0.1:1")
	if _, err := connector.Catalog(context.Background(), CatalogRequest{}); err == nil {
		t.Fatal("Catalog without a forum must fail")
	}
}

// The definition must declare the block: a connector without it says so
// instead of touching the network.
func TestDefinitionCatalogWithoutBlock(t *testing.T) {
	parsed, err := definition.Parse([]byte(`schema: 1
meta: { id: plain, name: Plain, type: public, protocol: torrent, urls: ["http://127.0.0.1:1/"] }
capabilities: { search: [q] }
search:
  request: { get: /search }
  response:
    type: html
    rows: { selector: "li" }
    fields:
      title: { selector: "a" }
    required: [title]
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	connector, err := NewDefinitionConnector(DefinitionConfig{Definition: parsed})
	if err != nil {
		t.Fatalf("NewDefinitionConnector: %v", err)
	}
	if _, err := connector.Catalog(context.Background(), CatalogRequest{Forum: "1"}); err == nil {
		t.Fatal("Catalog without a block must fail")
	}
}

// A fixed index topic is read as it is and its "next" link is followed: the
// DDUnlimited TV list is one topic over three pages, not a forum.
func TestDefinitionCatalogFollowsAFixedPage(t *testing.T) {
	var pages []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pages = append(pages, r.URL.String())
		switch r.URL.Path {
		case "/viewtopic.php":
			if r.URL.Query().Get("start") == "2" {
				writeBody(t, w, []byte(`<div class="postbody">
					<a class="postlink-local" href="/viewtopic.php?t=30">Serie C</a></div>`))
				return
			}
			writeBody(t, w, []byte(`<div class="postbody">
				<a class="postlink-local" href="/viewtopic.php?t=10">Serie A</a>
				<a class="postlink-local" href="/viewtopic.php?t=20">Serie B</a>
				<a class="right-box right" href="/viewtopic.php?t=61463&amp;start=2">Pagina 2</a></div>`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	definitionBody := `schema: 1
meta: { id: index, name: Index, type: private, protocol: ed2k, urls: ["` + server.URL + `/"] }
capabilities: { search: [q] }
search:
  request: { get: /search.php }
  response:
    type: html
    rows: { selector: "li" }
    fields:
      title: { selector: "a" }
    required: [title]
catalog:
  forums:
    - { id: "tv", url: "/viewtopic.php?t=61463", standard: 5000, next: "a.right-box.right" }
  page_size: 0
  response:
    type: html
    rows: { selector: 'a.postlink-local[href*="viewtopic.php?t="]' }
    fields:
      title:   { self: true, filters: [trim] }
      details: { self: true, attr: href, type: url }
    required: [title, details]
`
	parsed, err := definition.Parse([]byte(definitionBody))
	if err != nil {
		t.Fatalf("parse definition: %v", err)
	}
	connector, err := NewDefinitionConnector(DefinitionConfig{
		Definition: parsed,
		Transport:  NewTransport(TransportConfig{AllowPrivate: true}),
	})
	if err != nil {
		t.Fatalf("NewDefinitionConnector: %v", err)
	}
	result, err := connector.Catalog(context.Background(), CatalogRequest{Forum: "tv"})
	if err != nil {
		t.Fatalf("Catalog: %v", err)
	}
	if len(result.Entries) != 3 {
		t.Fatalf("entries = %d, want 3 across two pages", len(result.Entries))
	}
	if result.PageSize != 0 || result.More {
		t.Fatalf("static page result = %+v, want no page size and no more", result)
	}
	if len(pages) != 2 {
		t.Fatalf("pages fetched = %v, want the fixed page and its next", pages)
	}
}
