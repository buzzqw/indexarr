package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testDefinition(id string) Definition {
	return Definition{
		ID:            id,
		SchemaVersion: 1,
		Family:        "unit3d",
		State:         "verificata",
		Origin:        "local",
		Author:        "test",
		Body:          "schema: 1\nconnector: torznab\n",
		Hash:          strings.Repeat("a", 64),
	}
}

func mustCreateDefinition(t *testing.T, st *Store, id string) {
	t.Helper()
	if err := st.UpsertDefinition(context.Background(), testDefinition(id)); err != nil {
		t.Fatalf("UpsertDefinition(%s): %v", id, err)
	}
}

func mustCreateIndexer(t *testing.T, st *Store, definitionID, slug, name string) int64 {
	t.Helper()
	id, err := st.CreateIndexer(context.Background(), Indexer{
		DefinitionID: definitionID,
		Protocol:     "torrent",
		Slug:         slug,
		Name:         name,
		Enabled:      true,
		Priority:     25,
		ConfigJSON:   `{"kind":"torznab"}`,
	})
	if err != nil {
		t.Fatalf("CreateIndexer(%s): %v", slug, err)
	}
	return id
}

func TestIndexarr2Schema(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	version, err := st.schemaVersion(ctx)
	if err != nil {
		t.Fatalf("schemaVersion: %v", err)
	}
	if version != indexarr2UsenetPayloadVersion {
		t.Fatalf("schema version = %d, want %d", version, indexarr2UsenetPayloadVersion)
	}
	var name string
	if err := st.DB().QueryRowContext(ctx,
		`SELECT name FROM schema_migrations WHERE version = ?`,
		indexarr2FoundationVersion).Scan(&name); err != nil {
		t.Fatalf("schema_migrations row: %v", err)
	}
	if name != "indexarr2 foundation" {
		t.Fatalf("migration name = %q", name)
	}
	if err := st.DB().QueryRowContext(ctx,
		`SELECT name FROM schema_migrations WHERE version = ?`,
		indexarr2UsenetPayloadVersion).Scan(&name); err != nil {
		t.Fatalf("usenet payload migration row: %v", err)
	}
	if name != "usenet payload" {
		t.Fatalf("usenet payload migration name = %q", name)
	}
	for _, column := range []string{"protocol", "payload", "payload_type"} {
		exists, err := st.columnExists("releases", column)
		if err != nil {
			t.Fatalf("columnExists(%s): %v", column, err)
		}
		if !exists {
			t.Errorf("releases.%s is missing", column)
		}
	}
	for _, table := range []string{
		"definitions", "indexers", "indexer_secrets", "indexer_state",
		"release_sources", "indexer_history", "applications", "app_indexers",
		"proxies",
	} {
		var count int
		if err := st.DB().QueryRowContext(ctx,
			`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`,
			table).Scan(&count); err != nil {
			t.Fatalf("check table %s: %v", table, err)
		}
		if count != 1 {
			t.Errorf("table %s is missing", table)
		}
	}
}

func TestMigrationIdempotent(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "indexarr-migrate.db")

	first, err := Open(path)
	if err != nil {
		t.Fatalf("Open first: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close first: %v", err)
	}
	second, err := Open(path)
	if err != nil {
		t.Fatalf("Open second: %v", err)
	}
	defer second.Close()

	var rows int
	if err := second.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM schema_migrations WHERE version = ?`,
		indexarr2FoundationVersion).Scan(&rows); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if rows != 1 {
		t.Fatalf("migration rows = %d, want 1", rows)
	}
	version, err := second.schemaVersion(ctx)
	if err != nil {
		t.Fatalf("schemaVersion: %v", err)
	}
	if version != indexarr2UsenetPayloadVersion {
		t.Fatalf("schema version = %d", version)
	}
	if err := second.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM schema_migrations WHERE version = ?`,
		indexarr2UsenetPayloadVersion).Scan(&rows); err != nil {
		t.Fatalf("count payload migrations: %v", err)
	}
	if rows != 1 {
		t.Fatalf("payload migration rows = %d, want 1", rows)
	}
}

func TestFailedMigrationRollsBack(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	wantErr := errors.New("boom")
	err := st.applyMigration(ctx, 3, "broken", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `CREATE TABLE migration_should_rollback (id INTEGER)`); err != nil {
			return err
		}
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("applyMigration error = %v", err)
	}
	version, err := st.schemaVersion(ctx)
	if err != nil {
		t.Fatalf("schemaVersion: %v", err)
	}
	if version != indexarr2UsenetPayloadVersion {
		t.Fatalf("schema version after failure = %d", version)
	}
	var tables int
	if err := st.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'migration_should_rollback'`).
		Scan(&tables); err != nil {
		t.Fatalf("check rolled back table: %v", err)
	}
	if tables != 0 {
		t.Fatal("failed migration left its table behind")
	}
	var rows int
	if err := st.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM schema_migrations WHERE version = 3`).Scan(&rows); err != nil {
		t.Fatalf("check migration row: %v", err)
	}
	if rows != 0 {
		t.Fatal("failed migration was recorded")
	}
}

func TestBackfillLegacySources(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	first := testSource("Alpha Tracker")
	first.Kind = "torznab"
	first.URL = "http://alpha.test/api"
	first.APIKey = "super-secret-api-key"
	first.Username = "user"
	first.Password = "super-secret-password"
	first.Categories = "5000,2000"
	first.DefaultCategory = "5040"
	first.Query = "alpha"
	first.IntervalSeconds = 900
	firstID, err := st.CreateSource(ctx, first)
	if err != nil {
		t.Fatalf("CreateSource alpha: %v", err)
	}
	second := testSource("Beta RSS")
	second.Kind = "rss"
	second.URL = "http://beta.test/feed"
	second.APIKey = ""
	second.Enabled = false
	secondID, err := st.CreateSource(ctx, second)
	if err != nil {
		t.Fatalf("CreateSource beta: %v", err)
	}

	created, err := st.BackfillLegacySources(ctx)
	if err != nil {
		t.Fatalf("BackfillLegacySources: %v", err)
	}
	if created != 2 {
		t.Fatalf("created = %d, want 2", created)
	}

	definitions, err := st.ListDefinitions(ctx)
	if err != nil {
		t.Fatalf("ListDefinitions: %v", err)
	}
	if len(definitions) != 2 {
		t.Fatalf("definitions = %d, want 2", len(definitions))
	}
	indexers, err := st.ListIndexers(ctx)
	if err != nil {
		t.Fatalf("ListIndexers: %v", err)
	}
	if len(indexers) != 2 {
		t.Fatalf("indexers = %d, want 2", len(indexers))
	}

	byLegacy := map[int64]Indexer{}
	for _, indexer := range indexers {
		byLegacy[indexer.LegacySourceID] = indexer
	}
	for _, id := range []int64{firstID, secondID} {
		indexer, ok := byLegacy[id]
		if !ok {
			t.Fatalf("no indexer for legacy source %d", id)
		}
		if indexer.DefinitionID != legacyDefinitionID(id) {
			t.Errorf("definition id = %q", indexer.DefinitionID)
		}
		if indexer.Protocol != "torrent" || indexer.Priority != 25 {
			t.Errorf("indexer defaults = %#v", indexer)
		}
		if indexer.Slug == "" {
			t.Errorf("indexer %d has empty slug", id)
		}
		var config map[string]any
		if err := json.Unmarshal([]byte(indexer.ConfigJSON), &config); err != nil {
			t.Fatalf("config_json for %d is not JSON: %v", id, err)
		}
		configJSON := strings.ToLower(indexer.ConfigJSON)
		for _, forbidden := range []string{"api_key", "password", "username", "super-secret"} {
			if strings.Contains(configJSON, forbidden) {
				t.Errorf("config_json leaks %q: %s", forbidden, indexer.ConfigJSON)
			}
		}
		if _, ok := config["url"]; !ok {
			t.Errorf("config_json has no url: %s", indexer.ConfigJSON)
		}
		secrets, err := st.ListSecrets(ctx, indexer.ID)
		if err != nil {
			t.Fatalf("ListSecrets: %v", err)
		}
		if len(secrets) != 0 {
			t.Errorf("legacy secrets were copied: %#v", secrets)
		}
		def, err := st.GetDefinition(ctx, indexer.DefinitionID)
		if err != nil {
			t.Fatalf("GetDefinition: %v", err)
		}
		if def.State != "verificata" || def.Origin != "local" || def.Author != "migration" {
			t.Errorf("definition = %#v", def)
		}
		if len(def.Hash) != 64 {
			t.Errorf("hash = %q", def.Hash)
		}
		if !strings.Contains(def.Body, "schema: 1") || !strings.Contains(def.Body, "connector:") {
			t.Errorf("definition body = %q", def.Body)
		}
	}
	if got := byLegacy[secondID].Enabled; got {
		t.Errorf("disabled source produced an enabled indexer")
	}

	again, err := st.BackfillLegacySources(ctx)
	if err != nil {
		t.Fatalf("second BackfillLegacySources: %v", err)
	}
	if again != 0 {
		t.Fatalf("second run created %d rows, want 0", again)
	}

	// Deleting a bridged indexer is permanent: the definition stays as the
	// migration marker, so the next start does not recreate it.
	secondBridged := byLegacy[secondID]
	if err := st.DeleteIndexer(ctx, secondBridged.ID); err != nil {
		t.Fatalf("DeleteIndexer: %v", err)
	}
	afterDelete, err := st.BackfillLegacySources(ctx)
	if err != nil {
		t.Fatalf("BackfillLegacySources after delete: %v", err)
	}
	if afterDelete != 0 {
		t.Fatalf("backfill recreated %d deleted indexers", afterDelete)
	}
	if _, err := st.GetIndexer(ctx, secondBridged.ID); err == nil {
		t.Fatal("deleted bridged indexer was recreated")
	}

	// Removing the legacy source must not remove the bridged indexer.
	if err := st.DeleteSource(ctx, firstID); err != nil {
		t.Fatalf("DeleteSource: %v", err)
	}
	bridged := byLegacy[firstID]
	reloaded, err := st.GetIndexer(ctx, bridged.ID)
	if err != nil {
		t.Fatalf("indexer disappeared with its source: %v", err)
	}
	if reloaded.LegacySourceID != firstID {
		t.Errorf("legacy_source_id = %d, want %d", reloaded.LegacySourceID, firstID)
	}
}

func TestBackfillSlugCollision(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	for _, name := range []string{"Foo Bar", "Foo-Bar"} {
		if _, err := st.CreateSource(ctx, testSource(name)); err != nil {
			t.Fatalf("CreateSource(%q): %v", name, err)
		}
	}
	created, err := st.BackfillLegacySources(ctx)
	if err != nil {
		t.Fatalf("BackfillLegacySources: %v", err)
	}
	if created != 2 {
		t.Fatalf("created = %d, want 2", created)
	}
	indexers, err := st.ListIndexers(ctx)
	if err != nil {
		t.Fatalf("ListIndexers: %v", err)
	}
	seen := map[string]bool{}
	for _, indexer := range indexers {
		if seen[indexer.Slug] {
			t.Fatalf("duplicate slug %q", indexer.Slug)
		}
		seen[indexer.Slug] = true
	}
}

func TestDefinitionAndIndexerCRUD(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	def := testDefinition("site-one")
	if err := st.UpsertDefinition(ctx, def); err != nil {
		t.Fatalf("UpsertDefinition: %v", err)
	}
	loaded, err := st.GetDefinition(ctx, def.ID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	if loaded.Family != def.Family || loaded.Body != def.Body {
		t.Fatalf("definition = %#v", loaded)
	}
	def.Body = "schema: 1\nconnector: newznab\n"
	def.State = "stabile"
	if err := st.UpsertDefinition(ctx, def); err != nil {
		t.Fatalf("UpsertDefinition update: %v", err)
	}
	definitions, err := st.ListDefinitions(ctx)
	if err != nil {
		t.Fatalf("ListDefinitions: %v", err)
	}
	if len(definitions) != 1 || definitions[0].State != "stabile" {
		t.Fatalf("definitions = %#v", definitions)
	}

	proxyID, err := st.CreateProxy(ctx, Proxy{Kind: "socks5", URL: "socks5://proxy.test:1080"})
	if err != nil {
		t.Fatalf("CreateProxy: %v", err)
	}
	id, err := st.CreateIndexer(ctx, Indexer{
		DefinitionID: def.ID,
		Protocol:     "torrent",
		Slug:         "site-one",
		Name:         "Site One",
		Enabled:      true,
		Priority:     40,
		ConfigJSON:   `{"kind":"torznab","url":"http://site.test"}`,
		Tags:         "tv,hd",
		ProxyID:      &proxyID,
		QueryLimit:   100,
		GrabLimit:    20,
	})
	if err != nil {
		t.Fatalf("CreateIndexer: %v", err)
	}

	indexer, err := st.GetIndexer(ctx, id)
	if err != nil {
		t.Fatalf("GetIndexer: %v", err)
	}
	if indexer.Slug != "site-one" || indexer.Tags != "tv,hd" || !indexer.Enabled ||
		indexer.Priority != 40 || indexer.QueryLimit != 100 || indexer.GrabLimit != 20 {
		t.Fatalf("indexer = %#v", indexer)
	}
	if indexer.ProxyID == nil || *indexer.ProxyID != proxyID {
		t.Fatalf("proxy = %#v", indexer.ProxyID)
	}

	bySlug, err := st.GetIndexerBySlug(ctx, "site-one")
	if err != nil {
		t.Fatalf("GetIndexerBySlug: %v", err)
	}
	if bySlug.ID != id {
		t.Fatalf("slug lookup returned %d", bySlug.ID)
	}
	list, err := st.ListIndexers(ctx)
	if err != nil {
		t.Fatalf("ListIndexers: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("indexers = %d", len(list))
	}

	indexer.Name = "Site One Updated"
	indexer.Tags = "movies"
	indexer.Priority = 10
	indexer.QueryLimit = 50
	indexer.ProxyID = nil
	if err := st.UpdateIndexer(ctx, indexer); err != nil {
		t.Fatalf("UpdateIndexer: %v", err)
	}
	updated, err := st.GetIndexer(ctx, id)
	if err != nil {
		t.Fatalf("GetIndexer after update: %v", err)
	}
	if updated.Name != "Site One Updated" || updated.Tags != "movies" ||
		updated.Priority != 10 || updated.QueryLimit != 50 || updated.ProxyID != nil {
		t.Fatalf("updated indexer = %#v", updated)
	}

	if err := st.DeleteIndexer(ctx, id); err != nil {
		t.Fatalf("DeleteIndexer: %v", err)
	}
	if _, err := st.GetIndexer(ctx, id); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("GetIndexer after delete = %v", err)
	}
	if _, err := st.GetIndexerBySlug(ctx, "site-one"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("GetIndexerBySlug after delete = %v", err)
	}
	if err := st.UpdateIndexer(ctx, indexer); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("UpdateIndexer missing = %v", err)
	}
}

func TestSecretsRoundTripAndCascade(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	mustCreateDefinition(t, st, "secret-def")
	indexerID := mustCreateIndexer(t, st, "secret-def", "secret-site", "Secret Site")

	nonce := []byte{0x00, 0x01, 0xff, 0x80}
	ciphertext := []byte{0xde, 0xad, 0x00, 0xbe, 0xef}
	if err := st.PutSecret(ctx, Secret{
		IndexerID: indexerID, Key: "api_key", Nonce: nonce, Ciphertext: ciphertext,
	}); err != nil {
		t.Fatalf("PutSecret: %v", err)
	}
	secret, err := st.GetSecret(ctx, indexerID, "api_key")
	if err != nil {
		t.Fatalf("GetSecret: %v", err)
	}
	if !bytes.Equal(secret.Nonce, nonce) || !bytes.Equal(secret.Ciphertext, ciphertext) {
		t.Fatalf("secret payload = %#v", secret)
	}

	updatedCipher := []byte{0x11, 0x22}
	if err := st.PutSecret(ctx, Secret{
		IndexerID: indexerID, Key: "api_key", Nonce: nonce, Ciphertext: updatedCipher,
	}); err != nil {
		t.Fatalf("PutSecret update: %v", err)
	}
	secrets, err := st.ListSecrets(ctx, indexerID)
	if err != nil {
		t.Fatalf("ListSecrets: %v", err)
	}
	if len(secrets) != 1 || !bytes.Equal(secrets[0].Ciphertext, updatedCipher) {
		t.Fatalf("secrets = %#v", secrets)
	}

	if err := st.DeleteIndexer(ctx, indexerID); err != nil {
		t.Fatalf("DeleteIndexer: %v", err)
	}
	if _, err := st.GetSecret(ctx, indexerID, "api_key"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("secret survived cascade: %v", err)
	}
}

func TestIndexerStateUpsert(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	mustCreateDefinition(t, st, "state-def")
	indexerID := mustCreateIndexer(t, st, "state-def", "state-site", "State Site")

	login := time.Unix(1_700_000_000, 0)
	disabled := time.Unix(1_700_003_600, 0)
	state := IndexerState{
		IndexerID:     indexerID,
		CookiesEnc:    []byte{0x01, 0x02},
		LastLoginAt:   login,
		Circuit:       "open",
		DisabledUntil: disabled,
		LastError:     "blocked",
	}
	if err := st.UpsertIndexerState(ctx, state); err != nil {
		t.Fatalf("UpsertIndexerState: %v", err)
	}
	loaded, err := st.GetIndexerState(ctx, indexerID)
	if err != nil {
		t.Fatalf("GetIndexerState: %v", err)
	}
	if loaded.Circuit != "open" || loaded.LastError != "blocked" ||
		!bytes.Equal(loaded.CookiesEnc, state.CookiesEnc) {
		t.Fatalf("state = %#v", loaded)
	}
	if loaded.LastLoginAt.Unix() != login.Unix() || loaded.DisabledUntil.Unix() != disabled.Unix() {
		t.Fatalf("state times = %#v", loaded)
	}

	state.Circuit = "half-open"
	state.LastError = ""
	state.DisabledUntil = time.Time{}
	if err := st.UpsertIndexerState(ctx, state); err != nil {
		t.Fatalf("UpsertIndexerState update: %v", err)
	}
	loaded, err = st.GetIndexerState(ctx, indexerID)
	if err != nil {
		t.Fatalf("GetIndexerState: %v", err)
	}
	if loaded.Circuit != "half-open" || loaded.LastError != "" || !loaded.DisabledUntil.IsZero() {
		t.Fatalf("updated state = %#v", loaded)
	}
	if _, err := st.GetIndexerState(ctx, indexerID+100); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing state = %v", err)
	}
}

func TestReleaseSources(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	mustCreateDefinition(t, st, "rel-def")
	first := mustCreateIndexer(t, st, "rel-def", "rel-one", "Rel One")
	second := mustCreateIndexer(t, st, "rel-def", "rel-two", "Rel Two")

	now := time.Now()
	release := Release{
		DedupKey: "release-source-1", Title: "Release Source", SearchTitle: "release source",
		FirstSeenAt: now, LastSeenAt: now,
	}
	if _, err := st.UpsertRelease(ctx, &release); err != nil {
		t.Fatalf("UpsertRelease: %v", err)
	}

	t1 := time.Unix(1_700_000_000, 0)
	t2 := t1.Add(10 * time.Minute)
	t3 := t1.Add(time.Hour)
	if err := st.RecordReleaseSource(ctx, ReleaseSource{
		ReleaseID: release.ID, IndexerID: first,
		FirstSeenAt: t1, LastSeenAt: t1, Seeders: 1, Leechers: 2, DownloadRef: "ref-1",
	}); err != nil {
		t.Fatalf("RecordReleaseSource first: %v", err)
	}
	if err := st.RecordReleaseSource(ctx, ReleaseSource{
		ReleaseID: release.ID, IndexerID: second,
		FirstSeenAt: t2, LastSeenAt: t2, Seeders: 3, Leechers: 4,
	}); err != nil {
		t.Fatalf("RecordReleaseSource second: %v", err)
	}

	sources, err := st.ListReleaseSources(ctx, release.ID)
	if err != nil {
		t.Fatalf("ListReleaseSources: %v", err)
	}
	if len(sources) != 2 {
		t.Fatalf("release sources = %d", len(sources))
	}
	if sources[0].IndexerID != first || sources[1].IndexerID != second {
		t.Fatalf("ordering = %#v", sources)
	}

	if err := st.RecordReleaseSource(ctx, ReleaseSource{
		ReleaseID: release.ID, IndexerID: first,
		FirstSeenAt: t3, LastSeenAt: t3, Seeders: 9, Leechers: 8, DownloadRef: "ref-2",
	}); err != nil {
		t.Fatalf("RecordReleaseSource update: %v", err)
	}
	sources, err = st.ListReleaseSources(ctx, release.ID)
	if err != nil {
		t.Fatalf("ListReleaseSources: %v", err)
	}
	if len(sources) != 2 {
		t.Fatalf("release sources after upsert = %d", len(sources))
	}
	updated := sources[0]
	if updated.IndexerID != first {
		t.Fatalf("first source = %#v", updated)
	}
	if updated.FirstSeenAt.Unix() != t1.Unix() {
		t.Errorf("first_seen = %v, want %v", updated.FirstSeenAt, t1)
	}
	if updated.LastSeenAt.Unix() != t3.Unix() {
		t.Errorf("last_seen = %v, want %v", updated.LastSeenAt, t3)
	}
	if updated.Seeders != 9 || updated.Leechers != 8 || updated.DownloadRef != "ref-2" {
		t.Errorf("updated source = %#v", updated)
	}
}

func TestIndexerHistory(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	mustCreateDefinition(t, st, "hist-def")
	indexerID := mustCreateIndexer(t, st, "hist-def", "hist-site", "Hist Site")

	start := time.Unix(1_700_000_000, 0)
	for i := 0; i < 3; i++ {
		if _, err := st.RecordIndexerHistory(ctx, IndexerHistory{
			IndexerID: indexerID,
			Kind:      "search",
			Query:     "query",
			Status:    "ok",
			LatencyMS: int64(i),
			Bytes:     100,
			App:       "sonarr",
			StartedAt: start.Add(time.Duration(i) * time.Second),
		}); err != nil {
			t.Fatalf("RecordIndexerHistory %d: %v", i, err)
		}
	}
	history, err := st.ListIndexerHistory(ctx, indexerID, 2)
	if err != nil {
		t.Fatalf("ListIndexerHistory: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("history = %d, want 2", len(history))
	}
	if history[0].LatencyMS != 2 || history[1].LatencyMS != 1 {
		t.Fatalf("history order = %#v", history)
	}
	if history[0].App != "sonarr" || history[0].IndexerID != indexerID {
		t.Fatalf("history row = %#v", history[0])
	}

	if err := st.DeleteIndexer(ctx, indexerID); err != nil {
		t.Fatalf("DeleteIndexer: %v", err)
	}
	var nullRows int
	if err := st.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM indexer_history WHERE indexer_id IS NULL`).Scan(&nullRows); err != nil {
		t.Fatalf("count null history: %v", err)
	}
	if nullRows != 3 {
		t.Fatalf("history rows with NULL indexer = %d, want 3", nullRows)
	}
}

func TestApplicationsAppIndexersAndProxies(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	apiKey := []byte{0x00, 0xaa, 0xbb}
	appID, err := st.CreateApplication(ctx, Application{
		Kind: "sonarr", Name: "Sonarr", URL: "http://sonarr.test:8989",
		APIKeyEnc: apiKey, SyncLevel: "add",
	})
	if err != nil {
		t.Fatalf("CreateApplication: %v", err)
	}
	app, err := st.GetApplication(ctx, appID)
	if err != nil {
		t.Fatalf("GetApplication: %v", err)
	}
	if app.Kind != "sonarr" || !bytes.Equal(app.APIKeyEnc, apiKey) || app.SyncLevel != "add" {
		t.Fatalf("application = %#v", app)
	}
	apps, err := st.ListApplications(ctx)
	if err != nil {
		t.Fatalf("ListApplications: %v", err)
	}
	if len(apps) != 1 {
		t.Fatalf("applications = %d", len(apps))
	}

	app.Name = "Sonarr 4K"
	app.SyncLevel = "full"
	app.LastError = "timeout"
	app.LastSyncAt = time.Unix(1_700_000_000, 0)
	if err := st.UpdateApplication(ctx, app); err != nil {
		t.Fatalf("UpdateApplication: %v", err)
	}
	updated, err := st.GetApplication(ctx, appID)
	if err != nil {
		t.Fatalf("GetApplication: %v", err)
	}
	if updated.Name != "Sonarr 4K" || updated.SyncLevel != "full" ||
		updated.LastError != "timeout" || updated.LastSyncAt.Unix() != app.LastSyncAt.Unix() {
		t.Fatalf("updated application = %#v", updated)
	}
	if err := st.UpdateApplication(ctx, Application{ID: appID + 100, Name: "nope"}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("UpdateApplication missing = %v", err)
	}

	mustCreateDefinition(t, st, "app-def")
	indexerID := mustCreateIndexer(t, st, "app-def", "app-site", "App Site")
	if err := st.UpsertAppIndexer(ctx, AppIndexer{
		ApplicationID: appID, IndexerID: indexerID, RemoteID: "1", SyncedHash: "h1",
	}); err != nil {
		t.Fatalf("UpsertAppIndexer: %v", err)
	}
	if err := st.UpsertAppIndexer(ctx, AppIndexer{
		ApplicationID: appID, IndexerID: indexerID, RemoteID: "2", SyncedHash: "h2",
	}); err != nil {
		t.Fatalf("UpsertAppIndexer update: %v", err)
	}
	links, err := st.ListAppIndexers(ctx, appID)
	if err != nil {
		t.Fatalf("ListAppIndexers: %v", err)
	}
	if len(links) != 1 || links[0].RemoteID != "2" || links[0].SyncedHash != "h2" {
		t.Fatalf("app indexers = %#v", links)
	}
	if err := st.DeleteAppIndexer(ctx, appID, indexerID); err != nil {
		t.Fatalf("DeleteAppIndexer: %v", err)
	}
	links, err = st.ListAppIndexers(ctx, appID)
	if err != nil {
		t.Fatalf("ListAppIndexers: %v", err)
	}
	if len(links) != 0 {
		t.Fatalf("app indexers after delete = %#v", links)
	}

	if err := st.DeleteApplication(ctx, appID); err != nil {
		t.Fatalf("DeleteApplication: %v", err)
	}
	if _, err := st.GetApplication(ctx, appID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("GetApplication after delete = %v", err)
	}

	proxyID, err := st.CreateProxy(ctx, Proxy{
		Kind: "socks5", URL: "socks5://proxy.test:1080", SecretsEnc: []byte{0x01, 0x02},
	})
	if err != nil {
		t.Fatalf("CreateProxy: %v", err)
	}
	proxy, err := st.GetProxy(ctx, proxyID)
	if err != nil {
		t.Fatalf("GetProxy: %v", err)
	}
	if proxy.Kind != "socks5" || proxy.URL != "socks5://proxy.test:1080" ||
		!bytes.Equal(proxy.SecretsEnc, []byte{0x01, 0x02}) {
		t.Fatalf("proxy = %#v", proxy)
	}
	proxies, err := st.ListProxies(ctx)
	if err != nil {
		t.Fatalf("ListProxies: %v", err)
	}
	if len(proxies) != 1 {
		t.Fatalf("proxies = %d", len(proxies))
	}
	proxy.URL = "socks5://proxy2.test:1080"
	proxy.SecretsEnc = []byte{0x03}
	if err := st.UpdateProxy(ctx, proxy); err != nil {
		t.Fatalf("UpdateProxy: %v", err)
	}
	updatedProxy, err := st.GetProxy(ctx, proxyID)
	if err != nil {
		t.Fatalf("GetProxy: %v", err)
	}
	if updatedProxy.URL != proxy.URL || !bytes.Equal(updatedProxy.SecretsEnc, []byte{0x03}) {
		t.Fatalf("updated proxy = %#v", updatedProxy)
	}
	if err := st.DeleteProxy(ctx, proxyID); err != nil {
		t.Fatalf("DeleteProxy: %v", err)
	}
	if _, err := st.GetProxy(ctx, proxyID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("GetProxy after delete = %v", err)
	}
}

func TestIndexerForeignKey(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	_, err := st.CreateIndexer(ctx, Indexer{
		DefinitionID: "missing-definition",
		Protocol:     "torrent",
		Slug:         "orphan",
		Name:         "Orphan",
		Enabled:      true,
	})
	if err == nil {
		t.Fatal("CreateIndexer with unknown definition succeeded")
	}
	var count int
	if err := st.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM indexers WHERE slug = 'orphan'`).Scan(&count); err != nil {
		t.Fatalf("count orphan: %v", err)
	}
	if count != 0 {
		t.Fatalf("orphan indexer rows = %d", count)
	}
}
