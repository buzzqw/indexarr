package engine

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
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
	"sync/atomic"
	"syscall"
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
	// SessionEndpoint is the FlareSolverr URL injected by the caller (the
	// global network setting). It is used when the definition's session
	// section has no endpoint or names a setting that is empty.
	SessionEndpoint string
	// SessionTimeout is the global FlareSolverr timeout. Zero keeps the
	// definition/default value.
	SessionTimeout time.Duration
	// Sessions is the process-wide registry of live browser sessions. It lets
	// orphan cleanup leave the sessions of other connectors alone. Nil is
	// allowed: the connector then owns its session without cross-checks.
	Sessions *SessionRegistry
	// DeferredSearch, when set, receives one event for every search page the
	// background worker sends for a deferred search. The Manager uses it to
	// write the indexer_history row that a queued search would otherwise
	// never show: the client only sees ErrSearchDeferred, and the outcome of
	// the real send (accepted, refused, dropped, latency) stays invisible.
	DeferredSearch DeferredSearchObserver
	// DeferredSearchAllowed is checked before the background worker spends a
	// flood turn. It lets the Manager keep the indexer's daily query limit
	// valid for pages that were queued when the limit still had room: a nil
	// callback means no extra check. The queue is dropped when it returns an
	// error, so a queued page cannot bypass the limit.
	DeferredSearchAllowed func(ctx context.Context) error
}

var (
	_ Connector        = (*DefinitionConnector)(nil)
	_ SessionConnector = (*DefinitionConnector)(nil)
)

// DefinitionConnector executes one native definition. It owns an in-memory
// session (cookies plus login-extracted values) and is safe for concurrent use.
type DefinitionConnector struct {
	indexer   IndexerRef
	def       *definition.Definition
	settings  map[string]string
	declared  map[string]struct{}
	transport *Transport
	baseURL   *url.URL
	// mirrors are meta.urls in order (mirrors[0] is baseURL); mirror is the
	// one in use. A domain that stops resolving moves the connector to the
	// next address instead of failing every run.
	mirrors   []*url.URL
	mirror    atomic.Int32
	protocol  Protocol
	caps      Capabilities
	session   *Session
	authMu    sync.Mutex
	refresh   time.Duration
	appendKey bool
	secrets   []string

	downloadCache   *downloadCache
	resolveFailures atomic.Int64
	flare           *flareSessionState
	indexMu         sync.Mutex
	indexCache      map[string]indexCacheEntry
	searchMu        sync.Mutex
	searchCache     map[string]searchCacheEntry
	// Flood interval (search.interval): the next free search slot and the
	// searches deferred to the background worker.
	floodMu      sync.Mutex
	floodLane    chan struct{}
	floodWaiting int
	floodBusy    bool
	searchLast   time.Time
	// searchRefusals counts the refusals in a row and searchBlockedUntil is
	// the end of the pause they started: no search is sent before it.
	searchRefusals     int
	searchBlockedUntil time.Time
	searchQueue        []deferredSearch
	searchDraining     bool
	// deferredSearch reports the background sends, so a queued search leaves
	// a row in the history too.
	deferredSearch        DeferredSearchObserver
	deferredSearchAllowed func(context.Context) error
	// queueCtx stops the background queue when the manager drops the
	// connector (indexer deleted, disabled or definition changed).
	queueCtx    context.Context
	queueCancel context.CancelFunc
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
	// A definition may ask to skip certificate verification when its public
	// site serves an incomplete chain. The flag is per definition: the shared
	// transport keeps its policy and this connector gets a derivative that only
	// changes the TLS check, reusing the same politeness and SSRF guard.
	if def.Meta.InsecureTLS {
		if !strings.EqualFold(strings.TrimSpace(def.Meta.Type), "public") {
			return nil, fmt.Errorf("definition %q: meta.insecure_tls is only allowed for a public definition", def.Meta.ID)
		}
		if !transport.Config().InsecureTLS {
			derived := transport.Config()
			derived.InsecureTLS = true
			transport = NewTransport(derived)
		}
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

	queueCtx, queueCancel := context.WithCancel(context.Background())
	connector := &DefinitionConnector{
		indexer:               indexer,
		def:                   def,
		settings:              settings,
		declared:              declared,
		transport:             transport,
		baseURL:               baseURL,
		mirrors:               definitionMirrors(baseURL, def.Meta.URLs[1:]),
		protocol:              protocolFromDefinition(def),
		session:               newSession(),
		refresh:               refresh,
		appendKey:             strategy == "api_key" && !definitionUsesAnySetting(def, "api_key", "apikey"),
		deferredSearch:        cfg.DeferredSearch,
		deferredSearchAllowed: cfg.DeferredSearchAllowed,
		queueCtx:              queueCtx,
		queueCancel:           queueCancel,
	}
	connector.caps = connector.buildCapabilities()
	if def.Search.Index != nil {
		connector.indexCache = make(map[string]indexCacheEntry)
	}
	if def.Download != nil {
		connector.downloadCache = newDownloadCache(defaultDownloadCacheEntries, defaultDownloadCacheTTL)
	}
	connector.secrets = secretSettingValues(def, settings)
	connector.secrets = append(connector.secrets, settings["api_key"], settings["apikey"], settings["token"], settings["password"])
	sort.Slice(connector.secrets, func(i, j int) bool { return len(connector.secrets[i]) > len(connector.secrets[j]) })
	if def.Session != nil {
		if err := connector.configureSession(cfg); err != nil {
			return nil, err
		}
	}
	return connector, nil
}

// Capabilities implements Connector.
func (c *DefinitionConnector) Capabilities() Capabilities {
	out := Capabilities{
		Protocol:   c.caps.Protocol,
		Modes:      append([]Mode(nil), c.caps.Modes...),
		Params:     make(map[Mode][]string, len(c.caps.Params)),
		Categories: append([]int(nil), c.caps.Categories...),
		LocalOnly:  c.caps.LocalOnly,
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
	if c.def.Search.Index != nil {
		return c.searchIndex(ctx, req, req.FirstPageOnly)
	}
	return c.search(ctx, req, req.FirstPageOnly)
}

// Latest implements Connector. A definition with a latest section reads its
// real list of new releases, one page per site category; every other
// definition keeps the historical empty-keyword search.
func (c *DefinitionConnector) Latest(ctx context.Context, req LatestRequest) ([]Item, error) {
	if c.def.Latest == nil {
		return c.search(ctx, SearchRequest{
			Mode:        ModeSearch,
			Categories:  req.Categories,
			Limit:       req.Limit,
			Offset:      req.Offset,
			SkipDetails: req.SkipDetails,
		}, false)
	}
	return c.latestList(ctx, req)
}

// latestList reads the latest section. Each configured category mapping is one
// request: this is what lets the run follow the categories the linked apps
// actually consume (TV for Sonarr, movies for Radarr) instead of every
// category of the site. A definition without mappings reads one global list.
func (c *DefinitionConnector) latestList(ctx context.Context, req LatestRequest) ([]Item, error) {
	if err := c.ensureLogin(ctx); err != nil {
		return nil, err
	}
	latest := c.def.Latest
	targets := latestCategoryTargets(latest.Categories, req.Categories)
	if len(targets) == 0 {
		return nil, nil
	}
	var items []Item
	seen := make(map[string]struct{}, req.Limit)
	for _, target := range targets {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		categoryItems, err := c.latestCategory(ctx, latest, target, req, seen)
		if err != nil {
			return nil, err
		}
		items = append(items, categoryItems...)
		if req.Limit > 0 && len(items) >= req.Limit {
			break
		}
	}
	items = dedupeItems(items)
	if req.Limit > 0 && len(items) > req.Limit {
		items = items[:req.Limit]
	}
	if !req.SkipDetails {
		var err error
		items, err = c.resolveSearchDetails(ctx, items)
		if err != nil {
			return nil, err
		}
	}
	return items, nil
}

// latestCategory reads the pages of one site category. It stops on an empty
// page, at the configured max_pages and as soon as the caller's limit is met,
// so a periodic read costs the fewest possible requests.
func (c *DefinitionConnector) latestCategory(ctx context.Context, latest *definition.Latest, target string, req LatestRequest, seen map[string]struct{}) ([]Item, error) {
	plan, err := c.paginationPlanFor(latest.Pagination, SearchRequest{Offset: req.Offset, Limit: req.Limit}, false)
	if err != nil {
		return nil, fmt.Errorf("definition %q: latest pagination: %w", c.def.Meta.ID, err)
	}
	spec := c.def.Search.Response
	if latest.Response != nil {
		spec = *latest.Response
	}
	known := req.Known
	if !latest.StopsWhenKnown() {
		known = nil
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
			pageValue, paramValue = counter, counter
		case "offset":
			pageValue = pageIndex + 1
			offsetValue, paramValue = counter, counter
		default:
			pageValue, offsetValue, paramValue = 1, req.Offset, 1
		}
		build := func() (*http.Request, error) {
			return c.buildLatestRequest(ctx, latest, target, req, pageValue, offsetValue, paramValue)
		}
		request, err := build()
		if err != nil {
			return nil, err
		}
		response, err := c.doFeed(ctx, request)
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
			response, err = c.doFeed(ctx, request)
			if err != nil {
				return nil, err
			}
		}
		if response.Status >= 400 {
			return nil, fmt.Errorf("definition %q: latest returned HTTP %d", c.def.Meta.ID, response.Status)
		}
		rows, err := c.extractRowsWith(response, spec)
		if err != nil {
			return nil, err
		}
		if len(rows) == 0 {
			break
		}
		added := 0
		newRows := 0
		datedRows := 0
		oldRows := 0
		for _, raw := range rows {
			item, ok, err := c.normalizeRowWith(raw, response.URL, spec)
			if err != nil {
				return nil, err
			}
			observeNormalize(ctx, NormalizeObservation{OK: ok})
			if !ok {
				continue
			}
			key := item.InfoHash + "\x00" + item.Title + "\x00" + strconv.FormatInt(item.Size, 10)
			if _, duplicate := seen[key]; duplicate {
				continue
			}
			seen[key] = struct{}{}
			if known == nil || !known(item) {
				newRows++
			}
			if !item.Published.IsZero() {
				datedRows++
				if req.MaxAge > 0 && item.Published.Before(time.Now().Add(-req.MaxAge)) {
					oldRows++
				}
			}
			items = append(items, item)
			added++
			if req.Limit > 0 && len(items) >= req.Limit {
				break
			}
		}
		if added == 0 {
			break
		}
		// A page made entirely of releases already in the cache marks the end
		// of the new part of the list. The run stops there instead of walking
		// every configured page on every cycle.
		if known != nil && newRows == 0 {
			break
		}
		if req.MaxAge > 0 && datedRows > 0 && oldRows == datedRows {
			break
		}
		if req.Limit > 0 && len(items) >= req.Limit {
			break
		}
	}
	return items, nil
}

// latestCategoryTargets intersects the definition's category mappings with the
// categories requested by the caller. An empty mapping list is one global
// request; an empty request means every configured mapping.
func latestCategoryTargets(mappings []definition.LatestCategory, requested []int) []string {
	if len(mappings) == 0 {
		return []string{""}
	}
	out := make([]string, 0, len(mappings))
	for _, mapping := range mappings {
		if len(requested) == 0 || categoryAllows(requested, mapping.Standard) {
			out = append(out, mapping.Site)
		}
	}
	return out
}

func categoryAllows(allowed []int, category int) bool {
	for _, candidate := range allowed {
		if candidate == category {
			return true
		}
		if candidate > 0 && candidate%1000 == 0 && category/1000 == candidate/1000 {
			return true
		}
	}
	return false
}

// buildLatestRequest renders one latest page. It mirrors buildSearchRequest but
// reads latest.request and exposes {{ category }}.
func (c *DefinitionConnector) buildLatestRequest(ctx context.Context, latest *definition.Latest, category string, req LatestRequest, pageValue, offsetValue, paramValue int) (*http.Request, error) {
	values := c.templateValues(SearchRequest{
		Mode:       ModeSearch,
		Categories: req.Categories,
		Limit:      req.Limit,
	}, pageValue, offsetValue)
	if category != "" {
		values.scalars["category"] = category
		values.categories = []string{category}
	}

	pathTemplate := latest.Request.Post
	method := http.MethodPost
	if pathTemplate == "" {
		pathTemplate = latest.Request.Get
		method = http.MethodGet
	}
	if strings.TrimSpace(pathTemplate) == "" {
		return nil, fmt.Errorf("definition %q: latest.request needs get or post", c.def.Meta.ID)
	}
	renderedPath, err := values.render(pathTemplate)
	if err != nil {
		return nil, fmt.Errorf("definition %q: latest request: %w", c.def.Meta.ID, err)
	}
	target, err := c.buildURL(renderedPath)
	if err != nil {
		return nil, err
	}
	query := target.Query()
	params, err := values.renderMap(latest.Request.Params)
	if err != nil {
		return nil, fmt.Errorf("definition %q: latest params: %w", c.def.Meta.ID, err)
	}
	for name, value := range params {
		query.Set(name, value)
	}
	multi, err := values.renderMapList(latest.Request.ParamsMulti)
	if err != nil {
		return nil, fmt.Errorf("definition %q: search params: %w", c.def.Meta.ID, err)
	}
	for name, list := range multi {
		for _, value := range list {
			value = strings.TrimSpace(value)
			if value != "" && !strings.HasSuffix(value, "=") {
				query.Add(name, value)
			}
		}
	}
	if c.appendKey {
		query.Set("apikey", c.settings["api_key"])
	}
	if pagination := latest.Pagination; strings.TrimSpace(pagination.Param) != "" {
		if !query.Has(pagination.Param) {
			query.Set(pagination.Param, strconv.Itoa(paramValue))
		}
	}
	target.RawQuery = query.Encode()

	var body io.Reader
	if method == http.MethodPost {
		form, err := values.renderMap(latest.Request.Form)
		if err != nil {
			return nil, fmt.Errorf("definition %q: latest form: %w", c.def.Meta.ID, err)
		}
		encoded := url.Values{}
		for name, value := range form {
			encoded.Set(name, value)
		}
		body = strings.NewReader(encoded.Encode())
	}
	request, err := http.NewRequestWithContext(ctx, method, target.String(), body)
	if err != nil {
		return nil, fmt.Errorf("definition %q: build latest request", c.def.Meta.ID)
	}
	c.applyAuth(request)
	if method == http.MethodPost && request.Header.Get("Content-Type") == "" {
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	headers, err := values.renderMap(latest.Request.Headers)
	if err != nil {
		return nil, fmt.Errorf("definition %q: latest headers: %w", c.def.Meta.ID, err)
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	return request, nil
}

// Download implements Connector. Errors never contain the download URL
// because it may embed a passkey.
func (c *DefinitionConnector) Download(ctx context.Context, item Item) (Payload, error) {
	if strings.TrimSpace(item.Magnet) != "" {
		return Payload{Magnet: item.Magnet}, nil
	}
	// An expanded eD2k row already carries the exact file the client chose;
	// resolving the topic again would return the first link instead.
	if link := strings.TrimSpace(item.Ed2k); link != "" {
		return Payload{Ed2k: link}, nil
	}
	// A definition that resolves the download at grab time declares where the
	// link must be read. The item can still carry a DownloadURL that came from
	// somewhere else (an import from another manager, a stale cache, a bridge
	// on the local network): using it would bypass the definition and, in the
	// import case, hit a private address the SSRF guard rightly refuses.
	// Honor the declared source instead.
	declaredGrab := c.def.Download != nil && !c.resolvesOnSearch() && strings.TrimSpace(item.DetailsURL) != ""
	if declaredGrab {
		if c.def.Download.Before != nil {
			payload, handled, err := c.downloadWithBefore(ctx, item)
			if handled {
				return payload, err
			}
		}
		return c.downloadFromSection(ctx, item)
	}
	if strings.TrimSpace(item.DownloadURL) != "" {
		return c.downloadURL(ctx, item.DownloadURL, item.Title, resolveURL(item.DetailsURL, c.baseURL))
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
	var items []Item
	var err error
	if c.def.Search.Index != nil {
		items, err = c.searchIndex(ctx, SearchRequest{Mode: ModeSearch, Query: query}, true)
	} else {
		items, err = c.search(ctx, SearchRequest{Mode: ModeSearch, Query: query}, true)
	}
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
	if err := c.runSearchPreflight(ctx, req); err != nil {
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
		cacheKey := c.searchCacheKey(request)
		response, cached := c.cachedSearchPage(cacheKey)
		if !cached {
			release, err := c.takeSearchTurn(ctx, pageIndex == 0)
			if err != nil {
				later := func(ctx context.Context) (*http.Request, error) {
					return c.buildSearchRequest(ctx, req, pageValue, offsetValue, paramValue)
				}
				if errors.Is(err, errSearchLater) {
					// The first pages already answered: keep them, and fetch
					// this one in the background for the next request.
					c.deferSearch(cacheKey, req.Query, later)
					break
				}
				if errors.Is(err, ErrSearchDeferred) {
					queued := c.deferSearch(cacheKey, req.Query, later)
					if wait := c.searchBlockedFor(); wait > 0 {
						return nil, blockedSearchError(c.def.Meta.ID, wait, queued)
					}
					return nil, deferredSearchError(c.def.Meta.ID, c.searchInterval(), queued)
				}
				return nil, err
			}
			response, err = c.fetchSearchPage(ctx, request, build, &relogged)
			release()
			if errors.Is(err, errSearchRefused) && len(items) > 0 {
				// The first pages already answered: keep them.
				break
			}
			if err != nil {
				return nil, err
			}
			c.storeSearchPage(cacheKey, response)
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
				annotateQuery(&item, req)
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
	if !req.SkipDetails && c.expandsTopicFiles() {
		items = dropTopicsOfAnotherSeason(items, req)
	}
	if req.Limit > 0 && len(items) > req.Limit {
		items = items[:req.Limit]
	}
	if !req.SkipDetails {
		var err error
		items, err = c.resolveSearchDetails(ctx, items)
		if err != nil {
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
	return c.paginationPlanFor(c.def.Search.Pagination, req, firstPageOnly)
}

func (c *DefinitionConnector) paginationPlanFor(pagination definition.Pagination, req SearchRequest, firstPageOnly bool) (paginationPlan, error) {
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
	multi, err := values.renderMapList(c.def.Search.Request.ParamsMulti)
	if err != nil {
		return nil, fmt.Errorf("definition %q: search params: %w", c.def.Meta.ID, err)
	}
	for name, list := range multi {
		for _, value := range list {
			value = strings.TrimSpace(value)
			if value != "" && !strings.HasSuffix(value, "=") {
				query.Add(name, value)
			}
		}
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
	return c.extractRowsWith(response, c.def.Search.Response)
}

func (c *DefinitionConnector) extractRowsWith(response *Response, spec definition.Response) ([]map[string]string, error) {
	base := response.URL
	if base == nil {
		base = c.baseURL
	}
	switch strings.TrimSpace(spec.Type) {
	case "json":
		return extractJSONRows(response.Body, spec, base)
	case "xml":
		return extractXMLRows(response.Body, spec, base)
	default:
		return extractHTMLRows(htmlBody(response), spec, base)
	}
}

// downloadURL fetches a .torrent. referer is the release page the link came
// from: some sites (DLE engines among them) serve the file only when the
// request carries it, as a browser click would. It is sent only to the same
// host, so a details URL never leaks to another site.
func (c *DefinitionConnector) downloadURL(ctx context.Context, rawURL, title, referer string) (Payload, error) {
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
	if ref, err := url.Parse(strings.TrimSpace(referer)); err == nil && referer != "" && strings.EqualFold(ref.Hostname(), target.Hostname()) {
		request.Header.Set("Referer", ref.String())
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
	return c.downloadURL(ctx, resolved, item.Title, item.DetailsURL)
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
	return c.doWithFallback(ctx, request, false)
}

// doFeed uses the feed-friendly transport path (IPv4 fallback on timeout) for
// latest-list requests. Live searches keep the plain path so a slow host
// cannot consume the whole fan-out budget.
func (c *DefinitionConnector) doFeed(ctx context.Context, request *http.Request) (*Response, error) {
	return c.doWithFallback(ctx, request, true)
}

func (c *DefinitionConnector) doWithFallback(ctx context.Context, request *http.Request, feed bool) (*Response, error) {
	if c.flare != nil {
		return c.doSession(ctx, request)
	}
	send := func(request *http.Request) (*Response, error) {
		if feed {
			return c.transport.DoFeed(ctx, c.breakerKey(), request)
		}
		return c.transport.Do(ctx, c.breakerKey(), request)
	}
	current := int(c.mirror.Load())
	response, err := send(c.onMirror(request, current))
	// The other addresses are tried in a circle, so a connector that moved
	// to a mirror goes back to the first address when the mirror dies too.
	for step := 1; step < len(c.mirrors) && mirrorDown(response, err); step++ {
		next := (current + step) % len(c.mirrors)
		retry := c.onMirror(request, next)
		if retry == nil {
			break
		}
		if again, againErr := send(retry); !mirrorDown(again, againErr) {
			response, err = again, againErr
			c.mirror.CompareAndSwap(int32(current), int32(next))
		}
	}
	if err != nil {
		return nil, c.requestError(err)
	}
	c.session.Capture(response.URL, response.Header)
	return response, nil
}

// definitionMirrors parses the definition's addresses after the first one.
// An address without scheme or host is skipped.
func definitionMirrors(base *url.URL, others []string) []*url.URL {
	mirrors := []*url.URL{base}
	for _, raw := range others {
		parsed, err := url.Parse(strings.TrimSpace(raw))
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			continue
		}
		mirrors = append(mirrors, parsed)
	}
	return mirrors
}

// onMirror returns request sent to mirrors[index]. Only a request for one of
// the definition's own hosts moves; a request elsewhere (a tracker, a CDN)
// stays as it is. It returns nil when the body cannot be sent twice.
func (c *DefinitionConnector) onMirror(request *http.Request, index int) *http.Request {
	if index <= 0 || index >= len(c.mirrors) || !c.ownHost(request.URL.Host) {
		return request
	}
	target := c.mirrors[index]
	if request.URL.Host == target.Host && request.URL.Scheme == target.Scheme {
		return request
	}
	moved := request.Clone(request.Context())
	if request.Body != nil && request.Body != http.NoBody {
		if request.GetBody == nil {
			return nil
		}
		body, err := request.GetBody()
		if err != nil {
			return nil
		}
		moved.Body = body
	}
	moved.URL.Scheme = target.Scheme
	moved.URL.Host = target.Host
	moved.Host = target.Host
	return moved
}

// ownHost reports whether host is one of the definition's addresses.
func (c *DefinitionConnector) ownHost(host string) bool {
	for _, mirror := range c.mirrors {
		if strings.EqualFold(mirror.Host, host) {
			return true
		}
	}
	return false
}

// mirrorDown reports an answer that says the address itself is gone: a
// network failure from mirrorFailover, or a Cloudflare page saying the origin
// behind it is down or unreachable (502, 504, 52x, 530). A 503 is left alone:
// Cloudflare uses it for its challenge too.
func mirrorDown(response *Response, err error) bool {
	if err != nil {
		return mirrorFailover(err)
	}
	if response == nil {
		return false
	}
	switch response.Status {
	case http.StatusBadGateway, http.StatusGatewayTimeout, 520, 521, 522, 523, 524, 525, 526, 530:
		return true
	}
	return false
}

// mirrorFailover reports a failure that says the address itself is gone (the
// name does not resolve, nothing listens, no route, a certificate for another
// site), as opposed to a slow or refusing site, which a mirror would not fix.
func mirrorFailover(err error) bool {
	switch networkFailure(err) {
	case "no such host", "name resolution failed", "connection refused", "no route to host", "tls certificate not valid":
		return true
	}
	return false
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
		if kind := networkFailure(err); kind != "" {
			return errors.New("request failed: " + kind)
		}
		return errors.New("request failed")
	}
}

// networkFailure names the kind of a transport failure without the host or
// the URL, which can carry a passkey: "request failed" alone left the logs and
// the history without a cause, while a dead domain (no such host) and a
// refused port need different actions.
func networkFailure(err error) string {
	var dnsErr *net.DNSError
	var certErr *tls.CertificateVerificationError
	var unknownAuthority x509.UnknownAuthorityError
	var hostnameErr x509.HostnameError
	var invalidCert x509.CertificateInvalidError
	switch {
	case errors.As(err, &dnsErr):
		if dnsErr.IsNotFound {
			return "no such host"
		}
		return "name resolution failed"
	case errors.As(err, &certErr), errors.As(err, &unknownAuthority),
		errors.As(err, &hostnameErr), errors.As(err, &invalidCert):
		return "tls certificate not valid"
	case errors.Is(err, syscall.ECONNREFUSED):
		return "connection refused"
	case errors.Is(err, syscall.ECONNRESET), errors.Is(err, syscall.EPIPE):
		return "connection reset"
	case errors.Is(err, syscall.ENETUNREACH), errors.Is(err, syscall.EHOSTUNREACH):
		return "no route to host"
	case errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, io.EOF):
		return "connection closed by the site (unexpected eof)"
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "timed out"
	}
	return ""
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

// reLoginAfter logs in again unless another request already did it after
// stamp (the login time seen before the failed page). Two topic pages read in
// parallel that both find the session gone cost one login, not two.
func (c *DefinitionConnector) reLoginAfter(ctx context.Context, stamp time.Time) error {
	if !c.needsLogin() {
		return nil
	}
	c.authMu.Lock()
	defer c.authMu.Unlock()
	if current := c.session.loginStamp(); !current.IsZero() && current.After(stamp) {
		return nil
	}
	c.session.invalidate()
	return c.loginLocked(ctx)
}

// sessionLost reports whether an authenticated page came back without the
// definition's auth.logged_in marker.
func (c *DefinitionConnector) sessionLost(response *Response) bool {
	if c.def.Auth == nil || c.def.Auth.LoggedIn == nil || !c.needsLogin() || response == nil {
		return false
	}
	present, err := conditionMatches(*c.def.Auth.LoggedIn, response)
	return err == nil && !present
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
		if present && strings.TrimSpace(condition.Contains) != "" {
			text, err := extractHTMLValue(htmlBody(response), condition.Selector, "text")
			if err != nil {
				return false, err
			}
			matched = strings.Contains(strings.ToLower(text), strings.ToLower(strings.TrimSpace(condition.Contains)))
		}
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
		value, err := extractHTMLValue(htmlBody(response), condition.Selector, "text")
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
	joined := prefix + relative
	if joined == "" {
		return "/"
	}
	cleaned := path.Clean(joined)
	if strings.HasSuffix(relative, "/") && !strings.HasSuffix(cleaned, "/") {
		cleaned += "/"
	}
	return cleaned
}

func (c *DefinitionConnector) buildCapabilities() Capabilities {
	capabilities := Capabilities{
		Protocol:  c.protocol,
		Params:    make(map[Mode][]string),
		LocalOnly: !c.def.SearchUsesQuery(),
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
	switch strings.ToLower(strings.TrimSpace(def.Meta.Protocol)) {
	case "usenet":
		return ProtocolUsenet
	case "ed2k":
		return ProtocolEd2k
	default:
		return ProtocolTorrent
	}
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

// fetchSearchPage reads one search page. It logs in again once when the site
// answers as a guest or with 401/403, and turns the site's refusal page
// (search.blocked) into an error instead of an empty result.
func (c *DefinitionConnector) fetchSearchPage(ctx context.Context, request *http.Request, build func() (*http.Request, error), relogged *bool) (*Response, error) {
	stamp := c.session.loginStamp()
	response, err := c.do(ctx, request)
	if err != nil {
		return nil, err
	}

	// A forum that keeps one session per account answers the search as a
	// guest once the account logged in elsewhere: 200, no rows, and a
	// "not permitted to search" page instead of the results. The missing
	// auth.logged_in marker tells it apart from a real empty search.
	if response.Status < 400 && !*relogged && c.sessionLost(response) {
		*relogged = true
		if err := c.reLoginAfter(ctx, stamp); err != nil {
			return nil, err
		}
		if request, err = build(); err != nil {
			return nil, err
		}
		if response, err = c.do(ctx, request); err != nil {
			return nil, err
		}
	}

	if (response.Status == http.StatusUnauthorized || response.Status == http.StatusForbidden) &&
		c.needsLogin() && !*relogged {
		*relogged = true
		if err := c.forceReLogin(ctx); err != nil {
			return nil, err
		}
		if request, err = build(); err != nil {
			return nil, err
		}
		if response, err = c.do(ctx, request); err != nil {
			return nil, err
		}
	}

	if response.Status >= 400 {
		return nil, fmt.Errorf("definition %q: search returned HTTP %d", c.def.Meta.ID, response.Status)
	}
	if blocked := c.def.Search.Blocked; blocked != nil {
		if present, err := conditionMatches(*blocked, response); err == nil && present {
			message := c.conditionMessage(*blocked, response)
			if message == "" {
				message = "the site refused the search"
			}
			c.noteSearchRefused()
			return nil, fmt.Errorf("definition %q: %w: %s", c.def.Meta.ID, errSearchRefused, strings.Join(strings.Fields(message), " "))
		}
		c.noteSearchAccepted()
	}
	return response, nil
}

// expandsTopicFiles reports whether the search opens every topic and turns its
// files into releases (download.expand: file).
func (c *DefinitionConnector) expandsTopicFiles() bool {
	return c.def.Download != nil && strings.EqualFold(strings.TrimSpace(c.def.Download.Expand), "file")
}

// dropTopicsOfAnotherSeason keeps, for a search with a season, only the topics
// that name that season or none. The site search lists every season of a
// series ("New Tricks - Stagione 7" first): opening the others costs a page
// each and can never match, and the wanted season could fall past the limit.
func dropTopicsOfAnotherSeason(items []Item, req SearchRequest) []Item {
	season := req.Season
	if season <= 0 {
		season, _ = indexQuerySeasonEpisode(req.Query)
	}
	if season <= 0 {
		return items
	}
	kept := items[:0]
	for _, item := range items {
		if named, ok := indexTitleSeasonNumber(strings.ToLower(item.Title)); ok && named != season {
			continue
		}
		kept = append(kept, item)
	}
	return kept
}
