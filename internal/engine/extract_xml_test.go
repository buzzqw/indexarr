package engine

import (
	"strings"
	"testing"

	"github.com/buzzqw/indexarr/internal/definition"
)

func TestParseXMLDecodesLatin1(t *testing.T) {
	// TorrentDownloads still declares ISO-8859-1 while using Windows-1252
	// punctuation in practice. Both must decode to UTF-8 instead of failing
	// before any row is read.
	body := []byte("<?xml version=\"1.0\" encoding=\"iso-8859-1\"?>" +
		"<rss><channel><item><title>Caf\xe9 \x80</title></item></channel></rss>")
	root, err := parseXML(body)
	if err != nil {
		t.Fatalf("parseXML: %v", err)
	}
	nodes := lookupXMLNodes(root, "rss.channel.item.title")
	if len(nodes) != 1 {
		t.Fatalf("title nodes = %d, want 1", len(nodes))
	}
	if got, want := nodes[0].textContent(), "Café €"; got != want {
		t.Errorf("title = %q, want %q", got, want)
	}
}

func TestParseXMLRejectsUnknownCharset(t *testing.T) {
	body := []byte("<?xml version=\"1.0\" encoding=\"shift_jis\"?><rss/>")
	if _, err := parseXML(body); err == nil {
		t.Fatal("parseXML accepted an unsupported charset")
	}
}

func TestParseXMLIgnoresTrailingJunk(t *testing.T) {
	// TorrentDownloads appends a Cloudflare script after </rss>; the script
	// contains raw ampersands and must not fail an otherwise complete feed.
	body := []byte("<rss><channel><item><title>One</title></item></channel></rss>" +
		"<script>if (a && b) {}</script>")
	root, err := parseXML(body)
	if err != nil {
		t.Fatalf("parseXML: %v", err)
	}
	nodes := lookupXMLNodes(root, "rss.channel.item.title")
	if len(nodes) != 1 || nodes[0].textContent() != "One" {
		t.Fatalf("unexpected rows: %+v", nodes)
	}
}

// TestFeedXMLUnwrapsTheBrowserViewer covers a feed behind a Cloudflare
// challenge: the browser that solved it returns its own XML viewer, with the
// document as escaped text inside one element. Without the unwrap every row is
// lost and the source reports nothing.
func TestFeedXMLUnwrapsTheBrowserViewer(t *testing.T) {
	feed := `<rss version="2.0"><channel><title>BT4G - Search RSS for test</title>` +
		`<item><title>Example release</title><link>magnet:?xt=urn:btih:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa&amp;dn=Example</link></item>` +
		`</channel></rss>`
	escaped := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(feed)
	viewer := []byte(`<html><head><style>#webkit-xml-viewer-source-xml { display: none; }</style></head>` +
		`<body><div id="webkit-xml-viewer-source-xml">` + escaped + `</div></body></html>`)

	rows, err := extractXMLRows(feedXML(viewer), definition.Response{
		Type: "xml",
		Rows: definition.RowSelector{Path: "rss.channel.item"},
		Fields: map[string]definition.Field{
			"title":  {Path: "title"},
			"magnet": {Path: "link"},
		},
	}, nil)
	if err != nil {
		t.Fatalf("extractXMLRows: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want the wrapped item", len(rows))
	}
	if rows[0]["title"] != "Example release" {
		t.Fatalf("title = %q", rows[0]["title"])
	}
	if !strings.Contains(rows[0]["magnet"], "xt=urn:btih:aaaa") || !strings.Contains(rows[0]["magnet"], "dn=Example") {
		t.Fatalf("magnet = %q", rows[0]["magnet"])
	}
}

// TestFeedXMLLeavesADocumentAlone keeps the unwrap from touching a body that is
// already the feed, which is what a direct fetch or a solver with returnRawHtml
// hands back.
func TestFeedXMLLeavesADocumentAlone(t *testing.T) {
	body := []byte(`<?xml version="1.0"?><rss><channel><item><title>x</title></item></channel></rss>`)
	if got := feedXML(body); string(got) != string(body) {
		t.Fatalf("body changed: %q", got)
	}
}
