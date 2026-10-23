package family

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/buzzqw/indexarr/internal/engine"
	"github.com/buzzqw/indexarr/internal/feed"
)

const (
	// ConnectorRSS is the family connector for plain RSS 2.0 and Atom feeds.
	ConnectorRSS = "rss"
	// rssFeedTTL bounds how long a parsed feed is reused. Plain feeds have no
	// search endpoint, so Search filters the same document several times in a
	// run; a short TTL avoids a request per query without hiding new releases
	// for long.
	rssFeedTTL = 30 * time.Second
	// rssHealthMinRows is the minimum number of feed items a health test wants.
	rssHealthMinRows = 1

	rssBreakerFallback = "rss"
)

// RSSConfig configures a plain RSS 2.0/Atom feed connector. URL is the full
// feed address (including any credential in the query or path).
type RSSConfig struct {
	Indexer         engine.IndexerRef
	URL             string
	DefaultCategory string
	Query           string // optional client-side filter applied to titles
	Transport       *engine.Transport
	UserAgent       string
	Categories      []int
}

// RSS is the native connector for releases published through an RSS 2.0 or
// Atom feed. It reuses the pure parsers in internal/feed and never touches the
// legacy HTTP client.
type RSS struct {
	cfg             RSSConfig
	base            *url.URL
	breaker         string
	defaultCategory int
	query           string
	categories      []int
	secrets         []string
	now             func() time.Time

	mu       sync.Mutex
	cached   []engine.Item
	cachedAt time.Time
}

// NewRSS validates cfg and builds a feed connector.
func NewRSS(cfg RSSConfig) (*RSS, error) {
	base, err := parseBaseURL(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("rss: %w", err)
	}
	cfg.URL = strings.TrimSpace(cfg.URL)
	cfg.Query = strings.TrimSpace(cfg.Query)
	if cfg.Transport == nil {
		cfg.Transport = engine.NewTransport(engine.TransportConfig{UserAgent: cfg.UserAgent})
	}
	cfg.Indexer.Protocol = engine.ProtocolTorrent
	cfg.Categories = append([]int(nil), cfg.Categories...)

	defaultCategory, err := rssCategory(cfg.DefaultCategory)
	if err != nil {
		return nil, fmt.Errorf("rss: default_category: %w", err)
	}

	categories := make([]int, 0, len(cfg.Categories)+1)
	seen := make(map[int]struct{}, len(cfg.Categories)+1)
	addCategory := func(id int) {
		if id <= 0 {
			return
		}
		if _, exists := seen[id]; exists {
			return
		}
		seen[id] = struct{}{}
		categories = append(categories, id)
	}
	addCategory(defaultCategory)
	for _, id := range cfg.Categories {
		addCategory(id)
	}

	breaker := strings.TrimSpace(cfg.Indexer.Slug)
	if breaker == "" {
		breaker = rssBreakerFallback
	}

	now := time.Now
	if clock := cfg.Transport.Config().Clock; clock != nil {
		now = clock.Now
	}

	return &RSS{
		cfg:             cfg,
		base:            base,
		breaker:         breaker,
		defaultCategory: defaultCategory,
		query:           cfg.Query,
		categories:      categories,
		secrets:         rssSecrets(cfg.URL),
		now:             now,
	}, nil
}

// Capabilities implements engine.Connector. A feed has no search endpoint, so
// only free-text search is advertised and it is done client-side.
func (r *RSS) Capabilities() engine.Capabilities {
	return engine.Capabilities{
		Protocol:  engine.ProtocolTorrent,
		LocalOnly: true,
		Modes:     []engine.Mode{engine.ModeSearch},
		Params: map[engine.Mode][]string{
			engine.ModeSearch: {engine.ParamQuery},
		},
		Categories: append([]int(nil), r.categories...),
	}
}

// Latest implements engine.Connector by returning the newest feed items, up to
// the requested limit.
func (r *RSS) Latest(ctx context.Context, req engine.LatestRequest) ([]engine.Item, error) {
	items, err := r.feed(ctx)
	if err != nil {
		return nil, err
	}
	filtered := r.filter(items, "", req.Categories)
	return paginate(filtered, 0, req.Limit), nil
}

// Search implements engine.Connector. Plain RSS has no query parameter, so the
// cached feed is filtered client-side: every keyword and the configured Query
// must appear in the title, case-insensitively.
func (r *RSS) Search(ctx context.Context, req engine.SearchRequest) ([]engine.Item, error) {
	mode := req.Mode
	if mode == "" {
		mode = engine.ModeSearch
	}
	if mode != engine.ModeSearch {
		return nil, fmt.Errorf("rss: unsupported search mode %q", mode)
	}
	items, err := r.feed(ctx)
	if err != nil {
		return nil, err
	}
	filtered := r.filter(items, req.Query, req.Categories)
	return paginate(filtered, req.Offset, req.Limit), nil
}

// Download implements engine.Connector. It returns a magnet when the item
// already carries one, follows a .torrent URL through the shared transport, and
// surfaces a redirect to a magnet instead of following it.
func (r *RSS) Download(ctx context.Context, item engine.Item) (engine.Payload, error) {
	if magnet := firstNonEmpty(item.Magnet, item.Attributes["magneturl"], item.Attributes["magnet"]); magnet != "" {
		return engine.Payload{Magnet: magnet}, nil
	}
	if target := strings.TrimSpace(item.DownloadURL); strings.HasPrefix(strings.ToLower(target), "magnet:") {
		return engine.Payload{Magnet: target}, nil
	}
	if magnet := feed.MagnetFromHash(item.InfoHash, item.Title); magnet != "" {
		return engine.Payload{Magnet: magnet}, nil
	}

	// DownloadURL is the .torrent enclosure resolved from the feed. A details
	// link is not a payload; it must not be fetched here.
	target := firstNonEmpty(item.DownloadURL, item.Attributes["enclosure"])
	if !strings.HasPrefix(strings.ToLower(target), "http") {
		return engine.Payload{}, fmt.Errorf("rss: release download URL is missing")
	}
	target = resolveURL(r.base, target)

	resp, err := r.cfg.Transport.Download(ctx, r.breaker+":download", target, 0)
	if err != nil {
		return engine.Payload{}, r.redactError("rss: download", err, rssSecrets(target)...)
	}
	if resp.Status >= 300 && resp.Status < 400 {
		location := strings.TrimSpace(resp.Header.Get("Location"))
		if strings.HasPrefix(strings.ToLower(location), "magnet:") {
			return engine.Payload{Magnet: location}, nil
		}
		return engine.Payload{}, fmt.Errorf("rss: download redirected without a magnet (HTTP %d)", resp.Status)
	}
	if resp.Status < 200 || resp.Status >= 300 {
		return engine.Payload{}, fmt.Errorf("rss: download failed with HTTP status %d", resp.Status)
	}
	return engine.Payload{
		Body:        resp.Body,
		ContentType: "application/x-bittorrent",
		Filename:    safeFilename(item.Title, ".torrent"),
	}, nil
}

// Test implements engine.Connector with structured phases: the feed must be
// reachable, parse as RSS/Atom and contain at least one item.
func (r *RSS) Test(ctx context.Context) (engine.TestResult, error) {
	result := engine.TestResult{}
	var body []byte

	addPhase(&result, "reach", func() error {
		resp, err := r.request(ctx)
		if err != nil {
			return err
		}
		if !reachableStatus(resp.Status) {
			return fmt.Errorf("unexpected HTTP status %d", resp.Status)
		}
		body = resp.Body
		return nil
	})

	addPhase(&result, "parse", func() error {
		if body == nil {
			return fmt.Errorf("feed was not fetched")
		}
		_, err := feed.ParseRSS(body)
		if err != nil {
			return r.redactError("parse feed", err)
		}
		return nil
	})

	addPhase(&result, "items", func() error {
		if body == nil {
			return fmt.Errorf("feed was not fetched")
		}
		raw, err := feed.ParseRSS(body)
		if err != nil {
			return r.redactError("parse feed", err)
		}
		items := r.mapItems(raw)
		if len(items) < rssHealthMinRows {
			return fmt.Errorf("feed has %d item(s), want at least %d", len(items), rssHealthMinRows)
		}
		return nil
	})

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

// feed returns the parsed items, reusing the last successful fetch for a short
// TTL. Failures are never cached, so a broken feed retries on the next call.
func (r *RSS) feed(ctx context.Context) ([]engine.Item, error) {
	r.mu.Lock()
	if r.cached != nil && !r.cachedAt.IsZero() && r.now().Sub(r.cachedAt) < rssFeedTTL {
		cached := cloneItems(r.cached)
		r.mu.Unlock()
		return cached, nil
	}
	r.mu.Unlock()

	resp, err := r.request(ctx)
	if err != nil {
		return nil, err
	}
	if resp.Status < 200 || resp.Status >= 300 {
		return nil, fmt.Errorf("rss: feed request failed with HTTP status %d", resp.Status)
	}
	raw, err := feed.ParseRSS(resp.Body)
	if err != nil {
		return nil, r.redactError("rss: parse feed", err)
	}
	items := r.mapItems(raw)

	r.mu.Lock()
	r.cached = cloneItems(items)
	r.cachedAt = r.now()
	r.mu.Unlock()
	return items, nil
}

func (r *RSS) request(ctx context.Context) (*engine.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.cfg.URL, nil)
	if err != nil {
		return nil, r.redactError("rss: build request", err)
	}
	req.Header.Set("Accept", "application/rss+xml, application/atom+xml, application/xml;q=0.9, */*;q=0.8")
	if r.cfg.UserAgent != "" {
		req.Header.Set("User-Agent", r.cfg.UserAgent)
	}
	resp, err := r.cfg.Transport.Do(ctx, r.breaker, req)
	if err != nil {
		return nil, r.redactError("rss: fetch feed", err)
	}
	return resp, nil
}

func (r *RSS) mapItems(raw []feed.Item) []engine.Item {
	items := make([]engine.Item, 0, len(raw))
	for _, source := range raw {
		if item, ok := r.mapItem(source); ok {
			items = append(items, item)
		}
	}
	return items
}

func (r *RSS) mapItem(raw feed.Item) (engine.Item, bool) {
	title := strings.TrimSpace(raw.Title)
	if title == "" {
		return engine.Item{}, false
	}
	magnet := strings.TrimSpace(raw.Magnet)
	hash := strings.TrimSpace(raw.InfoHash)
	if hash == "" && magnet != "" {
		hash = feed.MagnetHash(magnet)
	}
	if magnet == "" && hash != "" {
		magnet = feed.MagnetFromHash(hash, title)
	}

	link := strings.TrimSpace(raw.Link)
	downloadURL := strings.TrimSpace(raw.TorrentURL)
	if downloadURL != "" {
		downloadURL = resolveURL(r.base, downloadURL)
	}

	details := ""
	if link != "" && !strings.HasPrefix(strings.ToLower(link), "magnet:") &&
		!strings.EqualFold(resolveURL(r.base, link), downloadURL) {
		details = resolveURL(r.base, link)
	}

	categories := rssFeedCategories(raw.Category, r.defaultCategory)
	attributes := make(map[string]string, 4)
	if raw.Category != "" {
		attributes["category"] = raw.Category
	}
	if link != "" {
		attributes["link"] = link
	}
	if downloadURL != "" {
		attributes["enclosure"] = downloadURL
	}
	if raw.GUID != "" {
		attributes["guid"] = raw.GUID
	}

	item := engine.Item{
		Protocol:    engine.ProtocolTorrent,
		Title:       title,
		GUID:        firstNonEmpty(raw.GUID, hash),
		DetailsURL:  details,
		DownloadURL: downloadURL,
		Magnet:      magnet,
		InfoHash:    hash,
		Size:        raw.Size,
		Seeders:     rssCount(raw.Seeders),
		Leechers:    rssCount(raw.Leechers),
		Categories:  categories,
		Published:   raw.Published,
		IMDbID:      raw.IMDbID,
		TMDbID:      raw.TMDbID,
		TVDbID:      raw.TVDbID,
		Attributes:  attributes,
	}
	if item.GUID == "" {
		item.GUID = firstNonEmpty(downloadURL, details, title)
	}
	return item, true
}

// filter keeps items whose title contains every keyword of both the request and
// the configured filter, and whose category intersects the requested ones.
func (r *RSS) filter(items []engine.Item, query string, categories []int) []engine.Item {
	keywords := append(splitKeywords(query), splitKeywords(r.query)...)
	out := make([]engine.Item, 0, len(items))
	for _, item := range items {
		if !titleMatches(item.Title, keywords) {
			continue
		}
		if len(categories) > 0 && !categoriesIntersect(item.Categories, categories) {
			continue
		}
		out = append(out, item)
	}
	return out
}

func (r *RSS) redactError(prefix string, err error, extra ...string) error {
	if err == nil {
		return nil
	}
	secrets := append(append([]string(nil), r.secrets...), extra...)
	return fmt.Errorf("%s: %s", prefix, redact(err.Error(), secrets...))
}

func rssCategory(value string) (int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, nil
	}
	id, err := strconv.Atoi(value)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("invalid category id %q", value)
	}
	return id, nil
}

func rssFeedCategories(raw string, fallback int) []int {
	out := make([]int, 0, 2)
	seen := make(map[int]struct{}, 2)
	add := func(id int) {
		if id <= 0 {
			return
		}
		if _, exists := seen[id]; exists {
			return
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	for _, part := range strings.Split(raw, ",") {
		id, err := strconv.Atoi(strings.TrimSpace(part))
		if err == nil && id > 0 {
			add(id)
		}
	}
	if len(out) == 0 {
		add(fallback)
	}
	return out
}

func rssCount(value int64) int64 {
	if value < 0 {
		return 0
	}
	return value
}

func splitKeywords(query string) []string {
	fields := strings.Fields(strings.ToLower(strings.TrimSpace(query)))
	if len(fields) == 0 {
		return nil
	}
	return fields
}

func titleMatches(title string, keywords []string) bool {
	if len(keywords) == 0 {
		return true
	}
	lower := strings.ToLower(title)
	for _, keyword := range keywords {
		if !strings.Contains(lower, keyword) {
			return false
		}
	}
	return true
}

func categoriesIntersect(have, want []int) bool {
	if len(want) == 0 {
		return true
	}
	for _, left := range have {
		for _, right := range want {
			if left == right {
				return true
			}
		}
	}
	return false
}

func paginate(items []engine.Item, offset, limit int) []engine.Item {
	if offset > 0 {
		if offset >= len(items) {
			return []engine.Item{}
		}
		items = items[offset:]
	}
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	return items
}

func cloneItems(items []engine.Item) []engine.Item {
	if items == nil {
		return nil
	}
	out := make([]engine.Item, len(items))
	for i, item := range items {
		out[i] = item
		out[i].Categories = append([]int(nil), item.Categories...)
		if item.Attributes != nil {
			attributes := make(map[string]string, len(item.Attributes))
			for key, value := range item.Attributes {
				attributes[key] = value
			}
			out[i].Attributes = attributes
		}
	}
	return out
}

// rssSecrets collects the parts of a feed or download URL that must not appear
// in an error message: userinfo, credential query values, opaque path segments
// and the whole URL as a fallback when the transport quotes it verbatim.
func rssSecrets(rawURL string) []string {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return nil
	}
	secrets := []string{rawURL}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return secrets
	}
	if parsed.User != nil {
		if username := parsed.User.Username(); username != "" {
			secrets = append(secrets, username)
		}
		if password, ok := parsed.User.Password(); ok && password != "" {
			secrets = append(secrets, password)
		}
	}
	for key, values := range parsed.Query() {
		if !rssSecretParam(key) {
			continue
		}
		for _, value := range values {
			if value = strings.TrimSpace(value); value != "" {
				secrets = append(secrets, value)
			}
		}
	}
	for _, segment := range strings.Split(parsed.Path, "/") {
		if rssSecretPathSegment(segment) {
			secrets = append(secrets, segment)
		}
	}
	return secrets
}

func rssSecretParam(name string) bool {
	normalized := strings.ToLower(strings.TrimSpace(name))
	normalized = strings.NewReplacer("_", "", "-", "", ".", "").Replace(normalized)
	for _, secret := range []string{
		"apikey", "apitoken", "passkey", "password", "passwd",
		"token", "secret", "rsskey", "authkey", "cookie", "credentials",
	} {
		if normalized == secret || strings.Contains(normalized, secret) {
			return true
		}
	}
	return false
}

// rssSecretPathSegment mirrors feed.RedactURL's heuristic: an opaque token of
// at least 16 characters mixing digits and letters (or hexadecimal only).
func rssSecretPathSegment(segment string) bool {
	if len(segment) < 16 {
		return false
	}
	digits, letters, hexOnly := false, false, true
	for _, r := range segment {
		switch {
		case r >= '0' && r <= '9':
			digits = true
		case (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F'):
			letters = true
		case (r >= 'g' && r <= 'z') || (r >= 'G' && r <= 'Z'):
			letters = true
			hexOnly = false
		case r == '-' || r == '_' || r == '~' || r == '+' || r == '=':
			hexOnly = false
		default:
			return false
		}
	}
	if letters && hexOnly {
		return true
	}
	return digits && letters
}
