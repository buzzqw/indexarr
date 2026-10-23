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
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/buzzqw/indexarr/internal/categories"
	"github.com/buzzqw/indexarr/internal/store"
)

// Server implements the Torznab endpoint and the .torrent download route.
type Server struct {
	store      *store.Store
	log        *slog.Logger
	version    string
	started    time.Time
	limits     *minuteLimiter
	liveSearch func(context.Context, store.Filter) (int, error)

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

// SetLiveSearch installs the callback used when the cache has no results.
func (s *Server) SetLiveSearch(fn func(context.Context, store.Filter) (int, error)) {
	s.liveSearch = fn
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
		s.writeCaps(w, settings)
		return
	}
	apiKey, ok := s.authorize(w, r, settings)
	if !ok {
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

	var selection releaseSelection
	var sqlSourceID int64
	if aggregate {
		filters := parseAggregateFilters(query)
		selection, err = s.resolveAggregateSelection(r.Context(), filters)
		if err != nil {
			s.log.Error("torznab: resolve filters", "component", "indexarr", "subsystem", "torznab", "error", err)
			writeTorznabError(w, http.StatusInternalServerError, torznabInternalError, "internal error")
			return
		}
	} else {
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
		selection = selectionForIndexer(indexer)
		sqlSourceID = indexer.LegacySourceID
	}
	if !selection.all && sqlSourceID == 0 && len(selection.ids) == 1 && len(selection.names) == 0 {
		for id := range selection.ids {
			sqlSourceID = id
		}
	}

	filter := store.Filter{
		Query:   strings.TrimSpace(query.Get("q")),
		Limit:   parseLimit(query.Get("limit")),
		Offset:  parseOffset(query.Get("offset")),
		Sort:    "recent",
		Season:  positiveInt(query.Get("season")),
		Episode: positiveInt(query.Get("ep")),
		IMDbID:  query.Get("imdbid"),
		TMDbID:  query.Get("tmdbid"),
		TVDbID:  query.Get("tvdbid"),
	}
	if rawCategories := strings.TrimSpace(query.Get("cat")); rawCategories != "" {
		filter.Categories = splitCategories(rawCategories)
	}
	base := s.baseURL(r, settings)
	if selection.empty() {
		s.writeSearch(w, base, settings, apiKey, nil, 0, filter.Offset, nil)
		return
	}
	if sqlSourceID > 0 {
		filter.SourceID = sqlSourceID
	} else if len(selection.ids) > 0 {
		filter.SourceIDs = make([]int64, 0, len(selection.ids))
		for id := range selection.ids {
			filter.SourceIDs = append(filter.SourceIDs, id)
		}
	}
	releases, total, err := s.store.SearchReleases(r.Context(), filter)
	if err != nil {
		s.log.Error("torznab: search", "component", "indexarr", "subsystem", "torznab", "error", err)
		writeTorznabError(w, http.StatusInternalServerError, torznabInternalError, "internal error")
		return
	}
	releases, total = applySelection(selection, sqlSourceID, releases, total)
	if total == 0 && settings.TorznabLiveSearch && s.liveSearch != nil &&
		strings.TrimSpace(filter.Query) != "" {
		timeout := time.Duration(settings.TorznabLiveTimeout) * time.Second
		if timeout <= 0 {
			timeout = 20 * time.Second
		}
		liveCtx, cancel := context.WithTimeout(r.Context(), timeout)
		stored, liveErr := s.liveSearch(liveCtx, filter)
		cancel()
		if liveErr != nil {
			s.log.Warn("torznab: live search", "component", "indexer", "subsystem", "torznab", "error", liveErr)
		}
		if stored > 0 {
			releases, total, err = s.store.SearchReleases(r.Context(), filter)
			if err != nil {
				s.log.Error("torznab: search after live", "component", "indexarr", "subsystem", "torznab", "error", err)
				writeTorznabError(w, http.StatusInternalServerError, torznabInternalError, "internal error")
				return
			}
			releases, total = applySelection(selection, sqlSourceID, releases, total)
		}
	}
	// Provenance is fetched once for the whole page: writeItem never queries
	// the store, so a page of N results stays at a constant query count.
	provenance := s.pageProvenance(r.Context(), releases)
	s.writeSearch(w, base, settings, apiKey, releases, total, filter.Offset, provenance)
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
	if err != nil || len(body) == 0 {
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

func (s *Server) writeCaps(w http.ResponseWriter, settings store.Settings) {
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
	b.WriteString(`<search available="yes" supportedParams="q"/>`)
	b.WriteString(`<tv-search available="yes" supportedParams="q,season,ep,imdbid,tvdbid,tmdbid"/>`)
	b.WriteString(`<movie-search available="yes" supportedParams="q,imdbid,tmdbid"/>`)
	b.WriteString(`<music-search available="yes" supportedParams="q"/>`)
	b.WriteString(`<book-search available="yes" supportedParams="q"/>`)
	b.WriteString(`</searching><categories>`)
	for _, group := range categories.Groups() {
		b.WriteString(`<category id="`)
		writeInt(&b, int64(group.Top.ID))
		b.WriteString(`" name="`)
		writeEsc(&b, group.Top.Name)
		b.WriteString(`">`)
		for _, child := range group.Children {
			b.WriteString(`<subcat id="`)
			writeInt(&b, int64(child.ID))
			b.WriteString(`" name="`)
			writeEsc(&b, child.Name)
			b.WriteString(`"/>`)
		}
		b.WriteString(`</category>`)
	}
	b.WriteString(`</categories></caps>`)
	s.writeXML(w, b.Bytes())
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

func (s *Server) writeSearch(w http.ResponseWriter, base string, settings store.Settings, apiKey string, releases []store.Release, total int64, offset int, provenance map[int64]store.ReleaseProvenance) {
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
	s.writeXML(w, b.Bytes())
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
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(body); err != nil {
		s.log.Debug("torznab: write response", "error", err)
	}
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
	var b bytes.Buffer
	b.WriteString(xml.Header)
	fmt.Fprintf(&b, `<error code="%d" description="%s"/>`, code, esc(message))
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(httpStatus)
	_, _ = w.Write(b.Bytes())
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
