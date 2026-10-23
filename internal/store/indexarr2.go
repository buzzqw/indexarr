package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Definition is a native or catalog indexer definition stored as YAML text.
type Definition struct {
	ID            string
	SchemaVersion int
	Family        string
	State         string
	Origin        string
	Author        string
	Body          string
	Hash          string
	UpdatedAt     time.Time
}

// Indexer is one configured instance of a definition.
type Indexer struct {
	ID             int64
	DefinitionID   string
	Protocol       string
	Slug           string
	Name           string
	Enabled        bool
	Priority       int
	ConfigJSON     string
	Tags           string
	ProxyID        *int64
	QueryLimit     int
	GrabLimit      int
	LegacySourceID int64
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// Secret is one vault-sealed value belonging to an indexer.
type Secret struct {
	IndexerID  int64
	Key        string
	Nonce      []byte
	Ciphertext []byte
	UpdatedAt  time.Time
}

// IndexerState holds mutable runtime state for an indexer.
type IndexerState struct {
	IndexerID     int64
	CookiesEnc    []byte
	LastLoginAt   time.Time
	Circuit       string
	DisabledUntil time.Time
	LastError     string
	UpdatedAt     time.Time
}

// ReleaseSource records which indexer supplied a release.
type ReleaseSource struct {
	ReleaseID   int64
	IndexerID   int64
	FirstSeenAt time.Time
	LastSeenAt  time.Time
	Seeders     int64
	Leechers    int64
	DownloadRef string
}

// IndexerHistory is one search or sync attempt.
type IndexerHistory struct {
	ID        int64
	IndexerID int64
	Kind      string
	Query     string
	Status    string
	LatencyMS int64
	Bytes     int64
	Error     string
	App       string
	StartedAt time.Time
}

// Application is a downstream *arr instance.
type Application struct {
	ID         int64
	Kind       string
	Name       string
	URL        string
	APIKeyEnc  []byte
	SyncLevel  string
	LastSyncAt time.Time
	LastError  string
}

// AppIndexer links an application to one indexer it knows about.
type AppIndexer struct {
	ApplicationID int64
	IndexerID     int64
	RemoteID      string
	SyncedHash    string
}

// Proxy is an optional outbound proxy configuration.
type Proxy struct {
	ID         int64
	Kind       string
	URL        string
	SecretsEnc []byte
}

// dbExecer is implemented by both *sql.DB and *sql.Tx, so the same statements
// can run standalone or inside the backfill transaction.
type dbExecer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

const definitionColumns = `id, schema_version, family, state, origin, author,
	body, hash, updated_at`

const indexerColumns = `id, definition_id, protocol, slug, name, enabled, priority,
	config_json, tags, proxy_id, query_limit, grab_limit, legacy_source_id,
	created_at, updated_at`

// UpsertDefinition inserts or replaces a definition by id.
func (s *Store) UpsertDefinition(ctx context.Context, d Definition) error {
	if err := upsertDefinition(ctx, s.db, d); err != nil {
		return fmt.Errorf("upsert definition %s: %w", d.ID, err)
	}
	return nil
}

func upsertDefinition(ctx context.Context, db dbExecer, d Definition) error {
	if strings.TrimSpace(d.ID) == "" {
		return errors.New("empty definition id")
	}
	if d.UpdatedAt.IsZero() {
		d.UpdatedAt = time.Now()
	}
	_, err := db.ExecContext(ctx, `
		INSERT INTO definitions(id, schema_version, family, state, origin, author,
			body, hash, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			schema_version = excluded.schema_version,
			family = excluded.family,
			state = excluded.state,
			origin = excluded.origin,
			author = excluded.author,
			body = excluded.body,
			hash = excluded.hash,
			updated_at = excluded.updated_at`,
		d.ID, d.SchemaVersion, d.Family, d.State, d.Origin, d.Author,
		d.Body, d.Hash, d.UpdatedAt.Unix())
	return err
}

// GetDefinition loads one definition by id.
func (s *Store) GetDefinition(ctx context.Context, id string) (Definition, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+definitionColumns+` FROM definitions WHERE id = ?`, id)
	out, err := scanDefinition(row)
	if err != nil {
		if err == sql.ErrNoRows {
			return Definition{}, fmt.Errorf("definition %s: %w", id, sql.ErrNoRows)
		}
		return Definition{}, err
	}
	return out, nil
}

// ListDefinitions returns every definition ordered by id.
func (s *Store) ListDefinitions(ctx context.Context) ([]Definition, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+definitionColumns+` FROM definitions ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Definition
	for rows.Next() {
		d, err := scanDefinition(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func scanDefinition(scanner interface{ Scan(...any) error }) (Definition, error) {
	var out Definition
	var updated int64
	if err := scanner.Scan(&out.ID, &out.SchemaVersion, &out.Family, &out.State,
		&out.Origin, &out.Author, &out.Body, &out.Hash, &updated); err != nil {
		return Definition{}, err
	}
	out.UpdatedAt = unixTime(updated)
	return out, nil
}

// CreateIndexer inserts an indexer and returns its id.
func (s *Store) CreateIndexer(ctx context.Context, in Indexer) (int64, error) {
	return createIndexer(ctx, s.db, in)
}

func createIndexer(ctx context.Context, db dbExecer, in Indexer) (int64, error) {
	if in.Protocol == "" {
		in.Protocol = "torrent"
	}
	if in.Priority == 0 {
		in.Priority = 25
	}
	if strings.TrimSpace(in.ConfigJSON) == "" {
		in.ConfigJSON = "{}"
	}
	now := time.Now()
	created := in.CreatedAt
	if created.IsZero() {
		created = now
	}
	updated := in.UpdatedAt
	if updated.IsZero() {
		updated = now
	}
	var proxy any
	if in.ProxyID != nil {
		proxy = *in.ProxyID
	}
	var legacy any
	if in.LegacySourceID > 0 {
		legacy = in.LegacySourceID
	}
	var id int64
	err := db.QueryRowContext(ctx, `
		INSERT INTO indexers(definition_id, protocol, slug, name, enabled, priority,
			config_json, proxy_id, tags, query_limit, grab_limit, legacy_source_id,
			created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		RETURNING id`,
		in.DefinitionID, in.Protocol, strings.TrimSpace(in.Slug), strings.TrimSpace(in.Name),
		boolInt(in.Enabled), in.Priority, in.ConfigJSON, proxy, in.Tags,
		in.QueryLimit, in.GrabLimit, legacy, created.Unix(), updated.Unix()).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("create indexer: %w", err)
	}
	return id, nil
}

// GetIndexer loads one indexer by id.
func (s *Store) GetIndexer(ctx context.Context, id int64) (Indexer, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+indexerColumns+` FROM indexers WHERE id = ?`, id)
	out, err := scanIndexer(row)
	if err != nil {
		if err == sql.ErrNoRows {
			return Indexer{}, fmt.Errorf("indexer %d: %w", id, sql.ErrNoRows)
		}
		return Indexer{}, err
	}
	return out, nil
}

// GetIndexerBySlug loads one indexer by its unique slug.
func (s *Store) GetIndexerBySlug(ctx context.Context, slug string) (Indexer, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+indexerColumns+` FROM indexers WHERE slug = ?`, slug)
	out, err := scanIndexer(row)
	if err != nil {
		if err == sql.ErrNoRows {
			return Indexer{}, fmt.Errorf("indexer %q: %w", slug, sql.ErrNoRows)
		}
		return Indexer{}, err
	}
	return out, nil
}

// ListIndexers returns every indexer ordered by name.
func (s *Store) ListIndexers(ctx context.Context) ([]Indexer, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+indexerColumns+` FROM indexers ORDER BY name COLLATE NOCASE, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Indexer
	for rows.Next() {
		in, err := scanIndexer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, in)
	}
	return out, rows.Err()
}

func scanIndexer(scanner interface{ Scan(...any) error }) (Indexer, error) {
	var out Indexer
	var enabled int
	var proxy, legacy sql.NullInt64
	var created, updated int64
	if err := scanner.Scan(&out.ID, &out.DefinitionID, &out.Protocol, &out.Slug,
		&out.Name, &enabled, &out.Priority, &out.ConfigJSON, &out.Tags, &proxy,
		&out.QueryLimit, &out.GrabLimit, &legacy, &created, &updated); err != nil {
		return Indexer{}, err
	}
	out.Enabled = enabled != 0
	if proxy.Valid {
		id := proxy.Int64
		out.ProxyID = &id
	}
	if legacy.Valid {
		out.LegacySourceID = legacy.Int64
	}
	out.CreatedAt = unixTime(created)
	out.UpdatedAt = unixTime(updated)
	return out, nil
}

// UpdateIndexer saves the editable fields of an indexer.
func (s *Store) UpdateIndexer(ctx context.Context, in Indexer) error {
	if in.Protocol == "" {
		in.Protocol = "torrent"
	}
	if strings.TrimSpace(in.ConfigJSON) == "" {
		in.ConfigJSON = "{}"
	}
	var proxy any
	if in.ProxyID != nil {
		proxy = *in.ProxyID
	}
	var legacy any
	if in.LegacySourceID > 0 {
		legacy = in.LegacySourceID
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE indexers SET definition_id = ?, protocol = ?, slug = ?, name = ?,
			enabled = ?, priority = ?, config_json = ?, proxy_id = ?, tags = ?,
			query_limit = ?, grab_limit = ?, legacy_source_id = ?, updated_at = ?
		WHERE id = ?`,
		in.DefinitionID, in.Protocol, strings.TrimSpace(in.Slug), strings.TrimSpace(in.Name),
		boolInt(in.Enabled), in.Priority, in.ConfigJSON, proxy, in.Tags,
		in.QueryLimit, in.GrabLimit, legacy, time.Now().Unix(), in.ID)
	if err != nil {
		return fmt.Errorf("update indexer %d: %w", in.ID, err)
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return fmt.Errorf("indexer %d: %w", in.ID, sql.ErrNoRows)
	}
	return nil
}

// DeleteIndexer removes an indexer. Secrets, state and provenance rows
// cascade; history keeps its rows with a NULL indexer_id.
func (s *Store) DeleteIndexer(ctx context.Context, id int64) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM indexers WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete indexer %d: %w", id, err)
	}
	return nil
}

// PutSecret inserts or replaces one encrypted secret for an indexer.
func (s *Store) PutSecret(ctx context.Context, in Secret) error {
	if in.UpdatedAt.IsZero() {
		in.UpdatedAt = time.Now()
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO indexer_secrets(indexer_id, key, nonce, ciphertext, updated_at)
		VALUES(?, ?, ?, ?, ?)
		ON CONFLICT(indexer_id, key) DO UPDATE SET
			nonce = excluded.nonce,
			ciphertext = excluded.ciphertext,
			updated_at = excluded.updated_at`,
		in.IndexerID, in.Key, in.Nonce, in.Ciphertext, in.UpdatedAt.Unix())
	if err != nil {
		return fmt.Errorf("put secret %q: %w", in.Key, err)
	}
	return nil
}

// GetSecret loads one secret by indexer and key.
func (s *Store) GetSecret(ctx context.Context, indexerID int64, key string) (Secret, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT indexer_id, key, nonce, ciphertext, updated_at
		FROM indexer_secrets WHERE indexer_id = ? AND key = ?`, indexerID, key)
	out, err := scanSecret(row)
	if err != nil {
		if err == sql.ErrNoRows {
			return Secret{}, fmt.Errorf("secret %d/%q: %w", indexerID, key, sql.ErrNoRows)
		}
		return Secret{}, err
	}
	return out, nil
}

// DeleteSecret removes one secret from an indexer. Deleting a missing key is
// not an error.
func (s *Store) DeleteSecret(ctx context.Context, indexerID int64, key string) error {
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM indexer_secrets WHERE indexer_id = ? AND key = ?`,
		indexerID, key); err != nil {
		return fmt.Errorf("delete secret %q: %w", key, err)
	}
	return nil
}

// ListSecrets returns every secret of an indexer ordered by key.
func (s *Store) ListSecrets(ctx context.Context, indexerID int64) ([]Secret, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT indexer_id, key, nonce, ciphertext, updated_at
		FROM indexer_secrets WHERE indexer_id = ? ORDER BY key`, indexerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Secret
	for rows.Next() {
		secret, err := scanSecret(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, secret)
	}
	return out, rows.Err()
}

func scanSecret(scanner interface{ Scan(...any) error }) (Secret, error) {
	var out Secret
	var updated int64
	if err := scanner.Scan(&out.IndexerID, &out.Key, &out.Nonce, &out.Ciphertext, &updated); err != nil {
		return Secret{}, err
	}
	out.UpdatedAt = unixTime(updated)
	return out, nil
}

// UpsertIndexerState inserts or replaces the runtime state of an indexer.
func (s *Store) UpsertIndexerState(ctx context.Context, in IndexerState) error {
	if in.Circuit == "" {
		in.Circuit = "closed"
	}
	if in.UpdatedAt.IsZero() {
		in.UpdatedAt = time.Now()
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO indexer_state(indexer_id, cookies_enc, last_login_at, circuit,
			disabled_until, last_error, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(indexer_id) DO UPDATE SET
			cookies_enc = excluded.cookies_enc,
			last_login_at = excluded.last_login_at,
			circuit = excluded.circuit,
			disabled_until = excluded.disabled_until,
			last_error = excluded.last_error,
			updated_at = excluded.updated_at`,
		in.IndexerID, in.CookiesEnc, in.LastLoginAt.Unix(), in.Circuit,
		in.DisabledUntil.Unix(), in.LastError, in.UpdatedAt.Unix())
	if err != nil {
		return fmt.Errorf("upsert indexer state %d: %w", in.IndexerID, err)
	}
	return nil
}

// GetIndexerState loads the runtime state of an indexer.
func (s *Store) GetIndexerState(ctx context.Context, indexerID int64) (IndexerState, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT indexer_id, cookies_enc, last_login_at, circuit, disabled_until,
			last_error, updated_at
		FROM indexer_state WHERE indexer_id = ?`, indexerID)
	var out IndexerState
	var lastLogin, disabledUntil, updated int64
	err := row.Scan(&out.IndexerID, &out.CookiesEnc, &lastLogin, &out.Circuit,
		&disabledUntil, &out.LastError, &updated)
	if err != nil {
		if err == sql.ErrNoRows {
			return IndexerState{}, fmt.Errorf("indexer state %d: %w", indexerID, sql.ErrNoRows)
		}
		return IndexerState{}, err
	}
	out.LastLoginAt = unixTime(lastLogin)
	out.DisabledUntil = unixTime(disabledUntil)
	out.UpdatedAt = unixTime(updated)
	return out, nil
}

// RecordReleaseSource stores provenance, keeping the original first_seen_at on
// conflict and refreshing last_seen_at and the swarm counters. The parent
// release keeps the best swarm values ever observed: a source with fewer
// seeders never lowers them.
func (s *Store) RecordReleaseSource(ctx context.Context, in ReleaseSource) error {
	now := time.Now()
	first := in.FirstSeenAt
	if first.IsZero() {
		first = now
	}
	last := in.LastSeenAt
	if last.IsZero() {
		last = now
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO release_sources(release_id, indexer_id, first_seen_at,
			last_seen_at, seeders, leechers, download_ref)
		VALUES(?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(release_id, indexer_id) DO UPDATE SET
			last_seen_at = excluded.last_seen_at,
			seeders = excluded.seeders,
			leechers = excluded.leechers,
			download_ref = CASE WHEN excluded.download_ref <> ''
				THEN excluded.download_ref ELSE release_sources.download_ref END`,
		in.ReleaseID, in.IndexerID, first.Unix(), last.Unix(), in.Seeders,
		in.Leechers, in.DownloadRef)
	if err != nil {
		return fmt.Errorf("record release source: %w", err)
	}
	// Keep the cached release row in sync with the healthiest source. The
	// source row itself keeps its own counters, so per-indexer history is not
	// lost.
	if _, err := s.db.ExecContext(ctx, `
		UPDATE releases SET seeders = MAX(seeders, ?), leechers = MAX(leechers, ?),
			last_seen_at = MAX(last_seen_at, ?)
		WHERE id = ?`,
		in.Seeders, in.Leechers, last.Unix(), in.ReleaseID); err != nil {
		return fmt.Errorf("update release swarm counters: %w", err)
	}
	return nil
}

// ListReleaseSources returns the provenance of one release.
func (s *Store) ListReleaseSources(ctx context.Context, releaseID int64) ([]ReleaseSource, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT release_id, indexer_id, first_seen_at, last_seen_at, seeders,
			leechers, download_ref
		FROM release_sources WHERE release_id = ?
		ORDER BY first_seen_at, indexer_id`, releaseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ReleaseSource
	for rows.Next() {
		var item ReleaseSource
		var first, last int64
		if err := rows.Scan(&item.ReleaseID, &item.IndexerID, &first, &last,
			&item.Seeders, &item.Leechers, &item.DownloadRef); err != nil {
			return nil, err
		}
		item.FirstSeenAt = unixTime(first)
		item.LastSeenAt = unixTime(last)
		out = append(out, item)
	}
	return out, rows.Err()
}

// RecordIndexerHistory appends one history row and returns its id.
func (s *Store) RecordIndexerHistory(ctx context.Context, in IndexerHistory) (int64, error) {
	if in.StartedAt.IsZero() {
		in.StartedAt = time.Now()
	}
	var indexer any
	if in.IndexerID > 0 {
		indexer = in.IndexerID
	}
	var id int64
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO indexer_history(indexer_id, kind, query, status, latency_ms,
			bytes, error, app, started_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)
		RETURNING id`,
		indexer, in.Kind, in.Query, in.Status, in.LatencyMS, in.Bytes, in.Error,
		in.App, in.StartedAt.Unix()).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("record indexer history: %w", err)
	}
	return id, nil
}

// CountIndexerHistory counts history rows of the given kinds for one indexer
// that started at or after since. An empty kinds slice counts every kind. It is
// used by the query and grab limits, so the caller decides the time window.
func (s *Store) CountIndexerHistory(ctx context.Context, indexerID int64, kinds []string, since time.Time) (int, error) {
	query := `SELECT COUNT(*) FROM indexer_history WHERE indexer_id = ?`
	args := []any{indexerID}
	if len(kinds) > 0 {
		placeholders := make([]string, len(kinds))
		for i, kind := range kinds {
			placeholders[i] = "?"
			args = append(args, kind)
		}
		query += ` AND kind IN (` + strings.Join(placeholders, ", ") + `)`
	}
	query += ` AND started_at >= ?`
	args = append(args, since.Unix())
	var count int
	if err := s.db.QueryRowContext(ctx, query, args...).Scan(&count); err != nil {
		return 0, fmt.Errorf("count indexer history %d: %w", indexerID, err)
	}
	return count, nil
}

// IndexerForRelease returns the indexer that most recently supplied a release,
// joined through release_sources. A release without provenance reports false
// with a nil error, matching the sql.ErrNoRows convention without exposing it.
func (s *Store) IndexerForRelease(ctx context.Context, releaseID int64) (Indexer, bool, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT `+qualifiedIndexerColumns("i")+`
		FROM release_sources rs
		JOIN indexers i ON i.id = rs.indexer_id
		WHERE rs.release_id = ?
		ORDER BY rs.last_seen_at DESC, rs.indexer_id DESC
		LIMIT 1`, releaseID)
	out, err := scanIndexer(row)
	if err != nil {
		if err == sql.ErrNoRows {
			return Indexer{}, false, nil
		}
		return Indexer{}, false, fmt.Errorf("indexer for release %d: %w", releaseID, err)
	}
	return out, true, nil
}

// qualifiedIndexerColumns prefixes the shared column list with a table alias so
// it can be used in joins.
func qualifiedIndexerColumns(alias string) string {
	columns := strings.Split(indexerColumns, ",")
	for i, column := range columns {
		columns[i] = alias + "." + strings.TrimSpace(column)
	}
	return strings.Join(columns, ", ")
}

// ListIndexerHistory returns the newest history rows for one indexer.
func (s *Store) ListIndexerHistory(ctx context.Context, indexerID int64, limit int) ([]IndexerHistory, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, indexer_id, kind, query, status, latency_ms, bytes, error, app,
			started_at
		FROM indexer_history WHERE indexer_id = ?
		ORDER BY started_at DESC, id DESC LIMIT ?`, indexerID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []IndexerHistory
	for rows.Next() {
		var item IndexerHistory
		var indexer sql.NullInt64
		var started int64
		if err := rows.Scan(&item.ID, &indexer, &item.Kind, &item.Query,
			&item.Status, &item.LatencyMS, &item.Bytes, &item.Error, &item.App,
			&started); err != nil {
			return nil, err
		}
		if indexer.Valid {
			item.IndexerID = indexer.Int64
		}
		item.StartedAt = unixTime(started)
		out = append(out, item)
	}
	return out, rows.Err()
}

// CreateApplication inserts a downstream application and returns its id.
func (s *Store) CreateApplication(ctx context.Context, in Application) (int64, error) {
	if in.SyncLevel == "" {
		in.SyncLevel = "full"
	}
	var id int64
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO applications(kind, name, url, api_key_enc, sync_level,
			last_sync_at, last_error, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)
		RETURNING id`,
		in.Kind, in.Name, in.URL, in.APIKeyEnc, in.SyncLevel,
		in.LastSyncAt.Unix(), in.LastError, time.Now().Unix(), time.Now().Unix()).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("create application: %w", err)
	}
	return id, nil
}

// GetApplication loads one application by id.
func (s *Store) GetApplication(ctx context.Context, id int64) (Application, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, kind, name, url, api_key_enc, sync_level, last_sync_at, last_error
		FROM applications WHERE id = ?`, id)
	out, err := scanApplication(row)
	if err != nil {
		if err == sql.ErrNoRows {
			return Application{}, fmt.Errorf("application %d: %w", id, sql.ErrNoRows)
		}
		return Application{}, err
	}
	return out, nil
}

// ListApplications returns every application ordered by name.
func (s *Store) ListApplications(ctx context.Context) ([]Application, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, kind, name, url, api_key_enc, sync_level, last_sync_at, last_error
		FROM applications ORDER BY name COLLATE NOCASE, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Application
	for rows.Next() {
		app, err := scanApplication(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, app)
	}
	return out, rows.Err()
}

func scanApplication(scanner interface{ Scan(...any) error }) (Application, error) {
	var out Application
	var lastSync int64
	if err := scanner.Scan(&out.ID, &out.Kind, &out.Name, &out.URL,
		&out.APIKeyEnc, &out.SyncLevel, &lastSync, &out.LastError); err != nil {
		return Application{}, err
	}
	out.LastSyncAt = unixTime(lastSync)
	return out, nil
}

// UpdateApplication saves an application.
func (s *Store) UpdateApplication(ctx context.Context, in Application) error {
	if in.SyncLevel == "" {
		in.SyncLevel = "full"
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE applications SET kind = ?, name = ?, url = ?, api_key_enc = ?,
			sync_level = ?, last_sync_at = ?, last_error = ?, updated_at = ?
		WHERE id = ?`,
		in.Kind, in.Name, in.URL, in.APIKeyEnc, in.SyncLevel,
		in.LastSyncAt.Unix(), in.LastError, time.Now().Unix(), in.ID)
	if err != nil {
		return fmt.Errorf("update application %d: %w", in.ID, err)
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return fmt.Errorf("application %d: %w", in.ID, sql.ErrNoRows)
	}
	return nil
}

// DeleteApplication removes an application and its app_indexers rows.
func (s *Store) DeleteApplication(ctx context.Context, id int64) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM applications WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete application %d: %w", id, err)
	}
	return nil
}

// UpsertAppIndexer records the remote identity of one indexer in an app.
func (s *Store) UpsertAppIndexer(ctx context.Context, in AppIndexer) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO app_indexers(application_id, indexer_id, remote_id, synced_hash)
		VALUES(?, ?, ?, ?)
		ON CONFLICT(application_id, indexer_id) DO UPDATE SET
			remote_id = excluded.remote_id,
			synced_hash = excluded.synced_hash`,
		in.ApplicationID, in.IndexerID, in.RemoteID, in.SyncedHash)
	if err != nil {
		return fmt.Errorf("upsert app indexer: %w", err)
	}
	return nil
}

// ListAppIndexers returns the indexers known to one application.
func (s *Store) ListAppIndexers(ctx context.Context, applicationID int64) ([]AppIndexer, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT application_id, indexer_id, remote_id, synced_hash
		FROM app_indexers WHERE application_id = ?
		ORDER BY indexer_id`, applicationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AppIndexer
	for rows.Next() {
		var item AppIndexer
		if err := rows.Scan(&item.ApplicationID, &item.IndexerID,
			&item.RemoteID, &item.SyncedHash); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// DeleteAppIndexer removes one app/indexer link.
func (s *Store) DeleteAppIndexer(ctx context.Context, applicationID, indexerID int64) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM app_indexers WHERE application_id = ? AND indexer_id = ?`,
		applicationID, indexerID)
	if err != nil {
		return fmt.Errorf("delete app indexer: %w", err)
	}
	return nil
}

// CreateProxy inserts a proxy and returns its id.
func (s *Store) CreateProxy(ctx context.Context, in Proxy) (int64, error) {
	now := time.Now().Unix()
	var id int64
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO proxies(kind, url, secrets_enc, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?)
		RETURNING id`, in.Kind, in.URL, in.SecretsEnc, now, now).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("create proxy: %w", err)
	}
	return id, nil
}

// GetProxy loads one proxy by id.
func (s *Store) GetProxy(ctx context.Context, id int64) (Proxy, error) {
	var out Proxy
	err := s.db.QueryRowContext(ctx, `
		SELECT id, kind, url, secrets_enc FROM proxies WHERE id = ?`, id).
		Scan(&out.ID, &out.Kind, &out.URL, &out.SecretsEnc)
	if err != nil {
		if err == sql.ErrNoRows {
			return Proxy{}, fmt.Errorf("proxy %d: %w", id, sql.ErrNoRows)
		}
		return Proxy{}, err
	}
	return out, nil
}

// ListProxies returns every proxy ordered by id.
func (s *Store) ListProxies(ctx context.Context) ([]Proxy, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, kind, url, secrets_enc FROM proxies ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Proxy
	for rows.Next() {
		var item Proxy
		if err := rows.Scan(&item.ID, &item.Kind, &item.URL, &item.SecretsEnc); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// UpdateProxy saves a proxy.
func (s *Store) UpdateProxy(ctx context.Context, in Proxy) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE proxies SET kind = ?, url = ?, secrets_enc = ?, updated_at = ?
		WHERE id = ?`, in.Kind, in.URL, in.SecretsEnc, time.Now().Unix(), in.ID)
	if err != nil {
		return fmt.Errorf("update proxy %d: %w", in.ID, err)
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return fmt.Errorf("proxy %d: %w", in.ID, sql.ErrNoRows)
	}
	return nil
}

// DeleteProxy removes a proxy; indexers using it fall back to no proxy.
func (s *Store) DeleteProxy(ctx context.Context, id int64) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM proxies WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete proxy %d: %w", id, err)
	}
	return nil
}

// BackfillLegacySources creates one definition and one indexer for every row
// in sources that does not have a bridged indexer yet. It never copies
// api_key, username or password. The operation is idempotent and runs inside
// a single transaction.
func (s *Store) BackfillLegacySources(ctx context.Context) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin source backfill: %w", err)
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx, `
		SELECT id, name, kind, manager, url, categories, default_category, query,
			enabled, interval_seconds
		FROM sources ORDER BY id`)
	if err != nil {
		return 0, fmt.Errorf("list sources for backfill: %w", err)
	}
	type legacySource struct {
		id              int64
		name            string
		kind            string
		manager         string
		url             string
		categories      string
		defaultCategory string
		query           string
		enabled         bool
		intervalSeconds int
	}
	var pending []legacySource
	for rows.Next() {
		var src legacySource
		var enabled int
		if err := rows.Scan(&src.id, &src.name, &src.kind, &src.manager, &src.url,
			&src.categories, &src.defaultCategory, &src.query, &enabled,
			&src.intervalSeconds); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scan source for backfill: %w", err)
		}
		src.enabled = enabled != 0
		if strings.HasPrefix(src.url, sealedPrefix) {
			// Legacy source URLs are sealed at rest. The numbered migration
			// runs before a codec is installed, so a sealed row can only be
			// bridged after start-up has resolved the vault; deferring it is
			// safer than copying ciphertext into a definition.
			opened, openErr := s.openField(src.url)
			if openErr != nil {
				continue
			}
			src.url = opened
		}
		pending = append(pending, src)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, fmt.Errorf("list sources for backfill: %w", err)
	}
	rows.Close()

	created := 0
	for _, src := range pending {
		var bridged int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM indexers WHERE legacy_source_id = ?`, src.id).Scan(&bridged); err != nil {
			return 0, fmt.Errorf("check backfill for source %d: %w", src.id, err)
		}
		// The legacy definition is created in the same transaction as the
		// indexer. If it exists while the indexer does not, the bridge was
		// migrated and then deliberately removed: do not recreate it on the
		// next start.
		var migrated int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM definitions WHERE id = ?`, legacyDefinitionID(src.id)).Scan(&migrated); err != nil {
			return 0, fmt.Errorf("check legacy definition for source %d: %w", src.id, err)
		}
		if bridged > 0 || migrated > 0 {
			continue
		}
		body, err := legacyDefinitionBody(src.id, src.name, src.kind, src.url)
		if err != nil {
			return 0, fmt.Errorf("render legacy definition %d: %w", src.id, err)
		}
		sum := sha256.Sum256(body)
		now := time.Now()
		def := Definition{
			ID:            legacyDefinitionID(src.id),
			SchemaVersion: 1,
			Family:        src.kind,
			State:         "verificata",
			Origin:        "local",
			Author:        "migration",
			Body:          string(body),
			Hash:          hex.EncodeToString(sum[:]),
			UpdatedAt:     now,
		}
		if err := upsertDefinition(ctx, tx, def); err != nil {
			return 0, fmt.Errorf("create legacy definition %d: %w", src.id, err)
		}
		slug, err := uniqueLegacySlug(ctx, tx, legacySlug(src.name), src.id)
		if err != nil {
			return 0, err
		}
		categories := Source{Categories: src.categories}.CategoryIDs()
		if categories == nil {
			categories = []int{}
		}
		config, err := json.Marshal(struct {
			Kind            string `json:"kind"`
			Manager         string `json:"manager"`
			URL             string `json:"url"`
			Categories      []int  `json:"categories"`
			DefaultCategory string `json:"default_category"`
			Query           string `json:"query"`
			IntervalSeconds int    `json:"interval_seconds"`
		}{
			Kind: src.kind, Manager: src.manager, URL: src.url,
			Categories: categories, DefaultCategory: src.defaultCategory,
			Query: src.query, IntervalSeconds: src.intervalSeconds,
		})
		if err != nil {
			return 0, fmt.Errorf("encode legacy config %d: %w", src.id, err)
		}
		if _, err := createIndexer(ctx, tx, Indexer{
			DefinitionID:   def.ID,
			Protocol:       "torrent",
			Slug:           slug,
			Name:           src.name,
			Enabled:        src.enabled,
			Priority:       25,
			ConfigJSON:     string(config),
			Tags:           "",
			LegacySourceID: src.id,
			CreatedAt:      now,
			UpdatedAt:      now,
		}); err != nil {
			return 0, fmt.Errorf("create legacy indexer %d: %w", src.id, err)
		}
		created++
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit source backfill: %w", err)
	}
	return created, nil
}

func legacyDefinitionID(sourceID int64) string {
	return "legacy-" + strconv.FormatInt(sourceID, 10)
}

type legacyDefinitionYAML struct {
	Schema    int        `yaml:"schema"`
	Connector string     `yaml:"connector"`
	Meta      legacyMeta `yaml:"meta"`
}

type legacyMeta struct {
	ID       string   `yaml:"id"`
	Name     string   `yaml:"name"`
	Type     string   `yaml:"type"`
	Protocol string   `yaml:"protocol"`
	URLs     []string `yaml:"urls"`
}

func legacyDefinitionBody(sourceID int64, name, kind, url string) ([]byte, error) {
	doc := legacyDefinitionYAML{
		Schema:    1,
		Connector: kind,
		Meta: legacyMeta{
			ID:       legacyDefinitionID(sourceID),
			Name:     name,
			Type:     "private",
			Protocol: "torrent",
			URLs:     []string{url},
		},
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// legacySlug lowercases a name and turns every run of non-ASCII-alphanumeric
// characters into a single dash.
func legacySlug(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case b.Len() > 0 && !dash:
			b.WriteByte('-')
			dash = true
		}
	}
	slug := strings.Trim(b.String(), "-")
	if slug == "" {
		slug = "indexer"
	}
	return slug
}

func uniqueLegacySlug(ctx context.Context, db dbExecer, base string, sourceID int64) (string, error) {
	if base == "" {
		base = "indexer"
	}
	candidate := base
	for attempt := 0; ; attempt++ {
		var exists int
		if err := db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM indexers WHERE slug = ?`, candidate).Scan(&exists); err != nil {
			return "", fmt.Errorf("check slug %q: %w", candidate, err)
		}
		if exists == 0 {
			return candidate, nil
		}
		if attempt == 0 {
			candidate = base + "-" + strconv.FormatInt(sourceID, 10)
			continue
		}
		candidate = fmt.Sprintf("%s-%d-%d", base, sourceID, attempt+1)
	}
}
