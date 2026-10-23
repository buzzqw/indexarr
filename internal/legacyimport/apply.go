package legacyimport

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/buzzqw/indexarr/internal/definition"
	"github.com/buzzqw/indexarr/internal/store"
	"github.com/buzzqw/indexarr/internal/vault"
)

// ErrVaultRequired is returned for a source whose credentials must be sealed
// but no vault is available. The admin API maps it to HTTP 503.
var ErrVaultRequired = errors.New("legacyimport: vault not configured, cannot seal source credentials")

// ApplyOptions controls an apply run. An empty SourceIDs migrates every
// eligible source. DryRun reports what would happen without writing anything.
//
// DisableSource is kept for callers that want to state the intent explicitly:
// the migration always disables a legacy source after its native indexer is
// written, which is what makes the bridge empty. There is no way to apply the
// migration and keep the legacy scheduler running, because that would produce
// duplicate releases.
type ApplyOptions struct {
	SourceIDs     []int64
	DryRun        bool
	DisableSource bool
}

// ApplyResult is the outcome for one legacy source. Err is never encoded by
// the admin API; the API exposes its redacted text in the error field.
type ApplyResult struct {
	SourceID     int64  `json:"source_id"`
	Name         string `json:"name"`
	DefinitionID string `json:"definition_id"`
	IndexerID    int64  `json:"indexer_id"`
	Applied      bool   `json:"applied"`
	Skipped      bool   `json:"skipped"`
	Reason       string `json:"reason"`
	Err          error  `json:"-"`
}

// Apply migrates legacy Torznab sources to native definitions and indexers.
//
// For every eligible source it upserts the deterministic native-<id>
// definition, updates the bridged indexer in place (same id and slug), seals
// the credentials in the vault and only then disables the legacy source. Every
// step is compensated on failure, so the bridge keeps working. Repeating an
// apply is a no-op.
func Apply(ctx context.Context, st *store.Store, v *vault.Vault, opts ApplyOptions) ([]ApplyResult, error) {
	if st == nil {
		return nil, errors.New("legacyimport: store is required")
	}
	allSources, err := st.ListSources(ctx)
	if err != nil {
		return nil, fmt.Errorf("legacyimport: list sources: %w", err)
	}
	indexers, err := st.ListIndexers(ctx)
	if err != nil {
		return nil, fmt.Errorf("legacyimport: list indexers: %w", err)
	}

	bySource := make(map[int64]store.Indexer, len(indexers))
	nativeIndexers := make(map[string]store.Indexer, len(indexers))
	for _, in := range indexers {
		if in.LegacySourceID > 0 {
			if _, exists := bySource[in.LegacySourceID]; !exists {
				bySource[in.LegacySourceID] = in
			}
		}
		nativeIndexers[in.DefinitionID] = in
	}

	type selection struct {
		id     int64
		source *store.Source
	}
	var selected []selection
	if len(opts.SourceIDs) == 0 {
		for i := range allSources {
			selected = append(selected, selection{id: allSources[i].ID, source: &allSources[i]})
		}
	} else {
		byID := make(map[int64]*store.Source, len(allSources))
		for i := range allSources {
			byID[allSources[i].ID] = &allSources[i]
		}
		for _, id := range opts.SourceIDs {
			selected = append(selected, selection{id: id, source: byID[id]})
		}
	}

	results := make([]ApplyResult, 0, len(selected))
	for _, item := range selected {
		if item.source == nil {
			results = append(results, ApplyResult{
				SourceID: item.id,
				Skipped:  true,
				Reason:   "source not found",
				Err:      fmt.Errorf("legacyimport: source %d not found", item.id),
			})
			continue
		}
		results = append(results, applyOne(ctx, st, v, *item.source, bySource[item.id], nativeIndexers, opts.DryRun))
	}
	return results, nil
}

func applyOne(ctx context.Context, st *store.Store, v *vault.Vault, source store.Source, bridge store.Indexer, nativeIndexers map[string]store.Indexer, dryRun bool) ApplyResult {
	result := ApplyResult{SourceID: source.ID, Name: source.Name}
	nativeID := nativeDefinitionID(source.ID)

	if in, ok := nativeIndexers[nativeID]; ok {
		result.DefinitionID = nativeID
		result.IndexerID = in.ID
		result.Skipped = true
		result.Reason = "already native"
		return result
	}

	secrets := sourceSecrets(source)
	skip := func(reason string) ApplyResult {
		result.Skipped = true
		result.Reason = redactSecrets(reason, secrets)
		return result
	}

	if strings.ToLower(strings.TrimSpace(source.Kind)) != "torznab" {
		return skip("source is not a Torznab source")
	}
	parsedURL, _ := url.Parse(strings.TrimSpace(source.URL))
	if err := nativeURL(source.URL); err != nil {
		return skip(err.Error())
	}
	cleanURL := sanitizedURL(source.URL)
	if cleanURL == "" {
		return skip("source URL cannot be sanitized")
	}
	if bridge.ID <= 0 {
		return skip("source has no bridged indexer")
	}
	if hasBasicAuth(source, parsedURL) {
		return skip("the Torznab family connector does not support HTTP Basic authentication; source credentials were left untouched")
	}

	apiKey := strings.TrimSpace(source.APIKey)
	if apiKey == "" {
		apiKey = apiKeyFromURL(parsedURL)
	}
	if apiKey != "" && v == nil {
		result.Skipped = true
		result.Reason = redactSecrets(ErrVaultRequired.Error(), secrets)
		result.Err = fmt.Errorf("%w: source %d", ErrVaultRequired, source.ID)
		return result
	}

	if dryRun {
		result.DefinitionID = nativeID
		result.IndexerID = bridge.ID
		result.Reason = "dry run: would migrate source to " + nativeID
		return result
	}

	body, err := nativeDefinitionBody(source, cleanURL)
	if err != nil {
		result.Skipped = true
		result.Err = errors.New(redactSecrets(err.Error(), secrets))
		result.Reason = result.Err.Error()
		return result
	}
	parsedDefinition, err := definition.Parse(body)
	if err != nil {
		result.Skipped = true
		result.Err = errors.New(redactSecrets("native definition is invalid: "+err.Error(), secrets))
		result.Reason = result.Err.Error()
		return result
	}
	if parsedDefinition.Meta.ID != nativeID {
		result.Skipped = true
		result.Err = errors.New("native definition id mismatch")
		result.Reason = result.Err.Error()
		return result
	}

	configJSON, err := nativeConfigJSON(bridge.ConfigJSON, cleanURL, source)
	if err != nil {
		result.Skipped = true
		result.Err = errors.New(redactSecrets("encode native config: "+err.Error(), secrets))
		result.Reason = result.Err.Error()
		return result
	}

	definitionExisted, err := definitionExists(ctx, st, nativeID)
	if err != nil {
		result.Skipped = true
		result.Err = errors.New(redactSecrets("read native definition: "+err.Error(), secrets))
		result.Reason = result.Err.Error()
		return result
	}

	previousSecrets, err := secretsOf(ctx, st, bridge.ID)
	if err != nil {
		result.Skipped = true
		result.Err = errors.New(redactSecrets("read existing secrets: "+err.Error(), secrets))
		result.Reason = result.Err.Error()
		return result
	}

	rollback := func(cause error) ApplyResult {
		var rollbackErrs []string
		if err := st.UpdateIndexer(ctx, bridge); err != nil {
			rollbackErrs = append(rollbackErrs, redactSecrets(err.Error(), secrets))
		}
		for _, secret := range previousSecrets {
			if err := st.PutSecret(ctx, secret); err != nil {
				rollbackErrs = append(rollbackErrs, redactSecrets(err.Error(), secrets))
			}
		}
		if apiKey != "" {
			if _, existed := previousSecrets["apikey"]; !existed {
				if err := st.DeleteSecret(ctx, bridge.ID, "apikey"); err != nil {
					rollbackErrs = append(rollbackErrs, redactSecrets(err.Error(), secrets))
				}
			}
		}
		if !definitionExisted {
			if err := deleteUnreferencedDefinition(ctx, st, nativeID); err != nil {
				rollbackErrs = append(rollbackErrs, redactSecrets(err.Error(), secrets))
			}
		}
		if err := st.SetSourceEnabled(ctx, source.ID, source.Enabled); err != nil {
			rollbackErrs = append(rollbackErrs, redactSecrets(err.Error(), secrets))
		}

		result.Skipped = true
		result.Err = errors.New(redactSecrets(cause.Error(), secrets))
		result.Reason = result.Err.Error()
		if len(rollbackErrs) > 0 {
			result.Reason += "; rollback failed: " + strings.Join(rollbackErrs, "; ")
		}
		return result
	}

	def := store.Definition{
		ID:            nativeID,
		SchemaVersion: 1,
		Family:        "torznab",
		State:         "verificata",
		Origin:        "local",
		Author:        "migration",
		Body:          string(body),
		Hash:          definition.Fingerprint(body),
	}
	if err := st.UpsertDefinition(ctx, def); err != nil {
		return rollback(fmt.Errorf("store native definition: %w", err))
	}

	updated := bridge
	updated.DefinitionID = nativeID
	updated.Protocol = "torrent"
	updated.ConfigJSON = configJSON
	updated.LegacySourceID = 0
	if err := st.UpdateIndexer(ctx, updated); err != nil {
		return rollback(fmt.Errorf("update bridged indexer: %w", err))
	}

	if apiKey != "" {
		nonce, ciphertext, err := v.Seal(apiKey)
		if err != nil {
			return rollback(fmt.Errorf("seal source credentials: %w", err))
		}
		if err := st.PutSecret(ctx, store.Secret{
			IndexerID:  bridge.ID,
			Key:        "apikey",
			Nonce:      nonce,
			Ciphertext: ciphertext,
		}); err != nil {
			return rollback(fmt.Errorf("store source credentials: %w", err))
		}
	}

	if err := st.SetSourceEnabled(ctx, source.ID, false); err != nil {
		return rollback(fmt.Errorf("disable legacy source: %w", err))
	}

	result.DefinitionID = nativeID
	result.IndexerID = bridge.ID
	result.Applied = true
	return result
}

// Revert moves sources back to the legacy bridge. It is the inverse of Apply:
// the indexer keeps its id and slug, regains legacy-<id> and legacy_source_id,
// and the legacy source is enabled again. Reverting twice is a no-op.
func Revert(ctx context.Context, st *store.Store, sourceIDs []int64) ([]ApplyResult, error) {
	if st == nil {
		return nil, errors.New("legacyimport: store is required")
	}
	if len(sourceIDs) == 0 {
		return []ApplyResult{}, nil
	}
	indexers, err := st.ListIndexers(ctx)
	if err != nil {
		return nil, fmt.Errorf("legacyimport: list indexers: %w", err)
	}
	byNative := make(map[string]store.Indexer, len(indexers))
	bySource := make(map[int64]store.Indexer, len(indexers))
	for _, in := range indexers {
		byNative[in.DefinitionID] = in
		if in.LegacySourceID > 0 {
			bySource[in.LegacySourceID] = in
		}
	}

	results := make([]ApplyResult, 0, len(sourceIDs))
	for _, id := range sourceIDs {
		results = append(results, revertOne(ctx, st, id, byNative, bySource))
	}
	return results, nil
}

func revertOne(ctx context.Context, st *store.Store, sourceID int64, byNative map[string]store.Indexer, bySource map[int64]store.Indexer) ApplyResult {
	result := ApplyResult{SourceID: sourceID}
	source, err := st.GetSource(ctx, sourceID)
	if err != nil {
		result.Skipped = true
		result.Reason = "source not found"
		result.Err = fmt.Errorf("legacyimport: source %d not found", sourceID)
		return result
	}
	result.Name = source.Name

	nativeID := nativeDefinitionID(sourceID)
	legacyID := legacyDefinitionID(sourceID)
	secrets := sourceSecrets(source)

	in, found := byNative[nativeID]
	if !found {
		in, found = bySource[sourceID]
	}
	if !found {
		result.Skipped = true
		result.Reason = "source has no indexer to revert"
		return result
	}
	result.IndexerID = in.ID
	if in.DefinitionID == legacyID && in.LegacySourceID == source.ID && source.Enabled {
		result.DefinitionID = legacyID
		result.Skipped = true
		result.Reason = "already bridged"
		return result
	}

	// Keep an existing legacy definition untouched: it may have been edited
	// since the backfill. Only create the minimal bridge definition when it is
	// missing.
	legacyExists, err := definitionExists(ctx, st, legacyID)
	if err != nil {
		result.Skipped = true
		result.Err = errors.New(redactSecrets("read legacy definition: "+err.Error(), secrets))
		result.Reason = result.Err.Error()
		return result
	}
	if !legacyExists {
		body, err := legacyDefinitionBody(source)
		if err != nil {
			result.Skipped = true
			result.Err = errors.New(redactSecrets(err.Error(), secrets))
			result.Reason = result.Err.Error()
			return result
		}
		if _, err := definition.Parse(body); err != nil {
			// The legacy body is intentionally minimal and is never executed by
			// the native engine; validation is still applied so a malformed
			// source is reported instead of stored.
			result.Skipped = true
			result.Err = errors.New(redactSecrets("legacy definition is invalid: "+err.Error(), secrets))
			result.Reason = result.Err.Error()
			return result
		}
		if err := st.UpsertDefinition(ctx, store.Definition{
			ID:            legacyID,
			SchemaVersion: 1,
			Family:        strings.ToLower(strings.TrimSpace(source.Kind)),
			State:         "verificata",
			Origin:        "local",
			Author:        "migration",
			Body:          string(body),
			Hash:          definition.Fingerprint(body),
		}); err != nil {
			result.Skipped = true
			result.Err = errors.New(redactSecrets("store legacy definition: "+err.Error(), secrets))
			result.Reason = result.Err.Error()
			return result
		}
	}

	configJSON, err := legacyConfigJSON(source)
	if err != nil {
		result.Skipped = true
		result.Err = errors.New(redactSecrets("encode legacy config: "+err.Error(), secrets))
		result.Reason = result.Err.Error()
		return result
	}
	updated := in
	updated.DefinitionID = legacyID
	updated.Protocol = "torrent"
	updated.ConfigJSON = configJSON
	updated.LegacySourceID = source.ID
	if err := st.UpdateIndexer(ctx, updated); err != nil {
		result.Skipped = true
		result.Err = errors.New(redactSecrets("update indexer: "+err.Error(), secrets))
		result.Reason = result.Err.Error()
		return result
	}
	if err := st.SetSourceEnabled(ctx, source.ID, true); err != nil {
		result.Skipped = true
		result.Err = errors.New(redactSecrets("enable legacy source: "+err.Error(), secrets))
		result.Reason = result.Err.Error()
		return result
	}
	// The store has no DeleteDefinition API. A definition may only disappear
	// when no indexer references it, so the guard lives in the statement.
	if err := deleteUnreferencedDefinition(ctx, st, nativeID); err != nil {
		result.Skipped = true
		result.Err = errors.New(redactSecrets("delete native definition: "+err.Error(), secrets))
		result.Reason = result.Err.Error()
		return result
	}

	result.DefinitionID = legacyID
	result.Applied = true
	return result
}

func nativeDefinitionID(sourceID int64) string {
	return "native-" + strconv.FormatInt(sourceID, 10)
}

func legacyDefinitionID(sourceID int64) string {
	return "legacy-" + strconv.FormatInt(sourceID, 10)
}

// nativeSetting differs from definition.Setting only in always writing
// required, so the generated document matches the documented migration body.
type nativeSetting struct {
	Name     string `yaml:"name"`
	Type     string `yaml:"type,omitempty"`
	Required bool   `yaml:"required"`
}

// nativeDefinitionBody renders the v1 document for a migrated Torznab source.
// The URL is stored without userinfo or credential query parameters; the
// credentials live in the vault.
func nativeDefinitionBody(source store.Source, cleanURL string) ([]byte, error) {
	ids := source.CategoryIDs()
	categories := make([]definition.Category, 0, len(ids))
	for _, id := range ids {
		if id < 1000 || id > 8999 {
			continue
		}
		text := strconv.Itoa(id)
		categories = append(categories, definition.Category{Site: text, Standard: id, Desc: text})
	}
	doc := struct {
		Schema       int                     `yaml:"schema"`
		Connector    string                  `yaml:"connector"`
		Meta         definition.Meta         `yaml:"meta"`
		Settings     []nativeSetting         `yaml:"settings"`
		Categories   []definition.Category   `yaml:"categories,omitempty"`
		Capabilities definition.Capabilities `yaml:"capabilities"`
	}{
		Schema:    1,
		Connector: "torznab",
		Meta: definition.Meta{
			ID:       nativeDefinitionID(source.ID),
			Name:     source.Name,
			Type:     "private",
			Protocol: "torrent",
			URLs:     []string{cleanURL},
		},
		Settings: []nativeSetting{
			{Name: "apikey", Type: "password", Required: false},
			{Name: "username", Type: "text", Required: false},
			{Name: "password", Type: "password", Required: false},
		},
		Categories: categories,
		Capabilities: definition.Capabilities{
			Search:      []string{"q"},
			TVSearch:    []string{"q", "season", "ep", "imdbid", "tvdbid", "tmdbid"},
			MovieSearch: []string{"q", "imdbid", "tmdbid"},
		},
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// nativeConfigJSON keeps the non-secret settings a native Torznab indexer
// needs. Unrelated bridged keys (kind, manager, ...) are dropped; user_agent
// and allow_private survive because they are still meaningful natively.
func nativeConfigJSON(existing, cleanURL string, source store.Source) (string, error) {
	config := map[string]any{}
	if strings.TrimSpace(existing) != "" {
		if err := json.Unmarshal([]byte(existing), &config); err != nil {
			config = map[string]any{}
		}
	}
	for _, key := range []string{"kind", "manager", "default_category", "interval_seconds", "query"} {
		delete(config, key)
	}
	config["base_url"] = cleanURL
	config["url"] = cleanURL
	categories := source.CategoryIDs()
	if categories == nil {
		categories = []int{}
	}
	config["categories"] = categories
	if query := strings.TrimSpace(source.Query); query != "" {
		config["query"] = query
	}
	if agent, _ := config["user_agent"].(string); strings.TrimSpace(agent) == "" {
		delete(config, "user_agent")
	}
	encoded, err := json.Marshal(config)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

// legacyConfigJSON mirrors the shape written by the store backfill.
func legacyConfigJSON(source store.Source) (string, error) {
	categories := source.CategoryIDs()
	if categories == nil {
		categories = []int{}
	}
	encoded, err := json.Marshal(struct {
		Kind            string `json:"kind"`
		Manager         string `json:"manager"`
		URL             string `json:"url"`
		Categories      []int  `json:"categories"`
		DefaultCategory string `json:"default_category"`
		Query           string `json:"query"`
		IntervalSeconds int    `json:"interval_seconds"`
	}{
		Kind:            source.Kind,
		Manager:         source.Manager,
		URL:             sanitizedURL(source.URL),
		Categories:      categories,
		DefaultCategory: source.DefaultCategory,
		Query:           source.Query,
		IntervalSeconds: source.IntervalSeconds,
	})
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

// legacyDefinitionBody mirrors the minimal bridge definition created by
// store.BackfillLegacySources.
func legacyDefinitionBody(source store.Source) ([]byte, error) {
	doc := struct {
		Schema    int    `yaml:"schema"`
		Connector string `yaml:"connector"`
		Meta      struct {
			ID       string   `yaml:"id"`
			Name     string   `yaml:"name"`
			Type     string   `yaml:"type"`
			Protocol string   `yaml:"protocol"`
			URLs     []string `yaml:"urls"`
		} `yaml:"meta"`
	}{
		Schema:    1,
		Connector: strings.ToLower(strings.TrimSpace(source.Kind)),
	}
	doc.Meta.ID = legacyDefinitionID(source.ID)
	doc.Meta.Name = source.Name
	doc.Meta.Type = "private"
	doc.Meta.Protocol = "torrent"
	doc.Meta.URLs = []string{sanitizedURL(source.URL)}

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func definitionExists(ctx context.Context, st *store.Store, id string) (bool, error) {
	_, err := st.GetDefinition(ctx, id)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return false, err
}

func secretsOf(ctx context.Context, st *store.Store, indexerID int64) (map[string]store.Secret, error) {
	list, err := st.ListSecrets(ctx, indexerID)
	if err != nil {
		return nil, err
	}
	out := make(map[string]store.Secret, len(list))
	for _, secret := range list {
		out[secret.Key] = secret
	}
	return out, nil
}

// deleteUnreferencedDefinition removes a definition only when no indexer
// points at it. The store has no DeleteDefinition method, hence the
// parameterized statement through DB().
func deleteUnreferencedDefinition(ctx context.Context, st *store.Store, id string) error {
	_, err := st.DB().ExecContext(ctx, `
		DELETE FROM definitions
		WHERE id = ?
		  AND NOT EXISTS (SELECT 1 FROM indexers WHERE definition_id = ?)`, id, id)
	return err
}

// hasBasicAuth reports whether the source carries HTTP Basic credentials,
// either in its fields or embedded in the URL.
func hasBasicAuth(source store.Source, parsed *url.URL) bool {
	if strings.TrimSpace(source.Username) != "" || strings.TrimSpace(source.Password) != "" {
		return true
	}
	return parsed != nil && parsed.User != nil && parsed.User.Username() != ""
}

// normalizeParam lowercases a query parameter name and removes separators so
// that api_key, api-key and api.key compare equal.
func normalizeParam(name string) string {
	normalized := strings.ToLower(strings.TrimSpace(name))
	return strings.NewReplacer("_", "", "-", "", ".", "").Replace(normalized)
}

// apiKeyFromURL recovers a Torznab API key embedded in a legacy URL. This is
// common in Jackett/Prowlarr sources and would otherwise be dropped by the
// URL sanitization.
func apiKeyFromURL(parsed *url.URL) string {
	if parsed == nil {
		return ""
	}
	query := parsed.Query()
	for key, values := range query {
		normalized := normalizeParam(key)
		if normalized != "apikey" && normalized != "apitoken" {
			continue
		}
		for _, value := range values {
			if candidate := strings.TrimSpace(value); candidate != "" {
				return candidate
			}
		}
	}
	return ""
}

// sourceSecrets lists every value that must never appear in a result, a log
// line or the admin JSON.
func sourceSecrets(source store.Source) []string {
	values := []string{source.APIKey, source.Username, source.Password}
	if parsed, err := url.Parse(strings.TrimSpace(source.URL)); err == nil {
		if parsed.User != nil {
			values = append(values, parsed.User.Username())
			if password, ok := parsed.User.Password(); ok {
				values = append(values, password)
			}
		}
		for key, list := range parsed.Query() {
			if isSecretParam(key) {
				values = append(values, list...)
			}
		}
	}
	return values
}

// redactSecrets removes known credentials and their URL-escaped form. Values
// shorter than four bytes are ignored so that generic messages stay readable.
func redactSecrets(text string, secrets []string) string {
	for _, secret := range secrets {
		secret = strings.TrimSpace(secret)
		if len(secret) < 4 {
			continue
		}
		text = strings.ReplaceAll(text, secret, "[redacted]")
		text = strings.ReplaceAll(text, url.QueryEscape(secret), "[redacted]")
	}
	return text
}
