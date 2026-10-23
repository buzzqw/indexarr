// Package torznabapi exposes the cached releases through a Torznab server, so
// Sonarr, Radarr, Lidarr and any other Torznab client can query indexarr the
// same way they query Jackett or Prowlarr.
package torznabapi

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/xml"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

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
}

// SetLiveSearch installs the callback used when the cache has no results.
func (s *Server) SetLiveSearch(fn func(context.Context, store.Filter) (int, error)) {
	s.liveSearch = fn
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
	pathTorznab  = "/torznab/api"
	pathDownload = "/dl/"
	maxResults   = 100
	defaultLimit = 50
)

// ServeHTTP routes the Torznab API, its alias and the torrent downloads.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == pathAPI || r.URL.Path == pathTorznab:
		s.handleAPI(w, r)
	case strings.HasPrefix(r.URL.Path, pathDownload):
		s.handleDownload(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) handleAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		s.writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	settings, err := s.store.LoadSettings(r.Context())
	if err != nil {
		s.log.Error("torznab: load settings", "component", "indexarr", "subsystem", "torznab", "error", err)
		s.writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	query := r.URL.Query()
	function := strings.ToLower(strings.TrimSpace(query.Get("t")))
	if function == "" {
		function = "search"
	}
	if function == "caps" {
		s.writeCaps(w, settings)
		return
	}
	if function == "get" {
		if _, ok := s.authorize(w, r, settings); !ok {
			return
		}
		id, _ := strconv.ParseInt(query.Get("id"), 10, 64)
		s.serveTorrentByID(w, r, id)
		return
	}
	apiKey, ok := s.authorize(w, r, settings)
	if !ok {
		return
	}
	switch function {
	case "search", "tvsearch", "movie", "music", "book":
	default:
		s.writeError(w, http.StatusBadRequest, "unsupported function "+function)
		return
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
	releases, total, err := s.store.SearchReleases(r.Context(), filter)
	if err != nil {
		s.log.Error("torznab: search", "component", "indexarr", "subsystem", "torznab", "error", err)
		s.writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
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
				s.writeError(w, http.StatusInternalServerError, "internal error")
				return
			}
		}
	}
	base := s.baseURL(r, settings)
	s.writeSearch(w, base, settings, apiKey, releases, total, filter.Offset)
}

// authorize checks the primary key and every additional key. It returns the
// key the client used so .torrent links keep working with that same key.
func (s *Server) authorize(w http.ResponseWriter, r *http.Request, settings store.Settings) (string, bool) {
	if settings.APIKey == "" {
		s.writeError(w, http.StatusServiceUnavailable, "API key not configured")
		return "", false
	}
	provided := strings.TrimSpace(r.URL.Query().Get("apikey"))
	if provided == "" {
		provided = strings.TrimSpace(r.Header.Get("X-Api-Key"))
	}
	if provided == "" {
		s.writeError(w, http.StatusUnauthorized, "invalid API key")
		return "", false
	}
	primary := subtle.ConstantTimeCompare([]byte(provided), []byte(settings.APIKey)) == 1
	var additional store.APIKey
	if !primary {
		key, found, err := s.store.LookupAPIKey(r.Context(), provided)
		if err != nil {
			s.log.Error("torznab: lookup API key", "component", "indexarr", "subsystem", "torznab", "error", err)
			s.writeError(w, http.StatusInternalServerError, "internal error")
			return "", false
		}
		if !found {
			s.writeError(w, http.StatusUnauthorized, "invalid API key")
			return "", false
		}
		additional = key
	}
	if settings.APIRateLimit > 0 && !s.limits.allow(provided, settings.APIRateLimit, time.Now()) {
		w.Header().Set("Retry-After", "60")
		s.writeError(w, http.StatusTooManyRequests, "rate limit exceeded")
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
	name = strings.TrimSuffix(name, ".torrent")
	id, err := strconv.ParseInt(strings.TrimSpace(name), 10, 64)
	if err != nil || id <= 0 {
		s.writeError(w, http.StatusBadRequest, "invalid release id")
		return
	}
	s.serveTorrentByID(w, r, id)
}

func (s *Server) serveTorrentByID(w http.ResponseWriter, r *http.Request, id int64) {
	release, err := s.store.GetRelease(r.Context(), id)
	if err != nil {
		s.writeError(w, http.StatusNotFound, "release not found")
		return
	}
	if len(release.Torrent) == 0 {
		s.writeError(w, http.StatusNotFound, "no .torrent stored for this release; use the magnet link")
		return
	}
	w.Header().Set("Content-Type", "application/x-bittorrent")
	w.Header().Set("Content-Disposition", `attachment; filename="`+TorrentFilename(release.Title)+`"`)
	w.Header().Set("Content-Length", strconv.Itoa(len(release.Torrent)))
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Method == http.MethodHead {
		return
	}
	if _, err := w.Write(release.Torrent); err != nil {
		s.log.Debug("torznab: write torrent", "error", err)
	}
}

func (s *Server) writeCaps(w http.ResponseWriter, settings store.Settings) {
	var b bytes.Buffer
	b.WriteString(xml.Header)
	b.WriteString(`<caps>`)
	fmt.Fprintf(&b, `<server version="1.0" title="%s"/>`, esc("indexarr "+s.version))
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
		fmt.Fprintf(&b, `<category id="%d" name="%s">`, group.Top.ID, esc(group.Top.Name))
		for _, child := range group.Children {
			fmt.Fprintf(&b, `<subcat id="%d" name="%s"/>`, child.ID, esc(child.Name))
		}
		b.WriteString(`</category>`)
	}
	b.WriteString(`</categories></caps>`)
	s.writeXML(w, b.Bytes())
}

func (s *Server) writeSearch(w http.ResponseWriter, base string, settings store.Settings, apiKey string, releases []store.Release, total int64, offset int) {
	var b bytes.Buffer
	b.WriteString(xml.Header)
	b.WriteString(`<rss version="2.0" xmlns:torznab="http://torznab.com/schemas/2015/feed" xmlns:newznab="http://www.newznab.com/DTD/2010/feeds/attributes/">`)
	b.WriteString(`<channel>`)
	b.WriteString(`<title>indexarr</title>`)
	fmt.Fprintf(&b, `<link>%s</link>`, esc(base))
	b.WriteString(`<description>indexarr Torznab index</description>`)
	fmt.Fprintf(&b, `<newznab:response offset="%d" total="%d"/>`, offset, total)
	for _, release := range releases {
		s.writeItem(&b, base, settings, apiKey, release)
	}
	b.WriteString(`</channel></rss>`)
	s.writeXML(w, b.Bytes())
}

func (s *Server) writeItem(b *bytes.Buffer, base string, settings store.Settings, apiKey string, release store.Release) {
	if apiKey == "" {
		apiKey = settings.APIKey
	}
	link := release.Magnet
	if len(release.Torrent) > 0 {
		link = base + "/dl/" + strconv.FormatInt(release.ID, 10) + ".torrent?apikey=" + url.QueryEscape(apiKey)
	}
	if link == "" {
		return
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
	fmt.Fprintf(b, `<title>%s</title>`, esc(release.Title))
	fmt.Fprintf(b, `<guid isPermaLink="false">%s</guid>`, esc(guid))
	fmt.Fprintf(b, `<link>%s</link>`, esc(link))
	fmt.Fprintf(b, `<comments>%s/releases/%d</comments>`, esc(base), release.ID)
	fmt.Fprintf(b, `<pubDate>%s</pubDate>`, published.Format(time.RFC1123Z))
	fmt.Fprintf(b, `<size>%d</size>`, size)
	fmt.Fprintf(b, `<category>%s</category>`, esc(category))
	fmt.Fprintf(b, `<enclosure url="%s" length="%d" type="application/x-bittorrent"/>`, esc(link), size)
	fmt.Fprintf(b, `<torznab:attr name="category" value="%s"/>`, esc(category))
	fmt.Fprintf(b, `<torznab:attr name="size" value="%d"/>`, size)
	if release.Seeders >= 0 {
		fmt.Fprintf(b, `<torznab:attr name="seeders" value="%d"/>`, release.Seeders)
	}
	if release.Leechers >= 0 {
		fmt.Fprintf(b, `<torznab:attr name="leechers" value="%d"/>`, release.Leechers)
	}
	if release.Seeders >= 0 && release.Leechers >= 0 {
		fmt.Fprintf(b, `<torznab:attr name="peers" value="%d"/>`, release.Seeders+release.Leechers)
	}
	if release.InfoHash != "" {
		fmt.Fprintf(b, `<torznab:attr name="infohash" value="%s"/>`, esc(release.InfoHash))
	}
	if release.Magnet != "" {
		fmt.Fprintf(b, `<torznab:attr name="magneturl" value="%s"/>`, esc(release.Magnet))
	}
	if !published.IsZero() {
		fmt.Fprintf(b, `<torznab:attr name="publishdate" value="%s"/>`, published.Format(time.RFC3339))
	}
	fmt.Fprintf(b, `<torznab:attr name="downloadvolumefactor" value="%d"/>`, settings.TorznabDownloadFactor)
	fmt.Fprintf(b, `<torznab:attr name="uploadvolumefactor" value="%d"/>`, settings.TorznabUploadFactor)
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

func (s *Server) writeError(w http.ResponseWriter, status int, message string) {
	var b bytes.Buffer
	b.WriteString(xml.Header)
	fmt.Fprintf(&b, `<error code="%d" description="%s"/>`, status, esc(message))
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_, _ = w.Write(b.Bytes())
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
	_ = xml.EscapeText(&b, []byte(text))
	return b.String()
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
	return name + ".torrent"
}
