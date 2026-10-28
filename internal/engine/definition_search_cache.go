package engine

import (
	"errors"
	"net/http"
	"time"
)

// errSearchRefused marks a search page the site answered with its refusal box
// (search.blocked) instead of the results.
var errSearchRefused = errors.New("search refused by the site")

// searchCacheEntries bounds the cached search pages: a forum result page is
// about 100 KB.
const searchCacheEntries = 64

type searchCacheEntry struct {
	response *Response
	expires  time.Time
}

// searchCacheKey is the key of a cacheable search page: the full GET URL, or
// "" when the definition has no search.cache or the request is not a GET.
func (c *DefinitionConnector) searchCacheKey(request *http.Request) string {
	if c.def.Search.Cache <= 0 || request == nil || request.Method != http.MethodGet || request.URL == nil {
		return ""
	}
	return request.URL.String()
}

func (c *DefinitionConnector) cachedSearchPage(key string) (*Response, bool) {
	if key == "" {
		return nil, false
	}
	c.searchMu.Lock()
	defer c.searchMu.Unlock()
	entry, ok := c.searchCache[key]
	if !ok {
		return nil, false
	}
	if !c.now().Before(entry.expires) {
		delete(c.searchCache, key)
		return nil, false
	}
	return entry.response, true
}

// storeSearchPage keeps a successful search page for search.cache. Refusals
// never reach it: fetchSearchPage returns them as errors.
func (c *DefinitionConnector) storeSearchPage(key string, response *Response) {
	if key == "" || response == nil || response.Status != http.StatusOK {
		return
	}
	c.searchMu.Lock()
	defer c.searchMu.Unlock()
	if c.searchCache == nil {
		c.searchCache = make(map[string]searchCacheEntry)
	}
	now := c.now()
	for candidate, entry := range c.searchCache {
		if !now.Before(entry.expires) {
			delete(c.searchCache, candidate)
		}
	}
	for len(c.searchCache) >= searchCacheEntries {
		oldest := ""
		var oldestExpiry time.Time
		for candidate, entry := range c.searchCache {
			if oldest == "" || entry.expires.Before(oldestExpiry) {
				oldest, oldestExpiry = candidate, entry.expires
			}
		}
		delete(c.searchCache, oldest)
	}
	c.searchCache[key] = searchCacheEntry{response: response, expires: now.Add(c.def.Search.Cache.Duration())}
}
