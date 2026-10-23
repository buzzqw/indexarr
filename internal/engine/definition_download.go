package engine

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/buzzqw/indexarr/internal/definition"
)

const (
	defaultDownloadMaxConcurrent = 2
	maxDownloadMaxConcurrent     = 8
	defaultDownloadCacheEntries  = 256
	defaultDownloadCacheTTL      = 10 * time.Minute
)

// resolvedDownload is one value found on a details page: either a magnet link
// or a torrent URL. The two are mutually exclusive.
type resolvedDownload struct {
	Magnet      string
	DownloadURL string
	InfoHash    string
}

func (r resolvedDownload) empty() bool {
	return r.Magnet == "" && r.DownloadURL == ""
}

type downloadCacheEntry struct {
	result  resolvedDownload
	expires time.Time
}

// downloadCache is a small LRU with a TTL. Expiry uses the connector clock so
// tests can advance time without sleeping.
type downloadCache struct {
	mu      sync.Mutex
	max     int
	ttl     time.Duration
	entries map[string]downloadCacheEntry
	order   []string
}

func newDownloadCache(max int, ttl time.Duration) *downloadCache {
	return &downloadCache{
		max:     max,
		ttl:     ttl,
		entries: make(map[string]downloadCacheEntry),
	}
}

func (c *downloadCache) get(key string, now time.Time) (resolvedDownload, bool) {
	if c == nil {
		return resolvedDownload{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	entry, ok := c.entries[key]
	if !ok {
		return resolvedDownload{}, false
	}
	if c.ttl > 0 && !now.Before(entry.expires) {
		delete(c.entries, key)
		c.removeLocked(key)
		return resolvedDownload{}, false
	}
	c.touchLocked(key)
	return entry.result, true
}

func (c *downloadCache) set(key string, result resolvedDownload, now time.Time) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	if _, exists := c.entries[key]; exists {
		c.touchLocked(key)
	} else {
		c.order = append(c.order, key)
	}
	entry := downloadCacheEntry{result: result}
	if c.ttl > 0 {
		entry.expires = now.Add(c.ttl)
	}
	c.entries[key] = entry

	for c.max > 0 && len(c.entries) > c.max && len(c.order) > 0 {
		oldest := c.order[0]
		c.order = c.order[1:]
		delete(c.entries, oldest)
	}
}

func (c *downloadCache) touchLocked(key string) {
	c.removeLocked(key)
	c.order = append(c.order, key)
}

func (c *downloadCache) removeLocked(key string) {
	for i, candidate := range c.order {
		if candidate == key {
			c.order = append(c.order[:i], c.order[i+1:]...)
			return
		}
	}
}

// downloadDetailsField returns the search field that holds the details URL.
// It is empty when the definition has no download section.
func (c *DefinitionConnector) downloadDetailsField() string {
	if c == nil || c.def == nil || c.def.Download == nil {
		return ""
	}
	from := strings.TrimSpace(c.def.Download.From)
	if from == "" {
		return "details"
	}
	return from
}

// resolvesOnSearch reports whether the search resolves details pages eagerly.
// Only an explicit resolve_on: search does so; an absent value means grab, so
// the site receives no extra requests until the user asks for a download.
func (c *DefinitionConnector) resolvesOnSearch() bool {
	if c == nil || c.def == nil || c.def.Download == nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(c.def.Download.ResolveOn), "search")
}

func (c *DefinitionConnector) maxConcurrentDownloads() int {
	value := defaultDownloadMaxConcurrent
	if c != nil && c.def != nil && c.def.Download != nil {
		value = c.def.Download.MaxConcurrent
	}
	if value <= 0 {
		value = defaultDownloadMaxConcurrent
	}
	if value < 1 {
		value = 1
	}
	if value > maxDownloadMaxConcurrent {
		value = maxDownloadMaxConcurrent
	}
	return value
}

// resolveSearchDetails opens the details pages of the items that have neither
// a magnet nor a direct download URL. The caller passes only the rows that
// survived deduplication and the requested limit. Items are resolved
// concurrently, at most max_concurrent at a time, and a failing page never
// aborts the whole result: the item is left as it was and the failure is
// counted.
func (c *DefinitionConnector) resolveSearchDetails(ctx context.Context, items []Item) error {
	if !c.resolvesOnSearch() {
		return nil
	}

	// Group items by details URL so repeated rows cost one request.
	groups := make(map[string][]int)
	var order []string
	for i := range items {
		if strings.TrimSpace(items[i].Magnet) != "" || strings.TrimSpace(items[i].DownloadURL) != "" {
			continue
		}
		rawURL := strings.TrimSpace(items[i].DetailsURL)
		if rawURL == "" {
			continue
		}
		resolved := resolveURL(rawURL, c.baseURL)
		if resolved == "" {
			c.resolveFailures.Add(1)
			continue
		}
		if _, exists := groups[resolved]; !exists {
			order = append(order, resolved)
		}
		groups[resolved] = append(groups[resolved], i)
	}
	if len(groups) == 0 {
		return nil
	}

	limit := c.maxConcurrentDownloads()
	semaphore := make(chan struct{}, limit)
	var wait sync.WaitGroup
	for _, rawURL := range order {
		if err := ctx.Err(); err != nil {
			break
		}
		indexes := groups[rawURL]
		wait.Add(1)
		go func(rawURL string, indexes []int) {
			defer wait.Done()
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			case <-ctx.Done():
				return
			}
			result, err := c.resolveDetailsCached(ctx, rawURL)
			if err != nil || result.empty() {
				c.resolveFailures.Add(1)
				return
			}
			for _, index := range indexes {
				applyResolvedDownload(&items[index], result)
			}
		}(rawURL, indexes)
	}
	wait.Wait()
	return ctx.Err()
}

func applyResolvedDownload(item *Item, result resolvedDownload) {
	if item == nil {
		return
	}
	if result.Magnet != "" {
		item.Magnet = result.Magnet
		if result.InfoHash != "" {
			item.InfoHash = result.InfoHash
		} else {
			item.InfoHash = normalizeInfoHash(result.Magnet)
		}
		return
	}
	if result.DownloadURL != "" && item.Magnet == "" {
		item.DownloadURL = result.DownloadURL
	}
}

// resolveDetailsCached returns the download target of one details page. The
// page body is cached by URL with a TTL; the connector clock makes expiry
// deterministic in tests.
func (c *DefinitionConnector) resolveDetailsCached(ctx context.Context, rawURL string) (resolvedDownload, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return resolvedDownload{}, errors.New("download: invalid details URL")
	}
	target, err := url.Parse(rawURL)
	if err != nil {
		return resolvedDownload{}, errors.New("download: invalid details URL")
	}
	if !target.IsAbs() {
		target, err = c.buildURL(rawURL)
		if err != nil {
			return resolvedDownload{}, errors.New("download: invalid details URL")
		}
	}
	if target.Scheme != "http" && target.Scheme != "https" {
		return resolvedDownload{}, errors.New("download: invalid details URL")
	}

	key := target.String()
	now := c.now()
	if c.downloadCache != nil {
		if result, ok := c.downloadCache.get(key, now); ok {
			return result, nil
		}
	}

	result, err := c.fetchDetailsDownload(ctx, target)
	if err != nil {
		return resolvedDownload{}, err
	}
	if c.downloadCache != nil {
		c.downloadCache.set(key, result, now)
	}
	return result, nil
}

func (c *DefinitionConnector) fetchDetailsDownload(ctx context.Context, target *url.URL) (resolvedDownload, error) {
	request, err := c.newRequest(ctx, http.MethodGet, target, nil)
	if err != nil {
		return resolvedDownload{}, errors.New("download: build details request failed")
	}
	response, err := c.do(ctx, request)
	if err != nil {
		return resolvedDownload{}, c.requestError(err)
	}

	if response.Status >= 300 && response.Status < 400 {
		location := strings.TrimSpace(response.Header.Get("Location"))
		if strings.HasPrefix(strings.ToLower(location), "magnet:") {
			return resolvedDownload{
				Magnet:   location,
				InfoHash: normalizeInfoHash(location),
			}, nil
		}
		return resolvedDownload{}, errors.New("download: unexpected redirect from details page")
	}
	if response.Status < 200 || response.Status >= 400 {
		return resolvedDownload{}, fmt.Errorf("download: details returned HTTP %d", response.Status)
	}

	for _, extractor := range c.def.Download.Extract {
		result, ok, err := c.extractDownloadTarget(response, extractor)
		if err != nil {
			return resolvedDownload{}, errors.New("download: details extractor failed")
		}
		if ok && !result.empty() {
			return result, nil
		}
	}
	return resolvedDownload{}, errors.New("download: no download target on details page")
}

func (c *DefinitionConnector) extractDownloadTarget(response *Response, target definition.DownloadTarget) (resolvedDownload, bool, error) {
	if target.Magnet != nil {
		value, err := c.extractDetailField(response, *target.Magnet)
		if err != nil {
			return resolvedDownload{}, false, err
		}
		if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(value)), "magnet:") {
			return resolvedDownload{}, false, nil
		}
		value = strings.TrimSpace(value)
		return resolvedDownload{
			Magnet:   value,
			InfoHash: normalizeInfoHash(value),
		}, true, nil
	}
	if target.Torrent != nil {
		value, err := c.extractDetailField(response, *target.Torrent)
		if err != nil {
			return resolvedDownload{}, false, err
		}
		value = strings.TrimSpace(value)
		if value == "" {
			return resolvedDownload{}, false, nil
		}
		if strings.HasPrefix(strings.ToLower(value), "magnet:") {
			return resolvedDownload{
				Magnet:   value,
				InfoHash: normalizeInfoHash(value),
			}, true, nil
		}
		base := response.URL
		if base == nil {
			base = c.baseURL
		}
		resolved := resolveURL(value, base, c.baseURL)
		if resolved == "" {
			return resolvedDownload{}, false, nil
		}
		parsed, err := url.Parse(resolved)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return resolvedDownload{}, false, nil
		}
		return resolvedDownload{DownloadURL: resolved}, true, nil
	}
	return resolvedDownload{}, false, nil
}

// extractDetailField applies one Field to a whole details response. Each
// selector is a fallback; path reads a JSON document.
func (c *DefinitionConnector) extractDetailField(response *Response, field definition.Field) (string, error) {
	var (
		value string
		err   error
	)
	if strings.TrimSpace(field.Path) != "" {
		var root any
		root, err = decodeJSON(response.Body)
		if err != nil {
			return "", err
		}
		value = jsonValueString(walkJSONPath(root, field.Path))
	} else {
		for _, selector := range field.Selector {
			value, err = extractHTMLValue(response.Body, selector, field.Attr)
			if err != nil {
				return "", err
			}
			if strings.TrimSpace(value) != "" {
				break
			}
		}
	}
	value, err = applyFilters(value, field.Filters)
	if err != nil {
		return "", err
	}
	return value, nil
}

// downloadFromSection resolves a details page at grab time using the
// definition's download section.
func (c *DefinitionConnector) downloadFromSection(ctx context.Context, item Item) (Payload, error) {
	rawURL := resolveURL(item.DetailsURL, c.baseURL)
	if rawURL == "" {
		return Payload{}, errors.New("download: invalid details URL")
	}
	result, err := c.resolveDetailsCached(ctx, rawURL)
	if err != nil {
		return Payload{}, err
	}
	if result.Magnet != "" {
		return Payload{Magnet: result.Magnet}, nil
	}
	if result.DownloadURL != "" {
		return c.downloadURL(ctx, result.DownloadURL, item.Title)
	}
	return Payload{}, errors.New("download: no download target on details page")
}
