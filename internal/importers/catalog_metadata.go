package importers

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// CatalogIndexer is the small slice of a manager's indexer metadata that the
// site catalog needs: the display fields, the site address, the privacy type,
// the language and the Torznab categories. It deliberately carries no
// credentials and no Cardigann definition: the catalog generator reads this
// metadata, never a Cardigann YAML file.
type CatalogIndexer struct {
	SourceID    string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	URL         string   `json:"url"`
	URLs        []string `json:"urls,omitempty"`
	Type        string   `json:"type"`
	Language    string   `json:"language,omitempty"`
	Categories  []int    `json:"categories,omitempty"`
	Configured  bool     `json:"configured"`
	Priority    int      `json:"priority,omitempty"`
}

// FetchJackettCatalog reads the complete indexer inventory from a running
// Jackett installation. It uses the documented read-only Torznab endpoint
// with configured=false, so unconfigured indexers are included too. The API
// key is sent as a query parameter because Jackett rejects a bare header; the
// value is never echoed in an error.
func FetchJackettCatalog(ctx context.Context, baseURL, apiKey string, client *http.Client) ([]CatalogIndexer, error) {
	const op = "jackett"
	root, err := normalizeManagerBase(baseURL)
	if err != nil {
		return nil, fmt.Errorf("%s: %s", op, redactSecrets(err.Error(), apiKey))
	}
	httpClient := client
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	document, err := fetchJackettIndexers(ctx, httpClient, root, apiKey, "false")
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, fmt.Errorf("%s: %w", op, ctxErr)
		}
		return nil, fmt.Errorf("%s: %s", op, redactSecrets(err.Error(), apiKey))
	}

	out := make([]CatalogIndexer, 0, len(document.Indexers))
	for _, item := range document.Indexers {
		id := strings.TrimSpace(item.ID)
		if id == "" || !safeJackettID(id) {
			continue
		}
		name := strings.TrimSpace(item.Title)
		if name == "" {
			name = id
		}
		siteURL := strings.TrimSpace(item.Link)
		if siteURL == "" {
			continue
		}
		configured := false
		if value, ok := boolValue(strings.TrimSpace(item.Configured)); ok {
			configured = value
		}
		out = append(out, CatalogIndexer{
			SourceID:    id,
			Name:        name,
			Description: strings.TrimSpace(item.Desc),
			URL:         siteURL,
			URLs:        []string{siteURL},
			Type:        strings.TrimSpace(item.Type),
			Language:    strings.TrimSpace(item.Language),
			Categories:  jackettCategoryIDs(item.Caps.Categories),
			Configured:  configured,
		})
	}
	return out, nil
}

// FetchProwlarrCatalog reads the indexer inventory from Prowlarr's v1 API.
// The returned entries contain the public site metadata only; API keys and
// usernames from the indexer fields are intentionally not copied.
func FetchProwlarrCatalog(ctx context.Context, baseURL, apiKey string, client *http.Client) ([]CatalogIndexer, error) {
	const op = "prowlarr"
	root, err := normalizeProwlarrBase(baseURL)
	if err != nil {
		return nil, fmt.Errorf("%s: %s", op, redactSecrets(err.Error(), apiKey))
	}
	httpClient := client
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	entries, err := fetchJSONArray(ctx, httpClient, root+"/api/v1/indexer", apiKey)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, fmt.Errorf("%s: %w", op, ctxErr)
		}
		return nil, fmt.Errorf("%s: list indexers: %s", op, redactSecrets(err.Error(), apiKey))
	}

	out := make([]CatalogIndexer, 0, len(entries))
	for index, raw := range entries {
		object, ok := decodeObject(raw)
		if !ok {
			continue
		}
		name := strings.TrimSpace(stringValue(object["name"]))
		if name == "" {
			continue
		}
		id := strings.TrimSpace(stringValue(object["id"]))
		if id == "" {
			id = fmt.Sprintf("prowlarr-%d", index+1)
		}
		urls := catalogURLs(object["indexerUrls"])
		urls = appendUniqueURLs(urls, catalogURLs(object["legacyUrls"]))
		if len(urls) == 0 {
			continue
		}
		categories := parseCategories(capabilityCategories(object))
		if len(categories) == 0 {
			categories = parseCategories(object["categories"])
		}
		enabled := true
		if raw, ok := object["enable"]; ok {
			if value, valid := boolValue(raw); valid {
				enabled = value
			}
		}
		out = append(out, CatalogIndexer{
			SourceID:    id,
			Name:        name,
			Description: strings.TrimSpace(stringValue(object["description"])),
			URL:         urls[0],
			URLs:        urls,
			Type:        normalizeCatalogType(stringValue(object["privacy"])),
			Language:    strings.TrimSpace(stringValue(object["language"])),
			Categories:  categories,
			Configured:  enabled,
			Priority:    intValue(object["priority"]),
		})
	}
	return out, nil
}

func normalizeCatalogType(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "public":
		return "public"
	case "semi-private", "semiprivate", "semi private":
		return "semi-private"
	default:
		return "private"
	}
}

func appendUniqueURLs(dst, extra []string) []string {
	seen := make(map[string]bool, len(dst)+len(extra))
	for _, item := range dst {
		seen[item] = true
	}
	for _, item := range extra {
		if item == "" || seen[item] {
			continue
		}
		seen[item] = true
		dst = append(dst, item)
	}
	return dst
}

func catalogURLs(value any) []string {
	var out []string
	seen := map[string]bool{}
	appendURL := func(raw string) {
		raw = strings.TrimSpace(raw)
		if raw == "" || seen[raw] {
			return
		}
		seen[raw] = true
		out = append(out, raw)
	}
	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			appendURL(stringValue(item))
		}
	case []string:
		for _, item := range typed {
			appendURL(item)
		}
	default:
		appendURL(stringValue(value))
	}
	return out
}
