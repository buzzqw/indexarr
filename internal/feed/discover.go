package feed

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	xhtml "golang.org/x/net/html"
)

// DiscoveredFeed is a feed candidate found on an HTML page.
type DiscoveredFeed struct {
	Title string
	URL   string
	Type  string
}

// SuggestKind guesses whether a URL is a Torznab endpoint or an RSS/HTML feed.
func SuggestKind(rawURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Host == "" {
		return "torznab"
	}
	subject := strings.ToLower(parsed.Host + parsed.Path + "?" + parsed.RawQuery)
	switch {
	case strings.Contains(subject, "extto"), strings.Contains(subject, "ext.to"),
		strings.Contains(subject, "torrentgalaxy"), strings.Contains(subject, "corsaro"):
		return "rss"
	case strings.Contains(subject, "rss"), strings.Contains(subject, "atom"),
		strings.Contains(subject, "feed"), strings.HasSuffix(strings.ToLower(parsed.Path), ".xml"):
		return "rss"
	default:
		return "torznab"
	}
}

// DiscoverFeeds fetches a page and returns the RSS/Atom links it advertises.
// If the URL itself is a feed, the URL is returned as the only candidate.
func DiscoverFeeds(ctx context.Context, rawURL string, opts RequestOptions, timeout time.Duration) ([]DiscoveredFeed, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, fmt.Errorf("URL must be an absolute http:// or https:// address")
	}
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	client := &http.Client{Timeout: timeout}
	body, status, err := getBodyStatus(ctx, client, parsed.String(), opts)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, describeHTTPError(status, body)
	}
	lower := bytes.ToLower(body)
	if bytes.Contains(lower, []byte("<rss")) || bytes.Contains(lower, []byte("<feed")) {
		return []DiscoveredFeed{{Title: parsed.Host, URL: parsed.String(), Type: "feed"}}, nil
	}
	document, err := xhtml.Parse(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("parse page: %w", err)
	}
	seen := map[string]bool{}
	var out []DiscoveredFeed
	add := func(title, href, kind string) {
		if href == "" || len(out) >= 30 {
			return
		}
		resolved, err := parsed.Parse(href)
		if err != nil {
			return
		}
		value := resolved.String()
		if seen[value] {
			return
		}
		seen[value] = true
		if strings.TrimSpace(title) == "" {
			title = resolved.Host
		}
		out = append(out, DiscoveredFeed{Title: strings.TrimSpace(title), URL: value, Type: kind})
	}
	walkHTML(document, func(node *xhtml.Node) {
		if node.Type != xhtml.ElementNode {
			return
		}
		if node.Data == "link" {
			rel := strings.ToLower(htmlNodeAttr(node, "rel"))
			kind := strings.ToLower(htmlNodeAttr(node, "type"))
			if strings.Contains(rel, "alternate") &&
				(strings.Contains(kind, "rss") || strings.Contains(kind, "atom") || strings.Contains(kind, "xml")) {
				add(htmlNodeAttr(node, "title"), htmlNodeAttr(node, "href"), "link")
			}
			return
		}
		if node.Data != "a" {
			return
		}
		href := htmlNodeAttr(node, "href")
		lowerHref := strings.ToLower(href)
		if strings.HasSuffix(lowerHref, ".xml") || strings.HasSuffix(lowerHref, ".rss") ||
			strings.HasSuffix(lowerHref, ".atom") || strings.Contains(lowerHref, "/rss") ||
			strings.Contains(lowerHref, "/feed") || strings.Contains(lowerHref, "/atom") {
			add(htmlNodeText(node), href, "anchor")
		}
	})
	if len(out) == 0 {
		return nil, fmt.Errorf("no RSS/Atom links found; try the direct feed URL")
	}
	return out, nil
}
