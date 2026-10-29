package indexersvc

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/buzzqw/indexarr/internal/indexer"
	"github.com/buzzqw/indexarr/internal/store"
	"github.com/buzzqw/indexarr/internal/vault"
)

func testDefinition(id string, withSearch bool, secretRequired bool) string {
	required := "true"
	if !secretRequired {
		required = "false"
	}
	body := `schema: 1
meta:
  id: ` + id + `
  name: Test ` + id + `
  type: private
  protocol: torrent
  urls:
    - https://example.invalid/
settings:
  - {name: url, type: text, required: true}
  - {name: apikey, type: password, required: ` + required + `}
`
	if withSearch {
		body += `search:
  request:
    get: /
  response:
    type: html
    rows: { selector: "tr" }
    fields:
      title: { selector: "a" }
`
	}
	return body
}

func newService(t *testing.T) (*Service, *store.Store, *vault.Vault, *indexer.Manager) {
	t.Helper()
	st, err := store.Open(t.TempDir() + "/service.db")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	v, err := vault.New(key)
	if err != nil {
		t.Fatalf("vault.New: %v", err)
	}
	manager, err := indexer.New(indexer.Config{Store: st, Vault: v})
	if err != nil {
		t.Fatalf("indexer.New: %v", err)
	}
	svc, err := New(Config{Store: st, Vault: v, Manager: manager})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return svc, st, v, manager
}

func storeDefinition(t *testing.T, st *store.Store, id, body string) {
	t.Helper()
	if err := st.UpsertDefinition(context.Background(), store.Definition{
		ID:            id,
		SchemaVersion: 1,
		State:         "bozza",
		Origin:        "local",
		Author:        "test",
		Body:          body,
		Hash:          "hash-" + id,
	}); err != nil {
		t.Fatalf("UpsertDefinition: %v", err)
	}
}

func TestCreateWithSecrets(t *testing.T) {
	svc, st, v, _ := newService(t)
	ctx := context.Background()
	storeDefinition(t, st, "test-create", testDefinition("test-create", true, true))

	created, err := svc.Create(ctx, Input{
		DefinitionID: "test-create",
		Name:         "My Indexer",
		Priority:     intPtr(10),
		Tags:         []string{"film", " hd "},
		Settings: map[string]string{
			"url":    "https://example.invalid/",
			"apikey": "super-secret-value",
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.Slug != "my-indexer" || created.Priority != 10 || created.Tags != "film,hd" {
		t.Errorf("created = %#v", created)
	}
	if strings.Contains(created.ConfigJSON, "super-secret-value") ||
		strings.Contains(created.ConfigJSON, "apikey") {
		t.Errorf("secret leaked into config_json: %s", created.ConfigJSON)
	}
	if !strings.Contains(created.ConfigJSON, "https://example.invalid/") {
		t.Errorf("an ordinary newznab URL left config_json: %s", created.ConfigJSON)
	}

	secret, err := st.GetSecret(ctx, created.ID, "apikey")
	if err != nil {
		t.Fatalf("GetSecret: %v", err)
	}
	plaintext, err := v.Open(secret.Nonce, secret.Ciphertext)
	if err != nil {
		t.Fatalf("vault.Open: %v", err)
	}
	if plaintext != "super-secret-value" {
		t.Errorf("secret = %q", plaintext)
	}

	keys, err := svc.SecretKeys(ctx, created.ID)
	if err != nil || len(keys) != 1 || keys[0] != "apikey" {
		t.Errorf("SecretKeys = %v, %v", keys, err)
	}
	defs, err := svc.Definitions(ctx)
	if err != nil || len(defs) != 1 || defs[0].ID != "test-create" {
		t.Errorf("Definitions = %#v, %v", defs, err)
	}
	indexers, err := svc.Indexers(ctx)
	if err != nil || len(indexers) != 1 || indexers[0].ID != created.ID {
		t.Errorf("Indexers = %#v, %v", indexers, err)
	}
}

func TestCreateRejectsMissingRequiredSetting(t *testing.T) {
	svc, st, _, _ := newService(t)
	ctx := context.Background()
	storeDefinition(t, st, "test-required", testDefinition("test-required", true, true))

	_, err := svc.Create(ctx, Input{
		DefinitionID: "test-required",
		Name:         "No key",
		Settings:     map[string]string{"url": "https://example.invalid/"},
	})
	if err == nil || !strings.Contains(err.Error(), "missing required setting: apikey") {
		t.Fatalf("Create error = %v", err)
	}
	if _, err := svc.Indexers(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestCreateSlugConflict(t *testing.T) {
	svc, st, _, _ := newService(t)
	ctx := context.Background()
	storeDefinition(t, st, "test-slug", testDefinition("test-slug", true, false))

	in := Input{
		DefinitionID: "test-slug",
		Name:         "Same name",
		Settings:     map[string]string{"url": "https://example.invalid/"},
	}
	if _, err := svc.Create(ctx, in); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	_, err := svc.Create(ctx, Input{
		DefinitionID: "test-slug",
		Name:         "Another name",
		Slug:         "same-name",
		Settings:     map[string]string{"url": "https://example.invalid/"},
	})
	if !errors.Is(err, ErrSlugTaken) {
		t.Fatalf("second Create error = %v, want ErrSlugTaken", err)
	}
}

func TestUpdateKeepsAndRemovesSecrets(t *testing.T) {
	svc, st, v, _ := newService(t)
	ctx := context.Background()
	storeDefinition(t, st, "test-update", testDefinition("test-update", true, false))

	created, err := svc.Create(ctx, Input{
		DefinitionID: "test-update",
		Name:         "Update me",
		Settings: map[string]string{
			"url":    "https://example.invalid/",
			"apikey": "keep-me",
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Omitting the secret from the update keeps the stored value.
	updated, err := svc.Update(ctx, created.ID, Input{
		DefinitionID: "test-update",
		Name:         "Updated",
		Settings: map[string]string{
			"url":        "https://example.invalid/",
			"categories": "5000,2000",
		},
	})
	if err != nil {
		t.Fatalf("Update keep: %v", err)
	}
	if updated.Name != "Updated" {
		t.Errorf("name = %q", updated.Name)
	}
	secret, err := st.GetSecret(ctx, created.ID, "apikey")
	if err != nil {
		t.Fatalf("GetSecret after keep: %v", err)
	}
	plaintext, err := v.Open(secret.Nonce, secret.Ciphertext)
	if err != nil || plaintext != "keep-me" {
		t.Fatalf("secret = %q, %v", plaintext, err)
	}

	// An explicit empty secret deletes it; empty public keys are removed too.
	if _, err := svc.Update(ctx, created.ID, Input{
		DefinitionID: "test-update",
		Name:         "Updated",
		Settings: map[string]string{
			"url":        "https://example.invalid/",
			"categories": "",
			"apikey":     "",
		},
	}); err != nil {
		t.Fatalf("Update remove: %v", err)
	}
	secrets, err := st.ListSecrets(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(secrets) != 0 {
		t.Errorf("secrets after removal = %#v", secrets)
	}
	current, err := st.GetIndexer(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(current.ConfigJSON, "categories") {
		t.Errorf("categories were not removed from config_json: %s", current.ConfigJSON)
	}
}

func TestDelete(t *testing.T) {
	svc, st, _, _ := newService(t)
	ctx := context.Background()
	storeDefinition(t, st, "test-delete", testDefinition("test-delete", true, false))

	created, err := svc.Create(ctx, Input{
		DefinitionID: "test-delete",
		Name:         "Delete me",
		Settings:     map[string]string{"url": "https://example.invalid/"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := svc.Delete(ctx, created.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := st.GetIndexer(ctx, created.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("GetIndexer after delete = %v", err)
	}
	if err := svc.Delete(ctx, created.ID); !errors.Is(err, ErrIndexerNotFound) {
		t.Fatalf("second Delete = %v, want ErrIndexerNotFound", err)
	}
}

func TestUpdateInvalidatesManager(t *testing.T) {
	svc, st, _, manager := newService(t)
	ctx := context.Background()

	body := `schema: 1
connector: newznab
meta:
  id: test-manager
  name: Test manager
  type: public
  protocol: usenet
  urls:
    - https://example.invalid/
settings:
  - {name: url, type: text, required: true}
`
	storeDefinition(t, st, "test-manager", body)
	created, err := svc.Create(ctx, Input{
		DefinitionID: "test-manager",
		Name:         "Manager",
		Settings:     map[string]string{"url": "https://example.invalid/api"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Populate the manager cache, then change the indexer through the service.
	if _, _, err := manager.Connector(ctx, created.ID); err != nil {
		t.Fatalf("Connector before update: %v", err)
	}
	if _, err := svc.Update(ctx, created.ID, Input{
		DefinitionID: "test-manager",
		Name:         "Manager renamed",
		Settings:     map[string]string{"url": "https://example.invalid/api"},
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	_, in, err := manager.Connector(ctx, created.ID)
	if err != nil {
		t.Fatalf("Connector after update: %v", err)
	}
	if in.Name != "Manager renamed" {
		t.Errorf("manager sees %q", in.Name)
	}
}

func TestNewRequiresStore(t *testing.T) {
	if _, err := New(Config{}); !errors.Is(err, ErrStoreRequired) {
		t.Fatalf("New error = %v, want ErrStoreRequired", err)
	}
}

func intPtr(value int) *int { return &value }

func intervalDefinition(id string) string {
	return `schema: 1
connector: rss
meta:
  id: ` + id + `
  name: ` + id + `
  type: public
  protocol: torrent
  urls: ["https://feed.example/"]
  interval: 15m
settings:
  - {name: url, type: text, required: true}
`
}

func TestCreatePrefillsSuggestedInterval(t *testing.T) {
	svc, st, _, _ := newService(t)
	ctx := context.Background()
	storeDefinition(t, st, "interval-feed", intervalDefinition("interval-feed"))

	created, err := svc.Create(ctx, Input{
		DefinitionID: "interval-feed",
		Name:         "Interval feed",
		Settings:     map[string]string{"url": "https://feed.example/rss"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.IntervalSeconds != 900 {
		t.Fatalf("interval = %d, want 900 from meta.interval", created.IntervalSeconds)
	}

	explicit := 0
	created, err = svc.Create(ctx, Input{
		DefinitionID:    "interval-feed",
		Name:            "Global interval feed",
		IntervalSeconds: &explicit,
		Settings:        map[string]string{"url": "https://feed.example/rss"},
	})
	if err != nil {
		t.Fatalf("Create explicit: %v", err)
	}
	if created.IntervalSeconds != 0 {
		t.Fatalf("explicit interval = %d, want 0", created.IntervalSeconds)
	}

	tooShort := 30
	if _, err := svc.Create(ctx, Input{
		DefinitionID:    "interval-feed",
		Name:            "Short interval feed",
		IntervalSeconds: &tooShort,
		Settings:        map[string]string{"url": "https://feed.example/rss"},
	}); err == nil {
		t.Fatal("Create accepted an interval below 60 seconds")
	}
}

func TestSensitiveURLIsStoredInTheVault(t *testing.T) {
	for name, raw := range map[string]string{
		"path":      "https://rss.example/SYNTHETICRSSKEY/feed.xml",
		"query":     "https://rss.example/feed.xml?passkey=SYNTHETICPASSKEY",
		"smartfeed": "https://mircrew-releases.org/smartfeed/feed?u=1&e=SYNTHETICSMARTFEEDTOKEN&f=51&t=2",
	} {
		t.Run(name, func(t *testing.T) {
			svc, st, v, _ := newService(t)
			ctx := context.Background()
			storeDefinition(t, st, "rss-secret", intervalDefinition("rss-secret"))
			created, err := svc.Create(ctx, Input{
				DefinitionID: "rss-secret",
				Name:         "RSS " + name,
				Settings:     map[string]string{"url": raw},
			})
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			if strings.Contains(created.ConfigJSON, raw) || strings.Contains(created.ConfigJSON, "SYNTHETIC") {
				t.Fatalf("sensitive URL leaked into config_json: %s", created.ConfigJSON)
			}
			secret, err := st.GetSecret(ctx, created.ID, "url")
			if err != nil {
				t.Fatalf("GetSecret(url): %v", err)
			}
			plain, err := v.Open(secret.Nonce, secret.Ciphertext)
			if err != nil {
				t.Fatalf("vault.Open: %v", err)
			}
			if plain != raw {
				t.Fatalf("stored URL = %q, want the original", plain)
			}
		})
	}
}

func TestSmartfeedURLSettingNameIsSealed(t *testing.T) {
	raw := "https://mircrew-releases.org/smartfeed/feed?u=1&e=SYNTHETICSMARTFEEDTOKEN&f=51&t=2"
	if !IsSecretSetting("smartfeed_url", raw) {
		t.Fatal("a smartfeed_url setting was not recognised as a secret")
	}
	if IsSecretSetting("smartfeed_url", "https://mircrew-releases.org/smartfeed/ui") {
		t.Error("the public Smartfeed page was marked secret")
	}
}

func TestPublicURLStaysInConfigAndCanBecomeSecret(t *testing.T) {
	svc, st, _, _ := newService(t)
	ctx := context.Background()
	storeDefinition(t, st, "rss-public", intervalDefinition("rss-public"))
	created, err := svc.Create(ctx, Input{
		DefinitionID: "rss-public",
		Name:         "Public RSS",
		Settings:     map[string]string{"url": "https://rss.example/feed.xml"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !strings.Contains(created.ConfigJSON, "https://rss.example/feed.xml") {
		t.Fatalf("public URL was not kept in config_json: %s", created.ConfigJSON)
	}

	updated, err := svc.Update(ctx, created.ID, Input{
		Settings: map[string]string{"url": "https://rss.example/feed.xml?passkey=SYNTHETICPASSKEY"},
	})
	if err != nil {
		t.Fatalf("Update to secret: %v", err)
	}
	if strings.Contains(updated.ConfigJSON, "SYNTHETICPASSKEY") || strings.Contains(updated.ConfigJSON, "passkey") {
		t.Fatalf("secret URL leaked into config_json: %s", updated.ConfigJSON)
	}
	if _, err := st.GetSecret(ctx, created.ID, "url"); err != nil {
		t.Fatalf("URL was not sealed: %v", err)
	}

	updated, err = svc.Update(ctx, created.ID, Input{
		Settings: map[string]string{"url": "https://rss.example/feed.xml"},
	})
	if err != nil {
		t.Fatalf("Update back to public: %v", err)
	}
	if !strings.Contains(updated.ConfigJSON, "https://rss.example/feed.xml") {
		t.Fatalf("public URL was not restored: %s", updated.ConfigJSON)
	}
	if _, err := st.GetSecret(ctx, created.ID, "url"); err == nil {
		t.Fatal("stale URL secret survived the transition to a public URL")
	}
}

func TestManagerEndpointShape(t *testing.T) {
	for raw, want := range map[string]bool{
		"http://jackett:9117/api/v2.0/indexers/all/results/torznab/api":  true,
		"https://jackett.local/api/v2.0/indexers/alpha/results/torznab/": true,
		"http://prowlarr:9696/15/api":                                    true,
		"http://prowlarr:9696/15/api/":                                   true,
		"http://prowlarr:9696/api/v1/indexer":                            true,
		"https://tracker.example/api":                                    false,
		"https://tracker.example/torznab/api":                            false,
		"https://tracker.example/api/v1/torrents":                        false,
		"": false,
	} {
		if got := managerEndpoint(raw); got != want {
			t.Errorf("managerEndpoint(%q) = %v, want %v", raw, got, want)
		}
	}
}

func TestCreateRejectsManagerEndpoint(t *testing.T) {
	svc, st, _, _ := newService(t)
	storeDefinition(t, st, "test-torznab", testDefinition("test-torznab", true, true))

	_, err := svc.Create(context.Background(), Input{
		DefinitionID: "test-torznab",
		Name:         "Jackett bridge",
		Settings: map[string]string{
			"url":    "http://127.0.0.1:9117/api/v2.0/indexers/alpha/results/torznab/",
			"apikey": "manager-key",
		},
	})
	if !errors.Is(err, ErrManagerEndpoint) {
		t.Fatalf("Create error = %v, want ErrManagerEndpoint", err)
	}
}

func TestCreateStoresManagementCriteria(t *testing.T) {
	svc, st, _, _ := newService(t)
	storeDefinition(t, st, "test-criteria", testDefinition("test-criteria", true, false))
	proxyID, err := st.CreateProxy(context.Background(), store.Proxy{
		Kind: "http", URL: "http://127.0.0.1:8080",
	})
	if err != nil {
		t.Fatalf("CreateProxy: %v", err)
	}
	minSeeders, seedTime, seasonPack, query, grab := 5, 60, 90, 10, 20
	ratio := 1.5
	created, err := svc.Create(context.Background(), Input{
		DefinitionID:              "test-criteria",
		Name:                      "Criteria",
		Priority:                  intPtr(20),
		ProxyID:                   &proxyID,
		MinSeeders:                &minSeeders,
		SeedRatio:                 &ratio,
		SeedTimeMinutes:           &seedTime,
		SeasonPackSeedTimeMinutes: &seasonPack,
		QueryLimit:                &query,
		GrabLimit:                 &grab,
		Settings:                  map[string]string{"url": "https://example.invalid/"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.MinSeeders != 5 || created.SeedRatio != 1.5 ||
		created.SeedTimeMinutes != 60 || created.SeasonPackSeedTimeMinutes != 90 ||
		created.QueryLimit != 10 || created.GrabLimit != 20 ||
		created.ProxyID == nil || *created.ProxyID != proxyID {
		t.Fatalf("stored criteria = %+v", created)
	}

	// Clearing the proxy and setting a zero seed value must persist.
	zero := 0
	var clearProxy *int64
	updated, err := svc.Update(context.Background(), created.ID, Input{
		ClearProxy: true,
		MinSeeders: &zero,
		QueryLimit: &zero,
		ProxyID:    clearProxy,
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.ProxyID != nil || updated.MinSeeders != 0 || updated.QueryLimit != 0 {
		t.Fatalf("cleared criteria = %+v", updated)
	}
}

func TestCreateAndUpdateAnEd2kIndexer(t *testing.T) {
	svc, st, _, _ := newService(t)
	ctx := context.Background()
	body := strings.Replace(testDefinition("test-ed2k", true, false), "protocol: torrent", "protocol: ed2k", 1)
	storeDefinition(t, st, "test-ed2k", body)

	created, err := svc.Create(ctx, Input{
		DefinitionID: "test-ed2k",
		Name:         "Forum eD2k",
		Settings:     map[string]string{"url": "https://example.invalid/"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.Protocol != "ed2k" {
		t.Errorf("protocol = %q, want ed2k", created.Protocol)
	}

	// The edit form posts the stored protocol back with the other fields.
	updated, err := svc.Update(ctx, created.ID, Input{
		DefinitionID: "test-ed2k",
		Name:         "Forum eD2k",
		Protocol:     "ed2k",
		Settings:     map[string]string{"url": "https://example.invalid/"},
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Protocol != "ed2k" {
		t.Errorf("protocol after update = %q, want ed2k", updated.Protocol)
	}

	if _, err := svc.Update(ctx, created.ID, Input{Protocol: "ftp"}); err == nil {
		t.Error("Update accepted protocol ftp")
	}
}
