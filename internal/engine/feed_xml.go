package engine

import (
	"bytes"
	"html"
	"strings"
)

// browserViewerID marks the element a browser puts a non-HTML document in:
// Chrome wraps an XML or text response in its own viewer, and the document
// arrives as the escaped text of this element. It matters here because a feed
// behind a Cloudflare challenge is fetched by the browser, not by the client.
const browserViewerID = `id="webkit-xml-viewer-source-xml"`

// feedXML returns the XML document of a response body, with or without the
// browser's viewer around it. A body that is already XML comes back unchanged;
// a viewer page yields the document it holds, unescaped once, because the
// viewed text carries the document's own entities twice.
func feedXML(body []byte) []byte {
	if !bytes.Contains(body, []byte(browserViewerID)) {
		return body
	}
	text := string(body)
	text = text[strings.Index(text, browserViewerID):]
	if start := strings.IndexByte(text, '>'); start >= 0 {
		text = text[start+1:]
	}
	if end := strings.Index(text, "</div>"); end >= 0 {
		text = text[:end]
	}
	if strings.Contains(text, "&lt;") {
		text = html.UnescapeString(text)
	}
	text = strings.TrimSpace(text)
	if !strings.Contains(text, "<") {
		return body
	}
	return []byte(text)
}
