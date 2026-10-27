package engine

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/buzzqw/indexarr/internal/definition"
)

const ed2kTestDefinition = `schema: 1
meta:
  id: ed2k-test
  name: eD2k test
  type: private
  protocol: ed2k
  urls:
    - http://REPLACE/
capabilities:
  search: [q]
search:
  request:
    get: /search.php
    params:
      keywords: "{{ query.keywords }}"
  response:
    type: html
    rows: { selector: "ul.topics > li.row" }
    fields:
      title:   { selector: "a.title" }
      details: { selector: "a.title", attr: href, type: url }
    required: [title, details]
download:
  resolve_on: %s
%s  from: details
  extract:
    - ed2k: { selector: 'a[href^="ed2k://"]', attr: href }
      all: true
health:
  query: test
`

func ed2kTestConnector(t *testing.T, resolveOn string, handler http.Handler) *DefinitionConnector {
	t.Helper()
	expand := "  expand: file\n"
	if resolveOn != "search" {
		expand = ""
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	source := strings.ReplaceAll(fmt.Sprintf(ed2kTestDefinition, resolveOn, expand), "http://REPLACE/", server.URL+"/")
	def, err := definition.Parse([]byte(source))
	if err != nil {
		t.Fatalf("parse definition: %v", err)
	}
	connector, err := NewDefinitionConnector(DefinitionConfig{
		Indexer:    IndexerRef{Slug: def.Meta.ID, Name: def.Meta.Name},
		Definition: def,
		Transport:  NewTransport(TransportConfig{AllowPrivate: true}),
	})
	if err != nil {
		t.Fatalf("NewDefinitionConnector: %v", err)
	}
	return connector
}

func TestParseEd2kFileLink(t *testing.T) {
	link, ok := parseEd2kFileLink("ed2k://|file|Rare%20Movie%20(1974).mkv|700000000|0123456789abcdef0123456789abcdef|/")
	if !ok {
		t.Fatal("link was rejected")
	}
	if link.Name != "Rare Movie (1974).mkv" || link.Size != 700000000 || link.Hash != "0123456789abcdef0123456789abcdef" {
		t.Errorf("link = %+v", link)
	}
	if !strings.HasPrefix(canonicalEd2kLink(link), "ed2k://|file|Rare%20Movie") {
		t.Errorf("canonical link = %q", canonicalEd2kLink(link))
	}
	// A link copied from a script keeps the page's HTML entities.
	scripted, ok := parseEd2kFileLink("ed2k://|file|Bones - 6x12 - L&#39;amore &#40;BDMux&#41;.mkv|700|0123456789abcdef0123456789abcdef|/")
	if !ok || scripted.Name != "Bones - 6x12 - L'amore (BDMux).mkv" {
		t.Errorf("scripted link = %+v, ok = %v", scripted, ok)
	}
	for _, bad := range []string{
		"",
		"magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef",
		"ed2k://|file|name.mkv|0|0123456789abcdef0123456789abcdef|/",
		"ed2k://|file|name.mkv|10|not-a-hash|/",
	} {
		if _, ok := parseEd2kFileLink(bad); ok {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestSearchExpandsEveryEd2kFile(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/search.php":
			writeBody(t, w, []byte(`<html><body><ul class="topics">
<li class="row"><a class="title" href="/viewtopic.php?t=1">Bones - Stagione 6 [COMPLETA]</a></li>
</ul></body></html>`))
		case "/viewtopic.php":
			writeBody(t, w, []byte(`<html><body>
<a href="ed2k://|file|Bones.S06E01.mkv|100|11111111111111111111111111111111|/">Bones S06E01.mkv</a>
<a href="ed2k://|file|Bones.S06E02.mkv|200|22222222222222222222222222222222|/">Bones S06E02.mkv</a>
</body></html>`))
		default:
			http.NotFound(w, r)
		}
	})
	connector := ed2kTestConnector(t, "search", handler)
	items, err := connector.Search(context.Background(), SearchRequest{Mode: ModeSearch, Query: "Bones"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("items = %d, want 2 (%+v)", len(items), items)
	}
	if items[0].Ed2k == "" || items[0].Ed2kHash != "11111111111111111111111111111111" || items[0].Size != 100 {
		t.Errorf("first item = %+v", items[0])
	}
	if !strings.Contains(items[0].Title, "S06E01") || !strings.Contains(items[0].Title, "Bones - Stagione 6") {
		t.Errorf("first title = %q", items[0].Title)
	}
	if items[1].Ed2kHash != "22222222222222222222222222222222" {
		t.Errorf("second item = %+v", items[1])
	}
}

func TestGrabReturnsTheEd2kLink(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/search.php":
			writeBody(t, w, []byte(`<html><body><ul class="topics">
<li class="row"><a class="title" href="/viewtopic.php?t=2">Rare Movie</a></li>
</ul></body></html>`))
		case "/viewtopic.php":
			writeBody(t, w, []byte(`<html><body>
<a href="ed2k://|file|Rare%20Movie.mkv|700|dddddddddddddddddddddddddddddddd|/">Rare Movie</a>
</body></html>`))
		default:
			http.NotFound(w, r)
		}
	})
	connector := ed2kTestConnector(t, "grab", handler)
	items, err := connector.Search(context.Background(), SearchRequest{Mode: ModeSearch, Query: "Rare"})
	if err != nil || len(items) != 1 {
		t.Fatalf("Search: %v, items %d", err, len(items))
	}
	// With resolve_on: grab the search carries only the details page.
	if items[0].Ed2k != "" {
		t.Fatalf("search resolved the link eagerly: %+v", items[0])
	}
	payload, err := connector.Download(context.Background(), items[0])
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if payload.Ed2k != "ed2k://|file|Rare%20Movie.mkv|700|dddddddddddddddddddddddddddddddd|/" {
		t.Errorf("payload = %+v", payload)
	}
}

func TestEd2kLinksInTextFindsScriptArrays(t *testing.T) {
	text := `<a href="ed2k://|file|One.mkv|10|0123456789abcdef0123456789abcdef|/">One</a>
<script>filearray[0]='ed2k://|file|Two.mkv|20|11111111111111111111111111111111|/';
filearray[1]='ed2k://|file|Three.mkv|30|22222222222222222222222222222222|/';</script>`
	links := ed2kLinksInText(text)
	if len(links) != 3 {
		t.Fatalf("links = %d: %v", len(links), links)
	}
	for i, want := range []string{
		"ed2k://|file|One.mkv|10|0123456789abcdef0123456789abcdef|/",
		"ed2k://|file|Two.mkv|20|11111111111111111111111111111111|/",
		"ed2k://|file|Three.mkv|30|22222222222222222222222222222222|/",
	} {
		if links[i] != want {
			t.Errorf("link %d = %q, want %q", i, links[i], want)
		}
	}
}

func TestEd2kSourceTopicWithTorrentIsATorrentRow(t *testing.T) {
	base := Item{Protocol: ProtocolEd2k, Title: "Old Game", GUID: "topic-1", DetailsURL: "http://site/viewtopic.php?t=1"}

	magnet := expandItem(base, resolvedDownload{Magnet: "magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567&dn=Old+Game"})
	if magnet.Protocol != ProtocolTorrent || magnet.InfoHash == "" {
		t.Errorf("magnet row = protocol %q infohash %q, want a torrent row", magnet.Protocol, magnet.InfoHash)
	}

	first := expandItem(base, resolvedDownload{DownloadURL: "http://torrents.site/torrent/1.torrent"})
	second := expandItem(base, resolvedDownload{DownloadURL: "http://torrents.site/torrent/2.torrent"})
	if first.Protocol != ProtocolTorrent || second.Protocol != ProtocolTorrent {
		t.Errorf("torrent rows = %q / %q, want torrent", first.Protocol, second.Protocol)
	}
	if first.GUID == second.GUID || !strings.HasPrefix(first.GUID, "topic-1#") {
		t.Errorf("two .torrent files of one topic share GUID %q / %q", first.GUID, second.GUID)
	}
	if strings.Contains(first.GUID, "torrents.site") {
		t.Errorf("GUID %q leaks the download URL", first.GUID)
	}

	ed2k := expandItem(base, resolvedDownload{
		Ed2k:     "ed2k://|file|Old%20Game.iso|700|0123456789abcdef0123456789abcdef|/",
		Ed2kHash: "0123456789abcdef0123456789abcdef",
		Size:     700,
	})
	if ed2k.Protocol != ProtocolEd2k || ed2k.GUID != "ed2k:0123456789abcdef0123456789abcdef" {
		t.Errorf("ed2k row = protocol %q guid %q", ed2k.Protocol, ed2k.GUID)
	}

	usenet := Item{Protocol: ProtocolUsenet}
	applyResolvedDownload(&usenet, resolvedDownload{DownloadURL: "http://site/get.nzb"})
	if usenet.Protocol != ProtocolUsenet {
		t.Errorf("usenet row became %q", usenet.Protocol)
	}
}
