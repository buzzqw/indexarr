package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/buzzqw/indexarr/internal/importers"
	"github.com/buzzqw/indexarr/internal/store"
	"github.com/buzzqw/indexarr/internal/vault"
)

const (
	testProwlarrKey = "prowlarr-key-SECRET"
	testAlphaKey    = "alpha-key-SECRET"
	testAlphaUser   = "alpha-user-SECRET"
	testAlphaPass   = "alpha-pass-SECRET"
)

func testPlan() importers.Plan {
	return importers.Plan{
		Source:        "prowlarr",
		ManagerURL:    "http://prowlarr:9696",
		ManagerAPIKey: testProwlarrKey,
		Indexers: []importers.Indexer{
			{
				Name: "ExtTo", Implementation: "Cardigann", Protocol: importers.ProtocolTorrent,
				DefinitionHint: "extto", RemoteID: "11",
				BaseURL: "https://extto.org/", SiteURL: "https://extto.org/",
				Enabled: true, Priority: 10, Categories: []int{5000, 2000}, Tags: []string{"tv"},
			},
			{
				Name: "Alpha Tracker", Implementation: "Torznab", Protocol: importers.ProtocolTorrent,
				DefinitionHint: "torznab-alpha", RemoteID: "16",
				BaseURL: "https://alpha.example.invalid/", SiteURL: "https://alpha.example.invalid/",
				APIKey: testAlphaKey, Username: testAlphaUser, Password: testAlphaPass,
				Enabled: true, Priority: 20, Categories: []int{2000, 5000}, Tags: []string{"hd"},
			},
			{
				Name: "Disabled Site", Implementation: "Cardigann", Protocol: importers.ProtocolTorrent,
				DefinitionHint: "disabled-site", RemoteID: "17",
				BaseURL: "https://disabled.example.invalid/", SiteURL: "https://disabled.example.invalid/",
				Enabled: false, Priority: 30,
			},
		},
	}
}

// applyPlan is the test-facing name of the shared apply path: the CLI opens
// the database exactly as it does at runtime and lets importers do the work.
func applyPlan(ctx context.Context, dbPath string, v *vault.Vault, targets []importers.NativeTarget) (importers.ApplyResult, error) {
	return applyTargets(ctx, dbPath, v, targets)
}

func testVault(t *testing.T) *vault.Vault {
	t.Helper()
	v, err := vault.New(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatalf("vault.New: %v", err)
	}
	return v
}

func openStore(t *testing.T, path string) *store.Store {
	t.Helper()
	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestApplyCreatesNativeIndexersAndSealsSecrets(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "import.db")
	v := testVault(t)

	targets, err := importers.Targets(testPlan())
	if err != nil {
		t.Fatalf("Targets: %v", err)
	}
	result, err := applyPlan(ctx, dbPath, v, targets)
	if err != nil {
		t.Fatalf("applyPlan: %v", err)
	}
	if result.Created != 3 || result.Updated != 0 || len(result.Applied) != 3 {
		t.Fatalf("result = %+v", result)
	}

	st := openStore(t, dbPath)
	rows, err := st.ListIndexers(ctx)
	if err != nil {
		t.Fatalf("ListIndexers: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("got %d indexers, want 3: %+v", len(rows), rows)
	}

	var sources int
	if err := st.DB().QueryRowContext(ctx, `SELECT count(*) FROM sources`).Scan(&sources); err != nil {
		t.Fatalf("count sources: %v", err)
	}
	if sources != 0 {
		t.Fatalf("apply wrote %d legacy sources, want 0", sources)
	}
	var legacy int
	if err := st.DB().QueryRowContext(ctx,
		`SELECT count(*) FROM indexers WHERE legacy_source_id IS NOT NULL`).Scan(&legacy); err != nil {
		t.Fatalf("count legacy indexers: %v", err)
	}
	if legacy != 0 {
		t.Fatalf("apply wrote %d indexers with legacy_source_id", legacy)
	}

	byName := make(map[string]store.Indexer, len(rows))
	for _, row := range rows {
		byName[row.Name] = row
	}

	extto := byName["ExtTo"]
	if extto.DefinitionID != "extto" || !extto.Enabled {
		t.Fatalf("ExtTo row = %+v", extto)
	}
	var exttoConfig map[string]string
	if err := json.Unmarshal([]byte(extto.ConfigJSON), &exttoConfig); err != nil {
		t.Fatalf("ExtTo config: %v", err)
	}
	if exttoConfig["filter"] != "c=TV" {
		t.Fatalf("ExtTo config = %v", exttoConfig)
	}

	alpha := byName["Alpha Tracker"]
	if alpha.DefinitionID != "torznab-generic" {
		t.Fatalf("Alpha definition = %q", alpha.DefinitionID)
	}
	var alphaConfig map[string]string
	if err := json.Unmarshal([]byte(alpha.ConfigJSON), &alphaConfig); err != nil {
		t.Fatalf("Alpha config: %v", err)
	}
	if alphaConfig["url"] != "http://prowlarr:9696/16/api" {
		t.Fatalf("Alpha endpoint = %q", alphaConfig["url"])
	}
	if alphaConfig["categories"] != "2000,5000" {
		t.Fatalf("Alpha categories = %q", alphaConfig["categories"])
	}
	if bytes.Contains([]byte(alpha.ConfigJSON), []byte(testAlphaKey)) {
		t.Fatalf("API key leaked into config_json: %s", alpha.ConfigJSON)
	}
	if alpha.Tags != "hd" || alpha.Protocol != "torrent" || alpha.Priority != 20 {
		t.Fatalf("Alpha metadata = %+v", alpha)
	}

	if byName["Disabled Site"].Enabled {
		t.Fatalf("disabled upstream indexer was re-enabled")
	}

	// Secrets are sealed: the columns must not contain the plaintext and the
	// vault must open them back to the original values.
	for key, want := range map[string]string{
		"api_key":  testProwlarrKey,
		"username": testAlphaUser,
		"password": testAlphaPass,
	} {
		secret, err := st.GetSecret(ctx, alpha.ID, key)
		if err != nil {
			t.Fatalf("GetSecret %s: %v", key, err)
		}
		if bytes.Contains(secret.Nonce, []byte(want)) || bytes.Contains(secret.Ciphertext, []byte(want)) {
			t.Fatalf("secret %q is stored in plaintext", key)
		}
		plaintext, err := v.Open(secret.Nonce, secret.Ciphertext)
		if err != nil {
			t.Fatalf("open secret %q: %v", key, err)
		}
		if plaintext != want {
			t.Fatalf("secret %q = %q, want sealed original", key, plaintext)
		}
	}
}

func TestApplyIsIdempotentAndUpdatesExistingRows(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "import.db")
	v := testVault(t)

	plan := testPlan()
	targets, err := importers.Targets(plan)
	if err != nil {
		t.Fatalf("Targets: %v", err)
	}
	first, err := applyPlan(ctx, dbPath, v, targets)
	if err != nil {
		t.Fatalf("first applyPlan: %v", err)
	}

	st := openStore(t, dbPath)
	before, err := st.ListIndexers(ctx)
	if err != nil {
		t.Fatalf("ListIndexers: %v", err)
	}
	ids := make(map[string]int64, len(before))
	for _, row := range before {
		ids[row.Name] = row.ID
	}

	// The upstream changed the priority and disabled a site: a re-run must
	// update the same rows, not create new ones.
	for i := range targets {
		if targets[i].Name == "Alpha Tracker" {
			targets[i].Priority = 99
		}
		if targets[i].Name == "Disabled Site" {
			targets[i].Enabled = true
		}
	}
	second, err := applyPlan(ctx, dbPath, v, targets)
	if err != nil {
		t.Fatalf("second applyPlan: %v", err)
	}
	if second.Created != 0 || second.Updated != 3 || len(second.Applied) != 3 {
		t.Fatalf("second result = %+v", second)
	}

	after, err := openStore(t, dbPath).ListIndexers(ctx)
	if err != nil {
		t.Fatalf("ListIndexers: %v", err)
	}
	if len(after) != len(before) {
		t.Fatalf("re-run duplicated indexers: %d -> %d", len(before), len(after))
	}
	byName := make(map[string]store.Indexer, len(after))
	for _, row := range after {
		byName[row.Name] = row
		if ids[row.Name] == 0 || ids[row.Name] != row.ID {
			t.Fatalf("%s id changed from %d to %d", row.Name, ids[row.Name], row.ID)
		}
	}
	if byName["Alpha Tracker"].Priority != 99 {
		t.Fatalf("Alpha priority was not updated: %+v", byName["Alpha Tracker"])
	}
	if !byName["Disabled Site"].Enabled {
		t.Fatalf("Disabled Site enabled state was not updated")
	}

	secret, err := openStore(t, dbPath).GetSecret(ctx, ids["Alpha Tracker"], "api_key")
	if err != nil {
		t.Fatalf("GetSecret: %v", err)
	}
	plaintext, err := v.Open(secret.Nonce, secret.Ciphertext)
	if err != nil || plaintext != testProwlarrKey {
		t.Fatalf("sealed secret after re-run = %q, %v", plaintext, err)
	}

	if first.Created != 3 {
		t.Fatalf("first run created %d, want 3", first.Created)
	}
}

func TestApplyKeepsDisabledUpstreamDisabled(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "import.db")
	targets, err := importers.Targets(testPlan())
	if err != nil {
		t.Fatalf("Targets: %v", err)
	}
	if _, err := applyPlan(ctx, dbPath, testVault(t), targets); err != nil {
		t.Fatalf("applyPlan: %v", err)
	}

	row, err := openStore(t, dbPath).GetIndexerBySlug(ctx, "disabled-site")
	if err != nil {
		t.Fatalf("GetIndexerBySlug: %v", err)
	}
	if row.Enabled {
		t.Fatalf("disabled site is enabled")
	}
	if row.Priority != 30 {
		t.Fatalf("disabled site priority = %d, want 30", row.Priority)
	}
}

func TestResolveVaultFromEnvironmentAndLegacyFile(t *testing.T) {
	key := bytes.Repeat([]byte{9}, 32)
	t.Setenv("INDEXARR_MASTER_KEY", base64.StdEncoding.EncodeToString(key))
	t.Setenv("INDEXARR_MASTER_KEY_FILE", "")

	dbPath := filepath.Join(t.TempDir(), "import.db")
	v, source, err := resolveVault(dbPath, "")
	if err != nil {
		t.Fatalf("resolveVault: %v", err)
	}
	if source.Kind != "env" {
		t.Fatalf("key source = %q, want env", source.Kind)
	}
	sealed, err := v.SealString("value")
	if err != nil {
		t.Fatalf("SealString: %v", err)
	}
	opened, err := v.OpenString(sealed)
	if err != nil || opened != "value" {
		t.Fatalf("OpenString = %q, %v", opened, err)
	}

	// The legacy <db>.key file is the documented fallback.
	t.Setenv("INDEXARR_MASTER_KEY", "")
	if err := os.WriteFile(dbPath+".key", key, 0o600); err != nil {
		t.Fatalf("write legacy key: %v", err)
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	v, source, err = resolveVault(dbPath, "")
	if err != nil {
		t.Fatalf("resolveVault legacy: %v", err)
	}
	if source.Kind != "legacy-data-dir" {
		t.Fatalf("legacy key source = %q, want legacy-data-dir", source.Kind)
	}
	if _, err := v.SealString("value"); err != nil {
		t.Fatalf("legacy vault cannot seal: %v", err)
	}
}
