package importers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// prowlarrBodyLimit bounds how much of a Prowlarr response is read. A normal
// installation with a few hundred indexers stays far below this.
const prowlarrBodyLimit = 16 << 20

// httpStatusError is returned for non-2xx responses. It intentionally keeps
// only the method, the sanitized URL and the status code: response bodies are
// never echoed because they can contain credentials.
type httpStatusError struct {
	Method string
	URL    string
	Status int
}

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("%s %s: unexpected HTTP status %d", e.Method, sanitizeURL(e.URL), e.Status)
}

// FromProwlarr reads the indexers, applications and indexer proxies of a
// running Prowlarr instance. It uses only the official v1 API and returns a
// neutral plan; nothing is written to the store.
//
// A malformed indexer is reported as a warning and skipped. A failure to list
// the indexers is fatal because the import would otherwise look successful
// while importing nothing. Applications and proxies are optional: an instance
// whose API key cannot read them still imports its indexers, with a warning.
func FromProwlarr(ctx context.Context, baseURL, apiKey string, client *http.Client) (Plan, error) {
	const op = "prowlarr"

	plan := Plan{Source: op}
	if err := ctx.Err(); err != nil {
		return Plan{}, fmt.Errorf("%s: %w", op, err)
	}
	root, err := normalizeProwlarrBase(baseURL)
	if err != nil {
		return Plan{}, fmt.Errorf("%s: %s", op, redactSecrets(err.Error(), apiKey))
	}
	plan.ManagerURL = root
	plan.ManagerAPIKey = apiKey
	httpClient := client
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}

	indexerEntries, err := fetchJSONArray(ctx, httpClient, root+"/api/v1/indexer", apiKey)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return Plan{}, fmt.Errorf("%s: %w", op, ctxErr)
		}
		return Plan{}, fmt.Errorf("%s: list indexers: %s", op, redactSecrets(err.Error(), apiKey))
	}
	plan.Indexers = parseProwlarrIndexers(indexerEntries, &plan.Warnings, apiKey)

	if appEntries, appErr := fetchJSONArray(ctx, httpClient, root+"/api/v1/applications", apiKey); appErr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return Plan{}, fmt.Errorf("%s: %w", op, ctxErr)
		}
		optionalEndpointWarning(&plan, "applications", appErr, apiKey)
	} else {
		plan.Applications = parseProwlarrApplications(appEntries, &plan.Warnings, apiKey)
	}

	plan.Warnings = sanitizeWarnings(plan.Warnings, apiKey)
	return plan, nil
}

// optionalEndpointWarning reports a failed optional endpoint, but stays quiet
// on 404: older or minimal servers simply do not implement it.
func optionalEndpointWarning(plan *Plan, label string, err error, secrets ...string) {
	var status *httpStatusError
	if errors.As(err, &status) && status.Status == http.StatusNotFound {
		return
	}
	addWarning(&plan.Warnings, fmt.Sprintf("prowlarr: %s unavailable: %s", label, err.Error()), secrets...)
}

// normalizeProwlarrBase validates the base URL and removes credentials and a
// redundant /api/v1 suffix. It never echoes the raw value: it may contain
// basic-auth credentials.
func normalizeProwlarrBase(raw string) (string, error) {
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
	parsed.Path = path
	parsed.RawPath = ""
	return strings.TrimRight(parsed.String(), "/"), nil
}

func fetchJSONArray(ctx context.Context, client *http.Client, endpoint, apiKey string) ([]json.RawMessage, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	if apiKey != "" {
		request.Header.Set("X-Api-Key", apiKey)
	}

	response, err := client.Do(request)
	if err != nil {
		detail := err.Error()
		var urlErr *url.Error
		if errors.As(err, &urlErr) && urlErr.Err != nil {
			detail = urlErr.Err.Error()
		}
		return nil, fmt.Errorf("%s %s: %s", http.MethodGet,
			sanitizeURL(endpoint), redactSecrets(detail, apiKey))
	}
	defer response.Body.Close()

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, &httpStatusError{Method: http.MethodGet, URL: endpoint, Status: response.StatusCode}
	}

	body, err := io.ReadAll(io.LimitReader(response.Body, prowlarrBodyLimit))
	if err != nil {
		return nil, fmt.Errorf("read response: %s", redactSecrets(err.Error(), apiKey))
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, errors.New("empty response")
	}

	var entries []json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&entries); err != nil {
		return nil, fmt.Errorf("parse JSON response: %s", redactSecrets(err.Error(), apiKey))
	}
	return entries, nil
}

func parseProwlarrIndexers(entries []json.RawMessage, warnings *[]string, secrets ...string) []Indexer {
	records := make([]indexerRecord, 0, len(entries))
	for index, raw := range entries {
		object, ok := decodeObject(raw)
		if !ok {
			addWarning(warnings, fmt.Sprintf("prowlarr: indexer entry %d is not a JSON object, skipped", index+1), secrets...)
			continue
		}

		id := stringValue(object["id"])
		name := strings.TrimSpace(stringValue(object["name"]))
		label := name
		if label == "" {
			label = id
		}
		if label == "" {
			label = strconv.Itoa(index + 1)
		}

		implementation := strings.TrimSpace(stringValue(object["implementation"]))
		if implementation == "" {
			addWarning(warnings, fmt.Sprintf("prowlarr: indexer %q has no implementation, skipped", label), secrets...)
			continue
		}
		if name == "" {
			addWarning(warnings, fmt.Sprintf("prowlarr: indexer %q has no name, skipped", label), secrets...)
			continue
		}

		fields := parseFields(object["fields"])
		categories := parseCategories(capabilityCategories(object))
		if len(categories) == 0 {
			categories = parseCategories(object["categories"])
		}
		if len(categories) == 0 {
			categories = parseCategories(fieldValue(fields, "categories", "category"))
		}

		enabled := true
		if raw, ok := object["enable"]; ok {
			if value, valid := boolValue(raw); valid {
				enabled = value
			}
		} else if raw, ok := object["enabled"]; ok {
			if value, valid := boolValue(raw); valid {
				enabled = value
			}
		}

		cardigannID := fieldValue(fields, "definitionId", "definitionName")
		siteURL := fieldValue(fields, "baseUrl", "base_url", "url")
		record := indexerRecord{
			sourceID: id,
			indexer: Indexer{
				Name:           name,
				Implementation: implementation,
				Protocol:       normalizeProtocol(stringValue(object["protocol"]), implementation),
				DefinitionHint: definitionHint(implementation, name, cardigannID),
				RemoteID:       id,
				BaseURL:        siteURL,
				SiteURL:        siteURL,
				APIKey:         fieldValue(fields, "apiKey", "api_key"),
				Username:       fieldValue(fields, "username", "user"),
				Password:       fieldValue(fields, "password"),
				Categories:     categories,
				Tags:           parseTags(object["tags"]),
				Priority:       intValue(object["priority"]),
				Enabled:        enabled,
				Fields:         publicFields(fields),
			},
		}
		records = append(records, record)
	}
	return sortIndexerRecords(records)
}

func capabilityCategories(object map[string]any) any {
	capabilities, ok := object["capabilities"].(map[string]any)
	if !ok {
		return nil
	}
	return capabilities["categories"]
}

var applicationKinds = map[string]string{
	"sonarr":        "sonarr",
	"radarr":        "radarr",
	"lidarr":        "lidarr",
	"readarr":       "readarr",
	"whisparr":      "whisparr",
	"mylar":         "mylar3",
	"mylar3":        "mylar3",
	"lazylibrarian": "lazylibrarian",
}

func applicationKind(implementation string) string {
	normalized := strings.ToLower(strings.TrimSpace(implementation))
	normalized = strings.NewReplacer("_", "", "-", "", " ", "").Replace(normalized)
	return applicationKinds[normalized]
}

func parseProwlarrApplications(entries []json.RawMessage, warnings *[]string, secrets ...string) []Application {
	out := make([]Application, 0, len(entries))
	for index, raw := range entries {
		object, ok := decodeObject(raw)
		if !ok {
			addWarning(warnings, fmt.Sprintf("prowlarr: application entry %d is not a JSON object, skipped", index+1), secrets...)
			continue
		}
		implementation := strings.TrimSpace(stringValue(object["implementation"]))
		kind := applicationKind(implementation)
		name := strings.TrimSpace(stringValue(object["name"]))
		if name == "" {
			name = implementation
		}
		if kind == "" {
			addWarning(warnings, fmt.Sprintf("prowlarr: application %q has unsupported implementation %q, skipped", name, implementation), secrets...)
			continue
		}

		fields := parseFields(object["fields"])
		baseURL := fieldValue(fields, "baseUrl", "base_url", "url")
		if baseURL == "" {
			addWarning(warnings, fmt.Sprintf("prowlarr: application %q has no URL, skipped", name), secrets...)
			continue
		}

		syncLevel := strings.TrimSpace(stringValue(object["syncLevel"]))
		if syncLevel == "" {
			syncLevel = strings.TrimSpace(stringValue(object["sync_level"]))
		}
		if syncLevel == "" {
			syncLevel = "full"
		}

		out = append(out, Application{
			Kind:      kind,
			Name:      name,
			URL:       baseURL,
			APIKey:    fieldValue(fields, "apiKey", "api_key"),
			SyncLevel: syncLevel,
		})
	}
	sortApplications(out)
	return out
}
