// Package torznabapi exposes the cached releases through a Torznab server, so
// Sonarr, Radarr, Lidarr and any other Torznab client can query indexarr the
// same way they query Jackett or Prowlarr.
package torznabapi

import (
	"bytes"
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/xml"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/buzzqw/indexarr/internal/categories"
	"github.com/buzzqw/indexarr/internal/definition"
	"github.com/buzzqw/indexarr/internal/engine"
	"github.com/buzzqw/indexarr/internal/indexer"
	"github.com/buzzqw/indexarr/internal/store"
)

// Server implements the Torznab endpoint and the .torrent download route.
type Server struct {
	store      *store.Store
	log        *slog.Logger
	version    string
	started    time.Time
	limits     *minuteLimiter
	liveSearch func(context.Context, indexer.LiveQuery) (indexer.LiveResult, error)
	// indexerCaps returns the real capabilities of the selected indexers. Nil
	// falls back to the historical fixed caps document.
	indexerCaps func(context.Context, []int64) (map[int64]engine.Capabilities, error)
	// latestIndexers restricts the *arr RSS path to the sites that publish a
	// latest list. Nil means no restriction (the historical cache response).
	latestIndexers func(context.Context) ([]int64, error)

	downloadResolver func(context.Context, int64) ([]byte, string, string, error)

	grabCheck  func(context.Context, int64) error
	grabRecord func(context.Context, int64, int64, string) error
}

// SetGrabGuard installs the optional grab policy. check is called before a
// stored or resolved payload is served; a non-nil error is reported as a grab
// limit and blocks the download. record is called after a GET response was
// written successfully. HEAD requests are checked but never recorded.
// Missing releases or releases without provenance are not blocked.
func (s *Server) SetGrabGuard(check func(context.Context, int64) error, record func(context.Context, int64, int64, string) error) {
	s.grabCheck = check
	s.grabRecord = record
}

// SetLiveSearch installs the callback used by the live fan-out. It runs on
// every search that carries a query, not only when the cache is empty.
func (s *Server) SetLiveSearch(fn func(context.Context, indexer.LiveQuery) (indexer.LiveResult, error)) {
	s.liveSearch = fn
}

// SetCapabilities installs the callback that returns the real capabilities of
// the selected indexers. The aggregate endpoint intersects them; a per-indexer
// endpoint exposes that indexer's own document.
func (s *Server) SetCapabilities(fn func(context.Context, []int64) (map[int64]engine.Capabilities, error)) {
	s.indexerCaps = fn
}

// SetLatestIndexers installs the list used by the *arr RSS path. Only the
// indexers that publish a new-releases list contribute to an empty-query
// request; a search-only site never does.
func (s *Server) SetLatestIndexers(fn func(context.Context) ([]int64, error)) {
	s.latestIndexers = fn
}

// SetDownloadResolver installs the callback used to fetch a payload that is
// not stored yet (an opaque release_sources.download_ref). The callback
// returns body, content type and filename.
func (s *Server) SetDownloadResolver(fn func(context.Context, int64) ([]byte, string, string, error)) {
	s.downloadResolver = fn
}

// New builds the server.
func New(st *store.Store, logger *slog.Logger, version string) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	return &Server{store: st, log: logger, version: version, started: time.Now(), limits: newMinuteLimiter()}
}

const (
	pathAPI      = "/api"
	pathNewznab  = "/newznab/api"
	pathTorznab  = "/torznab/api"
	pathDownload = "/dl/"
	maxResults   = 100
	defaultLimit = 50
)

// ServeHTTP routes the Torznab API, its Jackett-compatible aliases and the
// torrent downloads.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, pathDownload) {
		s.handleDownload(w, r)
		return
	}
	if slug, ok := torznabPath(r.URL.Path); ok {
		s.handleAPI(w, r, slug)
		return
	}
	http.NotFound(w, r)
}

func (s *Server) handleAPI(w http.ResponseWriter, r *http.Request, slug string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		writeTorznabError(w, http.StatusMethodNotAllowed, torznabIncorrectParameter, "method not allowed")
		return
	}
	settings, err := s.store.LoadSettings(r.Context())
	if err != nil {
		s.log.Error("torznab: load settings", "component", "indexarr", "subsystem", "torznab", "error", err)
		writeTorznabError(w, http.StatusInternalServerError, torznabInternalError, "internal error")
		return
	}
	query := r.URL.Query()
	function := strings.ToLower(strings.TrimSpace(query.Get("t")))
	if function == "" {
		function = "search"
	}
	aggregate := isAggregateSlug(slug)
	if function == "caps" {
		var ids []int64
		if !aggregate {
			indexer, found, err := s.resolveIndexer(r.Context(), slug)
			if err != nil {
				s.log.Error("torznab: resolve indexer", "component", "indexarr", "subsystem", "torznab", "error", err)
				writeTorznabError(w, http.StatusInternalServerError, torznabInternalError, "internal error")
				return
			}
			if !found {
				writeTorznabError(w, http.StatusNotFound, torznabIncorrectParameter, "indexer not found: "+slug)
				return
			}
			ids = []int64{indexer.ID}
		} else if selection, err := s.resolveAggregateSelection(r.Context(), parseAggregateFilters(query)); err == nil && !selection.all {
			for id := range selection.ids {
				ids = append(ids, id)
			}
			sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		}
		caps, ok := engine.Capabilities{}, false
		if s.indexerCaps != nil {
			if byID, err := s.indexerCaps(r.Context(), ids); err != nil {
				s.log.Warn("torznab: load caps", "component", "indexarr", "subsystem", "torznab", "error", err)
			} else if len(byID) > 0 {
				caps, ok = mergeCapabilities(byID), true
			}
		}
		s.writeCaps(w, settings, caps, ok)
		return
	}
	apiKey, ok := s.authorize(w, r, settings)
	if !ok {
		return
	}
	if function == "indexers" {
		s.writeIndexersAnswer(w, r, query, settings)
		return
	}
	if function == "get" {
		if !aggregate {
			if _, found, err := s.resolveIndexer(r.Context(), slug); err != nil {
				s.log.Error("torznab: resolve indexer", "component", "indexarr", "subsystem", "torznab", "error", err)
				writeTorznabError(w, http.StatusInternalServerError, torznabInternalError, "internal error")
				return
			} else if !found {
				writeTorznabError(w, http.StatusNotFound, torznabIncorrectParameter, "indexer not found: "+slug)
				return
			}
		}
		rawID := strings.TrimSpace(query.Get("id"))
		if rawID == "" {
			writeTorznabError(w, http.StatusBadRequest, torznabMissingParameter, "missing required parameter: id")
			return
		}
		if id, err := strconv.ParseInt(rawID, 10, 64); err == nil {
			if id <= 0 {
				writeTorznabError(w, http.StatusBadRequest, torznabIncorrectParameter, "invalid release id")
				return
			}
			s.serveReleaseByID(w, r, id, "")
			return
		}
		release, found, err := s.store.ReleaseByExternalID(r.Context(), rawID)
		if err != nil {
			s.log.Error("torznab: get by external id", "component", "indexarr", "subsystem", "torznab", "error", err)
			writeTorznabError(w, http.StatusInternalServerError, torznabInternalError, "internal error")
			return
		}
		if !found {
			writeTorznabError(w, http.StatusNotFound, torznabIncorrectParameter, "release not found")
			return
		}
		s.serveRelease(w, r, release, "")
		return
	}
	switch function {
	case "search", "tvsearch", "movie", "music", "book":
	default:
		writeTorznabError(w, http.StatusBadRequest, torznabNoSuchFunction, "unsupported function: "+function)
		return
	}

	started := time.Now()
	outcome := s.runSearch(r.Context(), slug, query, settings, apiKey,
		s.baseURL(r, settings), function)
	outcome.Duration = time.Since(started)
	s.recordAppSearch(r.Context(), r, function, outcome, started)
	s.writeSearchOutcome(w, outcome)
}

// searchOutcome is one search response produced by the exact /api code path.
// Keeping the XML and the rows together lets "Try as Sonarr" show the result
// table and the real client document without running the search twice.
type searchOutcome struct {
	Status       int
	Body         []byte
	Releases     []store.Release
	Total        int64
	Provenance   map[int64]store.ReleaseProvenance
	Mode         string
	Query        string
	IndexerIDs   []int64
	IndexerNames []string
	Live         []indexer.LiveIndexerResult
	Err          string
	Duration     time.Duration
}

// runSearch implements every search function of /api, /torznab/api and
// /newznab/api. The HTTP handler and Try both call it, so the XML shown in the
// UI is byte-for-byte the document Sonarr receives.
func (s *Server) runSearch(ctx context.Context, slug string, query url.Values, settings store.Settings, apiKey, base, mode string) searchOutcome {
	out := searchOutcome{Status: http.StatusOK, Mode: mode,
		Query: strings.TrimSpace(query.Get("q"))}
	aggregate := isAggregateSlug(slug)
	var selection releaseSelection
	var err error
	if aggregate {
		selection, err = s.resolveAggregateSelection(ctx, parseAggregateFilters(query))
		if err != nil {
			s.log.Error("torznab: resolve filters", "component", "indexarr", "subsystem", "torznab", "error", err)
			return s.errorOutcome(http.StatusInternalServerError, torznabInternalError, "internal error")
		}
	} else {
		indexer, found, lookupErr := s.resolveIndexer(ctx, slug)
		if lookupErr != nil {
			s.log.Error("torznab: resolve indexer", "component", "indexarr", "subsystem", "torznab", "error", lookupErr)
			return s.errorOutcome(http.StatusInternalServerError, torznabInternalError, "internal error")
		}
		if !found {
			return s.errorOutcome(http.StatusNotFound, torznabIncorrectParameter, "indexer not found: "+slug)
		}
		selection = selectionForIndexer(indexer)
	}
	out.IndexerIDs, out.IndexerNames = selectionLists(selection)

	filter := store.Filter{
		Query:   strings.TrimSpace(query.Get("q")),
		Limit:   parseLimit(query.Get("limit")),
		Offset:  parseOffset(query.Get("offset")),
		Sort:    "recent",
		Season:  positiveInt(query.Get("season")),
		Episode: positiveInt(query.Get("ep")),
		Year:    positiveInt(query.Get("year")),
		IMDbID:  query.Get("imdbid"),
		TMDbID:  query.Get("tmdbid"),
		TVDbID:  query.Get("tvdbid"),
	}
	if rawCategories := strings.TrimSpace(query.Get("cat")); rawCategories != "" {
		filter.Categories = splitCategories(rawCategories)
	}
	if selection.empty() {
		out.Body = s.searchXML(base, settings, apiKey, nil, 0, filter.Offset, nil)
		return out
	}
	if len(selection.ids) > 0 {
		filter.IndexerIDs = make([]int64, 0, len(selection.ids))
		for id := range selection.ids {
			filter.IndexerIDs = append(filter.IndexerIDs, id)
		}
	}
	// An *arr RSS request (t=search or t=tvsearch without q) reads only the
	// cache of the sites that publish a latest list. Search-only sites do not
	// participate and no synthetic query is generated for them. The rule is
	// for the aggregate feed: a per-site endpoint is already scoped to one
	// indexer, and Servarr validates a new per-site indexer with an empty
	// tvsearch/movie request. Filtering that request by "latest" made every
	// search-only site fail the add with "no results" (Blocco C).
	if aggregate && !liveQueryEligible(mode, filter) && (mode == "search" || mode == "tvsearch") && s.latestIndexers != nil {
		latest, latestErr := s.latestIndexers(ctx)
		if latestErr != nil {
			s.log.Warn("torznab: load latest indexers", "component", "indexarr",
				"subsystem", "torznab", "error", latestErr)
		} else {
			filter.IndexerIDs = intersectIndexerIDs(filter.IndexerIDs, latest)
			if len(filter.IndexerIDs) == 0 {
				out.Body = s.searchXML(base, settings, apiKey, nil, 0, filter.Offset, nil)
				return out
			}
		}
	}
	releases, total, err := s.store.SearchReleases(ctx, filter)
	if err != nil {
		s.log.Error("torznab: search", "component", "indexarr", "subsystem", "torznab", "error", err)
		return s.errorOutcome(http.StatusInternalServerError, torznabInternalError, "internal error")
	}
	// The live fan-out runs on every request that carries a query. The cache
	// is not replaced: what came back in time is stored, then the same cache
	// query runs again and provenance deduplicates the union.
	if filter.Offset == 0 && settings.TorznabLiveSearch && s.liveSearch != nil && liveQueryEligible(mode, filter) {
		timeout := time.Duration(settings.TorznabLiveTimeout) * time.Second
		if timeout <= 0 {
			timeout = 20 * time.Second
		}
		wait := time.Duration(settings.TorznabLiveWait) * time.Second
		if wait <= 0 {
			wait = 5 * time.Second
		}
		if wait > timeout {
			wait = timeout
		}
		liveCtx, cancel := context.WithTimeout(ctx, wait)
		liveResult, liveErr := s.liveSearch(liveCtx, indexer.LiveQuery{
			Mode:       mode,
			Query:      filter.Query,
			Season:     filter.Season,
			Episode:    filter.Episode,
			Year:       filter.Year,
			IMDbID:     filter.IMDbID,
			TMDbID:     filter.TMDbID,
			TVDbID:     filter.TVDbID,
			Categories: standardCategoryIDs(filter.Categories),
			Limit:      filter.Limit,
			IndexerIDs: filter.IndexerIDs,
		})
		cancel()
		out.Live = liveResult.Indexers
		if liveErr != nil {
			// The manager redacts connector errors; the app search row keeps a
			// stable message so a URL or key can never reach it.
			out.Err = "live search failed"
			s.log.Warn("torznab: live search", "component", "indexer", "subsystem", "torznab", "error", liveErr)
		}
		if liveResult.Stored > 0 {
			releases, total, err = s.store.SearchReleases(ctx, filter)
			if err != nil {
				s.log.Error("torznab: search after live", "component", "indexarr", "subsystem", "torznab", "error", err)
				return s.errorOutcome(http.StatusInternalServerError, torznabInternalError, "internal error")
			}
		}
	}
	// Provenance is fetched once for the whole page: writeItem never queries
	// the store, so a page of N results stays at a constant query count.
	out.Provenance = s.pageProvenance(ctx, releases)
	out.Releases = releases
	out.Total = total
	out.Body = s.searchXML(base, settings, apiKey, releases, total, filter.Offset, out.Provenance)
	return out
}

// liveQueryEligible decides whether a Torznab function is a real search or an
// RSS request. The *arr RSS calls t=search or t=tvsearch without q: they read
// the cache of the indexers that publish a latest list, and the fan-out must
// never invent a query for them.
func liveQueryEligible(mode string, filter store.Filter) bool {
	switch mode {
	case "search", "tvsearch":
		return strings.TrimSpace(filter.Query) != ""
	case "movie":
		return strings.TrimSpace(filter.Query) != "" ||
			strings.TrimSpace(filter.IMDbID) != "" ||
			strings.TrimSpace(filter.TMDbID) != ""
	case "music", "book":
		return strings.TrimSpace(filter.Query) != ""
	default:
		return false
	}
}

// intersectIndexerIDs keeps the request selection when present, otherwise the
// full allowed set. An empty selection means "all indexers" in the store, so
// the caller must check the length before querying.
func intersectIndexerIDs(selected, allowed []int64) []int64 {
	allowedSet := make(map[int64]struct{}, len(allowed))
	for _, id := range allowed {
		allowedSet[id] = struct{}{}
	}
	if len(selected) == 0 {
		out := make([]int64, 0, len(allowed))
		for _, id := range allowed {
			out = append(out, id)
		}
		return out
	}
	out := make([]int64, 0, len(selected))
	for _, id := range selected {
		if _, ok := allowedSet[id]; ok {
			out = append(out, id)
		}
	}
	return out
}

func standardCategoryIDs(values []string) []int {
	if len(values) == 0 {
		return nil
	}
	out := make([]int, 0, len(values))
	for _, value := range values {
		id, err := strconv.Atoi(strings.TrimSpace(value))
		if err == nil && id > 0 {
			out = append(out, id)
		}
	}
	return out
}

func (s *Server) errorOutcome(status, code int, message string) searchOutcome {
	return searchOutcome{Status: status, Body: torznabErrorBody(code, message), Err: message}
}

func selectionLists(selection releaseSelection) ([]int64, []string) {
	if len(selection.ids) == 0 {
		return nil, nil
	}
	ids := make([]int64, 0, len(selection.ids))
	for id := range selection.ids {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	names := make([]string, 0, len(ids))
	for _, id := range ids {
		names = append(names, selection.names[id])
	}
	return ids, names
}

func (s *Server) writeSearchOutcome(w http.ResponseWriter, out searchOutcome) {
	status := out.Status
	if status == 0 {
		status = http.StatusOK
	}
	body := out.Body
	if body == nil {
		body = torznabErrorBody(torznabInternalError, "internal error")
		status = http.StatusInternalServerError
	}
	writeXMLBody(w, status, body)
}

// TryRequest is one "Try as Sonarr/Radarr" request. APIKey is optional: when
// empty the server uses the primary Torznab key, which is what a fresh app
// configuration would use.
type TryRequest struct {
	IndexerSlug string
	Mode        string
	Query       string
	Season      int
	Episode     int
	IMDbID      string
	TVDbID      string
	TMDbID      string
	Limit       int
	APIKey      string
}

// TryResult is the exact XML and the rows behind it.
type TryResult struct {
	Status     int
	XML        []byte
	Releases   []store.Release
	Total      int64
	Provenance map[int64]store.ReleaseProvenance
	Duration   time.Duration
	Err        string
}

// Try runs the same code as /api and returns the document a Torznab client
// would receive. It never changes the external contract: the HTTP handler
// calls runSearch too.
func (s *Server) Try(ctx context.Context, base string, req TryRequest) (TryResult, error) {
	settings, err := s.store.LoadSettings(ctx)
	if err != nil {
		return TryResult{}, fmt.Errorf("try: load settings: %w", err)
	}
	mode := strings.ToLower(strings.TrimSpace(req.Mode))
	if mode == "" {
		mode = "search"
	}
	switch mode {
	case "search", "tvsearch", "movie", "music", "book":
	default:
		return TryResult{}, fmt.Errorf("try: unsupported function %q", mode)
	}
	query := url.Values{}
	query.Set("t", mode)
	if value := strings.TrimSpace(req.Query); value != "" {
		query.Set("q", value)
	}
	if req.Season > 0 {
		query.Set("season", strconv.Itoa(req.Season))
	}
	if req.Episode > 0 {
		query.Set("ep", strconv.Itoa(req.Episode))
	}
	if value := strings.TrimSpace(req.IMDbID); value != "" {
		query.Set("imdbid", value)
	}
	if value := strings.TrimSpace(req.TVDbID); value != "" {
		query.Set("tvdbid", value)
	}
	if value := strings.TrimSpace(req.TMDbID); value != "" {
		query.Set("tmdbid", value)
	}
	if req.Limit > 0 {
		query.Set("limit", strconv.Itoa(req.Limit))
	}
	apiKey := strings.TrimSpace(req.APIKey)
	if apiKey == "" {
		apiKey = settings.APIKey
	}
	started := time.Now()
	out := s.runSearch(ctx, req.IndexerSlug, query, settings, apiKey, base, mode)
	return TryResult{
		Status:     out.Status,
		XML:        out.Body,
		Releases:   out.Releases,
		Total:      out.Total,
		Provenance: out.Provenance,
		Duration:   time.Since(started),
		Err:        out.Err,
	}, nil
}

// authorize checks the primary key and every additional key. It returns the
// key the client used so .torrent links keep working with that same key.
func (s *Server) authorize(w http.ResponseWriter, r *http.Request, settings store.Settings) (string, bool) {
	if settings.APIKey == "" {
		writeTorznabError(w, http.StatusServiceUnavailable, torznabInsufficientPrivileges, "API key not configured")
		return "", false
	}
	provided := strings.TrimSpace(r.URL.Query().Get("apikey"))
	if provided == "" {
		provided = strings.TrimSpace(r.Header.Get("X-Api-Key"))
	}
	if provided == "" {
		writeTorznabError(w, http.StatusUnauthorized, torznabIncorrectAPIKey, "invalid API key")
		return "", false
	}
	primary := subtle.ConstantTimeCompare([]byte(provided), []byte(settings.APIKey)) == 1
	var additional store.APIKey
	if !primary {
		key, found, err := s.store.LookupAPIKey(r.Context(), provided)
		if err != nil {
			s.log.Error("torznab: lookup API key", "component", "indexarr", "subsystem", "torznab", "error", err)
			writeTorznabError(w, http.StatusInternalServerError, torznabInternalError, "internal error")
			return "", false
		}
		if !found {
			writeTorznabError(w, http.StatusUnauthorized, torznabIncorrectAPIKey, "invalid API key")
			return "", false
		}
		additional = key
	}
	if settings.APIRateLimit > 0 && !s.limits.allow(provided, settings.APIRateLimit, time.Now()) {
		w.Header().Set("Retry-After", "60")
		writeTorznabError(w, http.StatusTooManyRequests, torznabTooManyRequests, "rate limit exceeded")
		return "", false
	}
	if !primary {
		if err := s.store.TouchAPIKey(r.Context(), additional.ID); err != nil {
			s.log.Warn("torznab: update API key usage", "component", "indexarr", "subsystem", "torznab", "error", err)
		}
	}
	return provided, true
}

func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	settings, err := s.store.LoadSettings(r.Context())
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if _, ok := s.authorize(w, r, settings); !ok {
		return
	}
	name := strings.TrimPrefix(r.URL.Path, pathDownload)
	extension := ""
	lower := strings.ToLower(name)
	switch {
	case strings.HasSuffix(lower, ".torrent"):
		extension = ".torrent"
		name = name[:len(name)-len(".torrent")]
	case strings.HasSuffix(lower, ".nzb"):
		extension = ".nzb"
		name = name[:len(name)-len(".nzb")]
	}
	id, err := strconv.ParseInt(strings.TrimSpace(name), 10, 64)
	if err != nil || id <= 0 {
		s.writeError(w, http.StatusBadRequest, "invalid release id")
		return
	}
	s.serveReleaseByID(w, r, id, extension)
}

func (s *Server) serveReleaseByID(w http.ResponseWriter, r *http.Request, id int64, extension string) {
	release, err := s.store.GetRelease(r.Context(), id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeTorznabError(w, http.StatusNotFound, torznabIncorrectParameter, "release not found")
			return
		}
		s.log.Error("torznab: get release", "component", "indexarr", "subsystem", "torznab", "error", err)
		writeTorznabError(w, http.StatusInternalServerError, torznabInternalError, "internal error")
		return
	}
	s.serveRelease(w, r, release, extension)
}

func (s *Server) serveRelease(w http.ResponseWriter, r *http.Request, release store.Release, extension string) {
	usenet := isUsenetRelease(release)
	switch extension {
	case ".nzb":
		usenet = true
	case ".torrent":
		usenet = false
	}
	body, contentType, filename := s.releasePayload(r.Context(), release, usenet)
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(string(body))), "magnet:") {
		http.Redirect(w, r, strings.TrimSpace(string(body)), http.StatusFound)
		return
	}
	if len(body) == 0 {
		if usenet {
			s.writeError(w, http.StatusNotFound, "no .nzb stored for this release")
		} else {
			s.writeError(w, http.StatusNotFound, "no .torrent stored for this release; use the magnet link")
		}
		return
	}

	// A grab is a payload delivery: resolve the indexer that supplied the
	// release and apply its daily grab budget before writing anything.
	var grabIndexerID int64
	if s.grabCheck != nil || s.grabRecord != nil {
		indexer, found, err := s.store.IndexerForRelease(r.Context(), release.ID)
		if err != nil {
			s.log.Warn("torznab: resolve grab indexer", "component", "indexarr",
				"subsystem", "torznab", "release_id", release.ID)
		} else if found {
			grabIndexerID = indexer.ID
			if s.grabCheck != nil {
				if err := s.grabCheck(r.Context(), indexer.ID); err != nil {
					w.Header().Set("Retry-After", "3600")
					s.writeError(w, http.StatusTooManyRequests, "grab limit reached")
					return
				}
			}
		}
	}

	if strings.TrimSpace(contentType) == "" {
		contentType = "application/x-bittorrent"
		if usenet {
			contentType = "application/x-nzb"
		}
	}
	if strings.TrimSpace(filename) == "" {
		if usenet {
			filename = NZBFilename(release.Title)
		} else {
			filename = TorrentFilename(release.Title)
		}
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", `attachment; filename="`+headerFilename(filename)+`"`)
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Method == http.MethodHead {
		return
	}
	if _, err := w.Write(body); err != nil {
		s.log.Debug("torznab: write payload", "error", err)
		return
	}
	// A served payload is a download: stored-source retention protects it.
	markCtx := context.WithoutCancel(r.Context())
	if err := s.store.MarkReleaseDownloaded(markCtx, release.ID); err != nil {
		s.log.Warn("torznab: mark downloaded", "component", "indexarr",
			"subsystem", "torznab", "release_id", release.ID, "error", err)
	}
	if grabIndexerID > 0 && s.grabRecord != nil {
		recordCtx := context.WithoutCancel(r.Context())
		if err := s.grabRecord(recordCtx, grabIndexerID, release.ID, release.Title); err != nil {
			s.log.Warn("torznab: record grab", "component", "indexarr",
				"subsystem", "torznab", "indexer_id", grabIndexerID,
				"release_id", release.ID)
		}
	}
}

// releasePayload returns the stored bytes or asks the download resolver for
// them. The resolver is only consulted when no matching payload is cached.
func (s *Server) releasePayload(ctx context.Context, release store.Release, usenet bool) ([]byte, string, string) {
	if usenet {
		if len(release.Payload) > 0 {
			return release.Payload, release.PayloadType, NZBFilename(release.Title)
		}
	} else {
		if len(release.Torrent) > 0 {
			return release.Torrent, "application/x-bittorrent", TorrentFilename(release.Title)
		}
		if len(release.Payload) > 0 && !isUsenetRelease(release) {
			return release.Payload, release.PayloadType, TorrentFilename(release.Title)
		}
	}
	if s.downloadResolver == nil {
		return nil, "", ""
	}
	body, contentType, filename, err := s.downloadResolver(ctx, release.ID)
	if err != nil {
		s.log.Warn("torznab: resolve release payload", "release_id", release.ID, "error", err)
		return nil, "", ""
	}
	if len(body) == 0 {
		return nil, "", ""
	}
	return body, contentType, filename
}

func isUsenetRelease(release store.Release) bool {
	if strings.EqualFold(strings.TrimSpace(release.Protocol), "usenet") {
		return true
	}
	return strings.Contains(strings.ToLower(release.PayloadType), "nzb")
}

// capsModes is the ordered list of Torznab search functions and their XML
// element names.
var capsModes = []struct {
	mode engine.Mode
	tag  string
}{
	{engine.ModeSearch, "search"},
	{engine.ModeTV, "tv-search"},
	{engine.ModeMovie, "movie-search"},
	{engine.ModeMusic, "music-search"},
	{engine.ModeBook, "book-search"},
}

// mergeCapabilities intersects the parameters of every indexer that supports a
// mode, so the aggregate caps document never promises an ID that only one
// private site understands. Categories are the union of the selected indexers.
func mergeCapabilities(byID map[int64]engine.Capabilities) engine.Capabilities {
	ids := make([]int64, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	var list []engine.Capabilities
	for _, id := range ids {
		list = append(list, byID[id])
	}
	if len(list) == 0 {
		return engine.Capabilities{}
	}
	out := engine.Capabilities{Params: make(map[engine.Mode][]string)}
	seenCategories := make(map[int]struct{})
	for _, caps := range list {
		for _, category := range caps.Categories {
			if _, ok := seenCategories[category]; ok {
				continue
			}
			seenCategories[category] = struct{}{}
			out.Categories = append(out.Categories, category)
		}
	}
	sort.Ints(out.Categories)
	for _, definition := range capsModes {
		var common []string
		found := false
		for _, caps := range list {
			if !caps.Supports(definition.mode) {
				continue
			}
			params := append([]string(nil), caps.Params[definition.mode]...)
			if !found {
				common = params
				found = true
				continue
			}
			common = intersectParams(common, params)
		}
		if !found {
			continue
		}
		out.Modes = append(out.Modes, definition.mode)
		out.Params[definition.mode] = common
	}
	return out
}

func intersectParams(left, right []string) []string {
	rightSet := make(map[string]struct{}, len(right))
	for _, param := range right {
		rightSet[param] = struct{}{}
	}
	out := make([]string, 0, len(left))
	for _, param := range left {
		if _, ok := rightSet[param]; ok {
			out = append(out, param)
		}
	}
	return out
}

// torznabCapabilities reflects what indexarr itself can accept, not only what
// the remote site declares. The TV fan-out turns season/ep into the SxxEyy
// keyword for definitions that only declare q, so both parameters are safe to
// advertise for every TV-capable connector. IDs remain untouched: they are
// advertised only when the underlying definitions really support them.
func torznabCapabilities(caps engine.Capabilities) engine.Capabilities {
	if caps.Params == nil {
		caps.Params = make(map[engine.Mode][]string)
	}
	// A definition that only declares a plain keyword search can still answer
	// TV and movie queries: indexarr maps them to the same q parameter (the TV
	// fan-out additionally turns season/ep into the SxxEyy keyword). Without
	// this, Sonarr/Radarr decide "No RSS feed query available" for every
	// search-only site and refuse to add its per-site indexer (Blocco C).
	searchQuery := caps.Supports(engine.ModeSearch)
	if searchQuery {
		hasQuery := false
		for _, param := range caps.Params[engine.ModeSearch] {
			if param == engine.ParamQuery {
				hasQuery = true
				break
			}
		}
		searchQuery = hasQuery
	}
	if searchQuery {
		for _, mode := range []engine.Mode{engine.ModeTV, engine.ModeMovie} {
			if caps.Supports(mode) {
				continue
			}
			caps.Modes = append(caps.Modes, mode)
			caps.Params[mode] = []string{engine.ParamQuery}
		}
	}
	if !caps.Supports(engine.ModeTV) {
		return caps
	}
	hasQuery := false
	for _, param := range caps.Params[engine.ModeTV] {
		if param == engine.ParamQuery {
			hasQuery = true
			break
		}
	}
	if !hasQuery {
		return caps
	}
	params := []string{engine.ParamQuery, engine.ParamSeason, engine.ParamEpisode}
	for _, param := range caps.Params[engine.ModeTV] {
		switch param {
		case engine.ParamQuery, engine.ParamSeason, engine.ParamEpisode:
			continue
		default:
			params = append(params, param)
		}
	}
	caps.Params[engine.ModeTV] = params
	return caps
}

func (s *Server) writeCaps(w http.ResponseWriter, settings store.Settings, caps engine.Capabilities, haveCaps bool) {
	var b bytes.Buffer
	b.Grow(4096)
	b.WriteString(xml.Header)
	b.WriteString(`<caps>`)
	b.WriteString(`<server version="1.0" title="`)
	writeEsc(&b, "indexarr "+s.version)
	b.WriteString(`"/>`)
	b.WriteString(`<limits max="100" default="50"/>`)
	b.WriteString(`<registration available="no" open="no"/>`)
	b.WriteString(`<searching>`)
	if !haveCaps || len(caps.Modes) == 0 {
		// Fresh installation or a definition that could not be built: keep the
		// historical document so app setup is never blocked by one indexer.
		caps = legacyCapabilities()
	}
	caps = torznabCapabilities(caps)
	for _, definition := range capsModes {
		params := caps.Params[definition.mode]
		if !caps.Supports(definition.mode) {
			b.WriteString(`<` + definition.tag + ` available="no"/>`)
			continue
		}
		b.WriteString(`<` + definition.tag + ` available="yes" supportedParams="`)
		writeEsc(&b, strings.Join(params, ","))
		b.WriteString(`"/>`)
	}
	b.WriteString(`</searching><categories>`)
	writeCapsCategories(&b, caps.Categories)
	b.WriteString(`</categories></caps>`)
	s.writeXML(w, b.Bytes())
}

func legacyCapabilities() engine.Capabilities {
	return engine.Capabilities{
		Modes: []engine.Mode{engine.ModeSearch, engine.ModeTV, engine.ModeMovie, engine.ModeMusic, engine.ModeBook},
		Params: map[engine.Mode][]string{
			engine.ModeSearch: {engine.ParamQuery},
			engine.ModeTV:     {engine.ParamQuery, engine.ParamSeason, engine.ParamEpisode, engine.ParamIMDbID, engine.ParamTVDbID, engine.ParamTMDbID},
			engine.ModeMovie:  {engine.ParamQuery, engine.ParamIMDbID, engine.ParamTMDbID},
			engine.ModeMusic:  {engine.ParamQuery},
			engine.ModeBook:   {engine.ParamQuery},
		},
	}
}

func writeCapsCategories(b *bytes.Buffer, only []int) {
	selected := make(map[int]struct{}, len(only))
	for _, id := range only {
		selected[id] = struct{}{}
	}
	filter := len(selected) > 0
	for _, group := range categories.Groups() {
		_, topSelected := selected[group.Top.ID]
		if !filter {
			topSelected = true
		}
		children := make([]struct {
			id   int
			name string
		}, 0, len(group.Children))
		for _, child := range group.Children {
			if filter && !topSelected {
				if _, ok := selected[child.ID]; !ok {
					continue
				}
			}
			children = append(children, struct {
				id   int
				name string
			}{child.ID, child.Name})
		}
		if filter && !topSelected && len(children) == 0 {
			continue
		}
		b.WriteString(`<category id="`)
		writeInt(b, int64(group.Top.ID))
		b.WriteString(`" name="`)
		writeEsc(b, group.Top.Name)
		b.WriteString(`">`)
		for _, child := range children {
			b.WriteString(`<subcat id="`)
			writeInt(b, int64(child.id))
			b.WriteString(`" name="`)
			writeEsc(b, child.name)
			b.WriteString(`"/>`)
		}
		b.WriteString(`</category>`)
	}
}

// pageProvenance loads the aggregated provenance of one result page with a
// single store call. A failure degrades gracefully to the release row counters:
// the search response is still served.
func (s *Server) pageProvenance(ctx context.Context, releases []store.Release) map[int64]store.ReleaseProvenance {
	if len(releases) == 0 {
		return nil
	}
	ids := make([]int64, 0, len(releases))
	for _, release := range releases {
		if release.ID > 0 {
			ids = append(ids, release.ID)
		}
	}
	provenance, err := s.store.ReleaseProvenance(ctx, ids)
	if err != nil {
		s.log.Warn("torznab: load provenance", "component", "indexarr",
			"subsystem", "torznab", "error", err)
		return nil
	}
	return provenance
}

func (s *Server) searchXML(base string, settings store.Settings, apiKey string, releases []store.Release, total int64, offset int, provenance map[int64]store.ReleaseProvenance) []byte {
	var b bytes.Buffer
	// One item is roughly 1.3 KiB with a magnet and provenance attributes;
	// reserving the estimated size avoids the repeated slice growth of a 100
	// item response.
	b.Grow(2048 + len(releases)*1400)
	b.WriteString(xml.Header)
	b.WriteString(`<rss version="2.0" xmlns:torznab="http://torznab.com/schemas/2015/feed" xmlns:newznab="http://www.newznab.com/DTD/2010/feeds/attributes/">`)
	b.WriteString(`<channel>`)
	b.WriteString(`<title>indexarr</title>`)
	b.WriteString(`<link>`)
	writeEsc(&b, base)
	b.WriteString(`</link>`)
	b.WriteString(`<description>indexarr Torznab index</description>`)
	b.WriteString(`<newznab:response offset="`)
	writeInt(&b, int64(offset))
	b.WriteString(`" total="`)
	writeInt(&b, total)
	b.WriteString(`"/>`)
	key := url.QueryEscape(apiKey)
	for _, release := range releases {
		s.writeItem(&b, base, &settings, key, release, provenance[release.ID])
	}
	b.WriteString(`</channel></rss>`)
	return b.Bytes()
}

func (s *Server) writeItem(b *bytes.Buffer, base string, settings *store.Settings, key string, release store.Release, provenance store.ReleaseProvenance) {
	usenet := isUsenetRelease(release)
	id := strconv.FormatInt(release.ID, 10)
	link := ""
	switch {
	case len(release.Torrent) > 0:
		link = base + "/dl/" + id + ".torrent?apikey=" + key
	case len(release.Payload) > 0 && !usenet:
		link = base + "/dl/" + id + ".torrent?apikey=" + key
	case usenet:
		// Newznab clients grab through the same proxy; the resolver fetches an
		// opaque reference on first use.
		link = base + "/dl/" + id + ".nzb?apikey=" + key
	case release.Magnet != "":
		link = release.Magnet
	case provenance.HasDownloadRef:
		// The listing had no magnet: the sealed reference resolves the real
		// download only when the client grabs, so a live search never opens a
		// details page.
		link = base + "/dl/" + id + ".torrent?apikey=" + key
	}
	if link == "" {
		return
	}
	enclosureType := "application/x-bittorrent"
	if usenet {
		enclosureType = "application/x-nzb"
	}
	size := release.Size
	if size <= 0 {
		size = release.TorrentSize
	}
	category := release.Category
	if category == "" {
		category = "8000"
	}
	published := release.PublishedAt
	if published.IsZero() {
		published = release.FirstSeenAt
	}
	guid := release.InfoHash
	if guid == "" {
		guid = "indexarr-" + release.DedupKey
	}
	b.WriteString(`<item>`)
	writeTag(b, "title", release.Title)
	b.WriteString(`<guid isPermaLink="false">`)
	writeEsc(b, guid)
	b.WriteString(`</guid>`)
	writeTag(b, "link", link)
	b.WriteString(`<comments>`)
	writeEsc(b, base)
	b.WriteString(`/releases/`)
	b.WriteString(id)
	b.WriteString(`</comments>`)
	b.WriteString(`<pubDate>`)
	b.WriteString(published.Format(time.RFC1123Z))
	b.WriteString(`</pubDate>`)
	writeIntTag(b, "size", size)
	writeTag(b, "category", category)
	b.WriteString(`<enclosure url="`)
	writeEsc(b, link)
	b.WriteString(`" length="`)
	writeInt(b, size)
	b.WriteString(`" type="`)
	b.WriteString(enclosureType)
	b.WriteString(`"/>`)
	writeAttr(b, "category", category)
	writeAttrInt(b, "size", size)
	// The release row is the fallback; provenance can be fresher because a
	// single indexer may have reported a bigger swarm.
	seeders, leechers := release.Seeders, release.Leechers
	if provenance.Seeders > seeders {
		seeders = provenance.Seeders
	}
	if provenance.Leechers > leechers {
		leechers = provenance.Leechers
	}
	if seeders >= 0 {
		writeAttrInt(b, "seeders", seeders)
	}
	if leechers >= 0 {
		writeAttrInt(b, "leechers", leechers)
	}
	if seeders >= 0 && leechers >= 0 {
		writeAttrInt(b, "peers", seeders+leechers)
	}
	if provenance.Indexers > 1 {
		// Additive attributes: *arr clients ignore unknown names, while a
		// provenance-aware client can prefer a release seen by many indexers.
		// download_ref and tracker URLs are never part of this output.
		writeAttrInt(b, "indexers", int64(provenance.Indexers))
		writeAttr(b, "provenance", strings.Join(provenance.Names, "|"))
	}
	if release.InfoHash != "" {
		writeAttr(b, "infohash", release.InfoHash)
	}
	if !usenet && release.Magnet != "" {
		writeAttr(b, "magneturl", release.Magnet)
	}
	if usenet {
		writeAttr(b, "protocol", "usenet")
	}
	if !published.IsZero() {
		b.WriteString(`<torznab:attr name="publishdate" value="`)
		b.WriteString(published.Format(time.RFC3339))
		b.WriteString(`"/>`)
	}
	writeAttrInt(b, "downloadvolumefactor", int64(settings.TorznabDownloadFactor))
	writeAttrInt(b, "uploadvolumefactor", int64(settings.TorznabUploadFactor))
	b.WriteString(`</item>`)
}

func (s *Server) writeXML(w http.ResponseWriter, body []byte) {
	writeXMLBody(w, http.StatusOK, body)
}

// writeXMLBody writes a complete Torznab document with an explicit status.
// Search errors used to call writeTorznabError directly; going through one
// writer keeps the headers identical for /api and Try.
func writeXMLBody(w http.ResponseWriter, status int, body []byte) {
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// torznabErrorBody is the error document without any header side effect.
func torznabErrorBody(code int, message string) []byte {
	var b bytes.Buffer
	b.WriteString(xml.Header)
	fmt.Fprintf(&b, `<error code="%d" description="%s"/>`, code, esc(message))
	return b.Bytes()
}

// Torznab/Newznab protocol error codes.
const (
	torznabIncorrectAPIKey        = 100
	torznabInsufficientPrivileges = 101
	torznabMissingParameter       = 200
	torznabIncorrectParameter     = 201
	torznabNoSuchFunction         = 300
	torznabTooManyRequests        = 400
	torznabInternalError          = 500
	torznabUnknownError           = 900
)

// torznabError maps the semantic failure names used by the server to their
// protocol codes.
var torznabError = map[string]int{
	"incorrect_api_key":       torznabIncorrectAPIKey,
	"insufficient_privileges": torznabInsufficientPrivileges,
	"missing_parameter":       torznabMissingParameter,
	"incorrect_parameter":     torznabIncorrectParameter,
	"no_such_function":        torznabNoSuchFunction,
	"too_many_requests":       torznabTooManyRequests,
	"internal_error":          torznabInternalError,
	"unknown_error":           torznabUnknownError,
}

func torznabCodeForStatus(status int) int {
	switch status {
	case http.StatusUnauthorized:
		return torznabError["incorrect_api_key"]
	case http.StatusForbidden:
		return torznabError["insufficient_privileges"]
	case http.StatusBadRequest, http.StatusNotFound:
		return torznabError["incorrect_parameter"]
	case http.StatusTooManyRequests:
		return torznabError["too_many_requests"]
	case http.StatusInternalServerError:
		return torznabError["internal_error"]
	default:
		return torznabError["unknown_error"]
	}
}

// writeTorznabError writes the standard Torznab XML error document while
// keeping the HTTP status the endpoint has always used.
func writeTorznabError(w http.ResponseWriter, httpStatus int, code int, message string) {
	writeXMLBody(w, httpStatus, torznabErrorBody(code, message))
}

// writeError is kept for the /dl proxy and maps the HTTP status to the closest
// Torznab code.
func (s *Server) writeError(w http.ResponseWriter, status int, message string) {
	writeTorznabError(w, status, torznabCodeForStatus(status), message)
}

func (s *Server) baseURL(r *http.Request, settings store.Settings) string {
	return BaseURL(r, settings)
}

// BaseURL returns the absolute URL indexarr is reachable at, preferring the
// configured public_url and falling back to the request (honouring the usual
// reverse-proxy headers).
func BaseURL(r *http.Request, settings store.Settings) string {
	if settings.PublicURL != "" {
		return strings.TrimRight(settings.PublicURL, "/")
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if forwarded := r.Header.Get("X-Forwarded-Proto"); forwarded != "" {
		scheme = strings.TrimSpace(strings.Split(forwarded, ",")[0])
	}
	host := r.Host
	if forwarded := r.Header.Get("X-Forwarded-Host"); forwarded != "" {
		host = strings.TrimSpace(strings.Split(forwarded, ",")[0])
	}
	return scheme + "://" + host
}

func esc(text string) string {
	var b bytes.Buffer
	writeEsc(&b, text)
	return b.String()
}

// writeEsc escapes XML text and double-quoted attribute values directly into
// b. It matches xml.EscapeText byte for byte, but avoids the temporary buffer
// and byte slice that xml.EscapeText needs for every attribute. Invalid UTF-8
// falls back to the standard library, which substitutes U+FFFD.
func writeEsc(b *bytes.Buffer, text string) {
	if !utf8.ValidString(text) {
		_ = xml.EscapeText(b, []byte(text))
		return
	}
	last := 0
	for i := 0; i < len(text); i++ {
		var replacement string
		switch text[i] {
		case '&':
			replacement = "&amp;"
		case '<':
			replacement = "&lt;"
		case '>':
			replacement = "&gt;"
		case '"':
			replacement = "&#34;"
		case '\'':
			replacement = "&#39;"
		case '\t':
			replacement = "&#x9;"
		case '\n':
			replacement = "&#xA;"
		case '\r':
			replacement = "&#xD;"
		default:
			continue
		}
		b.WriteString(text[last:i])
		b.WriteString(replacement)
		last = i + 1
	}
	b.WriteString(text[last:])
}

func writeTag(b *bytes.Buffer, name, text string) {
	b.WriteByte('<')
	b.WriteString(name)
	b.WriteByte('>')
	writeEsc(b, text)
	b.WriteString(`</`)
	b.WriteString(name)
	b.WriteByte('>')
}

func writeIntTag(b *bytes.Buffer, name string, value int64) {
	b.WriteByte('<')
	b.WriteString(name)
	b.WriteByte('>')
	writeInt(b, value)
	b.WriteString(`</`)
	b.WriteString(name)
	b.WriteByte('>')
}

// writeInt appends a decimal integer without allocating.
func writeInt(b *bytes.Buffer, value int64) {
	var scratch [20]byte
	b.Write(strconv.AppendInt(scratch[:0], value, 10))
}

func writeAttr(b *bytes.Buffer, name, value string) {
	b.WriteString(`<torznab:attr name="`)
	b.WriteString(name)
	b.WriteString(`" value="`)
	writeEsc(b, value)
	b.WriteString(`"/>`)
}

func writeAttrInt(b *bytes.Buffer, name string, value int64) {
	b.WriteString(`<torznab:attr name="`)
	b.WriteString(name)
	b.WriteString(`" value="`)
	writeInt(b, value)
	b.WriteString(`"/>`)
}

func parseLimit(raw string) int {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value <= 0 {
		return defaultLimit
	}
	if value > maxResults {
		return maxResults
	}
	return value
}

func parseOffset(raw string) int {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value < 0 {
		return 0
	}
	return value
}

func positiveInt(raw string) int {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value <= 0 {
		return 0
	}
	return value
}

func splitCategories(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		out = append(out, part)
	}
	return out
}

// TorrentFilename builds a safe download name from a release title.
func TorrentFilename(title string) string {
	return safeFilename(title, ".torrent")
}

// NZBFilename builds a safe Newznab download name from a release title.
func NZBFilename(title string) string {
	return safeFilename(title, ".nzb")
}

func safeFilename(title, extension string) string {
	var b strings.Builder
	for _, r := range title {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '.' || r == '-' || r == '_' || r == ' ':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	name := strings.TrimSpace(b.String())
	if name == "" {
		name = "release"
	}
	runes := []rune(name)
	if len(runes) > 100 {
		name = string(runes[:100])
	}
	return name + extension
}

// headerFilename keeps a resolver-provided name safe for Content-Disposition.
func headerFilename(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "release"
	}
	name = strings.Map(func(r rune) rune {
		switch r {
		case '"', '\r', '\n', '\\', '/':
			return '_'
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, name)
	if name == "" {
		return "release"
	}
	return name
}

// writeIndexersAnswer implements the Jackett-compatible t=indexers list used by
// gextto to build one lane per site. The XML intentionally contains only
// public settings: no API key and no secret.
func (s *Server) writeIndexersAnswer(w http.ResponseWriter, r *http.Request, query url.Values, settings store.Settings) {
	configuredOnly := strings.EqualFold(strings.TrimSpace(query.Get("configured")), "true")
	indexers, err := s.store.ListIndexers(r.Context())
	if err != nil {
		writeTorznabError(w, http.StatusInternalServerError, torznabInternalError, "internal error")
		return
	}
	capsByID := map[int64]engine.Capabilities{}
	if s.indexerCaps != nil {
		if caps, err := s.indexerCaps(r.Context(), nil); err == nil {
			capsByID = caps
		}
	}
	base := strings.TrimSuffix(s.baseURL(r, settings), "/")
	var b bytes.Buffer
	b.WriteString(xml.Header)
	b.WriteString("<indexers>")
	for _, indexer := range indexers {
		if configuredOnly && !indexer.Enabled {
			continue
		}
		if configuredOnly {
			state, stateErr := s.store.GetIndexerState(r.Context(), indexer.ID)
			if stateErr == nil && (strings.TrimSpace(state.LastError) != "" || strings.EqualFold(strings.TrimSpace(state.Circuit), "open")) {
				continue
			}
			if s.indexerCaps != nil && len(capsByID) > 0 {
				if _, ok := capsByID[indexer.ID]; !ok {
					continue
				}
			}
		}
		parsed, _ := s.definitionForIndexer(r.Context(), indexer)
		name := indexer.Name
		description, language, kind := "", "", "private"
		if parsed != nil {
			name = parsed.Meta.Name
			description = parsed.Meta.Description
			language = parsed.Meta.Language
			kind = parsed.Meta.Type
		}
		b.WriteString(`<indexer id="`)
		writeEsc(&b, indexer.Slug)
		b.WriteString(`" configured="`)
		if indexer.Enabled {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
		b.WriteString(`">`)
		b.WriteString("<title>")
		writeEsc(&b, name)
		b.WriteString("</title><description>")
		writeEsc(&b, description)
		b.WriteString("</description><link>")
		writeEsc(&b, base+"/api/v2.0/indexers/"+url.PathEscape(indexer.Slug)+"/results/torznab/api")
		b.WriteString("</link><language>")
		writeEsc(&b, language)
		b.WriteString("</language><type>")
		writeEsc(&b, kind)
		b.WriteString("</type>")
		caps, ok := capsByID[indexer.ID]
		if !ok {
			caps = legacyCapabilities()
		}
		writeIndexerCaps(&b, caps)
		b.WriteString("</indexer>")
	}
	b.WriteString("</indexers>")
	s.writeXML(w, b.Bytes())
}

func (s *Server) definitionForIndexer(ctx context.Context, indexer store.Indexer) (*definition.Definition, error) {
	def, err := s.store.GetDefinition(ctx, indexer.DefinitionID)
	if err != nil {
		return nil, err
	}
	return definition.Parse([]byte(def.Body))
}

func writeIndexerCaps(b *bytes.Buffer, caps engine.Capabilities) {
	b.WriteString("<caps><searching>")
	for _, definition := range capsModes {
		if !caps.Supports(definition.mode) {
			b.WriteString(`<` + definition.tag + ` available="no"/>`)
			continue
		}
		b.WriteString(`<` + definition.tag + ` available="yes" supportedParams="`)
		writeEsc(b, strings.Join(caps.Params[definition.mode], ","))
		b.WriteString(`"/>`)
	}
	b.WriteString("</searching></caps>")
}
