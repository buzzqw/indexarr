package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// indexarr2FoundationVersion is the first numbered migration. The legacy
// schema above is created idempotently on every open and is not numbered.
const (
	indexarr2FoundationVersion      = 1
	indexarr2UsenetPayloadVersion   = 2
	indexarr2DropLegacyVersion      = 3
	indexarr2IntervalVersion        = 4
	indexarr2ActivityVersion        = 5
	indexarr2LiveSearchVersion      = 6
	indexarr2DownloadClientsVersion = 7
	indexarr2PairChannelsVersion    = 8
	indexarr2Cycle6Version          = 9
	indexarr2DedupVersion           = 10
	indexarr2TGxHashlessVersion     = 11
	indexarr2DownloadStatusVersion  = 12
	indexarr2SeedersCheckedVersion  = 13
	// indexarr2AppSnapshotsVersion adds app_snapshots (the state of an
	// application's indexer list before every change) and the per-application
	// tag filter used by the sync.
	indexarr2AppSnapshotsVersion = 14
	// indexarr2NotifyVersion stores the notification channels' secrets sealed
	// outside the settings table, the anti-spam timestamps and the last
	// notified run state of every indexer.
	indexarr2NotifyVersion = 15
	// indexarr2IndexerCriteriaVersion adds the seed criteria Prowlarr exposes
	// per indexer and syncs to the *arr applications.
	indexarr2IndexerCriteriaVersion = 16
	// indexarr2ReleaseFilesVersion adds the file list of every stored torrent
	// and a trigram index for searching inside file names.
	indexarr2ReleaseFilesVersion = 17
	// indexarr2Ed2kProtocolVersion widens the indexer protocol CHECK to the
	// eD2k network. SQLite cannot alter a CHECK constraint in place, so the
	// table is rebuilt; see applyEd2kProtocolMigration.
	indexarr2Ed2kProtocolVersion = 18
	// indexarr2Ed2kClientVersion widens the download client protocol CHECK to
	// eD2k, so an aMule client can be stored next to the torrent and usenet
	// ones.
	indexarr2Ed2kClientVersion = 19
	// indexarr2AppProfilesVersion stores the per-application sync profile
	// Prowlarr keeps in its application settings: the three search flags, a
	// seeder floor and the categories the app should receive.
	indexarr2AppProfilesVersion = 20
	// indexarr2PendingSearchesVersion keeps the search pages a flood-limited
	// site could not take yet (search.interval), so a restart resumes them
	// instead of dropping them.
	indexarr2PendingSearchesVersion = 21
	// indexarr2GrabReleaseVersion links each grab history row to the release it
	// delivered, so the Download tab can offer its magnet or .torrent link.
	indexarr2GrabReleaseVersion = 22
	// indexarr2IndexerTimeoutVersion stores an optional per-indexer request
	// timeout. Zero keeps the global fetch_timeout_seconds, so an existing row
	// behaves exactly as before; a site whose first answer is slow can get a
	// longer window without slowing every other source.
	indexarr2IndexerTimeoutVersion = 23
	// latestSchemaVersion is the newest numbered migration. Tests use it
	// instead of pinning a version so a new migration does not break them.
	latestSchemaVersion = indexarr2IndexerTimeoutVersion
)

const schemaMigrationsDDL = `CREATE TABLE IF NOT EXISTS schema_migrations (
	version    INTEGER PRIMARY KEY,
	name       TEXT    NOT NULL,
	applied_at INTEGER NOT NULL
)`

// indexarr2FoundationSchema creates the indexarr 2 data model. Tables are
// ordered so that foreign keys always point at an existing table: proxies
// before indexers, definitions before indexers, indexers before the tables
// that reference them.
var indexarr2FoundationSchema = []string{
	`CREATE TABLE IF NOT EXISTS proxies (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		kind        TEXT    NOT NULL DEFAULT '',
		url         TEXT    NOT NULL DEFAULT '',
		secrets_enc BLOB,
		created_at  INTEGER NOT NULL DEFAULT 0,
		updated_at  INTEGER NOT NULL DEFAULT 0
	)`,
	`CREATE TABLE IF NOT EXISTS definitions (
		id             TEXT PRIMARY KEY,
		schema_version INTEGER NOT NULL DEFAULT 1,
		family         TEXT    NOT NULL DEFAULT '',
		state          TEXT    NOT NULL DEFAULT 'bozza'
			CHECK (state IN ('bozza', 'verificata', 'stabile', 'degradata')),
		origin         TEXT    NOT NULL DEFAULT 'local'
			CHECK (origin IN ('builtin', 'catalog', 'local')),
		author         TEXT    NOT NULL DEFAULT '',
		body           TEXT    NOT NULL,
		hash           TEXT    NOT NULL,
		updated_at     INTEGER NOT NULL DEFAULT 0
	)`,
	`CREATE TABLE IF NOT EXISTS indexers (
		id               INTEGER PRIMARY KEY AUTOINCREMENT,
		definition_id    TEXT    NOT NULL REFERENCES definitions(id) ON DELETE RESTRICT,
		protocol         TEXT    NOT NULL DEFAULT 'torrent'
			CHECK (protocol IN ('torrent', 'usenet', 'ed2k')),
		slug             TEXT    NOT NULL UNIQUE,
		name             TEXT    NOT NULL,
		enabled          INTEGER NOT NULL DEFAULT 1,
		priority         INTEGER NOT NULL DEFAULT 25,
		config_json      TEXT    NOT NULL DEFAULT '{}',
		proxy_id         INTEGER REFERENCES proxies(id) ON DELETE SET NULL,
		tags             TEXT    NOT NULL DEFAULT '',
		query_limit      INTEGER NOT NULL DEFAULT 0,
		grab_limit       INTEGER NOT NULL DEFAULT 0,
		created_at       INTEGER NOT NULL DEFAULT 0,
		updated_at       INTEGER NOT NULL DEFAULT 0
	)`,
	`CREATE INDEX IF NOT EXISTS indexers_definition ON indexers(definition_id)`,
	`CREATE INDEX IF NOT EXISTS indexers_proxy ON indexers(proxy_id)`,
	`CREATE INDEX IF NOT EXISTS indexers_enabled ON indexers(enabled)`,
	`CREATE INDEX IF NOT EXISTS definitions_family ON definitions(family)`,
	`CREATE INDEX IF NOT EXISTS definitions_state ON definitions(state)`,
	`CREATE TABLE IF NOT EXISTS indexer_secrets (
		indexer_id INTEGER NOT NULL REFERENCES indexers(id) ON DELETE CASCADE,
		key        TEXT    NOT NULL,
		nonce      BLOB    NOT NULL,
		ciphertext BLOB    NOT NULL,
		updated_at INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (indexer_id, key)
	)`,
	`CREATE TABLE IF NOT EXISTS indexer_state (
		indexer_id     INTEGER PRIMARY KEY REFERENCES indexers(id) ON DELETE CASCADE,
		cookies_enc    BLOB,
		last_login_at  INTEGER NOT NULL DEFAULT 0,
		circuit        TEXT    NOT NULL DEFAULT 'closed'
			CHECK (circuit IN ('closed', 'open', 'half-open')),
		disabled_until INTEGER NOT NULL DEFAULT 0,
		last_error     TEXT    NOT NULL DEFAULT '',
		updated_at     INTEGER NOT NULL DEFAULT 0
	)`,
	`CREATE INDEX IF NOT EXISTS indexer_state_circuit ON indexer_state(circuit)`,
	`CREATE TABLE IF NOT EXISTS release_sources (
		release_id   INTEGER NOT NULL REFERENCES releases(id) ON DELETE CASCADE,
		indexer_id   INTEGER NOT NULL REFERENCES indexers(id) ON DELETE CASCADE,
		first_seen_at INTEGER NOT NULL DEFAULT 0,
		last_seen_at  INTEGER NOT NULL DEFAULT 0,
		seeders      INTEGER NOT NULL DEFAULT 0,
		leechers     INTEGER NOT NULL DEFAULT 0,
		download_ref TEXT    NOT NULL DEFAULT '',
		PRIMARY KEY (release_id, indexer_id)
	)`,
	`CREATE INDEX IF NOT EXISTS release_sources_indexer ON release_sources(indexer_id)`,
	`CREATE INDEX IF NOT EXISTS release_sources_last_seen ON release_sources(last_seen_at)`,
	`CREATE TABLE IF NOT EXISTS indexer_history (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		indexer_id INTEGER REFERENCES indexers(id) ON DELETE SET NULL,
		kind       TEXT    NOT NULL DEFAULT '',
		query      TEXT    NOT NULL DEFAULT '',
		status     TEXT    NOT NULL DEFAULT '',
		latency_ms INTEGER NOT NULL DEFAULT 0,
		bytes      INTEGER NOT NULL DEFAULT 0,
		error      TEXT    NOT NULL DEFAULT '',
		app        TEXT    NOT NULL DEFAULT '',
		started_at INTEGER NOT NULL DEFAULT 0
	)`,
	`CREATE INDEX IF NOT EXISTS indexer_history_started ON indexer_history(started_at)`,
	`CREATE INDEX IF NOT EXISTS indexer_history_indexer_started
		ON indexer_history(indexer_id, started_at)`,
	`CREATE TABLE IF NOT EXISTS applications (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		kind        TEXT    NOT NULL DEFAULT '',
		name        TEXT    NOT NULL,
		url         TEXT    NOT NULL DEFAULT '',
		api_key_enc BLOB,
		sync_level  TEXT    NOT NULL DEFAULT 'full'
			CHECK (sync_level IN ('disabled', 'add', 'add-remove', 'full')),
		last_sync_at INTEGER NOT NULL DEFAULT 0,
		last_error  TEXT    NOT NULL DEFAULT '',
		created_at  INTEGER NOT NULL DEFAULT 0,
		updated_at  INTEGER NOT NULL DEFAULT 0
	)`,
	`CREATE INDEX IF NOT EXISTS applications_kind ON applications(kind)`,
	`CREATE TABLE IF NOT EXISTS app_indexers (
		application_id INTEGER NOT NULL REFERENCES applications(id) ON DELETE CASCADE,
		indexer_id     INTEGER NOT NULL REFERENCES indexers(id) ON DELETE CASCADE,
		remote_id      TEXT    NOT NULL DEFAULT '',
		synced_hash    TEXT    NOT NULL DEFAULT '',
		PRIMARY KEY (application_id, indexer_id)
	)`,
	`CREATE INDEX IF NOT EXISTS app_indexers_indexer ON app_indexers(indexer_id)`,
	// app_secrets holds the settings-level secrets (notification channels)
	// sealed with the vault key. They are deliberately not in `settings`, so a
	// database dump never contains the Telegram token or the webhook URL.
	`CREATE TABLE IF NOT EXISTS app_secrets (
		key        TEXT PRIMARY KEY,
		nonce      BLOB NOT NULL,
		ciphertext BLOB NOT NULL,
		updated_at INTEGER NOT NULL DEFAULT 0
	)`,
	// notify_events is the anti-spam ledger: one row per event kind and
	// source, holding the last successful delivery.
	`CREATE TABLE IF NOT EXISTS notify_events (
		kind         TEXT NOT NULL,
		source       TEXT NOT NULL,
		last_sent_at INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (kind, source)
	)`,
	// indexer_notify_state remembers the last notified condition of an
	// indexer, so a notification is sent on a change and not on every failure.
	`CREATE TABLE IF NOT EXISTS indexer_notify_state (
		indexer_id INTEGER PRIMARY KEY REFERENCES indexers(id) ON DELETE CASCADE,
		state      TEXT    NOT NULL DEFAULT '',
		updated_at INTEGER NOT NULL DEFAULT 0
	)`,
}

// applyMigration records and runs one numbered migration. It is a no-op when
// the version is already present, and a failure rolls the whole migration back
// without touching schema_migrations.
func (s *Store) applyMigration(ctx context.Context, version int, name string, fn func(*sql.Tx) error) error {
	if _, err := s.db.ExecContext(ctx, schemaMigrationsDDL); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}
	applied, err := s.migrationApplied(ctx, version)
	if err != nil {
		return err
	}
	if applied {
		return nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration %d: %w", version, err)
	}
	defer tx.Rollback()

	// Re-check inside the transaction: another process may have applied the
	// migration between the read above and the transaction starting.
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, version).Scan(&applied); err != nil {
		return fmt.Errorf("check migration %d: %w", version, err)
	}
	if applied {
		return nil
	}
	if fn != nil {
		if err := fn(tx); err != nil {
			return fmt.Errorf("migration %d (%s): %w", version, name, err)
		}
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO schema_migrations(version, name, applied_at) VALUES(?, ?, ?)`,
		version, name, time.Now().Unix()); err != nil {
		return fmt.Errorf("record migration %d: %w", version, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %d: %w", version, err)
	}
	return nil
}

// schemaVersion returns the highest applied migration, or 0 when the database
// has no numbered migrations yet.
func (s *Store) schemaVersion(ctx context.Context) (int, error) {
	var tables int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'schema_migrations'`).Scan(&tables); err != nil {
		return 0, fmt.Errorf("check schema_migrations: %w", err)
	}
	if tables == 0 {
		return 0, nil
	}
	var version int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&version); err != nil {
		return 0, fmt.Errorf("read schema version: %w", err)
	}
	return version, nil
}

func (s *Store) migrationApplied(ctx context.Context, version int) (bool, error) {
	var count int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, version).Scan(&count); err != nil {
		return false, fmt.Errorf("check migration %d: %w", version, err)
	}
	return count > 0, nil
}

// pairChannelsMigration updates the existing TorrentLeech rows without
// deleting anything: the RSS indexer becomes the latest channel and the bridge
// becomes the search channel. The general importer applies the same rule to
// new imports; this migration only repairs databases created before it.
func pairChannelsMigration(tx *sql.Tx) error {
	rows, err := tx.Query(`SELECT id, slug, config_json FROM indexers
		WHERE slug IN ('torrentleech', 'torrentleech-rss')`)
	if err != nil {
		return err
	}
	type row struct {
		id     int64
		slug   string
		config map[string]string
	}
	var changes []row
	for rows.Next() {
		var item row
		var raw string
		if err := rows.Scan(&item.id, &item.slug, &raw); err != nil {
			rows.Close()
			return err
		}
		item.config = map[string]string{}
		if strings.TrimSpace(raw) != "" {
			if err := json.Unmarshal([]byte(raw), &item.config); err != nil {
				rows.Close()
				return fmt.Errorf("indexer %s config: %w", item.slug, err)
			}
		}
		changes = append(changes, item)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, item := range changes {
		item.config["site"] = "torrentleech.org"
		if item.slug == "torrentleech-rss" {
			item.config["channel"] = "latest"
			item.config["latest"] = "true"
		} else {
			item.config["channel"] = "search"
			item.config["latest"] = "false"
		}
		encoded, err := json.Marshal(item.config)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE indexers SET config_json = ?, updated_at = ? WHERE id = ?`,
			string(encoded), time.Now().Unix(), item.id); err != nil {
			return err
		}
	}
	return nil
}

func cycle6Migration(tx *sql.Tx) error {
	if _, err := tx.Exec(`ALTER TABLE app_searches ADD COLUMN params TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	rows, err := tx.Query(`SELECT id, config_json FROM indexers`)
	if err != nil {
		return err
	}
	type change struct {
		id     int64
		config map[string]string
	}
	var changes []change
	for rows.Next() {
		var item change
		var raw string
		if err := rows.Scan(&item.id, &raw); err != nil {
			rows.Close()
			return err
		}
		item.config = map[string]string{}
		if strings.TrimSpace(raw) != "" {
			if err := json.Unmarshal([]byte(raw), &item.config); err != nil {
				rows.Close()
				return fmt.Errorf("indexer %d config: %w", item.id, err)
			}
		}
		changes = append(changes, item)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, item := range changes {
		raw := item.config["categories"]
		if raw == "" {
			continue
		}
		kept := make([]string, 0, 8)
		for _, part := range strings.Split(raw, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			value, err := strconv.Atoi(part)
			if err != nil || value >= 100000 {
				continue
			}
			kept = append(kept, part)
		}
		if len(kept) == len(strings.Split(raw, ",")) {
			continue
		}
		item.config["categories"] = strings.Join(kept, ",")
		encoded, err := json.Marshal(item.config)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE indexers SET config_json = ?, updated_at = ? WHERE id = ?`,
			string(encoded), time.Now().Unix(), item.id); err != nil {
			return err
		}
	}
	return nil
}

// applyEd2kProtocolMigration rebuilds the indexers table so its protocol CHECK
// accepts 'ed2k'. SQLite cannot widen a CHECK constraint in place. The
// migration runs on one dedicated connection with foreign keys disabled: the
// child tables keep referencing "indexers", so after the rename they point at
// the new table and no row is lost. Foreign keys are switched back on before
// the connection returns to the pool.
func (s *Store) applyEd2kProtocolMigration(ctx context.Context) error {
	applied, err := s.migrationApplied(ctx, indexarr2Ed2kProtocolVersion)
	if err != nil {
		return err
	}
	if applied {
		return nil
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("begin migration %d: %w", indexarr2Ed2kProtocolVersion, err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
		return fmt.Errorf("disable foreign keys: %w", err)
	}
	defer func() {
		_, _ = conn.ExecContext(context.WithoutCancel(ctx), `PRAGMA foreign_keys=ON`)
	}()

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration %d: %w", indexarr2Ed2kProtocolVersion, err)
	}
	defer tx.Rollback()

	statements := []string{
		`CREATE TABLE indexers_new (
			id               INTEGER PRIMARY KEY AUTOINCREMENT,
			definition_id    TEXT    NOT NULL REFERENCES definitions(id) ON DELETE RESTRICT,
			protocol         TEXT    NOT NULL DEFAULT 'torrent'
				CHECK (protocol IN ('torrent', 'usenet', 'ed2k')),
			slug             TEXT    NOT NULL UNIQUE,
			name             TEXT    NOT NULL,
			enabled          INTEGER NOT NULL DEFAULT 1,
			priority         INTEGER NOT NULL DEFAULT 25,
			config_json      TEXT    NOT NULL DEFAULT '{}',
			proxy_id         INTEGER REFERENCES proxies(id) ON DELETE SET NULL,
			tags             TEXT    NOT NULL DEFAULT '',
			query_limit      INTEGER NOT NULL DEFAULT 0,
			grab_limit       INTEGER NOT NULL DEFAULT 0,
			created_at       INTEGER NOT NULL DEFAULT 0,
			updated_at       INTEGER NOT NULL DEFAULT 0,
			interval_seconds INTEGER NOT NULL DEFAULT 0,
			min_seeders      INTEGER NOT NULL DEFAULT 0,
			seed_ratio       REAL    NOT NULL DEFAULT 0,
			seed_time_minutes INTEGER NOT NULL DEFAULT 0,
			season_pack_seed_time_minutes INTEGER NOT NULL DEFAULT 0
		)`,
		`INSERT INTO indexers_new(id, definition_id, protocol, slug, name, enabled,
			priority, config_json, proxy_id, tags, query_limit, grab_limit,
			created_at, updated_at, interval_seconds, min_seeders, seed_ratio,
			seed_time_minutes, season_pack_seed_time_minutes)
		 SELECT id, definition_id, protocol, slug, name, enabled, priority,
			config_json, proxy_id, tags, query_limit, grab_limit, created_at, updated_at,
			interval_seconds, min_seeders, seed_ratio, seed_time_minutes,
			season_pack_seed_time_minutes
		 FROM indexers`,
		`DROP TABLE indexers`,
		`ALTER TABLE indexers_new RENAME TO indexers`,
		`CREATE INDEX IF NOT EXISTS indexers_definition ON indexers(definition_id)`,
		`CREATE INDEX IF NOT EXISTS indexers_proxy ON indexers(proxy_id)`,
		`CREATE INDEX IF NOT EXISTS indexers_enabled ON indexers(enabled)`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("migration %d (%s): %w", indexarr2Ed2kProtocolVersion, "ed2k protocol", err)
		}
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO schema_migrations(version, name, applied_at) VALUES(?, ?, ?)`,
		indexarr2Ed2kProtocolVersion, "ed2k protocol", time.Now().Unix()); err != nil {
		return fmt.Errorf("record migration %d: %w", indexarr2Ed2kProtocolVersion, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %d: %w", indexarr2Ed2kProtocolVersion, err)
	}
	return nil
}
