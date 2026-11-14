package feed

import (
	"strings"
	"testing"
)

// TestSanitizeXMLKeepsCDATA: the sanitiser must not touch a CDATA section. Its
// text is literal, so escaping a raw & there does not make the document more
// parseable; it only changes the value a strict parser reads back. Knaben keeps
// the magnet in CDATA, and the escaped ampersand used to strip the name and the
// trackers from every stored link.
func TestSanitizeXMLKeepsCDATA(t *testing.T) {
	const magnet = "magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567&dn=Show&tr=udp%3A%2F%2Ftracker.example%3A1337%2Fannounce"
	input := `<rss><channel><item><title>Show</title>` +
		`<link><![CDATA[` + magnet + `]]></link></item></channel></rss>`

	out := string(sanitizeXML([]byte(input)))
	if !strings.Contains(out, "<![CDATA["+magnet+"]]>") {
		t.Errorf("the CDATA section was changed:\n%s", out)
	}
	if strings.Contains(out, "&amp;") {
		t.Errorf("the sanitiser escaped inside CDATA:\n%s", out)
	}
}

// TestSanitizeXMLEscapesOutsideCDATA keeps the original job: a raw & or an HTML
// entity in ordinary text still becomes valid XML.
func TestSanitizeXMLEscapesOutsideCDATA(t *testing.T) {
	out := string(sanitizeXML([]byte(`<category>TV & HD &nbsp; 1080p</category>`)))
	if !strings.Contains(out, "TV &amp; HD") {
		t.Errorf("a raw & outside CDATA was not escaped: %q", out)
	}
	if strings.Contains(out, "&nbsp;") {
		t.Errorf("a named entity was not mapped: %q", out)
	}
}

// TestParseRSSKeepsCDATAMagnet is the end-to-end shape of the Knaben bug: the
// parsed item carries the magnet the site published, with the real separators.
func TestParseRSSKeepsCDATAMagnet(t *testing.T) {
	const magnet = "magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567&dn=Show.S01E01&tr=udp%3A%2F%2Ftracker.example%3A1337%2Fannounce"
	feed := `<?xml version="1.0" encoding="UTF-8"?><rss version="2.0"><channel>
<item><title>Show.S01E01</title><guid isPermaLink="false">0123456789abcdef0123456789abcdef01234567</guid>
<link><![CDATA[` + magnet + `]]></link></item></channel></rss>`

	items, err := ParseRSS([]byte(feed))
	if err != nil {
		t.Fatalf("ParseRSS: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1", len(items))
	}
	if items[0].Magnet != magnet {
		t.Errorf("magnet = %q, want %q", items[0].Magnet, magnet)
	}
}
