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

	_ "modernc.org/sqlite"
)

// Store owns the SQLite database. A single connection is kept, as the pure-Go
// driver is serialised per connection and indexarr mixes a web UI with
// background writers.
type Store struct {
	db   *sql.DB
	path string
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
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
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
		`CREATE TABLE IF NOT EXISTS sources (
			id               INTEGER PRIMARY KEY AUTOINCREMENT,
			name             TEXT    NOT NULL UNIQUE,
			kind             TEXT    NOT NULL DEFAULT 'torznab',
			manager          TEXT    NOT NULL DEFAULT 'auto',
			url              TEXT    NOT NULL,
			api_key          TEXT    NOT NULL DEFAULT '',
			username         TEXT    NOT NULL DEFAULT '',
			password         TEXT    NOT NULL DEFAULT '',
			categories       TEXT    NOT NULL DEFAULT '',
			query            TEXT    NOT NULL DEFAULT '',
			enabled          INTEGER NOT NULL DEFAULT 1,
			interval_seconds INTEGER NOT NULL DEFAULT 0,
			last_run_at      INTEGER NOT NULL DEFAULT 0,
			last_ok_at       INTEGER NOT NULL DEFAULT 0,
			last_error       TEXT    NOT NULL DEFAULT '',
			last_duration_ms INTEGER NOT NULL DEFAULT 0,
			last_items       INTEGER NOT NULL DEFAULT 0,
			total_items      INTEGER NOT NULL DEFAULT 0,
			created_at       INTEGER NOT NULL DEFAULT 0,
			updated_at       INTEGER NOT NULL DEFAULT 0,
			consecutive_errors INTEGER NOT NULL DEFAULT 0,
			default_category TEXT NOT NULL DEFAULT ''
		)`,
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
			source_id          INTEGER REFERENCES sources(id) ON DELETE SET NULL,
			source_name        TEXT    NOT NULL DEFAULT '',
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
		`CREATE TABLE IF NOT EXISTS runs (
			id              INTEGER PRIMARY KEY AUTOINCREMENT,
			source_id       INTEGER REFERENCES sources(id) ON DELETE SET NULL,
			source_name     TEXT    NOT NULL DEFAULT '',
			started_at      INTEGER NOT NULL DEFAULT 0,
			finished_at     INTEGER NOT NULL DEFAULT 0,
			status          TEXT    NOT NULL DEFAULT 'running',
			items_seen      INTEGER NOT NULL DEFAULT 0,
			items_new       INTEGER NOT NULL DEFAULT 0,
			items_updated   INTEGER NOT NULL DEFAULT 0,
			items_blocked   INTEGER NOT NULL DEFAULT 0,
			torrents_fetched INTEGER NOT NULL DEFAULT 0,
			error           TEXT    NOT NULL DEFAULT '',
			duration_ms     INTEGER NOT NULL DEFAULT 0
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
		`CREATE INDEX IF NOT EXISTS releases_source ON releases(source_id)`,
		`CREATE INDEX IF NOT EXISTS releases_category ON releases(category)`,
		`CREATE INDEX IF NOT EXISTS releases_last_seen ON releases(last_seen_at)`,
		`CREATE INDEX IF NOT EXISTS releases_imdb ON releases(imdb_id)`,
		`CREATE INDEX IF NOT EXISTS releases_tmdb ON releases(tmdb_id)`,
		`CREATE INDEX IF NOT EXISTS releases_tvdb ON releases(tvdb_id)`,
		`CREATE INDEX IF NOT EXISTS runs_started ON runs(started_at DESC)`,
		`CREATE INDEX IF NOT EXISTS runs_source ON runs(source_id)`,
	}
	for _, statement := range statements {
		if _, err := s.db.Exec(statement); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}
	if err := s.ensureSourceColumns(); err != nil {
		return err
	}
	if err := s.ensureReleaseColumns(); err != nil {
		return err
	}
	if err := s.ensureRunColumns(); err != nil {
		return err
	}
	// These indexes depend on columns added by ensureReleaseColumns, so they
	// cannot live in the statements above: an existing database does not have
	// hidden/favorite until the ALTER TABLE has run.
	for _, statement := range []string{
		`CREATE INDEX IF NOT EXISTS releases_hidden ON releases(hidden)`,
		`CREATE INDEX IF NOT EXISTS releases_favorite ON releases(favorite)`,
		`CREATE INDEX IF NOT EXISTS releases_visible_published ON releases(hidden, published_at DESC)`,
		`CREATE INDEX IF NOT EXISTS releases_visible_seen ON releases(hidden, last_seen_at DESC)`,
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

// ensureRunColumns adds the blocked counter to a database created by an older
// version.
func (s *Store) ensureRunColumns() error {
	exists, err := s.columnExists("runs", "items_blocked")
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	if _, err := s.db.Exec("ALTER TABLE runs ADD COLUMN items_blocked INTEGER NOT NULL DEFAULT 0"); err != nil {
		return fmt.Errorf("migrate runs.items_blocked: %w", err)
	}
	return nil
}

// ensureSourceColumns adds the HTTP Basic columns to a database created by an
// older version. New databases get them from CREATE TABLE.
func (s *Store) ensureSourceColumns() error {
	for _, column := range []struct {
		name string
		ddl  string
	}{
		{"username", "TEXT NOT NULL DEFAULT ''"},
		{"password", "TEXT NOT NULL DEFAULT ''"},
		{"consecutive_errors", "INTEGER NOT NULL DEFAULT 0"},
		{"default_category", "TEXT NOT NULL DEFAULT ''"},
	} {
		exists, err := s.columnExists("sources", column.name)
		if err != nil {
			return err
		}
		if exists {
			continue
		}
		if _, err := s.db.Exec("ALTER TABLE sources ADD COLUMN " + column.name + " " + column.ddl); err != nil {
			return fmt.Errorf("migrate sources.%s: %w", column.name, err)
		}
	}
	return nil
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
