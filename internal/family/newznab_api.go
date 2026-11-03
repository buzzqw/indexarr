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
)

// newznabCapsTTL is how long a caps document stays cached.
const newznabCapsTTL = time.Hour

// newznabAPI is the part of the Newznab protocol that Newznab (Usenet) and
// Torznab (torrent) connectors share: configuration, caps cache, the search
// request, the HTTP call and the connection test. Each connector embeds it
// and adds what differs: how a row becomes an item and how a release is
// downloaded.
type newznabAPI struct {
	// name prefixes errors ("torznab: …") and is the breaker key when the
	// indexer has no slug.
	name     string
	protocol engine.Protocol

	cfg     NewznabConfig
	base    *url.URL
	baseURL string
	breaker string

	capsMu sync.Mutex
	caps   *Caps
	capsAt time.Time
}

func newNewznabAPI(name string, protocol engine.Protocol, cfg NewznabConfig) (*newznabAPI, error) {
	base, err := parseBaseURL(cfg.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	cfg.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	cfg.APIKey = strings.TrimSpace(cfg.APIKey)
	if cfg.Transport == nil {
		cfg.Transport = engine.NewTransport(engine.TransportConfig{UserAgent: cfg.UserAgent})
	}
	cfg.Indexer.Protocol = protocol
	cfg.Categories = append([]int(nil), cfg.Categories...)

	breaker := strings.TrimSpace(cfg.Indexer.Slug)
	if breaker == "" {
		breaker = name
	}
	return &newznabAPI{
		name:     name,
		protocol: protocol,
		cfg:      cfg,
		base:     base,
		baseURL:  cfg.BaseURL,
		breaker:  breaker,
	}, nil
}

// Capabilities implements engine.Connector. Until a caps document has been
// fetched it advertises every search mode with its usual parameters.
func (a *newznabAPI) Capabilities() engine.Capabilities {
	a.capsMu.Lock()
	cached := a.caps.clone()
	a.capsMu.Unlock()

	caps := engine.Capabilities{
		Protocol: a.protocol,
		Modes: []engine.Mode{
			engine.ModeSearch,
			engine.ModeTV,
			engine.ModeMovie,
			engine.ModeMusic,
			engine.ModeBook,
		},
		Params:     defaultNewznabParams(),
		Categories: append([]int(nil), a.cfg.Categories...),
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

// Caps fetches and parses the caps document. Results are cached for one hour;
// any failure invalidates the cache so the next call retries.
func (a *newznabAPI) Caps(ctx context.Context) (*Caps, error) {
	a.capsMu.Lock()
	defer a.capsMu.Unlock()

	now := time.Now()
	if a.caps != nil && now.Sub(a.capsAt) < newznabCapsTTL {
		return a.caps.clone(), nil
	}
	a.caps = nil

	values := url.Values{}
	values.Set("t", "caps")
	if a.cfg.APIKey != "" {
		values.Set("apikey", a.cfg.APIKey)
	}
	resp, err := a.get(ctx, values, a.name+": caps request")
	if err != nil {
		a.capsAt = time.Time{}
		return nil, err
	}
	if resp.Status < 200 || resp.Status >= 300 {
		a.capsAt = time.Time{}
		return nil, fmt.Errorf("%s: caps failed with HTTP status %d", a.name, resp.Status)
	}
	parsed, err := ParseCaps(resp.Body)
	if err != nil {
		a.capsAt = time.Time{}
		return nil, fmt.Errorf("%s: parse caps: %s", a.name, redact(err.Error(), a.cfg.APIKey))
	}
	a.caps = parsed
	a.capsAt = now
	return parsed.clone(), nil
}

func (a *newznabAPI) resetCaps() {
	a.capsMu.Lock()
	a.caps = nil
	a.capsAt = time.Time{}
	a.capsMu.Unlock()
}

// search sends one t=<mode> request and turns every row into an item with
// parseItem; rows parseItem rejects are skipped.
func (a *newznabAPI) search(ctx context.Context, req engine.SearchRequest, parseItem func(newznabRSSItem) (engine.Item, bool)) ([]engine.Item, error) {
	mode := req.Mode
	if mode == "" {
		mode = engine.ModeSearch
	}
	if !mode.Valid() {
		return nil, fmt.Errorf("%s: unsupported search mode %q", a.name, mode)
	}

	resp, err := a.get(ctx, a.searchValues(mode, req), a.name+": search request")
	if err != nil {
		return nil, err
	}
	if resp.Status < 200 || resp.Status >= 300 {
		return nil, fmt.Errorf("%s: search failed with HTTP status %d", a.name, resp.Status)
	}
	rows, err := parseNewznabRSS(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("%s: search: %s", a.name, redact(err.Error(), a.cfg.APIKey))
	}
	items := make([]engine.Item, 0, len(rows))
	for _, row := range rows {
		if item, ok := parseItem(row); ok {
			items = append(items, item)
		}
	}
	return items, nil
}

// searchValues builds the query of a search: every non-empty field of the
// request, the configured categories when the request names none, and the
// API key.
func (a *newznabAPI) searchValues(mode engine.Mode, req engine.SearchRequest) url.Values {
	categories := req.Categories
	if len(categories) == 0 {
		categories = a.cfg.Categories
	}

	values := url.Values{}
	values.Set("t", string(mode))
	setTrimmed := func(key, value string) {
		if value = strings.TrimSpace(value); value != "" {
			values.Set(key, value)
		}
	}
	setPositive := func(key string, value int) {
		if value > 0 {
			values.Set(key, strconv.Itoa(value))
		}
	}
	setTrimmed("q", req.Query)
	setPositive("season", req.Season)
	setPositive("ep", req.Episode)
	setTrimmed("imdbid", req.IMDbID)
	setTrimmed("tvdbid", req.TVDbID)
	setTrimmed("tmdbid", req.TMDbID)
	setTrimmed("rid", req.RID)
	setTrimmed("artist", req.Artist)
	setTrimmed("album", req.Album)
	setTrimmed("author", req.Author)
	setTrimmed("title", req.Title)
	setTrimmed("cat", joinCategories(categories))
	setPositive("limit", req.Limit)
	setPositive("offset", req.Offset)
	values.Set("extended", "1")
	setTrimmed("apikey", a.cfg.APIKey)
	return values
}

// test runs the three phases of a connection test: the endpoint answers, the
// caps document parses, and a one-row search succeeds (proving the API key).
// Download is skipped because it would consume a real release.
func (a *newznabAPI) test(ctx context.Context, search func(context.Context, engine.SearchRequest) ([]engine.Item, error)) (engine.TestResult, error) {
	result := engine.TestResult{}

	addPhase(&result, "reach", func() error {
		values := url.Values{}
		values.Set("t", "caps")
		resp, err := a.get(ctx, values, a.name+": reach request")
		if err != nil {
			return err
		}
		if !reachableStatus(resp.Status) {
			return fmt.Errorf("unexpected HTTP status %d", resp.Status)
		}
		return nil
	})
	addPhase(&result, "caps", func() error {
		a.resetCaps()
		_, err := a.Caps(ctx)
		return err
	})
	addPhase(&result, "search", func() error {
		_, err := search(ctx, engine.SearchRequest{
			Mode:  engine.ModeSearch,
			Query: "test",
			Limit: 1,
		})
		return err
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

func (a *newznabAPI) get(ctx context.Context, values url.Values, operation string) (*engine.Response, error) {
	target, err := withQuery(a.baseURL, values)
	if err != nil {
		return nil, fmt.Errorf("%s: build request: %w", operation, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, fmt.Errorf("%s: build request: %s", operation, redact(err.Error(), a.cfg.APIKey))
	}
	req.Header.Set("Accept", "application/xml, application/rss+xml;q=0.9, */*;q=0.8")
	if a.cfg.UserAgent != "" {
		req.Header.Set("User-Agent", a.cfg.UserAgent)
	}
	resp, err := a.cfg.Transport.Do(ctx, a.breaker, req)
	if err != nil {
		return nil, redactError(operation, err, a.cfg.APIKey)
	}
	return resp, nil
}
