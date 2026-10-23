// Package indexersvc is the server-side service behind the native indexer
// pages of the web interface.
//
// It owns the rules for creating and updating one configured indexer:
// definition and required-setting validation, slug normalisation, the split
// between public configuration and vault-sealed secrets, and invalidation of
// the runtime connector cache. Secret values are never returned or logged.
package indexersvc

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/buzzqw/indexarr/internal/definition"
	"github.com/buzzqw/indexarr/internal/feed"
	"github.com/buzzqw/indexarr/internal/indexer"
	"github.com/buzzqw/indexarr/internal/store"
	"github.com/buzzqw/indexarr/internal/vault"
)

// vaultNonceSize is the AES-GCM nonce length used by vault.SealString.
const vaultNonceSize = 12

var (
	// ErrStoreRequired is returned by New without a store.
	ErrStoreRequired = errors.New("indexersvc: store is required")
	// ErrVaultRequired is returned when secrets are supplied without a vault.
	ErrVaultRequired = errors.New("indexersvc: vault not configured")
	// ErrDefinitionNotFound marks an unknown definition id.
	ErrDefinitionNotFound = errors.New("definition not found")
	// ErrIndexerNotFound marks an unknown indexer id.
	ErrIndexerNotFound = errors.New("indexer not found")
	// ErrSlugTaken marks a duplicate indexer slug.
	ErrSlugTaken = errors.New("indexer slug already exists")
)

// Config wires the service to the store, the vault and the runtime manager.
type Config struct {
	Store   *store.Store
	Vault   *vault.Vault
	Manager *indexer.Manager
}

// Service implements the native indexer use cases of the web UI.
type Service struct {
	store   *store.Store
	vault   *vault.Vault
	manager *indexer.Manager
}

// New builds a Service. The store is required; the vault and the manager are
// optional and only needed for secrets and cache invalidation.
func New(cfg Config) (*Service, error) {
	if cfg.Store == nil {
		return nil, ErrStoreRequired
	}
	return &Service{store: cfg.Store, vault: cfg.Vault, manager: cfg.Manager}, nil
}

// Input is one indexer form submission. Settings mixes public values and
// secrets; the service decides where each key belongs.
type Input struct {
	DefinitionID string
	Name         string
	Slug         string
	Protocol     string
	Priority     *int
	Tags         []string
	Enabled      *bool
	// IntervalSeconds overrides the global scheduler interval when positive.
	// Nil means "use the definition suggestion, then the global default"; an
	// explicit zero means the global default.
	IntervalSeconds *int
	Settings        map[string]string
}

// Create validates the definition, splits the settings, stores the public
// configuration in config_json and seals the secrets in the vault.
func (s *Service) Create(ctx context.Context, in Input) (store.Indexer, error) {
	definitionID := strings.TrimSpace(in.DefinitionID)
	if definitionID == "" {
		return store.Indexer{}, errors.New("definition_id is required")
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return store.Indexer{}, errors.New("name is required")
	}

	parsed, err := s.parseDefinition(ctx, definitionID)
	if err != nil {
		return store.Indexer{}, err
	}

	protocol := strings.ToLower(strings.TrimSpace(in.Protocol))
	if protocol == "" {
		protocol = strings.ToLower(strings.TrimSpace(parsed.Meta.Protocol))
	}
	if !validProtocol(protocol) {
		return store.Indexer{}, errors.New("protocol must be torrent or usenet")
	}

	settings := normalizeSettings(in.Settings)
	for _, setting := range parsed.Settings {
		if setting.Required && settings[setting.Name] == "" {
			return store.Indexer{}, fmt.Errorf("missing required setting: %s", setting.Name)
		}
	}

	slug := NormalizeSlug(in.Slug)
	if slug == "" {
		slug = NormalizeSlug(name)
	}
	if slug == "" {
		return store.Indexer{}, errors.New("slug is required")
	}
	if err := s.ensureSlugAvailable(ctx, slug, 0); err != nil {
		return store.Indexer{}, err
	}

	public, secrets, _, err := splitSettings(settings)
	if err != nil {
		return store.Indexer{}, err
	}
	if len(secrets) > 0 && s.vault == nil {
		return store.Indexer{}, ErrVaultRequired
	}
	sealed, err := s.sealSecrets(secrets)
	if err != nil {
		return store.Indexer{}, err
	}

	configJSON, err := encodePublic(public)
	if err != nil {
		return store.Indexer{}, err
	}

	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	priority := 25
	if in.Priority != nil {
		priority = *in.Priority
	}
	interval, err := resolveInterval(in.IntervalSeconds, parsed.Meta.Interval.Duration())
	if err != nil {
		return store.Indexer{}, err
	}

	id, err := s.store.CreateIndexer(ctx, store.Indexer{
		DefinitionID:    definitionID,
		Protocol:        protocol,
		Slug:            slug,
		Name:            name,
		Enabled:         enabled,
		Priority:        priority,
		ConfigJSON:      configJSON,
		Tags:            joinTags(in.Tags),
		IntervalSeconds: interval,
	})
	if err != nil {
		if isUniqueError(err) {
			return store.Indexer{}, ErrSlugTaken
		}
		return store.Indexer{}, err
	}

	for key, secret := range sealed {
		if err := s.store.PutSecret(ctx, store.Secret{
			IndexerID:  id,
			Key:        key,
			Nonce:      secret.nonce,
			Ciphertext: secret.ciphertext,
		}); err != nil {
			_ = s.store.DeleteIndexer(ctx, id)
			return store.Indexer{}, err
		}
	}
	s.invalidate(id)
	return s.store.GetIndexer(ctx, id)
}

// Update applies the editable fields. Secret values left empty are deleted,
// while a secret omitted from the input is kept unchanged.
func (s *Service) Update(ctx context.Context, id int64, in Input) (store.Indexer, error) {
	current, err := s.store.GetIndexer(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return store.Indexer{}, ErrIndexerNotFound
		}
		return store.Indexer{}, err
	}

	if definitionID := strings.TrimSpace(in.DefinitionID); definitionID != "" &&
		definitionID != current.DefinitionID {
		if _, err := s.store.GetDefinition(ctx, definitionID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return store.Indexer{}, ErrDefinitionNotFound
			}
			return store.Indexer{}, err
		}
		current.DefinitionID = definitionID
	}

	parsed, err := s.parseDefinition(ctx, current.DefinitionID)
	if err != nil {
		return store.Indexer{}, err
	}

	if name := strings.TrimSpace(in.Name); name != "" {
		current.Name = name
	}
	if in.Slug != "" {
		slug := NormalizeSlug(in.Slug)
		if slug == "" {
			return store.Indexer{}, errors.New("slug is required")
		}
		if err := s.ensureSlugAvailable(ctx, slug, current.ID); err != nil {
			return store.Indexer{}, err
		}
		current.Slug = slug
	}
	if protocol := strings.ToLower(strings.TrimSpace(in.Protocol)); protocol != "" {
		if !validProtocol(protocol) {
			return store.Indexer{}, errors.New("protocol must be torrent or usenet")
		}
		current.Protocol = protocol
	}
	if in.Priority != nil {
		current.Priority = *in.Priority
	}
	if in.IntervalSeconds != nil {
		interval, err := resolveInterval(in.IntervalSeconds, 0)
		if err != nil {
			return store.Indexer{}, err
		}
		current.IntervalSeconds = interval
	}
	if in.Enabled != nil {
		current.Enabled = *in.Enabled
	}
	if in.Tags != nil {
		current.Tags = joinTags(in.Tags)
	}

	var sealed map[string]sealedSecret
	var deleteKeys []string
	if in.Settings != nil {
		config, err := decodePublic(current.ConfigJSON)
		if err != nil {
			return store.Indexer{}, err
		}
		existingSecrets, err := s.secretKeySet(ctx, current.ID)
		if err != nil {
			return store.Indexer{}, err
		}
		settings := normalizeSettings(in.Settings)

		if err := validateRequired(parsed, settings, config, existingSecrets); err != nil {
			return store.Indexer{}, err
		}

		public, secrets, deletes, err := splitSettings(settings)
		if err != nil {
			return store.Indexer{}, err
		}
		// A URL that becomes sensitive moves from config_json to the vault, so
		// the stale public value must not survive in the database.
		for key := range secrets {
			delete(config, key)
		}
		// A secret replaced by a plain value must be removed from the vault,
		// otherwise the stale secret would win when the connector is built.
		for key, value := range public {
			if existingSecrets[key] {
				deletes = append(deletes, key)
			}
			if value == "" {
				delete(config, key)
				continue
			}
			config[key] = value
		}
		configJSON, err := json.Marshal(config)
		if err != nil {
			return store.Indexer{}, err
		}
		current.ConfigJSON = string(configJSON)

		if len(secrets) > 0 && s.vault == nil {
			return store.Indexer{}, ErrVaultRequired
		}
		sealed, err = s.sealSecrets(secrets)
		if err != nil {
			return store.Indexer{}, err
		}
		deleteKeys = deletes
	}

	if err := s.store.UpdateIndexer(ctx, current); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return store.Indexer{}, ErrIndexerNotFound
		}
		if isUniqueError(err) {
			return store.Indexer{}, ErrSlugTaken
		}
		return store.Indexer{}, err
	}
	for key, secret := range sealed {
		if err := s.store.PutSecret(ctx, store.Secret{
			IndexerID:  id,
			Key:        key,
			Nonce:      secret.nonce,
			Ciphertext: secret.ciphertext,
		}); err != nil {
			return store.Indexer{}, err
		}
	}
	for _, key := range deleteKeys {
		if err := s.store.DeleteSecret(ctx, id, key); err != nil {
			return store.Indexer{}, err
		}
	}
	s.invalidate(id)
	return s.store.GetIndexer(ctx, id)
}

// Delete removes an indexer and invalidates its cached connector.
func (s *Service) Delete(ctx context.Context, id int64) error {
	if _, err := s.store.GetIndexer(ctx, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrIndexerNotFound
		}
		return err
	}
	s.invalidate(id)
	return s.store.DeleteIndexer(ctx, id)
}

// SecretKeys returns the names of the stored secrets of an indexer. It never
// returns their values.
func (s *Service) SecretKeys(ctx context.Context, id int64) ([]string, error) {
	secrets, err := s.store.ListSecrets(ctx, id)
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(secrets))
	for _, secret := range secrets {
		keys = append(keys, secret.Key)
	}
	return keys, nil
}

// Definitions returns every stored definition.
func (s *Service) Definitions(ctx context.Context) ([]store.Definition, error) {
	return s.store.ListDefinitions(ctx)
}

// Indexers returns every configured indexer.
func (s *Service) Indexers(ctx context.Context) ([]store.Indexer, error) {
	return s.store.ListIndexers(ctx)
}

// parseDefinition loads and validates a stored definition.
func (s *Service) parseDefinition(ctx context.Context, id string) (*definition.Definition, error) {
	stored, err := s.store.GetDefinition(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("%w: %s", ErrDefinitionNotFound, id)
		}
		return nil, err
	}
	parsed, err := definition.Parse([]byte(stored.Body))
	if err != nil {
		return nil, fmt.Errorf("definition %s: %w", id, err)
	}
	return parsed, nil
}

// ensureSlugAvailable reports ErrSlugTaken when another indexer owns the slug.
func (s *Service) ensureSlugAvailable(ctx context.Context, slug string, self int64) error {
	in, err := s.store.GetIndexerBySlug(ctx, slug)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}
	if in.ID != self {
		return ErrSlugTaken
	}
	return nil
}

// invalidate drops the runtime connector of an indexer after a change.
func (s *Service) invalidate(id int64) {
	if s.manager != nil {
		s.manager.Invalidate(id)
	}
}

type sealedSecret struct {
	nonce      []byte
	ciphertext []byte
}

// sealSecrets encrypts every secret value with the vault. SealString returns a
// base64(nonce||ciphertext) blob; the store keeps the two parts separately.
func (s *Service) sealSecrets(values map[string]string) (map[string]sealedSecret, error) {
	if len(values) == 0 {
		return nil, nil
	}
	if s.vault == nil {
		return nil, ErrVaultRequired
	}
	out := make(map[string]sealedSecret, len(values))
	for key, value := range values {
		blob, err := s.vault.SealString(value)
		if err != nil {
			return nil, err
		}
		raw, err := base64.StdEncoding.DecodeString(blob)
		if err != nil {
			return nil, err
		}
		if len(raw) < vaultNonceSize {
			return nil, errors.New("indexersvc: sealed secret is too short")
		}
		out[key] = sealedSecret{
			nonce:      raw[:vaultNonceSize],
			ciphertext: raw[vaultNonceSize:],
		}
	}
	return out, nil
}

func (s *Service) secretKeySet(ctx context.Context, id int64) (map[string]bool, error) {
	secrets, err := s.store.ListSecrets(ctx, id)
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(secrets))
	for _, secret := range secrets {
		out[secret.Key] = true
	}
	return out, nil
}

// validateRequired checks the definition's required settings against the
// submitted values, the existing public configuration and the secrets already
// stored for the indexer.
func validateRequired(parsed *definition.Definition, provided map[string]string, config map[string]any, existingSecrets map[string]bool) error {
	for _, setting := range parsed.Settings {
		if !setting.Required {
			continue
		}
		// A key present with an empty value explicitly clears the setting
		// (for a secret it deletes the vault row), so it cannot satisfy a
		// required setting even when an old value exists.
		if value, present := provided[setting.Name]; present {
			if value != "" {
				continue
			}
			return fmt.Errorf("missing required setting: %s", setting.Name)
		}
		if existingSecrets[setting.Name] {
			continue
		}
		if !IsSecretKey(setting.Name) {
			if text := settingText(config[setting.Name]); text != "" {
				continue
			}
		}
		return fmt.Errorf("missing required setting: %s", setting.Name)
	}
	return nil
}

// encodePublic marshals the public configuration, dropping empty values.
func encodePublic(public map[string]string) (string, error) {
	clean := make(map[string]string, len(public))
	for key, value := range public {
		if value == "" {
			continue
		}
		clean[key] = value
	}
	encoded, err := json.Marshal(clean)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func decodePublic(raw string) (map[string]any, error) {
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

func normalizeSettings(settings map[string]string) map[string]string {
	out := make(map[string]string, len(settings))
	for key, value := range settings {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		out[key] = strings.TrimSpace(value)
	}
	return out
}

// splitSettings separates public configuration keys from secrets. An empty
// secret value is a request to delete the stored secret.
func splitSettings(settings map[string]string) (public map[string]string, secrets map[string]string, deletes []string, err error) {
	public = make(map[string]string)
	secrets = make(map[string]string)
	for key, value := range settings {
		if strings.TrimSpace(key) == "" {
			return nil, nil, nil, errors.New("settings: key must not be empty")
		}
		if IsSecretSetting(key, value) {
			if value == "" {
				deletes = append(deletes, key)
				continue
			}
			secrets[key] = value
			continue
		}
		public[key] = value
	}
	return public, secrets, deletes, nil
}

// secretFragments are the key fragments that mark a value as a secret.
var secretFragments = []string{
	"api_key", "apikey", "token", "password", "passkey", "secret", "cookie",
}

// IsSecretKey reports whether a setting name is stored in the vault.
func IsSecretKey(key string) bool {
	lower := strings.ToLower(strings.TrimSpace(key))
	for _, fragment := range secretFragments {
		if strings.Contains(lower, fragment) {
			return true
		}
	}
	return false
}

// IsURLSettingKey reports whether a setting holds a URL. Such settings are
// normally public, but a URL carrying an rsskey, passkey, apikey, key or token
// must be sealed like a password.
func IsURLSettingKey(key string) bool {
	lower := strings.ToLower(strings.TrimSpace(key))
	return lower == "url" || lower == "feed" || strings.HasSuffix(lower, "_url") || strings.HasSuffix(lower, "_uri")
}

// IsSecretSetting reports whether a setting value belongs in the vault.
func IsSecretSetting(key, value string) bool {
	if IsSecretKey(key) {
		return true
	}
	return IsURLSettingKey(key) && feed.IsSensitiveURL(value)
}

// resolveInterval returns the effective per-indexer interval. Nil means "use
// the definition suggestion when it has one"; an explicit zero means the
// global default. A positive value below one minute is rejected.
func resolveInterval(requested *int, suggested time.Duration) (int, error) {
	if requested == nil {
		if suggested >= time.Minute {
			return int(suggested / time.Second), nil
		}
		return 0, nil
	}
	seconds := *requested
	if seconds < 0 || (seconds > 0 && seconds < 60) {
		return 0, errors.New("interval_seconds must be 0 or at least 60")
	}
	return seconds, nil
}

// NormalizeSlug lowercases a value and turns every run of unsupported
// characters into a single dash.
func NormalizeSlug(raw string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(strings.TrimSpace(raw)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case b.Len() > 0 && !dash:
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

func joinTags(tags []string) string {
	out := make([]string, 0, len(tags))
	for _, tag := range tags {
		if tag = strings.TrimSpace(tag); tag != "" {
			out = append(out, tag)
		}
	}
	return strings.Join(out, ",")
}

func settingText(value any) string {
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
	default:
		encoded, err := json.Marshal(typed)
		if err != nil {
			return fmt.Sprint(typed)
		}
		return string(encoded)
	}
}

func validProtocol(protocol string) bool {
	return protocol == "torrent" || protocol == "usenet"
}

func isUniqueError(err error) bool {
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "unique constraint") || strings.Contains(text, "constraint failed: indexers.slug")
}
