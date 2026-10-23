package importers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/buzzqw/indexarr/internal/catalog"
	"github.com/buzzqw/indexarr/internal/store"
	"github.com/buzzqw/indexarr/internal/vault"
)

// AppliedIndexer is one row created or updated by ApplyTargets. Secrets are
// deliberately absent: the result is safe to print, render and log.
type AppliedIndexer struct {
	ID           int64
	Name         string
	DefinitionID string
	ViaManager   string
	Updated      bool
}

// ApplyResult summarises one import run. Created and Updated count the native
// indexers written; Skipped counts duplicate targets in the same selection;
// Errors counts the failure that stopped the run (zero when err is nil).
type ApplyResult struct {
	Applied []AppliedIndexer
	Dropped []DroppedTarget
	Created int
	Updated int
	Skipped int
	Errors  int
}

// ApplyTargets creates or updates the native indexers described by targets. It
// is the single apply path shared by cmd/indexarr-import and the /import page:
//
//   - it writes only the native indexers table and the vault, never sources and
//     never legacy_source_id;
//   - it syncs the built-in catalog first, because every indexer references a
//     definition;
//   - it is idempotent: a target already present (same definition, endpoint and
//     slug/name) is updated in place;
//   - a disabled upstream target stays disabled.
//
// Secrets are sealed with secretVault before they touch the database. When a
// target carries secrets and secretVault is nil the run fails instead of
// writing plaintext.
func ApplyTargets(ctx context.Context, st *store.Store, secretVault *vault.Vault, targets []NativeTarget) (ApplyResult, error) {
	var result ApplyResult
	if st == nil {
		return result, errors.New("importers: nil store")
	}
	if _, err := catalog.Sync(ctx, st); err != nil {
		return result, fmt.Errorf("sync catalog: %w", err)
	}
	existing, err := st.ListIndexers(ctx)
	if err != nil {
		return result, err
	}

	targets = PairChannels(targets)
	targets, result.Dropped = Deduplicate(targets)
	result.Skipped = len(result.Dropped)
	seen := make(map[string]bool, len(targets))
	for _, target := range targets {
		key := target.Key()
		if seen[key] {
			result.Skipped++
			continue
		}
		seen[key] = true

		if err := validateTarget(target); err != nil {
			result.Errors++
			return result, err
		}
		slug, current, found := chooseIndexer(existing, target)
		configJSON, err := encodeConfig(target.Config)
		if err != nil {
			result.Errors++
			return result, err
		}
		row := store.Indexer{
			DefinitionID: target.DefinitionID,
			Protocol:     string(target.Protocol),
			Slug:         slug,
			Name:         target.Name,
			Enabled:      target.Enabled,
			Priority:     target.Priority,
			ConfigJSON:   configJSON,
			Tags:         strings.Join(target.Tags, ","),
		}

		item := AppliedIndexer{
			Name:         target.Name,
			DefinitionID: target.DefinitionID,
			ViaManager:   target.ViaManager,
		}
		created := false
		if found {
			row.ID = current.ID
			row.CreatedAt = current.CreatedAt
			if err := st.UpdateIndexer(ctx, row); err != nil {
				result.Errors++
				return result, err
			}
			item.ID = current.ID
			item.Updated = true
			result.Updated++
		} else {
			id, err := st.CreateIndexer(ctx, row)
			if err != nil {
				result.Errors++
				return result, err
			}
			item.ID = id
			created = true
			result.Created++
		}

		if err := writeSecrets(ctx, st, secretVault, item.ID, target.Secrets, found); err != nil {
			if created {
				_ = st.DeleteIndexer(ctx, item.ID)
			}
			result.Errors++
			return result, err
		}
		result.Applied = append(result.Applied, item)
		existing = append(existing, store.Indexer{
			ID: item.ID, DefinitionID: target.DefinitionID,
			Slug: slug, Name: target.Name,
		})
	}
	return result, nil
}

func validateTarget(target NativeTarget) error {
	if strings.TrimSpace(target.Name) == "" {
		return errors.New("imported indexer has no name")
	}
	if strings.TrimSpace(target.DefinitionID) == "" {
		return fmt.Errorf("imported indexer %q has no definition", target.Name)
	}
	if target.ViaManager != "" {
		if strings.TrimSpace(target.BaseURL) == "" {
			return fmt.Errorf("imported indexer %q: no %s endpoint", target.Name, target.ViaManager)
		}
		if strings.TrimSpace(target.RemoteID) == "" {
			return fmt.Errorf("imported indexer %q: no %s indexer id", target.Name, target.ViaManager)
		}
	}
	return nil
}

// chooseIndexer finds the row this target already owns (idempotent re-import)
// or allocates a free slug. The key is definition + base URL + name/slug; an
// existing row with the generated slug and name is adopted even when the
// definition improved from a bridge to a verified one.
func chooseIndexer(existing []store.Indexer, target NativeTarget) (string, store.Indexer, bool) {
	slug := strings.TrimSpace(target.Slug)
	if slug == "" {
		slug = slugify(target.Name)
	}
	if slug == "" {
		slug = "imported"
	}
	base := normalizeTargetURL(target.BaseURL)

	for _, current := range existing {
		if current.DefinitionID != target.DefinitionID || !strings.EqualFold(current.Name, target.Name) {
			continue
		}
		if current.Slug == slug || normalizeTargetURL(configURL(current.ConfigJSON)) == base {
			return current.Slug, current, true
		}
	}
	for _, current := range existing {
		if current.Slug != slug {
			continue
		}
		if strings.EqualFold(current.Name, target.Name) {
			return current.Slug, current, true
		}
		return allocateSlug(existing, slug), store.Indexer{}, false
	}
	return slug, store.Indexer{}, false
}

func allocateSlug(existing []store.Indexer, base string) string {
	taken := make(map[string]bool, len(existing))
	for _, current := range existing {
		taken[current.Slug] = true
	}
	for i := 2; ; i++ {
		candidate := base + "-" + strconv.Itoa(i)
		if !taken[candidate] {
			return candidate
		}
	}
}

func writeSecrets(ctx context.Context, st *store.Store, secretVault *vault.Vault,
	indexerID int64, secrets map[string]string, clearStale bool) error {
	if len(secrets) > 0 && secretVault == nil {
		return fmt.Errorf("indexer %d: vault key required to seal secrets", indexerID)
	}
	keys := make([]string, 0, len(secrets))
	for key := range secrets {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		nonce, ciphertext, err := secretVault.Seal(secrets[key])
		if err != nil {
			return err
		}
		if err := st.PutSecret(ctx, store.Secret{
			IndexerID: indexerID, Key: key, Nonce: nonce, Ciphertext: ciphertext,
		}); err != nil {
			return err
		}
	}
	if clearStale {
		// Remove generated credentials the target no longer carries, so a
		// deleted upstream key does not keep working from a stale row.
		stored, err := st.ListSecrets(ctx, indexerID)
		if err != nil {
			return err
		}
		for _, secret := range stored {
			if secrets[secret.Key] != "" || !generatedSecretKey(secret.Key) {
				continue
			}
			if err := st.DeleteSecret(ctx, indexerID, secret.Key); err != nil {
				return err
			}
		}
	}
	return nil
}

func generatedSecretKey(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "api_key", "apikey", "api_token", "username", "password", "passkey", "token":
		return true
	default:
		return false
	}
}

func encodeConfig(config map[string]string) (string, error) {
	clean := make(map[string]string, len(config))
	for key, value := range config {
		if strings.TrimSpace(value) == "" {
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

func configURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	var config map[string]any
	if err := json.Unmarshal([]byte(raw), &config); err != nil {
		return ""
	}
	for _, key := range []string{"url", "base_url"} {
		if value, ok := config[key].(string); ok && strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
