package appsync

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// simpleClient covers Mylar3 and LazyLibrarian. Both expose a command API on
// /api and store Torznab/Newznab providers by name, not by numeric id. The
// client keeps a small name cache so Reconcile can use its integer IDs.
type simpleClient struct {
	kind   Kind
	root   string
	apiKey string
	http   *http.Client

	mu   sync.Mutex
	refs map[int]simpleRef
}

type simpleRef struct {
	name         string
	providerType string // torznab | newznab
}

type simpleResponse struct {
	Success bool `json:"success"`
	Data    struct {
		Torznabs []map[string]any `json:"torznabs"`
		Newznabs []map[string]any `json:"newznabs"`
	} `json:"data"`
	Error struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func newSimple(cfg Config, root string) *simpleClient {
	return &simpleClient{
		kind:   cfg.Kind,
		root:   root,
		apiKey: cfg.APIKey,
		http:   cfg.HTTPClient,
		refs:   make(map[int]simpleRef),
	}
}

// Test calls GET /api?cmd=getVersion&apikey=... and tolerates the different
// JSON envelopes the two applications use.
func (c *simpleClient) Test(ctx context.Context) (string, error) {
	op := fmt.Sprintf("appsync: %s test", c.kind)
	status, body, err := c.command(ctx, "getVersion", nil)
	if err != nil {
		return "", fmt.Errorf("%s: %s", op, err)
	}
	if !isSuccess(status) {
		return "", apiError(op, status, body, c.apiKey)
	}
	version := versionFromBody(body)
	if version == "" {
		return "", fmt.Errorf("%s: no version in response", op)
	}
	return c.kind.displayName() + " " + version, nil
}

// ListIndexers reads the Torznab and Newznab providers of the application.
func (c *simpleClient) ListIndexers(ctx context.Context) ([]RemoteIndexer, error) {
	command := "listProviders"
	if c.kind == KindLazyLibrarian {
		command = "listNabProviders"
	}
	op := fmt.Sprintf("appsync: %s list indexers", c.kind)
	status, body, err := c.command(ctx, command, nil)
	if err != nil {
		return nil, fmt.Errorf("%s: %s", op, err)
	}
	if !isSuccess(status) {
		return nil, apiError(op, status, body, c.apiKey)
	}
	var response simpleResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("%s: decode response: %w", op, err)
	}
	if !response.Success {
		return nil, fmt.Errorf("%s: %s", op, simpleErrorMessage(response))
	}

	remote := make([]RemoteIndexer, 0, len(response.Data.Torznabs)+len(response.Data.Newznabs))
	refs := make(map[int]simpleRef)
	add := func(items []map[string]any, providerType string) {
		for _, item := range items {
			name := strings.TrimSpace(asString(item["name"]))
			if name == "" {
				continue
			}
			spec := c.specFromRemote(name, providerType, item)
			id := simpleRemoteID(providerType, name)
			refs[id] = simpleRef{name: name, providerType: providerType}
			remote = append(remote, RemoteIndexer{
				ID: id, Name: name,
				Implementation: spec.Implementation, ConfigContract: spec.ConfigContract,
				Priority: spec.Priority, Fields: specFields(spec), RawHash: SpecHash(spec),
			})
		}
	}
	add(response.Data.Torznabs, "torznab")
	add(response.Data.Newznabs, "newznab")
	sort.SliceStable(remote, func(i, j int) bool { return remote[i].Name < remote[j].Name })

	c.mu.Lock()
	c.refs = refs
	c.mu.Unlock()
	return remote, nil
}

// AddIndexer creates a provider. The application returns only a success flag,
// so the returned remote is built from the spec.
func (c *simpleClient) AddIndexer(ctx context.Context, spec IndexerSpec) (RemoteIndexer, error) {
	providerType, params := c.providerParams(spec)
	params.Set("cmd", "addProvider")
	status, body, err := c.request(ctx, params)
	if err != nil {
		return RemoteIndexer{}, c.wrap("add indexer", err)
	}
	if !c.commandOK(status, body, "add indexer") {
		return RemoteIndexer{}, c.commandError(status, body, "add indexer")
	}
	return c.remoteFromSpec(spec, providerType), nil
}

// UpdateIndexer changes an existing provider. Mylar/Lazy identify it by name,
// so the spec name is the key; the cached remote id is only for Reconcile.
func (c *simpleClient) UpdateIndexer(ctx context.Context, remoteID int, spec IndexerSpec) (RemoteIndexer, error) {
	providerType, params := c.providerParams(spec)
	params.Set("cmd", "changeProvider")
	params.Set("altername", spec.Name)
	status, body, err := c.request(ctx, params)
	if err != nil {
		return RemoteIndexer{}, c.wrap("update indexer", err)
	}
	if !c.commandOK(status, body, "update indexer") {
		return RemoteIndexer{}, c.commandError(status, body, "update indexer")
	}
	return c.remoteFromSpec(spec, providerType), nil
}

// DeleteIndexer removes a provider by the name remembered from ListIndexers.
func (c *simpleClient) DeleteIndexer(ctx context.Context, remoteID int) error {
	c.mu.Lock()
	ref, ok := c.refs[remoteID]
	c.mu.Unlock()
	if !ok {
		if _, err := c.ListIndexers(ctx); err != nil {
			return c.wrap("delete indexer", err)
		}
		c.mu.Lock()
		ref, ok = c.refs[remoteID]
		c.mu.Unlock()
	}
	if !ok {
		return fmt.Errorf("appsync: %s delete indexer: remote id %d not found", c.kind, remoteID)
	}
	params := url.Values{}
	params.Set("cmd", "delProvider")
	params.Set("name", ref.name)
	params.Set("providertype", ref.providerType)
	status, body, err := c.request(ctx, params)
	if err != nil {
		return c.wrap("delete indexer", err)
	}
	if !c.commandOK(status, body, "delete indexer") {
		return c.commandError(status, body, "delete indexer")
	}
	return nil
}

func (c *simpleClient) providerParams(spec IndexerSpec) (string, url.Values) {
	providerType := "torznab"
	if strings.EqualFold(strings.TrimSpace(spec.Protocol), "usenet") {
		providerType = "newznab"
	}
	params := url.Values{}
	params.Set("name", spec.Name)
	params.Set("providertype", providerType)
	params.Set("host", spec.BaseURL)
	params.Set("prov_apikey", spec.APIKey)
	params.Set("enabled", "true")
	params.Set("categories", joinIntsCSV(spec.Categories))
	if c.kind == KindLazyLibrarian {
		params.Set("dlpriority", strconv.Itoa(lazyPriority(spec.Priority)))
		if providerType == "torznab" {
			params.Set("seeders", strconv.Itoa(spec.MinSeeders))
			params.Set("seed_ratio", strconv.FormatFloat(spec.SeedRatio, 'f', -1, 64))
			params.Set("seed_duration", strconv.Itoa(spec.SeedTimeMinutes))
		}
	}
	return providerType, params
}

func (c *simpleClient) remoteFromSpec(spec IndexerSpec, providerType string) RemoteIndexer {
	return RemoteIndexer{
		ID: simpleRemoteID(providerType, spec.Name), Name: spec.Name,
		Implementation: spec.Implementation, ConfigContract: spec.ConfigContract,
		Priority: spec.Priority, Fields: specFields(spec), RawHash: SpecHash(spec),
	}
}

// specFromRemote rebuilds the parts of a provider that Reconcile compares.
// Mylar has no priority or seed fields; Lazy has priority and three seed
// fields. The values the application does not store are normalized to the
// same defaults the syncjob uses.
func (c *simpleClient) specFromRemote(name, providerType string, item map[string]any) IndexerSpec {
	protocol := "torrent"
	implementation, contract := "Torznab", "TorznabSettings"
	if providerType == "newznab" {
		protocol, implementation, contract = "usenet", "Newznab", "NewznabSettings"
	}
	spec := IndexerSpec{
		Name: name, BaseURL: asString(item["host"]), APIKey: asString(item["apikey"]),
		Categories:     asIntListCSV(item["categories"]),
		Implementation: implementation, ConfigContract: contract, Protocol: protocol,
		EnableRSS: true, EnableAutomaticSearch: true, EnableInteractiveSearch: true,
		Priority: 25,
	}
	if c.kind == KindLazyLibrarian {
		priority := asInt(item["dlpriority"])
		if priority <= 0 {
			priority = asInt(item["priority"])
		}
		if priority > 0 {
			spec.Priority = 50 - priority + 1
		}
		spec.MinSeeders = asInt(item["minimum_seeders"])
		if spec.MinSeeders == 0 {
			spec.MinSeeders = asInt(item["seeders"])
		}
		spec.SeedRatio = asFloat(item["seed_ratio"])
		spec.SeedTimeMinutes = asInt(item["seed_time"])
		if spec.SeedTimeMinutes == 0 {
			spec.SeedTimeMinutes = asInt(item["seed_duration"])
		}
	}
	return spec
}

func (c *simpleClient) command(ctx context.Context, command string, params url.Values) (int, []byte, error) {
	if params == nil {
		params = url.Values{}
	}
	params.Set("cmd", command)
	return c.request(ctx, params)
}

func (c *simpleClient) request(ctx context.Context, params url.Values) (int, []byte, error) {
	parsed, err := url.Parse(c.root)
	if err != nil {
		return 0, nil, err
	}
	path := strings.TrimRight(parsed.Path, "/")
	if !strings.HasSuffix(strings.ToLower(path), "/api") {
		path += "/api"
	}
	parsed.Path = path
	query := parsed.Query()
	for key, values := range params {
		for _, value := range values {
			query.Set(key, value)
		}
	}
	if c.apiKey != "" {
		query.Set("apikey", c.apiKey)
	}
	parsed.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Accept", "application/json")
	return execute(ctx, c.http, req, c.apiKey)
}

func (c *simpleClient) commandOK(status int, body []byte, operation string) bool {
	if !isSuccess(status) {
		return false
	}
	var response simpleResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return true // some builds return a plain success body
	}
	return response.Success || len(body) == 0
}

func (c *simpleClient) commandError(status int, body []byte, operation string) error {
	op := fmt.Sprintf("appsync: %s %s", c.kind, operation)
	if !isSuccess(status) {
		return apiError(op, status, body, c.apiKey)
	}
	var response simpleResponse
	if err := json.Unmarshal(body, &response); err == nil && !response.Success {
		return fmt.Errorf("%s: %s", op, simpleErrorMessage(response))
	}
	return fmt.Errorf("%s: application rejected the request", op)
}

func (c *simpleClient) wrap(operation string, err error) error {
	return fmt.Errorf("appsync: %s %s: %w", c.kind, operation, err)
}

func simpleErrorMessage(response simpleResponse) string {
	if response.Error.Message != "" {
		return response.Error.Message
	}
	return "application returned success=false"
}

func simpleRemoteID(providerType, name string) int {
	// FNV-1a over type and name, mapped to a positive int. The value is stable
	// across calls and unique for the provider pairs the apps expose.
	var hash uint32 = 2166136261
	for _, value := range []byte(providerType + "\x00" + name) {
		hash ^= uint32(value)
		hash *= 16777619
	}
	return int(hash & 0x7fffffff)
}

func lazyPriority(priority int) int {
	if priority <= 0 {
		priority = 25
	}
	return 50 - priority + 1
}

func joinIntsCSV(values []int) string {
	if len(values) == 0 {
		return ""
	}
	sorted := append([]int(nil), values...)
	sort.Ints(sorted)
	parts := make([]string, 0, len(sorted))
	for _, value := range sorted {
		parts = append(parts, strconv.Itoa(value))
	}
	return strings.Join(parts, ",")
}

func asIntListCSV(value any) []int {
	switch typed := value.(type) {
	case []int:
		return typed
	case []any:
		return asIntSlice(typed)
	}
	raw := strings.TrimSpace(asString(value))
	if raw == "" {
		return nil
	}
	var out []int
	for _, part := range strings.Split(raw, ",") {
		if number, err := strconv.Atoi(strings.TrimSpace(part)); err == nil {
			out = append(out, number)
		}
	}
	return out
}

func specFields(spec IndexerSpec) map[string]any {
	return map[string]any{
		"baseUrl":            spec.BaseURL,
		"apiKey":             spec.APIKey,
		"categories":         intsOrEmpty(spec.Categories),
		"minimumSeeders":     spec.MinSeeders,
		"seedRatio":          spec.SeedRatio,
		"seedTime":           spec.SeedTimeMinutes,
		"seasonPackSeedTime": spec.SeasonPackSeedTimeMinutes,
	}
}

// IndexerSnapshot stores the provider list in the same JSON shape the Servarr
// snapshot uses, so the App page can restore it. Mylar and Lazy identify
// providers by name; the snapshot keeps name, host, key and categories.
func (c *simpleClient) IndexerSnapshot(ctx context.Context) ([]byte, error) {
	remote, err := c.ListIndexers(ctx)
	if err != nil {
		return nil, err
	}
	entries := make([]map[string]any, 0, len(remote))
	for _, item := range remote {
		protocol := "torrent"
		if strings.EqualFold(item.Implementation, "Newznab") {
			protocol = "usenet"
		}
		fields := map[string]any{
			"baseUrl":    asString(item.Fields["baseUrl"]),
			"apiKey":     asString(item.Fields["apiKey"]),
			"categories": item.Fields["categories"],
		}
		if c.kind == KindLazyLibrarian {
			fields["minimumSeeders"] = asInt(item.Fields["minimumSeeders"])
			fields["seedRatio"] = asFloat(item.Fields["seedRatio"])
			fields["seedTime"] = asInt(item.Fields["seedTime"])
		}
		entries = append(entries, map[string]any{
			"name":                    item.Name,
			"implementation":          item.Implementation,
			"configContract":          item.ConfigContract,
			"priority":                item.Priority,
			"tags":                    []int{},
			"enableRss":               true,
			"enableAutomaticSearch":   true,
			"enableInteractiveSearch": true,
			"protocol":                protocol,
			"fields":                  fields,
		})
	}
	return json.Marshal(entries)
}
