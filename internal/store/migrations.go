package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/buzzqw/indexarr/internal/textutil"
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
	// indexarr2RemoteOriginVersion widens the definitions.origin CHECK to
	// 'remote', the origin of a definition read from a catalog address: the
	// embedded catalog must not overwrite it, otherwise an update without a
	// new release would be undone by the next start. SQLite cannot alter a
	// CHECK constraint in place, so the table is rebuilt.
	indexarr2RemoteOriginVersion = 24
	// indexarr2TitleMojibakeVersion repairs the release titles a site handed
	// out with the UTF-8 bytes read as Latin-1/CP1252 (LimeTorrents does it):
	// the bytes are all there, only read with the wrong charset.
	indexarr2TitleMojibakeVersion = 25
	// indexarr2GrabConfirmationVersion stores the completion a download client
	// reports for a grab. The grab row keeps the delivery time; confirmed_at
	// and confirmed_by add the evidence that the file really arrived.
	indexarr2GrabConfirmationVersion = 26
	// indexarr2TopicIndexVersion keeps the local history of a forum's topics
	// (threads and series) with one cursor per forum, so the walk resumes
	// after a restart and a search can answer from it.
	indexarr2TopicIndexVersion = 27
	// latestSchemaVersion is the newest numbered migration. Tests use it
	// instead of pinning a version so a new migration does not break them.
	latestSchemaVersion = indexarr2TopicIndexVersion
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

// applyRemoteOriginMigration rebuilds the definitions table so its origin
// CHECK accepts 'remote'. It follows applyEd2kProtocolMigration: one dedicated
// connection, foreign keys disabled, a new table filled from the old one and
// renamed over it, so the indexers that reference definitions keep pointing at
// the same rows.
func (s *Store) applyRemoteOriginMigration(ctx context.Context) error {
	applied, err := s.migrationApplied(ctx, indexarr2RemoteOriginVersion)
	if err != nil {
		return err
	}
	if applied {
		return nil
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("begin migration %d: %w", indexarr2RemoteOriginVersion, err)
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
		return fmt.Errorf("begin migration %d: %w", indexarr2RemoteOriginVersion, err)
	}
	defer tx.Rollback()

	statements := []string{
		`CREATE TABLE definitions_new (
			id             TEXT PRIMARY KEY,
			schema_version INTEGER NOT NULL DEFAULT 1,
			family         TEXT    NOT NULL DEFAULT '',
			state          TEXT    NOT NULL DEFAULT 'bozza'
				CHECK (state IN ('bozza', 'verificata', 'stabile', 'degradata')),
			origin         TEXT    NOT NULL DEFAULT 'local'
				CHECK (origin IN ('builtin', 'catalog', 'local', 'remote')),
			author         TEXT    NOT NULL DEFAULT '',
			body           TEXT    NOT NULL,
			hash           TEXT    NOT NULL,
			updated_at     INTEGER NOT NULL DEFAULT 0
		)`,
		`INSERT INTO definitions_new(id, schema_version, family, state, origin,
			author, body, hash, updated_at)
		 SELECT id, schema_version, family, state, origin, author, body, hash,
			updated_at FROM definitions`,
		`DROP TABLE definitions`,
		`ALTER TABLE definitions_new RENAME TO definitions`,
		`CREATE INDEX IF NOT EXISTS definitions_family ON definitions(family)`,
		`CREATE INDEX IF NOT EXISTS definitions_state ON definitions(state)`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("migration %d (%s): %w", indexarr2RemoteOriginVersion, "remote origin", err)
		}
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO schema_migrations(version, name, applied_at) VALUES(?, ?, ?)`,
		indexarr2RemoteOriginVersion, "remote origin", time.Now().Unix()); err != nil {
		return fmt.Errorf("record migration %d: %w", indexarr2RemoteOriginVersion, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %d: %w", indexarr2RemoteOriginVersion, err)
	}
	return nil
}

// repairDoubleEncodedTitles rewrites the release titles a site served with the
// wrong charset, and the search title and magnet name built from them, so a
// client that adds the release by magnet shows the readable name before the
// metadata arrives. Only a row the repair actually changes is touched; a row
// whose dedup key was built from its title is left alone, because repairing the
// title would change that key and the release would come back as a new one on
// the next sighting.
func repairDoubleEncodedTitles(tx *sql.Tx) error {
	rows, err := tx.Query(`
		SELECT id, title, search_title, magnet
		FROM releases
		WHERE dedup_key NOT LIKE 'title:%'
			AND (title GLOB '*[^ -~]*' OR magnet GLOB '*[^ -~]*')`)
	if err != nil {
		return err
	}
	type repair struct {
		id     int64
		title  string
		search string
		magnet string
	}
	var repairs []repair
	for rows.Next() {
		var id int64
		var title, search, magnet string
		if err := rows.Scan(&id, &title, &search, &magnet); err != nil {
			rows.Close()
			return err
		}
		fixed := textutil.RepairDoubleEncoded(title)
		fixedMagnet := repairMagnetName(magnet)
		if fixed == title && fixedMagnet == magnet {
			continue
		}
		if fixed != title && search != "" {
			search = textutil.NormalizeTitle(fixed)
		}
		repairs = append(repairs, repair{
			id:     id,
			title:  fixed,
			search: search,
			magnet: fixedMagnet,
		})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, item := range repairs {
		if _, err := tx.Exec(`UPDATE releases SET title = ?, search_title = ?, magnet = ? WHERE id = ?`,
			item.title, item.search, item.magnet, item.id); err != nil {
			return err
		}
	}
	return nil
}

// repairMagnetName repairs the display name of a magnet link when the name
// itself is double encoded, and leaves every other parameter byte for byte as
// it is. The name is compared with nothing: a site's magnet often keeps the
// spacing the listing collapsed, so the name is repaired on its own.
func repairMagnetName(magnet string) string {
	lower := strings.ToLower(magnet)
	if !strings.HasPrefix(lower, "magnet:") {
		return magnet
	}
	for _, prefix := range []string{"?dn=", "&dn="} {
		at := strings.Index(lower, prefix)
		if at < 0 {
			continue
		}
		start := at + len(prefix)
		value := magnet[start:]
		if end := strings.IndexByte(value, '&'); end >= 0 {
			value = value[:end]
		}
		decoded, err := url.QueryUnescape(value)
		if err != nil {
			return magnet
		}
		fixed := textutil.RepairDoubleEncoded(decoded)
		if fixed == decoded {
			return magnet
		}
		return magnet[:start] + url.QueryEscape(fixed) + magnet[start+len(value):]
	}
	return magnet
}
