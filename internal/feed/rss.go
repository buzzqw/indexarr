package feed

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/buzzqw/indexarr/internal/textutil"
)

type rssDocument struct {
	XMLName xml.Name `xml:"rss"`
	Channel struct {
		Title string    `xml:"title"`
		Items []rssItem `xml:"item"`
	} `xml:"channel"`
}

type rssItem struct {
	Title       string      `xml:"title"`
	GUID        guidField   `xml:"guid"`
	Link        string      `xml:"link"`
	PubDate     string      `xml:"pubDate"`
	Published   string      `xml:"published"`
	Updated     string      `xml:"updated"`
	Size        string      `xml:"size"`
	Categories  []string    `xml:"category"`
	Enclosure   enclosure   `xml:"enclosure"`
	Attrs       []torznabAt `xml:"attr"`
	MagnetURL   string      `xml:"magneturl"`
	InfoHash    string      `xml:"infohash"`
	Description string      `xml:"description"`
}

type guidField struct {
	Value       string `xml:",chardata"`
	IsPermaLink string `xml:"isPermaLink,attr"`
}

type enclosure struct {
	URL    string `xml:"url,attr"`
	Length string `xml:"length,attr"`
	Type   string `xml:"type,attr"`
}

// torznabAt matches <torznab:attr> and <newznab:attr>: Go's XML decoder matches
// the local name when the field tag carries no namespace.
type torznabAt struct {
	Name  string `xml:"name,attr"`
	Value string `xml:"value,attr"`
}

type atomDocument struct {
	XMLName xml.Name    `xml:"feed"`
	Title   string      `xml:"title"`
	Entries []atomEntry `xml:"entry"`
}

type atomEntry struct {
	ID        string     `xml:"id"`
	Title     string     `xml:"title"`
	Published string     `xml:"published"`
	Updated   string     `xml:"updated"`
	Links     []atomLink `xml:"link"`
	Category  []atomCat  `xml:"category"`
}

type atomLink struct {
	Href   string `xml:"href,attr"`
	Rel    string `xml:"rel,attr"`
	Type   string `xml:"type,attr"`
	Length string `xml:"length,attr"`
}

type atomCat struct {
	Term string `xml:"term,attr"`
}

// ParseRSS parses an RSS 2.0 feed, an Atom feed or a Torznab search response
// into normalised items. The input is sanitised first: scraped feeds often
// contain a raw & or an HTML entity that strict XML would reject.
func ParseRSS(data []byte) ([]Item, error) {
	data = bytes.TrimSpace(bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF}))
	if len(data) == 0 {
		return nil, fmt.Errorf("empty feed")
	}
	if LooksLikeHTML(data) {
		return nil, ErrHTMLPage
	}
	data = sanitizeXML(data)
	var rss rssDocument
	rssErr := xml.Unmarshal(data, &rss)
	if rssErr == nil {
		return itemsFromRSS(rss), nil
	}
	var atom atomDocument
	if atomErr := xml.Unmarshal(data, &atom); atomErr == nil {
		return itemsFromAtom(atom), nil
	}
	return nil, fmt.Errorf("parse feed: %w", rssErr)
}

func itemsFromRSS(doc rssDocument) []Item {
	items := make([]Item, 0, len(doc.Channel.Items))
	for _, raw := range doc.Channel.Items {
		item, ok := convertRSSItem(raw)
		if ok {
			items = append(items, item)
		}
	}
	return items
}

func convertRSSItem(raw rssItem) (Item, bool) {
	title := strings.TrimSpace(raw.Title)
	if title == "" {
		return Item{}, false
	}
	attrs := make(map[string]string, len(raw.Attrs))
	var categoryAttrs []string
	for _, attr := range raw.Attrs {
		name := strings.ToLower(strings.TrimSpace(attr.Name))
		if name == "" {
			continue
		}
		value := strings.TrimSpace(attr.Value)
		if _, exists := attrs[name]; !exists {
			attrs[name] = value
		}
		if name == "category" {
			categoryAttrs = append(categoryAttrs, value)
		}
	}

	guid := strings.TrimSpace(raw.GUID.Value)
	if guid == "" {
		guid = firstNonEmpty(attrs["guid"], raw.Link)
	}
	magnet := firstNonEmpty(
		magnetValue(attrs["magneturl"]),
		magnetValue(raw.MagnetURL),
		magnetValue(raw.Link),
		magnetValue(raw.Enclosure.URL),
		magnetValue(attrs["magnet"]),
	)
	hash := NormalizeInfoHash(firstNonEmpty(attrs["infohash"], raw.InfoHash))
	if hash == "" {
		hash = MagnetHash(magnet)
	}
	if magnet == "" && hash != "" {
		magnet = MagnetFromHash(hash, title)
	}

	torrentURL := ""
	if LooksLikeTorrentURL(raw.Enclosure.URL, raw.Enclosure.Type) {
		torrentURL = strings.TrimSpace(raw.Enclosure.URL)
	} else if LooksLikeTorrentURL(raw.Link, "") {
		torrentURL = strings.TrimSpace(raw.Link)
	}

	return Item{
		Title:      title,
		GUID:       guid,
		Link:       strings.TrimSpace(raw.Link),
		Magnet:     magnet,
		TorrentURL: torrentURL,
		InfoHash:   hash,
		Size:       firstSize(attrs["size"], raw.Size, raw.Enclosure.Length),
		Seeders:    atoiUnknown(attrs["seeders"]),
		Leechers:   atoiUnknown(firstNonEmpty(attrs["peers"], attrs["leechers"])),
		Category:   NormalizeCategory(append(categoryAttrs, raw.Categories...)...),
		Published:  ParseTime(firstNonEmpty(raw.PubDate, raw.Published, raw.Updated)),
		IMDbID:     textutil.NormalizeID(attrs["imdbid"]),
		TMDbID:     textutil.NormalizeID(attrs["tmdbid"]),
		TVDbID:     textutil.NormalizeID(attrs["tvdbid"]),
	}, true
}

func itemsFromAtom(doc atomDocument) []Item {
	items := make([]Item, 0, len(doc.Entries))
	for _, entry := range doc.Entries {
		title := strings.TrimSpace(entry.Title)
		if title == "" {
			continue
		}
		var enclosureURL, enclosureType, enclosureLength, alternate string
		for _, link := range entry.Links {
			switch strings.ToLower(link.Rel) {
			case "enclosure":
				enclosureURL = link.Href
				enclosureType = link.Type
				enclosureLength = link.Length
			case "alternate", "":
				if alternate == "" {
					alternate = link.Href
				}
			}
		}
		magnet := firstNonEmpty(magnetValue(alternate), magnetValue(enclosureURL))
		hash := MagnetHash(magnet)
		if magnet == "" && hash != "" {
			magnet = MagnetFromHash(hash, title)
		}
		torrentURL := ""
		if LooksLikeTorrentURL(enclosureURL, enclosureType) {
			torrentURL = enclosureURL
		} else if LooksLikeTorrentURL(alternate, "") {
			torrentURL = alternate
		}
		var categories []string
		for _, category := range entry.Category {
			categories = append(categories, category.Term)
		}
		items = append(items, Item{
			Title:      title,
			GUID:       firstNonEmpty(entry.ID, alternate),
			Link:       alternate,
			Magnet:     magnet,
			TorrentURL: torrentURL,
			InfoHash:   hash,
			Size:       firstSize(enclosureLength),
			Category:   NormalizeCategory(categories...),
			Published:  ParseTime(firstNonEmpty(entry.Published, entry.Updated)),
		})
	}
	return items
}

// FetchRSS downloads and parses a feed. If the URL is an HTML page, it first
// looks for a feed embedded in the page and then for an RSS/Atom link; only
// when neither exists does it report that the URL is not a feed.
func FetchRSS(ctx context.Context, client *http.Client, rawURL string, opts RequestOptions) ([]Item, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, fmt.Errorf("invalid feed URL %q", rawURL)
	}
	body, err := getBody(ctx, client, rawURL, opts)
	if err != nil {
		return nil, err
	}
	// ext.to and TorrentGalaxy are HTML listings with dedicated parsers, not
	// RSS feeds; they are matched by host and parsed before the feed decoder.
	if site := htmlSiteForURL(parsed); site != siteNone && LooksLikeHTML(body) {
		return fetchHTMLListing(ctx, client, parsed, body, opts, site)
	}
	items, parseErr := ParseRSS(body)
	if parseErr == nil {
		return items, nil
	}
	if !errors.Is(parseErr, ErrHTMLPage) {
		return nil, parseErr
	}

	if embedded := extractEmbeddedFeed(body); embedded != nil {
		if items, err := ParseRSS(embedded); err == nil {
			return items, nil
		}
	}

	candidates := discoverFeedURLs(body, parsed)
	var lastErr error
	for _, candidate := range candidates {
		candidateBody, err := getBody(ctx, client, candidate, opts)
		if err != nil {
			lastErr = err
			continue
		}
		items, err := ParseRSS(candidateBody)
		if err == nil {
			return items, nil
		}
		if errors.Is(err, ErrHTMLPage) {
			if embedded := extractEmbeddedFeed(candidateBody); embedded != nil {
				if items, embeddedErr := ParseRSS(embedded); embeddedErr == nil {
					return items, nil
				}
			}
		}
		lastErr = err
	}
	if len(candidates) > 0 {
		if lastErr != nil {
			return nil, fmt.Errorf("%s returned HTML; %d feed link(s) found but none could be read: %w",
				hostLabel(rawURL), len(candidates), lastErr)
		}
		return nil, fmt.Errorf("%s returned HTML; feed links were found but none was a readable feed", hostLabel(rawURL))
	}
	return nil, htmlFeedProblem(rawURL, body, opts.FlareSolverr != "")
}

// TestRSS downloads a feed and reports how many items it contains.
func TestRSS(ctx context.Context, client *http.Client, rawURL string, opts RequestOptions) (string, error) {
	items, err := FetchRSS(ctx, client, rawURL, opts)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Feed reachable, %d item(s)", len(items)), nil
}

func atoiUnknown(raw string) int64 {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return -1
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return -1
	}
	return value
}

func magnetValue(raw string) string {
	value := strings.TrimSpace(raw)
	if strings.HasPrefix(strings.ToLower(value), "magnet:") {
		return value
	}
	return ""
}

func firstSize(values ...string) int64 {
	for _, value := range values {
		if size := ParseSize(value); size > 0 {
			return size
		}
	}
	return 0
}
