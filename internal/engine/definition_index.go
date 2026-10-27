package engine

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/buzzqw/indexarr/internal/definition"
)

const (
	defaultIndexMaxTopics = 5
	indexCacheTTL         = 30 * time.Minute
	indexCacheEntries     = 8
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
	matches := matchIndexEntries(entries, req)
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
// pages for movie searches. A generic search reads both.
func indexPageForMode(page definition.IndexPage, mode Mode) bool {
	if page.Standard == 0 {
		return true
	}
	switch mode {
	case ModeTV:
		return page.Standard/1000 == 5
	case ModeMovie:
		return page.Standard/1000 == 2
	default:
		return true
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
	response, err := c.fetchIndexPage(ctx, target)
	if err != nil {
		return nil, err
	}
	rows, err := c.extractRowsWith(response, c.def.Search.Index.Response)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(rows))
	entries := make([]indexEntry, 0, len(rows))
	for _, row := range rows {
		title := strings.TrimSpace(row["title"])
		details := resolveURL(strings.TrimSpace(row["details"]), response.URL, c.baseURL)
		if title == "" || details == "" {
			continue
		}
		if _, duplicate := seen[details]; duplicate {
			continue
		}
		seen[details] = struct{}{}
		entries = append(entries, indexEntry{Title: title, Details: details})
	}
	c.storeIndexEntries(key, entries)
	return entries, nil
}

func (c *DefinitionConnector) fetchIndexPage(ctx context.Context, target *url.URL) (*Response, error) {
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
	c.indexCache[key] = indexCacheEntry{entries: entries, expires: c.now().Add(indexCacheTTL)}
}

// matchIndexEntries ranks titles by how many query terms they contain. It is
// deliberately tolerant of punctuation and stopwords, because the lists use
// the site's own spelling ("Dune - Parte due" for "Dune Part Two").
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
		score := 0
		for _, token := range tokens {
			if strings.Contains(title, token) {
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
		if req.Season > 0 && indexTitleSeason(title, req.Season) {
			score += 2
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
