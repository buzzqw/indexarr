// Package indexer is the integration layer of indexarr 2: it turns a stored
// definition plus one configured indexer plus its vault secrets into a live
// engine.Connector, caches the result, and runs one indexer into the release
// cache. Every configured indexer goes through its family or definition
// connector; there is no bridged legacy path.
package indexer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/buzzqw/indexarr/internal/definition"
	"github.com/buzzqw/indexarr/internal/engine"
	"github.com/buzzqw/indexarr/internal/family"
	"github.com/buzzqw/indexarr/internal/store"
	"github.com/buzzqw/indexarr/internal/vault"
)

// DefaultMaxTorrentsPerRun bounds how many .torrent payloads one run fetches.
const DefaultMaxTorrentsPerRun = 30

// maxRunItems bounds the engine items kept in RunStats for the admin UI.
const maxRunItems = 100

var (
	// ErrVaultRequired is returned when an indexer has sealed secrets but the
	// Manager was built without a vault.
	ErrVaultRequired = errors.New("indexer: vault required for indexer secrets")

	// ErrStoreRequired is returned by New without a store.
	ErrStoreRequired = errors.New("indexer: store is required")

	// ErrQueryLimit is returned when an indexer has exhausted its daily query
	// budget. The limit covers search, latest and run attempts.
	ErrQueryLimit = errors.New("indexer: daily query limit reached")

	// ErrGrabLimit is returned when an indexer has exhausted its daily grab
	// budget for release downloads.
	ErrGrabLimit = errors.New("indexer: daily grab limit reached")
)

// Config configures a Manager. Store is required; every other field has a
// usable default.
type Config struct {
	Store             *store.Store
	Vault             *vault.Vault      // nil => indexers with secrets cannot be built
	Registry          *engine.Registry  // nil => built with family.Register
	Transport         *engine.Transport // nil => NewTransport(TransportConfig{})
	Logger            *slog.Logger
	MaxTorrentsPerRun int // 0 => 30
}

// Manager builds and caches connectors and runs indexers into the store.
type Manager struct {
	store       *store.Store
	vault       *vault.Vault
	registry    *engine.Registry
	transport   *engine.Transport
	logger      *slog.Logger
	maxTorrents int

	mu    sync.Mutex
	cache map[int64]cachedConnector

	// liveCache keeps recent fan-out results so Sonarr/Radarr repetitions and
	// pagination do not hit every site again. It is bounded and expires.
	liveCacheMu sync.Mutex
	liveCache   map[string]liveCacheEntry

	liveFlightMu sync.Mutex
	liveFlights  map[string]*liveFlight

	// liveSem is the global fan-out limit shared by every Torznab request.
	liveSem chan struct{}

	// liveWorkers deduplicates in-flight (indexer, effective query) workers.
	// A detached worker remains here until it finishes, so later requests join
	// it instead of starting a second one.
	liveWorkersMu  sync.Mutex
	liveWorkers    map[string]*liveWorker
	liveWaiters    atomic.Int64
	liveWaitersMax atomic.Int64
	liveQueueDrop  atomic.Int64

	// liveStats keeps a small per-indexer picture of the newest live searches.
	// The early-finish rule uses it to stop waiting for sites that, on average,
	// answer late without bringing a result.
	liveStatsMu sync.Mutex
	liveStats   map[int64]*liveStatSeries

	// liveRuns keeps the fan-out of each query alive after the first page
	// returned. The next page waits for it, so the rows found by the late
	// workers are part of the same client search instead of the next one.
	liveRunsMu sync.Mutex
	liveRuns   map[string]*liveRun

	// liveEpisodes remembers when one Sonarr/Radarr search for an episode
	// started. The title variants of the same episode share one wait budget,
	// so four requests cannot cost four full windows.
	liveEpisodesMu sync.Mutex
	liveEpisodes   map[string]time.Time
}

type liveRun struct {
	done    chan struct{}
	started time.Time
}

type liveFlight struct {
	done   chan struct{}
	result LiveResult
	err    error
}

type liveWorker struct {
	done      chan struct{}
	result    LiveIndexerResult
	abandoned chan struct{}
	mu        sync.Mutex
	refs      int
	running   bool
	abandon   sync.Once
}

type liveCacheEntry struct {
	result  LiveResult
	expires time.Time
	created time.Time
}

const liveCacheMaxEntries = 256

type cachedConnector struct {
	key       string
	connector engine.Connector
	secrets   []string
	transport *engine.Transport
}

// RunStats is what one Run did. Items is capped to keep memory bounded.
//
// Skipped is the legacy "not stored" counter: it includes duplicates, which
// are also reported separately, so existing callers keep their meaning. The
// Discarded* counters partition the real discard reasons and sum to
// Discarded. Completeness is measured on the normalized items the connector
// returned, before any run-level filter.
type RunStats struct {
	Seen     int
	New      int
	Updated  int
	Blocked  int
	Skipped  int
	Torrents int
	Items    []engine.Item

	Duplicates      int
	Discarded       int
	DiscardNoLink   int
	DiscardTooOld   int
	DiscardCategory int
	DiscardParse    int
	DiscardOther    int
	FieldTotal      int
	FieldSize       int
	FieldSeeders    int
	FieldCategory   int
	FieldDate       int
	FieldHash       int
	SampleItems     []store.SampleItem
	maxSampleItems  int
}

// New builds a Manager.
func New(cfg Config) (*Manager, error) {
	if cfg.Store == nil {
		return nil, ErrStoreRequired
	}
	registry := cfg.Registry
	if registry == nil {
		registry = engine.NewRegistry()
		if err := family.Register(registry); err != nil {
			return nil, fmt.Errorf("indexer: register family connectors: %w", err)
		}
	}
	transport := cfg.Transport
	if transport == nil {
		transport = engine.NewTransport(engine.TransportConfig{})
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	maxTorrents := cfg.MaxTorrentsPerRun
	if maxTorrents <= 0 {
		maxTorrents = DefaultMaxTorrentsPerRun
	}
	m := &Manager{
		store:        cfg.Store,
		vault:        cfg.Vault,
		registry:     registry,
		transport:    transport,
		logger:       logger,
		maxTorrents:  maxTorrents,
		cache:        make(map[int64]cachedConnector),
		liveCache:    make(map[string]liveCacheEntry),
		liveFlights:  make(map[string]*liveFlight),
		liveWorkers:  make(map[string]*liveWorker),
		liveStats:    make(map[int64]*liveStatSeries),
		liveRuns:     make(map[string]*liveRun),
		liveEpisodes: make(map[string]time.Time),
		liveSem:      make(chan struct{}, liveGlobalConcurrency),
	}
	// Best effort: a fresh database simply starts with an empty picture and
	// the fan-out waits normally until it has measured the sites.
	seedCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	m.seedLiveStats(seedCtx)
	cancel()
	return m, nil
}

// Connector returns the live connector for one indexer together with the
// indexer row it was built from. Connectors are cached per indexer and rebuilt
// when the definition body, the definition timestamp or the indexer row
// changes.
func (m *Manager) Connector(ctx context.Context, indexerID int64) (engine.Connector, store.Indexer, error) {
	indexer, err := m.store.GetIndexer(ctx, indexerID)
	if err != nil {
		return nil, store.Indexer{}, fmt.Errorf("indexer %d: %w", indexerID, err)
	}
	connector, err := m.connectorFor(ctx, indexer)
	if err != nil {
		return nil, indexer, err
	}
	return connector, indexer, nil
}

// Invalidate drops the cached connector of one indexer.
func (m *Manager) Invalidate(indexerID int64) {
	m.mu.Lock()
	delete(m.cache, indexerID)
	m.mu.Unlock()
	m.liveCacheMu.Lock()
	m.liveCache = make(map[string]liveCacheEntry)
	m.liveCacheMu.Unlock()
}

// InvalidateAll drops every cached connector.
func (m *Manager) InvalidateAll() {
	m.mu.Lock()
	m.cache = make(map[int64]cachedConnector)
	m.mu.Unlock()
	m.liveCacheMu.Lock()
	m.liveCache = make(map[string]liveCacheEntry)
	m.liveCacheMu.Unlock()
}

// connectorFor loads the definition, builds the settings map and returns the
// cached or freshly built connector.
func (m *Manager) connectorFor(ctx context.Context, indexer store.Indexer) (engine.Connector, error) {
	def, err := m.store.GetDefinition(ctx, indexer.DefinitionID)
	if err != nil {
		return nil, fmt.Errorf("indexer %s: load definition %q: %w", indexer.Slug, indexer.DefinitionID, err)
	}
	// The key carries the definition fingerprint, so a cached connector was
	// built from this exact body: the YAML is parsed only on a miss.
	key := connectorKey(def, indexer)
	m.mu.Lock()
	if entry, ok := m.cache[indexer.ID]; ok && entry.key == key {
		m.mu.Unlock()
		return entry.connector, nil
	}
	m.mu.Unlock()

	parsed, err := definition.Parse([]byte(def.Body))
	if err != nil {
		return nil, fmt.Errorf("indexer %s: parse definition %q: %w", indexer.Slug, def.ID, err)
	}

	config, err := decodeConfig(indexer.ConfigJSON)
	if err != nil {
		return nil, fmt.Errorf("indexer %s: parse config_json: %w", indexer.Slug, err)
	}
	settings := settingsFromConfig(config)
	secretValues, err := m.applySecrets(ctx, indexer.ID, settings)
	if err != nil {
		return nil, err
	}
	applyDefaults(indexer, parsed, settings)

	network, err := m.store.LoadSettings(ctx)
	if err != nil {
		return nil, fmt.Errorf("indexer %s: load network settings: %w", indexer.Slug, err)
	}
	solver, solverOverride, err := challengeSolver(settings, network)
	if err != nil {
		return nil, redactError(fmt.Errorf("indexer %s: %w", indexer.Slug, err), secretValues)
	}

	proxyURL, proxySecrets, err := m.proxyURLFor(ctx, indexer)
	if err != nil {
		return nil, redactError(err, append(secretValues, proxySecrets...))
	}
	secretValues = append(secretValues, proxySecrets...)

	protocol := engine.Protocol(strings.TrimSpace(indexer.Protocol))
	if !protocol.Valid() {
		protocol = engine.ProtocolTorrent
	}
	ref := engine.IndexerRef{
		ID:       indexer.ID,
		Slug:     indexer.Slug,
		Name:     indexer.Name,
		Protocol: protocol,
	}

	// The shared transport serves every public indexer. A local indexer (an
	// explicit opt-in) and an indexer with a proxy get their own transport, so
	// private addresses and the proxy stay scoped to that one indexer.
	transport := m.transport
	transportCfg := m.transport.Config()
	allowPrivate := settingBool(settings["allow_private"])
	// Settings are read on every connector build, so changing the global
	// FlareSolverr URL or its timeout takes effect without a restart.
	if solver != nil || transportCfg.Solver != nil {
		solverOverride = true
	}
	if allowPrivate || proxyURL != "" || solverOverride {
		if allowPrivate {
			transportCfg.AllowPrivate = true
		}
		if proxyURL != "" {
			transportCfg.ProxyURL = proxyURL
		}
		transportCfg.Solver = solver
		transport = engine.NewTransport(transportCfg)
	}

	name := connectorName(parsed, def)
	var connector engine.Connector
	switch name {
	case "", "definition":
		connector, err = engine.NewDefinitionConnector(engine.DefinitionConfig{
			Indexer:         ref,
			Definition:      parsed,
			Settings:        settings,
			Transport:       transport,
			SessionEndpoint: network.FlareSolverrURL,
			SessionTimeout:  time.Duration(network.FlareSolverrTimeoutSeconds) * time.Second,
		})
		if err != nil {
			return nil, redactError(fmt.Errorf("indexer %s: build definition connector: %w", indexer.Slug, err), secretValues)
		}
	default:
		// Every family connector is native now, including torznab and rss:
		// the Jackett/Prowlarr bridge is an ordinary torznab indexer.
		if _, ok := m.registry.Factory(name); !ok {
			return nil, fmt.Errorf("indexer %s: connector %q is not registered", indexer.Slug, name)
		}
		connector, err = m.registry.NewConnector(name, engine.Config{
			Indexer:   ref,
			Settings:  settings,
			Transport: transport,
		})
		if err != nil {
			return nil, redactError(fmt.Errorf("indexer %s: build connector %q: %w", indexer.Slug, name, err), secretValues)
		}
	}

	entry := cachedConnector{key: key, connector: connector, secrets: secretValues, transport: transport}
	m.mu.Lock()
	// A concurrent build may have won the race. Reuse it so callers always
	// observe one cached connector per indexer.
	if existing, ok := m.cache[indexer.ID]; ok && existing.key == key {
		m.mu.Unlock()
		return existing.connector, nil
	}
	m.cache[indexer.ID] = entry
	m.mu.Unlock()
	return connector, nil
}

// connectorName applies the precedence fixed by the plan: the YAML
// connector, then the definitions.family column, then the native definition
// interpreter.
func connectorName(parsed *definition.Definition, def store.Definition) string {
	if name := strings.ToLower(strings.TrimSpace(parsed.Connector)); name != "" {
		return name
	}
	if name := strings.ToLower(strings.TrimSpace(def.Family)); name != "" {
		return name
	}
	return "definition"
}

func connectorKey(def store.Definition, indexer store.Indexer) string {
	var b strings.Builder
	b.WriteString(definition.Fingerprint([]byte(def.Body)))
	b.WriteByte('|')
	b.WriteString(strconv.FormatInt(def.UpdatedAt.UnixNano(), 10))
	b.WriteByte('|')
	b.WriteString(strconv.FormatInt(indexer.UpdatedAt.UnixNano(), 10))
	b.WriteByte('|')
	b.WriteString(indexer.ConfigJSON)
	b.WriteByte('|')
	b.WriteString(indexer.Name)
	b.WriteByte('|')
	b.WriteString(indexer.Slug)
	b.WriteByte('|')
	b.WriteString(indexer.Protocol)
	b.WriteByte('|')
	if indexer.ProxyID != nil {
		b.WriteString(strconv.FormatInt(*indexer.ProxyID, 10))
	}
	return b.String()
}

// proxyURLFor resolves the optional proxy of an indexer. Sealed credentials are
// decrypted and injected as username:password into the proxy URL. The returned
// plaintext values are used only to redact errors and are never logged.
func (m *Manager) proxyURLFor(ctx context.Context, indexer store.Indexer) (string, []string, error) {
	if indexer.ProxyID == nil {
		return "", nil, nil
	}
	proxy, err := m.store.GetProxy(ctx, *indexer.ProxyID)
	if err != nil {
		return "", nil, fmt.Errorf("indexer %s: load proxy %d: %w", indexer.Slug, *indexer.ProxyID, err)
	}
	raw := strings.TrimSpace(proxy.URL)
	if raw == "" {
		return "", nil, fmt.Errorf("indexer %s: proxy %d has no URL", indexer.Slug, proxy.ID)
	}
	if len(proxy.SecretsEnc) == 0 {
		return raw, nil, nil
	}
	if m.vault == nil {
		return "", nil, fmt.Errorf("indexer %s: proxy %d has sealed credentials: %w",
			indexer.Slug, proxy.ID, ErrVaultRequired)
	}
	credentials, err := m.vault.OpenString(string(proxy.SecretsEnc))
	if err != nil {
		return "", nil, fmt.Errorf("indexer %s: decrypt proxy %d credentials: %w",
			indexer.Slug, proxy.ID, err)
	}
	withCredentials, err := proxyWithCredentials(raw, credentials)
	if err != nil {
		return "", nil, fmt.Errorf("indexer %s: proxy %d: %w", indexer.Slug, proxy.ID, err)
	}
	var secrets []string
	if strings.TrimSpace(credentials) != "" {
		secrets = append(secrets, credentials)
	}
	return withCredentials, secrets, nil
}

// proxyWithCredentials injects decrypted credentials into a proxy URL. The
// error never repeats the URL or the credentials.
func proxyWithCredentials(raw, credentials string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Hostname() == "" {
		return "", errors.New("invalid proxy URL")
	}
	credentials = strings.TrimSpace(credentials)
	if credentials == "" {
		return raw, nil
	}
	username, password, hasPassword := strings.Cut(credentials, ":")
	if hasPassword {
		parsed.User = url.UserPassword(username, password)
	} else {
		parsed.User = url.User(username)
	}
	return parsed.String(), nil
}

// applySecrets decrypts every secret of the indexer into settings and returns
// the plaintext values so errors can be redacted. Values never appear in
// returned errors or logs.
func (m *Manager) applySecrets(ctx context.Context, indexerID int64, settings map[string]string) ([]string, error) {
	secrets, err := m.store.ListSecrets(ctx, indexerID)
	if err != nil {
		return nil, fmt.Errorf("indexer %d: list secrets: %w", indexerID, err)
	}
	if len(secrets) == 0 {
		return nil, nil
	}
	if m.vault == nil {
		return nil, fmt.Errorf("indexer %d: %w", indexerID, ErrVaultRequired)
	}
	values := make([]string, 0, len(secrets))
	for _, secret := range secrets {
		value, err := m.vault.Open(secret.Nonce, secret.Ciphertext)
		if err != nil {
			return nil, fmt.Errorf("indexer %d: decrypt secret %q: %w", indexerID, secret.Key, err)
		}
		settings[secret.Key] = value
		if value != "" {
			values = append(values, value)
		}
	}
	return values, nil
}

// applyDefaults gives family connectors the settings they read and exposes the
// indexer identity to definitions. An explicitly configured value always wins.
func applyDefaults(indexer store.Indexer, parsed *definition.Definition, settings map[string]string) {
	// Format v1 setting names are slugs, so a definition declares "apikey" and
	// the family connectors read the canonical "api_key". Keep the two names in
	// sync here as the definition connector already does for its own settings.
	if strings.TrimSpace(settings["api_key"]) == "" {
		settings["api_key"] = settings["apikey"]
	}
	if strings.TrimSpace(settings["apikey"]) == "" {
		settings["apikey"] = settings["api_key"]
	}
	if strings.TrimSpace(settings["base_url"]) == "" {
		if url := strings.TrimSpace(settings["url"]); url != "" {
			settings["base_url"] = url
		} else if parsed != nil && len(parsed.Meta.URLs) > 0 {
			settings["base_url"] = parsed.Meta.URLs[0]
		}
	}
	if strings.TrimSpace(settings["url"]) == "" {
		settings["url"] = settings["base_url"]
	}
	if strings.TrimSpace(settings["name"]) == "" {
		settings["name"] = indexer.Name
	}
	if strings.TrimSpace(settings["slug"]) == "" {
		settings["slug"] = indexer.Slug
	}
	if strings.TrimSpace(settings["protocol"]) == "" {
		settings["protocol"] = indexer.Protocol
	}
}

// decodeConfig parses config_json into a generic object. Numbers stay exact by
// using json.Number.
func decodeConfig(raw string) (map[string]any, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "null" {
		return map[string]any{}, nil
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	var out map[string]any
	if err := decoder.Decode(&out); err != nil {
		return nil, err
	}
	if out == nil {
		out = map[string]any{}
	}
	return out, nil
}

// settingsFromConfig flattens config_json into the string map connectors
// receive. Arrays become comma-separated lists.
func settingsFromConfig(config map[string]any) map[string]string {
	out := make(map[string]string, len(config))
	for key, value := range config {
		out[key] = settingString(value)
	}
	return out
}

func settingString(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case bool:
		return strconv.FormatBool(typed)
	case json.Number:
		return typed.String()
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case []any:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			if text := settingString(item); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, ",")
	case map[string]any:
		encoded, err := json.Marshal(typed)
		if err != nil {
			return ""
		}
		return string(encoded)
	default:
		return fmt.Sprint(typed)
	}
}

// queryFromConfig returns the configured run query, if any.
func queryFromConfig(config map[string]any) string {
	value, ok := config["query"]
	if !ok {
		return ""
	}
	return strings.TrimSpace(settingString(value))
}

// categoriesFromValue accepts an array or a comma-separated string.
func categoriesFromValue(value any) []int {
	switch typed := value.(type) {
	case nil:
		return nil
	case []int:
		return append([]int(nil), typed...)
	case []any:
		out := make([]int, 0, len(typed))
		for _, item := range typed {
			out = appendCategories(out, settingString(item))
		}
		return dedupeCategories(out)
	case string:
		return dedupeCategories(appendCategories(nil, typed))
	default:
		return dedupeCategories(appendCategories(nil, settingString(typed)))
	}
}

func appendCategories(out []int, raw string) []int {
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, err := strconv.Atoi(part)
		if err != nil || id <= 0 {
			continue
		}
		out = append(out, id)
	}
	return out
}

// challengeSolver resolves the Cloudflare retry for one indexer. The
// per-indexer "challenge" setting is "none" or "flaresolverr"; empty follows
// the global FlareSolverr URL. A per-indexer URL and timeout override the
// global values. The second return value says whether the connector needs a
// private transport even when it has no proxy and no private-address opt-in.
func challengeSolver(settings map[string]string, network store.Settings) (engine.ChallengeSolver, bool, error) {
	mode := strings.ToLower(strings.TrimSpace(settings["challenge"]))
	switch mode {
	case "", "auto", "none", "flaresolverr":
	default:
		return nil, false, fmt.Errorf("challenge: unsupported value %q", mode)
	}
	perSiteURL := strings.TrimSpace(settings["flaresolverr_url"])
	perSiteTimeout := strings.TrimSpace(settings["flaresolverr_timeout"])
	override := mode != "" || perSiteURL != "" || perSiteTimeout != ""

	if mode == "none" {
		return nil, true, nil
	}

	endpoint := perSiteURL
	if endpoint == "" {
		endpoint = strings.TrimSpace(network.FlareSolverrURL)
	}
	timeoutSeconds := network.FlareSolverrTimeoutSeconds
	if perSiteTimeout != "" {
		value, err := strconv.Atoi(perSiteTimeout)
		if err != nil || value < 5 || value > 300 {
			return nil, override, fmt.Errorf("challenge: FlareSolverr timeout must be 5-300 seconds")
		}
		timeoutSeconds = value
	}
	if timeoutSeconds <= 0 {
		timeoutSeconds = 60
	}
	if endpoint == "" {
		if mode == "flaresolverr" {
			return nil, override, errors.New("challenge: FlareSolverr is enabled but no URL is configured")
		}
		return nil, override, nil
	}
	return engine.NewFlareSolverrSolver(endpoint, time.Duration(timeoutSeconds)*time.Second), override, nil
}

func settingBool(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func dedupeCategories(in []int) []int {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[int]struct{}, len(in))
	out := make([]int, 0, len(in))
	for _, id := range in {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

// breakerOpen reports whether the existing per-indexer breaker is open for the
// transport that actually serves this indexer (the shared one, or the private
// transport built for a local/proxied site).
func (m *Manager) breakerOpen(indexerID int64, slug string) bool {
	m.mu.Lock()
	entry, ok := m.cache[indexerID]
	m.mu.Unlock()
	transport := m.transport
	if ok && entry.transport != nil {
		transport = entry.transport
	}
	return transport != nil && transport.BreakerState(slug) == engine.BreakerOpen
}

// redact removes every known secret value of the indexer and the usual
// credential query parameters from text. It is safe to call at any time.
func (m *Manager) redact(indexerID int64, text string) string {
	m.mu.Lock()
	var secrets []string
	if entry, ok := m.cache[indexerID]; ok {
		secrets = append(secrets, entry.secrets...)
	}
	m.mu.Unlock()

	return redactValues(text, secrets)
}

// RedactError removes every known secret value of an indexer from an error
// message. It is safe to call from the scheduler before logging.
func (m *Manager) RedactError(indexerID int64, err error) string {
	if err == nil {
		return ""
	}
	return m.redact(indexerID, errorText(err))
}

func errorText(err error) string {
	text := strings.TrimSpace(err.Error())
	text = strings.ReplaceAll(text, "\n", " ")
	if len(text) > 500 {
		text = text[:500] + "…"
	}
	return text
}

func hashText(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
