package importers

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
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
				Name: "TorrentLeech", Implementation: "Cardigann", Protocol: ProtocolTorrent,
				DefinitionHint: "torrentleech", RemoteID: "15",
				BaseURL: "https://www.torrentleech.org/", SiteURL: "https://www.torrentleech.org/",
				Username: "tl-user-SECRET", Password: "tl-pass-SECRET",
				Enabled: true, Priority: 25, Categories: []int{2000, 5000},
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
	if result.Created != 3 || result.Updated != 0 || result.Skipped != 0 || result.Errors != 0 ||
		len(result.Pending) != 1 {
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
	if alphaConfig["url"] != "https://alpha.example.invalid/" {
		t.Fatalf("Alpha endpoint = %q", alphaConfig["url"])
	}
	if bytes.Contains([]byte(alpha.ConfigJSON), []byte("alpha-key-SECRET")) {
		t.Fatalf("API key leaked into config_json: %s", alpha.ConfigJSON)
	}
	if _, ok := byName["Disabled Site"]; ok {
		t.Fatalf("a pending source must not create an indexer")
	}
	leech := byName["TorrentLeech"]
	if leech.DefinitionID != "torrentleech" || !leech.Enabled {
		t.Fatalf("TorrentLeech row = %+v", leech)
	}

	for _, check := range []struct {
		indexerID int64
		key       string
		want      string
	}{
		{alpha.ID, "api_key", "alpha-key-SECRET"},
		{leech.ID, "username", "tl-user-SECRET"},
		{leech.ID, "password", "tl-pass-SECRET"},
	} {
		secret, err := st.GetSecret(ctx, check.indexerID, check.key)
		if err != nil {
			t.Fatalf("GetSecret %s: %v", check.key, err)
		}
		if bytes.Contains(secret.Nonce, []byte(check.want)) || bytes.Contains(secret.Ciphertext, []byte(check.want)) {
			t.Fatalf("secret %q is stored in plaintext", check.key)
		}
		plaintext, err := v.Open(secret.Nonce, secret.Ciphertext)
		if err != nil || plaintext != check.want {
			t.Fatalf("secret %q = %q, %v", check.key, plaintext, err)
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
	if result.Created != 0 || result.Updated != 3 || result.Skipped != 0 || len(result.Pending) != 1 {
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
	if result.Created != 3 || result.Skipped != 4 || len(result.Pending) != 1 {
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

func TestApplyStoresDerivedFamilyDefinition(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "family.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()

	target := Target("jackett", "http://jackett:9117", "jackett-key", Indexer{
		Name:     "13City",
		Protocol: ProtocolTorrent,
		RemoteID: "13city",
		SiteURL:  "https://13city.org/",
		Username: "user-SECRET",
		Password: "pass-SECRET",
		Enabled:  true,
		Priority: 12,
	}, builtinEntries(t))
	if target.Status != TargetFamily {
		t.Fatalf("target = %+v", target)
	}

	result, err := ApplyTargets(ctx, st, applyTestVault(t), []NativeTarget{target})
	if err != nil {
		t.Fatalf("ApplyTargets: %v", err)
	}
	if result.Created != 1 || len(result.Pending) != 0 {
		t.Fatalf("result = %+v", result)
	}

	stored, err := st.GetDefinition(ctx, "jk-13city")
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	if stored.Family != "nexusphp" || stored.State != "bozza" || stored.Origin != "catalog" {
		t.Fatalf("stored definition = %+v", stored)
	}
	if !strings.Contains(stored.Body, "connector: nexusphp") {
		t.Fatalf("stored body is not the family definition: %s", stored.Body)
	}

	row, err := st.GetIndexerBySlug(ctx, "13city")
	if err != nil {
		t.Fatalf("GetIndexerBySlug: %v", err)
	}
	if row.DefinitionID != "jk-13city" || !row.Enabled {
		t.Fatalf("family indexer = %+v", row)
	}

	// A second run updates the same row and does not duplicate the definition.
	again, err := ApplyTargets(ctx, st, applyTestVault(t), []NativeTarget{target})
	if err != nil {
		t.Fatalf("second ApplyTargets: %v", err)
	}
	if again.Created != 0 || again.Updated != 1 {
		t.Fatalf("second result = %+v", again)
	}
}

func TestApplyDoesNotOverwriteLocalFamilyDefinition(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "local.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()

	const localBody = "schema: 1\nconnector: nexusphp\nmeta:\n  id: jk-13city\n  name: Local edit\n  language: en-US\n  type: private\n  protocol: torrent\n  author: local\n  urls:\n    - https://13city.org/\nsettings:\n  - { name: url, type: text, label: URL, required: true }\n  - { name: username, type: text, label: User, required: true }\n  - { name: password, type: password, label: Pass, required: true }\ncategories:\n  - { site: \"1\", standard: 2000, desc: Movies }\ncapabilities:\n  search: [q]\nhealth:\n  query: test\n  min_rows: 0\n"
	if err := st.UpsertDefinition(ctx, store.Definition{
		ID: "jk-13city", SchemaVersion: 1, Family: "nexusphp", State: "bozza",
		Origin: "local", Author: "local", Body: localBody, Hash: "local-hash",
	}); err != nil {
		t.Fatalf("UpsertDefinition: %v", err)
	}

	target := Target("jackett", "http://jackett:9117", "jackett-key", Indexer{
		Name: "13City", Protocol: ProtocolTorrent, RemoteID: "13city",
		SiteURL: "https://13city.org/", Username: "u", Password: "p", Enabled: true,
	}, builtinEntries(t))
	if _, err := ApplyTargets(ctx, st, applyTestVault(t), []NativeTarget{target}); err != nil {
		t.Fatalf("ApplyTargets: %v", err)
	}

	stored, err := st.GetDefinition(ctx, "jk-13city")
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	if stored.Origin != "local" || stored.Body != localBody {
		t.Fatalf("the local definition was overwritten: %+v", stored)
	}
}
