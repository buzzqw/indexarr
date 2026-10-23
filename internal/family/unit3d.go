package family

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/buzzqw/indexarr/internal/engine"
)

const unit3dBreakerFallback = "unit3d"

// Unit3DConfig configures one UNIT3D tracker.
type Unit3DConfig struct {
	Indexer    engine.IndexerRef
	BaseURL    string
	APIKey     string
	Transport  *engine.Transport
	Categories []int // site category ids requested when a search names none
	UserAgent  string
}

// Unit3D is the native connector shared by every UNIT3D tracker.
type Unit3D struct {
	cfg      Unit3DConfig
	base     *url.URL
	breaker  string
	apiBase  string
	baseHref string
}

// NewUnit3D validates cfg and builds a connector. A nil transport uses the
// engine's default transport, so every request still goes through the shared
// guard, rate limiter and circuit breaker implementation.
func NewUnit3D(cfg Unit3DConfig) (*Unit3D, error) {
	base, err := parseBaseURL(cfg.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("unit3d: %w", err)
	}
	cfg.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	cfg.APIKey = strings.TrimSpace(cfg.APIKey)
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("unit3d: API key is required")
	}
	if cfg.Transport == nil {
		cfg.Transport = engine.NewTransport(engine.TransportConfig{UserAgent: cfg.UserAgent})
	}
	cfg.Indexer.Protocol = engine.ProtocolTorrent
	cfg.Categories = append([]int(nil), cfg.Categories...)

	breaker := strings.TrimSpace(cfg.Indexer.Slug)
	if breaker == "" {
		breaker = unit3dBreakerFallback
	}

	apiBase := cfg.BaseURL
	if !strings.HasSuffix(strings.ToLower(apiBase), "/api") {
		apiBase += "/api"
	}

	return &Unit3D{
		cfg:      cfg,
		base:     base,
		breaker:  breaker,
		apiBase:  apiBase,
		baseHref: cfg.BaseURL,
	}, nil
}

// Capabilities implements engine.Connector.
func (u *Unit3D) Capabilities() engine.Capabilities {
	return engine.Capabilities{
		Protocol: engine.ProtocolTorrent,
		Modes:    []engine.Mode{engine.ModeSearch, engine.ModeTV, engine.ModeMovie},
		Params: map[engine.Mode][]string{
			engine.ModeSearch: {engine.ParamQuery},
			engine.ModeTV: {
				engine.ParamQuery,
				engine.ParamIMDbID,
				engine.ParamTMDbID,
				engine.ParamTVDbID,
				engine.ParamSeason,
				engine.ParamEpisode,
			},
			engine.ModeMovie: {
				engine.ParamQuery,
				engine.ParamIMDbID,
				engine.ParamTMDbID,
				engine.ParamTVDbID,
			},
		},
		Categories: append([]int(nil), u.cfg.Categories...),
	}
}

// Search implements engine.Connector.
func (u *Unit3D) Search(ctx context.Context, req engine.SearchRequest) ([]engine.Item, error) {
	mode := req.Mode
	if mode == "" {
		mode = engine.ModeSearch
	}
	switch mode {
	case engine.ModeSearch, engine.ModeTV, engine.ModeMovie:
	default:
		return nil, fmt.Errorf("unit3d: unsupported search mode %q", mode)
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

	categories := req.Categories
	if len(categories) == 0 {
		categories = u.cfg.Categories
	}

	values := url.Values{}
	values.Set("sortField", "created_at")
	values.Set("sortDirection", "desc")
	values.Set("perPage", strconv.Itoa(unit3dPerPage(req.Limit)))
	values.Set("page", strconv.Itoa(unit3dPage(req.Limit, req.Offset)))
	if text != "" {
		// UNIT3D's filter endpoint uses `name`; `search` is accepted by newer
		// releases and by some forks, so both carry the same normalized text.
		values.Set("name", text)
		values.Set("search", text)
	}
	for _, category := range categories {
		if category > 0 {
			values.Add("categories[]", strconv.Itoa(category))
		}
	}
	if id := strings.TrimSpace(req.IMDbID); id != "" {
		values.Set("imdbId", id)
	}
	if id := strings.TrimSpace(req.TMDbID); id != "" {
		values.Set("tmdbId", id)
	}
	if id := strings.TrimSpace(req.TVDbID); id != "" {
		values.Set("tvdbId", id)
	}

	return u.fetch(ctx, values)
}

// Latest implements engine.Connector. UNIT3D has no dedicated latest endpoint:
// an unfiltered first page sorted by creation date is the latest feed.
func (u *Unit3D) Latest(ctx context.Context, req engine.LatestRequest) ([]engine.Item, error) {
	categories := req.Categories
	if len(categories) == 0 {
		categories = u.cfg.Categories
	}
	values := url.Values{}
	values.Set("sortField", "created_at")
	values.Set("sortDirection", "desc")
	values.Set("perPage", strconv.Itoa(unit3dPerPage(req.Limit)))
	values.Set("page", "1")
	for _, category := range categories {
		if category > 0 {
			values.Add("categories[]", strconv.Itoa(category))
		}
	}
	return u.fetch(ctx, values)
}

// Download implements engine.Connector. It prefers the download link returned
// in the search response and falls back to the API download endpoint. A 302 to
// a magnet URI is surfaced in Payload.Magnet instead of being followed.
func (u *Unit3D) Download(ctx context.Context, item engine.Item) (engine.Payload, error) {
	target := strings.TrimSpace(item.DownloadURL)
	if target == "" {
		target = strings.TrimSpace(item.Magnet)
		if target != "" {
			return engine.Payload{Magnet: target}, nil
		}
		id := strings.TrimSpace(item.Attributes["id"])
		if id == "" {
			id = strings.TrimSpace(item.GUID)
		}
		if id == "" {
			return engine.Payload{}, fmt.Errorf("unit3d: torrent id is missing")
		}
		target = u.downloadURL(id)
	}
	if strings.HasPrefix(strings.ToLower(target), "magnet:") {
		return engine.Payload{Magnet: target}, nil
	}
	target = resolveURL(u.base, target)

	resp, err := u.cfg.Transport.Download(ctx, u.breaker, target, 0)
	if err != nil {
		return engine.Payload{}, redactError("unit3d: download", err, u.cfg.APIKey)
	}
	location := strings.TrimSpace(resp.Header.Get("Location"))
	if resp.Status >= 300 && resp.Status < 400 {
		if strings.HasPrefix(strings.ToLower(location), "magnet:") {
			return engine.Payload{Magnet: location}, nil
		}
		return engine.Payload{}, fmt.Errorf("unit3d: download redirected without a magnet (HTTP %d)", resp.Status)
	}
	if resp.Status < 200 || resp.Status >= 300 {
		return engine.Payload{}, fmt.Errorf("unit3d: download failed with HTTP status %d", resp.Status)
	}
	return engine.Payload{
		Body:        resp.Body,
		ContentType: "application/x-bittorrent",
		Filename:    safeFilename(item.Title, ".torrent"),
	}, nil
}

// Test implements engine.Connector. Download is not tested because it would
// consume a real release; the search phase proves the API token works.
func (u *Unit3D) Test(ctx context.Context) (engine.TestResult, error) {
	result := engine.TestResult{}

	reach := func() error {
		values := url.Values{}
		values.Set("perPage", "1")
		resp, err := u.filter(ctx, values, false)
		if err != nil {
			return err
		}
		if !reachableStatus(resp.Status) {
			return fmt.Errorf("unexpected HTTP status %d", resp.Status)
		}
		return nil
	}
	addPhase(&result, "reach", reach)

	auth := func() error {
		values := url.Values{}
		values.Set("perPage", "1")
		values.Set("page", "1")
		resp, err := u.filter(ctx, values, true)
		if err != nil {
			return err
		}
		if resp.Status < 200 || resp.Status >= 300 {
			return fmt.Errorf("authentication failed with HTTP status %d", resp.Status)
		}
		envelope, err := decodeUnit3DEnvelope(resp.Body)
		if err != nil {
			return err
		}
		if envelope.Data == nil {
			return fmt.Errorf("response has no data array")
		}
		if len(envelope.Meta) == 0 || string(envelope.Meta) == "null" {
			return fmt.Errorf("response has no meta object")
		}
		return nil
	}
	addPhase(&result, "auth", auth)

	search := func() error {
		values := url.Values{}
		values.Set("search", "test")
		values.Set("name", "test")
		values.Set("perPage", "1")
		values.Set("page", "1")
		resp, err := u.filter(ctx, values, true)
		if err != nil {
			return err
		}
		if resp.Status < 200 || resp.Status >= 300 {
			return fmt.Errorf("search failed with HTTP status %d", resp.Status)
		}
		envelope, err := decodeUnit3DEnvelope(resp.Body)
		if err != nil {
			return err
		}
		if envelope.Data == nil {
			return fmt.Errorf("response has no data array")
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

func (u *Unit3D) fetch(ctx context.Context, values url.Values) ([]engine.Item, error) {
	resp, err := u.filter(ctx, values, true)
	if err != nil {
		return nil, err
	}
	if resp.Status < 200 || resp.Status >= 300 {
		return nil, fmt.Errorf("unit3d: search failed with HTTP status %d", resp.Status)
	}
	envelope, err := decodeUnit3DEnvelope(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("unit3d: decode response: %s", redact(err.Error(), u.cfg.APIKey))
	}
	if envelope.Data == nil {
		return nil, fmt.Errorf("unit3d: response has no data array")
	}

	items := make([]engine.Item, 0, len(*envelope.Data))
	for _, raw := range *envelope.Data {
		item, ok := u.parseRow(raw)
		if ok {
			items = append(items, item)
		}
	}
	return items, nil
}

func (u *Unit3D) filter(ctx context.Context, values url.Values, withToken bool) (*engine.Response, error) {
	query := make(url.Values, len(values)+1)
	for key, list := range values {
		query[key] = append([]string(nil), list...)
	}
	if withToken {
		query.Set("api_token", u.cfg.APIKey)
	}
	target, err := withQuery(joinPath(u.apiBase, "/torrents/filter"), query)
	if err != nil {
		return nil, fmt.Errorf("unit3d: build request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, fmt.Errorf("unit3d: build request: %s", redact(err.Error(), u.cfg.APIKey))
	}
	req.Header.Set("Accept", "application/json")
	if u.cfg.UserAgent != "" {
		req.Header.Set("User-Agent", u.cfg.UserAgent)
	}
	resp, err := u.cfg.Transport.Do(ctx, u.breaker, req)
	if err != nil {
		return nil, redactError("unit3d: request", err, u.cfg.APIKey)
	}
	return resp, nil
}

func (u *Unit3D) downloadURL(id string) string {
	target := joinPath(u.apiBase, "/torrents/"+url.PathEscape(id)+"/download")
	parsed, err := url.Parse(target)
	if err != nil {
		return target
	}
	query := parsed.Query()
	query.Set("api_token", u.cfg.APIKey)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

type unit3dEnvelope struct {
	Data *[]json.RawMessage `json:"data"`
	Meta json.RawMessage    `json:"meta"`
}

type unit3dRow struct {
	ID           json.RawMessage  `json:"id"`
	DownloadLink json.RawMessage  `json:"download_link"`
	Magnet       json.RawMessage  `json:"magnet"`
	Attributes   unit3dAttributes `json:"attributes"`
}

type unit3dAttributes struct {
	ID           json.RawMessage `json:"id"`
	Name         string          `json:"name"`
	InfoHash     json.RawMessage `json:"info_hash"`
	Size         json.RawMessage `json:"size"`
	Seeders      json.RawMessage `json:"seeders"`
	Leechers     json.RawMessage `json:"leechers"`
	CreatedAt    json.RawMessage `json:"created_at"`
	Category     json.RawMessage `json:"category"`
	CategoryID   json.RawMessage `json:"category_id"`
	IMDbID       json.RawMessage `json:"imdb_id"`
	TMDbID       json.RawMessage `json:"tmdb_id"`
	TVDbID       json.RawMessage `json:"tvdb_id"`
	DownloadLink json.RawMessage `json:"download_link"`
	Magnet       json.RawMessage `json:"magnet"`
	DetailsLink  json.RawMessage `json:"details_link"`
}

func decodeUnit3DEnvelope(body []byte) (unit3dEnvelope, error) {
	var envelope unit3dEnvelope
	if len(strings.TrimSpace(string(body))) == 0 {
		return envelope, fmt.Errorf("empty response body")
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return envelope, fmt.Errorf("invalid JSON response")
	}
	return envelope, nil
}

func (u *Unit3D) parseRow(raw json.RawMessage) (engine.Item, bool) {
	var row unit3dRow
	if err := json.Unmarshal(raw, &row); err != nil {
		return engine.Item{}, false
	}
	title := strings.TrimSpace(row.Attributes.Name)
	if title == "" {
		return engine.Item{}, false
	}

	id := coerceIDString(row.ID)
	if id == "" {
		id = coerceIDString(row.Attributes.ID)
	}
	download := firstNonEmpty(coerceString(row.DownloadLink), coerceString(row.Attributes.DownloadLink))
	magnet := firstNonEmpty(coerceString(row.Magnet), coerceString(row.Attributes.Magnet))
	if strings.HasPrefix(strings.ToLower(download), "magnet:") {
		if magnet == "" {
			magnet = download
		}
		download = ""
	}
	hash := strings.TrimSpace(coerceString(row.Attributes.InfoHash))
	if hash == "" {
		hash = infoHashFromMagnet(magnet)
	}
	if id == "" && download == "" && magnet == "" && hash == "" {
		return engine.Item{}, false
	}

	details := strings.TrimSpace(coerceString(row.Attributes.DetailsLink))
	if details == "" && id != "" {
		details = joinPath(u.baseHref, "/torrents/"+url.PathEscape(id))
	}

	categoryID, categoryName := parseUnit3DCategory(row.Attributes.Category)
	if categoryID == 0 {
		categoryID = coerceInt(row.Attributes.CategoryID)
	}
	categories := make([]int, 0, 1)
	if categoryID > 0 {
		categories = append(categories, categoryID)
	}

	created := strings.TrimSpace(coerceString(row.Attributes.CreatedAt))
	attributes := map[string]string{
		"id":           id,
		"info_hash":    hash,
		"created_at":   created,
		"imdb_id":      coerceIDString(row.Attributes.IMDbID),
		"tmdb_id":      coerceIDString(row.Attributes.TMDbID),
		"tvdb_id":      coerceIDString(row.Attributes.TVDbID),
		"category":     categoryName,
		"category_id":  strconv.Itoa(categoryID),
		"details_link": details,
	}
	if id == "" {
		delete(attributes, "id")
	}
	if categoryID <= 0 {
		delete(attributes, "category_id")
	}

	guid := id
	if guid == "" {
		guid = hash
	}

	item := engine.Item{
		Protocol:    engine.ProtocolTorrent,
		Title:       title,
		GUID:        guid,
		DetailsURL:  details,
		DownloadURL: "",
		Magnet:      magnet,
		InfoHash:    hash,
		Size:        coerceInt64(row.Attributes.Size),
		Seeders:     coerceInt64(row.Attributes.Seeders),
		Leechers:    coerceInt64(row.Attributes.Leechers),
		Categories:  categories,
		Published:   parseDate(created),
		IMDbID:      coerceIDString(row.Attributes.IMDbID),
		TMDbID:      coerceIDString(row.Attributes.TMDbID),
		TVDbID:      coerceIDString(row.Attributes.TVDbID),
		Attributes:  attributes,
	}
	switch {
	case download != "":
		item.DownloadURL = resolveURL(u.base, download)
	case id != "":
		item.DownloadURL = u.downloadURL(id)
	}
	return item, true
}

// parseUnit3DCategory accepts a string, a number, an object with id/name or a
// one-element array of any of those.
func parseUnit3DCategory(raw json.RawMessage) (int, string) {
	value := strings.TrimSpace(string(raw))
	if value == "" || value == "null" {
		return 0, ""
	}
	switch value[0] {
	case '{':
		var obj struct {
			ID   json.RawMessage `json:"id"`
			Name json.RawMessage `json:"name"`
		}
		if err := json.Unmarshal(raw, &obj); err != nil {
			return 0, ""
		}
		id := coerceInt(obj.ID)
		name := strings.TrimSpace(coerceString(obj.Name))
		if id == 0 && name != "" {
			if numeric, err := strconv.Atoi(name); err == nil {
				id = numeric
			}
		}
		return id, name
	case '[':
		var list []json.RawMessage
		if err := json.Unmarshal(raw, &list); err != nil || len(list) == 0 {
			return 0, ""
		}
		return parseUnit3DCategory(list[0])
	case '"':
		name := strings.TrimSpace(coerceString(raw))
		if numeric, err := strconv.Atoi(name); err == nil {
			return numeric, name
		}
		return 0, name
	default:
		if numeric, err := strconv.Atoi(value); err == nil {
			return numeric, value
		}
		return 0, strings.TrimSpace(coerceString(raw))
	}
}

func unit3dPerPage(limit int) int {
	if limit <= 0 {
		return 100
	}
	if limit > 100 {
		return 100
	}
	return limit
}

func unit3dPage(limit, offset int) int {
	if offset <= 0 {
		return 1
	}
	perPage := unit3dPerPage(limit)
	if perPage <= 0 {
		return 1
	}
	return offset/perPage + 1
}

func reachableStatus(status int) bool {
	if status >= 200 && status < 400 {
		return true
	}
	switch status {
	case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusTooManyRequests:
		return true
	default:
		return false
	}
}

func addPhase(result *engine.TestResult, name string, run func() error) {
	started := time.Now()
	err := run()
	phase := engine.TestPhase{
		Name:     name,
		OK:       err == nil,
		Duration: time.Since(started),
	}
	if err != nil {
		phase.Message = err.Error()
	}
	result.Phases = append(result.Phases, phase)
}
