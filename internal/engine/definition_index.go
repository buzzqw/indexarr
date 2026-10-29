package engine

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/buzzqw/indexarr/internal/definition"
)

const (
	defaultIndexMaxTopics = 5
	indexCacheTTL         = 30 * time.Minute
	indexCacheEntries     = 8
	// indexMaxPages bounds the pages followed for one list.
	indexMaxPages = 10
)

type indexEntry struct {
	Title    string
	Details  string
	Standard int
}

type indexCacheEntry struct {
	entries []indexEntry
	expires time.Time
}

// searchIndex answers a query from the definition's alphabetical lists. It
// fetches only the pages that can contain the title (the list without a letter
// plus the letter page of the query), matches locally and opens just the
// chosen topics. The normal search request stays as a fallback when nothing
// matches.
func (c *DefinitionConnector) searchIndex(ctx context.Context, req SearchRequest, firstPageOnly bool) ([]Item, error) {
	index := c.def.Search.Index
	if index == nil {
		return nil, errors.New("definition: no title index")
	}
	if err := c.ensureLogin(ctx); err != nil {
		return nil, err
	}
	letter := indexQueryLetter(req.Query)
	var entries []indexEntry
	seenDetails := make(map[string]struct{})
	for _, page := range index.Pages {
		if !indexPageForMode(page, req.Mode) {
			continue
		}
		if page.Letter != "" && !strings.EqualFold(strings.TrimSpace(page.Letter), letter) {
			continue
		}
		pageEntries, err := c.indexEntries(ctx, page)
		if err != nil {
			return nil, err
		}
		for _, entry := range pageEntries {
			// The TV list and a letter page can both contain the same topic;
			// one details URL is one candidate.
			if _, duplicate := seenDetails[entry.Details]; duplicate {
				continue
			}
			seenDetails[entry.Details] = struct{}{}
			if page.Standard > 0 {
				entry.Standard = page.Standard
			}
			entries = append(entries, entry)
		}
	}
	matchReq := req
	if matchReq.Season <= 0 {
		// A free-text search ("Bones S06E05", as Gextto's manual search sends
		// it) names the season only in the query: without it every season of
		// the series is a candidate, the shortest titles win the max_topics
		// slots and the wanted season may never be opened.
		matchReq.Season, matchReq.Episode = indexQuerySeasonEpisode(req.Query)
	}
	matches := matchIndexEntries(entries, matchReq)
	maxTopics := index.MaxTopics
	if maxTopics <= 0 {
		maxTopics = defaultIndexMaxTopics
	}
	if len(matches) > maxTopics {
		matches = matches[:maxTopics]
	}
	items := make([]Item, 0, len(matches))
	for _, match := range matches {
		category := match.Standard
		if category <= 0 {
			category = otherCategory
		}
		items = append(items, Item{
			Protocol:   c.protocol,
			Title:      match.Title,
			DetailsURL: match.Details,
			Categories: []int{category},
		})
	}
	if len(items) > 0 && !req.SkipDetails {
		resolved, err := c.resolveSearchDetails(ctx, items)
		if err != nil {
			return nil, err
		}
		items = resolved
	}
	if len(items) > 0 {
		return items, nil
	}
	if c.def.Search.Request.Get != "" || c.def.Search.Request.Post != "" {
		return c.search(ctx, req, firstPageOnly)
	}
	return nil, nil
}

// indexPageForMode keeps the TV list for TV searches and the movie letter
// pages for movie searches. A generic search reads both. Book and music
// searches read none: a title list of films and series cannot answer them, and
// a spurious match there would skip the site search fallback.
func indexPageForMode(page definition.IndexPage, mode Mode) bool {
	switch mode {
	case ModeSearch:
		return true
	case ModeTV:
		return page.Standard == 0 || page.Standard/1000 == 5
	case ModeMovie:
		return page.Standard == 0 || page.Standard/1000 == 2
	default:
		return false
	}
}

// indexQueryLetter returns the letter page to read for a query: the first
// significant character, or "#" for a number or a query without letters.
func indexQueryLetter(query string) string {
	for _, r := range strings.TrimSpace(query) {
		switch {
		case unicode.IsLetter(r):
			return strings.ToUpper(string(r))
		case unicode.IsDigit(r):
			return "#"
		}
	}
	return "#"
}

func (c *DefinitionConnector) indexEntries(ctx context.Context, page definition.IndexPage) ([]indexEntry, error) {
	key := strings.TrimSpace(page.Get)
	if entries, ok := c.cachedIndexEntries(key); ok {
		return entries, nil
	}
	target, err := c.buildURL(key)
	if err != nil {
		return nil, err
	}
	// Links back to a list page are navigation (the letter bar of a movie
	// topic points to the other letter topics), not titles.
	listPages := c.indexListURLs()
	seen := make(map[string]struct{})
	visited := make(map[string]struct{})
	var entries []indexEntry
	for pages := 0; target != nil && pages < indexMaxPages; pages++ {
		visited[target.String()] = struct{}{}
		response, err := c.fetchIndexPage(ctx, target)
		if err != nil {
			return nil, err
		}
		rows, err := c.extractRowsWith(response, c.def.Search.Index.Response)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			title := strings.TrimSpace(row["title"])
			details := resolveURL(strings.TrimSpace(row["details"]), response.URL, c.baseURL)
			if title == "" || details == "" {
				continue
			}
			if _, list := listPages[details]; list {
				continue
			}
			if _, duplicate := seen[details]; duplicate {
				continue
			}
			seen[details] = struct{}{}
			entries = append(entries, indexEntry{Title: title, Details: details})
		}
		target = c.nextIndexPage(response, page.Next, visited)
	}
	c.storeIndexEntries(key, entries)
	return entries, nil
}

// nextIndexPage returns the following page of a list split over several
// pages, or nil when the page has no next link or it was already read.
func (c *DefinitionConnector) nextIndexPage(response *Response, selector string, visited map[string]struct{}) *url.URL {
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return nil
	}
	href, err := extractHTMLValue(htmlBody(response), selector, "href")
	if err != nil || strings.TrimSpace(href) == "" {
		return nil
	}
	next, err := url.Parse(resolveURL(strings.TrimSpace(href), response.URL, c.baseURL))
	if err != nil || !c.ownHost(next.Host) {
		return nil
	}
	if _, done := visited[next.String()]; done {
		return nil
	}
	return next
}

// indexListURLs returns the absolute URLs of the definition's list pages.
func (c *DefinitionConnector) indexListURLs() map[string]struct{} {
	out := make(map[string]struct{}, len(c.def.Search.Index.Pages))
	for _, page := range c.def.Search.Index.Pages {
		if target, err := c.buildURL(strings.TrimSpace(page.Get)); err == nil {
			out[target.String()] = struct{}{}
		}
	}
	return out
}

func (c *DefinitionConnector) fetchIndexPage(ctx context.Context, target *url.URL) (*Response, error) {
	stamp := c.session.loginStamp()
	response, err := c.fetchIndexPageOnce(ctx, target)
	if err != nil || !c.sessionLost(response) {
		return response, err
	}
	// The forum keeps one session per account: a login from another browser
	// leaves the list readable as a guest or not at all. Log in again once.
	if err := c.reLoginAfter(ctx, stamp); err != nil {
		return nil, err
	}
	return c.fetchIndexPageOnce(ctx, target)
}

func (c *DefinitionConnector) fetchIndexPageOnce(ctx context.Context, target *url.URL) (*Response, error) {
	request, err := c.newRequest(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, errors.New("search index: build request failed")
	}
	response, err := c.do(ctx, request)
	if err != nil {
		return nil, c.requestError(err)
	}
	if (response.Status == http.StatusUnauthorized || response.Status == http.StatusForbidden) && c.needsLogin() {
		if err := c.forceReLogin(ctx); err != nil {
			return nil, err
		}
		request, err = c.newRequest(ctx, http.MethodGet, target, nil)
		if err != nil {
			return nil, errors.New("search index: build request failed")
		}
		response, err = c.do(ctx, request)
		if err != nil {
			return nil, c.requestError(err)
		}
	}
	if response.Status < 200 || response.Status >= 400 {
		return nil, errors.New("search index: page returned an error")
	}
	return response, nil
}

func (c *DefinitionConnector) cachedIndexEntries(key string) ([]indexEntry, bool) {
	c.indexMu.Lock()
	defer c.indexMu.Unlock()
	entry, ok := c.indexCache[key]
	if !ok || (!entry.expires.IsZero() && !c.now().Before(entry.expires)) {
		if ok {
			delete(c.indexCache, key)
		}
		return nil, false
	}
	return entry.entries, true
}

func (c *DefinitionConnector) storeIndexEntries(key string, entries []indexEntry) {
	c.indexMu.Lock()
	defer c.indexMu.Unlock()
	if c.indexCache == nil {
		c.indexCache = make(map[string]indexCacheEntry)
	}
	// Bound the cache: an index page can hold thousands of titles, so keep a
	// handful of them and drop the oldest expiry first.
	for len(c.indexCache) >= indexCacheEntries {
		oldest := ""
		var oldestExpiry time.Time
		for candidate, entry := range c.indexCache {
			if oldest == "" || entry.expires.Before(oldestExpiry) {
				oldest, oldestExpiry = candidate, entry.expires
			}
		}
		delete(c.indexCache, oldest)
	}
	ttl := indexCacheTTL
	if configured := c.def.Search.Index.Cache.Duration(); configured > 0 {
		ttl = configured
	}
	c.indexCache[key] = indexCacheEntry{entries: entries, expires: c.now().Add(ttl)}
}

// matchIndexEntries ranks titles by how many query terms start one of their
// words. It is deliberately tolerant of punctuation and stopwords, because the
// lists use the site's own spelling ("Dune - Parte due" for "Dune Part Two":
// "part" starts "parte"). A term never matches inside a word, so "It" does not
// pick "Hitman" and "Up" does not pick "Superman": a spurious match would hide
// the site search fallback.
func matchIndexEntries(entries []indexEntry, req SearchRequest) []indexEntry {
	tokens := indexQueryTokens(req.Query)
	if len(tokens) == 0 {
		return nil
	}
	type scored struct {
		entry indexEntry
		score int
	}
	var matches []scored
	for _, entry := range entries {
		title := normalizeIndexText(entry.Title)
		if title == "" {
			continue
		}
		words := withJoinedInitials(strings.Fields(title))
		score := 0
		for _, token := range tokens {
			if indexWordPrefix(words, token) {
				score++
			}
		}
		if score == 0 {
			continue
		}
		// Two-term queries need both terms: "The Last of Us" must not match
		// "Tuck Everlasting" just because it contains "last". Longer queries
		// allow one miss for the site's own spelling ("Dune Part Two" against
		// "Dune - Parte due").
		needed := len(tokens)
		if needed > 2 {
			needed--
		}
		if score < needed {
			continue
		}
		if req.Season > 0 {
			// A topic that names another season holds none of the wanted
			// episodes: opening it costs a page and expands dozens of files
			// that can never match. A title without a season (a miniseries,
			// "Stagione Unica") stays a candidate.
			if season, ok := indexTitleSeasonNumber(title); ok && season != req.Season {
				continue
			}
			if indexTitleSeason(title, req.Season) {
				score += 2
			}
		}
		if req.Episode > 0 && indexTitleEpisode(title, req.Season, req.Episode) {
			score++
		}
		matches = append(matches, scored{entry: entry, score: score})
	}
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].score != matches[j].score {
			return matches[i].score > matches[j].score
		}
		return len(matches[i].entry.Title) < len(matches[j].entry.Title)
	})
	out := make([]indexEntry, len(matches))
	for i, match := range matches {
		out[i] = match.entry
	}
	return out
}

func indexWordPrefix(words []string, token string) bool {
	for _, word := range words {
		if strings.HasPrefix(word, token) {
			return true
		}
	}
	return false
}

var indexStopwords = map[string]struct{}{
	"the": {}, "of": {}, "and": {}, "a": {}, "an": {},
	"il": {}, "lo": {}, "la": {}, "i": {}, "gli": {}, "le": {}, "un": {}, "una": {},
	"di": {}, "da": {}, "in": {}, "e": {}, "ed": {}, "per": {}, "con": {}, "su": {},
	"al": {}, "alla": {}, "del": {}, "della": {}, "dei": {}, "delle": {},
	"stagione": {}, "season": {}, "serie": {}, "series": {},
}

func indexQueryTokens(query string) []string {
	normalized := normalizeIndexText(query)
	var out []string
	for _, token := range joinedInitials(strings.Fields(normalized)) {
		out = append(out, token)
	}
	for _, token := range strings.Fields(normalized) {
		if len(token) < 2 {
			continue
		}
		if _, stop := indexStopwords[token]; stop {
			continue
		}
		if isEpisodeToken(token) {
			continue
		}
		out = append(out, token)
	}
	if len(out) == 0 {
		// A one-word query such as "Up" is still a real title.
		for _, token := range strings.Fields(normalized) {
			if len(token) >= 1 {
				out = append(out, token)
			}
		}
	}
	return out
}

var indexEpisodeToken = regexp.MustCompile(`^(?:s\d{1,2}(?:e\d{1,3})?|\d{1,2}x\d{1,3})$`)

func isEpisodeToken(token string) bool {
	return indexEpisodeToken.MatchString(token)
}

var indexQueryEpisode = regexp.MustCompile(`(?i)\bs(\d{1,2})(?:[ ._-]?e(\d{1,3}))?\b|\b(\d{1,2})x(\d{1,3})\b`)

// indexQuerySeasonEpisode reads the season (and episode, when present) a
// free-text query names: "S06E05", "S06" or "6x05". Zero means none.
func indexQuerySeasonEpisode(query string) (int, int) {
	match := indexQueryEpisode.FindStringSubmatch(query)
	if match == nil {
		return 0, 0
	}
	seasonText, episodeText := match[1], match[2]
	if seasonText == "" {
		seasonText, episodeText = match[3], match[4]
	}
	season, err := strconv.Atoi(seasonText)
	if err != nil || season <= 0 {
		return 0, 0
	}
	episode, _ := strconv.Atoi(episodeText)
	return season, episode
}

func normalizeIndexText(value string) string {
	var builder strings.Builder
	builder.Grow(len(value))
	lastSpace := true
	for _, r := range strings.ToLower(value) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			builder.WriteRune(r)
			lastSpace = false
		default:
			if !lastSpace {
				builder.WriteByte(' ')
				lastSpace = true
			}
		}
	}
	return strings.TrimSpace(builder.String())
}

var indexSeasonNumber = regexp.MustCompile(`\b(?:stagione|season)\s+(\d{1,2})\b|\bs(\d{1,2})(?:e\d{1,3})?\b`)

// indexTitleSeasonNumber reads the season a list title names, if any.
func indexTitleSeasonNumber(title string) (int, bool) {
	match := indexSeasonNumber.FindStringSubmatch(title)
	if match == nil {
		return 0, false
	}
	value := match[1]
	if value == "" {
		value = match[2]
	}
	season, err := strconv.Atoi(value)
	if err != nil || season <= 0 {
		return 0, false
	}
	return season, true
}

func indexTitleSeason(title string, season int) bool {
	needle := "stagione " + itoaPositive(season)
	needle2 := "season " + itoaPositive(season)
	needle3 := "s" + pad2(season)
	return strings.Contains(title, needle) || strings.Contains(title, needle2) || strings.Contains(title, needle3)
}

func indexTitleEpisode(title string, season, episode int) bool {
	if season <= 0 || episode <= 0 {
		return false
	}
	needles := []string{
		"s" + pad2(season) + "e" + pad2(episode),
		itoaPositive(season) + "x" + pad2(episode),
	}
	for _, needle := range needles {
		if strings.Contains(title, needle) {
			return true
		}
	}
	return false
}

func itoaPositive(value int) string {
	if value <= 0 {
		return "0"
	}
	digits := make([]byte, 0, 4)
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}

func pad2(value int) string {
	if value < 10 {
		return "0" + itoaPositive(value)
	}
	return itoaPositive(value)
}

// joinedInitials returns the runs of single-character words joined into one:
// "9 1 1" gives "911", "s w a t" gives "swat". Titles written with dots or
// dashes between the letters ("9-1-1", "S.W.A.T.", "N.C.I.S.") lose their
// words to the normalization, and the list may spell them either way.
func joinedInitials(words []string) []string {
	var out []string
	var run strings.Builder
	count := 0
	flush := func() {
		if count >= 2 {
			out = append(out, run.String())
		}
		run.Reset()
		count = 0
	}
	for _, word := range words {
		if len([]rune(word)) == 1 {
			run.WriteString(word)
			count++
			continue
		}
		flush()
	}
	flush()
	return out
}

// withJoinedInitials adds the joined runs to a title's words.
func withJoinedInitials(words []string) []string {
	joined := joinedInitials(words)
	if len(joined) == 0 {
		return words
	}
	return append(append([]string(nil), words...), joined...)
}
