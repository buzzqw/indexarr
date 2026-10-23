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

	// liveSem is the global fan-out limit shared by every Torznab request.
	liveSem chan struct{}
}

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
	return &Manager{
		store:       cfg.Store,
		vault:       cfg.Vault,
		registry:    registry,
		transport:   transport,
		logger:      logger,
		maxTorrents: maxTorrents,
		cache:       make(map[int64]cachedConnector),
		liveSem:     make(chan struct{}, liveGlobalConcurrency),
	}, nil
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
}

// InvalidateAll drops every cached connector.
func (m *Manager) InvalidateAll() {
	m.mu.Lock()
	m.cache = make(map[int64]cachedConnector)
	m.mu.Unlock()
}

// connectorFor loads the definition, builds the settings map and returns the
// cached or freshly built connector.
func (m *Manager) connectorFor(ctx context.Context, indexer store.Indexer) (engine.Connector, error) {
	def, err := m.store.GetDefinition(ctx, indexer.DefinitionID)
	if err != nil {
		return nil, fmt.Errorf("indexer %s: load definition %q: %w", indexer.Slug, indexer.DefinitionID, err)
	}
	parsed, err := definition.Parse([]byte(def.Body))
	if err != nil {
		return nil, fmt.Errorf("indexer %s: parse definition %q: %w", indexer.Slug, def.ID, err)
	}

	key := connectorKey(def, indexer)
	m.mu.Lock()
	if entry, ok := m.cache[indexer.ID]; ok && entry.key == key {
		m.mu.Unlock()
		return entry.connector, nil
	}
	m.mu.Unlock()

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
	if allowPrivate || proxyURL != "" {
		if allowPrivate {
			transportCfg.AllowPrivate = true
		}
		if proxyURL != "" {
			transportCfg.ProxyURL = proxyURL
		}
		transport = engine.NewTransport(transportCfg)
	}

	name := connectorName(parsed, def)
	var connector engine.Connector
	switch name {
	case "", "definition":
		connector, err = engine.NewDefinitionConnector(engine.DefinitionConfig{
			Indexer:    ref,
			Definition: parsed,
			Settings:   settings,
			Transport:  transport,
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
