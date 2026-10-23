package engine

import "testing"

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
