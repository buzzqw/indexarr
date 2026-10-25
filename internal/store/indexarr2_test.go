package store

import (
	"bytes"
	"context"
	"database/sql"
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
	if version != indexarr2ReleaseFilesVersion {
		t.Fatalf("schema version = %d, want %d", version, indexarr2ActivityVersion)
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
	exists, err := st.columnExists("indexers", "interval_seconds")
	if err != nil {
		t.Fatalf("columnExists(indexers.interval_seconds): %v", err)
	}
	if !exists {
		t.Error("indexers.interval_seconds is missing")
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
	if version != indexarr2ReleaseFilesVersion {
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
	err := st.applyMigration(ctx, 99, "broken", func(tx *sql.Tx) error {
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
	if version != indexarr2ReleaseFilesVersion {
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
		`SELECT COUNT(*) FROM schema_migrations WHERE version = 99`).Scan(&rows); err != nil {
		t.Fatalf("check migration row: %v", err)
	}
	if rows != 0 {
		t.Fatal("failed migration was recorded")
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

func TestRecordReleaseSourceAfterIndexerDelete(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	mustCreateDefinition(t, st, "gone-def")
	indexerID := mustCreateIndexer(t, st, "gone-def", "gone-site", "Gone Site")
	now := time.Now()
	release := Release{DedupKey: "gone-release", Title: "Gone Release", SearchTitle: "gone release", FirstSeenAt: now, LastSeenAt: now}
	if _, err := st.UpsertRelease(ctx, &release); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteIndexer(ctx, indexerID); err != nil {
		t.Fatal(err)
	}
	err := st.RecordReleaseSource(ctx, ReleaseSource{ReleaseID: release.ID, IndexerID: indexerID})
	if !errors.Is(err, ErrIndexerGone) {
		t.Fatalf("RecordReleaseSource after delete = %v, want ErrIndexerGone", err)
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

func TestDropLegacySourcesMigration(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "old-schema.db")
	st, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	mustCreateDefinition(t, st, "keep-def")
	keepID := mustCreateIndexer(t, st, "keep-def", "keep", "Keep")
	bridgeID := mustCreateIndexer(t, st, "keep-def", "bridge", "Bridge")
	release := Release{DedupKey: "keep-release", Title: "Keep release", SearchTitle: "keep release"}
	if _, err := st.UpsertRelease(ctx, &release); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	raw, err := sql.Open("sqlite", "file:"+dsnPath(path))
	if err != nil {
		t.Fatalf("raw open: %v", err)
	}
	for _, statement := range []string{
		`CREATE TABLE sources (id INTEGER PRIMARY KEY, name TEXT NOT NULL)`,
		`INSERT INTO sources(id, name) VALUES(1, 'old')`,
		`ALTER TABLE indexers ADD COLUMN legacy_source_id INTEGER`,
		`CREATE UNIQUE INDEX indexers_legacy_source ON indexers(legacy_source_id) WHERE legacy_source_id IS NOT NULL`,
		`CREATE TABLE runs (id INTEGER PRIMARY KEY, source_id INTEGER REFERENCES sources(id))`,
		`ALTER TABLE releases ADD COLUMN source_id INTEGER REFERENCES sources(id) ON DELETE SET NULL`,
		`ALTER TABLE releases ADD COLUMN source_name TEXT NOT NULL DEFAULT ''`,
		`UPDATE releases SET source_id = 1, source_name = 'old' WHERE id = ?`,
		`UPDATE indexers SET legacy_source_id = 1 WHERE id = ?`,
		`DELETE FROM schema_migrations WHERE version = 3`,
	} {
		var args []any
		if strings.Contains(statement, "?") {
			if strings.Contains(statement, "releases") {
				args = []any{release.ID}
			} else {
				args = []any{bridgeID}
			}
		}
		if _, err := raw.Exec(statement, args...); err != nil {
			_ = raw.Close()
			t.Fatalf("%s: %v", statement, err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	migrated, err := Open(path)
	if err != nil {
		t.Fatalf("Open migrated: %v", err)
	}
	defer migrated.Close()
	version, err := migrated.schemaVersion(ctx)
	if err != nil || version != indexarr2ReleaseFilesVersion {
		t.Fatalf("schema version = %d, %v", version, err)
	}
	for _, table := range []string{"sources", "runs"} {
		var count int
		if err := migrated.DB().QueryRowContext(ctx,
			`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Errorf("table %s still exists", table)
		}
	}
	for _, column := range []struct{ table, name string }{
		{"indexers", "legacy_source_id"},
		{"releases", "source_id"},
		{"releases", "source_name"},
	} {
		exists, err := migrated.columnExists(column.table, column.name)
		if err != nil {
			t.Fatal(err)
		}
		if exists {
			t.Errorf("%s.%s still exists", column.table, column.name)
		}
	}
	if _, err := migrated.GetRelease(ctx, release.ID); err != nil {
		t.Errorf("release was not preserved: %v", err)
	}
	if _, err := migrated.GetIndexer(ctx, keepID); err != nil {
		t.Errorf("native indexer was not preserved: %v", err)
	}
	if _, err := migrated.GetIndexer(ctx, bridgeID); err == nil {
		t.Error("bridge indexer was not removed")
	}
	var provenance int
	if err := migrated.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='release_sources'`).Scan(&provenance); err != nil {
		t.Fatal(err)
	}
	if provenance != 1 {
		t.Error("release_sources was not recreated")
	}
}

func TestSetDefinitionStateKeepsTheDefinition(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	def := testDefinition("health-state")
	def.UpdatedAt = time.Unix(1_700_000_000, 0)
	if err := st.UpsertDefinition(ctx, def); err != nil {
		t.Fatalf("UpsertDefinition: %v", err)
	}
	before, err := st.GetDefinition(ctx, def.ID)
	if err != nil {
		t.Fatal(err)
	}

	if err := st.SetDefinitionState(ctx, def.ID, "degradata"); err != nil {
		t.Fatalf("SetDefinitionState: %v", err)
	}
	after, err := st.GetDefinition(ctx, def.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.State != "degradata" {
		t.Errorf("state = %q", after.State)
	}
	if after.Body != before.Body || after.Hash != before.Hash ||
		after.UpdatedAt.Unix() != before.UpdatedAt.Unix() {
		t.Errorf("state transition changed the definition: %#v", after)
	}

	if err := st.SetDefinitionState(ctx, def.ID, "verificata"); err != nil {
		t.Fatalf("recovery: %v", err)
	}
	recovered, err := st.GetDefinition(ctx, def.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.State != "verificata" || recovered.Body != before.Body {
		t.Errorf("recovery = %#v", recovered)
	}

	if err := st.SetDefinitionState(ctx, def.ID, "not-a-state"); err == nil {
		t.Error("invalid state was accepted")
	}
	if err := st.SetDefinitionState(ctx, "missing", "stabile"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("missing definition error = %v", err)
	}
}

func TestAppSnapshotsAndTagFilter(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	appID, err := st.CreateApplication(ctx, Application{
		Kind: "sonarr", Name: "Sonarr", URL: "http://sonarr.test:8989",
		SyncLevel: "full", TagFilter: "tv, 4k",
	})
	if err != nil {
		t.Fatalf("CreateApplication: %v", err)
	}
	app, err := st.GetApplication(ctx, appID)
	if err != nil {
		t.Fatalf("GetApplication: %v", err)
	}
	if app.TagFilter != "tv, 4k" {
		t.Fatalf("tag filter = %q", app.TagFilter)
	}

	payload := `[{"name":"indexarr · A"},{"name":"Manual"}]`
	baseID, err := st.CreateAppSnapshot(ctx, AppSnapshot{
		AppID: appID, Reason: SnapshotReasonSync, IndexerCount: 2, Payload: payload,
		CreatedAt: time.Unix(1_600_000_000, 0),
	})
	if err != nil {
		t.Fatalf("CreateAppSnapshot: %v", err)
	}
	// An invalid reason is rejected instead of silently stored.
	if _, err := st.CreateAppSnapshot(ctx, AppSnapshot{
		AppID: appID, Reason: "sometimes", Payload: "[]",
	}); err == nil {
		t.Fatal("CreateAppSnapshot accepted an invalid reason")
	}

	// A snapshot can only be read through its own application.
	otherID, err := st.CreateApplication(ctx, Application{
		Kind: "radarr", Name: "Radarr", URL: "http://radarr.test:7878", SyncLevel: "full",
	})
	if err != nil {
		t.Fatalf("CreateApplication other: %v", err)
	}
	if _, err := st.GetAppSnapshot(ctx, otherID, baseID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("cross-application snapshot read = %v", err)
	}
	got, err := st.GetAppSnapshot(ctx, appID, baseID)
	if err != nil {
		t.Fatalf("GetAppSnapshot: %v", err)
	}
	if got.Reason != SnapshotReasonSync || got.Payload != payload {
		t.Fatalf("snapshot = %#v", got)
	}

	// Retention keeps exactly the newest 20 rows, newest first.
	for i := 0; i < 25; i++ {
		if _, err := st.CreateAppSnapshot(ctx, AppSnapshot{
			AppID: appID, Reason: SnapshotReasonManual,
			IndexerCount: i, Payload: `[{"name":"indexarr · A"}]`,
			CreatedAt: time.Unix(1_700_000_000+int64(i), 0),
		}); err != nil {
			t.Fatalf("CreateAppSnapshot %d: %v", i, err)
		}
	}
	snapshots, err := st.ListAppSnapshots(ctx, appID)
	if err != nil {
		t.Fatalf("ListAppSnapshots: %v", err)
	}
	if len(snapshots) != AppSnapshotRetention {
		t.Fatalf("snapshots = %d, want %d", len(snapshots), AppSnapshotRetention)
	}
	if snapshots[0].IndexerCount != 24 {
		t.Errorf("newest snapshot count = %d, want 24", snapshots[0].IndexerCount)
	}
	for i := 1; i < len(snapshots); i++ {
		if snapshots[i].CreatedAt.After(snapshots[i-1].CreatedAt) {
			t.Fatalf("snapshots are not newest first at %d", i)
		}
	}

	// A snapshot can only be read through its own application.
	if _, err := st.GetAppSnapshot(ctx, otherID, snapshots[0].ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("cross-application snapshot read = %v", err)
	}

	// Deleting the application removes its snapshots.
	if err := st.DeleteApplication(ctx, appID); err != nil {
		t.Fatalf("DeleteApplication: %v", err)
	}
	remaining, err := st.ListAppSnapshots(ctx, appID)
	if err != nil {
		t.Fatalf("ListAppSnapshots after delete: %v", err)
	}
	if len(remaining) != 0 {
		t.Fatalf("snapshots after delete = %d", len(remaining))
	}
}
