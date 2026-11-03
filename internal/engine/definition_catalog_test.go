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
    rows: { selector: "ul.topiclist.topics > li.row" }
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
			<li class="row"><a class="topictitle" href="/viewtopic.php?t=1">Serie A</a></li>
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
	if !result.More {
		t.Error("a full page must say that more rows follow")
	}
}

func TestDefinitionCatalogShortPageEndsTheForum(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeBody(t, w, []byte(`<ul class="topiclist topics">
			<li class="row"><a class="topictitle" href="/viewtopic.php?t=9">Ultima</a></li>
		</ul>`))
	}))
	defer server.Close()

	connector := newCatalogConnector(t, server.URL)
	result, err := connector.Catalog(context.Background(), CatalogRequest{Forum: "26", Start: 400})
	if err != nil {
		t.Fatalf("Catalog: %v", err)
	}
	if len(result.Entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(result.Entries))
	}
	if result.More {
		t.Error("a page shorter than page_size finishes the forum")
	}
	if result.Entries[0].Standard != 2040 {
		t.Errorf("category = %d, want 2040", result.Entries[0].Standard)
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
