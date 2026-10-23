package family

import (
	"context"
	"encoding/base32"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/buzzqw/indexarr/internal/engine"
)

const (
	torznabBreakerFallback = "torznab"
	torznabCapsTTL         = time.Hour
)

// TorznabConfig configures a generic Torznab API client. BaseURL is the API
// endpoint, for example https://indexer.example/api.
type TorznabConfig struct {
	Indexer    engine.IndexerRef
	BaseURL    string
	APIKey     string
	Transport  *engine.Transport
	Categories []int
	UserAgent  string
}

// Torznab is the generic connector for direct Torznab endpoints. It speaks the
// same protocol as Newznab but produces torrent releases: infohash, magnet and
// a .torrent payload.
type Torznab struct {
	cfg     TorznabConfig
	base    *url.URL
	baseURL string
	breaker string

	capsMu sync.Mutex
	caps   *Caps
	capsAt time.Time
}

// NewTorznab validates cfg and builds a connector.
func NewTorznab(cfg TorznabConfig) (*Torznab, error) {
	base, err := parseBaseURL(cfg.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("torznab: %w", err)
	}
	cfg.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	cfg.APIKey = strings.TrimSpace(cfg.APIKey)
	if cfg.Transport == nil {
		cfg.Transport = engine.NewTransport(engine.TransportConfig{UserAgent: cfg.UserAgent})
	}
	cfg.Indexer.Protocol = engine.ProtocolTorrent
	cfg.Categories = append([]int(nil), cfg.Categories...)

	breaker := strings.TrimSpace(cfg.Indexer.Slug)
	if breaker == "" {
		breaker = torznabBreakerFallback
	}

	return &Torznab{
		cfg:     cfg,
		base:    base,
		baseURL: cfg.BaseURL,
		breaker: breaker,
	}, nil
}

// Capabilities implements engine.Connector. Until a caps document has been
// fetched it advertises every Torznab search mode with its usual parameters.
func (t *Torznab) Capabilities() engine.Capabilities {
	t.capsMu.Lock()
	cached := t.caps.clone()
	t.capsMu.Unlock()

	caps := engine.Capabilities{
		Protocol: engine.ProtocolTorrent,
		Modes: []engine.Mode{
			engine.ModeSearch,
			engine.ModeTV,
			engine.ModeMovie,
			engine.ModeMusic,
			engine.ModeBook,
		},
		Params:     defaultNewznabParams(),
		Categories: append([]int(nil), t.cfg.Categories...),
	}
	if cached == nil {
		return caps
	}

	if cached.Modes != nil {
		caps.Modes = cached.Modes
	}
	if cached.Params != nil {
		caps.Params = cached.Params
	}
	if ids := flattenCapsCategories(cached.Categories); len(ids) > 0 {
		caps.Categories = ids
	}
	return caps
}

// Caps fetches and parses the Torznab caps document. Results are cached for
// one hour; any failure invalidates the cache so the next call retries.
func (t *Torznab) Caps(ctx context.Context) (*Caps, error) {
	t.capsMu.Lock()
	defer t.capsMu.Unlock()

	now := time.Now()
	if t.caps != nil && now.Sub(t.capsAt) < torznabCapsTTL {
		return t.caps.clone(), nil
	}
	t.caps = nil

	values := url.Values{}
	values.Set("t", "caps")
	if t.cfg.APIKey != "" {
		values.Set("apikey", t.cfg.APIKey)
	}
	resp, err := t.get(ctx, values, "torznab: caps request")
	if err != nil {
		t.capsAt = time.Time{}
		return nil, err
	}
	if resp.Status < 200 || resp.Status >= 300 {
		t.capsAt = time.Time{}
		return nil, fmt.Errorf("torznab: caps failed with HTTP status %d", resp.Status)
	}
	parsed, err := ParseTorznabCaps(resp.Body)
	if err != nil {
		t.capsAt = time.Time{}
		return nil, fmt.Errorf("torznab: parse caps: %s", redact(err.Error(), t.cfg.APIKey))
	}
	t.caps = parsed
	t.capsAt = now
	return parsed.clone(), nil
}

// ParseTorznabCaps parses a Torznab caps document. Torznab reuses the Newznab
// caps schema, so this is a named entry point for callers and tests.
func ParseTorznabCaps(body []byte) (*Caps, error) {
	return ParseCaps(body)
}

// Search implements engine.Connector.
func (t *Torznab) Search(ctx context.Context, req engine.SearchRequest) ([]engine.Item, error) {
	mode := req.Mode
	if mode == "" {
		mode = engine.ModeSearch
	}
	if !mode.Valid() {
		return nil, fmt.Errorf("torznab: unsupported search mode %q", mode)
	}

	categories := req.Categories
	if len(categories) == 0 {
		categories = t.cfg.Categories
	}

	values := url.Values{}
	values.Set("t", string(mode))
	if q := strings.TrimSpace(req.Query); q != "" {
		values.Set("q", q)
	}
	if req.Season > 0 {
		values.Set("season", strconv.Itoa(req.Season))
	}
	if req.Episode > 0 {
		values.Set("ep", strconv.Itoa(req.Episode))
	}
	if id := strings.TrimSpace(req.IMDbID); id != "" {
		values.Set("imdbid", id)
	}
	if id := strings.TrimSpace(req.TVDbID); id != "" {
		values.Set("tvdbid", id)
	}
	if id := strings.TrimSpace(req.TMDbID); id != "" {
		values.Set("tmdbid", id)
	}
	if id := strings.TrimSpace(req.RID); id != "" {
		values.Set("rid", id)
	}
	if artist := strings.TrimSpace(req.Artist); artist != "" {
		values.Set("artist", artist)
	}
	if album := strings.TrimSpace(req.Album); album != "" {
		values.Set("album", album)
	}
	if author := strings.TrimSpace(req.Author); author != "" {
		values.Set("author", author)
	}
	if title := strings.TrimSpace(req.Title); title != "" {
		values.Set("title", title)
	}
	if cat := joinCategories(categories); cat != "" {
		values.Set("cat", cat)
	}
	if req.Limit > 0 {
		values.Set("limit", strconv.Itoa(req.Limit))
	}
	if req.Offset > 0 {
		values.Set("offset", strconv.Itoa(req.Offset))
	}
	values.Set("extended", "1")
	if t.cfg.APIKey != "" {
		values.Set("apikey", t.cfg.APIKey)
	}

	resp, err := t.get(ctx, values, "torznab: search request")
	if err != nil {
		return nil, err
	}
	if resp.Status < 200 || resp.Status >= 300 {
		return nil, fmt.Errorf("torznab: search failed with HTTP status %d", resp.Status)
	}
	items, err := t.parseItems(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("torznab: search: %s", redact(err.Error(), t.cfg.APIKey))
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

	// Prefer a real indexer id. A GUID synthesised from the infohash or the
	// download URL is not a t=get id, so those fall back to the release URL.
	id := strings.TrimSpace(item.Attributes["guid"])
	if id == "" {
		candidate := strings.TrimSpace(item.GUID)
		if candidate != "" &&
			candidate != strings.TrimSpace(item.DownloadURL) &&
			!strings.EqualFold(candidate, strings.TrimSpace(item.InfoHash)) {
			id = candidate
		}
	}

	var resp *engine.Response
	var err error
	if id != "" {
		values := url.Values{}
		values.Set("t", "get")
		values.Set("id", id)
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
	result := engine.TestResult{}

	reach := func() error {
		values := url.Values{}
		values.Set("t", "caps")
		resp, err := t.get(ctx, values, "torznab: reach request")
		if err != nil {
			return err
		}
		if !reachableStatus(resp.Status) {
			return fmt.Errorf("unexpected HTTP status %d", resp.Status)
		}
		return nil
	}
	addPhase(&result, "reach", reach)

	caps := func() error {
		t.resetCaps()
		_, err := t.Caps(ctx)
		return err
	}
	addPhase(&result, "caps", caps)

	search := func() error {
		_, err := t.Search(ctx, engine.SearchRequest{
			Mode:  engine.ModeSearch,
			Query: "test",
			Limit: 1,
		})
		return err
	}
	addPhase(&result, "search", search)

	result.OK = true
	for _, phase := range result.Phases {
		if !phase.OK {
			result.OK = false
		}
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	return result, nil
}

func (t *Torznab) resetCaps() {
	t.capsMu.Lock()
	t.caps = nil
	t.capsAt = time.Time{}
	t.capsMu.Unlock()
}

func (t *Torznab) get(ctx context.Context, values url.Values, operation string) (*engine.Response, error) {
	target, err := withQuery(t.baseURL, values)
	if err != nil {
		return nil, fmt.Errorf("%s: build request: %w", operation, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, fmt.Errorf("%s: build request: %s", operation, redact(err.Error(), t.cfg.APIKey))
	}
	req.Header.Set("Accept", "application/xml, application/rss+xml;q=0.9, */*;q=0.8")
	if t.cfg.UserAgent != "" {
		req.Header.Set("User-Agent", t.cfg.UserAgent)
	}
	resp, err := t.cfg.Transport.Do(ctx, t.breaker, req)
	if err != nil {
		return nil, redactError(operation, err, t.cfg.APIKey)
	}
	return resp, nil
}

func (t *Torznab) parseItems(body []byte) ([]engine.Item, error) {
	rows, err := parseNewznabRSS(body)
	if err != nil {
		return nil, err
	}
	items := make([]engine.Item, 0, len(rows))
	for _, row := range rows {
		if item, ok := t.parseItem(row); ok {
			items = append(items, item)
		}
	}
	return items, nil
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

	return engine.Item{
		Protocol:    engine.ProtocolTorrent,
		Title:       title,
		GUID:        itemGUID,
		DetailsURL:  details,
		DownloadURL: downloadURL,
		Magnet:      magnet,
		InfoHash:    hash,
		Size:        size,
		Seeders:     parseInt64(attributes["seeders"]),
		Leechers:    parseInt64(attributes["leechers"]),
		Categories:  categories,
		Published:   parseDate(pubDate),
		IMDbID:      cleanAttrID(attributes["imdbid"]),
		TMDbID:      cleanAttrID(attributes["tmdbid"]),
		TVDbID:      cleanAttrID(attributes["tvdbid"]),
		Attributes:  attributes,
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
