package family

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
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

const (
	nexusPHPBreakerFallback = "nexusphp"
	nexusLoginTTL           = 6 * time.Hour
	nexusMaxPages           = 5
	nexusAssumedPageSize    = 50
)

// NexusPHPConfig configures one NexusPHP tracker. Username and Password are
// optional: a public NexusPHP site can be searched with only BaseURL.
//
// CategoryMap maps a site category id (as text) or a category name to a
// standard (Torznab) category. Categories lists the site category ids sent in
// the cat query parameter.
type NexusPHPConfig struct {
	Indexer     engine.IndexerRef
	BaseURL     string
	Username    string
	Password    string
	Passkey     string
	Transport   *engine.Transport
	Categories  []int
	UserAgent   string
	Profile     NexusPHPProfile
	CategoryMap map[string]int
}

// NexusPHPProfile describes the site-specific HTML paths and selectors.
type NexusPHPProfile struct {
	LoginPath, LoginUserField, LoginPassField, LoginSuccessSelector, LoginErrorSelector string
	SearchPath, RowsSelector, TitleSelector, DetailsSelector, DownloadSelector,
	SizeSelector, SeedersSelector, LeechersSelector, DateSelector, CategorySelector string
	PageParam string
}

// DefaultNexusPHPProfile returns selectors that match the stock NexusPHP
// torrent table. LoginPath is the login page; the form is posted to its sibling
// takelogin.php.
func DefaultNexusPHPProfile() NexusPHPProfile {
	return NexusPHPProfile{
		LoginPath:            "/login.php",
		LoginUserField:       "username",
		LoginPassField:       "password",
		LoginSuccessSelector: `a[href*="logout.php"]`,
		LoginErrorSelector:   `.error, .alert-error, p.error`,
		SearchPath:           "/torrents.php",
		RowsSelector:         "table.torrents tr",
		TitleSelector:        `a[href*="details.php"]`,
		DetailsSelector:      `a[href*="details.php"]`,
		DownloadSelector:     `a[href*="download.php"]`,
		SizeSelector:         "td:nth-child(5)",
		SeedersSelector:      "td:nth-child(6)",
		LeechersSelector:     "td:nth-child(7)",
		DateSelector:         "td:nth-child(4)",
		CategorySelector:     `a[href*="cat="]`,
		PageParam:            "page",
	}
}

var _ engine.Connector = (*NexusPHP)(nil)

// NexusPHP is the native connector shared by every NexusPHP tracker.
type NexusPHP struct {
	cfg           NexusPHPConfig
	base          *url.URL
	breaker       string
	loginPage     string
	loginPostPath string
	downloadPS    string
	categories    map[string]int

	jar *nexusCookieJar

	authMu   sync.Mutex
	loggedIn bool
	loginAt  time.Time
}

// NewNexusPHP validates cfg and builds a connector.
func NewNexusPHP(cfg NexusPHPConfig) (*NexusPHP, error) {
	base, err := parseBaseURL(cfg.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("nexusphp: %w", err)
	}
	cfg.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	cfg.Username = strings.TrimSpace(cfg.Username)
	cfg.Passkey = strings.TrimSpace(cfg.Passkey)
	cfg.Profile = nexusMergeProfile(cfg.Profile)
	if cfg.Transport == nil {
		cfg.Transport = engine.NewTransport(engine.TransportConfig{UserAgent: cfg.UserAgent})
	}
	cfg.Indexer.Protocol = engine.ProtocolTorrent
	cfg.Categories = append([]int(nil), cfg.Categories...)

	if !strings.HasSuffix(base.Path, "/") {
		base.Path += "/"
	}

	categoryMap := nexusDefaultCategoryMap()
	for key, standard := range cfg.CategoryMap {
		categoryMap[strings.ToLower(strings.TrimSpace(key))] = standard
	}

	breaker := strings.TrimSpace(cfg.Indexer.Slug)
	if breaker == "" {
		breaker = nexusPHPBreakerFallback
	}

	return &NexusPHP{
		cfg:           cfg,
		base:          base,
		breaker:       breaker,
		loginPage:     nexusAbsolutePath(cfg.Profile.LoginPath),
		loginPostPath: nexusLoginPostPath(cfg.Profile.LoginPath),
		downloadPS:    nexusSiblingPath(cfg.Profile.SearchPath, "download.php"),
		categories:    categoryMap,
		jar:           newNexusCookieJar(),
	}, nil
}

// Capabilities implements engine.Connector.
func (n *NexusPHP) Capabilities() engine.Capabilities {
	return engine.Capabilities{
		Protocol: engine.ProtocolTorrent,
		Modes:    []engine.Mode{engine.ModeSearch, engine.ModeTV, engine.ModeMovie},
		Params: map[engine.Mode][]string{
			engine.ModeSearch: {engine.ParamQuery},
			engine.ModeTV: {
				engine.ParamQuery,
				engine.ParamSeason,
				engine.ParamEpisode,
				engine.ParamIMDbID,
			},
			engine.ModeMovie: {
				engine.ParamQuery,
				engine.ParamIMDbID,
			},
		},
		Categories: append([]int(nil), n.cfg.Categories...),
	}
}

// Search implements engine.Connector.
func (n *NexusPHP) Search(ctx context.Context, req engine.SearchRequest) ([]engine.Item, error) {
	mode := req.Mode
	if mode == "" {
		mode = engine.ModeSearch
	}
	switch mode {
	case engine.ModeSearch, engine.ModeTV, engine.ModeMovie:
	default:
		return nil, fmt.Errorf("nexusphp: unsupported search mode %q", mode)
	}

	text := nexusSearchText(req, mode)
	categories := req.Categories
	if len(categories) == 0 {
		categories = n.cfg.Categories
	}
	return n.paginate(ctx, text, categories, req.Limit, req.Offset)
}

// Latest implements engine.Connector. NexusPHP has no separate latest API; an
// unfiltered torrents.php page is the latest feed.
func (n *NexusPHP) Latest(ctx context.Context, req engine.LatestRequest) ([]engine.Item, error) {
	categories := req.Categories
	if len(categories) == 0 {
		categories = n.cfg.Categories
	}
	return n.paginate(ctx, "", categories, req.Limit, 0)
}

// Download implements engine.Connector. It appends the configured passkey when
// the site link does not carry one and surfaces magnet redirects.
func (n *NexusPHP) Download(ctx context.Context, item engine.Item) (engine.Payload, error) {
	target := strings.TrimSpace(item.DownloadURL)
	if target == "" {
		id := firstNonEmpty(item.Attributes["id"], nexusGUIDID(item.GUID), nexusQueryID(item.DetailsURL))
		if id != "" {
			target = n.downloadURL(id)
		}
	}
	if target == "" {
		magnet := strings.TrimSpace(item.Magnet)
		if magnet != "" {
			return engine.Payload{Magnet: magnet}, nil
		}
		return engine.Payload{}, fmt.Errorf("nexusphp: download URL is missing")
	}
	if strings.HasPrefix(strings.ToLower(target), "magnet:") {
		return engine.Payload{Magnet: target}, nil
	}
	target = resolveURL(n.base, target)
	target = n.withPasskey(target)

	resp, err := n.doAuthenticated(ctx, target, "application/x-bittorrent")
	if err != nil {
		return engine.Payload{}, err
	}
	if resp.Status >= 300 && resp.Status < 400 {
		location := strings.TrimSpace(resp.Header.Get("Location"))
		if strings.HasPrefix(strings.ToLower(location), "magnet:") {
			return engine.Payload{Magnet: location}, nil
		}
		return engine.Payload{}, fmt.Errorf("nexusphp: download redirected without a magnet (HTTP %d)", resp.Status)
	}
	if resp.Status < 200 || resp.Status >= 300 {
		return engine.Payload{}, fmt.Errorf("nexusphp: download failed with HTTP status %d", resp.Status)
	}
	return engine.Payload{
		Body:        resp.Body,
		ContentType: "application/x-bittorrent",
		Filename:    safeFilename(item.Title, ".torrent"),
	}, nil
}

// Test implements engine.Connector. Download is not tested because it would
// consume a real release.
func (n *NexusPHP) Test(ctx context.Context) (engine.TestResult, error) {
	result := engine.TestResult{}

	reach := func() error {
		target := joinPath(n.cfg.BaseURL, n.loginPage)
		resp, err := n.rawRequest(ctx, http.MethodGet, target, "text/html")
		if err != nil {
			return err
		}
		if !reachableStatus(resp.Status) {
			return fmt.Errorf("unexpected HTTP status %d", resp.Status)
		}
		return nil
	}
	addPhase(&result, "reach", reach)

	login := func() error {
		if !n.hasCredentials() {
			return fmt.Errorf("username and password are required")
		}
		return n.ensureLogin(ctx, true)
	}
	addPhase(&result, "login", login)

	search := func() error {
		_, err := n.paginate(ctx, "test", n.cfg.Categories, 1, 0)
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

func (n *NexusPHP) paginate(ctx context.Context, text string, categories []int, limit, offset int) ([]engine.Item, error) {
	targetLimit := limit
	if targetLimit <= 0 {
		targetLimit = 100
	}
	startPage := nexusStartPage(offset)
	seen := make(map[string]struct{})
	items := make([]engine.Item, 0, targetLimit)

	for page := startPage; page < startPage+nexusMaxPages; page++ {
		pageItems, err := n.searchPage(ctx, text, categories, page)
		if err != nil {
			return nil, err
		}
		if len(pageItems) == 0 {
			break
		}
		for _, item := range pageItems {
			key := nexusDedupKey(item)
			if key != "" {
				if _, exists := seen[key]; exists {
					continue
				}
				seen[key] = struct{}{}
			}
			items = append(items, item)
			if len(items) >= targetLimit {
				return items, nil
			}
		}
	}
	return items, nil
}

func (n *NexusPHP) searchPage(ctx context.Context, text string, categories []int, page int) ([]engine.Item, error) {
	values := url.Values{}
	if strings.TrimSpace(text) != "" {
		values.Set("search", text)
	}
	pageParam := firstNonEmpty(n.cfg.Profile.PageParam, "page")
	values.Set(pageParam, strconv.Itoa(page))
	if len(categories) > 0 {
		ids := make([]string, 0, len(categories))
		for _, category := range categories {
			if category > 0 {
				ids = append(ids, strconv.Itoa(category))
			}
		}
		if len(ids) > 0 {
			values.Set("cat", strings.Join(ids, ","))
		}
	}

	target, err := withQuery(joinPath(n.cfg.BaseURL, n.cfg.Profile.SearchPath), values)
	if err != nil {
		return nil, redactError("nexusphp: build request", err, n.secrets()...)
	}
	resp, err := n.doAuthenticated(ctx, target, "text/html")
	if err != nil {
		return nil, err
	}
	if resp.Status < 200 || resp.Status >= 300 {
		return nil, fmt.Errorf("nexusphp: search failed with HTTP status %d", resp.Status)
	}
	return n.parseRows(resp.Body)
}

func (n *NexusPHP) parseRows(body []byte) ([]engine.Item, error) {
	document, err := nexusParseDocument(body)
	if err != nil {
		return nil, fmt.Errorf("nexusphp: parse response: %w", err)
	}
	selector, err := cascadia.Compile(n.cfg.Profile.RowsSelector)
	if err != nil {
		return nil, fmt.Errorf("nexusphp: invalid rows selector %q: %w", n.cfg.Profile.RowsSelector, err)
	}
	rows := cascadia.QueryAll(document, selector)
	items := make([]engine.Item, 0, len(rows))
	for _, row := range rows {
		item, ok, err := n.parseRow(row)
		if err != nil {
			return nil, err
		}
		if ok {
			items = append(items, item)
		}
	}
	return items, nil
}

func (n *NexusPHP) parseRow(row *html.Node) (engine.Item, bool, error) {
	title, err := nexusSelectText(row, n.cfg.Profile.TitleSelector)
	if err != nil {
		return engine.Item{}, false, err
	}
	title = strings.Join(strings.Fields(title), " ")
	if title == "" {
		return engine.Item{}, false, nil
	}

	detailsHref, err := nexusSelectHref(row, n.cfg.Profile.DetailsSelector)
	if err != nil {
		return engine.Item{}, false, err
	}
	if detailsHref == "" {
		detailsHref = nexusFirstHref(row, func(href string) bool {
			return strings.Contains(strings.ToLower(href), "details.php")
		})
	}
	downloadHref, err := nexusSelectHref(row, n.cfg.Profile.DownloadSelector)
	if err != nil {
		return engine.Item{}, false, err
	}
	if downloadHref == "" {
		downloadHref = nexusFirstHref(row, func(href string) bool {
			return strings.Contains(strings.ToLower(href), "download.php")
		})
	}

	id := firstNonEmpty(nexusQueryID(detailsHref), nexusQueryID(downloadHref))
	hash, err := nexusInfoHash(row)
	if err != nil {
		return engine.Item{}, false, err
	}
	siteCategory, categoryText, err := n.categoryFromRow(row)
	if err != nil {
		return engine.Item{}, false, err
	}

	sizeText, err := nexusSelectText(row, n.cfg.Profile.SizeSelector)
	if err != nil {
		return engine.Item{}, false, err
	}
	seedersText, err := nexusSelectText(row, n.cfg.Profile.SeedersSelector)
	if err != nil {
		return engine.Item{}, false, err
	}
	leechersText, err := nexusSelectText(row, n.cfg.Profile.LeechersSelector)
	if err != nil {
		return engine.Item{}, false, err
	}
	dateText, err := nexusSelectText(row, n.cfg.Profile.DateSelector)
	if err != nil {
		return engine.Item{}, false, err
	}

	detailsURL := resolveURL(n.base, detailsHref)
	downloadURL := resolveURL(n.base, downloadHref)
	if strings.HasPrefix(strings.ToLower(downloadURL), "magnet:") {
		downloadURL = ""
	}
	if downloadURL != "" {
		downloadURL = n.withPasskey(downloadURL)
	} else if id != "" {
		downloadURL = n.downloadURL(id)
	}

	guid := ""
	switch {
	case id != "":
		guid = "nexusphp:" + id
	case hash != "":
		guid = "nexusphp:" + hash
	default:
		guid = detailsURL
	}

	attributes := map[string]string{}
	setAttribute := func(key, value string) {
		if strings.TrimSpace(value) != "" {
			attributes[key] = value
		}
	}
	setAttribute("id", id)
	setAttribute("info_hash", hash)
	setAttribute("category", categoryText)
	if siteCategory > 0 {
		setAttribute("site_category_id", strconv.Itoa(siteCategory))
	}
	setAttribute("details_link", detailsURL)

	return engine.Item{
		Protocol:    engine.ProtocolTorrent,
		Title:       title,
		GUID:        guid,
		DetailsURL:  detailsURL,
		DownloadURL: downloadURL,
		InfoHash:    hash,
		Size:        nexusParseSize(sizeText),
		Seeders:     nexusParseCount(seedersText),
		Leechers:    nexusParseCount(leechersText),
		Categories:  n.categoriesFor(siteCategory, categoryText),
		Published:   nexusParseDate(dateText),
		Attributes:  attributes,
	}, true, nil
}

func (n *NexusPHP) categoryFromRow(row *html.Node) (int, string, error) {
	siteID := 0
	text := ""

	if node, err := nexusSelectNode(row, n.cfg.Profile.CategorySelector); err != nil {
		return 0, "", err
	} else if node != nil {
		text = strings.Join(strings.Fields(nexusNodeText(node)), " ")
		siteID = nexusCategoryIDFromHref(nexusAttr(node, "href"))
		if text == "" {
			text = strings.TrimSpace(nexusAttr(node, "title"))
		}
	}

	if siteID == 0 {
		anchors, err := nexusAnchors(row)
		if err != nil {
			return 0, "", err
		}
		for _, anchor := range anchors {
			if id := nexusCategoryIDFromHref(nexusAttr(anchor, "href")); id > 0 {
				siteID = id
				if text == "" {
					text = strings.Join(strings.Fields(nexusNodeText(anchor)), " ")
				}
				break
			}
		}
	}
	return siteID, text, nil
}

func (n *NexusPHP) categoriesFor(siteID int, text string) []int {
	if siteID > 0 {
		if mapped, ok := n.categories[strconv.Itoa(siteID)]; ok {
			if mapped > 0 {
				return []int{mapped}
			}
			return nil
		}
		if siteID >= 1000 {
			return []int{siteID}
		}
	}
	if mapped, ok := n.categories[strings.ToLower(strings.TrimSpace(text))]; ok && mapped > 0 {
		return []int{mapped}
	}
	return nil
}

func (n *NexusPHP) downloadURL(id string) string {
	values := url.Values{}
	values.Set("id", id)
	if n.cfg.Passkey != "" {
		values.Set("passkey", n.cfg.Passkey)
	}
	target, err := withQuery(joinPath(n.cfg.BaseURL, n.downloadPS), values)
	if err != nil {
		return joinPath(n.cfg.BaseURL, n.downloadPS)
	}
	return target
}

func (n *NexusPHP) withPasskey(raw string) string {
	if n.cfg.Passkey == "" || strings.TrimSpace(raw) == "" {
		return raw
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	if !strings.EqualFold(parsed.Host, n.base.Host) {
		return raw
	}
	query := parsed.Query()
	if query.Get("passkey") == "" && query.Get("authkey") == "" {
		query.Set("passkey", n.cfg.Passkey)
	}
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func (n *NexusPHP) hasCredentials() bool {
	return n.cfg.Username != "" && n.cfg.Password != ""
}

func (n *NexusPHP) secrets() []string {
	return []string{n.cfg.Password, n.cfg.Passkey}
}

func (n *NexusPHP) invalidate() {
	n.authMu.Lock()
	n.loggedIn = false
	n.loginAt = time.Time{}
	n.authMu.Unlock()
}

func (n *NexusPHP) ensureLogin(ctx context.Context, force bool) error {
	if !n.hasCredentials() {
		return nil
	}
	n.authMu.Lock()
	defer n.authMu.Unlock()
	if !force && n.loggedIn && time.Since(n.loginAt) < nexusLoginTTL {
		return nil
	}
	if err := n.loginLocked(ctx); err != nil {
		n.loggedIn = false
		n.loginAt = time.Time{}
		n.jar.reset()
		return err
	}
	n.loggedIn = true
	n.loginAt = time.Now()
	return nil
}

func (n *NexusPHP) loginLocked(ctx context.Context) error {
	loginPage := joinPath(n.cfg.BaseURL, n.loginPage)
	resp, err := n.rawRequest(ctx, http.MethodGet, loginPage, "text/html")
	if err != nil {
		return err
	}
	if resp.Status < 200 || resp.Status >= 300 {
		return fmt.Errorf("nexusphp: login page returned HTTP status %d", resp.Status)
	}
	document, err := nexusParseDocument(resp.Body)
	if err != nil {
		return fmt.Errorf("nexusphp: parse login page: %w", err)
	}
	hidden, err := nexusHiddenInputs(document)
	if err != nil {
		return err
	}

	form := url.Values{}
	for key, value := range hidden {
		form.Set(key, value)
	}
	userField := firstNonEmpty(n.cfg.Profile.LoginUserField, "username")
	passField := firstNonEmpty(n.cfg.Profile.LoginPassField, "password")
	form.Set(userField, n.cfg.Username)
	form.Set(passField, n.cfg.Password)

	target := joinPath(n.cfg.BaseURL, n.loginPostPath)
	postResp, err := n.loginPost(ctx, target, loginPage, form)
	if err != nil {
		return err
	}
	if postResp.Status >= 300 && postResp.Status < 400 {
		location := strings.TrimSpace(postResp.Header.Get("Location"))
		if location == "" {
			return fmt.Errorf("nexusphp: login redirected without a Location")
		}
		resolved := resolveURL(n.base, location)
		parsed, err := url.Parse(resolved)
		if err != nil || parsed.Host == "" {
			return fmt.Errorf("nexusphp: login redirected to an invalid URL")
		}
		if !strings.EqualFold(parsed.Host, n.base.Host) {
			return fmt.Errorf("nexusphp: login redirected off the tracker host")
		}
		// The POST's Set-Cookie header has been captured; fetch the target
		// page with the session cookie so success selectors can be checked.
		follow, err := n.rawRequest(ctx, http.MethodGet, resolved, "text/html")
		if err != nil {
			return err
		}
		if follow.Status < 200 || follow.Status >= 300 {
			return fmt.Errorf("nexusphp: login redirect returned HTTP status %d", follow.Status)
		}
		return n.checkLoginResponse(follow.Body)
	}
	if postResp.Status < 200 || postResp.Status >= 300 {
		return fmt.Errorf("nexusphp: login failed with HTTP status %d", postResp.Status)
	}
	return n.checkLoginResponse(postResp.Body)
}

// loginPost sends the login form with redirects disabled so the Set-Cookie
// header on a 302 is captured instead of being discarded by the redirect
// follower. It reuses the shared transport's timeout, size limit, proxy and
// SSRF policy; the shared rate limiter and breaker still guard every other
// request.
func (n *NexusPHP) loginPost(ctx context.Context, target, referer string, form url.Values) (*engine.Response, error) {
	if state := n.cfg.Transport.BreakerState(n.breaker); state == engine.BreakerOpen {
		return nil, fmt.Errorf("nexusphp: login request: %w", engine.ErrBreakerOpen)
	}
	config := n.cfg.Transport.Config()
	timeout := config.Timeout
	if timeout <= 0 {
		timeout = 45 * time.Second
	}
	maxBody := config.MaxBodyBytes
	if maxBody <= 0 {
		maxBody = 16 << 20
	}
	parsedTarget, err := url.Parse(target)
	if err != nil {
		return nil, redactError("nexusphp: build login request", err, n.secrets()...)
	}
	guard := &engine.Guard{AllowPrivate: config.AllowPrivate}
	if err := guard.CheckURL(parsedTarget); err != nil {
		return nil, redactError("nexusphp: login request", err, n.secrets()...)
	}

	dialer := &net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}
	httpTransport := &http.Transport{
		DialContext:           guard.DialContext(dialer),
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          4,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
	if strings.TrimSpace(config.ProxyURL) != "" {
		proxy, err := url.Parse(config.ProxyURL)
		if err != nil {
			return nil, fmt.Errorf("nexusphp: login proxy is invalid")
		}
		httpTransport.Proxy = http.ProxyURL(proxy)
	}
	client := &http.Client{
		Transport: httpTransport,
		Timeout:   timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, redactError("nexusphp: build login request", err, n.secrets()...)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "text/html")
	if referer != "" {
		req.Header.Set("Referer", referer)
	}
	userAgent := n.cfg.UserAgent
	if userAgent == "" {
		userAgent = config.UserAgent
	}
	if userAgent != "" {
		req.Header.Set("User-Agent", userAgent)
	}
	if cookie := n.jar.header(); cookie != "" {
		req.Header.Set("Cookie", cookie)
	}

	raw, err := client.Do(req)
	if err != nil {
		return nil, redactError("nexusphp: login request", err, n.secrets()...)
	}
	defer raw.Body.Close()
	body, err := nexusReadLimited(raw.Body, maxBody)
	if err != nil {
		return nil, redactError("nexusphp: read login response", err, n.secrets()...)
	}
	response := &engine.Response{
		Status: raw.StatusCode,
		Header: raw.Header.Clone(),
		Body:   body,
		URL:    raw.Request.URL,
	}
	n.jar.capture(response.Header)
	return response, nil
}

func nexusReadLimited(body io.Reader, max int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(body, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, engine.ErrBodyTooLarge
	}
	return data, nil
}

func (n *NexusPHP) checkLoginResponse(body []byte) error {
	document, err := nexusParseDocument(body)
	if err != nil {
		return fmt.Errorf("nexusphp: parse login response: %w", err)
	}
	if selector := strings.TrimSpace(n.cfg.Profile.LoginErrorSelector); selector != "" {
		node, err := nexusSelectNode(document, selector)
		if err != nil {
			return err
		}
		if node != nil {
			message := strings.Join(strings.Fields(nexusNodeText(node)), " ")
			if message == "" {
				message = "site reported an error"
			}
			return redactError("nexusphp: login failed", fmt.Errorf("%s", message), n.secrets()...)
		}
	}
	if selector := strings.TrimSpace(n.cfg.Profile.LoginSuccessSelector); selector != "" {
		present, err := nexusSelectorPresent(document, selector)
		if err != nil {
			return err
		}
		if !present {
			return fmt.Errorf("nexusphp: login failed: success marker %q not found", selector)
		}
	}
	return nil
}

func (n *NexusPHP) doAuthenticated(ctx context.Context, target, accept string) (*engine.Response, error) {
	if n.hasCredentials() {
		if err := n.ensureLogin(ctx, false); err != nil {
			return nil, err
		}
	}
	resp, err := n.rawRequest(ctx, http.MethodGet, target, accept)
	if err != nil {
		return nil, err
	}
	if (resp.Status == http.StatusUnauthorized || resp.Status == http.StatusForbidden) && n.hasCredentials() {
		n.invalidate()
		n.jar.reset()
		if err := n.ensureLogin(ctx, true); err != nil {
			return nil, err
		}
		return n.rawRequest(ctx, http.MethodGet, target, accept)
	}
	return resp, nil
}

func (n *NexusPHP) rawRequest(ctx context.Context, method, target, accept string) (*engine.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, target, nil)
	if err != nil {
		return nil, redactError("nexusphp: build request", err, n.secrets()...)
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	if n.cfg.UserAgent != "" {
		req.Header.Set("User-Agent", n.cfg.UserAgent)
	}
	if cookie := n.jar.header(); cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	resp, err := n.cfg.Transport.Do(ctx, n.breaker, req)
	if err != nil {
		return nil, redactError("nexusphp: request", err, n.secrets()...)
	}
	n.jar.capture(resp.Header)
	return resp, nil
}

type nexusCookieJar struct {
	mu      sync.Mutex
	cookies map[string]string
}

func newNexusCookieJar() *nexusCookieJar {
	return &nexusCookieJar{cookies: make(map[string]string)}
}

func (j *nexusCookieJar) capture(header http.Header) {
	if j == nil || header == nil {
		return
	}
	response := &http.Response{Header: header}
	cookies := response.Cookies()
	if len(cookies) == 0 {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, cookie := range cookies {
		if cookie == nil || cookie.Name == "" {
			continue
		}
		if cookie.MaxAge < 0 || (!cookie.Expires.IsZero() && cookie.Expires.Before(time.Now())) {
			delete(j.cookies, cookie.Name)
			continue
		}
		j.cookies[cookie.Name] = cookie.Value
	}
}

func (j *nexusCookieJar) header() string {
	if j == nil {
		return ""
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if len(j.cookies) == 0 {
		return ""
	}
	names := make([]string, 0, len(j.cookies))
	for name := range j.cookies {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, (&http.Cookie{Name: name, Value: j.cookies[name]}).String())
	}
	return strings.Join(parts, "; ")
}

func (j *nexusCookieJar) reset() {
	if j == nil {
		return
	}
	j.mu.Lock()
	j.cookies = make(map[string]string)
	j.mu.Unlock()
}

func nexusMergeProfile(profile NexusPHPProfile) NexusPHPProfile {
	defaults := DefaultNexusPHPProfile()
	set := func(dst *string, value string) {
		if strings.TrimSpace(*dst) == "" {
			*dst = value
		}
	}
	set(&profile.LoginPath, defaults.LoginPath)
	set(&profile.LoginUserField, defaults.LoginUserField)
	set(&profile.LoginPassField, defaults.LoginPassField)
	set(&profile.LoginSuccessSelector, defaults.LoginSuccessSelector)
	set(&profile.LoginErrorSelector, defaults.LoginErrorSelector)
	set(&profile.SearchPath, defaults.SearchPath)
	set(&profile.RowsSelector, defaults.RowsSelector)
	set(&profile.TitleSelector, defaults.TitleSelector)
	set(&profile.DetailsSelector, defaults.DetailsSelector)
	set(&profile.DownloadSelector, defaults.DownloadSelector)
	set(&profile.SizeSelector, defaults.SizeSelector)
	set(&profile.SeedersSelector, defaults.SeedersSelector)
	set(&profile.LeechersSelector, defaults.LeechersSelector)
	set(&profile.DateSelector, defaults.DateSelector)
	set(&profile.CategorySelector, defaults.CategorySelector)
	set(&profile.PageParam, defaults.PageParam)
	return profile
}

func nexusLoginPostPath(loginPath string) string {
	cleaned := strings.TrimSpace(loginPath)
	if cleaned == "" {
		return "/takelogin.php"
	}
	base := path.Base(cleaned)
	if strings.EqualFold(base, "login.php") {
		return nexusAbsolutePath(path.Join(path.Dir(cleaned), "takelogin.php"))
	}
	return nexusAbsolutePath(cleaned)
}

func nexusAbsolutePath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "/"
	}
	if !strings.HasPrefix(value, "/") {
		return "/" + value
	}
	return value
}

func nexusSiblingPath(searchPath, name string) string {
	cleaned := strings.TrimSpace(searchPath)
	if cleaned == "" {
		return "/" + name
	}
	return nexusAbsolutePath(path.Join(path.Dir(cleaned), name))
}

func nexusSearchText(req engine.SearchRequest, mode engine.Mode) string {
	text := firstNonEmpty(req.Query, req.Title)
	if mode == engine.ModeTV {
		if text == "" {
			text = strings.TrimSpace(req.IMDbID)
		}
		switch {
		case req.Season > 0 && req.Episode > 0:
			marker := fmt.Sprintf("S%02dE%02d", req.Season, req.Episode)
			if !strings.Contains(strings.ToUpper(text), marker) {
				text = strings.TrimSpace(text + " " + marker)
			}
		case req.Season > 0:
			marker := fmt.Sprintf("S%02d", req.Season)
			if !strings.Contains(strings.ToUpper(text), marker) {
				text = strings.TrimSpace(text + " " + marker)
			}
		}
	}
	if mode == engine.ModeMovie && text == "" {
		text = strings.TrimSpace(req.IMDbID)
	}
	return strings.TrimSpace(text)
}

func nexusStartPage(offset int) int {
	if offset <= 0 {
		return 1
	}
	return offset/nexusAssumedPageSize + 1
}

func nexusDedupKey(item engine.Item) string {
	if hash := normalizeInfoHash(item.InfoHash); hash != "" {
		return "hash:" + hash
	}
	if item.GUID != "" {
		return "guid:" + item.GUID
	}
	if item.DownloadURL != "" {
		return "download:" + item.DownloadURL
	}
	if item.DetailsURL != "" {
		return "details:" + item.DetailsURL
	}
	return ""
}

func nexusGUIDID(guid string) string {
	guid = strings.TrimSpace(guid)
	if strings.HasPrefix(guid, "nexusphp:") {
		suffix := strings.TrimPrefix(guid, "nexusphp:")
		if numericID(suffix) {
			return suffix
		}
	}
	return ""
}

func nexusQueryID(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(parsed.Query().Get("id"))
}

func nexusCategoryIDFromHref(href string) int {
	href = strings.TrimSpace(href)
	if href == "" {
		return 0
	}
	parsed, err := url.Parse(href)
	if err != nil {
		return 0
	}
	return parseInt(parsed.Query().Get("cat"))
}

func nexusParseDocument(body []byte) (*html.Node, error) {
	document, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	return document, nil
}

func nexusSelectorPresent(document *html.Node, selector string) (bool, error) {
	node, err := nexusSelectNode(document, selector)
	return node != nil, err
}

func nexusSelectNode(node *html.Node, selector string) (*html.Node, error) {
	selector = strings.TrimSpace(selector)
	if selector == "" || node == nil {
		return nil, nil
	}
	compiled, err := cascadia.Compile(selector)
	if err != nil {
		return nil, fmt.Errorf("nexusphp: invalid selector %q: %w", selector, err)
	}
	return cascadia.Query(node, compiled), nil
}

func nexusSelectText(node *html.Node, selector string) (string, error) {
	selected, err := nexusSelectNode(node, selector)
	if err != nil || selected == nil {
		return "", err
	}
	return nexusNodeText(selected), nil
}

func nexusSelectHref(node *html.Node, selector string) (string, error) {
	selected, err := nexusSelectNode(node, selector)
	if err != nil || selected == nil {
		return "", err
	}
	return strings.TrimSpace(nexusAttr(selected, "href")), nil
}

func nexusAnchors(node *html.Node) ([]*html.Node, error) {
	selector, err := cascadia.Compile("a[href]")
	if err != nil {
		return nil, err
	}
	return cascadia.QueryAll(node, selector), nil
}

func nexusFirstHref(node *html.Node, match func(string) bool) string {
	anchors, err := nexusAnchors(node)
	if err != nil {
		return ""
	}
	for _, anchor := range anchors {
		href := strings.TrimSpace(nexusAttr(anchor, "href"))
		if href != "" && match(href) {
			return href
		}
	}
	return ""
}

func nexusNodeText(node *html.Node) string {
	if node == nil {
		return ""
	}
	var out strings.Builder
	var walk func(*html.Node)
	walk = func(current *html.Node) {
		if current.Type == html.TextNode {
			out.WriteString(current.Data)
		}
		for child := current.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(node)
	return strings.TrimSpace(out.String())
}

func nexusAttr(node *html.Node, name string) string {
	if node == nil {
		return ""
	}
	for _, attr := range node.Attr {
		if strings.EqualFold(attr.Key, name) {
			return attr.Val
		}
	}
	return ""
}

func nexusHiddenInputs(document *html.Node) (map[string]string, error) {
	selector, err := cascadia.Compile("input")
	if err != nil {
		return nil, err
	}
	hidden := make(map[string]string)
	for _, node := range cascadia.QueryAll(document, selector) {
		if !strings.EqualFold(strings.TrimSpace(nexusAttr(node, "type")), "hidden") {
			continue
		}
		name := strings.TrimSpace(nexusAttr(node, "name"))
		if name == "" {
			continue
		}
		hidden[name] = nexusAttr(node, "value")
	}
	return hidden, nil
}

var nexusHashPattern = regexp.MustCompile(`(?i)\b[0-9a-f]{40}\b|\b[a-z2-7]{32}\b`)

func nexusInfoHash(row *html.Node) (string, error) {
	anchors, err := nexusAnchors(row)
	if err != nil {
		return "", err
	}
	for _, anchor := range anchors {
		href := strings.TrimSpace(nexusAttr(anchor, "href"))
		if href == "" {
			continue
		}
		if strings.HasPrefix(strings.ToLower(href), "magnet:") {
			if hash := normalizeInfoHash(infoHashFromMagnet(href)); hash != "" {
				return hash, nil
			}
		}
		parsed, err := url.Parse(href)
		if err == nil {
			for _, key := range []string{"info_hash", "infohash", "hash"} {
				if hash := normalizeInfoHash(parsed.Query().Get(key)); hash != "" {
					return hash, nil
				}
			}
		}
		if match := nexusHashPattern.FindString(href); match != "" {
			if hash := normalizeInfoHash(match); hash != "" {
				return hash, nil
			}
		}
	}
	return "", nil
}

var nexusSizePattern = regexp.MustCompile(`(?i)^([0-9][0-9.,]*)\s*([kmgtpe]?(?:i?b)?|bytes?)$`)

func nexusParseSize(raw string) int64 {
	value := strings.TrimSpace(raw)
	value = strings.Trim(value, "[]() ")
	if value == "" {
		return 0
	}
	match := nexusSizePattern.FindStringSubmatch(value)
	if match == nil {
		return 0
	}
	number := strings.TrimSpace(match[1])
	unit := strings.ToLower(strings.TrimSpace(match[2]))

	number = nexusNormalizeNumber(number)
	parsed, err := strconv.ParseFloat(number, 64)
	if err != nil || parsed < 0 {
		return 0
	}
	multipliers := map[string]float64{
		"": 1, "b": 1, "byte": 1, "bytes": 1,
		"k": 1 << 10, "kb": 1 << 10, "kib": 1 << 10,
		"m": 1 << 20, "mb": 1 << 20, "mib": 1 << 20,
		"g": 1 << 30, "gb": 1 << 30, "gib": 1 << 30,
		"t": 1 << 40, "tb": 1 << 40, "tib": 1 << 40,
		"p": 1 << 50, "pb": 1 << 50, "pib": 1 << 50,
		"e": 1 << 60, "eb": 1 << 60, "eib": 1 << 60,
	}
	multiplier, ok := multipliers[unit]
	if !ok {
		return 0
	}
	return int64(parsed * multiplier)
}

func nexusNormalizeNumber(value string) string {
	if !strings.Contains(value, ",") {
		return value
	}
	if strings.Contains(value, ".") {
		return strings.ReplaceAll(value, ",", "")
	}
	parts := strings.Split(value, ",")
	if len(parts) == 2 && len(parts[1]) == 3 {
		return parts[0] + parts[1]
	}
	return strings.Replace(value, ",", ".", 1)
}

func nexusParseCount(raw string) int64 {
	var digits strings.Builder
	for _, r := range raw {
		if r >= '0' && r <= '9' {
			digits.WriteRune(r)
		}
	}
	if digits.Len() == 0 {
		return 0
	}
	return parseInt64(digits.String())
}

func nexusParseDate(raw string) time.Time {
	value := strings.Join(strings.Fields(raw), " ")
	if value == "" {
		return time.Time{}
	}
	layouts := []string{
		"2006-01-02 15:04:05",
		"2006-01-02 15:04",
		"2006-01-02",
		"02-01-2006 15:04:05",
		"02-01-2006 15:04",
		"02-01-2006",
		"2-1-2006 15:04",
		"2-1-2006",
	}
	for _, layout := range layouts {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed
		}
	}
	return parseDate(value)
}

func nexusDefaultCategoryMap() map[string]int {
	return map[string]int{
		"401": 2000, "402": 5000, "403": 3000, "404": 4000,
		"405": 4000, "406": 7000, "407": 5070, "408": 5080,
		"409": 5060, "410": 8000,
		"movies": 2000, "movie": 2000,
		"tv": 5000, "tv series": 5000, "tv shows": 5000,
		"music": 3000, "audio": 3000,
		"games": 4000, "software": 4000, "apps": 4000, "applications": 4000,
		"books": 7000, "ebooks": 7000, "e-books": 7000,
		"anime": 5070, "documentaries": 5080, "documentary": 5080,
		"sports": 5060, "misc": 8000, "other": 8000,
	}
}
