package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/buzzqw/indexarr/internal/definition"
)

const defaultRefreshAfter = 6 * time.Hour

// DefinitionConfig builds a connector that interprets a native YAML
// definition. Settings must already be decrypted; values are never logged.
type DefinitionConfig struct {
	Indexer    IndexerRef
	Definition *definition.Definition
	Settings   map[string]string
	Transport  *Transport
}

var _ Connector = (*DefinitionConnector)(nil)

// DefinitionConnector executes one native definition. It owns an in-memory
// session (cookies plus login-extracted values) and is safe for concurrent use.
type DefinitionConnector struct {
	indexer   IndexerRef
	def       *definition.Definition
	settings  map[string]string
	declared  map[string]struct{}
	transport *Transport
	baseURL   *url.URL
	protocol  Protocol
	caps      Capabilities
	session   *Session
	authMu    sync.Mutex
	refresh   time.Duration
	appendKey bool
	secrets   []string

	downloadCache   *downloadCache
	resolveFailures atomic.Int64
}

// NewDefinitionConnector validates the definition's settings and prepares the
// connector. It does not perform any network I/O; login happens lazily.
func NewDefinitionConnector(cfg DefinitionConfig) (*DefinitionConnector, error) {
	def := cfg.Definition
	if def == nil {
		return nil, errors.New("engine: nil definition")
	}
	if len(def.Meta.URLs) == 0 || strings.TrimSpace(def.Meta.URLs[0]) == "" {
		return nil, fmt.Errorf("definition %q: meta.urls is empty", def.Meta.ID)
	}
	baseURL, err := url.Parse(strings.TrimSpace(def.Meta.URLs[0]))
	if err != nil || baseURL.Scheme == "" || baseURL.Host == "" {
		return nil, fmt.Errorf("definition %q: invalid meta.urls[0]", def.Meta.ID)
	}

	settings := make(map[string]string, len(def.Settings)+len(cfg.Settings))
	declared := make(map[string]struct{}, len(def.Settings))
	for name, value := range cfg.Settings {
		settings[name] = value
	}
	for _, setting := range def.Settings {
		declared[setting.Name] = struct{}{}
		if strings.TrimSpace(settings[setting.Name]) == "" && setting.Default != "" {
			settings[setting.Name] = setting.Default
		}
	}
	// The canonical Newznab setting name is api_key, but the definition
	// validator only allows slug names, so definitions frequently declare
	// "apikey". Accept both spellings and keep them in sync.
	if settings["api_key"] == "" {
		settings["api_key"] = settings["apikey"]
	}
	if settings["apikey"] == "" {
		settings["apikey"] = settings["api_key"]
	}
	for _, setting := range def.Settings {
		if setting.Required && strings.TrimSpace(settings[setting.Name]) == "" {
			return nil, fmt.Errorf("definition %q: missing required setting %q", def.Meta.ID, setting.Name)
		}
	}

	strategy := authStrategy(def)
	switch strategy {
	case "none", "api_key", "basic", "token", "form", "cookie":
	default:
		return nil, fmt.Errorf("definition %q: unknown auth strategy %q", def.Meta.ID, strategy)
	}
	if strategy == "api_key" && strings.TrimSpace(settings["api_key"]) == "" {
		return nil, fmt.Errorf("definition %q: missing required setting %q", def.Meta.ID, "api_key")
	}

	transport := cfg.Transport
	if transport == nil {
		transport = NewTransport(TransportConfig{})
	}

	indexer := cfg.Indexer
	if indexer.Slug == "" {
		indexer.Slug = def.Meta.ID
	}
	if indexer.Name == "" {
		indexer.Name = def.Meta.Name
	}

	refresh := defaultRefreshAfter
	if def.Auth != nil && def.Auth.RefreshAfter.Duration() > 0 {
		refresh = def.Auth.RefreshAfter.Duration()
	}

	connector := &DefinitionConnector{
		indexer:   indexer,
		def:       def,
		settings:  settings,
		declared:  declared,
		transport: transport,
		baseURL:   baseURL,
		protocol:  protocolFromDefinition(def),
		session:   newSession(),
		refresh:   refresh,
		appendKey: strategy == "api_key" && !definitionUsesAnySetting(def, "api_key", "apikey"),
	}
	connector.caps = connector.buildCapabilities()
	if def.Download != nil {
		connector.downloadCache = newDownloadCache(defaultDownloadCacheEntries, defaultDownloadCacheTTL)
	}
	connector.secrets = secretSettingValues(def, settings)
	connector.secrets = append(connector.secrets, settings["api_key"], settings["apikey"], settings["token"], settings["password"])
	sort.Slice(connector.secrets, func(i, j int) bool { return len(connector.secrets[i]) > len(connector.secrets[j]) })
	return connector, nil
}

// Capabilities implements Connector.
func (c *DefinitionConnector) Capabilities() Capabilities {
	out := Capabilities{
		Protocol:   c.caps.Protocol,
		Modes:      append([]Mode(nil), c.caps.Modes...),
		Params:     make(map[Mode][]string, len(c.caps.Params)),
		Categories: append([]int(nil), c.caps.Categories...),
	}
	for mode, params := range c.caps.Params {
		out.Params[mode] = append([]string(nil), params...)
	}
	return out
}

// Search implements Connector.
func (c *DefinitionConnector) Search(ctx context.Context, req SearchRequest) ([]Item, error) {
	if !req.Mode.Valid() {
		return nil, fmt.Errorf("definition %q: unsupported search mode %q", c.def.Meta.ID, req.Mode)
	}
	if !c.caps.Supports(req.Mode) {
		return nil, fmt.Errorf("definition %q: search mode %q is not declared", c.def.Meta.ID, req.Mode)
	}
	return c.search(ctx, req, false)
}

// Latest implements Connector. It is a search with empty keywords over the
// requested categories.
func (c *DefinitionConnector) Latest(ctx context.Context, req LatestRequest) ([]Item, error) {
	return c.search(ctx, SearchRequest{
		Mode:       ModeSearch,
		Categories: req.Categories,
		Limit:      req.Limit,
	}, false)
}

// Download implements Connector. Errors never contain the download URL
// because it may embed a passkey.
func (c *DefinitionConnector) Download(ctx context.Context, item Item) (Payload, error) {
	if strings.TrimSpace(item.Magnet) != "" {
		return Payload{Magnet: item.Magnet}, nil
	}
	if strings.TrimSpace(item.DownloadURL) != "" {
		return c.downloadURL(ctx, item.DownloadURL, item.Title)
	}
	if strings.TrimSpace(item.DetailsURL) != "" {
		if c.def.Download != nil {
			return c.downloadFromSection(ctx, item)
		}
		return c.downloadFromDetails(ctx, item)
	}
	return Payload{}, errors.New("download: item has no magnet, download URL or details URL")
}

// Test implements Connector. Every phase is reported, including the ones that
// are deliberately skipped in this wave.
func (c *DefinitionConnector) Test(ctx context.Context) (TestResult, error) {
	result := TestResult{}

	run := func(name string, fn func(context.Context) (bool, string)) bool {
		start := time.Now()
		ok, message := fn(ctx)
		result.Phases = append(result.Phases, TestPhase{
			Name:     name,
			OK:       ok,
			Message:  message,
			Duration: time.Since(start),
		})
		return ok
	}

	var reachOK bool
	if err := ctx.Err(); err != nil {
		return result, err
	}
	reachOK = run("reach", c.testReach)
	if err := ctx.Err(); err != nil {
		return result, err
	}
	_ = run("auth", c.testAuth)
	if err := ctx.Err(); err != nil {
		return result, err
	}
	searchOK := run("search", c.testSearch)
	if err := ctx.Err(); err != nil {
		return result, err
	}
	_ = run("download", func(context.Context) (bool, string) {
		return true, "skipped"
	})

	result.OK = reachOK && searchOK
	for _, phase := range result.Phases {
		if phase.Message == "skipped" {
			continue
		}
		if !phase.OK {
			result.OK = false
		}
	}
	return result, nil
}

func (c *DefinitionConnector) testReach(ctx context.Context) (bool, string) {
	request, err := c.newRequest(ctx, http.MethodGet, c.baseURL, nil)
	if err != nil {
		return false, "build request failed"
	}
	response, err := c.do(ctx, request)
	if err != nil {
		return false, err.Error()
	}
	if response.Status >= 200 && response.Status < 400 {
		return true, fmt.Sprintf("HTTP %d", response.Status)
	}
	return false, fmt.Sprintf("HTTP %d", response.Status)
}

func (c *DefinitionConnector) testAuth(ctx context.Context) (bool, string) {
	if !c.needsLogin() {
		return true, "not required"
	}
	if err := c.forceReLogin(ctx); err != nil {
		return false, err.Error()
	}
	return true, "login ok"
}

func (c *DefinitionConnector) testSearch(ctx context.Context) (bool, string) {
	query := "test"
	minRows := 1
	if c.def.Health != nil {
		if strings.TrimSpace(c.def.Health.Query) != "" {
			query = c.def.Health.Query
		}
		if c.def.Health.MinRows > 0 {
			minRows = c.def.Health.MinRows
		}
	}
	items, err := c.search(ctx, SearchRequest{Mode: ModeSearch, Query: query}, true)
	if err != nil {
		return false, err.Error()
	}
	if len(items) < minRows {
		return false, fmt.Sprintf("%d items, want at least %d", len(items), minRows)
	}
	return true, fmt.Sprintf("%d items", len(items))
}

// search executes the definition request for every page. firstPageOnly is
// used by Test, which must not walk the whole pagination.
func (c *DefinitionConnector) search(ctx context.Context, req SearchRequest, firstPageOnly bool) ([]Item, error) {
	plan, err := c.paginationPlan(req, firstPageOnly)
	if err != nil {
		return nil, err
	}
	if err := c.ensureLogin(ctx); err != nil {
		return nil, err
	}

	var items []Item
	relogged := false

	for pageIndex := 0; pageIndex < plan.maxPages; pageIndex++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		counter := plan.start + pageIndex*plan.step
		pageValue, offsetValue, paramValue := pageIndex+1, req.Offset, pageIndex+1
		switch plan.kind {
		case "page":
			pageValue = counter
			offsetValue = req.Offset
			paramValue = counter
		case "offset":
			pageValue = pageIndex + 1
			offsetValue = counter
			paramValue = counter
		default:
			pageValue = 1
			offsetValue = req.Offset
			paramValue = 1
		}

		build := func() (*http.Request, error) {
			return c.buildSearchRequest(ctx, req, pageValue, offsetValue, paramValue)
		}
		request, err := build()
		if err != nil {
			return nil, err
		}
		response, err := c.do(ctx, request)
		if err != nil {
			return nil, err
		}

		if (response.Status == http.StatusUnauthorized || response.Status == http.StatusForbidden) &&
			c.needsLogin() && !relogged {
			relogged = true
			if err := c.forceReLogin(ctx); err != nil {
				return nil, err
			}
			request, err = build()
			if err != nil {
				return nil, err
			}
			response, err = c.do(ctx, request)
			if err != nil {
				return nil, err
			}
		}

		if response.Status >= 400 {
			return nil, fmt.Errorf("definition %q: search returned HTTP %d", c.def.Meta.ID, response.Status)
		}

		rows, err := c.extractRows(response)
		if err != nil {
			return nil, err
		}
		if len(rows) == 0 {
			break
		}

		for _, raw := range rows {
			item, ok, err := c.normalizeRow(raw, response.URL)
			if err != nil {
				return nil, err
			}
			observeNormalize(ctx, NormalizeObservation{OK: ok})
			if ok {
				items = append(items, item)
			}
		}

		if firstPageOnly {
			break
		}
		if req.Limit > 0 && len(items) >= req.Limit {
			break
		}
	}

	// Details pages are opened only for the rows that survive deduplication
	// and the caller's limit: a search for 10 results must never cost 50
	// details requests.
	items = dedupeItems(items)
	if req.Limit > 0 && len(items) > req.Limit {
		items = items[:req.Limit]
	}
	if !req.SkipDetails {
		if err := c.resolveSearchDetails(ctx, items); err != nil {
			return nil, err
		}
	}
	return items, nil
}

type paginationPlan struct {
	kind     string
	start    int
	step     int
	maxPages int
}

func (c *DefinitionConnector) paginationPlan(req SearchRequest, firstPageOnly bool) (paginationPlan, error) {
	pagination := c.def.Search.Pagination
	kind := strings.TrimSpace(pagination.Type)
	if kind == "" {
		kind = "none"
	}

	maxPages := pagination.MaxPages
	if maxPages <= 0 {
		maxPages = 1
	}
	if firstPageOnly {
		maxPages = 1
	}

	plan := paginationPlan{kind: kind, maxPages: maxPages}
	switch kind {
	case "none":
		plan.start = 1
		plan.step = 1
		plan.maxPages = 1
	case "page":
		plan.start = pagination.Start
		if plan.start == 0 {
			plan.start = 1
		}
		plan.step = pagination.Step
		if plan.step == 0 {
			plan.step = 1
		}
	case "offset":
		plan.start = pagination.Start
		if plan.start == 0 && req.Offset != 0 {
			plan.start = req.Offset
		}
		plan.step = pagination.Step
		if plan.step == 0 {
			plan.step = req.Limit
		}
		if plan.step <= 0 {
			plan.step = 1
		}
	case "cursor":
		return plan, fmt.Errorf("definition %q: cursor pagination is not supported yet", c.def.Meta.ID)
	default:
		return plan, fmt.Errorf("definition %q: unknown pagination type %q", c.def.Meta.ID, kind)
	}

	stopWhen := strings.TrimSpace(pagination.StopWhen)
	if stopWhen != "" && stopWhen != "empty_rows" {
		return plan, fmt.Errorf("definition %q: unknown pagination stop_when %q", c.def.Meta.ID, stopWhen)
	}
	return plan, nil
}

func (c *DefinitionConnector) buildSearchRequest(ctx context.Context, req SearchRequest, pageValue, offsetValue, paramValue int) (*http.Request, error) {
	values := c.templateValues(req, pageValue, offsetValue)

	pathTemplate := c.def.Search.Request.Post
	method := http.MethodPost
	if pathTemplate == "" {
		pathTemplate = c.def.Search.Request.Get
		method = http.MethodGet
	}
	if strings.TrimSpace(pathTemplate) == "" {
		return nil, fmt.Errorf("definition %q: search.request needs get or post", c.def.Meta.ID)
	}

	renderedPath, err := values.render(pathTemplate)
	if err != nil {
		return nil, fmt.Errorf("definition %q: search request: %w", c.def.Meta.ID, err)
	}
	target, err := c.buildURL(renderedPath)
	if err != nil {
		return nil, err
	}

	query := target.Query()
	params, err := values.renderMap(c.def.Search.Request.Params)
	if err != nil {
		return nil, fmt.Errorf("definition %q: search params: %w", c.def.Meta.ID, err)
	}
	for name, value := range params {
		query.Set(name, value)
	}
	if c.appendKey {
		query.Set("apikey", c.settings["api_key"])
	}
	if pagination := c.def.Search.Pagination; strings.TrimSpace(pagination.Param) != "" {
		if !query.Has(pagination.Param) {
			query.Set(pagination.Param, strconv.Itoa(paramValue))
		}
	}
	target.RawQuery = query.Encode()

	var body io.Reader
	var form map[string]string
	if method == http.MethodPost {
		form, err = values.renderMap(c.def.Search.Request.Form)
		if err != nil {
			return nil, fmt.Errorf("definition %q: search form: %w", c.def.Meta.ID, err)
		}
		encoded := url.Values{}
		for name, value := range form {
			encoded.Set(name, value)
		}
		body = strings.NewReader(encoded.Encode())
	}

	request, err := http.NewRequestWithContext(ctx, method, target.String(), body)
	if err != nil {
		return nil, fmt.Errorf("definition %q: build search request", c.def.Meta.ID)
	}
	c.applyAuth(request)
	if method == http.MethodPost && request.Header.Get("Content-Type") == "" {
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	headers, err := values.renderMap(c.def.Search.Request.Headers)
	if err != nil {
		return nil, fmt.Errorf("definition %q: search headers: %w", c.def.Meta.ID, err)
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	return request, nil
}

func (c *DefinitionConnector) extractRows(response *Response) ([]map[string]string, error) {
	base := response.URL
	if base == nil {
		base = c.baseURL
	}
	switch strings.TrimSpace(c.def.Search.Response.Type) {
	case "json":
		return extractJSONRows(response.Body, c.def.Search.Response, base)
	case "xml":
		return extractXMLRows(response.Body, c.def.Search.Response, base)
	default:
		return extractHTMLRows(response.Body, c.def.Search.Response, base)
	}
}

func (c *DefinitionConnector) downloadURL(ctx context.Context, rawURL, title string) (Payload, error) {
	rawURL = strings.TrimSpace(rawURL)
	if strings.HasPrefix(strings.ToLower(rawURL), "magnet:") {
		return Payload{Magnet: rawURL}, nil
	}
	target, err := url.Parse(rawURL)
	if err != nil {
		return Payload{}, errors.New("download: invalid download URL")
	}
	// Build the request through the connector so session cookies, Basic
	// credentials and token headers are applied exactly like a search. The
	// transport error is redacted below: the URL can carry a passkey.
	request, err := c.newRequest(ctx, http.MethodGet, target, nil)
	if err != nil {
		return Payload{}, errors.New("download: build request failed")
	}
	response, err := c.do(ctx, request)
	if err != nil {
		return Payload{}, c.requestError(err)
	}

	if response.Status >= 300 && response.Status < 400 {
		location := strings.TrimSpace(response.Header.Get("Location"))
		if strings.HasPrefix(strings.ToLower(location), "magnet:") {
			return Payload{Magnet: location}, nil
		}
		return Payload{}, errors.New("download: unexpected redirect")
	}
	if response.Status < 200 || response.Status >= 300 {
		return Payload{}, fmt.Errorf("download: HTTP %d", response.Status)
	}

	contentType := strings.TrimSpace(response.Header.Get("Content-Type"))
	if contentType == "" {
		contentType = "application/x-bittorrent"
	}
	return Payload{
		Body:        append([]byte(nil), response.Body...),
		ContentType: contentType,
		Filename:    torrentFilename(title),
	}, nil
}

func (c *DefinitionConnector) downloadFromDetails(ctx context.Context, item Item) (Payload, error) {
	detailsURL, err := url.Parse(item.DetailsURL)
	if err != nil {
		return Payload{}, errors.New("download: invalid details URL")
	}
	request, err := c.newRequest(ctx, http.MethodGet, detailsURL, nil)
	if err != nil {
		return Payload{}, errors.New("download: build details request failed")
	}

	response, err := c.do(ctx, request)
	if err != nil {
		return Payload{}, err
	}
	if response.Status >= 300 && response.Status < 400 {
		location := strings.TrimSpace(response.Header.Get("Location"))
		if strings.HasPrefix(strings.ToLower(location), "magnet:") {
			return Payload{Magnet: location}, nil
		}
		return Payload{}, errors.New("download: unexpected redirect from details page")
	}
	if response.Status < 200 || response.Status >= 400 {
		return Payload{}, fmt.Errorf("download: details returned HTTP %d", response.Status)
	}

	href, err := firstHTMLLink(response.Body, func(href string) bool {
		lower := strings.ToLower(href)
		if strings.HasPrefix(lower, "magnet:") {
			return true
		}
		parsed, err := url.Parse(href)
		path := lower
		if err == nil {
			path = strings.ToLower(parsed.Path)
		}
		return strings.HasSuffix(path, ".torrent") || strings.Contains(path, "/download")
	})
	if err != nil {
		return Payload{}, errors.New("download: details page could not be parsed")
	}
	if href == "" {
		return Payload{}, errors.New("download: no .torrent or /download link on details page")
	}
	if strings.HasPrefix(strings.ToLower(href), "magnet:") {
		return Payload{Magnet: href}, nil
	}

	base := response.URL
	if base == nil {
		base = c.baseURL
	}
	resolved := resolveURL(href, base, c.baseURL)
	if resolved == "" {
		return Payload{}, errors.New("download: invalid download link")
	}
	return c.downloadURL(ctx, resolved, item.Title)
}

func (c *DefinitionConnector) newRequest(ctx context.Context, method string, target *url.URL, body io.Reader) (*http.Request, error) {
	if target == nil {
		target = c.baseURL
	}
	request, err := http.NewRequestWithContext(ctx, method, target.String(), body)
	if err != nil {
		return nil, err
	}
	c.applyAuth(request)
	return request, nil
}

func (c *DefinitionConnector) do(ctx context.Context, request *http.Request) (*Response, error) {
	response, err := c.transport.Do(ctx, c.breakerKey(), request)
	if err != nil {
		return nil, c.requestError(err)
	}
	c.session.Capture(response.URL, response.Header)
	return response, nil
}

// requestError removes any URL from transport errors. A passkey can appear in
// the query string, so only sentinel classes are preserved.
func (c *DefinitionConnector) requestError(err error) error {
	switch {
	case errors.Is(err, context.Canceled):
		return fmt.Errorf("request canceled: %w", context.Canceled)
	case errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("request timed out: %w", context.DeadlineExceeded)
	case errors.Is(err, ErrBreakerOpen):
		return fmt.Errorf("request failed: %w", ErrBreakerOpen)
	case errors.Is(err, ErrRateLimited):
		return fmt.Errorf("request failed: %w", ErrRateLimited)
	case errors.Is(err, ErrBodyTooLarge):
		return fmt.Errorf("request failed: %w", ErrBodyTooLarge)
	case errors.Is(err, ErrBlockedAddress):
		return fmt.Errorf("request failed: %w", ErrBlockedAddress)
	case errors.Is(err, ErrChallenge):
		// The transport message names the host and the status, never the URL,
		// so it is safe to keep and it tells the operator whether FlareSolverr
		// was missing, unreachable or simply did not solve the check.
		return fmt.Errorf("request blocked by a cloudflare challenge: %w", err)
	default:
		return errors.New("request failed")
	}
}

func (c *DefinitionConnector) applyAuth(request *http.Request) {
	if request == nil {
		return
	}
	if cookie := c.session.Cookies(request.URL); cookie != "" && request.Header.Get("Cookie") == "" {
		request.Header.Set("Cookie", cookie)
	}
	// A pasted session cookie wins over the jar: it is the credential the
	// user explicitly supplied in the form (the reCAPTCHA login path).
	if cookie := c.pastedCookie(); cookie != "" && request.Header.Get("Cookie") == "" {
		request.Header.Set("Cookie", cookie)
	}
	switch authStrategy(c.def) {
	case "basic":
		request.SetBasicAuth(c.settings["username"], c.settings["password"])
	case "token":
		if token := c.settings["token"]; token != "" {
			request.Header.Set("Authorization", "Bearer "+token)
		}
	}
}

func (c *DefinitionConnector) templateValues(req SearchRequest, pageValue, offsetValue int) *templateValues {
	values := newTemplateValues()
	values.settings = c.settings
	values.declaredSettings = c.declared
	values.query = queryTemplateMap(req, c.def.Search.KeywordsFormat)
	values.extracted = c.session.Values()
	values.categories = c.siteCategoryIDs(req.Categories)
	values.scalars["page"] = strconv.Itoa(pageValue)
	values.scalars["offset"] = strconv.Itoa(offsetValue)
	values.scalars["limit"] = strconv.Itoa(req.Limit)
	return values
}

func (c *DefinitionConnector) authTemplateValues() *templateValues {
	values := c.templateValues(SearchRequest{}, 1, 0)
	return values
}

func (c *DefinitionConnector) needsLogin() bool {
	if c.pastedCookie() != "" {
		// A cookie the user copied from the browser already is the session:
		// there is no form to submit and no login page to visit.
		return false
	}
	strategy := authStrategy(c.def)
	if strategy == "cookie" {
		// With no steps a cookie strategy means "use the pasted cookie"; only
		// an explicit login flow still needs to run.
		return len(c.def.Auth.Steps) > 0
	}
	return strategy == "form"
}

// pastedCookie returns the session cookie supplied in the settings, if any.
// Browsers copy the whole "Cookie:" request header; the prefix, line breaks
// and surrounding whitespace are removed before the value reaches a request.
func (c *DefinitionConnector) pastedCookie() string {
	raw := strings.TrimSpace(c.settings["cookie"])
	if raw == "" {
		return ""
	}
	raw = strings.TrimSpace(strings.TrimPrefix(raw, "Cookie:"))
	raw = strings.TrimSpace(strings.TrimPrefix(raw, "cookie:"))
	raw = strings.ReplaceAll(raw, "\r", " ")
	raw = strings.ReplaceAll(raw, "\n", " ")
	return strings.TrimSpace(raw)
}

func (c *DefinitionConnector) ensureLogin(ctx context.Context) error {
	if !c.needsLogin() {
		return nil
	}
	c.authMu.Lock()
	defer c.authMu.Unlock()
	if c.session.loginValid(c.now(), c.refresh) {
		return nil
	}
	return c.loginLocked(ctx)
}

func (c *DefinitionConnector) forceReLogin(ctx context.Context) error {
	if !c.needsLogin() {
		return nil
	}
	c.authMu.Lock()
	defer c.authMu.Unlock()
	c.session.invalidate()
	return c.loginLocked(ctx)
}

func (c *DefinitionConnector) loginLocked(ctx context.Context) error {
	auth := c.def.Auth
	if auth == nil || len(auth.Steps) == 0 {
		return c.loginError("no auth steps defined")
	}

	var last *Response
	for index, step := range auth.Steps {
		if err := ctx.Err(); err != nil {
			return err
		}
		values := c.authTemplateValues()

		pathTemplate := step.Post
		method := http.MethodPost
		if pathTemplate == "" {
			pathTemplate = step.Get
			method = http.MethodGet
		}
		if strings.TrimSpace(pathTemplate) == "" {
			return c.loginError(fmt.Sprintf("auth step %d has no get or post", index+1))
		}

		renderedPath, err := values.render(pathTemplate)
		if err != nil {
			return c.loginError(fmt.Sprintf("auth step %d: %v", index+1, err))
		}
		target, err := c.buildURL(renderedPath)
		if err != nil {
			return c.loginError(fmt.Sprintf("auth step %d: invalid URL", index+1))
		}

		var body io.Reader
		if method == http.MethodPost {
			form, err := values.renderMap(step.Form)
			if err != nil {
				return c.loginError(fmt.Sprintf("auth step %d: %v", index+1, err))
			}
			encoded := url.Values{}
			for name, value := range form {
				encoded.Set(name, value)
			}
			body = strings.NewReader(encoded.Encode())
		}

		request, err := http.NewRequestWithContext(ctx, method, target.String(), body)
		if err != nil {
			return c.loginError(fmt.Sprintf("auth step %d: build request failed", index+1))
		}
		c.applyAuth(request)
		if method == http.MethodPost && request.Header.Get("Content-Type") == "" {
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		headers, err := values.renderMap(step.Headers)
		if err != nil {
			return c.loginError(fmt.Sprintf("auth step %d: %v", index+1, err))
		}
		for name, value := range headers {
			request.Header.Set(name, value)
		}

		response, err := c.do(ctx, request)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return err
			}
			return c.loginError(err.Error())
		}
		last = response

		for name, extractor := range step.Extract {
			value, err := extractAuthValue(response.Body, extractor)
			if err != nil {
				return c.loginError(fmt.Sprintf("extract %q: %v", name, err))
			}
			c.session.SetValue(name, value)
		}
	}
	if last == nil {
		return c.loginError("no response")
	}

	for _, condition := range auth.Failure {
		matched, err := conditionMatches(condition, last)
		if err != nil {
			return c.loginError(err.Error())
		}
		if matched {
			return c.loginError(c.conditionMessage(condition, last))
		}
	}

	success := last.Status >= 200 && last.Status < 400
	if auth.Success != nil {
		matched, err := conditionMatches(*auth.Success, last)
		if err != nil {
			return c.loginError(err.Error())
		}
		success = matched
	}
	if !success {
		return c.loginError(fmt.Sprintf("HTTP %d", last.Status))
	}

	c.session.markLoggedIn(c.now())
	return nil
}

func conditionMatches(condition definition.Condition, response *Response) (bool, error) {
	if condition.Selector == "" && condition.Status == 0 {
		return false, errors.New("condition needs selector or status")
	}
	matched := true
	if condition.Selector != "" {
		present, err := htmlSelectorPresent(response.Body, condition.Selector)
		if err != nil {
			return false, err
		}
		matched = present
	}
	if condition.Status != 0 {
		matched = matched && response.Status == condition.Status
	}
	return matched, nil
}

func (c *DefinitionConnector) conditionMessage(condition definition.Condition, response *Response) string {
	message := strings.TrimSpace(condition.Message)
	if message == "" {
		return ""
	}
	if strings.EqualFold(message, "text") && condition.Selector != "" {
		value, err := extractHTMLValue(response.Body, condition.Selector, "text")
		if err == nil && strings.TrimSpace(value) != "" {
			return value
		}
		return ""
	}
	if strings.Contains(message, "{{") {
		if rendered, err := c.authTemplateValues().render(message); err == nil {
			return rendered
		}
	}
	return message
}

func extractAuthValue(body []byte, extractor definition.Extractor) (string, error) {
	switch {
	case extractor.Selector != "":
		return extractHTMLValue(body, extractor.Selector, extractor.Attr)
	case extractor.JSON != "":
		root, err := decodeJSON(body)
		if err != nil {
			return "", err
		}
		return jsonValueString(walkJSONPath(root, extractor.JSON)), nil
	case extractor.Regex != "":
		compiled, err := regexp.Compile(extractor.Regex)
		if err != nil {
			return "", err
		}
		match := compiled.FindStringSubmatch(string(body))
		if len(match) < 2 {
			return "", nil
		}
		return match[1], nil
	default:
		return "", nil
	}
}

func (c *DefinitionConnector) loginError(message string) error {
	message = strings.TrimSpace(c.redact(message))
	if message == "" {
		return errors.New("login failed")
	}
	return fmt.Errorf("login failed: %s", message)
}

func (c *DefinitionConnector) redact(message string) string {
	for _, secret := range c.secrets {
		if secret != "" {
			message = strings.ReplaceAll(message, secret, "[redacted]")
		}
	}
	return message
}

func (c *DefinitionConnector) buildURL(rendered string) (*url.URL, error) {
	rendered = strings.TrimSpace(rendered)
	parsed, err := url.Parse(rendered)
	if err != nil {
		return nil, fmt.Errorf("definition %q: invalid request URL", c.def.Meta.ID)
	}
	if parsed.IsAbs() {
		if parsed.Scheme != "http" && parsed.Scheme != "https" {
			return nil, fmt.Errorf("definition %q: unsupported URL scheme", c.def.Meta.ID)
		}
		return parsed, nil
	}
	if parsed.Host != "" {
		return &url.URL{
			Scheme:   c.baseURL.Scheme,
			Host:     parsed.Host,
			Path:     parsed.Path,
			RawQuery: parsed.RawQuery,
			Fragment: parsed.Fragment,
		}, nil
	}

	target := *c.baseURL
	target.Path = joinURLPath(c.baseURL.Path, parsed.Path)
	target.RawPath = ""
	target.RawQuery = parsed.RawQuery
	target.Fragment = parsed.Fragment
	return &target, nil
}

func joinURLPath(basePath, relative string) string {
	if relative == "" {
		if basePath == "" {
			return "/"
		}
		return basePath
	}
	if !strings.HasPrefix(relative, "/") {
		relative = "/" + relative
	}
	prefix := strings.TrimSuffix(basePath, "/")
	return prefix + relative
}

func (c *DefinitionConnector) buildCapabilities() Capabilities {
	capabilities := Capabilities{
		Protocol: c.protocol,
		Params:   make(map[Mode][]string),
	}
	add := func(mode Mode, params []string) {
		if len(params) == 0 {
			return
		}
		capabilities.Modes = append(capabilities.Modes, mode)
		capabilities.Params[mode] = append([]string(nil), params...)
	}
	add(ModeSearch, c.def.Capabilities.Search)
	add(ModeTV, c.def.Capabilities.TVSearch)
	add(ModeMovie, c.def.Capabilities.MovieSearch)
	add(ModeMusic, c.def.Capabilities.MusicSearch)
	add(ModeBook, c.def.Capabilities.BookSearch)

	seen := make(map[int]struct{}, len(c.def.Categories))
	for _, category := range c.def.Categories {
		if _, exists := seen[category.Standard]; exists {
			continue
		}
		seen[category.Standard] = struct{}{}
		capabilities.Categories = append(capabilities.Categories, category.Standard)
	}
	return capabilities
}

func (c *DefinitionConnector) breakerKey() string {
	if c.indexer.Slug != "" {
		return c.indexer.Slug
	}
	return c.def.Meta.ID
}

func (c *DefinitionConnector) now() time.Time {
	if c.transport != nil && c.transport.clock != nil {
		return c.transport.clock.Now()
	}
	return time.Now()
}

func protocolFromDefinition(def *definition.Definition) Protocol {
	if strings.EqualFold(strings.TrimSpace(def.Meta.Protocol), "usenet") {
		return ProtocolUsenet
	}
	return ProtocolTorrent
}

func authStrategy(def *definition.Definition) string {
	if def == nil || def.Auth == nil {
		return "none"
	}
	strategy := strings.TrimSpace(strings.ToLower(def.Auth.Strategy))
	if strategy == "" {
		return "none"
	}
	return strategy
}

func definitionUsesAnySetting(def *definition.Definition, names ...string) bool {
	needles := make([]string, 0, len(names))
	for _, name := range names {
		needles = append(needles, "settings."+name)
	}
	contains := func(value string) bool {
		for _, needle := range needles {
			if strings.Contains(value, needle) {
				return true
			}
		}
		return false
	}
	containsMap := func(values map[string]string) bool {
		for _, value := range values {
			if contains(value) {
				return true
			}
		}
		return false
	}

	request := def.Search.Request
	if contains(request.Get) || contains(request.Post) || containsMap(request.Params) ||
		containsMap(request.Form) || containsMap(request.Headers) {
		return true
	}
	if def.Auth != nil {
		for _, step := range def.Auth.Steps {
			if contains(step.Get) || contains(step.Post) || containsMap(step.Form) || containsMap(step.Headers) {
				return true
			}
		}
	}
	return false
}

func secretSettingValues(def *definition.Definition, settings map[string]string) []string {
	var secrets []string
	for _, setting := range def.Settings {
		if !isSecretSetting(setting) {
			continue
		}
		if value := settings[setting.Name]; value != "" {
			secrets = append(secrets, value)
		}
	}
	sort.Slice(secrets, func(i, j int) bool { return len(secrets[i]) > len(secrets[j]) })
	return secrets
}

func isSecretSetting(setting definition.Setting) bool {
	if strings.EqualFold(setting.Type, "password") {
		return true
	}
	name := strings.ToLower(setting.Name)
	for _, marker := range []string{"password", "passwd", "pass", "token", "api_key", "apikey", "cookie", "secret", "passkey"} {
		if strings.Contains(name, marker) {
			return true
		}
	}
	return false
}

func torrentFilename(title string) string {
	name := strings.TrimSpace(title)
	var builder strings.Builder
	for _, character := range name {
		switch {
		case character < 0x20:
			continue
		case strings.ContainsRune(`/\:*?"<>|`, character):
			builder.WriteRune('_')
		default:
			builder.WriteRune(character)
		}
	}
	name = strings.TrimSpace(builder.String())
	if name == "" {
		name = "download"
	}
	lower := strings.ToLower(name)
	if strings.HasSuffix(lower, ".torrent") || strings.HasSuffix(lower, ".nzb") {
		return name
	}
	return name + ".torrent"
}
