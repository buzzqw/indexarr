package family

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/buzzqw/indexarr/internal/engine"
)

const gazelleBreakerFallback = "gazelle"

// GazelleConfig configures one Gazelle (or Gazelle-derived) tracker.
//
// APIKey is either an API token, an authkey:torrent_pass pair or a session
// cookie. CategoryMap optionally overrides the site category id to standard
// (Torznab) category mapping; the defaults cover the common Gazelle categories.
type GazelleConfig struct {
	Indexer      engine.IndexerRef
	BaseURL      string
	APIKey       string // API token or authkey:torrent_pass pair
	Transport    *engine.Transport
	Categories   []int
	UserAgent    string
	BrowsePath   string // default "/ajax.php"
	DownloadPath string // default "/torrents.php"
	CategoryMap  map[int]int
}

var _ engine.Connector = (*Gazelle)(nil)

// Gazelle is the native connector shared by every Gazelle tracker.
type Gazelle struct {
	cfg          GazelleConfig
	base         *url.URL
	breaker      string
	browsePath   string
	downloadPath string
	token        string
	authKey      string
	torrentPass  string
	cookieAuth   bool
	categoryMap  map[int]int
}

// NewGazelle validates cfg and builds a connector.
func NewGazelle(cfg GazelleConfig) (*Gazelle, error) {
	base, err := parseBaseURL(cfg.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("gazelle: %w", err)
	}
	cfg.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	cfg.APIKey = strings.TrimSpace(cfg.APIKey)
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("gazelle: API key is required")
	}
	if cfg.Transport == nil {
		cfg.Transport = engine.NewTransport(engine.TransportConfig{UserAgent: cfg.UserAgent})
	}
	cfg.Indexer.Protocol = engine.ProtocolTorrent
	cfg.Categories = append([]int(nil), cfg.Categories...)

	// Relative links must resolve against the tracker root even when BaseURL
	// carries a sub-path such as https://host/gazelle.
	if !strings.HasSuffix(base.Path, "/") {
		base.Path += "/"
	}

	authKey, torrentPass := gazelleSplitCredentials(cfg.APIKey)
	token := cfg.APIKey
	if authKey != "" {
		token = authKey
	}
	// A session cookie always contains "name=value"; a bare API token does not.
	cookieAuth := authKey == "" && strings.Contains(cfg.APIKey, "=")

	breaker := strings.TrimSpace(cfg.Indexer.Slug)
	if breaker == "" {
		breaker = gazelleBreakerFallback
	}

	categoryMap := gazelleDefaultCategoryMap()
	for site, standard := range cfg.CategoryMap {
		categoryMap[site] = standard
	}

	return &Gazelle{
		cfg:          cfg,
		base:         base,
		breaker:      breaker,
		browsePath:   gazellePath(cfg.BrowsePath, "/ajax.php"),
		downloadPath: gazellePath(cfg.DownloadPath, "/torrents.php"),
		token:        token,
		authKey:      authKey,
		torrentPass:  torrentPass,
		cookieAuth:   cookieAuth,
		categoryMap:  categoryMap,
	}, nil
}

// Capabilities implements engine.Connector.
func (g *Gazelle) Capabilities() engine.Capabilities {
	return engine.Capabilities{
		Protocol: engine.ProtocolTorrent,
		Modes: []engine.Mode{
			engine.ModeSearch,
			engine.ModeTV,
			engine.ModeMovie,
			engine.ModeMusic,
			engine.ModeBook,
		},
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
			engine.ModeMusic: {
				engine.ParamQuery,
				engine.ParamArtist,
				engine.ParamAlbum,
			},
			engine.ModeBook: {
				engine.ParamQuery,
				engine.ParamAuthor,
				engine.ParamTitle,
			},
		},
		Categories: append([]int(nil), g.cfg.Categories...),
	}
}

// Search implements engine.Connector.
func (g *Gazelle) Search(ctx context.Context, req engine.SearchRequest) ([]engine.Item, error) {
	mode := req.Mode
	if mode == "" {
		mode = engine.ModeSearch
	}
	switch mode {
	case engine.ModeSearch, engine.ModeTV, engine.ModeMovie, engine.ModeMusic, engine.ModeBook:
	default:
		return nil, fmt.Errorf("gazelle: unsupported search mode %q", mode)
	}

	text := gazelleSearchText(req, mode)
	return g.search(ctx, text, req.Limit, req.Offset)
}

// Latest implements engine.Connector. Gazelle has no dedicated latest
// endpoint: an unfiltered browse sorted by time is the latest feed.
func (g *Gazelle) Latest(ctx context.Context, req engine.LatestRequest) ([]engine.Item, error) {
	return g.search(ctx, "", req.Limit, 0)
}

// Download implements engine.Connector. A redirect to a magnet URI is
// surfaced in Payload.Magnet instead of being followed.
func (g *Gazelle) Download(ctx context.Context, item engine.Item) (engine.Payload, error) {
	target := strings.TrimSpace(item.DownloadURL)
	id := firstNonEmpty(item.Attributes["torrent_id"], gazelleGUIDTorrentID(item.GUID))
	switch {
	case g.authKey != "" && id != "" && !strings.HasPrefix(strings.ToLower(target), "magnet:"):
		// A paired authkey/torrent_pass always authorizes through the query.
		target = g.downloadURL(id)
	case target == "" && id != "":
		target = g.downloadURL(id)
	}
	if target == "" {
		magnet := strings.TrimSpace(item.Magnet)
		if magnet != "" {
			return engine.Payload{Magnet: magnet}, nil
		}
		return engine.Payload{}, fmt.Errorf("gazelle: torrent id is missing")
	}
	if strings.HasPrefix(strings.ToLower(target), "magnet:") {
		return engine.Payload{Magnet: target}, nil
	}
	target = resolveURL(g.base, target)

	var (
		resp *engine.Response
		err  error
	)
	if g.authKey != "" {
		// The authkey/torrent_pass pair authenticates through the query, so
		// the plain download path is enough and no header is needed.
		resp, err = g.cfg.Transport.Download(ctx, g.breaker, target, 0)
	} else {
		var req *http.Request
		req, err = http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return engine.Payload{}, redactError("gazelle: download", err, g.secrets()...)
		}
		// Never send tracker credentials to a third-party host.
		if g.sameHost(target) {
			g.applyAuth(req)
		}
		req.Header.Set("Accept", "application/x-bittorrent")
		if g.cfg.UserAgent != "" {
			req.Header.Set("User-Agent", g.cfg.UserAgent)
		}
		resp, err = g.cfg.Transport.Do(ctx, g.breaker, req)
	}
	if err != nil {
		return engine.Payload{}, redactError("gazelle: download", err, g.secrets()...)
	}
	if resp.Status >= 300 && resp.Status < 400 {
		location := strings.TrimSpace(resp.Header.Get("Location"))
		if strings.HasPrefix(strings.ToLower(location), "magnet:") {
			return engine.Payload{Magnet: location}, nil
		}
		return engine.Payload{}, fmt.Errorf("gazelle: download redirected without a magnet (HTTP %d)", resp.Status)
	}
	if resp.Status < 200 || resp.Status >= 300 {
		return engine.Payload{}, fmt.Errorf("gazelle: download failed with HTTP status %d", resp.Status)
	}
	return engine.Payload{
		Body:        resp.Body,
		ContentType: "application/x-bittorrent",
		Filename:    safeFilename(item.Title, ".torrent"),
	}, nil
}

// Test implements engine.Connector. Download is not tested because it would
// consume a real release; the search phase proves the token works.
func (g *Gazelle) Test(ctx context.Context) (engine.TestResult, error) {
	result := engine.TestResult{}

	reach := func() error {
		values := g.browseValues("", 1, 1)
		resp, err := g.browseRequest(ctx, values)
		if err != nil {
			return err
		}
		if !reachableStatus(resp.Status) {
			return fmt.Errorf("unexpected HTTP status %d", resp.Status)
		}
		return nil
	}
	addPhase(&result, "reach", reach)

	search := func() error {
		values := g.browseValues("test", 1, 1)
		resp, err := g.browseRequest(ctx, values)
		if err != nil {
			return err
		}
		if _, err := decodeGazelleResults(resp.Body); err != nil {
			return redactError("search", err, g.secrets()...)
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

func (g *Gazelle) search(ctx context.Context, text string, limit, offset int) ([]engine.Item, error) {
	perPage := gazellePerPage(limit)
	page := gazellePage(perPage, offset)
	values := g.browseValues(text, page, perPage)

	resp, err := g.browseRequest(ctx, values)
	if err != nil {
		return nil, err
	}
	results, err := decodeGazelleResults(resp.Body)
	if err != nil {
		return nil, redactError("gazelle: decode response", err, g.secrets()...)
	}

	items := make([]engine.Item, 0, len(results))
	for _, raw := range results {
		items = append(items, g.parseGroup(raw)...)
	}
	return items, nil
}

func (g *Gazelle) browseValues(text string, page, perPage int) url.Values {
	values := url.Values{}
	values.Set("action", "browse")
	if strings.TrimSpace(text) != "" {
		values.Set("searchstr", text)
	}
	values.Set("order_by", "time")
	values.Set("order_way", "desc")
	values.Set("page", strconv.Itoa(page))
	values.Set("perpage", strconv.Itoa(perPage))
	return values
}

func (g *Gazelle) browseRequest(ctx context.Context, values url.Values) (*engine.Response, error) {
	target, err := withQuery(joinPath(g.cfg.BaseURL, g.browsePath), values)
	if err != nil {
		return nil, redactError("gazelle: build request", err, g.secrets()...)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, redactError("gazelle: build request", err, g.secrets()...)
	}
	g.applyAuth(req)
	req.Header.Set("Accept", "application/json")
	if g.cfg.UserAgent != "" {
		req.Header.Set("User-Agent", g.cfg.UserAgent)
	}
	resp, err := g.cfg.Transport.Do(ctx, g.breaker, req)
	if err != nil {
		return nil, redactError("gazelle: request", err, g.secrets()...)
	}
	if resp.Status < 200 || resp.Status >= 300 {
		return nil, fmt.Errorf("gazelle: browse failed with HTTP status %d", resp.Status)
	}
	return resp, nil
}

func (g *Gazelle) applyAuth(req *http.Request) {
	if req == nil {
		return
	}
	if g.cookieAuth {
		req.Header.Set("Cookie", g.cfg.APIKey)
		return
	}
	if g.token != "" {
		req.Header.Set("Authorization", "token "+g.token)
	}
}

func (g *Gazelle) sameHost(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" {
		return true
	}
	return strings.EqualFold(parsed.Host, g.base.Host)
}

func (g *Gazelle) downloadURL(id string) string {
	values := url.Values{}
	values.Set("action", "download")
	values.Set("id", id)
	if g.authKey != "" {
		values.Set("authkey", g.authKey)
		values.Set("torrent_pass", g.torrentPass)
	}
	target, err := withQuery(joinPath(g.cfg.BaseURL, g.downloadPath), values)
	if err != nil {
		return joinPath(g.cfg.BaseURL, g.downloadPath)
	}
	return target
}

func (g *Gazelle) secrets() []string {
	return []string{g.cfg.APIKey, g.token, g.authKey, g.torrentPass}
}

type gazelleGroupJSON struct {
	GroupID      json.RawMessage `json:"groupId"`
	GroupName    json.RawMessage `json:"groupName"`
	GroupYear    json.RawMessage `json:"groupYear"`
	CategoryID   json.RawMessage `json:"categoryId"`
	CategoryName json.RawMessage `json:"categoryName"`
	GroupTime    json.RawMessage `json:"groupTime"`
	Torrents     json.RawMessage `json:"torrents"`
}

type gazelleTorrentJSON struct {
	TorrentID     json.RawMessage `json:"torrentId"`
	InfoHash      json.RawMessage `json:"infoHash"`
	InfoHashAlt   json.RawMessage `json:"info_hash"`
	Size          json.RawMessage `json:"size"`
	Seeders       json.RawMessage `json:"seeders"`
	Leechers      json.RawMessage `json:"leechers"`
	FilePath      json.RawMessage `json:"filePath"`
	Format        json.RawMessage `json:"format"`
	Encoding      json.RawMessage `json:"encoding"`
	Media         json.RawMessage `json:"media"`
	RemasterTitle json.RawMessage `json:"remasterTitle"`
	AddedTime     json.RawMessage `json:"addedTime"`
	Time          json.RawMessage `json:"time"`
	DownloadLink  json.RawMessage `json:"downloadLink"`
	MagnetLink    json.RawMessage `json:"magnetLink"`
}

func (g *Gazelle) parseGroup(raw json.RawMessage) []engine.Item {
	var group gazelleGroupJSON
	if err := json.Unmarshal(raw, &group); err != nil {
		return nil
	}
	groupName := strings.TrimSpace(coerceString(group.GroupName))
	groupID := coerceIDString(group.GroupID)
	groupYear := coerceIDString(group.GroupYear)
	categoryID := coerceInt(group.CategoryID)
	categoryName := strings.TrimSpace(coerceString(group.CategoryName))
	categories := g.categoriesFor(categoryID, categoryName)

	groupTime := coerceString(group.GroupTime)
	details := ""
	if groupID != "" {
		details = joinPath(g.cfg.BaseURL, "/torrents.php") + "?id=" + url.QueryEscape(groupID)
	}

	torrents := decodeGazelleTorrents(group.Torrents)
	items := make([]engine.Item, 0, len(torrents))
	for _, rawTorrent := range torrents {
		var torrent gazelleTorrentJSON
		if err := json.Unmarshal(rawTorrent, &torrent); err != nil {
			continue
		}
		torrentID := coerceIDString(torrent.TorrentID)
		hash := normalizeInfoHash(firstNonEmpty(
			coerceString(torrent.InfoHash),
			coerceString(torrent.InfoHashAlt),
		))
		magnet := strings.TrimSpace(coerceString(torrent.MagnetLink))
		if hash == "" {
			hash = infoHashFromMagnet(magnet)
		}
		if torrentID == "" && hash == "" {
			continue
		}

		filePath := strings.TrimSpace(coerceString(torrent.FilePath))
		format := strings.TrimSpace(coerceString(torrent.Format))
		title := gazelleTitle(groupName, filePath, format, groupYear)

		download := strings.TrimSpace(coerceString(torrent.DownloadLink))
		if strings.HasPrefix(strings.ToLower(download), "magnet:") {
			if magnet == "" {
				magnet = download
			}
			download = ""
		}

		guid := "gazelle:" + torrentID
		if torrentID == "" {
			guid = "gazelle:" + hash
		}

		item := engine.Item{
			Protocol:   engine.ProtocolTorrent,
			Title:      title,
			GUID:       guid,
			DetailsURL: details,
			Magnet:     magnet,
			InfoHash:   hash,
			Size:       gazelleSize(torrent.Size),
			Seeders:    coerceInt64(torrent.Seeders),
			Leechers:   coerceInt64(torrent.Leechers),
			Categories: categories,
			Published: gazelleTime(firstNonEmpty(
				coerceString(torrent.AddedTime),
				coerceString(torrent.Time),
				groupTime,
			)),
			Attributes: gazelleAttributes(torrentID, groupID, groupYear, categoryID, categoryName, filePath, format,
				coerceString(torrent.Encoding), coerceString(torrent.Media), coerceString(torrent.RemasterTitle), details),
		}
		switch {
		case g.authKey != "" && torrentID != "":
			item.DownloadURL = g.downloadURL(torrentID)
		case download != "":
			item.DownloadURL = resolveURL(g.base, download)
		case torrentID != "":
			item.DownloadURL = g.downloadURL(torrentID)
		}
		items = append(items, item)
	}
	return items
}

func (g *Gazelle) categoriesFor(siteID int, name string) []int {
	if siteID > 0 {
		if mapped, ok := g.categoryMap[siteID]; ok {
			if mapped > 0 {
				return []int{mapped}
			}
			return nil
		}
		if siteID >= 1000 {
			return []int{siteID}
		}
	}
	if name != "" {
		if mapped, ok := gazelleDefaultCategoryNames[strings.ToLower(name)]; ok && mapped > 0 {
			return []int{mapped}
		}
	}
	return nil
}

func gazelleAttributes(torrentID, groupID, groupYear string, categoryID int, categoryName, filePath, format, encoding, media, remaster, details string) map[string]string {
	attributes := make(map[string]string, 12)
	set := func(key, value string) {
		if strings.TrimSpace(value) != "" {
			attributes[key] = value
		}
	}
	set("torrent_id", torrentID)
	set("group_id", groupID)
	set("group_year", groupYear)
	set("category", categoryName)
	if categoryID > 0 {
		set("category_id", strconv.Itoa(categoryID))
	}
	set("file_path", filePath)
	set("format", format)
	set("encoding", encoding)
	set("media", media)
	set("remaster_title", remaster)
	set("details_link", details)
	return attributes
}

func gazelleTitle(groupName, filePath, format, groupYear string) string {
	title := strings.TrimSpace(groupName)
	if title == "" {
		title = strings.TrimSpace(filePath)
	}
	if filePath != "" && !strings.EqualFold(filePath, title) && !gazelleContains(title, filePath) {
		addition := filePath
		// "Artist - Album/01 - Track.flac" under group "Artist - Album"
		// adds only the track part, not the album twice.
		if strings.HasPrefix(strings.ToLower(filePath), strings.ToLower(title)) {
			addition = strings.TrimLeft(filePath[len(title):], "/\\")
		}
		if addition != "" && !gazelleContains(title, addition) {
			title = strings.TrimSpace(title + " " + addition)
		}
	}
	if format != "" && !gazelleContains(title, format) {
		title = strings.TrimSpace(title + " " + format)
	}
	if title == "" {
		title = "Gazelle release"
		if groupYear != "" {
			title += " " + groupYear
		}
	}
	return strings.Join(strings.Fields(title), " ")
}

func gazelleContains(haystack, needle string) bool {
	return strings.Contains(strings.ToLower(haystack), strings.ToLower(strings.TrimSpace(needle)))
}

func gazelleSearchText(req engine.SearchRequest, mode engine.Mode) string {
	if mode == engine.ModeMusic {
		artist := strings.TrimSpace(req.Artist)
		album := strings.TrimSpace(req.Album)
		if artist != "" || album != "" {
			return strings.Join(strings.Fields(artist+" "+album), " ")
		}
		return firstNonEmpty(req.Query, req.Title)
	}

	text := firstNonEmpty(req.Query, req.Title)
	if mode == engine.ModeBook && text == "" {
		text = firstNonEmpty(req.Author, req.Title)
	}
	if mode == engine.ModeTV {
		if text == "" {
			text = strings.TrimSpace(req.IMDbID)
		}
		switch {
		case req.Season > 0 && req.Episode > 0:
			marker := fmt.Sprintf("S%02dE%02d", req.Season, req.Episode)
			if !gazelleContains(text, marker) {
				text = strings.TrimSpace(text + " " + marker)
			}
		case req.Season > 0:
			marker := fmt.Sprintf("S%02d", req.Season)
			if !gazelleContains(text, marker) {
				text = strings.TrimSpace(text + " " + marker)
			}
		}
	}
	if mode == engine.ModeMovie && text == "" {
		text = strings.TrimSpace(req.IMDbID)
	}
	return strings.TrimSpace(text)
}

func gazelleSplitCredentials(raw string) (authKey, torrentPass string) {
	idx := strings.Index(raw, ":")
	if idx <= 0 || idx == len(raw)-1 {
		return "", ""
	}
	left := strings.TrimSpace(raw[:idx])
	right := strings.TrimSpace(raw[idx+1:])
	if left == "" || right == "" || strings.Contains(left, "=") || strings.Contains(right, "=") {
		return "", ""
	}
	return left, right
}

func gazellePath(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		value = fallback
	}
	if !strings.HasPrefix(value, "/") {
		value = "/" + value
	}
	return value
}

func gazellePerPage(limit int) int {
	if limit <= 0 {
		return 100
	}
	if limit > 100 {
		return 100
	}
	return limit
}

func gazellePage(perPage, offset int) int {
	if offset <= 0 || perPage <= 0 {
		return 1
	}
	return offset/perPage + 1
}

func gazelleGUIDTorrentID(guid string) string {
	suffix := strings.TrimPrefix(strings.TrimSpace(guid), "gazelle:")
	if suffix != "" && numericID(suffix) {
		return suffix
	}
	return ""
}

func numericID(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func gazelleSize(raw json.RawMessage) int64 {
	if size := coerceInt64(raw); size > 0 {
		return size
	}
	return nexusParseSize(coerceString(raw))
}

func gazelleTime(raw string) time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}
	}
	if seconds, err := strconv.ParseInt(raw, 10, 64); err == nil && seconds >= 1000000000 {
		return time.Unix(seconds, 0).UTC()
	}
	return parseDate(raw)
}

// decodeGazelleResults accepts both the documented envelope
// {status, response:{results}} and the flatter {results} variant.
func decodeGazelleResults(body []byte) ([]json.RawMessage, error) {
	if len(strings.TrimSpace(string(body))) == 0 {
		return nil, fmt.Errorf("empty response body")
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(body, &root); err != nil {
		return nil, fmt.Errorf("invalid JSON response")
	}
	if rawStatus, ok := root["status"]; ok {
		status := strings.ToLower(strings.TrimSpace(coerceString(rawStatus)))
		switch status {
		case "", "success", "ok", "true", "1":
		default:
			message := firstNonEmpty(
				coerceString(root["error"]),
				coerceString(root["errorMessage"]),
				coerceString(root["message"]),
			)
			if message == "" {
				message = "status " + status
			}
			return nil, fmt.Errorf("API error: %s", message)
		}
	}

	container := root
	if rawResponse, ok := root["response"]; ok {
		var response map[string]json.RawMessage
		if err := json.Unmarshal(rawResponse, &response); err == nil {
			container = response
		} else {
			var list []json.RawMessage
			if err := json.Unmarshal(rawResponse, &list); err == nil {
				return list, nil
			}
			return nil, fmt.Errorf("invalid response object")
		}
	}

	rawResults, ok := container["results"]
	if !ok {
		return nil, fmt.Errorf("response has no results array")
	}
	var results []json.RawMessage
	if err := json.Unmarshal(rawResults, &results); err != nil {
		return nil, fmt.Errorf("results is not an array")
	}
	return results, nil
}

func decodeGazelleTorrents(raw json.RawMessage) []json.RawMessage {
	value := strings.TrimSpace(string(raw))
	if value == "" || value == "null" {
		return nil
	}
	switch value[0] {
	case '[':
		var list []json.RawMessage
		if err := json.Unmarshal(raw, &list); err != nil {
			return nil
		}
		return list
	case '{':
		var object map[string]json.RawMessage
		if err := json.Unmarshal(raw, &object); err != nil {
			return nil
		}
		keys := make([]string, 0, len(object))
		for key := range object {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		list := make([]json.RawMessage, 0, len(keys))
		for _, key := range keys {
			list = append(list, object[key])
		}
		return list
	default:
		return nil
	}
}

func gazelleDefaultCategoryMap() map[int]int {
	return map[int]int{
		0: 3000, // Music
		1: 3000, // Music
		2: 4000, // Applications
		3: 7000, // E-Books
		4: 3030, // Audiobooks
		5: 7000, // E-Learning
		6: 5000, // Comedy
		7: 7030, // Comics
		8: 5070, // Anime
	}
}

var gazelleDefaultCategoryNames = map[string]int{
	"music":        3000,
	"audio":        3000,
	"applications": 4000,
	"application":  4000,
	"software":     4000,
	"games":        4000,
	"e-books":      7000,
	"ebooks":       7000,
	"books":        7000,
	"audiobooks":   3030,
	"e-learning":   7000,
	"comedy":       5000,
	"comics":       7030,
	"anime":        5070,
	"movies":       2000,
	"tv":           5000,
}
