package importers

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/buzzqw/indexarr/internal/store"
	"github.com/buzzqw/indexarr/internal/vault"
)

func applyTestPlan() Plan {
	return Plan{
		Source:        "prowlarr",
		ManagerURL:    "http://prowlarr:9696",
		ManagerAPIKey: "manager-key-SECRET",
		Indexers: []Indexer{
			{
				Name: "ExtTo", Implementation: "Cardigann", Protocol: ProtocolTorrent,
				DefinitionHint: "extto", RemoteID: "11",
				BaseURL: "https://extto.org/", SiteURL: "https://extto.org/",
				Enabled: true, Priority: 10, Categories: []int{5000, 2000}, Tags: []string{"tv"},
			},
			{
				Name: "Alpha Tracker", Implementation: "Torznab", Protocol: ProtocolTorrent,
				DefinitionHint: "torznab-alpha", RemoteID: "16",
				BaseURL: "https://alpha.example.invalid/", SiteURL: "https://alpha.example.invalid/",
				APIKey: "alpha-key-SECRET", Username: "alpha-user-SECRET", Password: "alpha-pass-SECRET",
				Enabled: true, Priority: 20, Categories: []int{2000, 5000}, Tags: []string{"hd"},
			},
			{
				Name: "Disabled Site", Implementation: "Cardigann", Protocol: ProtocolTorrent,
				DefinitionHint: "disabled-site", RemoteID: "17",
				BaseURL: "https://disabled.example.invalid/", SiteURL: "https://disabled.example.invalid/",
				Enabled: false, Priority: 30,
			},
		},
	}
}

func applyTestVault(t *testing.T) *vault.Vault {
	t.Helper()
	v, err := vault.New(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatalf("vault.New: %v", err)
	}
	return v
}

func TestApplyTargetsCreatesNativeIndexersAndSealsSecrets(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "apply.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()
	v := applyTestVault(t)

	targets, err := Targets(applyTestPlan())
	if err != nil {
		t.Fatalf("Targets: %v", err)
	}
	result, err := ApplyTargets(ctx, st, v, targets)
	if err != nil {
		t.Fatalf("ApplyTargets: %v", err)
	}
	if result.Created != 3 || result.Updated != 0 || result.Skipped != 0 || result.Errors != 0 {
		t.Fatalf("result = %+v", result)
	}

	rows, err := st.ListIndexers(ctx)
	if err != nil {
		t.Fatalf("ListIndexers: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("got %d indexers, want 3: %+v", len(rows), rows)
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
	if bytes.Contains([]byte(alpha.ConfigJSON), []byte("alpha-key-SECRET")) {
		t.Fatalf("API key leaked into config_json: %s", alpha.ConfigJSON)
	}
	if byName["Disabled Site"].Enabled {
		t.Fatalf("disabled upstream indexer was re-enabled")
	}

	for key, want := range map[string]string{
		"api_key":  "manager-key-SECRET",
		"username": "alpha-user-SECRET",
		"password": "alpha-pass-SECRET",
	} {
		secret, err := st.GetSecret(ctx, alpha.ID, key)
		if err != nil {
			t.Fatalf("GetSecret %s: %v", key, err)
		}
		if bytes.Contains(secret.Nonce, []byte(want)) || bytes.Contains(secret.Ciphertext, []byte(want)) {
			t.Fatalf("secret %q is stored in plaintext", key)
		}
		plaintext, err := v.Open(secret.Nonce, secret.Ciphertext)
		if err != nil || plaintext != want {
			t.Fatalf("secret %q = %q, %v", key, plaintext, err)
		}
	}
}

func TestApplyTargetsIsIdempotent(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "apply.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()
	v := applyTestVault(t)

	targets, err := Targets(applyTestPlan())
	if err != nil {
		t.Fatalf("Targets: %v", err)
	}
	if _, err := ApplyTargets(ctx, st, v, targets); err != nil {
		t.Fatalf("first ApplyTargets: %v", err)
	}
	before, err := st.ListIndexers(ctx)
	if err != nil {
		t.Fatalf("ListIndexers: %v", err)
	}

	result, err := ApplyTargets(ctx, st, v, targets)
	if err != nil {
		t.Fatalf("second ApplyTargets: %v", err)
	}
	if result.Created != 0 || result.Updated != 3 || result.Skipped != 0 {
		t.Fatalf("second result = %+v", result)
	}
	after, err := st.ListIndexers(ctx)
	if err != nil {
		t.Fatalf("ListIndexers: %v", err)
	}
	if len(after) != len(before) {
		t.Fatalf("re-run duplicated indexers: %d -> %d", len(before), len(after))
	}
	ids := make(map[string]int64, len(before))
	for _, row := range before {
		ids[row.Name] = row.ID
	}
	for _, row := range after {
		if ids[row.Name] != row.ID {
			t.Fatalf("%s id changed from %d to %d", row.Name, ids[row.Name], row.ID)
		}
	}
}

func TestApplyTargetsSkipsDuplicateTargets(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "apply.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()

	targets, err := Targets(applyTestPlan())
	if err != nil {
		t.Fatalf("Targets: %v", err)
	}
	// The same site twice in one selection is imported once.
	doubled := append(append([]NativeTarget(nil), targets...), targets...)
	result, err := ApplyTargets(ctx, st, applyTestVault(t), doubled)
	if err != nil {
		t.Fatalf("ApplyTargets: %v", err)
	}
	if result.Created != 3 || result.Skipped != 3 {
		t.Fatalf("result = %+v", result)
	}
	rows, err := st.ListIndexers(ctx)
	if err != nil {
		t.Fatalf("ListIndexers: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("got %d indexers, want 3", len(rows))
	}
}
