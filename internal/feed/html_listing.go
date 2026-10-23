package feed

import (
	"bytes"
	"context"
	"fmt"
	stdhtml "html"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	xhtml "golang.org/x/net/html"
)

// htmlSite identifies the HTML listings that Gextto reads with dedicated
// parsers. Their pages are not RSS, so indexarr uses the same approach.
type htmlSite int

const (
	siteNone htmlSite = iota
	siteExtTo
	siteTorrentGalaxy
	siteCorsaro
)

func htmlSiteForURL(u *url.URL) htmlSite {
	if u == nil {
		return siteNone
	}
	host := strings.ToLower(u.Hostname())
	switch {
	case strings.Contains(host, "extto"), strings.Contains(host, "ext.to"):
		return siteExtTo
	case strings.Contains(host, "torrentgalaxy"):
		return siteTorrentGalaxy
	case strings.Contains(host, "corsaro"):
		return siteCorsaro
	}
	return siteNone
}

func fetchHTMLListing(ctx context.Context, client *http.Client, base *url.URL, body []byte, opts RequestOptions, site htmlSite) ([]Item, error) {
	document, err := xhtml.Parse(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("%s: parse HTML listing: %w", hostLabel(base.String()), err)
	}
	switch site {
	case siteExtTo:
		return parseExtToListing(ctx, client, document, base, opts)
	case siteTorrentGalaxy:
		return parseTorrentGalaxyListing(ctx, client, document, base, opts)
	case siteCorsaro:
		return parseCorsaroListing(ctx, client, document, base, opts)
	default:
		return nil, nil
	}
}

type listingDetail struct {
	title string
	url   string
}

// parseExtToListing pairs the title link and the magnet link of each ext.to
// row. No detail page is needed when the row already carries the magnet.
func parseExtToListing(ctx context.Context, client *http.Client, document *xhtml.Node, base *url.URL, opts RequestOptions) ([]Item, error) {
	category := extToCategory(base)
	var items []Item
	var pending []listingDetail
	seen := map[string]bool{}

	walkHTML(document, func(node *xhtml.Node) {
		if node.Type != xhtml.ElementNode || node.Data != "tr" {
			return
		}
		var title, detail, magnet string
		walkHTML(node, func(anchor *xhtml.Node) {
			if anchor.Type != xhtml.ElementNode || anchor.Data != "a" {
				return
			}
			href := htmlNodeAttr(anchor, "href")
			if href == "" {
				return
			}
			if strings.HasPrefix(strings.ToLower(href), "magnet:") && magnet == "" {
				magnet = href
				return
			}
			if strings.Contains(href, "/post-detail/") && title == "" {
				text := htmlNodeText(anchor)
				if text == "" {
					text = htmlNodeAttr(anchor, "title")
				}
				if text != "" {
					title = text
					detail = href
				}
			}
		})
		if title == "" && magnet != "" {
			title = titleFromMagnet(magnet)
		}
		if title == "" {
			return
		}
		if magnet != "" {
			addHTMLItem(&items, seen, title, magnet, category)
			return
		}
		if detail != "" && len(pending) < 50 {
			if resolved, err := base.Parse(detail); err == nil {
				pending = append(pending, listingDetail{title: title, url: resolved.String()})
			}
		}
	})

	// Some ext.to layouts expose only direct magnet buttons (no title link):
	// recover the title from the magnet dn parameter.
	if len(items) == 0 && len(pending) == 0 {
		walkHTML(document, func(node *xhtml.Node) {
			if node.Type != xhtml.ElementNode || node.Data != "a" {
				return
			}
			href := htmlNodeAttr(node, "href")
			if !strings.HasPrefix(strings.ToLower(href), "magnet:") {
				return
			}
			title := strings.TrimSpace(htmlNodeText(node))
			if title = titleFromMagnet(href); title == "" {
				title = strings.TrimSpace(htmlNodeText(node))
			}
			if title != "" {
				addHTMLItem(&items, seen, title, href, category)
			}
		})
	}

	if len(pending) > 0 {
		details, err := fetchListingDetails(ctx, client, pending, opts, category)
		items = append(items, details...)
		if len(items) == 0 && err != nil {
			return nil, err
		}
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("%s: no releases found in the HTML listing; login may be required or the page layout changed", hostLabel(base.String()))
	}
	return items, nil
}

// parseTorrentGalaxyListing follows the post-detail links of a user page: the
// listing has no magnets, they are on the detail page.
func parseTorrentGalaxyListing(ctx context.Context, client *http.Client, document *xhtml.Node, base *url.URL, opts RequestOptions) ([]Item, error) {
	var pending []listingDetail
	walkHTML(document, func(node *xhtml.Node) {
		if len(pending) >= 50 || node.Type != xhtml.ElementNode || node.Data != "div" ||
			!htmlNodeHasClass(node, "tgxtablerow") {
			return
		}
		var title, detail string
		walkHTML(node, func(anchor *xhtml.Node) {
			if anchor.Type != xhtml.ElementNode || anchor.Data != "a" || title != "" {
				return
			}
			href := htmlNodeAttr(anchor, "href")
			if !strings.Contains(href, "post-detail") {
				return
			}
			text := htmlNodeText(anchor)
			if text == "" {
				return
			}
			title = text
			detail = href
		})
		if title == "" || detail == "" {
			return
		}
		if resolved, err := base.Parse(detail); err == nil {
			pending = append(pending, listingDetail{title: title, url: resolved.String()})
		}
	})
	if len(pending) == 0 {
		return nil, fmt.Errorf("%s: no post-detail rows found; login may be required or the page layout changed", hostLabel(base.String()))
	}
	items, err := fetchListingDetails(ctx, client, pending, opts, "")
	if len(items) == 0 {
		if err != nil {
			return nil, fmt.Errorf("%s: no magnets found in %d detail page(s): %w",
				hostLabel(base.String()), len(pending), err)
		}
		return nil, fmt.Errorf("%s: no magnets found in the detail pages", hostLabel(base.String()))
	}
	return items, nil
}

// parseCorsaroListing handles the classic Corsaro listing: title anchors point
// to /torrent/... or /torrents/... detail pages, and some rows carry a magnet
// directly. The detail pages are fetched with a small concurrency and a cache.
func parseCorsaroListing(ctx context.Context, client *http.Client, document *xhtml.Node, base *url.URL, opts RequestOptions) ([]Item, error) {
	var items []Item
	seen := map[string]bool{}
	var pending []listingDetail
	seenDetail := map[string]bool{}
	walkHTML(document, func(node *xhtml.Node) {
		if node.Type != xhtml.ElementNode || node.Data != "a" || len(pending) >= 50 {
			return
		}
		href := htmlNodeAttr(node, "href")
		if href == "" {
			return
		}
		title := htmlNodeText(node)
		if title == "" {
			title = htmlNodeAttr(node, "title")
		}
		if strings.HasPrefix(strings.ToLower(href), "magnet:") {
			if title == "" || strings.EqualFold(title, "magnet") {
				title = titleFromMagnet(href)
			}
			addHTMLItem(&items, seen, title, href, "")
			return
		}
		lower := strings.ToLower(href)
		if !strings.Contains(lower, "/torrent/") && !strings.Contains(lower, "/torrents/") {
			return
		}
		if title == "" {
			return
		}
		resolved, err := base.Parse(href)
		if err != nil {
			return
		}
		if seenDetail[resolved.String()] {
			return
		}
		seenDetail[resolved.String()] = true
		pending = append(pending, listingDetail{title: title, url: resolved.String()})
	})
	if len(pending) > 0 {
		details, err := fetchListingDetails(ctx, client, pending, opts, "")
		if err != nil && len(items) == 0 && len(details) == 0 {
			return nil, err
		}
		for _, item := range details {
			addHTMLItem(&items, seen, item.Title, item.Magnet, item.Category)
		}
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("%s: no releases found in the HTML listing; login may be required or the page layout changed", hostLabel(base.String()))
	}
	return items, nil
}

func fetchListingDetails(ctx context.Context, client *http.Client, refs []listingDetail, opts RequestOptions, category string) ([]Item, error) {
	type result struct {
		detail listingDetail
		magnet string
		err    error
	}
	jobs := make(chan listingDetail)
	results := make(chan result)
	workers := 6
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ref := range jobs {
				if magnet := cachedDetailMagnet(ref.url); magnet != "" {
					results <- result{detail: ref, magnet: magnet}
					continue
				}
				body, err := getBody(ctx, client, ref.url, opts)
				if err != nil {
					results <- result{detail: ref, err: err}
					continue
				}
				magnet := extractMagnet(body)
				if magnet == "" {
					results <- result{detail: ref, err: fmt.Errorf("no magnet in %s", hostLabel(ref.url))}
					continue
				}
				cacheDetailMagnet(ref.url, magnet)
				results <- result{detail: ref, magnet: magnet}
			}
		}()
	}
	go func() {
		for _, ref := range refs {
			jobs <- ref
		}
		close(jobs)
		wg.Wait()
		close(results)
	}()

	var items []Item
	var firstErr error
	seen := map[string]bool{}
	for res := range results {
		if res.err != nil {
			if firstErr == nil {
				firstErr = res.err
			}
			continue
		}
		addHTMLItem(&items, seen, res.detail.title, res.magnet, category)
	}
	if len(items) == 0 && firstErr != nil {
		return nil, firstErr
	}
	return items, nil
}

func addHTMLItem(out *[]Item, seen map[string]bool, title, magnet, category string) {
	if isPlaceholderMagnet(magnet) {
		return
	}
	hash := MagnetHash(magnet)
	if hash == "" || seen[hash] {
		return
	}
	seen[hash] = true
	*out = append(*out, Item{
		Title:    title,
		Magnet:   magnet,
		InfoHash: hash,
		Category: category,
		Seeders:  -1,
		Leechers: -1,
	})
}

// isPlaceholderMagnet is ext.to's anti-bot magnet: it has no real infohash and
// must never become a release.
func isPlaceholderMagnet(magnet string) bool {
	return strings.Contains(strings.ToLower(magnet), "aremousemovesmostlystraightlined")
}

func extToCategory(base *url.URL) string {
	if base == nil {
		return ""
	}
	for _, raw := range base.Query()["filter"] {
		value := strings.ToLower(strings.TrimSpace(raw))
		if !strings.HasPrefix(value, "c=") {
			continue
		}
		switch strings.TrimPrefix(value, "c=") {
		case "tv":
			return "5000"
		case "movies":
			return "2000"
		case "music":
			return "3000"
		case "games", "apps":
			return "4000"
		case "books":
			return "7000"
		case "anime":
			return "5070"
		case "docus":
			return "5080"
		case "xxx":
			return "6000"
		}
	}
	return ""
}

func titleFromMagnet(magnet string) string {
	parsed, err := url.Parse(magnet)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(parsed.Query().Get("dn"))
}

var magnetURLPattern = regexp.MustCompile(`(?i)magnet:\?[^"'\s<>\\]+`)

func extractMagnet(body []byte) string {
	text := stdhtml.UnescapeString(string(body))
	for _, candidate := range magnetURLPattern.FindAllString(text, -1) {
		candidate = strings.TrimRight(candidate, "&;,")
		if MagnetHash(candidate) != "" {
			// candidate is a substring of the unescaped page. Clone it before
			// returning/caching, otherwise the whole detail page stays alive as
			// the backing array of every cached magnet.
			return strings.Clone(candidate)
		}
	}
	return ""
}

// Detail magnets are cached for six hours: a user page lists the same releases
// every run and a detail request each time would hammer TorrentGalaxy.
type detailCacheEntry struct {
	magnet  string
	expires time.Time
}

var detailCache = struct {
	sync.Mutex
	entries map[string]detailCacheEntry
}{entries: map[string]detailCacheEntry{}}

const (
	detailCacheTTL = 6 * time.Hour
	detailCacheMax = 10000
)

func cachedDetailMagnet(key string) string {
	detailCache.Lock()
	defer detailCache.Unlock()
	entry, ok := detailCache.entries[key]
	if !ok {
		return ""
	}
	if time.Now().After(entry.expires) {
		delete(detailCache.entries, key)
		return ""
	}
	return entry.magnet
}

func cacheDetailMagnet(key, magnet string) {
	detailCache.Lock()
	defer detailCache.Unlock()
	if len(detailCache.entries) >= detailCacheMax {
		now := time.Now()
		for entryKey, entry := range detailCache.entries {
			if now.After(entry.expires) {
				delete(detailCache.entries, entryKey)
			}
		}
		if len(detailCache.entries) >= detailCacheMax {
			detailCache.entries = map[string]detailCacheEntry{}
		}
	}
	detailCache.entries[key] = detailCacheEntry{
		magnet:  magnet,
		expires: time.Now().Add(detailCacheTTL),
	}
}

func walkHTML(node *xhtml.Node, fn func(*xhtml.Node)) {
	if node == nil {
		return
	}
	if node.Type == xhtml.ElementNode && (node.Data == "script" || node.Data == "style") {
		return
	}
	fn(node)
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		walkHTML(child, fn)
	}
}

func htmlNodeAttr(node *xhtml.Node, key string) string {
	for _, attr := range node.Attr {
		if strings.EqualFold(attr.Key, key) {
			return attr.Val
		}
	}
	return ""
}

func htmlNodeHasClass(node *xhtml.Node, class string) bool {
	for _, token := range strings.Fields(htmlNodeAttr(node, "class")) {
		if token == class {
			return true
		}
	}
	return false
}

func htmlNodeText(node *xhtml.Node) string {
	var b strings.Builder
	var walk func(*xhtml.Node)
	walk = func(n *xhtml.Node) {
		if n.Type == xhtml.ElementNode && (n.Data == "script" || n.Data == "style") {
			return
		}
		if n.Type == xhtml.TextNode {
			b.WriteString(n.Data)
			b.WriteByte(' ')
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(node)
	return strings.Join(strings.Fields(b.String()), " ")
}
