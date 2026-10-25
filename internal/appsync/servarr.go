package appsync

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// servarrClient talks to Sonarr, Radarr, Lidarr, Readarr and Whisparr. They
// share the v3 indexer API; older Whisparr builds only expose v1. The version
// is resolved by probing /system/status once: a 404 on v3 selects v1, anything
// else keeps v3. Resolving once avoids mistaking a legitimate resource-level
// 404 (for example deleting an already-removed indexer) for an old API.
type servarrClient struct {
	kind   Kind
	name   string
	root   string
	apiKey string
	http   *http.Client

	mu      sync.Mutex
	version string // "", "v3" or "v1"
}

func newServarr(cfg Config, root string) *servarrClient {
	return &servarrClient{
		kind:   cfg.Kind,
		name:   cfg.Name,
		root:   root,
		apiKey: cfg.APIKey,
		http:   cfg.HTTPClient,
	}
}

func (c *servarrClient) apiVersion(ctx context.Context) string {
	c.mu.Lock()
	version := c.version
	c.mu.Unlock()
	if version != "" {
		return version
	}

	version = "v3"
	if status, err := c.rawStatus(ctx, "v3"); err == nil && status == http.StatusNotFound {
		if v1Status, v1Err := c.rawStatus(ctx, "v1"); v1Err == nil && v1Status == http.StatusOK {
			version = "v1"
		}
	}

	c.mu.Lock()
	if c.version == "" {
		c.version = version
	}
	version = c.version
	c.mu.Unlock()
	return version
}

func (c *servarrClient) rawStatus(ctx context.Context, version string) (int, error) {
	status, _, err := c.doVersion(ctx, version, http.MethodGet, "/system/status", nil)
	return status, err
}

// do issues an authenticated request using the resolved API version.
func (c *servarrClient) do(ctx context.Context, method, path string, body []byte) (int, []byte, error) {
	return c.doVersion(ctx, c.apiVersion(ctx), method, path, body)
}

func (c *servarrClient) doVersion(ctx context.Context, version, method, path string, body []byte) (int, []byte, error) {
	endpoint := c.root + "/api/" + version + path
	var reader *bytes.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	var req *http.Request
	var err error
	if reader != nil {
		req, err = http.NewRequestWithContext(ctx, method, endpoint, reader)
	} else {
		req, err = http.NewRequestWithContext(ctx, method, endpoint, nil)
	}
	if err != nil {
		return 0, nil, fmt.Errorf("appsync: %s %s: %w", c.kind, method, err)
	}
	if c.apiKey != "" {
		req.Header.Set("X-Api-Key", c.apiKey)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	status, responseBody, err := execute(ctx, c.http, req, c.apiKey)
	if err != nil {
		return 0, nil, fmt.Errorf("appsync: %s: %s", c.kind, err)
	}
	return status, responseBody, nil
}

// Test verifies connectivity and returns a human-readable app/version string.
func (c *servarrClient) Test(ctx context.Context) (string, error) {
	op := fmt.Sprintf("appsync: %s test", c.kind)
	status, body, err := c.do(ctx, http.MethodGet, "/system/status", nil)
	if err != nil {
		return "", err
	}
	if !isSuccess(status) {
		return "", apiError(op, status, body, c.apiKey)
	}

	var payload struct {
		AppName      string `json:"appName"`
		Version      string `json:"version"`
		InstanceName string `json:"instanceName"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", fmt.Errorf("%s: decode system status: %w", op, err)
	}

	name := strings.TrimSpace(payload.AppName)
	if name == "" {
		name = c.kind.displayName()
	}
	version := strings.TrimSpace(payload.Version)
	if version != "" {
		return name + " " + version, nil
	}
	return name, nil
}

// ListIndexers returns every indexer configured in the application.
func (c *servarrClient) ListIndexers(ctx context.Context) ([]RemoteIndexer, error) {
	op := fmt.Sprintf("appsync: %s list indexers", c.kind)
	status, body, err := c.do(ctx, http.MethodGet, "/indexer", nil)
	if err != nil {
		return nil, err
	}
	if !isSuccess(status) {
		return nil, apiError(op, status, body, c.apiKey)
	}

	var payloads []map[string]any
	if err := json.Unmarshal(body, &payloads); err != nil {
		return nil, fmt.Errorf("%s: decode response: %w", op, err)
	}
	out := make([]RemoteIndexer, 0, len(payloads))
	for _, payload := range payloads {
		out = append(out, remoteFromMap(payload))
	}
	return out, nil
}

// IndexerSnapshot returns the application's raw indexer list for a snapshot.
// The list is validated as a JSON array before it is stored, and the
// application API key is redacted even though it travels only in a header:
// a snapshot must never become a place where a secret is written down.
func (c *servarrClient) IndexerSnapshot(ctx context.Context) ([]byte, error) {
	op := fmt.Sprintf("appsync: %s snapshot indexers", c.kind)
	status, body, err := c.do(ctx, http.MethodGet, "/indexer", nil)
	if err != nil {
		return nil, err
	}
	if !isSuccess(status) {
		return nil, apiError(op, status, body, c.apiKey)
	}
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return []byte("[]"), nil
	}
	if body[0] != '[' || !json.Valid(body) {
		return nil, fmt.Errorf("%s: unexpected response", op)
	}
	if c.apiKey != "" {
		body = bytes.ReplaceAll(body, []byte(c.apiKey), []byte("[REDACTED]"))
	}
	return body, nil
}

// AddIndexer obtains the schema template (when available), merges the desired
// fields and creates the indexer.
func (c *servarrClient) AddIndexer(ctx context.Context, spec IndexerSpec) (RemoteIndexer, error) {
	op := fmt.Sprintf("appsync: %s add indexer", c.kind)
	if strings.TrimSpace(spec.Name) == "" {
		return RemoteIndexer{}, fmt.Errorf("%s: name is required", op)
	}

	schema := c.schemaFor(ctx, spec)
	payload := buildAddPayload(spec, schema)
	encoded, err := json.Marshal(payload)
	if err != nil {
		return RemoteIndexer{}, fmt.Errorf("%s: encode payload: %w", op, err)
	}

	// forceSave skips Servarr's blocking indexer test. indexarr already tests
	// the indexer from its own UI, and a live test can legitimately return no
	// results at the moment of the sync (rate limit, empty query, offline
	// site). Without forceSave one such site aborts the whole reconciliation,
	// exactly what Prowlarr avoids by adding first and reporting health after.
	status, body, err := c.do(ctx, http.MethodPost, "/indexer?forceSave=true", encoded)
	if err != nil {
		return RemoteIndexer{}, err
	}
	if !isSuccess(status) {
		return RemoteIndexer{}, apiError(op, status, body, c.apiKey)
	}

	if len(bytes.TrimSpace(body)) == 0 {
		payload["id"] = 0
		return remoteFromMap(payload), nil
	}
	remote, err := decodeRemote(body)
	if err != nil {
		return RemoteIndexer{}, fmt.Errorf("%s: %w", op, err)
	}
	return remote, nil
}

// UpdateIndexer fetches the full object, merges our settings without dropping
// unknown keys and sends it back. Servarr's PUT requires the complete object.
func (c *servarrClient) UpdateIndexer(ctx context.Context, remoteID int, spec IndexerSpec) (RemoteIndexer, error) {
	op := fmt.Sprintf("appsync: %s update indexer %d", c.kind, remoteID)
	if remoteID <= 0 {
		return RemoteIndexer{}, fmt.Errorf("%s: invalid remote ID", op)
	}

	status, body, err := c.do(ctx, http.MethodGet, fmt.Sprintf("/indexer/%d", remoteID), nil)
	if err != nil {
		return RemoteIndexer{}, err
	}
	if !isSuccess(status) {
		return RemoteIndexer{}, apiError(op, status, body, c.apiKey)
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return RemoteIndexer{}, fmt.Errorf("%s: decode existing indexer: %w", op, err)
	}
	if payload == nil {
		return RemoteIndexer{}, fmt.Errorf("%s: empty indexer payload", op)
	}
	payload["id"] = remoteID
	applyUpdateSpec(payload, spec)

	encoded, err := json.Marshal(payload)
	if err != nil {
		return RemoteIndexer{}, fmt.Errorf("%s: encode payload: %w", op, err)
	}

	// forceSave bypasses the blocking test for the same reason as AddIndexer.
	status, body, err = c.do(ctx, http.MethodPut,
		fmt.Sprintf("/indexer/%d?forceSave=true", remoteID), encoded)
	if err != nil {
		return RemoteIndexer{}, err
	}
	if !isSuccess(status) {
		return RemoteIndexer{}, apiError(op, status, body, c.apiKey)
	}

	if len(bytes.TrimSpace(body)) == 0 {
		return remoteFromMap(payload), nil
	}
	remote, err := decodeRemote(body)
	if err != nil {
		return RemoteIndexer{}, fmt.Errorf("%s: %w", op, err)
	}
	if remote.ID == 0 {
		remote.ID = remoteID
	}
	return remote, nil
}

// DeleteIndexer removes an indexer. A 404 is treated as success so repeated
// reconciliation is idempotent.
func (c *servarrClient) DeleteIndexer(ctx context.Context, remoteID int) error {
	op := fmt.Sprintf("appsync: %s delete indexer %d", c.kind, remoteID)
	if remoteID <= 0 {
		return fmt.Errorf("%s: invalid remote ID", op)
	}
	status, body, err := c.do(ctx, http.MethodDelete, fmt.Sprintf("/indexer/%d", remoteID), nil)
	if err != nil {
		return err
	}
	if status == http.StatusNotFound {
		return nil
	}
	if !isSuccess(status) {
		return apiError(op, status, body, c.apiKey)
	}
	return nil
}

// ResolveTags returns one Servarr tag ID per label, creating the missing tags.
// Sonarr/Radarr/Lidarr/Readarr/Whisparr store indexer tags as objects behind
// /api/v3/tag, unlike indexarr where a tag is a free-form label. Applications
// that do not expose /tag (404) leave labels unresolved, so tags are simply
// not sent rather than failing the whole sync.
func (c *servarrClient) ResolveTags(ctx context.Context, labels []string) ([]int, error) {
	op := fmt.Sprintf("appsync: %s resolve tags", c.kind)
	wanted := make([]string, 0, len(labels))
	seen := make(map[string]bool, len(labels))
	for _, label := range labels {
		label = strings.TrimSpace(label)
		if label == "" {
			continue
		}
		key := strings.ToLower(label)
		if seen[key] {
			continue
		}
		seen[key] = true
		wanted = append(wanted, label)
	}
	if len(wanted) == 0 {
		return []int{}, nil
	}

	status, body, err := c.do(ctx, http.MethodGet, "/tag", nil)
	if err != nil {
		return nil, err
	}
	if status == http.StatusNotFound {
		return nil, nil
	}
	if !isSuccess(status) {
		return nil, apiError(op, status, body, c.apiKey)
	}
	byLabel := decodeTagLabels(body)

	out := make([]int, len(labels))
	for i, label := range labels {
		label = strings.TrimSpace(label)
		if label == "" {
			continue
		}
		key := strings.ToLower(label)
		if id, ok := byLabel[key]; ok {
			out[i] = id
			continue
		}
		encoded, err := json.Marshal(map[string]any{"label": label})
		if err != nil {
			return nil, fmt.Errorf("%s: encode tag: %w", op, err)
		}
		createStatus, createBody, err := c.do(ctx, http.MethodPost, "/tag", encoded)
		if err != nil {
			return nil, err
		}
		if !isSuccess(createStatus) {
			// A concurrent sync may have created the same label between the
			// list and the create: re-read instead of failing.
			if refreshed, refreshErr := c.tagLabels(ctx); refreshErr == nil {
				if id, ok := refreshed[key]; ok {
					out[i] = id
					byLabel[key] = id
					continue
				}
			}
			return nil, apiError(op, createStatus, createBody, c.apiKey)
		}
		var created struct {
			ID int `json:"id"`
		}
		if err := json.Unmarshal(createBody, &created); err != nil || created.ID <= 0 {
			return nil, fmt.Errorf("%s: decode created tag", op)
		}
		byLabel[key] = created.ID
		out[i] = created.ID
	}
	return out, nil
}

// tagLabels returns the application's tags keyed by lower-case label. A 404
// yields an empty map: tag support is optional.
func (c *servarrClient) tagLabels(ctx context.Context) (map[string]int, error) {
	status, body, err := c.do(ctx, http.MethodGet, "/tag", nil)
	if err != nil {
		return nil, err
	}
	if status == http.StatusNotFound {
		return map[string]int{}, nil
	}
	if !isSuccess(status) {
		return nil, apiError("appsync: list tags", status, body, c.apiKey)
	}
	return decodeTagLabels(body), nil
}

func decodeTagLabels(body []byte) map[string]int {
	out := make(map[string]int)
	var tags []struct {
		ID    int    `json:"id"`
		Label string `json:"label"`
	}
	if err := json.Unmarshal(body, &tags); err != nil {
		return out
	}
	for _, tag := range tags {
		label := strings.ToLower(strings.TrimSpace(tag.Label))
		if label != "" && tag.ID > 0 {
			out[label] = tag.ID
		}
	}
	return out
}

// schemaFor looks up the schema template for spec's implementation. It returns
// nil when the schema cannot be fetched or does not contain a match; callers
// then send a minimal valid payload.
func (c *servarrClient) schemaFor(ctx context.Context, spec IndexerSpec) map[string]any {
	status, body, err := c.do(ctx, http.MethodGet, "/indexer/schema", nil)
	if err != nil || !isSuccess(status) {
		return nil
	}
	var schemas []map[string]any
	if err := json.Unmarshal(body, &schemas); err != nil {
		return nil
	}

	wantImplementation, wantContract, _ := normalizeSpecIdentity(spec)
	var match map[string]any
	for _, schema := range schemas {
		implementation := asString(schema["implementation"])
		if !strings.EqualFold(implementation, wantImplementation) {
			continue
		}
		if match == nil {
			match = schema
		}
		if strings.EqualFold(asString(schema["configContract"]), wantContract) {
			return schema
		}
	}
	return match
}

func buildAddPayload(spec IndexerSpec, schema map[string]any) map[string]any {
	payload := schema
	if payload == nil {
		payload = map[string]any{}
	} else {
		// The schema is a template, not an existing object.
		delete(payload, "id")
	}

	implementation, configContract, protocol := normalizeSpecIdentity(spec)
	payload["name"] = spec.Name
	payload["implementation"] = implementation
	payload["configContract"] = configContract
	payload["protocol"] = protocol
	payload["priority"] = normalizePriority(spec.Priority)
	payload["tags"] = intsOrEmpty(spec.Tags)
	payload["enableRss"] = spec.EnableRSS
	payload["enableAutomaticSearch"] = spec.EnableAutomaticSearch
	payload["enableInteractiveSearch"] = spec.EnableInteractiveSearch

	desired := desiredFields(spec)
	payload["fields"] = mergeFields(payload["fields"], desired)
	return payload
}

func applyUpdateSpec(payload map[string]any, spec IndexerSpec) {
	implementation, configContract := normalizeImplementation(spec.Implementation, spec.ConfigContract)
	if implementation != "" {
		payload["implementation"] = implementation
	}
	if configContract != "" {
		payload["configContract"] = configContract
	}
	if implementation != "" || configContract != "" {
		payload["protocol"] = resolveProtocol(spec.Protocol, implementation, configContract)
	}
	if strings.TrimSpace(spec.Name) != "" {
		payload["name"] = spec.Name
	}
	payload["priority"] = normalizePriority(spec.Priority)
	payload["tags"] = intsOrEmpty(spec.Tags)
	payload["enableRss"] = spec.EnableRSS
	payload["enableAutomaticSearch"] = spec.EnableAutomaticSearch
	payload["enableInteractiveSearch"] = spec.EnableInteractiveSearch

	payload["fields"] = mergeFields(payload["fields"], desiredFields(spec))
}

// desiredFields is the set of values indexarr owns. For updates the three core
// fields are always present, including empty values, so a successful update is
// reflected by the same SpecHash the next reconciliation computes.
func desiredFields(spec IndexerSpec) map[string]any {
	fields := make(map[string]any, len(spec.AdditionalFields)+3)
	for key, value := range spec.AdditionalFields {
		if key == "" {
			continue
		}
		fields[key] = value
	}
	fields["baseUrl"] = normalizeURLString(spec.BaseURL)
	fields["apiKey"] = spec.APIKey
	fields["categories"] = intsOrEmpty(spec.Categories)
	if !strings.EqualFold(strings.TrimSpace(spec.Protocol), "usenet") {
		// indexarr owns these fields, exactly like Prowlarr. Sending zero is
		// necessary to clear a value and to make the next reconciliation
		// converge on the same hash.
		fields["minimumSeeders"] = spec.MinSeeders
		fields["seedRatio"] = spec.SeedRatio
		fields["seedTime"] = spec.SeedTimeMinutes
		fields["seasonPackSeedTime"] = spec.SeasonPackSeedTimeMinutes
	}
	return fields
}

// mergeFields updates field objects in place, preserving fields and field
// properties that indexarr does not understand, then appends missing values in
// a stable order.
func mergeFields(raw any, desired map[string]any) []any {
	out := make([]any, 0, len(desired))
	seen := make(map[string]bool, len(desired))

	switch fields := raw.(type) {
	case []any:
		for _, item := range fields {
			field, ok := item.(map[string]any)
			if !ok {
				out = append(out, item)
				continue
			}
			name := asString(field["name"])
			if value, ok := desired[name]; ok && name != "" {
				field["value"] = value
				seen[name] = true
			}
			out = append(out, field)
		}
	case []map[string]any:
		for _, field := range fields {
			name := asString(field["name"])
			if value, ok := desired[name]; ok && name != "" {
				field["value"] = value
				seen[name] = true
			}
			out = append(out, field)
		}
	}

	missing := make([]string, 0, len(desired))
	for name := range desired {
		if !seen[name] {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	for _, name := range missing {
		out = append(out, map[string]any{"name": name, "value": desired[name]})
	}
	return out
}

func decodeRemote(body []byte) (RemoteIndexer, error) {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return RemoteIndexer{}, fmt.Errorf("decode indexer: %w", err)
	}
	return remoteFromMap(payload), nil
}

func remoteFromMap(payload map[string]any) RemoteIndexer {
	fields := fieldMap(payload["fields"])
	remote := RemoteIndexer{
		ID:             asInt(payload["id"]),
		Name:           asString(payload["name"]),
		Implementation: asString(payload["implementation"]),
		ConfigContract: asString(payload["configContract"]),
		Priority:       asInt(payload["priority"]),
		Tags:           asIntSlice(payload["tags"]),
		Fields:         fields,
	}
	remote.RawHash = rawHash(remote.Name, remote.Implementation, remote.ConfigContract,
		fields, remote.Priority, remote.Tags,
		asBool(payload["enableRss"]),
		asBool(payload["enableAutomaticSearch"]),
		asBool(payload["enableInteractiveSearch"]))
	return remote
}

func rawHash(name, implementation, configContract string, fields map[string]any,
	priority int, tags []int, enableRSS, enableAutomaticSearch, enableInteractiveSearch bool) string {
	return SpecHash(IndexerSpec{
		Name:                      name,
		Implementation:            implementation,
		ConfigContract:            configContract,
		BaseURL:                   asString(fields["baseUrl"]),
		APIKey:                    asString(fields["apiKey"]),
		Categories:                asIntSlice(fields["categories"]),
		Priority:                  priority,
		Tags:                      tags,
		EnableRSS:                 enableRSS,
		EnableAutomaticSearch:     enableAutomaticSearch,
		EnableInteractiveSearch:   enableInteractiveSearch,
		MinSeeders:                asInt(fields["minimumSeeders"]),
		SeedRatio:                 asFloat(fields["seedRatio"]),
		SeedTimeMinutes:           asInt(fields["seedTime"]),
		SeasonPackSeedTimeMinutes: asInt(fields["seasonPackSeedTime"]),
	})
}

func fieldMap(raw any) map[string]any {
	fields := make(map[string]any)
	switch list := raw.(type) {
	case []any:
		for _, item := range list {
			field, ok := item.(map[string]any)
			if !ok {
				continue
			}
			name := asString(field["name"])
			if name != "" {
				fields[name] = field["value"]
			}
		}
	case map[string]any:
		for name, value := range list {
			fields[name] = value
		}
	}
	return fields
}

func resolveProtocol(explicit, implementation, configContract string) string {
	protocol := strings.ToLower(strings.TrimSpace(explicit))
	if protocol == "torrent" || protocol == "usenet" {
		return protocol
	}
	if strings.EqualFold(configContract, "NewznabSettings") ||
		strings.EqualFold(implementation, "Newznab") {
		return "usenet"
	}
	return "torrent"
}

func asString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case json.Number:
		return typed.String()
	default:
		return ""
	}
}

func asInt(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case json.Number:
		if number, err := typed.Int64(); err == nil {
			return int(number)
		}
	case string:
		if number, err := strconv.Atoi(strings.TrimSpace(typed)); err == nil {
			return number
		}
	}
	return 0
}

func asFloat(value any) float64 {
	switch typed := value.(type) {
	case float64:
		return typed
	case float32:
		return float64(typed)
	case int:
		return float64(typed)
	case int64:
		return float64(typed)
	case json.Number:
		if number, err := typed.Float64(); err == nil {
			return number
		}
	case string:
		if number, err := strconv.ParseFloat(strings.TrimSpace(typed), 64); err == nil {
			return number
		}
	}
	return 0
}

func asBool(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		parsed, _ := strconv.ParseBool(strings.TrimSpace(typed))
		return parsed
	}
	return false
}

func asIntSlice(value any) []int {
	switch typed := value.(type) {
	case []int:
		return append([]int(nil), typed...)
	case []any:
		out := make([]int, 0, len(typed))
		for _, item := range typed {
			switch item.(type) {
			case float64, int, int64, json.Number, string:
				out = append(out, asInt(item))
			}
		}
		return out
	default:
		return nil
	}
}

func intsOrEmpty(in []int) []int {
	if len(in) == 0 {
		return []int{}
	}
	return append([]int(nil), in...)
}
