package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
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

// resolvedDownload is one value found on a details page: a magnet link, a
// torrent URL or an eD2k file link. The three are mutually exclusive.
type resolvedDownload struct {
	Magnet      string
	DownloadURL string
	Ed2k        string
	Ed2kHash    string
	InfoHash    string
	Title       string
	Size        int64
}

func (r resolvedDownload) empty() bool {
	return r.Magnet == "" && r.DownloadURL == "" && r.Ed2k == ""
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

// resolveSearchDetails opens the details pages of the items that have no
// direct link yet. The caller passes only the rows that survived deduplication
// and the requested limit. Items are resolved concurrently, at most
// max_concurrent at a time, and a failing page never aborts the whole result:
// the item is left as it was and the failure is counted.
//
// With download.expand: file a details page that carries many eD2k links (a
// season pack, a discography) becomes one item per file, so the archive keeps
// every link instead of only the first.
func (c *DefinitionConnector) resolveSearchDetails(ctx context.Context, items []Item) ([]Item, error) {
	if !c.resolvesOnSearch() {
		return items, nil
	}
	// A pre-download engagement action must never run while searching. The
	// validator already rejects before + resolve_on: search; this guard keeps
	// a hand-built Definition safe too.
	if c.def.Download != nil && c.def.Download.Before != nil {
		return items, nil
	}

	// Group items by details URL so repeated rows cost one request.
	groups := make(map[string][]int)
	var order []string
	for i := range items {
		if strings.TrimSpace(items[i].Magnet) != "" ||
			strings.TrimSpace(items[i].DownloadURL) != "" ||
			strings.TrimSpace(items[i].Ed2k) != "" {
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
		return items, nil
	}

	expand := strings.EqualFold(strings.TrimSpace(c.def.Download.Expand), "file")
	replaced := make(map[int][]Item)
	var replacedMu sync.Mutex
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
			var candidates []resolvedDownload
			var err error
			if expand {
				candidates, err = c.fetchDetailsDownloads(ctx, rawURL, 0, 0)
			} else {
				var result resolvedDownload
				result, err = c.resolveDetailsCached(ctx, rawURL, 0, 0)
				if err == nil && !result.empty() {
					candidates = []resolvedDownload{result}
				}
			}
			if err != nil || len(candidates) == 0 {
				c.resolveFailures.Add(1)
				return
			}
			for _, index := range indexes {
				base := items[index]
				if !expand || len(candidates) == 1 {
					applied := base
					applyResolvedDownload(&applied, candidates[0])
					replacedMu.Lock()
					replaced[index] = []Item{applied}
					replacedMu.Unlock()
					continue
				}
				expanded := make([]Item, 0, len(candidates))
				for _, candidate := range candidates {
					expanded = append(expanded, expandItem(base, candidate))
				}
				replacedMu.Lock()
				replaced[index] = expanded
				replacedMu.Unlock()
			}
		}(rawURL, indexes)
	}
	wait.Wait()

	out := make([]Item, 0, len(items))
	for i, item := range items {
		if expanded, ok := replaced[i]; ok {
			out = append(out, expanded...)
			continue
		}
		out = append(out, item)
	}
	return out, ctx.Err()
}

// expandItem turns one file of a details page into its own release. The topic
// title stays in front of the file name so a season pack is still readable,
// while the eD2k hash gives the row its stable identity.
func expandItem(base Item, candidate resolvedDownload) Item {
	item := base
	item.Magnet = ""
	item.InfoHash = ""
	item.DownloadURL = ""
	item.Ed2k = ""
	item.Ed2kHash = ""
	name := candidateName(candidate, "")
	applyResolvedDownload(&item, candidate)
	if name == "" {
		return item
	}
	// applyResolvedDownload may have copied the file name into Title; the
	// expanded row keeps the topic title in front of it for readability.
	lowerBase := strings.ToLower(strings.TrimSpace(base.Title))
	if fileNameIsRelease(base.Title, name) {
		// "Bones.S06E12.HDTV.avi" is already a release name. With the topic
		// in front ("BONES - Stagione 6 - Bones.S06E12…") Gextto and the Arr
		// read the series as "BONES - Stagione 6 - Bones" and never match it
		// to the monitored show.
		item.Title = name
	} else if !strings.Contains(lowerBase, strings.ToLower(name)) {
		item.Title = strings.TrimSpace(base.Title + " - " + name)
	} else {
		item.Title = name
	}
	item.Size = candidate.Size
	if item.Size <= 0 {
		item.Size = base.Size
	}
	switch {
	case candidate.Ed2k != "":
		item.GUID = "ed2k:" + candidate.Ed2kHash
	case candidate.DownloadURL != "" && item.InfoHash == "":
		// Two .torrent files of one topic share the topic GUID; without a
		// distinct one the archive keeps only the first. The URL may carry a
		// passkey, so only its digest enters the GUID.
		topic := base.GUID
		if topic == "" {
			topic = base.DetailsURL
		}
		sum := sha256.Sum256([]byte(candidate.DownloadURL))
		item.GUID = topic + "#" + hex.EncodeToString(sum[:8])
	}
	return item
}

// fetchDetailsDownloads reads every candidate of one details page. It is the
// expansion path: no cache, because a group already guarantees one request per
// details URL in a search.
func (c *DefinitionConnector) fetchDetailsDownloads(ctx context.Context, rawURL string, season, episode int) ([]resolvedDownload, error) {
	target, err := c.detailsURL(rawURL)
	if err != nil {
		return nil, err
	}
	response, err := c.fetchDetailsPage(ctx, target)
	if err != nil {
		return nil, err
	}
	title, size := c.detailsTitleSize(response)
	candidates, err := c.extractDownloadCandidates(response)
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return nil, errors.New("download: no download target on details page")
	}
	for i := range candidates {
		if candidates[i].Title == "" {
			candidates[i].Title = title
		}
		if candidates[i].Size == 0 {
			candidates[i].Size = size
		}
	}
	return candidates, nil
}

func (c *DefinitionConnector) detailsURL(rawURL string) (*url.URL, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return nil, errors.New("download: invalid details URL")
	}
	target, err := url.Parse(rawURL)
	if err != nil {
		return nil, errors.New("download: invalid details URL")
	}
	if !target.IsAbs() {
		target, err = c.buildURL(rawURL)
		if err != nil {
			return nil, errors.New("download: invalid details URL")
		}
	}
	if target.Scheme != "http" && target.Scheme != "https" {
		return nil, errors.New("download: invalid details URL")
	}
	return target, nil
}

func (c *DefinitionConnector) detailsTitleSize(response *Response) (string, int64) {
	title := ""
	if c.def.Download.Title != nil {
		if value, err := c.extractDetailField(response, *c.def.Download.Title); err == nil {
			title = strings.TrimSpace(value)
		}
	}
	size := int64(0)
	if c.def.Download.Size != nil {
		if value, err := c.extractDetailField(response, *c.def.Download.Size); err == nil {
			size = parseSize(value)
		}
	}
	return title, size
}

func applyResolvedDownload(item *Item, result resolvedDownload) {
	if item == nil {
		return
	}
	if strings.TrimSpace(result.Title) != "" {
		item.Title = strings.TrimSpace(result.Title)
	}
	if result.Size > 0 {
		item.Size = result.Size
	}
	if result.Magnet != "" {
		item.Magnet = result.Magnet
		if result.InfoHash != "" {
			item.InfoHash = result.InfoHash
		} else {
			item.InfoHash = normalizeInfoHash(result.Magnet)
		}
		torrentFromEd2kSource(item)
		return
	}
	if result.Ed2k != "" {
		item.Ed2k = result.Ed2k
		item.Ed2kHash = result.Ed2kHash
		item.Protocol = ProtocolEd2k
		if result.Size > 0 {
			item.Size = result.Size
		}
		return
	}
	if result.DownloadURL != "" && item.Magnet == "" {
		item.DownloadURL = result.DownloadURL
		torrentFromEd2kSource(item)
	}
}

// torrentFromEd2kSource marks a row of an eD2k source whose topic carried a
// magnet or a .torrent instead of an ed2k:// link. The row is a torrent: the
// archive must store it as one and a grab must reach a torrent client, not
// aMule.
func torrentFromEd2kSource(item *Item) {
	if item.Protocol == ProtocolEd2k {
		item.Protocol = ProtocolTorrent
	}
}

// resolveDetailsCached returns the download target of one details page. The
// page body is cached by URL with a TTL; the connector clock makes expiry
// deterministic in tests.
func (c *DefinitionConnector) resolveDetailsCached(ctx context.Context, rawURL string, season, episode int) (resolvedDownload, error) {
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
	if strings.EqualFold(strings.TrimSpace(c.def.Download.Select), "season_episode") {
		key += fmt.Sprintf("#season=%d&episode=%d", season, episode)
	}
	now := c.now()
	if c.downloadCache != nil {
		if result, ok := c.downloadCache.get(key, now); ok {
			return result, nil
		}
	}

	result, err := c.fetchDetailsDownload(ctx, target, season, episode)
	if err != nil {
		return resolvedDownload{}, err
	}
	if c.downloadCache != nil {
		c.downloadCache.set(key, result, now)
	}
	return result, nil
}

func (c *DefinitionConnector) fetchDetailsDownload(ctx context.Context, target *url.URL, season, episode int) (resolvedDownload, error) {
	response, err := c.fetchDetailsPage(ctx, target)
	if err != nil {
		return resolvedDownload{}, err
	}

	title := ""
	if c.def.Download.Title != nil {
		if value, err := c.extractDetailField(response, *c.def.Download.Title); err == nil {
			title = strings.TrimSpace(value)
		}
	}
	size := int64(0)
	if c.def.Download.Size != nil {
		if value, err := c.extractDetailField(response, *c.def.Download.Size); err == nil {
			size = parseSize(value)
		}
	}
	candidates, err := c.extractDownloadCandidates(response)
	if err != nil {
		return resolvedDownload{}, err
	}
	if len(candidates) == 0 {
		return resolvedDownload{}, errors.New("download: no download target on details page")
	}
	result, ok := c.chooseCandidate(title, candidates, season, episode)
	if !ok {
		return resolvedDownload{}, errors.New("download: no magnet matches the requested season and episode")
	}
	if result.Title == "" {
		result.Title = title
	}
	if result.Size == 0 {
		result.Size = size
	}
	return result, nil
}

// fetchDetailsPage issues the GET for a details page and applies the same
// redirect and status rules as every other download request.
func (c *DefinitionConnector) fetchDetailsPage(ctx context.Context, target *url.URL) (*Response, error) {
	stamp := c.session.loginStamp()
	response, err := c.fetchDetailsPageOnce(ctx, target)
	if err != nil || !c.sessionLost(response) {
		return response, err
	}
	// The site dropped the session (auth.logged_in is missing): log in again
	// once and read the page again instead of returning a page without links.
	if err := c.reLoginAfter(ctx, stamp); err != nil {
		return nil, err
	}
	return c.fetchDetailsPageOnce(ctx, target)
}

func (c *DefinitionConnector) fetchDetailsPageOnce(ctx context.Context, target *url.URL) (*Response, error) {
	request, err := c.newRequest(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, errors.New("download: build details request failed")
	}
	response, err := c.do(ctx, request)
	if err != nil {
		return nil, c.requestError(err)
	}

	if response.Status >= 300 && response.Status < 400 {
		location := strings.TrimSpace(response.Header.Get("Location"))
		if strings.HasPrefix(strings.ToLower(location), "magnet:") {
			return &Response{
				Status: http.StatusOK,
				Header: make(http.Header),
				Body:   []byte(`<html><body><a href="` + html.EscapeString(location) + `">magnet</a></body></html>`),
				URL:    response.URL,
			}, nil
		}
		return nil, errors.New("download: unexpected redirect from details page")
	}
	if response.Status < 200 || response.Status >= 400 {
		return nil, fmt.Errorf("download: details returned HTTP %d", response.Status)
	}
	return response, nil
}

func (c *DefinitionConnector) extractDownloadCandidates(response *Response) ([]resolvedDownload, error) {
	for _, target := range c.def.Download.Extract {
		results, ok, err := c.extractDownloadTargets(response, target)
		if err != nil {
			return nil, errors.New("download: details extractor failed")
		}
		if ok && len(results) > 0 {
			return results, nil
		}
	}
	return nil, nil
}

func (c *DefinitionConnector) extractDownloadTarget(response *Response, target definition.DownloadTarget) (resolvedDownload, bool, error) {
	results, ok, err := c.extractDownloadTargets(response, target)
	if err != nil || !ok || len(results) == 0 {
		return resolvedDownload{}, ok, err
	}
	return results[0], true, nil
}

func (c *DefinitionConnector) extractDownloadTargets(response *Response, target definition.DownloadTarget) ([]resolvedDownload, bool, error) {
	if target.Magnet != nil {
		values, err := c.extractDetailFields(response, *target.Magnet, target.All)
		if err != nil {
			return nil, false, err
		}
		var out []resolvedDownload
		for _, value := range values {
			value = strings.TrimSpace(value)
			if !strings.HasPrefix(strings.ToLower(value), "magnet:") {
				continue
			}
			out = append(out, resolvedDownload{
				Magnet:   value,
				InfoHash: normalizeInfoHash(value),
			})
		}
		return out, len(out) > 0, nil
	}
	if target.Torrent != nil {
		values, err := c.extractDetailFields(response, *target.Torrent, target.All)
		if err != nil {
			return nil, false, err
		}
		base := response.URL
		if base == nil {
			base = c.baseURL
		}
		var out []resolvedDownload
		for _, value := range values {
			value = strings.TrimSpace(value)
			if value == "" {
				continue
			}
			if strings.HasPrefix(strings.ToLower(value), "magnet:") {
				out = append(out, resolvedDownload{
					Magnet:   value,
					InfoHash: normalizeInfoHash(value),
				})
				continue
			}
			resolved := resolveURL(value, base, c.baseURL)
			if resolved == "" {
				continue
			}
			parsed, err := url.Parse(resolved)
			if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
				continue
			}
			out = append(out, resolvedDownload{DownloadURL: resolved})
		}
		return out, len(out) > 0, nil
	}
	if target.Ed2k != nil {
		values, err := c.extractDetailFields(response, *target.Ed2k, target.All)
		if err != nil {
			return nil, false, err
		}
		var out []resolvedDownload
		seen := make(map[string]struct{})
		for _, value := range values {
			// A selected node can be one link; a body or a script can hold a
			// JavaScript array of them. Scan every ed2k:// URI found in the
			// value instead of parsing the whole text as a single link.
			for _, raw := range ed2kLinksInText(value) {
				link, ok := parseEd2kFileLink(raw)
				if !ok {
					continue
				}
				if _, duplicate := seen[link.Hash]; duplicate {
					continue
				}
				seen[link.Hash] = struct{}{}
				out = append(out, resolvedDownload{
					Ed2k:     canonicalEd2kLink(link),
					Ed2kHash: link.Hash,
					Title:    link.Name,
					Size:     link.Size,
				})
			}
		}
		return out, len(out) > 0, nil
	}
	return nil, false, nil
}

// extractDetailFields applies one Field to a whole details response and
// returns every value when all is true, otherwise only the first non-empty
// one. Each selector is a fallback; path reads a JSON document.
func (c *DefinitionConnector) extractDetailFields(response *Response, field definition.Field, all bool) ([]string, error) {
	if field.Body {
		// The whole response: a page that builds its links in JavaScript has
		// no node to select, and the eD2k extractor scans the text for every
		// ed2k:// URI.
		value := string(response.Body)
		var err error
		value, err = applyFilters(value, field.Filters)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(value) == "" {
			return nil, nil
		}
		return []string{value}, nil
	}
	if strings.TrimSpace(field.Path) != "" {
		root, err := decodeJSON(response.Body)
		if err != nil {
			return nil, err
		}
		value, err := applyFilters(jsonValueString(walkJSONPath(root, field.Path)), field.Filters)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(value) == "" {
			return nil, nil
		}
		return []string{value}, nil
	}

	body := htmlBody(response)
	var out []string
	for _, selector := range field.Selector {
		if all {
			values, err := extractHTMLValues(body, selector, field.Attr)
			if err != nil {
				return nil, err
			}
			for _, value := range values {
				filtered, err := applyFilters(value, field.Filters)
				if err != nil {
					return nil, err
				}
				if strings.TrimSpace(filtered) != "" {
					out = append(out, filtered)
				}
			}
			if len(out) > 0 {
				break
			}
			continue
		}
		value, err := extractHTMLValue(body, selector, field.Attr)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(value) == "" {
			continue
		}
		value, err = applyFilters(value, field.Filters)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(value) != "" {
			out = append(out, value)
		}
		if len(out) > 0 {
			break
		}
	}
	return out, nil
}

// extractDetailField keeps the historical single-value helper.
func (c *DefinitionConnector) extractDetailField(response *Response, field definition.Field) (string, error) {
	values, err := c.extractDetailFields(response, field, false)
	if err != nil || len(values) == 0 {
		return "", err
	}
	return values[0], nil
}

// downloadFromSection resolves a details page at grab time using the
// definition's download section.
func (c *DefinitionConnector) downloadFromSection(ctx context.Context, item Item) (Payload, error) {
	rawURL := resolveURL(item.DetailsURL, c.baseURL)
	if rawURL == "" {
		return Payload{}, errors.New("download: invalid details URL")
	}
	season, episode := c.requestedSeasonEpisode(item)
	result, err := c.resolveDetailsCached(ctx, rawURL, season, episode)
	if err != nil {
		return Payload{}, err
	}
	if result.Magnet != "" {
		return Payload{Magnet: result.Magnet}, nil
	}
	if result.Ed2k != "" {
		return Payload{Ed2k: result.Ed2k}, nil
	}
	if result.DownloadURL != "" {
		return c.downloadURL(ctx, result.DownloadURL, item.Title, rawURL)
	}
	return Payload{}, errors.New("download: no download target on details page")
}

// fileNameIsRelease reports whether a file of a topic names its episode
// ("S06E12", "6x12") and shares a word with the topic title, so the file name
// alone says which series and episode it is.
func fileNameIsRelease(topic, name string) bool {
	normalized := normalizeIndexText(name)
	words := strings.Fields(normalized)
	episode := false
	for _, word := range words {
		if isEpisodeToken(word) && strings.ContainsAny(word, "ex") {
			episode = true
			break
		}
	}
	if !episode {
		return false
	}
	for _, token := range indexQueryTokens(topic) {
		if len(token) >= 3 && !isEpisodeToken(token) && indexWordPrefix(words, token) {
			return true
		}
	}
	return false
}
