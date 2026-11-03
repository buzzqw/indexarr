package family

import (
	"context"
	"encoding/base32"
	"encoding/hex"
	"fmt"
	"net/url"
	"path"
	"strconv"
	"strings"

	"github.com/buzzqw/indexarr/internal/engine"
)

// TorznabConfig configures a generic Torznab API client: the same settings
// as a Newznab one. BaseURL is the API endpoint, for example
// https://indexer.example/api.
type TorznabConfig = NewznabConfig

// Torznab is the generic connector for direct Torznab endpoints. It speaks the
// same protocol as Newznab but produces torrent releases: infohash, magnet and
// a .torrent payload.
type Torznab struct {
	*newznabAPI
}

// NewTorznab validates cfg and builds a connector.
func NewTorznab(cfg TorznabConfig) (*Torznab, error) {
	api, err := newNewznabAPI("torznab", engine.ProtocolTorrent, cfg)
	if err != nil {
		return nil, err
	}
	return &Torznab{newznabAPI: api}, nil
}

// ParseTorznabCaps parses a Torznab caps document. Torznab reuses the Newznab
// caps schema, so this is a named entry point for callers and tests.
func ParseTorznabCaps(body []byte) (*Caps, error) {
	return ParseCaps(body)
}

// Search implements engine.Connector. The season and episode of the request
// are kept on every item, so Download can send them back with t=get.
func (t *Torznab) Search(ctx context.Context, req engine.SearchRequest) ([]engine.Item, error) {
	items, err := t.search(ctx, req, t.parseItem)
	if err != nil {
		return nil, err
	}
	if req.Season > 0 || req.Episode > 0 {
		for i := range items {
			if items[i].Attributes == nil {
				items[i].Attributes = make(map[string]string)
			}
			if req.Season > 0 {
				items[i].Attributes["season"] = strconv.Itoa(req.Season)
			}
			if req.Episode > 0 {
				items[i].Attributes["episode"] = strconv.Itoa(req.Episode)
			}
		}
	}
	return items, nil
}

// Latest implements engine.Connector by searching within the requested
// categories with no keyword, which is the conventional Torznab latest feed.
func (t *Torznab) Latest(ctx context.Context, req engine.LatestRequest) ([]engine.Item, error) {
	return t.Search(ctx, engine.SearchRequest{
		Mode:       engine.ModeSearch,
		Categories: req.Categories,
		Limit:      req.Limit,
	})
}

// Download implements engine.Connector. A magnet is returned as-is; otherwise
// it asks the indexer for t=get&id=<guid> or falls back to the release URL.
// A 302 to a magnet URI is surfaced in Payload.Magnet instead of being
// followed.
func (t *Torznab) Download(ctx context.Context, item engine.Item) (engine.Payload, error) {
	if magnet := strings.TrimSpace(item.Magnet); magnet != "" {
		return engine.Payload{Magnet: magnet}, nil
	}
	if target := strings.TrimSpace(item.DownloadURL); strings.HasPrefix(strings.ToLower(target), "magnet:") {
		return engine.Payload{Magnet: target}, nil
	}

	// Prefer a real indexer id. A GUID synthesised from the infohash, the
	// download URL or the tracker's release page is not a t=get id, so those
	// fall back to the release URL. Prowlarr in particular returns the site
	// download URL as <guid>; sending it as t=get&id= answers HTTP 400, while
	// its <link> is already the working per-indexer download.
	id := opaqueTorznabID(item.Attributes["guid"], item)
	if id == "" {
		id = opaqueTorznabID(item.GUID, item)
	}

	var resp *engine.Response
	var err error
	if id != "" {
		values := url.Values{}
		values.Set("t", "get")
		values.Set("id", id)
		if season := strings.TrimSpace(item.Attributes["season"]); season != "" {
			values.Set("season", season)
		}
		if episode := strings.TrimSpace(item.Attributes["episode"]); episode != "" {
			values.Set("ep", episode)
		}
		if t.cfg.APIKey != "" {
			values.Set("apikey", t.cfg.APIKey)
		}
		target, buildErr := withQuery(t.baseURL, values)
		if buildErr != nil {
			return engine.Payload{}, fmt.Errorf("torznab: build download request: %w", buildErr)
		}
		resp, err = t.cfg.Transport.Download(ctx, t.breaker, target, 0)
	} else {
		target := resolveURL(t.base, strings.TrimSpace(item.DownloadURL))
		if target == "" || !strings.HasPrefix(strings.ToLower(target), "http") {
			return engine.Payload{}, fmt.Errorf("torznab: release download URL is missing")
		}
		target = withAPIKey(target, t.cfg.APIKey)
		resp, err = t.cfg.Transport.Download(ctx, t.breaker, target, 0)
	}
	if err != nil {
		return engine.Payload{}, redactError("torznab: download", err, t.cfg.APIKey)
	}
	if resp.Status >= 300 && resp.Status < 400 {
		location := strings.TrimSpace(resp.Header.Get("Location"))
		if strings.HasPrefix(strings.ToLower(location), "magnet:") {
			return engine.Payload{Magnet: location}, nil
		}
		return engine.Payload{}, fmt.Errorf("torznab: download redirected without a magnet (HTTP %d)", resp.Status)
	}
	if resp.Status < 200 || resp.Status >= 300 {
		return engine.Payload{}, fmt.Errorf("torznab: download failed with HTTP status %d", resp.Status)
	}
	return engine.Payload{
		Body:        resp.Body,
		ContentType: "application/x-bittorrent",
		Filename:    safeFilename(item.Title, ".torrent"),
	}, nil
}

// Test implements engine.Connector. Download is skipped because it would
// consume a real release; the search phase proves the API key works.
func (t *Torznab) Test(ctx context.Context) (engine.TestResult, error) {
	return t.test(ctx, t.Search)
}

func (t *Torznab) parseItem(row newznabRSSItem) (engine.Item, bool) {
	title := strings.TrimSpace(row.Title)
	if title == "" {
		return engine.Item{}, false
	}
	guid := strings.TrimSpace(row.GUID)
	link := strings.TrimSpace(row.Link)
	enclosureURL := strings.TrimSpace(row.Enclosure.URL)
	comments := strings.TrimSpace(row.Comments)
	pubDate := strings.TrimSpace(row.PubDate)

	attributes := make(map[string]string, len(row.Attrs)+5)
	// A release can declare more than one audio language, and each one is a
	// separate `language` attribute: the map would keep only the last, so they
	// are collected on the way.
	var languages []string
	categories := make([]int, 0, 2)
	seenCategories := make(map[int]struct{}, 2)
	addCategory := func(value string) {
		id, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil || id <= 0 {
			return
		}
		if _, exists := seenCategories[id]; exists {
			return
		}
		seenCategories[id] = struct{}{}
		categories = append(categories, id)
	}

	for _, attr := range row.Attrs {
		name := strings.ToLower(strings.TrimSpace(attr.Name))
		if name == "" {
			continue
		}
		value := strings.TrimSpace(attr.Value)
		attributes[name] = value
		if name == "category" {
			addCategory(value)
		}
		if name == "language" && value != "" {
			languages = append(languages, value)
		}
	}

	category := strings.TrimSpace(attributes["category"])
	if category == "" {
		category = strings.TrimSpace(row.Category)
		if category != "" {
			attributes["category"] = category
		}
	}
	addCategory(row.Category)
	if guid != "" {
		attributes["guid"] = guid
	}
	if link != "" {
		attributes["link"] = link
	}
	if comments != "" {
		attributes["comments"] = comments
	}
	if pubDate != "" {
		attributes["pubdate"] = pubDate
	}
	if enclosureURL != "" {
		attributes["enclosure"] = resolveURL(t.base, enclosureURL)
	}

	magnet := strings.TrimSpace(attributes["magneturl"])
	if magnet == "" && strings.HasPrefix(strings.ToLower(link), "magnet:") {
		magnet = link
	}
	if magnet == "" && strings.HasPrefix(strings.ToLower(enclosureURL), "magnet:") {
		magnet = enclosureURL
	}

	hash := normalizeInfoHash(attributes["infohash"])
	if hash == "" {
		hash = normalizeInfoHash(infoHashFromMagnet(magnet))
	}
	if hash != "" {
		attributes["infohash"] = hash
	}

	downloadURL := ""
	switch {
	case magnet != "":
		// A magnet needs no tracker request; Download returns it as a payload.
	case isTorrentEnclosure(enclosureURL, row.Enclosure.Type):
		downloadURL = resolveURL(t.base, enclosureURL)
	case link != "":
		downloadURL = resolveURL(t.base, link)
	case enclosureURL != "":
		downloadURL = resolveURL(t.base, enclosureURL)
	}

	if guid == "" && link == "" && enclosureURL == "" && magnet == "" && hash == "" {
		return engine.Item{}, false
	}

	size := parseInt64(attributes["size"])
	if size == 0 {
		size = parseInt64(row.Enclosure.Length)
	}

	details := comments
	if details == "" {
		details = link
	}

	itemGUID := guid
	if itemGUID == "" {
		itemGUID = hash
	}

	freeleech, freeleechKnown := torznabFreeleech(attributes)

	return engine.Item{
		Protocol:       engine.ProtocolTorrent,
		Freeleech:      freeleech,
		FreeleechKnown: freeleechKnown,
		Languages:      engine.ParseLanguages(languages...),
		Title:          title,
		GUID:           itemGUID,
		DetailsURL:     details,
		DownloadURL:    downloadURL,
		Magnet:         magnet,
		InfoHash:       hash,
		Size:           size,
		Seeders:        parseInt64(attributes["seeders"]),
		Leechers:       parseInt64(attributes["leechers"]),
		Categories:     categories,
		Published:      parseDate(pubDate),
		IMDbID:         cleanAttrID(attributes["imdbid"]),
		TMDbID:         cleanAttrID(attributes["tmdbid"]),
		TVDbID:         cleanAttrID(attributes["tvdbid"]),
		Attributes:     attributes,
	}, true
}

// isTorrentEnclosure reports whether an enclosure is a .torrent payload.
func isTorrentEnclosure(rawURL, contentType string) bool {
	if strings.EqualFold(strings.TrimSpace(contentType), "application/x-bittorrent") {
		return true
	}
	if strings.Contains(strings.ToLower(contentType), "bittorrent") {
		return true
	}
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return false
	}
	return strings.EqualFold(path.Ext(parsed.Path), ".torrent")
}

// normalizeInfoHash lowercases a btih hash and converts the 32-character
// base32 form to the canonical 40-character hexadecimal form when possible.
func normalizeInfoHash(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}
	value = strings.Trim(strings.ToLower(value), "\"' ")
	value = strings.TrimPrefix(value, "urn:btih:")
	if value == "" {
		return ""
	}
	if len(value) == 32 {
		decoded, err := base32.StdEncoding.WithPadding(base32.NoPadding).
			DecodeString(strings.ToUpper(value))
		if err == nil && len(decoded) == 20 {
			return hex.EncodeToString(decoded)
		}
	}
	if len(value) == 40 {
		if _, err := hex.DecodeString(value); err == nil {
			return value
		}
	}
	return value
}

// opaqueTorznabID reports whether raw can be sent as a Torznab t=get id. URLs
// (the tracker release page or download URL Prowlarr exposes as <guid>),
// magnets and infohashes cannot: the release download URL is used instead.
func opaqueTorznabID(raw string, item engine.Item) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}
	lower := strings.ToLower(value)
	if strings.Contains(lower, "://") || strings.HasPrefix(lower, "magnet:") {
		return ""
	}
	if strings.EqualFold(value, strings.TrimSpace(item.DownloadURL)) ||
		strings.EqualFold(value, strings.TrimSpace(item.InfoHash)) {
		return ""
	}
	return value
}

// withAPIKey appends apikey to rawURL unless the URL already carries it.
func withAPIKey(rawURL, apiKey string) string {
	if apiKey == "" {
		return rawURL
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	query := parsed.Query()
	if query.Get("apikey") == "" && query.Get("api_key") == "" {
		query.Set("apikey", apiKey)
	}
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

// torznabFreeleech reads the download quota an upstream indexer declared. It
// follows the same attributes indexarr emits itself, so a chain (indexarr →
// indexarr, or Prowlarr → indexarr) keeps the information: an explicit
// `freeleechknown=0` means "not stated" and never becomes a paid release,
// `downloadvolumefactor` 0 or a positive `freeleech`/`neutralleech` marks a
// free release, and a positive factor marks a paid one.
func torznabFreeleech(attributes map[string]string) (bool, bool) {
	if known, err := strconv.Atoi(strings.TrimSpace(attributes["freeleechknown"])); err == nil && known == 0 {
		return false, false
	}
	if factor, err := strconv.ParseFloat(strings.TrimSpace(attributes["downloadvolumefactor"]), 64); err == nil {
		return factor == 0, true
	}
	for _, key := range []string{"freeleech", "neutralleech"} {
		switch strings.ToLower(strings.TrimSpace(attributes[key])) {
		case "1", "true", "yes", "free", "on":
			return true, true
		case "0", "false", "no", "off":
			return false, true
		}
	}
	return false, false
}
