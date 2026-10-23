package importers

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// jackettAPIBodyLimit bounds the t=indexers document. A large Jackett
// installation stays far below this.
const jackettAPIBodyLimit = 16 << 20

// jackettProbeConcurrency caps the read-only per-indexer caps probes. Jackett
// has no bulk health endpoint, so the importer asks each configured indexer;
// keeping this small avoids loading the manager while it is also serving.
const jackettProbeConcurrency = 4

type jackettIndexersDocument struct {
	XMLName  xml.Name             `xml:"indexers"`
	Indexers []jackettIndexerItem `xml:"indexer"`
}

type jackettIndexerItem struct {
	ID         string             `xml:"id,attr"`
	Configured string             `xml:"configured,attr"`
	Title      string             `xml:"title"`
	Desc       string             `xml:"description"`
	Link       string             `xml:"link"`
	Language   string             `xml:"language"`
	Type       string             `xml:"type"`
	Caps       jackettIndexerCaps `xml:"caps"`
}

type jackettIndexerCaps struct {
	Categories []jackettCategory `xml:"categories>category"`
}

type jackettCategory struct {
	ID       string            `xml:"id,attr"`
	Name     string            `xml:"name,attr"`
	Subcats  []jackettCategory `xml:"subcat"`
	Children []jackettCategory `xml:"category"`
}

// FromJackettAPI reads the configured indexers from the live Jackett Torznab
// endpoint (GET .../api/v2.0/indexers/all/results/torznab/api?t=indexers&configured=true).
// The copied Jackett data directory stays available as an offline fallback,
// but the live list is the source of truth: it reflects what the running
// instance actually serves.
//
// Every configured indexer is probed with a read-only caps request. An
// indexer that fails the probe (for example HTTP 500 or "Indexer is not
// configured") is still returned, but disabled with a warning that explains
// why, so the coordinator sees it in the import table instead of a bridge
// that silently breaks later.
func FromJackettAPI(ctx context.Context, baseURL, apiKey string, client *http.Client) (Plan, error) {
	const op = "jackett"

	plan := Plan{Source: op}
	if err := ctx.Err(); err != nil {
		return Plan{}, fmt.Errorf("%s: %w", op, err)
	}
	root, err := normalizeManagerBase(baseURL)
	if err != nil {
		return Plan{}, fmt.Errorf("%s: %s", op, redactSecrets(err.Error(), apiKey))
	}
	plan.ManagerURL = root
	plan.ManagerAPIKey = apiKey

	httpClient := client
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	document, err := fetchJackettIndexers(ctx, httpClient, root, apiKey)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return Plan{}, fmt.Errorf("%s: %w", op, ctxErr)
		}
		return Plan{}, fmt.Errorf("%s: %s", op, redactSecrets(err.Error(), apiKey))
	}

	records := make([]indexerRecord, 0, len(document.Indexers))
	for _, item := range document.Indexers {
		id := strings.TrimSpace(item.ID)
		if id == "" || !safeJackettID(id) {
			addWarning(&plan.Warnings,
				fmt.Sprintf("jackett: ignored an indexer with an unsafe id %q", id), apiKey)
			continue
		}
		configured := true
		if value, ok := boolValue(strings.TrimSpace(item.Configured)); ok {
			configured = value
		}
		if !configured {
			continue
		}
		name := strings.TrimSpace(item.Title)
		if name == "" {
			name = id
		}
		siteURL := strings.TrimSpace(item.Link)
		if siteURL == "" {
			addWarning(&plan.Warnings,
				fmt.Sprintf("jackett: indexer %q has no site URL, skipped", name), apiKey)
			continue
		}
		records = append(records, indexerRecord{
			sourceID: id,
			indexer: Indexer{
				Name:           name,
				Implementation: "Jackett",
				Protocol:       ProtocolTorrent,
				DefinitionHint: slugify(id),
				RemoteID:       id,
				BaseURL:        root + "/api/v2.0/indexers/" + url.PathEscape(id) + "/results/torznab/",
				SiteURL:        siteURL,
				APIKey:         apiKey,
				Categories:     jackettCategoryIDs(item.Caps.Categories),
				Priority:       25,
				Enabled:        true,
				Fields: map[string]string{
					"site_link":   siteURL,
					"site_type":   strings.TrimSpace(item.Type),
					"language":    strings.TrimSpace(item.Language),
					"description": strings.TrimSpace(item.Desc),
				},
			},
		})
	}
	plan.Indexers = sortIndexerRecords(records)

	probeJackettIndexers(ctx, httpClient, root, apiKey, plan.Indexers, &plan.Warnings)
	plan.Warnings = sanitizeWarnings(plan.Warnings, apiKey)
	return plan, nil
}

// normalizeManagerBase validates a manager base URL and strips credentials,
// query, fragment and a redundant /api or /api/v1 suffix.
func normalizeManagerBase(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("base URL is empty")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", errors.New("base URL is not a valid URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", errors.New("base URL must use http or https")
	}
	if parsed.Host == "" {
		return "", errors.New("base URL has no host")
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.Fragment = ""
	path := strings.TrimRight(parsed.Path, "/")
	path = strings.TrimSuffix(path, "/api/v1")
	path = strings.TrimSuffix(path, "/api")
	parsed.Path = path
	parsed.RawPath = ""
	return strings.TrimRight(parsed.String(), "/"), nil
}

func fetchJackettIndexers(ctx context.Context, client *http.Client, root, apiKey string) (jackettIndexersDocument, error) {
	// Jackett authenticates the Torznab API with ?apikey=; an X-Api-Key header
	// alone is rejected with <error code="100" description="Invalid API Key">.
	// The URL never reaches a warning or an error: sanitizeURL drops the query.
	query := url.Values{}
	query.Set("t", "indexers")
	query.Set("configured", "true")
	if apiKey != "" {
		query.Set("apikey", apiKey)
	}
	endpoint := root + "/api/v2.0/indexers/all/results/torznab/api?" + query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return jackettIndexersDocument{}, fmt.Errorf("build request: %w", err)
	}
	request.Header.Set("Accept", "application/xml, application/json;q=0.8, */*;q=0.5")
	if apiKey != "" {
		request.Header.Set("X-Api-Key", apiKey)
	}
	response, err := client.Do(request)
	if err != nil {
		return jackettIndexersDocument{}, fmt.Errorf("%s %s: %s", http.MethodGet,
			sanitizeURL(endpoint), redactSecrets(err.Error(), apiKey))
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return jackettIndexersDocument{}, &httpStatusError{
			Method: http.MethodGet, URL: endpoint, Status: response.StatusCode,
		}
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, jackettAPIBodyLimit))
	if err != nil {
		return jackettIndexersDocument{}, fmt.Errorf("read response: %s", redactSecrets(err.Error(), apiKey))
	}
	var document jackettIndexersDocument
	if err := xml.Unmarshal(body, &document); err != nil {
		return jackettIndexersDocument{}, fmt.Errorf("parse t=indexers XML: %s", redactSecrets(err.Error(), apiKey))
	}
	if strings.TrimSpace(document.XMLName.Local) != "indexers" {
		return jackettIndexersDocument{}, errors.New("unexpected t=indexers response")
	}
	return document, nil
}

// probeJackettIndexers tests every indexer with t=caps and disables the ones
// that fail. It never changes the remote instance: the requests are GETs.
func probeJackettIndexers(ctx context.Context, client *http.Client, root, apiKey string, indexers []Indexer, warnings *[]string) {
	if len(indexers) == 0 {
		return
	}
	limit := jackettProbeConcurrency
	if limit < 1 {
		limit = 1
	}
	semaphore := make(chan struct{}, limit)
	var wg sync.WaitGroup
	var mu sync.Mutex
	for i := range indexers {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		go func(index *Indexer) {
			defer wg.Done()
			semaphore <- struct{}{}
			defer func() { <-semaphore }()
			reason := jackettProbe(ctx, client, root, apiKey, index.RemoteID)
			if reason == "" {
				return
			}
			mu.Lock()
			index.Enabled = false
			index.DisabledReason = reason
			addWarning(warnings,
				fmt.Sprintf("jackett: %q imported disabled: %s", index.Name, reason), apiKey)
			mu.Unlock()
		}(&indexers[i])
	}
	wg.Wait()
}

// jackettProbe returns an empty string when the indexer answers caps, or a
// short redacted reason when it does not.
func jackettProbe(ctx context.Context, client *http.Client, root, apiKey, id string) string {
	query := url.Values{}
	query.Set("t", "caps")
	if apiKey != "" {
		query.Set("apikey", apiKey)
	}
	endpoint := root + "/api/v2.0/indexers/" + url.PathEscape(id) +
		"/results/torznab/api?" + query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "invalid caps request"
	}
	request.Header.Set("Accept", "application/xml, */*;q=0.5")
	if apiKey != "" {
		request.Header.Set("X-Api-Key", apiKey)
	}
	response, err := client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return ""
		}
		return "caps request failed"
	}
	defer response.Body.Close()
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		return ""
	}
	// Read a small slice only to recognise Jackett's configuration error; the
	// body itself is never echoed because it can contain account details.
	body, _ := io.ReadAll(io.LimitReader(response.Body, 2048))
	text := strings.ToLower(string(body))
	switch {
	case strings.Contains(text, "not configured"):
		return fmt.Sprintf("HTTP %d, indexer is not configured", response.StatusCode)
	case response.StatusCode == http.StatusInternalServerError:
		return "HTTP 500"
	default:
		return "HTTP " + strconv.Itoa(response.StatusCode)
	}
}

func jackettCategoryIDs(categories []jackettCategory) []int {
	var ids []int
	var walk func([]jackettCategory)
	walk = func(items []jackettCategory) {
		for _, item := range items {
			if id, err := strconv.Atoi(strings.TrimSpace(item.ID)); err == nil && id > 0 {
				ids = append(ids, id)
			}
			walk(item.Subcats)
			walk(item.Children)
		}
	}
	walk(categories)
	seen := make(map[int]bool, len(ids))
	out := make([]int, 0, len(ids))
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	sort.Ints(out)
	return out
}
