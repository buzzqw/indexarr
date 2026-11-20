package family

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	stdcat "github.com/buzzqw/indexarr/internal/categories"
	"github.com/buzzqw/indexarr/internal/engine"
)

// The Rats Search connector talks to a Rats Search node (librats/rats-search),
// a BitTorrent index crawled from the DHT and served over the node's own REST
// API. It is a source like any other: it feeds the cache, it joins the live
// fan-out and it never replaces the stored catalogue.
//
// The node knows one word filter and a coarse content type, so this connector
// is a keyword source, like the public DHT search engines already in the
// catalog. What the node does not offer is handled here:
//
//   - a request for TV and a request for movies are the same search on the
//     node ("video" covers both), so a video row is published under 2000 and
//     5000 and the client's own title and season checks discard the rest;
//   - season, episode and year are folded into the keyword text, because the
//     API has no parameter for them;
//   - the requested categories become the node's `type` when one content type
//     covers all of them, and are applied to the answer otherwise.
const (
	ratsLabel        = "rats"
	ratsBreakerKey   = "rats"
	ratsDefaultLimit = 100
	// ratsMaxNodeMessage keeps a node's error text from filling a history row.
	ratsMaxNodeMessage = 200
)

// RatsConfig configures one Rats Search node. BaseURL is the node's REST
// address, for example http://127.0.0.1:8095; the /api suffix is optional.
type RatsConfig struct {
	Indexer    engine.IndexerRef
	BaseURL    string
	Transport  *engine.Transport
	Categories []int // categories used when a request names none
	UserAgent  string
	// SafeSearch asks the node to keep adult releases out of a keyword
	// search. It is a setting because the node's own classifier decides what
	// is adult.
	SafeSearch bool
}

// Rats is the connector for a Rats Search node.
type Rats struct {
	cfg     RatsConfig
	apiBase string
	breaker string
}

var _ engine.Connector = (*Rats)(nil)

// NewRats validates cfg and builds a connector.
func NewRats(cfg RatsConfig) (*Rats, error) {
	apiBase, err := ratsAPIBase(cfg.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", ratsLabel, err)
	}
	cfg.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	cfg.Indexer.Protocol = engine.ProtocolTorrent
	cfg.Categories = append([]int(nil), cfg.Categories...)
	if cfg.Transport == nil {
		cfg.Transport = engine.NewTransport(engine.TransportConfig{UserAgent: cfg.UserAgent})
	}
	breaker := strings.TrimSpace(cfg.Indexer.Slug)
	if breaker == "" {
		breaker = ratsBreakerKey
	}
	return &Rats{cfg: cfg, apiBase: apiBase, breaker: breaker}, nil
}

// ratsAPIBase turns the address the user typed into the root of the REST API.
// The node serves its methods under /api, so both "http://host:8095" and
// "http://host:8095/api" are accepted.
func ratsAPIBase(raw string) (string, error) {
	parsed, err := parseBaseURL(raw)
	if err != nil {
		return "", err
	}
	path := strings.TrimRight(parsed.Path, "/")
	if !strings.HasSuffix(path, "/api") {
		path += "/api"
	}
	parsed.Path = path
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return strings.TrimRight(parsed.String(), "/"), nil
}

// Capabilities implements engine.Connector. Every mode is a keyword search:
// the parameters a client sends are folded into the node's text filter.
func (r *Rats) Capabilities() engine.Capabilities {
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
			engine.ModeTV:     {engine.ParamQuery, engine.ParamSeason, engine.ParamEpisode},
			engine.ModeMovie:  {engine.ParamQuery, engine.ParamYear},
			engine.ModeMusic:  {engine.ParamQuery, engine.ParamArtist, engine.ParamAlbum},
			engine.ModeBook:   {engine.ParamQuery, engine.ParamAuthor, engine.ParamTitle},
		},
		Categories: append([]int(nil), r.cfg.Categories...),
	}
}

// Search implements engine.Connector. A search without keywords — the
// conventional "latest" query an indexer receives — is answered with the
// node's newest torrents instead of an empty text filter.
func (r *Rats) Search(ctx context.Context, req engine.SearchRequest) ([]engine.Item, error) {
	mode := req.Mode
	if mode == "" {
		mode = engine.ModeSearch
	}
	if !mode.Valid() {
		return nil, fmt.Errorf("%s: unsupported search mode %q", ratsLabel, mode)
	}

	categories := req.Categories
	if len(categories) == 0 {
		categories = r.cfg.Categories
	}

	text := ratsText(req)
	if text == "" {
		return r.recent(ctx, categories, req.Limit, 0)
	}

	values := url.Values{}
	values.Set("text", text)
	values.Set("limit", strconv.Itoa(ratsLimit(req.Limit)))
	if req.Offset > 0 {
		values.Set("index", strconv.Itoa(req.Offset))
	}
	if object := ratsTypeFor(categories); object != "" {
		values.Set("type", object)
	}
	if r.cfg.SafeSearch {
		values.Set("safeSearch", "true")
	}
	items, err := r.search(ctx, values, categories)
	if err != nil {
		return nil, err
	}
	return ratsFilter(items, categories), nil
}

// Latest implements engine.Connector with the node's own list of new torrents.
// The API answers one page and takes no categories, no safe-search flag and no
// page number, so the selection happens here: rows outside the requested
// categories, or older than the source's max age, are dropped.
func (r *Rats) Latest(ctx context.Context, req engine.LatestRequest) ([]engine.Item, error) {
	categories := req.Categories
	if len(categories) == 0 {
		categories = r.cfg.Categories
	}
	return r.recent(ctx, categories, req.Limit, req.MaxAge)
}

// Download implements engine.Connector. The node serves no .torrent: a release
// leaves as the magnet its infohash builds.
func (r *Rats) Download(ctx context.Context, item engine.Item) (engine.Payload, error) {
	if magnet := strings.TrimSpace(item.Magnet); magnet != "" {
		return engine.Payload{Magnet: magnet}, nil
	}
	hash := normalizeInfoHash(item.InfoHash)
	if !ratsHexHash(hash) {
		return engine.Payload{}, fmt.Errorf("%s: release infohash is missing", ratsLabel)
	}
	return engine.Payload{Magnet: ratsMagnet(hash, item.Title)}, nil
}

// Test implements engine.Connector. The reach phase proves the REST API answers
// and reports how large the node's index is; the search phase proves a query
// comes back in the documented shape. An empty node answers the search with no
// rows and that is not a fault: the node is simply still crawling.
func (r *Rats) Test(ctx context.Context) (engine.TestResult, error) {
	result := engine.TestResult{}

	started := time.Now()
	torrents, err := r.stats(ctx)
	phase := engine.TestPhase{Name: "reach", OK: err == nil, Duration: time.Since(started)}
	if err != nil {
		phase.Message = err.Error()
	} else {
		phase.Message = fmt.Sprintf("%d torrents in the node's index", torrents)
	}
	result.Phases = append(result.Phases, phase)

	addPhase(&result, "search", func() error {
		_, searchErr := r.Search(ctx, engine.SearchRequest{
			Mode:  engine.ModeSearch,
			Query: "test",
			Limit: 1,
		})
		return searchErr
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

// ratsText builds the node's single keyword filter from a normalized request.
// Order matters: a music request carries artist and album instead of a query,
// a book request author and title, and a tv request season and episode.
func ratsText(req engine.SearchRequest) string {
	text := firstNonEmpty(
		req.Query,
		strings.TrimSpace(req.Artist+" "+req.Album),
		strings.TrimSpace(req.Author+" "+req.Title),
		req.Title,
		req.Album,
		req.Artist,
		req.Author,
	)
	if req.Season > 0 && req.Episode > 0 {
		marker := fmt.Sprintf("S%02dE%02d", req.Season, req.Episode)
		if !strings.Contains(strings.ToUpper(text), marker) {
			text = strings.TrimSpace(text + " " + marker)
		}
	}
	if req.Year > 0 && !strings.Contains(text, strconv.Itoa(req.Year)) {
		text = strings.TrimSpace(text + " " + strconv.Itoa(req.Year))
	}
	return text
}

// ratsTypeFor returns the node's content type when one type covers every
// requested category, and "" when none does: asking for "video" on a search
// that also wants audio would drop the audio rows.
func ratsTypeFor(requested []int) string {
	object := ""
	for _, id := range requested {
		next := ratsTypeForCategory(id)
		if next == "" {
			return ""
		}
		if object == "" {
			object = next
			continue
		}
		if object != next {
			return ""
		}
	}
	return object
}

// ratsTypeForCategory maps a standard category to the node's content type.
// Other (8000 and its children) has no equivalent and returns "": those rows
// are picked up by the answer and kept by ratsFilter.
func ratsTypeForCategory(id int) string {
	switch {
	case id >= 1000 && id < 2000:
		return "games" // Console
	case id >= 2000 && id < 3000:
		return "video" // Movies
	case id >= 5000 && id < 6000:
		return "video" // TV
	case id >= 6000 && id < 7000:
		return "video" // XXX is video content, and the node has no adult type
	case id >= 3000 && id < 4000:
		return "audio"
	case id >= 7000 && id < 8000:
		return "books"
	case id == 4050:
		return "games"
	case id >= 4000 && id < 5000:
		return "software"
	default:
		return ""
	}
}

// ratsStandardCategories maps the node's content type to standard categories.
//
// A video row is the only ambiguous case: the node detects the file type, not
// the format, so a film and an episode both arrive as "video". A stored release
// carries one category, and publishing it under both Movies and TV would mean
// storing only the first one — the other client would never find it in the
// cache. The request that found it therefore decides: a TV search stores it
// under TV, a movie search under Movies, and a search that names neither (the
// manual search page, parse-only calls) stores Movies, the broader bucket.
func ratsStandardCategories(raw string, requested []int) []int {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "video":
		tv, movies := false, false
		for _, id := range requested {
			switch {
			case id >= 5000 && id < 6000:
				tv = true
			case id >= 2000 && id < 3000:
				movies = true
			}
		}
		if tv && !movies {
			return []int{5000}
		}
		return []int{2000}
	case "audio":
		return []int{3000}
	case "books", "book":
		return []int{7000}
	case "software":
		return []int{4000}
	case "games", "game":
		return []int{4050}
	default:
		// pictures, archive, other and anything new the node adds
		return []int{8000}
	}
}

// ratsFilter keeps the rows that answer the requested categories. An empty
// selection keeps everything: the source is configured without a narrowing.
func ratsFilter(items []engine.Item, requested []int) []engine.Item {
	if len(requested) == 0 {
		return items
	}
	out := items[:0]
	for _, item := range items {
		if ratsMatchesCategories(item.Categories, requested) {
			out = append(out, item)
		}
	}
	return out
}

// ratsMatchesCategories reports whether one of the requested categories, or
// its parent, is one of the item's categories or their parent.
func ratsMatchesCategories(have, requested []int) bool {
	for _, want := range requested {
		wantParent := stdcat.Parent(want)
		if wantParent == 0 {
			wantParent = want
		}
		for _, got := range have {
			gotParent := stdcat.Parent(got)
			if gotParent == 0 {
				gotParent = got
			}
			if got == want || got == wantParent || gotParent == want || gotParent == wantParent {
				return true
			}
		}
	}
	return false
}

func (r *Rats) search(ctx context.Context, values url.Values, categories []int) ([]engine.Item, error) {
	data, err := r.get(ctx, "search.torrents", values)
	if err != nil {
		return nil, err
	}
	rows, err := ratsRows(data, "search.torrents")
	if err != nil {
		return nil, err
	}
	return ratsItems(rows, categories), nil
}

// recent reads the node's newest torrents. maxAge keeps a walk from filling the
// cache with rows the source's own max age already excludes; the node answers
// newest first, so the walk stops at the first dated row that is too old.
func (r *Rats) recent(ctx context.Context, categories []int, limit int, maxAge time.Duration) ([]engine.Item, error) {
	values := url.Values{}
	values.Set("limit", strconv.Itoa(ratsLimit(limit)))
	data, err := r.get(ctx, "search.recent", values)
	if err != nil {
		return nil, err
	}
	rows, err := ratsRows(data, "search.recent")
	if err != nil {
		return nil, err
	}

	items := make([]engine.Item, 0, len(rows))
	cutoff := time.Time{}
	if maxAge > 0 {
		cutoff = time.Now().Add(-maxAge)
	}
	for _, item := range ratsItems(rows, categories) {
		if !cutoff.IsZero() && !item.Published.IsZero() && item.Published.Before(cutoff) {
			break
		}
		items = append(items, item)
	}
	return ratsFilter(items, categories), nil
}

// stats returns how many torrents the node holds. It is the proof that the
// REST API is on and answering.
func (r *Rats) stats(ctx context.Context) (int64, error) {
	data, err := r.get(ctx, "stats.get", nil)
	if err != nil {
		return 0, err
	}
	if len(data) == 0 || string(data) == "null" {
		return 0, nil
	}
	var payload struct {
		Torrents json.RawMessage `json:"torrents"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return 0, fmt.Errorf("%s: stats.get: the answer is not a statistics object", ratsLabel)
	}
	return coerceInt64(payload.Torrents), nil
}

// get calls one method and returns the envelope's data. A node that reports a
// failure is a failure: the message it sent is the cause, not an empty answer.
func (r *Rats) get(ctx context.Context, method string, values url.Values) (json.RawMessage, error) {
	target, err := withQuery(r.apiBase+"/"+method, values)
	if err != nil {
		return nil, fmt.Errorf("%s: build request: %w", ratsLabel, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, fmt.Errorf("%s: build request: %s", ratsLabel, err)
	}
	req.Header.Set("Accept", "application/json")
	if r.cfg.UserAgent != "" {
		req.Header.Set("User-Agent", r.cfg.UserAgent)
	}
	resp, err := r.cfg.Transport.Do(ctx, r.breaker, req)
	if err != nil {
		return nil, redactError(ratsLabel+": "+method, err)
	}
	if resp.Status < 200 || resp.Status >= 300 {
		return nil, fmt.Errorf("%s: %s failed with HTTP status %d", ratsLabel, method, resp.Status)
	}
	data, err := ratsData(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("%s: %s: %w", ratsLabel, method, err)
	}
	return data, nil
}

// ratsEnvelope is the documented answer of every method:
// {success, data, requestId} or {success: false, error}.
type ratsEnvelope struct {
	Success *bool           `json:"success"`
	Data    json.RawMessage `json:"data"`
	Error   string          `json:"error"`
}

// ratsTorrent is one row of search.torrents or search.recent: the fields of the
// Torrent Object Format the connector reads. Everything else in the object (the
// file list, the piece size, the tracker payloads) is not needed to index a
// release. The numbers are read through json.RawMessage because the node has
// answered with both numbers and numeric strings across versions.
type ratsTorrent struct {
	Hash        json.RawMessage `json:"hash"`
	Name        json.RawMessage `json:"name"`
	Size        json.RawMessage `json:"size"`
	Added       json.RawMessage `json:"added"`
	ContentType json.RawMessage `json:"contentType"`
	Seeders     json.RawMessage `json:"seeders"`
	Leechers    json.RawMessage `json:"leechers"`
}

// ratsData reads the envelope and returns its data member.
func ratsData(body []byte) (json.RawMessage, error) {
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, errors.New("empty response body")
	}
	var envelope ratsEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, errors.New("invalid JSON response")
	}
	if envelope.Success != nil && !*envelope.Success {
		message := strings.TrimSpace(envelope.Error)
		if message == "" {
			message = "the node reported a failure"
		}
		return nil, fmt.Errorf("the node answered: %s", ratsShorten(message, ratsMaxNodeMessage))
	}
	return envelope.Data, nil
}

// ratsRows decodes the data member of a search method. An empty or null answer
// is a node with nothing to say, not a broken response.
func ratsRows(data json.RawMessage, method string) ([]ratsTorrent, error) {
	if len(data) == 0 || string(data) == "null" {
		return nil, nil
	}
	var rows []ratsTorrent
	if err := json.Unmarshal(data, &rows); err != nil {
		return nil, fmt.Errorf("%s: the answer is not a list of torrents", method)
	}
	return rows, nil
}

// ratsItems turns the rows into releases. A row without a name or without a
// usable infohash is skipped: it could not be grabbed, and a release indexarr
// cannot hand back is not a release.
func ratsItems(rows []ratsTorrent, requested []int) []engine.Item {
	items := make([]engine.Item, 0, len(rows))
	for _, row := range rows {
		title := strings.TrimSpace(coerceString(row.Name))
		hash := normalizeInfoHash(coerceString(row.Hash))
		if title == "" || !ratsHexHash(hash) {
			continue
		}
		items = append(items, engine.Item{
			Protocol: engine.ProtocolTorrent,
			Title:    title,
			// The infohash is the release's identity, and it is the same one a
			// site would hand out for the same torrent, so the cache merges
			// the two rows and keeps the site's seeders.
			GUID:       hash,
			Magnet:     ratsMagnet(hash, title),
			InfoHash:   hash,
			Size:       coerceInt64(row.Size),
			Seeders:    coerceInt64(row.Seeders),
			Leechers:   coerceInt64(row.Leechers),
			Categories: ratsStandardCategories(coerceString(row.ContentType), requested),
			Published:  ratsAdded(row.Added),
		})
	}
	return items
}

// ratsMagnet builds the magnet a client grabs. It matches the link indexarr
// builds for a definition that only carries an infohash.
func ratsMagnet(hash, title string) string {
	magnet := "magnet:?xt=urn:btih:" + hash
	if title = strings.TrimSpace(title); title != "" {
		magnet += "&dn=" + url.QueryEscape(title)
	}
	return magnet
}

// ratsAdded reads the node's added timestamp. It is documented in milliseconds;
// a value small enough to be seconds is read as seconds, so an older node still
// dates its rows instead of sending them all to 1970.
func ratsAdded(raw json.RawMessage) time.Time {
	value := coerceInt64(raw)
	if value <= 0 {
		return time.Time{}
	}
	if value < 1e11 {
		return time.Unix(value, 0)
	}
	return time.UnixMilli(value)
}

// ratsHexHash reports whether hash is the canonical 40-character hexadecimal
// infohash a magnet needs.
func ratsHexHash(hash string) bool {
	if len(hash) != 40 {
		return false
	}
	_, err := hex.DecodeString(hash)
	return err == nil
}

func ratsLimit(limit int) int {
	if limit <= 0 {
		return ratsDefaultLimit
	}
	return limit
}

// ratsShorten keeps a message readable in a history row.
func ratsShorten(message string, max int) string {
	message = strings.Join(strings.Fields(message), " ")
	if len(message) <= max {
		return message
	}
	return strings.TrimSpace(message[:max]) + "…"
}
