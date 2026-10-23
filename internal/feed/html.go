package feed

import (
	"bytes"
	"errors"
	"fmt"
	stdhtml "html"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// ErrHTMLPage is returned when an indexer or feed URL answers with an HTML
// page instead of RSS/Atom XML.
var ErrHTMLPage = errors.New("input is an HTML page, not an RSS/Atom feed")

var (
	titlePattern    = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	tagPattern      = regexp.MustCompile(`(?is)<[^>]*>`)
	linkTagPattern  = regexp.MustCompile(`(?is)<link\b([^>]*)>`)
	anchorPattern   = regexp.MustCompile(`(?is)<a\b([^>]*)>(.*?)</a>`)
	htmlAttrPattern = regexp.MustCompile(`(?is)([a-zA-Z_:][-a-zA-Z0-9_:.]*)\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+))`)
	feedWordPattern = regexp.MustCompile(`(?i)\b(rss|atom|feed)\b`)
)

// LooksLikeHTML reports whether data is an HTML document rather than XML. It
// checks the document root, not the mere presence of "<html": a valid feed can
// mention an escaped HTML tag in a title or description.
func LooksLikeHTML(data []byte) bool {
	sample := data
	if len(sample) > 4096 {
		sample = sample[:4096]
	}
	text := strings.ToLower(strings.TrimSpace(string(sample)))
	text = strings.TrimPrefix(text, "\xef\xbb\xbf")
	if strings.HasPrefix(text, "<?xml") {
		if end := strings.Index(text, "?>"); end >= 0 {
			text = strings.TrimSpace(text[end+2:])
		}
	}
	for strings.HasPrefix(text, "<!--") {
		end := strings.Index(text, "-->")
		if end < 0 {
			break
		}
		text = strings.TrimSpace(text[end+3:])
	}
	return strings.HasPrefix(text, "<!doctype html") ||
		strings.HasPrefix(text, "<html") ||
		strings.HasPrefix(text, "<head") ||
		strings.HasPrefix(text, "<body")
}

// extractEmbeddedFeed pulls a raw <rss> or <feed> document out of an HTML
// page. Some sites render a feed through their own HTML viewer instead of
// serving application/rss+xml directly.
func extractEmbeddedFeed(page []byte) []byte {
	lower := bytes.ToLower(page)
	for _, tags := range [][2]string{
		{"<rss", "</rss>"},
		{"<feed", "</feed>"},
	} {
		start := bytes.Index(lower, []byte(tags[0]))
		if start < 0 {
			continue
		}
		end := bytes.Index(lower[start:], []byte(tags[1]))
		if end < 0 {
			continue
		}
		return page[start : start+end+len(tags[1])]
	}
	return nil
}

// discoverFeedURLs finds feed links in an HTML page: <link rel="alternate"
// type="application/rss+xml"> and anchors whose text or path says RSS/feed.
func discoverFeedURLs(page []byte, base *url.URL) []string {
	if base == nil {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	add := func(raw string) {
		if len(out) >= 5 {
			return
		}
		raw = strings.TrimSpace(stdhtml.UnescapeString(raw))
		if raw == "" {
			return
		}
		ref, err := url.Parse(raw)
		if err != nil {
			return
		}
		resolved := base.ResolveReference(ref)
		scheme := strings.ToLower(resolved.Scheme)
		if scheme != "http" && scheme != "https" {
			return
		}
		value := resolved.String()
		if value == base.String() || seen[value] {
			return
		}
		seen[value] = true
		out = append(out, value)
	}

	for _, tag := range linkTagPattern.FindAllSubmatch(page, -1) {
		attrs := htmlAttrs(string(tag[1]))
		kind := strings.ToLower(attrs["type"])
		if !strings.Contains(kind, "rss") && !strings.Contains(kind, "atom") {
			continue
		}
		rel := strings.ToLower(attrs["rel"])
		if rel == "" || strings.Contains(rel, "alternate") {
			add(attrs["href"])
		}
	}
	for _, tag := range anchorPattern.FindAllSubmatch(page, -1) {
		attrs := htmlAttrs(string(tag[1]))
		text := stripHTML(string(tag[2]))
		if isFeedAnchor(attrs["href"], text, attrs["title"]) {
			add(attrs["href"])
		}
	}
	return out
}

func isFeedAnchor(href, text, title string) bool {
	href = strings.TrimSpace(href)
	lower := strings.ToLower(href)
	if href == "" || strings.HasPrefix(lower, "#") ||
		strings.HasPrefix(lower, "javascript:") || strings.HasPrefix(lower, "mailto:") {
		return false
	}
	if feedWordPattern.MatchString(strings.ToLower(text + " " + title)) {
		return true
	}
	parsed, err := url.Parse(href)
	if err != nil {
		return false
	}
	path := strings.ToLower(strings.TrimRight(parsed.Path, "/"))
	switch path {
	case "/rss", "/feed", "/atom", "/rss.xml", "/feed.xml", "/atom.xml", "/index.xml":
		return true
	}
	return strings.HasSuffix(path, ".rss") || strings.HasSuffix(path, ".atom")
}

func htmlAttrs(raw string) map[string]string {
	out := map[string]string{}
	for _, match := range htmlAttrPattern.FindAllStringSubmatch(raw, -1) {
		value := match[2]
		if value == "" {
			value = match[3]
		}
		if value == "" {
			value = match[4]
		}
		out[strings.ToLower(match[1])] = value
	}
	return out
}

func stripHTML(text string) string {
	return tagPattern.ReplaceAllString(text, " ")
}

// htmlFeedProblem explains that the response is a web page, not a feed.
func htmlFeedProblem(rawURL string, page []byte, solverConfigured bool) error {
	host := hostLabel(rawURL)
	if isChallengeResponse(http.StatusOK, http.Header{"Content-Type": []string{"text/html"}}, page) {
		if solverConfigured {
			return fmt.Errorf("%s returned a Cloudflare challenge that FlareSolverr could not solve", host)
		}
		return fmt.Errorf("%s returned a Cloudflare challenge instead of a feed; configure FlareSolverr in Settings", host)
	}
	title := htmlTitle(page)
	if title != "" {
		return fmt.Errorf("%s returned the HTML page %q, not an RSS/Atom feed; use the feed URL (often /rss or /feed), not the website page", host, title)
	}
	return fmt.Errorf("%s returned HTML, not an RSS/Atom feed; use the feed URL (often /rss or /feed), not the website page", host)
}

func htmlTitle(page []byte) string {
	match := titlePattern.FindSubmatch(page)
	if match == nil {
		return ""
	}
	title := stdhtml.UnescapeString(stripHTML(string(match[1])))
	title = strings.Join(strings.Fields(title), " ")
	return truncateText(title, 120)
}

func hostLabel(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err == nil && parsed.Host != "" {
		return parsed.Host
	}
	return "the feed"
}

func truncateText(text string, max int) string {
	runes := []rune(text)
	if len(runes) <= max {
		return text
	}
	return string(runes[:max]) + "…"
}
