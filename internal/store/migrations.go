package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// indexarr2FoundationVersion is the first numbered migration. The legacy
// schema above is created idempotently on every open and is not numbered.
const (
	indexarr2FoundationVersion    = 1
	indexarr2UsenetPayloadVersion = 2
	indexarr2DropLegacyVersion    = 3
	indexarr2IntervalVersion      = 4
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
			CHECK (protocol IN ('torrent', 'usenet')),
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
