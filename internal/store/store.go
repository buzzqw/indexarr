// Package store is the SQLite persistence layer of indexarr. The schema is
// created on first start and is safe to open repeatedly; there is no separate
// migration tool.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/buzzqw/indexarr/internal/vault"
	_ "modernc.org/sqlite"
)

// Store owns the SQLite database. A single connection is kept, as the pure-Go
// driver is serialised per connection and indexarr mixes a web UI with
// background writers.
type Store struct {
	db   *sql.DB
	path string
	// vault seals and opens the settings-level secrets (notification
	// channels). It is optional: a store without a vault keeps the legacy
	// plaintext rows so tests and embedded uses still work.
	vault *vault.Vault
	// counts caches the dashboard counters for a few seconds.
	counts countsCache
}

// Open opens (and creates when missing) the database at path.
func Open(path string) (*Store, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("store: empty database path")
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create database directory: %w", err)
		}
	}
	dsn := "file:" + dsnPath(path) +
		"?_pragma=busy_timeout(10000)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)&_pragma=cache_size(-4000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	// WAL allows several readers concurrently with one writer. A small pool
	// keeps a burst of Torznab clients from serialising behind a single
	// connection while still bounding memory: each connection owns a 4 MiB
	// page cache. busy_timeout absorbs the brief write-lock contention.
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping %s: %w", path, err)
	}
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("enable WAL on %s: %w", path, err)
	}
	s := &Store{db: db, path: path}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// dsnPath percent-escapes the characters SQLite treats as URI syntax.
func dsnPath(path string) string {
	replacer := strings.NewReplacer("%", "%25", "?", "%3F", "#", "%23")
	return replacer.Replace(path)
}

// Path returns the database file path.
func (s *Store) Path() string { return s.path }

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// DB exposes the underlying handle for tests and maintenance operations.
func (s *Store) DB() *sql.DB { return s.db }

// migrate creates the schema. Every statement is idempotent.
func (s *Store) migrate() error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS releases (
			id                 INTEGER PRIMARY KEY AUTOINCREMENT,
			dedup_key          TEXT    NOT NULL UNIQUE,
			infohash           TEXT    NOT NULL DEFAULT '',
			imdb_id            TEXT    NOT NULL DEFAULT '',
			tmdb_id            TEXT    NOT NULL DEFAULT '',
			tvdb_id            TEXT    NOT NULL DEFAULT '',
			title              TEXT    NOT NULL,
			search_title       TEXT    NOT NULL DEFAULT '',
			size               INTEGER NOT NULL DEFAULT 0,
			seeders            INTEGER NOT NULL DEFAULT 0,
			leechers           INTEGER NOT NULL DEFAULT 0,
			category           TEXT    NOT NULL DEFAULT '',
			published_at       INTEGER NOT NULL DEFAULT 0,
			guid               TEXT    NOT NULL DEFAULT '',
			magnet             TEXT    NOT NULL DEFAULT '',
			torrent            BLOB,
			torrent_size       INTEGER NOT NULL DEFAULT 0,
			torrent_fetched_at INTEGER NOT NULL DEFAULT 0,
			first_seen_at      INTEGER NOT NULL DEFAULT 0,
			last_seen_at       INTEGER NOT NULL DEFAULT 0,
			times_seen         INTEGER NOT NULL DEFAULT 1,
			favorite           INTEGER NOT NULL DEFAULT 0,
			hidden             INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE IF NOT EXISTS blocklist (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			kind       TEXT    NOT NULL,
			value      TEXT    NOT NULL,
			note       TEXT    NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL DEFAULT 0,
			UNIQUE(kind, value)
		)`,
		`CREATE TABLE IF NOT EXISTS saved_searches (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			name       TEXT    NOT NULL UNIQUE,
			query      TEXT    NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE IF NOT EXISTS api_keys (
			id           INTEGER PRIMARY KEY AUTOINCREMENT,
			name         TEXT    NOT NULL,
			key          TEXT    NOT NULL UNIQUE,
			created_at   INTEGER NOT NULL DEFAULT 0,
			last_used_at INTEGER NOT NULL DEFAULT 0,
			requests     INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE IF NOT EXISTS settings (
			key   TEXT PRIMARY KEY,
			value TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS releases_infohash ON releases(infohash)`,
		`CREATE INDEX IF NOT EXISTS releases_guid ON releases(guid)`,
		`CREATE INDEX IF NOT EXISTS releases_published ON releases(published_at DESC)`,
		`CREATE INDEX IF NOT EXISTS releases_category ON releases(category)`,
		`CREATE INDEX IF NOT EXISTS releases_last_seen ON releases(last_seen_at)`,
		`CREATE INDEX IF NOT EXISTS releases_imdb ON releases(imdb_id)`,
		`CREATE INDEX IF NOT EXISTS releases_tmdb ON releases(tmdb_id)`,
		`CREATE INDEX IF NOT EXISTS releases_tvdb ON releases(tvdb_id)`,
	}
	for _, statement := range statements {
		if _, err := s.db.Exec(statement); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}
	if err := s.ensureReleaseColumns(); err != nil {
		return err
	}
	// Numbered migrations start here. The baseline above remains idempotent so
	// databases created before schema_migrations keep working unchanged.
	if err := s.applyMigration(context.Background(), indexarr2FoundationVersion,
		"indexarr2 foundation", func(tx *sql.Tx) error {
			for _, statement := range indexarr2FoundationSchema {
				if _, err := tx.Exec(statement); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
		return err
	}
	// Migration 2 gives every release a protocol and a generic payload, so
	// Usenet .nzb files live next to torrents without a second cache.
	if err := s.applyMigration(context.Background(), indexarr2UsenetPayloadVersion,
		"usenet payload", func(tx *sql.Tx) error {
			for _, statement := range []string{
				`ALTER TABLE releases ADD COLUMN protocol TEXT NOT NULL DEFAULT 'torrent'`,
				`ALTER TABLE releases ADD COLUMN payload BLOB`,
				`ALTER TABLE releases ADD COLUMN payload_type TEXT NOT NULL DEFAULT ''`,
			} {
				if _, err := tx.Exec(statement); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
		return err
	}
	// Migration 3 removes the legacy sources path from databases created before
	// WP16. Fresh databases already have the final schema.
	if err := s.applyMigration(context.Background(), indexarr2DropLegacyVersion,
		"drop legacy sources", s.dropLegacySources); err != nil {
		return err
	}
	// Migration 4 adds the optional per-indexer scheduler interval. Zero keeps
	// the global default_interval_seconds, so existing rows and the fresh-start
	// database keep their current cadence.
	if err := s.applyMigration(context.Background(), indexarr2IntervalVersion,
		"indexer interval", func(tx *sql.Tx) error {
			_, err := tx.Exec(`ALTER TABLE indexers ADD COLUMN interval_seconds INTEGER NOT NULL DEFAULT 0`)
			return err
		}); err != nil {
		return err
	}
	// Migration 5 records what each run read and discarded. The metrics live
	// in a child table so indexer_history keeps its narrow, append-only shape
	// and a row without metrics still reads as "unknown". Samples are one row
	// per indexer, overwritten by every successful run: no unbounded files.
	// app_searches is the app-facing view of the Torznab requests.
	if err := s.applyMigration(context.Background(), indexarr2ActivityVersion,
		"indexer activity", func(tx *sql.Tx) error {
			for _, statement := range []string{
				`CREATE TABLE IF NOT EXISTS indexer_history_metrics (
					history_id            INTEGER PRIMARY KEY
						REFERENCES indexer_history(id) ON DELETE CASCADE,
					seen                  INTEGER NOT NULL DEFAULT 0,
					items_new             INTEGER NOT NULL DEFAULT 0,
					updated               INTEGER NOT NULL DEFAULT 0,
					duplicates            INTEGER NOT NULL DEFAULT 0,
					discarded             INTEGER NOT NULL DEFAULT 0,
					discarded_no_link     INTEGER NOT NULL DEFAULT 0,
					discarded_blocked     INTEGER NOT NULL DEFAULT 0,
					discarded_too_old     INTEGER NOT NULL DEFAULT 0,
					discarded_category    INTEGER NOT NULL DEFAULT 0,
					discarded_parse       INTEGER NOT NULL DEFAULT 0,
					discarded_other       INTEGER NOT NULL DEFAULT 0,
					field_total           INTEGER NOT NULL DEFAULT 0,
					field_size            INTEGER NOT NULL DEFAULT 0,
					field_seeders         INTEGER NOT NULL DEFAULT 0,
					field_category        INTEGER NOT NULL DEFAULT 0,
					field_date            INTEGER NOT NULL DEFAULT 0,
					field_hash            INTEGER NOT NULL DEFAULT 0
				)`,
				`CREATE TABLE IF NOT EXISTS indexer_samples (
					indexer_id  INTEGER PRIMARY KEY
						REFERENCES indexers(id) ON DELETE CASCADE,
					request_url TEXT    NOT NULL DEFAULT '',
					method      TEXT    NOT NULL DEFAULT 'GET',
					status      INTEGER NOT NULL DEFAULT 0,
					latency_ms  INTEGER NOT NULL DEFAULT 0,
					body        TEXT    NOT NULL DEFAULT '',
					body_bytes  INTEGER NOT NULL DEFAULT 0,
					truncated   INTEGER NOT NULL DEFAULT 0,
					items_json  TEXT    NOT NULL DEFAULT '[]',
					items_count INTEGER NOT NULL DEFAULT 0,
					fetched_at  INTEGER NOT NULL DEFAULT 0
				)`,
				`CREATE TABLE IF NOT EXISTS app_searches (
					id            INTEGER PRIMARY KEY AUTOINCREMENT,
					app           TEXT    NOT NULL DEFAULT '',
					mode          TEXT    NOT NULL DEFAULT '',
					query         TEXT    NOT NULL DEFAULT '',
					indexer_ids   TEXT    NOT NULL DEFAULT '[]',
					indexer_names TEXT    NOT NULL DEFAULT '[]',
					results_json  TEXT    NOT NULL DEFAULT '{}',
					total_results INTEGER NOT NULL DEFAULT 0,
					duration_ms   INTEGER NOT NULL DEFAULT 0,
					error         TEXT    NOT NULL DEFAULT '',
					created_at    INTEGER NOT NULL DEFAULT 0
				)`,
				`CREATE INDEX IF NOT EXISTS app_searches_created
					ON app_searches(created_at)`,
				`CREATE INDEX IF NOT EXISTS indexer_samples_fetched
					ON indexer_samples(fetched_at)`,
			} {
				if _, err := tx.Exec(statement); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
		return err
	}
	// Migration 6 stores the live fan-out of each app search: which indexers
	// answered, with which effective mode, how many results they stored, how
	// long they took and who missed the request deadline. It is additive and
	// old rows read as an empty fan-out.
	if err := s.applyMigration(context.Background(), indexarr2LiveSearchVersion,
		"app search live fan-out", func(tx *sql.Tx) error {
			_, err := tx.Exec(`ALTER TABLE app_searches ADD COLUMN live_json TEXT NOT NULL DEFAULT '[]'`)
			return err
		}); err != nil {
		return err
	}
	// Migration 7 moves download clients out of the single settings row into
	// their own table. It is additive: the old download_client* settings stay
	// in place and remain the fallback until the operator adds a client in App.
	if err := s.applyMigration(context.Background(), indexarr2DownloadClientsVersion,
		"download clients", func(tx *sql.Tx) error {
			for _, statement := range []string{
				`CREATE TABLE IF NOT EXISTS download_clients (
					id           INTEGER PRIMARY KEY AUTOINCREMENT,
					name         TEXT    NOT NULL DEFAULT '',
					kind         TEXT    NOT NULL DEFAULT 'qbittorrent',
					protocol     TEXT    NOT NULL DEFAULT 'torrent'
						CHECK (protocol IN ('torrent', 'usenet')),
					url          TEXT    NOT NULL DEFAULT '',
					username     TEXT    NOT NULL DEFAULT '',
					password_enc TEXT,
					apikey_enc   TEXT,
					category     TEXT    NOT NULL DEFAULT '',
					save_path    TEXT    NOT NULL DEFAULT '',
					paused       INTEGER NOT NULL DEFAULT 0,
					enabled      INTEGER NOT NULL DEFAULT 1,
					is_default   INTEGER NOT NULL DEFAULT 0,
					created_at   INTEGER NOT NULL DEFAULT 0,
					updated_at   INTEGER NOT NULL DEFAULT 0
				)`,
				`CREATE INDEX IF NOT EXISTS download_clients_protocol
					ON download_clients(protocol, enabled)`,
				`CREATE INDEX IF NOT EXISTS download_clients_default
					ON download_clients(is_default)`,
			} {
				if _, err := tx.Exec(statement); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
		return err
	}
	// Migration 8 pairs an existing native RSS feed with its search bridge:
	// the feed keeps the scheduled latest run and the bridge answers searches.
	// It is a data-only, non-destructive migration for the TorrentLeech pair
	// created before the general pairing rule existed.
	if err := s.applyMigration(context.Background(), indexarr2PairChannelsVersion,
		"pair latest and search channels", pairChannelsMigration); err != nil {
		return err
	}
	// Migration 9 records the sanitized Torznab query parameters for app
	// searches and drops custom manager categories (ids >= 100000) from the
	// existing indexer configurations. It is additive and non-destructive.
	if err := s.applyMigration(context.Background(), indexarr2Cycle6Version,
		"app search params and standard categories", cycle6Migration); err != nil {
		return err
	}
	if err := s.migrateLiveWaitDefault(context.Background()); err != nil {
		return err
	}
	// Migration 10 supports intelligent deduplication: it records how many
	// duplicates an app search collapsed, which source delivered a grab, and
	// adds the exact-title index the conservative matcher uses.
	if err := s.applyMigration(context.Background(), indexarr2DedupVersion,
		"intelligent deduplication", func(tx *sql.Tx) error {
			for _, statement := range []string{
				`ALTER TABLE app_searches ADD COLUMN duplicates_merged INTEGER NOT NULL DEFAULT 0`,
				`ALTER TABLE release_sources ADD COLUMN grab_count INTEGER NOT NULL DEFAULT 0`,
				`ALTER TABLE release_sources ADD COLUMN last_grab_at INTEGER NOT NULL DEFAULT 0`,
				`CREATE INDEX IF NOT EXISTS releases_search_title ON releases(search_title)`,
			} {
				if _, err := tx.Exec(statement); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
		return err
	}
	// Migration 11 removes the hashless TorrentGalaxy rows left by the old
	// grab-only definition when the same title already has a hashed row from
	// the same TGx source. It is idempotent and runs once per database.
	if err := s.applyMigration(context.Background(), indexarr2TGxHashlessVersion,
		"remove orphan TGx hashless rows", func(tx *sql.Tx) error {
			_, err := cleanupTorrentGalaxyHashlessTx(context.Background(), tx)
			return err
		}); err != nil {
		return err
	}
	// Migration 12 records download-attempt health per source. A repeated
	// 404 quarantines that source with a growing backoff; any success clears
	// it. Login/challenge errors are informational and do not kill the link.
	if err := s.applyMigration(context.Background(), indexarr2SeedersCheckedVersion,
		"seeders refresh timestamp", func(tx *sql.Tx) error {
			_, err := tx.Exec(`ALTER TABLE release_sources ADD COLUMN seeders_checked_at INTEGER NOT NULL DEFAULT 0`)
			return err
		}); err != nil {
		return err
	}
	// Migration 12 records download-attempt health per source. A repeated
	// 404 quarantines that source with a growing backoff; any success clears
	// it. Login/challenge errors are informational and do not kill the link.
	if err := s.applyMigration(context.Background(), indexarr2DownloadStatusVersion,
		"download status and quarantine", func(tx *sql.Tx) error {
			for _, statement := range []string{
				`ALTER TABLE release_sources ADD COLUMN download_failures INTEGER NOT NULL DEFAULT 0`,
				`ALTER TABLE release_sources ADD COLUMN quarantined_until INTEGER NOT NULL DEFAULT 0`,
				`ALTER TABLE release_sources ADD COLUMN last_download_error TEXT NOT NULL DEFAULT ''`,
			} {
				if _, err := tx.Exec(statement); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
		return err
	}
	// Migration 14 stores the indexer list of an application before every
	// change, so the App page can restore a previous state, and adds the tag
	// filter that decides which indexarr indexers belong to each app. It is
	// additive: existing applications keep an empty filter, which means "all".
	if err := s.applyMigration(context.Background(), indexarr2AppSnapshotsVersion,
		"app snapshots and tag filters", func(tx *sql.Tx) error {
			for _, statement := range []string{
				`ALTER TABLE applications ADD COLUMN tag_filter TEXT NOT NULL DEFAULT ''`,
				`CREATE TABLE IF NOT EXISTS app_snapshots (
					id            INTEGER PRIMARY KEY AUTOINCREMENT,
					app_id        INTEGER NOT NULL REFERENCES applications(id) ON DELETE CASCADE,
					created_at    INTEGER NOT NULL DEFAULT 0,
					reason        TEXT    NOT NULL DEFAULT 'sync'
						CHECK (reason IN ('sync', 'manual', 'before-restore')),
					indexer_count INTEGER NOT NULL DEFAULT 0,
					payload       TEXT    NOT NULL DEFAULT '[]'
				)`,
				`CREATE INDEX IF NOT EXISTS app_snapshots_app
					ON app_snapshots(app_id, created_at DESC, id DESC)`,
			} {
				if _, err := tx.Exec(statement); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
		return err
	}
	// Migration 15 adds the notification state: sealed settings secrets, the
	// anti-spam ledger and the last notified run state of every indexer. The
	// tables are additive; existing rows are untouched.
	if err := s.applyMigration(context.Background(), indexarr2NotifyVersion,
		"notification secrets, anti-spam and state", func(tx *sql.Tx) error {
			for _, statement := range []string{
				`CREATE TABLE IF NOT EXISTS app_secrets (
					key        TEXT PRIMARY KEY,
					nonce      BLOB NOT NULL,
					ciphertext BLOB NOT NULL,
					updated_at INTEGER NOT NULL DEFAULT 0
				)`,
				`CREATE TABLE IF NOT EXISTS notify_events (
					kind         TEXT NOT NULL,
					source       TEXT NOT NULL,
					last_sent_at INTEGER NOT NULL DEFAULT 0,
					PRIMARY KEY (kind, source)
				)`,
				`CREATE TABLE IF NOT EXISTS indexer_notify_state (
					indexer_id INTEGER PRIMARY KEY REFERENCES indexers(id) ON DELETE CASCADE,
					state      TEXT    NOT NULL DEFAULT '',
					updated_at INTEGER NOT NULL DEFAULT 0
				)`,
			} {
				if _, err := tx.Exec(statement); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
		return err
	}
	// These indexes depend on columns added by ensureReleaseColumns, and the
	// release table may have been rebuilt by migration 3, so they are created
	// after the numbered migrations.
	for _, statement := range []string{
		`CREATE INDEX IF NOT EXISTS releases_hidden ON releases(hidden)`,
		`CREATE INDEX IF NOT EXISTS releases_favorite ON releases(favorite)`,
		`CREATE INDEX IF NOT EXISTS releases_visible_published_id
			ON releases(hidden, published_at DESC, id DESC)`,
		`CREATE INDEX IF NOT EXISTS releases_visible_seen_id
			ON releases(hidden, last_seen_at DESC, id DESC)`,
		`CREATE INDEX IF NOT EXISTS releases_visible_category_published
			ON releases(hidden, category, published_at DESC, id DESC)`,
		// Superseded by the *_id variants, which also order the id tie-break.
		`DROP INDEX IF EXISTS releases_visible_published`,
		`DROP INDEX IF EXISTS releases_visible_seen`,
	} {
		if _, err := s.db.Exec(statement); err != nil {
			return fmt.Errorf("migrate index: %w", err)
		}
	}
	if err := s.migrateFTS(); err != nil {
		return err
	}
	return nil
}

// dropLegacySources migrates a pre-WP16 database to the native-only schema.
// The production database is disposable, so cached releases and run history
// that depend on the legacy model are dropped rather than converted. A fresh
// database has none of these objects and the migration is a no-op.
func (s *Store) dropLegacySources(tx *sql.Tx) error {
	hasTable := func(name string) (bool, error) {
		var count int
		err := tx.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&count)
		return count > 0, err
	}
	hasColumn := func(table, column string) (bool, error) {
		rows, err := tx.Query(`PRAGMA table_info(` + table + `)`)
		if err != nil {
			return false, err
		}
		defer rows.Close()
		for rows.Next() {
			var (
				cid        int
				name       string
				ctype      string
				notNull    int
				defaultV   sql.NullString
				primaryKey int
			)
			if err := rows.Scan(&cid, &name, &ctype, &notNull, &defaultV, &primaryKey); err != nil {
				return false, err
			}
			if strings.EqualFold(name, column) {
				return true, nil
			}
		}
		return false, rows.Err()
	}

	if has, err := hasColumn("indexers", "legacy_source_id"); err != nil {
		return err
	} else if has {
		if _, err := tx.Exec(`DELETE FROM indexers WHERE legacy_source_id IS NOT NULL`); err != nil {
			return fmt.Errorf("delete legacy bridge indexers: %w", err)
		}
		if _, err := tx.Exec(`DROP INDEX IF EXISTS indexers_legacy_source`); err != nil {
			return err
		}
		if _, err := tx.Exec(`ALTER TABLE indexers DROP COLUMN legacy_source_id`); err != nil {
			return fmt.Errorf("drop indexers.legacy_source_id: %w", err)
		}
	}
	if _, err := tx.Exec(`DELETE FROM definitions
		WHERE origin = 'local' AND author = 'migration'
		  AND id NOT IN (SELECT DISTINCT definition_id FROM indexers)`); err != nil {
		return fmt.Errorf("delete legacy definitions: %w", err)
	}

	releaseHasSource, err := hasColumn("releases", "source_id")
	if err != nil {
		return err
	}
	if !releaseHasSource {
		if has, err := hasTable("runs"); err != nil {
			return err
		} else if !has {
			if has, err := hasTable("sources"); err != nil {
				return err
			} else if !has {
				return nil
			}
		}
	}

	// Drop the children of releases before rebuilding the parent.
	for _, statement := range []string{
		`DROP TABLE IF EXISTS release_sources`,
		`DROP TRIGGER IF EXISTS releases_fts_ai`,
		`DROP TRIGGER IF EXISTS releases_fts_ad`,
		`DROP TRIGGER IF EXISTS releases_fts_au`,
		`DROP TABLE IF EXISTS releases_fts`,
		`DROP TABLE IF EXISTS runs`,
	} {
		if _, err := tx.Exec(statement); err != nil {
			return err
		}
	}

	if releaseHasSource {
		if _, err := tx.Exec(`CREATE TABLE releases_new (
			id                 INTEGER PRIMARY KEY AUTOINCREMENT,
			dedup_key          TEXT    NOT NULL UNIQUE,
			infohash           TEXT    NOT NULL DEFAULT '',
			imdb_id            TEXT    NOT NULL DEFAULT '',
			tmdb_id            TEXT    NOT NULL DEFAULT '',
			tvdb_id            TEXT    NOT NULL DEFAULT '',
			title              TEXT    NOT NULL,
			search_title       TEXT    NOT NULL DEFAULT '',
			size               INTEGER NOT NULL DEFAULT 0,
			seeders            INTEGER NOT NULL DEFAULT 0,
			leechers           INTEGER NOT NULL DEFAULT 0,
			category           TEXT    NOT NULL DEFAULT '',
			published_at       INTEGER NOT NULL DEFAULT 0,
			guid               TEXT    NOT NULL DEFAULT '',
			magnet             TEXT    NOT NULL DEFAULT '',
			torrent            BLOB,
			torrent_size       INTEGER NOT NULL DEFAULT 0,
			torrent_fetched_at INTEGER NOT NULL DEFAULT 0,
			first_seen_at      INTEGER NOT NULL DEFAULT 0,
			last_seen_at       INTEGER NOT NULL DEFAULT 0,
			times_seen         INTEGER NOT NULL DEFAULT 1,
			favorite           INTEGER NOT NULL DEFAULT 0,
			hidden             INTEGER NOT NULL DEFAULT 0,
			protocol           TEXT    NOT NULL DEFAULT 'torrent',
			payload            BLOB,
			payload_type       TEXT    NOT NULL DEFAULT '',
			downloaded_at      INTEGER NOT NULL DEFAULT 0
		)`); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO releases_new(
			id, dedup_key, infohash, imdb_id, tmdb_id, tvdb_id, title, search_title,
			size, seeders, leechers, category, published_at, guid, magnet, torrent,
			torrent_size, torrent_fetched_at, first_seen_at, last_seen_at, times_seen,
			favorite, hidden, protocol, payload, payload_type)
			SELECT id, dedup_key, infohash, imdb_id, tmdb_id, tvdb_id, title, search_title,
			size, seeders, leechers, category, published_at, guid, magnet, torrent,
			torrent_size, torrent_fetched_at, first_seen_at, last_seen_at, times_seen,
			favorite, hidden, protocol, payload, payload_type
			FROM releases`); err != nil {
			return fmt.Errorf("copy releases: %w", err)
		}
		if _, err := tx.Exec(`DROP TABLE releases`); err != nil {
			return err
		}
		if _, err := tx.Exec(`ALTER TABLE releases_new RENAME TO releases`); err != nil {
			return err
		}
	}

	if _, err := tx.Exec(`DROP TABLE IF EXISTS sources`); err != nil {
		return err
	}
	// Recreate the provenance table dropped above; it now references only the
	// native indexers.
	for _, statement := range []string{
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
	} {
		if _, err := tx.Exec(statement); err != nil {
			return err
		}
	}
	return nil
}

// migrateFTS creates the full-text index and its triggers. The trigram
// tokenizer is used because indexarr matches substrings inside normalized
// titles, not only whole words. The index is external-content, so titles are
// not duplicated; triggers keep it in sync with every write.
func (s *Store) migrateFTS() error {
	var exists int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master
		WHERE type = 'table' AND name = 'releases_fts'`).Scan(&exists); err != nil {
		return fmt.Errorf("check FTS table: %w", err)
	}
	if exists == 0 {
		if _, err := s.db.Exec(`CREATE VIRTUAL TABLE releases_fts USING fts5(
			search_title,
			content = 'releases',
			content_rowid = 'id',
			tokenize = 'trigram'
		)`); err != nil {
			return fmt.Errorf("create FTS table: %w", err)
		}
		if _, err := s.db.Exec(`INSERT INTO releases_fts(releases_fts) VALUES('rebuild')`); err != nil {
			return fmt.Errorf("rebuild FTS index: %w", err)
		}
	}
	// The B-tree on search_title cannot serve leading-wildcard LIKE queries;
	// the trigram index above replaces it.
	if _, err := s.db.Exec(`DROP INDEX IF EXISTS releases_search`); err != nil {
		return fmt.Errorf("drop legacy search index: %w", err)
	}
	for _, trigger := range []string{
		`CREATE TRIGGER IF NOT EXISTS releases_fts_ai AFTER INSERT ON releases BEGIN
			INSERT INTO releases_fts(rowid, search_title) VALUES (new.id, new.search_title);
		END`,
		`CREATE TRIGGER IF NOT EXISTS releases_fts_ad AFTER DELETE ON releases BEGIN
			INSERT INTO releases_fts(releases_fts, rowid, search_title)
			VALUES ('delete', old.id, old.search_title);
		END`,
		`CREATE TRIGGER IF NOT EXISTS releases_fts_au AFTER UPDATE OF search_title ON releases
		WHEN old.search_title <> new.search_title BEGIN
			INSERT INTO releases_fts(releases_fts, rowid, search_title)
			VALUES ('delete', old.id, old.search_title);
			INSERT INTO releases_fts(rowid, search_title) VALUES (new.id, new.search_title);
		END`,
	} {
		if _, err := s.db.Exec(trigger); err != nil {
			return fmt.Errorf("create FTS trigger: %w", err)
		}
	}
	return nil
}

// ensureReleaseColumns adds the curation and favorite columns to a database
// created by an older version.
func (s *Store) ensureReleaseColumns() error {
	for _, column := range []struct {
		name string
		ddl  string
	}{
		{"favorite", "INTEGER NOT NULL DEFAULT 0"},
		{"hidden", "INTEGER NOT NULL DEFAULT 0"},
		{"downloaded_at", "INTEGER NOT NULL DEFAULT 0"},
	} {
		exists, err := s.columnExists("releases", column.name)
		if err != nil {
			return err
		}
		if exists {
			continue
		}
		if _, err := s.db.Exec("ALTER TABLE releases ADD COLUMN " + column.name + " " + column.ddl); err != nil {
			return fmt.Errorf("migrate releases.%s: %w", column.name, err)
		}
	}
	return nil
}

// boolInt stores a Go bool as the 0/1 integer SQLite expects.
func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

// unixTime converts a stored Unix timestamp to a time. Zero stays the zero
// time so callers can distinguish "never" from the epoch.
func unixTime(value int64) time.Time {
	if value <= 0 {
		return time.Time{}
	}
	return time.Unix(value, 0)
}

func (s *Store) columnExists(table, column string) (bool, error) {
	rows, err := s.db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			cid        int
			name       string
			ctype      string
			notNull    int
			defaultV   sql.NullString
			primaryKey int
		)
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &defaultV, &primaryKey); err != nil {
			return false, err
		}
		if strings.EqualFold(name, column) {
			return true, nil
		}
	}
	return false, rows.Err()
}

// SizeBytes reports the database size including the write-ahead log.
func (s *Store) SizeBytes() (int64, error) {
	var total int64
	for _, suffix := range []string{"", "-wal", "-shm"} {
		info, err := os.Stat(s.path + suffix)
		if err == nil {
			total += info.Size()
			continue
		}
		if !errors.Is(err, os.ErrNotExist) {
			return total, err
		}
	}
	return total, nil
}

// Vacuum rebuilds the database file, reclaiming free pages.
func (s *Store) Vacuum(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, "VACUUM")
	if err != nil {
		return fmt.Errorf("vacuum: %w", err)
	}
	return nil
}

// IntegrityCheck runs SQLite's full integrity check and returns its verdict.
func (s *Store) IntegrityCheck(ctx context.Context) (string, error) {
	var result string
	if err := s.db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&result); err != nil {
		return "", fmt.Errorf("integrity check: %w", err)
	}
	return result, nil
}

// BackupTo writes a consistent copy of the database to target. The caller owns
// the file and must remove it after serving it.
func (s *Store) BackupTo(ctx context.Context, target string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	_ = os.Remove(target)
	quoted := strings.ReplaceAll(target, "'", "''")
	if _, err := s.db.ExecContext(ctx, "VACUUM INTO '"+quoted+"'"); err != nil {
		return fmt.Errorf("backup: %w", err)
	}
	return nil
}

// migrateLiveWaitDefault raises the old implicit 1/2 second live-search wait to
// the new 5 second default once. A later explicit user choice is left alone.
func (s *Store) migrateLiveWaitDefault(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var marker string
	err = tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = 'torznab_live_wait_default_v5'`).Scan(&marker)
	if err == nil {
		return nil
	}
	if err != sql.ErrNoRows {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE settings SET value = '5' WHERE key = 'torznab_live_wait_seconds' AND value IN ('1','2')`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO settings(key, value) VALUES('torznab_live_wait_default_v5','1')`); err != nil {
		return err
	}
	return tx.Commit()
}
