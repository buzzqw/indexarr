package family

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/buzzqw/indexarr/internal/engine"
)

const (
	newznabBreakerFallback = "newznab"
	newznabCapsTTL         = time.Hour
)

// NewznabConfig configures a generic Newznab API client. BaseURL is the API
// endpoint, for example https://indexer.example/api.
type NewznabConfig struct {
	Indexer    engine.IndexerRef
	BaseURL    string
	APIKey     string
	Transport  *engine.Transport
	Categories []int
	UserAgent  string
}

// Newznab is the generic Newznab connector used by Usenet indexers and by
// presets that expose a Newznab-compatible API.
type Newznab struct {
	cfg     NewznabConfig
	base    *url.URL
	baseURL string
	breaker string

	capsMu sync.Mutex
	caps   *Caps
	capsAt time.Time
}

// CapsCategory is one Newznab category and, at most, one level of children.
type CapsCategory struct {
	ID            int
	Name          string
	Subcategories []CapsCategory
}

// CapsLimits mirrors the Newznab <limits> element.
type CapsLimits struct {
	Max     int
	Default int
}

// Caps is the parsed form of a Newznab caps document.
type Caps struct {
	Modes      []engine.Mode
	Params     map[engine.Mode][]string
	Categories []CapsCategory
	Limits     CapsLimits
}

// NewNewznab validates cfg and builds a connector.
func NewNewznab(cfg NewznabConfig) (*Newznab, error) {
	base, err := parseBaseURL(cfg.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("newznab: %w", err)
	}
	cfg.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	cfg.APIKey = strings.TrimSpace(cfg.APIKey)
	if cfg.Transport == nil {
		cfg.Transport = engine.NewTransport(engine.TransportConfig{UserAgent: cfg.UserAgent})
	}
	cfg.Indexer.Protocol = engine.ProtocolUsenet
	cfg.Categories = append([]int(nil), cfg.Categories...)

	breaker := strings.TrimSpace(cfg.Indexer.Slug)
	if breaker == "" {
		breaker = newznabBreakerFallback
	}

	return &Newznab{
		cfg:     cfg,
		base:    base,
		baseURL: cfg.BaseURL,
		breaker: breaker,
	}, nil
}

// Capabilities implements engine.Connector. Until a caps document has been
// fetched it advertises every Newznab search mode with its usual parameters.
func (n *Newznab) Capabilities() engine.Capabilities {
	n.capsMu.Lock()
	cached := n.caps.clone()
	n.capsMu.Unlock()

	caps := engine.Capabilities{
		Protocol: engine.ProtocolUsenet,
		Modes: []engine.Mode{
			engine.ModeSearch,
			engine.ModeTV,
			engine.ModeMovie,
			engine.ModeMusic,
			engine.ModeBook,
		},
		Params:     defaultNewznabParams(),
		Categories: append([]int(nil), n.cfg.Categories...),
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

// Caps fetches and parses the Newznab caps document. Results are cached for
// one hour; any failure invalidates the cache so the next call retries.
func (n *Newznab) Caps(ctx context.Context) (*Caps, error) {
	n.capsMu.Lock()
	defer n.capsMu.Unlock()

	now := time.Now()
	if n.caps != nil && now.Sub(n.capsAt) < newznabCapsTTL {
		return n.caps.clone(), nil
	}
	n.caps = nil

	values := url.Values{}
	values.Set("t", "caps")
	if n.cfg.APIKey != "" {
		values.Set("apikey", n.cfg.APIKey)
	}
	resp, err := n.get(ctx, values, "newznab: caps request")
	if err != nil {
		n.capsAt = time.Time{}
		return nil, err
	}
	if resp.Status < 200 || resp.Status >= 300 {
		n.capsAt = time.Time{}
		return nil, fmt.Errorf("newznab: caps failed with HTTP status %d", resp.Status)
	}
	parsed, err := ParseCaps(resp.Body)
	if err != nil {
		n.capsAt = time.Time{}
		return nil, fmt.Errorf("newznab: parse caps: %s", redact(err.Error(), n.cfg.APIKey))
	}
	n.caps = parsed
	n.capsAt = now
	return parsed.clone(), nil
}

// Search implements engine.Connector.
func (n *Newznab) Search(ctx context.Context, req engine.SearchRequest) ([]engine.Item, error) {
	mode := req.Mode
	if mode == "" {
		mode = engine.ModeSearch
	}
	if !mode.Valid() {
		return nil, fmt.Errorf("newznab: unsupported search mode %q", mode)
	}

	categories := req.Categories
	if len(categories) == 0 {
		categories = n.cfg.Categories
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
	if n.cfg.APIKey != "" {
		values.Set("apikey", n.cfg.APIKey)
	}

	resp, err := n.get(ctx, values, "newznab: search request")
	if err != nil {
		return nil, err
	}
	if resp.Status < 200 || resp.Status >= 300 {
		return nil, fmt.Errorf("newznab: search failed with HTTP status %d", resp.Status)
	}
	items, err := n.parseItems(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("newznab: search: %s", redact(err.Error(), n.cfg.APIKey))
	}
	return items, nil
}

// Latest implements engine.Connector by searching within the requested
// categories with no keyword, which is the conventional Newznab latest feed.
func (n *Newznab) Latest(ctx context.Context, req engine.LatestRequest) ([]engine.Item, error) {
	return n.Search(ctx, engine.SearchRequest{
		Mode:       engine.ModeSearch,
		Categories: req.Categories,
		Limit:      req.Limit,
	})
}

// Download implements engine.Connector.
func (n *Newznab) Download(ctx context.Context, item engine.Item) (engine.Payload, error) {
	id := strings.TrimSpace(item.GUID)
	if id == "" {
		id = strings.TrimSpace(item.Attributes["guid"])
	}

	var resp *engine.Response
	var err error
	if id == "" {
		target := strings.TrimSpace(item.DownloadURL)
		if target == "" || !strings.HasPrefix(strings.ToLower(target), "http") {
			return engine.Payload{}, fmt.Errorf("newznab: release id is missing")
		}
		resp, err = n.cfg.Transport.Download(ctx, n.breaker, resolveURL(n.base, target), 0)
	} else {
		values := url.Values{}
		values.Set("t", "get")
		values.Set("id", id)
		if n.cfg.APIKey != "" {
			values.Set("apikey", n.cfg.APIKey)
		}
		target, buildErr := withQuery(n.baseURL, values)
		if buildErr != nil {
			return engine.Payload{}, fmt.Errorf("newznab: build download request: %w", buildErr)
		}
		resp, err = n.cfg.Transport.Download(ctx, n.breaker, target, 0)
	}
	if err != nil {
		return engine.Payload{}, redactError("newznab: download", err, n.cfg.APIKey)
	}
	if resp.Status < 200 || resp.Status >= 300 {
		return engine.Payload{}, fmt.Errorf("newznab: download failed with HTTP status %d", resp.Status)
	}
	return engine.Payload{
		Body:        resp.Body,
		ContentType: "application/x-nzb",
		Filename:    safeFilename(item.Title, ".nzb"),
	}, nil
}

// Test implements engine.Connector. Download is skipped because it would
// consume a real release.
func (n *Newznab) Test(ctx context.Context) (engine.TestResult, error) {
	result := engine.TestResult{}

	reach := func() error {
		values := url.Values{}
		values.Set("t", "caps")
		resp, err := n.get(ctx, values, "newznab: reach request")
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
		n.resetCaps()
		_, err := n.Caps(ctx)
		return err
	}
	addPhase(&result, "caps", caps)

	search := func() error {
		_, err := n.Search(ctx, engine.SearchRequest{
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

func (n *Newznab) resetCaps() {
	n.capsMu.Lock()
	n.caps = nil
	n.capsAt = time.Time{}
	n.capsMu.Unlock()
}

func (n *Newznab) get(ctx context.Context, values url.Values, operation string) (*engine.Response, error) {
	target, err := withQuery(n.baseURL, values)
	if err != nil {
		return nil, fmt.Errorf("%s: build request: %w", operation, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, fmt.Errorf("%s: build request: %s", operation, redact(err.Error(), n.cfg.APIKey))
	}
	req.Header.Set("Accept", "application/xml, application/rss+xml;q=0.9, */*;q=0.8")
	if n.cfg.UserAgent != "" {
		req.Header.Set("User-Agent", n.cfg.UserAgent)
	}
	resp, err := n.cfg.Transport.Do(ctx, n.breaker, req)
	if err != nil {
		return nil, redactError(operation, err, n.cfg.APIKey)
	}
	return resp, nil
}

func (n *Newznab) parseItems(body []byte) ([]engine.Item, error) {
	rows, err := parseNewznabRSS(body)
	if err != nil {
		return nil, err
	}
	items := make([]engine.Item, 0, len(rows))
	for _, row := range rows {
		if item, ok := n.parseItem(row); ok {
			items = append(items, item)
		}
	}
	return items, nil
}

func (n *Newznab) parseItem(row newznabRSSItem) (engine.Item, bool) {
	title := strings.TrimSpace(row.Title)
	if title == "" {
		return engine.Item{}, false
	}
	guid := strings.TrimSpace(row.GUID)
	link := strings.TrimSpace(row.Link)
	enclosureURL := strings.TrimSpace(row.Enclosure.URL)
	if guid == "" && link == "" && enclosureURL == "" {
		return engine.Item{}, false
	}

	attributes := make(map[string]string, len(row.Attrs)+3)
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
		addCategory(category)
	}
	if guid != "" {
		attributes["guid"] = guid
	}
	if link != "" {
		attributes["link"] = link
	}
	if comments := strings.TrimSpace(row.Comments); comments != "" {
		attributes["comments"] = comments
	}

	size := parseInt64(attributes["size"])
	if size == 0 {
		size = parseInt64(row.Enclosure.Length)
	}

	details := strings.TrimSpace(row.Comments)
	if details == "" {
		details = link
	}
	downloadURL := ""
	if enclosureURL != "" {
		downloadURL = resolveURL(n.base, enclosureURL)
	}

	return engine.Item{
		Protocol:    engine.ProtocolUsenet,
		Title:       title,
		GUID:        guid,
		DetailsURL:  details,
		DownloadURL: downloadURL,
		InfoHash:    "",
		Size:        size,
		Seeders:     parseInt64(attributes["seeders"]),
		Leechers:    parseInt64(attributes["leechers"]),
		Categories:  categories,
		Published:   parseDate(row.PubDate),
		IMDbID:      cleanAttrID(attributes["imdbid"]),
		TMDbID:      cleanAttrID(attributes["tmdbid"]),
		TVDbID:      cleanAttrID(attributes["tvdbid"]),
		Attributes:  attributes,
	}, true
}

// ParseCaps parses a Newznab <caps> document. It is exported so presets and
// tests can validate a caps fixture without a live server.
func ParseCaps(body []byte) (*Caps, error) {
	body = bytes.TrimSpace(bytes.TrimPrefix(body, []byte{0xef, 0xbb, 0xbf}))
	if len(body) == 0 {
		return nil, fmt.Errorf("empty caps document")
	}
	var doc capsDocument
	if err := xml.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("invalid caps XML")
	}

	parsed := &Caps{
		Modes:  make([]engine.Mode, 0, 5),
		Params: make(map[engine.Mode][]string),
	}
	type modeSpec struct {
		mode engine.Mode
		doc  *capsSearchXML
	}
	specs := []modeSpec{
		{engine.ModeSearch, doc.Searching.Search},
		{engine.ModeTV, doc.Searching.TV},
		{engine.ModeMovie, doc.Searching.Movie},
		{engine.ModeMusic, doc.Searching.Music},
		{engine.ModeBook, doc.Searching.Book},
	}
	for _, spec := range specs {
		if spec.doc == nil || capsDisabled(spec.doc.Available) {
			continue
		}
		parsed.Modes = append(parsed.Modes, spec.mode)
		parsed.Params[spec.mode] = parseSupportedParams(spec.doc.SupportedParams)
	}

	parsed.Limits = CapsLimits{
		Max:     parseInt(doc.Limits.Max),
		Default: parseInt(doc.Limits.Default),
	}
	for _, category := range doc.Categories.Category {
		id := parseInt(category.ID)
		if id <= 0 {
			continue
		}
		entry := CapsCategory{ID: id, Name: strings.TrimSpace(category.Name)}
		for _, sub := range category.Subcategories {
			subID := parseInt(sub.ID)
			if subID <= 0 {
				continue
			}
			entry.Subcategories = append(entry.Subcategories, CapsCategory{
				ID:   subID,
				Name: strings.TrimSpace(sub.Name),
			})
		}
		parsed.Categories = append(parsed.Categories, entry)
	}
	return parsed, nil
}

func (c *Caps) clone() *Caps {
	if c == nil {
		return nil
	}
	out := &Caps{
		Modes:  append([]engine.Mode{}, c.Modes...),
		Params: make(map[engine.Mode][]string, len(c.Params)),
		Limits: c.Limits,
	}
	for mode, params := range c.Params {
		out.Params[mode] = append([]string(nil), params...)
	}
	out.Categories = cloneCapsCategories(c.Categories)
	return out
}

func cloneCapsCategories(in []CapsCategory) []CapsCategory {
	if in == nil {
		return nil
	}
	out := make([]CapsCategory, len(in))
	for i, category := range in {
		out[i] = CapsCategory{ID: category.ID, Name: category.Name}
		if category.Subcategories != nil {
			out[i].Subcategories = cloneCapsCategories(category.Subcategories)
		}
	}
	return out
}

func flattenCapsCategories(categories []CapsCategory) []int {
	ids := make([]int, 0, len(categories))
	seen := make(map[int]struct{}, len(categories))
	add := func(id int) {
		if id <= 0 {
			return
		}
		if _, exists := seen[id]; exists {
			return
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	for _, category := range categories {
		add(category.ID)
		for _, sub := range category.Subcategories {
			add(sub.ID)
		}
	}
	return ids
}

func defaultNewznabParams() map[engine.Mode][]string {
	return map[engine.Mode][]string{
		engine.ModeSearch: {engine.ParamQuery},
		engine.ModeTV: {
			engine.ParamQuery,
			engine.ParamSeason,
			engine.ParamEpisode,
			engine.ParamIMDbID,
			engine.ParamTVDbID,
		},
		engine.ModeMovie: {
			engine.ParamQuery,
			engine.ParamIMDbID,
			engine.ParamTMDbID,
		},
		engine.ModeMusic: {
			engine.ParamQuery,
			engine.ParamArtist,
			engine.ParamAlbum,
			engine.ParamRID,
		},
		engine.ModeBook: {
			engine.ParamQuery,
			engine.ParamAuthor,
			engine.ParamTitle,
		},
	}
}

func parseSupportedParams(value string) []string {
	if strings.TrimSpace(value) == "" {
		return []string{}
	}
	params := make([]string, 0, 8)
	seen := make(map[string]struct{}, 8)
	for _, raw := range strings.Split(value, ",") {
		param := normalizeParam(raw)
		if param == "" || !engine.ValidParam(param) {
			continue
		}
		if _, exists := seen[param]; exists {
			continue
		}
		seen[param] = struct{}{}
		params = append(params, param)
	}
	return params
}

func normalizeParam(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "q", "query":
		return engine.ParamQuery
	case "season":
		return engine.ParamSeason
	case "ep", "episode":
		return engine.ParamEpisode
	case "imdbid", "imdb":
		return engine.ParamIMDbID
	case "tmdbid", "tmdb":
		return engine.ParamTMDbID
	case "tvdbid", "tvdb":
		return engine.ParamTVDbID
	case "tvmazeid":
		return engine.ParamTVMazeID
	case "rid":
		return engine.ParamRID
	case "artist":
		return engine.ParamArtist
	case "album":
		return engine.ParamAlbum
	case "author":
		return engine.ParamAuthor
	case "title":
		return engine.ParamTitle
	case "year":
		return engine.ParamYear
	case "genre":
		return engine.ParamGenre
	default:
		return ""
	}
}

func capsDisabled(available string) bool {
	switch strings.ToLower(strings.TrimSpace(available)) {
	case "no", "false", "0", "off":
		return true
	default:
		return false
	}
}

func joinCategories(categories []int) string {
	if len(categories) == 0 {
		return ""
	}
	values := make([]string, 0, len(categories))
	for _, category := range categories {
		if category > 0 {
			values = append(values, strconv.Itoa(category))
		}
	}
	return strings.Join(values, ",")
}

func cleanAttrID(value string) string {
	value = strings.TrimSpace(value)
	if value == "0" {
		return ""
	}
	return value
}

func parseInt64(value string) int64 {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if n, err := strconv.ParseInt(value, 10, 64); err == nil {
		return n
	}
	if f, err := strconv.ParseFloat(value, 64); err == nil {
		return int64(f)
	}
	return 0
}

func parseInt(value string) int {
	return int(parseInt64(value))
}

type capsDocument struct {
	XMLName   xml.Name `xml:"caps"`
	Searching struct {
		Search *capsSearchXML `xml:"search"`
		TV     *capsSearchXML `xml:"tv-search"`
		Movie  *capsSearchXML `xml:"movie-search"`
		Music  *capsSearchXML `xml:"music-search"`
		Book   *capsSearchXML `xml:"book-search"`
	} `xml:"searching"`
	Limits struct {
		Max     string `xml:"max,attr"`
		Default string `xml:"default,attr"`
	} `xml:"limits"`
	Categories struct {
		Category []capsCategoryXML `xml:"category"`
	} `xml:"categories"`
}

type capsSearchXML struct {
	Available       string `xml:"available,attr"`
	SupportedParams string `xml:"supportedParams,attr"`
}

type capsCategoryXML struct {
	ID            string            `xml:"id,attr"`
	Name          string            `xml:"name,attr"`
	Subcategories []capsCategoryXML `xml:"subcat"`
}

type newznabRSSDocument struct {
	XMLName xml.Name        `xml:"rss"`
	Channel *newznabChannel `xml:"channel"`
}

type newznabChannel struct {
	Items []newznabRSSItem `xml:"item"`
}

type newznabRSSItem struct {
	Title     string           `xml:"title"`
	GUID      string           `xml:"guid"`
	Link      string           `xml:"link"`
	Comments  string           `xml:"comments"`
	PubDate   string           `xml:"pubDate"`
	Category  string           `xml:"category"`
	Enclosure newznabEnclosure `xml:"enclosure"`
	Attrs     []newznabAttr    `xml:"attr"`
}

type newznabEnclosure struct {
	URL    string `xml:"url,attr"`
	Length string `xml:"length,attr"`
	Type   string `xml:"type,attr"`
}

type newznabAttr struct {
	Name  string `xml:"name,attr"`
	Value string `xml:"value,attr"`
}

type newznabErrorDoc struct {
	Code        string `xml:"code,attr"`
	Description string `xml:"description,attr"`
}

func parseNewznabRSS(body []byte) ([]newznabRSSItem, error) {
	body = bytes.TrimSpace(bytes.TrimPrefix(body, []byte{0xef, 0xbb, 0xbf}))
	if len(body) == 0 {
		return nil, fmt.Errorf("empty response body")
	}
	var root struct {
		XMLName xml.Name
	}
	if err := xml.Unmarshal(body, &root); err != nil {
		return nil, fmt.Errorf("invalid XML response")
	}
	if strings.EqualFold(root.XMLName.Local, "error") {
		var apiError newznabErrorDoc
		if err := xml.Unmarshal(body, &apiError); err != nil {
			return nil, fmt.Errorf("invalid XML error response")
		}
		description := strings.TrimSpace(apiError.Description)
		if description == "" {
			description = "request rejected"
		}
		code := strings.TrimSpace(apiError.Code)
		if code == "" {
			code = "unknown"
		}
		return nil, fmt.Errorf("API error %s: %s", code, description)
	}

	var doc newznabRSSDocument
	if err := xml.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("invalid RSS response")
	}
	if doc.Channel == nil {
		return nil, fmt.Errorf("response is not an RSS feed")
	}
	return doc.Channel.Items, nil
}
