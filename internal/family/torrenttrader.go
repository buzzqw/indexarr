package family

import (
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/andybalholm/cascadia"
	"golang.org/x/net/html"

	"github.com/buzzqw/indexarr/internal/engine"
)

// TorrentTrader/TBDev is a large family of PHP trackers. The family shares a
// login form (takelogin.php), a torrents.php listing and download.php links,
// but forks rename classes and table columns. Every selector is therefore
// overridable through TorrentTraderProfile.
//
// The selectors below are synthetic defaults ("bozza"): they cover the stock
// TorrentTrader/TBDev layout and the most common forks, and are meant to be
// pinned against a real site before the connector is marked verified.

const (
	torrentTraderBreakerFallback = "torrenttrader"
	torrentTraderLoginTTL        = 6 * time.Hour
	torrentTraderMaxPages        = 5
	torrentTraderDefaultDownload = "download.php?id=%s"
)

// TorrentTraderProfile overrides the site-specific parts of the connector.
// A selector may contain a comma-separated CSS list; the first matching
// alternative wins. Column position fallbacks are expressed with :nth-child.
type TorrentTraderProfile struct {
	LoginPath        string
	SearchPath       string
	RowsSelector     string
	TitleSelector    string
	DetailsSelector  string
	DownloadSelector string
	SizeSelector     string
	SeedersSelector  string
	LeechersSelector string
	DateSelector     string
	CategorySelector string
	PageParam        string
}

// DefaultTorrentTraderProfile returns the stock TorrentTrader/TBDev layout.
func DefaultTorrentTraderProfile() TorrentTraderProfile {
	return TorrentTraderProfile{
		LoginPath:        "takelogin.php",
		SearchPath:       "torrents.php?search=...&page=...",
		RowsSelector:     "table.table > tbody > tr",
		TitleSelector:    `a[href*="details.php?id="]`,
		DetailsSelector:  `a[href*="details.php?id="]`,
		DownloadSelector: `a[href*="download.php"]`,
		SizeSelector:     "td.size, td:nth-child(4)",
		SeedersSelector:  "td.seeders, td:nth-child(5)",
		LeechersSelector: "td.leechers, td:nth-child(6)",
		DateSelector:     "td.added, td.date, td:nth-child(7)",
		CategorySelector: `a[href*="cat="], td:nth-child(1)`,
		PageParam:        "page",
	}
}

func (p TorrentTraderProfile) withDefaults() TorrentTraderProfile {
	defaults := DefaultTorrentTraderProfile()
	if strings.TrimSpace(p.LoginPath) == "" {
		p.LoginPath = defaults.LoginPath
	}
	if strings.TrimSpace(p.SearchPath) == "" {
		p.SearchPath = defaults.SearchPath
	}
	if strings.TrimSpace(p.RowsSelector) == "" {
		p.RowsSelector = defaults.RowsSelector
	}
	if strings.TrimSpace(p.TitleSelector) == "" {
		p.TitleSelector = defaults.TitleSelector
	}
	if strings.TrimSpace(p.DetailsSelector) == "" {
		p.DetailsSelector = defaults.DetailsSelector
	}
	if strings.TrimSpace(p.DownloadSelector) == "" {
		p.DownloadSelector = defaults.DownloadSelector
	}
	if strings.TrimSpace(p.SizeSelector) == "" {
		p.SizeSelector = defaults.SizeSelector
	}
	if strings.TrimSpace(p.SeedersSelector) == "" {
		p.SeedersSelector = defaults.SeedersSelector
	}
	if strings.TrimSpace(p.LeechersSelector) == "" {
		p.LeechersSelector = defaults.LeechersSelector
	}
	if strings.TrimSpace(p.DateSelector) == "" {
		p.DateSelector = defaults.DateSelector
	}
	if strings.TrimSpace(p.CategorySelector) == "" {
		p.CategorySelector = defaults.CategorySelector
	}
	if strings.TrimSpace(p.PageParam) == "" {
		p.PageParam = defaults.PageParam
	}
	return p
}

// TorrentTraderConfig configures one TorrentTrader/TBDev tracker.
type TorrentTraderConfig struct {
	Indexer    engine.IndexerRef
	BaseURL    string
	Username   string
	Password   string
	Passkey    string
	Transport  *engine.Transport
	Categories []int // site category ids used when a search names none
	UserAgent  string
	Profile    TorrentTraderProfile
}

// TorrentTrader is the native connector shared by TorrentTrader and TBDev
// trackers.
type TorrentTrader struct {
	core *htmlTracker
}

var _ engine.Connector = (*TorrentTrader)(nil)

// NewTorrentTrader validates cfg and builds a connector. Credentials are
// optional because some trackers expose public listings; when both are set the
// connector logs in lazily and refreshes the session every six hours.
func NewTorrentTrader(cfg TorrentTraderConfig) (*TorrentTrader, error) {
	base, err := parseBaseURL(cfg.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("torrenttrader: %w", err)
	}
	if err := validateCredentialPair(cfg.Username, cfg.Password); err != nil {
		return nil, fmt.Errorf("torrenttrader: %w", err)
	}
	profile := cfg.Profile.withDefaults()
	if err := validateProfileSelectors(profile.RowsSelector, profile.TitleSelector,
		profile.DetailsSelector, profile.DownloadSelector, profile.SizeSelector,
		profile.SeedersSelector, profile.LeechersSelector, profile.DateSelector,
		profile.CategorySelector); err != nil {
		return nil, fmt.Errorf("torrenttrader: %w", err)
	}

	cfg.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	cfg.Username = strings.TrimSpace(cfg.Username)
	cfg.Password = strings.TrimSpace(cfg.Password)
	cfg.Passkey = strings.TrimSpace(cfg.Passkey)
	cfg.Indexer.Protocol = engine.ProtocolTorrent
	cfg.Categories = append([]int(nil), cfg.Categories...)
	if cfg.Transport == nil {
		cfg.Transport = engine.NewTransport(engine.TransportConfig{UserAgent: cfg.UserAgent})
	}

	breaker := strings.TrimSpace(cfg.Indexer.Slug)
	if breaker == "" {
		breaker = torrentTraderBreakerFallback
	}

	return &TorrentTrader{core: &htmlTracker{
		label:         "torrenttrader",
		base:          base,
		transport:     cfg.Transport,
		breaker:       breaker,
		userAgent:     cfg.UserAgent,
		username:      cfg.Username,
		password:      cfg.Password,
		passkey:       cfg.Passkey,
		categories:    cfg.Categories,
		loginPaths:    []string{profile.LoginPath},
		searchPath:    profile.SearchPath,
		pageParam:     profile.PageParam,
		categoryParam: "cat",
		rows:          profile.RowsSelector,
		rowFallbacks:  []string{"table#sortabletable tr", "table.lista tr"},
		title:         profile.TitleSelector,
		details:       profile.DetailsSelector,
		downloadLink:  profile.DownloadSelector,
		size:          profile.SizeSelector,
		seeders:       profile.SeedersSelector,
		leechers:      profile.LeechersSelector,
		date:          profile.DateSelector,
		category:      profile.CategorySelector,
		downloadByID:  torrentTraderDefaultDownload,
		session:       newTrackerSession(),
	}}, nil
}

// Capabilities implements engine.Connector.
func (t *TorrentTrader) Capabilities() engine.Capabilities {
	return t.core.capabilities()
}

// Search implements engine.Connector.
func (t *TorrentTrader) Search(ctx context.Context, req engine.SearchRequest) ([]engine.Item, error) {
	return t.core.search(ctx, req)
}

// Latest implements engine.Connector. It is an unfiltered, unqueried listing.
func (t *TorrentTrader) Latest(ctx context.Context, req engine.LatestRequest) ([]engine.Item, error) {
	return t.core.latest(ctx, req)
}

// Download implements engine.Connector.
func (t *TorrentTrader) Download(ctx context.Context, item engine.Item) (engine.Payload, error) {
	return t.core.download(ctx, item)
}

// Test implements engine.Connector.
func (t *TorrentTrader) Test(ctx context.Context) (engine.TestResult, error) {
	return t.core.test(ctx)
}

// TorrentTraderFactory builds a TorrentTrader connector from generic settings.
// It reads base_url (or url), username, password, passkey, api_key (used as a
// passkey fallback), categories and user_agent.
func TorrentTraderFactory(cfg engine.Config) (engine.Connector, error) {
	settings := cfg.Settings
	categories, err := parseCategoryList(settings["categories"])
	if err != nil {
		return nil, fmt.Errorf("torrenttrader: categories: %w", err)
	}
	return NewTorrentTrader(TorrentTraderConfig{
		Indexer:    cfg.Indexer,
		BaseURL:    firstNonEmpty(settings["base_url"], settings["url"]),
		Username:   settings["username"],
		Password:   settings["password"],
		Passkey:    firstNonEmpty(settings["passkey"], settings["api_key"]),
		Transport:  transportFromSettings(cfg.Transport, settings),
		Categories: categories,
		UserAgent:  settings["user_agent"],
	})
}

// htmlTracker is the shared implementation of the HTML tracker connectors. It
// keeps one cookie jar per connector, performs a lazy form login and parses
// listing rows with cascadia selectors.
type htmlTracker struct {
	label     string
	base      *url.URL
	transport *engine.Transport
	breaker   string
	userAgent string

	username string
	password string
	passkey  string

	categories []int

	loginPaths    []string
	searchPath    string
	pageParam     string
	categoryParam string

	rows         string
	rowFallbacks []string
	title        string
	details      string
	downloadLink string
	size         string
	seeders      string
	leechers     string
	date         string
	category     string

	downloadByID string // fmt template with one %s (the site id)

	session *trackerSession
	loginMu sync.Mutex
}

func (h *htmlTracker) capabilities() engine.Capabilities {
	return engine.Capabilities{
		Protocol: engine.ProtocolTorrent,
		Modes:    []engine.Mode{engine.ModeSearch, engine.ModeTV, engine.ModeMovie},
		Params: map[engine.Mode][]string{
			engine.ModeSearch: {engine.ParamQuery},
			engine.ModeTV:     {engine.ParamQuery, engine.ParamSeason, engine.ParamEpisode},
			engine.ModeMovie:  {engine.ParamQuery, engine.ParamYear},
		},
		Categories: append([]int(nil), h.categories...),
	}
}

func (h *htmlTracker) search(ctx context.Context, req engine.SearchRequest) ([]engine.Item, error) {
	mode := req.Mode
	if mode == "" {
		mode = engine.ModeSearch
	}
	switch mode {
	case engine.ModeSearch, engine.ModeTV, engine.ModeMovie:
	default:
		return nil, fmt.Errorf("%s: unsupported search mode %q", h.label, mode)
	}

	text := strings.TrimSpace(req.Query)
	if text == "" {
		text = strings.TrimSpace(req.Title)
	}
	if req.Season > 0 && req.Episode > 0 {
		marker := fmt.Sprintf("S%02dE%02d", req.Season, req.Episode)
		if !strings.Contains(strings.ToUpper(text), marker) {
			text = strings.TrimSpace(text + " " + marker)
		}
	}
	if req.Year > 0 && !strings.Contains(text, strconv.Itoa(req.Year)) {
		text = strings.TrimSpace(text + " " + strconv.Itoa(req.Year))
	}

	categories := req.Categories
	if len(categories) == 0 {
		categories = h.categories
	}
	return h.collect(ctx, text, categories, req.Limit, req.Offset)
}

func (h *htmlTracker) latest(ctx context.Context, req engine.LatestRequest) ([]engine.Item, error) {
	categories := req.Categories
	if len(categories) == 0 {
		categories = h.categories
	}
	return h.collect(ctx, "", categories, req.Limit, 0)
}

func (h *htmlTracker) collect(ctx context.Context, text string, categories []int, limit, offset int) ([]engine.Item, error) {
	startPage := 1
	if offset > 0 {
		if limit > 0 {
			startPage = offset/limit + 1
		} else {
			startPage = offset + 1
		}
	}

	items := make([]engine.Item, 0, 64)
	seen := make(map[string]struct{})
	for index := 0; index < torrentTraderMaxPages; index++ {
		page := startPage + index
		pageItems, err := h.fetchPage(ctx, page, text, categories)
		if err != nil {
			return nil, err
		}
		if len(pageItems) == 0 {
			break
		}
		for _, item := range pageItems {
			key := item.GUID
			if key == "" {
				key = item.Title + "|" + item.DownloadURL
			}
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			items = append(items, item)
		}
		if limit > 0 && len(items) >= limit {
			items = items[:limit]
			break
		}
	}
	return items, nil
}

func (h *htmlTracker) fetchPage(ctx context.Context, page int, text string, categories []int) ([]engine.Item, error) {
	resp, err := h.searchResponse(ctx, page, text, categories)
	if err != nil {
		return nil, err
	}
	// An out-of-range page is simply the end of the listing.
	if page > 1 && (resp.Status == http.StatusNotFound || resp.Status == http.StatusBadRequest ||
		resp.Status == http.StatusGone) {
		return nil, nil
	}
	if !reachableStatus(resp.Status) || resp.Status >= 300 {
		return nil, fmt.Errorf("%s: search failed with HTTP status %d", h.label, resp.Status)
	}
	return h.parseRows(resp.Body)
}

func (h *htmlTracker) searchResponse(ctx context.Context, page int, text string, categories []int) (*engine.Response, error) {
	values := url.Values{}
	if text != "" {
		values.Set("search", text)
	}
	if len(categories) > 0 {
		ids := make([]string, 0, len(categories))
		for _, category := range categories {
			if category > 0 {
				ids = append(ids, strconv.Itoa(category))
			}
		}
		if len(ids) > 0 {
			values.Set(h.categoryParam, strings.Join(ids, ","))
		}
	}
	values.Set(h.pageParam, strconv.Itoa(page))

	target, err := buildTrackerURL(h.base, h.searchPath, values, "search", h.pageParam, h.categoryParam)
	if err != nil {
		return nil, fmt.Errorf("%s: build search request: %w", h.label, err)
	}
	resp, err := h.request(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	return resp, nil
}

func (h *htmlTracker) parseRows(body []byte) ([]engine.Item, error) {
	document, err := trackerDocument(body)
	if err != nil {
		return nil, fmt.Errorf("%s: parse search response: %s", h.label, redact(err.Error(), h.secrets()...))
	}
	rows := trackerRows(document, append([]string{h.rows}, h.rowFallbacks...)...)
	items := make([]engine.Item, 0, len(rows))
	for _, row := range rows {
		item, ok := h.parseRow(row)
		if ok {
			items = append(items, item)
		}
	}
	return items, nil
}

func (h *htmlTracker) parseRow(row *html.Node) (engine.Item, bool) {
	titleNode := trackerFirst(row, h.title)
	title := trackerText(titleNode)
	if title == "" {
		return engine.Item{}, false
	}

	details := ""
	if node := trackerFirst(row, h.details); node != nil {
		details = trackerAttr(node, "href")
	}
	if details == "" && titleNode != nil {
		details = trackerAttr(titleNode, "href")
	}
	details = resolveURL(h.base, details)

	download := ""
	magnet := ""
	if node := trackerFirst(row, h.downloadLink); node != nil {
		download = trackerAttr(node, "href")
	}
	switch {
	case strings.HasPrefix(strings.ToLower(strings.TrimSpace(download)), "magnet:"):
		magnet = strings.TrimSpace(download)
		download = ""
	case download == "":
		if node := trackerFirst(row, `a[href^="magnet:"]`); node != nil {
			magnet = strings.TrimSpace(trackerAttr(node, "href"))
		}
	}
	if download != "" {
		download = h.withPasskey(resolveURL(h.base, download))
	}

	id := trackerID(details, download, magnet)
	if details == "" && download == "" && magnet == "" {
		return engine.Item{}, false
	}
	if id == "" {
		id = trackerIDFromMagnetOrURL(details, download, magnet)
	}
	guid := firstNonEmpty(id, details, download, magnet, title)

	categoryID := h.parseCategory(row)
	categories := make([]int, 0, 1)
	if categoryID > 0 {
		categories = append(categories, categoryID)
	}

	attributes := make(map[string]string)
	if id != "" {
		attributes["id"] = id
	}
	if categoryName := trackerText(trackerFirst(row, h.category)); categoryName != "" {
		attributes["category"] = categoryName
	}
	if len(attributes) == 0 {
		attributes = nil
	}

	return engine.Item{
		Protocol:    engine.ProtocolTorrent,
		Title:       title,
		GUID:        guid,
		DetailsURL:  details,
		DownloadURL: download,
		Magnet:      magnet,
		InfoHash:    infoHashFromMagnet(magnet),
		Size:        trackerParseSize(trackerText(trackerFirst(row, h.size))),
		Seeders:     trackerParseInt(trackerText(trackerFirst(row, h.seeders))),
		Leechers:    trackerParseInt(trackerText(trackerFirst(row, h.leechers))),
		Categories:  categories,
		Published:   trackerParseDate(trackerText(trackerFirst(row, h.date))),
		Attributes:  attributes,
	}, true
}

func (h *htmlTracker) parseCategory(row *html.Node) int {
	node := trackerFirst(row, h.category)
	if node == nil {
		return 0
	}
	if href := trackerAttr(node, "href"); href != "" {
		if id := trackerQueryCategoryID(href); id > 0 {
			return id
		}
	}
	if anchor := trackerFirst(node, "a[href]"); anchor != nil {
		if id := trackerQueryCategoryID(trackerAttr(anchor, "href")); id > 0 {
			return id
		}
	}
	return trackerLeadingInt(trackerText(node))
}

func (h *htmlTracker) download(ctx context.Context, item engine.Item) (engine.Payload, error) {
	target := strings.TrimSpace(item.DownloadURL)
	if target == "" && item.Attributes != nil {
		target = strings.TrimSpace(item.Attributes["download"])
	}
	if target == "" {
		if magnet := strings.TrimSpace(item.Magnet); magnet != "" {
			return engine.Payload{Magnet: magnet}, nil
		}
		id := trackerItemID(item)
		if id == "" {
			return engine.Payload{}, fmt.Errorf("%s: torrent id is missing", h.label)
		}
		target = fmt.Sprintf(h.downloadByID, url.QueryEscape(id))
	}
	if strings.HasPrefix(strings.ToLower(target), "magnet:") {
		return engine.Payload{Magnet: target}, nil
	}
	target = h.withPasskey(resolveURL(h.base, target))

	resp, err := h.transport.Download(ctx, h.breaker, target, 0)
	if err != nil {
		return engine.Payload{}, redactError(h.label+": download", err, h.secrets()...)
	}
	if resp.Status >= 300 && resp.Status < 400 {
		location := strings.TrimSpace(resp.Header.Get("Location"))
		if strings.HasPrefix(strings.ToLower(location), "magnet:") {
			return engine.Payload{Magnet: location}, nil
		}
		return engine.Payload{}, fmt.Errorf("%s: download redirected without a magnet (HTTP %d)", h.label, resp.Status)
	}
	if resp.Status < 200 || resp.Status >= 300 {
		return engine.Payload{}, fmt.Errorf("%s: download failed with HTTP status %d", h.label, resp.Status)
	}

	contentType := strings.TrimSpace(resp.Header.Get("Content-Type"))
	if contentType == "" {
		contentType = "application/x-bittorrent"
	}
	if strings.HasPrefix(strings.ToLower(contentType), "text/html") {
		// A tracker that answers a download with HTML is serving the login
		// page or an error page, not a torrent.
		return engine.Payload{}, fmt.Errorf("%s: download returned an HTML page instead of a torrent", h.label)
	}
	return engine.Payload{
		Body:        resp.Body,
		ContentType: contentType,
		Filename:    safeFilename(item.Title, ".torrent"),
	}, nil
}

func (h *htmlTracker) test(ctx context.Context) (engine.TestResult, error) {
	result := engine.TestResult{}

	reach := func() error {
		resp, err := h.do(ctx, http.MethodGet, resolveURL(h.base, "/"), nil)
		if err != nil {
			return err
		}
		if !reachableStatus(resp.Status) {
			return fmt.Errorf("unexpected HTTP status %d", resp.Status)
		}
		return nil
	}
	addPhase(&result, "reach", reach)

	if h.hasCredentials() {
		login := func() error { return h.ensureLogin(ctx, true) }
		addPhase(&result, "login", login)
	}

	search := func() error {
		resp, err := h.searchResponse(ctx, 1, "test", h.categories)
		if err != nil {
			return err
		}
		if resp.Status < 200 || resp.Status >= 300 {
			return fmt.Errorf("search failed with HTTP status %d", resp.Status)
		}
		if _, err := h.parseRows(resp.Body); err != nil {
			return err
		}
		return nil
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

func (h *htmlTracker) hasCredentials() bool {
	return h.username != "" && h.password != ""
}

func (h *htmlTracker) secrets() []string {
	return []string{h.password, h.passkey}
}

func (h *htmlTracker) withPasskey(raw string) string {
	if h.passkey == "" || raw == "" {
		return raw
	}
	raw = strings.ReplaceAll(raw, "{passkey}", url.QueryEscape(h.passkey))
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" {
		return raw
	}
	query := parsed.Query()
	if query.Get("passkey") != "" || query.Get("torrent_pass") != "" || query.Get("authkey") != "" {
		return parsed.String()
	}
	query.Set("passkey", h.passkey)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

// request performs an authenticated request and retries once after a
// 401/403 by forcing a new login.
func (h *htmlTracker) request(ctx context.Context, method, target string, form url.Values) (*engine.Response, error) {
	for attempt := 0; attempt < 2; attempt++ {
		if err := h.ensureLogin(ctx, attempt > 0); err != nil {
			return nil, err
		}
		resp, err := h.do(ctx, method, target, form)
		if err != nil {
			return nil, err
		}
		if (resp.Status == http.StatusUnauthorized || resp.Status == http.StatusForbidden) &&
			h.hasCredentials() && attempt == 0 {
			h.session.invalidate()
			continue
		}
		return resp, nil
	}
	return nil, fmt.Errorf("%s: request failed", h.label)
}

func (h *htmlTracker) do(ctx context.Context, method, target string, form url.Values) (*engine.Response, error) {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return nil, fmt.Errorf("%s: build request: %s", h.label, redact(err.Error(), h.secrets()...))
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	if h.userAgent != "" {
		req.Header.Set("User-Agent", h.userAgent)
	}
	h.session.apply(req)

	resp, err := h.transport.Do(ctx, h.breaker, req)
	if err != nil {
		return nil, redactError(h.label+": request", err, h.secrets()...)
	}
	h.session.capture(resp)
	return resp, nil
}

// ensureLogin logs in when credentials are configured. The session is reused
// for torrentTraderLoginTTL; force invalidates it and logs in again.
func (h *htmlTracker) ensureLogin(ctx context.Context, force bool) error {
	if !h.hasCredentials() {
		return nil
	}
	h.loginMu.Lock()
	defer h.loginMu.Unlock()

	if !force && h.session.loginValid(time.Now(), torrentTraderLoginTTL) {
		return nil
	}
	h.session.invalidate()

	csrfBody, loginURL, err := h.loginForm(ctx)
	if err != nil {
		return err
	}
	form := url.Values{}
	for key, value := range trackerHiddenFields(csrfBody) {
		form.Set(key, value)
	}
	form.Set("username", h.username)
	form.Set("password", h.password)

	resp, err := h.do(ctx, http.MethodPost, loginURL, form)
	if err != nil {
		return err
	}
	if resp.Status < 200 || resp.Status >= 400 {
		return fmt.Errorf("%s: login failed with HTTP status %d", h.label, resp.Status)
	}
	if trackerLoginFailed(resp.Body) {
		return fmt.Errorf("%s: login failed: the site rejected the credentials", h.label)
	}
	h.session.markLoggedIn(time.Now())
	return nil
}

// loginForm fetches the first reachable login page so the posted form carries
// the site's CSRF token and any pre-session cookie.
func (h *htmlTracker) loginForm(ctx context.Context) ([]byte, string, error) {
	first := ""
	for _, path := range h.loginPaths {
		if strings.TrimSpace(path) == "" {
			continue
		}
		target := resolveURL(h.base, path)
		if first == "" {
			first = target
		}
		resp, err := h.do(ctx, http.MethodGet, target, nil)
		if err != nil {
			return nil, "", err
		}
		if resp.Status >= 200 && resp.Status < 400 {
			return resp.Body, target, nil
		}
	}

	// TorrentTrader puts the form on the index page while posting it to
	// takelogin.php, so fall back to the home page for the CSRF token.
	root := resolveURL(h.base, "/")
	resp, err := h.do(ctx, http.MethodGet, root, nil)
	if err == nil && resp.Status >= 200 && resp.Status < 400 {
		if first == "" {
			first = root
		}
		return resp.Body, first, nil
	}
	if first == "" {
		first = root
	}
	return nil, first, nil
}

// trackerSession is a minimal cookie jar for the family HTML connectors. The
// engine exposes cookies through Session but keeps its constructor private, so
// the family keeps the same semantics locally.
type trackerSession struct {
	mu       sync.RWMutex
	cookies  map[string]string
	loggedIn bool
	loginAt  time.Time
}

func newTrackerSession() *trackerSession {
	return &trackerSession{cookies: make(map[string]string)}
}

func (s *trackerSession) capture(resp *engine.Response) {
	if s == nil || resp == nil || len(resp.Header) == 0 {
		return
	}
	response := &http.Response{Header: resp.Header}
	cookies := response.Cookies()
	if len(cookies) == 0 {
		return
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, cookie := range cookies {
		if cookie == nil || cookie.Name == "" {
			continue
		}
		if cookie.MaxAge < 0 || (!cookie.Expires.IsZero() && cookie.Expires.Before(now)) {
			delete(s.cookies, cookie.Name)
			continue
		}
		s.cookies[cookie.Name] = cookie.Value
	}
}

func (s *trackerSession) apply(req *http.Request) {
	if s == nil || req == nil {
		return
	}
	s.mu.RLock()
	if len(s.cookies) == 0 {
		s.mu.RUnlock()
		return
	}
	names := make([]string, 0, len(s.cookies))
	for name := range s.cookies {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, (&http.Cookie{Name: name, Value: s.cookies[name]}).String())
	}
	cookie := strings.Join(parts, "; ")
	s.mu.RUnlock()
	req.Header.Set("Cookie", cookie)
}

func (s *trackerSession) invalidate() {
	s.mu.Lock()
	s.loggedIn = false
	s.loginAt = time.Time{}
	s.mu.Unlock()
}

func (s *trackerSession) markLoggedIn(now time.Time) {
	s.mu.Lock()
	s.loggedIn = true
	s.loginAt = now
	s.mu.Unlock()
}

func (s *trackerSession) loginValid(now time.Time, refresh time.Duration) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.loggedIn {
		return false
	}
	if refresh <= 0 {
		return true
	}
	return now.Sub(s.loginAt) < refresh
}

// validateCredentialPair rejects a half-configured login.
func validateCredentialPair(username, password string) error {
	hasUser := strings.TrimSpace(username) != ""
	hasPass := strings.TrimSpace(password) != ""
	if hasUser != hasPass {
		return fmt.Errorf("username and password must be set together")
	}
	return nil
}

func validateProfileSelectors(selectors ...string) error {
	for _, spec := range selectors {
		for _, part := range strings.Split(spec, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			if _, err := cascadia.Compile(part); err != nil {
				return fmt.Errorf("invalid selector %q", part)
			}
		}
	}
	return nil
}

func buildTrackerURL(base *url.URL, path string, values url.Values, clear ...string) (string, error) {
	target := resolveURL(base, path)
	parsed, err := url.Parse(target)
	if err != nil {
		return "", fmt.Errorf("invalid URL")
	}
	query := parsed.Query()
	for _, key := range clear {
		if key != "" {
			query.Del(key)
		}
	}
	for key, list := range values {
		query.Del(key)
		for _, value := range list {
			if value != "" {
				query.Add(key, value)
			}
		}
	}
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func trackerDocument(body []byte) (*html.Node, error) {
	document, err := html.Parse(strings.NewReader(string(body)))
	if err != nil {
		return nil, fmt.Errorf("invalid HTML response")
	}
	return document, nil
}

func trackerRows(document *html.Node, selectors ...string) []*html.Node {
	for _, spec := range selectors {
		for _, part := range strings.Split(spec, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			compiled, err := cascadia.Compile(part)
			if err != nil {
				continue
			}
			if nodes := cascadia.QueryAll(document, compiled); len(nodes) > 0 {
				return nodes
			}
		}
	}
	return nil
}

func trackerFirst(root *html.Node, spec string) *html.Node {
	if root == nil {
		return nil
	}
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		compiled, err := cascadia.Compile(part)
		if err != nil {
			continue
		}
		if node := cascadia.Query(root, compiled); node != nil {
			return node
		}
	}
	return nil
}

func trackerText(node *html.Node) string {
	if node == nil {
		return ""
	}
	var builder strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			builder.WriteString(n.Data)
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(node)
	return strings.Join(strings.Fields(builder.String()), " ")
}

func trackerAttr(node *html.Node, name string) string {
	if node == nil {
		return ""
	}
	for _, attr := range node.Attr {
		if strings.EqualFold(attr.Key, name) {
			return strings.TrimSpace(attr.Val)
		}
	}
	return ""
}

// trackerHiddenFields returns the hidden inputs (and any CSRF/nonce input) of
// the first form in body.
func trackerHiddenFields(body []byte) map[string]string {
	document, err := trackerDocument(body)
	if err != nil {
		return nil
	}
	out := make(map[string]string)
	for _, node := range trackerAll(document, "input[name]") {
		name := trackerAttr(node, "name")
		if name == "" {
			continue
		}
		lower := strings.ToLower(name)
		if lower == "username" || lower == "password" || lower == "login" || lower == "submit" {
			continue
		}
		typ := strings.ToLower(trackerAttr(node, "type"))
		isToken := strings.Contains(lower, "csrf") || strings.Contains(lower, "token") ||
			strings.Contains(lower, "nonce") || strings.Contains(lower, "security")
		if typ != "hidden" && !isToken {
			continue
		}
		out[name] = trackerAttr(node, "value")
	}
	return out
}

func trackerAll(root *html.Node, selector string) []*html.Node {
	compiled, err := cascadia.Compile(selector)
	if err != nil {
		return nil
	}
	return cascadia.QueryAll(root, compiled)
}

var trackerLoginFailureMarkers = []string{
	"login failed",
	"invalid username",
	"incorrect password",
	"bad username",
	"wrong password",
	"username or password is incorrect",
}

func trackerLoginFailed(body []byte) bool {
	lower := strings.ToLower(string(body))
	for _, marker := range trackerLoginFailureMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	// A redirect chain that ends on the login form means the credentials were
	// not accepted (the POST redirect lost the new session cookie).
	if strings.Contains(lower, `type="password"`) &&
		(strings.Contains(lower, "takelogin.php") || strings.Contains(lower, "page=login")) {
		return true
	}
	return false
}

func trackerQueryCategoryID(raw string) int {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return 0
	}
	for key, list := range parsed.Query() {
		lower := strings.ToLower(key)
		if lower != "cat" && lower != "category" && lower != "catid" {
			continue
		}
		for _, value := range list {
			if id := trackerLeadingInt(value); id > 0 {
				return id
			}
		}
	}
	return 0
}

// trackerID returns the stable site id carried by a details or download URL.
func trackerID(rawURLs ...string) string {
	for _, raw := range rawURLs {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		parsed, err := url.Parse(raw)
		if err != nil {
			continue
		}
		query := parsed.Query()
		for _, key := range []string{"id", "torrent", "tid", "torrentid"} {
			if value := strings.TrimSpace(query.Get(key)); value != "" {
				if _, err := strconv.Atoi(value); err == nil {
					return value
				}
			}
		}
	}
	return ""
}

func trackerIDFromMagnetOrURL(rawURLs ...string) string {
	for _, raw := range rawURLs {
		if hash := infoHashFromMagnet(raw); hash != "" {
			return hash
		}
		if id := trackerID(raw); id != "" {
			return id
		}
	}
	return ""
}

func trackerItemID(item engine.Item) string {
	if item.Attributes != nil {
		if id := strings.TrimSpace(item.Attributes["id"]); id != "" {
			return id
		}
		if id := trackerID(item.Attributes["download"]); id != "" {
			return id
		}
	}
	if id := trackerID(item.DetailsURL, item.DownloadURL); id != "" {
		return id
	}
	guid := strings.TrimSpace(item.GUID)
	if guid != "" {
		if _, err := strconv.Atoi(guid); err == nil {
			return guid
		}
	}
	return ""
}

func trackerLeadingInt(value string) int {
	value = strings.TrimSpace(value)
	started := false
	end := 0
	for index, r := range value {
		if r >= '0' && r <= '9' {
			if !started {
				started = true
			}
			end = index + 1
			continue
		}
		if started {
			break
		}
	}
	if !started {
		return 0
	}
	number, err := strconv.Atoi(value[:end])
	if err != nil {
		return 0
	}
	return number
}

var (
	trackerSizePattern = regexp.MustCompile(`(?i)^([0-9]+(?:[.,][0-9]+)*)\s*([kmgtpe]?i?b)?`)
	trackerIntPattern  = regexp.MustCompile(`[0-9][0-9,.\s]*`)
)

func trackerParseSize(value string) int64 {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\u00a0", " "))
	if value == "" || value == "-" {
		return 0
	}
	match := trackerSizePattern.FindStringSubmatch(value)
	if match == nil {
		return 0
	}
	number := trackerParseFloat(match[1])
	unit := strings.ToUpper(strings.TrimSpace(match[2]))
	multiplier := float64(1)
	switch unit {
	case "", "B":
	case "KB", "KIB":
		multiplier = 1 << 10
	case "MB", "MIB":
		multiplier = 1 << 20
	case "GB", "GIB":
		multiplier = 1 << 30
	case "TB", "TIB":
		multiplier = 1 << 40
	case "PB", "PIB":
		multiplier = 1 << 50
	default:
		return 0
	}
	return int64(math.Round(number * multiplier))
}

func trackerParseFloat(raw string) float64 {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	if strings.Contains(raw, ",") {
		parts := strings.Split(raw, ",")
		thousands := len(parts) > 1
		for _, part := range parts[1:] {
			if len(part) != 3 {
				thousands = false
				break
			}
		}
		if thousands {
			raw = strings.Join(parts, "")
		} else {
			raw = strings.ReplaceAll(raw, ",", ".")
		}
	}
	number, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0
	}
	return number
}

func trackerParseInt(value string) int64 {
	raw := trackerIntPattern.FindString(value)
	raw = strings.ReplaceAll(raw, " ", "")
	raw = strings.ReplaceAll(raw, ",", "")
	raw = strings.TrimSuffix(raw, ".")
	if raw == "" {
		return 0
	}
	number, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0
	}
	return int64(number)
}

func trackerParseDate(value string) time.Time {
	value = strings.TrimSpace(value)
	if value == "" || value == "-" {
		return time.Time{}
	}
	layouts := []string{
		"2006-01-02 15:04:05",
		"02/01/2006 15:04",
		"02-01-2006 15:04",
		"02/01/2006 15:04:05",
		"02-01-2006 15:04:05",
		"2006-01-02 15:04",
		"02/01/2006",
		"02-01-2006",
		"2006-01-02",
	}
	for _, layout := range layouts {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed
		}
	}
	return parseDate(value)
}
