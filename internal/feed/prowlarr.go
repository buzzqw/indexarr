package feed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"github.com/buzzqw/indexarr/internal/textutil"
)

// isProwlarrJSON reports whether the resolved endpoint is Prowlarr's JSON
// search API rather than a Torznab XML endpoint.
func (c *Client) isProwlarrJSON() bool {
	return strings.Contains(c.endpoint, "/api/v1/search")
}

func (c *Client) prowlarrBase() string {
	return strings.TrimSuffix(c.endpoint, "/api/v1/search")
}

// fetchProwlarr uses the JSON search API when a query is available and falls
// back to the per-indexer Torznab feeds. With no query (the periodic "latest"
// mode) only the Torznab feeds can list recent releases.
func (c *Client) fetchProwlarr(ctx context.Context, query string, limit int) ([]Item, error) {
	query = strings.TrimSpace(query)
	if query != "" {
		items, err := c.fetchProwlarrJSON(ctx, query, limit)
		if err == nil && len(items) > 0 {
			return items, nil
		}
		fallback, fallbackErr := c.fetchProwlarrTorznab(ctx, query, limit)
		if fallbackErr == nil && len(fallback) > 0 {
			return fallback, nil
		}
		switch {
		case err != nil && fallbackErr != nil:
			return nil, fmt.Errorf("Prowlarr search failed: %v; per-indexer search failed: %w", err, fallbackErr)
		case fallbackErr != nil:
			return nil, fallbackErr
		case err != nil:
			return nil, err
		}
		return items, nil
	}
	return c.fetchProwlarrTorznab(ctx, "", limit)
}

func (c *Client) fetchProwlarrJSON(ctx context.Context, query string, limit int) ([]Item, error) {
	values := url.Values{}
	values.Set("query", query)
	values.Set("type", "search")
	values.Set("limit", strconv.Itoa(limit))
	values.Set("apikey", c.cfg.APIKey)
	if len(c.cfg.Categories) > 0 {
		values.Set("categories", joinInts(c.cfg.Categories))
	}
	body, err := getBody(ctx, c.http, withQuery(c.endpoint, values), c.options())
	if err != nil {
		return nil, err
	}
	if LooksLikeHTML(body) {
		return nil, fmt.Errorf("%s returned HTML instead of Prowlarr JSON; check the URL and the API key", hostLabel(c.endpoint))
	}
	var raw []map[string]any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&raw); err != nil {
		return nil, fmt.Errorf("parse Prowlarr response: %w", err)
	}
	return c.itemsFromProwlarr(ctx, raw), nil
}

func (c *Client) itemsFromProwlarr(ctx context.Context, raw []map[string]any) []Item {
	items := make([]Item, 0, len(raw))
	resolved := 0
	for _, entry := range raw {
		if protocol := strings.ToLower(jsonString(entry, "protocol")); protocol != "" && protocol != "torrent" {
			continue
		}
		title := strings.TrimSpace(jsonString(entry, "title"))
		if title == "" {
			continue
		}
		magnet := jsonString(entry, "magnetUrl")
		if !strings.HasPrefix(strings.ToLower(magnet), "magnet:") {
			// Prowlarr can expose a proxy URL that redirects to the magnet.
			if resolved < 12 && isHTTPURL(magnet) {
				resolved++
				magnet = resolveRedirectMagnet(ctx, c.http, magnet, c.options())
			} else {
				magnet = ""
			}
		}
		hash := NormalizeInfoHash(jsonString(entry, "infoHash"))
		if hash == "" {
			hash = MagnetHash(magnet)
		}
		if magnet == "" && hash != "" {
			magnet = MagnetFromHash(hash, title)
		}
		downloadURL := strings.TrimSpace(jsonString(entry, "downloadUrl"))
		if !isHTTPURL(downloadURL) {
			downloadURL = ""
		}
		if magnet == "" && downloadURL == "" && hash == "" {
			continue
		}
		seeders := jsonIntKnown(entry, "seeders")
		leechers := jsonIntKnown(entry, "leechers")
		if leechers < 0 {
			leechers = jsonIntKnown(entry, "peers")
		}
		items = append(items, Item{
			Title:      title,
			GUID:       firstNonEmpty(jsonString(entry, "guid"), downloadURL, magnet),
			Link:       firstNonEmpty(downloadURL, magnet),
			Magnet:     magnet,
			TorrentURL: downloadURL,
			InfoHash:   hash,
			Size:       jsonInt(entry, "size"),
			Seeders:    seeders,
			Leechers:   leechers,
			Category:   prowlarrCategory(entry),
			Published:  ParseTime(jsonString(entry, "publishDate")),
			IMDbID:     textutil.NormalizeID(jsonString(entry, "imdbId")),
			TMDbID:     textutil.NormalizeID(jsonString(entry, "tmdbId")),
			TVDbID:     textutil.NormalizeID(jsonString(entry, "tvdbId")),
		})
	}
	return items
}

// fetchProwlarrTorznab queries every enabled torrent indexer of a Prowlarr
// instance through its Torznab endpoint. It is the only way to obtain recent
// releases without a search term.
func (c *Client) fetchProwlarrTorznab(ctx context.Context, query string, limit int) ([]Item, error) {
	indexers, err := c.prowlarrIndexers(ctx)
	if err != nil {
		return nil, err
	}
	if len(indexers) == 0 {
		return nil, fmt.Errorf("Prowlarr has no enabled torrent indexer")
	}
	base := c.prowlarrBase()
	type job struct{ id int64 }
	jobs := make(chan job)
	var mu sync.Mutex
	var items []Item
	var firstErr error
	workers := 3
	if workers > len(indexers) {
		workers = len(indexers)
	}
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range jobs {
				endpoint := base + "/" + strconv.FormatInt(job.id, 10) + "/api"
				results, err := c.fetchTorznab(ctx, endpoint, query, limit)
				mu.Lock()
				if err != nil {
					if firstErr == nil {
						firstErr = err
					}
				} else {
					items = append(items, results...)
				}
				mu.Unlock()
			}
		}()
	}
	for _, id := range indexers {
		select {
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			return nil, ctx.Err()
		case jobs <- job{id: id}:
		}
	}
	close(jobs)
	wg.Wait()
	if len(items) == 0 && firstErr != nil {
		return nil, firstErr
	}
	return items, nil
}

// prowlarrIndexers lists the enabled torrent indexers of a Prowlarr instance.
func (c *Client) prowlarrIndexers(ctx context.Context) ([]int64, error) {
	values := url.Values{}
	values.Set("apikey", c.cfg.APIKey)
	body, err := getBody(ctx, c.http, withQuery(c.prowlarrBase()+"/api/v1/indexer", values), c.options())
	if err != nil {
		return nil, err
	}
	var raw []map[string]any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&raw); err != nil {
		return nil, fmt.Errorf("parse Prowlarr indexer list: %w", err)
	}
	var ids []int64
	for _, entry := range raw {
		if enabled, ok := entry["enable"].(bool); ok && !enabled {
			continue
		}
		protocol := strings.ToLower(jsonString(entry, "protocol"))
		if protocol != "" && protocol != "torrent" {
			continue
		}
		id := jsonInt(entry, "id")
		if id <= 0 {
			continue
		}
		ids = append(ids, id)
		if len(ids) >= 50 {
			break
		}
	}
	return ids, nil
}

func (c *Client) testProwlarr(ctx context.Context) (string, error) {
	values := url.Values{}
	values.Set("apikey", c.cfg.APIKey)
	body, err := getBody(ctx, c.http, withQuery(c.prowlarrBase()+"/api/v1/system/status", values), c.options())
	if err == nil {
		var status struct {
			Version string `json:"version"`
		}
		if json.Unmarshal(body, &status) == nil {
			return strings.TrimSpace("Prowlarr " + status.Version), nil
		}
		return "Prowlarr reachable", nil
	}
	indexers, listErr := c.prowlarrIndexers(ctx)
	if listErr != nil {
		return "", fmt.Errorf("Prowlarr status failed: %v; indexer list failed: %w", err, listErr)
	}
	return fmt.Sprintf("Prowlarr reachable, %d torrent indexer(s)", len(indexers)), nil
}

func jsonString(entry map[string]any, key string) string {
	value, ok := entry[key]
	if !ok || value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case json.Number:
		return typed.String()
	case float64:
		if typed == float64(int64(typed)) {
			return strconv.FormatInt(int64(typed), 10)
		}
		return strconv.FormatFloat(typed, 'f', -1, 64)
	default:
		return ""
	}
}

func jsonInt(entry map[string]any, key string) int64 {
	value, ok := entry[key]
	if !ok || value == nil {
		return 0
	}
	switch typed := value.(type) {
	case json.Number:
		number, err := typed.Int64()
		if err != nil || number < 0 {
			return 0
		}
		return number
	case float64:
		if typed < 0 {
			return 0
		}
		return int64(typed)
	case string:
		return atoi(typed)
	default:
		return 0
	}
}

// jsonIntKnown is jsonInt with -1 meaning "not reported": MirCrew and other
// indexers may omit seeders entirely, and 0 would wrongly look like a dead
// torrent.
func jsonIntKnown(entry map[string]any, key string) int64 {
	value, ok := entry[key]
	if !ok || value == nil {
		return -1
	}
	switch typed := value.(type) {
	case json.Number:
		number, err := typed.Int64()
		if err != nil {
			return -1
		}
		return number
	case float64:
		return int64(typed)
	case string:
		number, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		if err != nil {
			return -1
		}
		return number
	default:
		return -1
	}
}

func prowlarrCategory(entry map[string]any) string {
	if raw, ok := entry["categories"]; ok {
		if category := categoryFromJSON(raw); category != "" {
			return category
		}
	}
	return NormalizeCategory(jsonString(entry, "category"))
}

func categoryFromJSON(raw any) string {
	switch typed := raw.(type) {
	case []any:
		for _, element := range typed {
			if category := categoryFromJSON(element); category != "" {
				return category
			}
		}
	case map[string]any:
		return NormalizeCategory(jsonString(typed, "id"))
	case json.Number:
		return NormalizeCategory(typed.String())
	case float64:
		return NormalizeCategory(strconv.FormatInt(int64(typed), 10))
	case string:
		return NormalizeCategory(typed)
	}
	return ""
}
