package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
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
	ID           int64
	DefinitionID string
	Protocol     string
	Slug         string
	Name         string
	Enabled      bool
	Priority     int
	ConfigJSON   string
	Tags         string
	ProxyID      *int64
	QueryLimit   int
	GrabLimit    int
	// Seed criteria copied to the *arr applications by the app sync. Zero
	// means "leave the application default".
	MinSeeders                int
	SeedRatio                 float64
	SeedTimeMinutes           int
	SeasonPackSeedTimeMinutes int
	// IntervalSeconds overrides the global scheduler interval when positive.
	// Zero keeps the global default_interval_seconds.
	IntervalSeconds int
	CreatedAt       time.Time
	UpdatedAt       time.Time
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
	// Metrics is written with the history row when the caller measured the
	// execution. It is nil for rows recorded before migration 5 and for
	// operations that have nothing to measure (a download, a grab).
	Metrics *RunMetrics
}

// Application is a downstream *arr instance.
type Application struct {
	ID        int64
	Kind      string
	Name      string
	URL       string
	APIKeyEnc []byte
	SyncLevel string
	// TagFilter is the comma-separated list of indexarr tag labels that belong
	// to this application. Empty means "every indexer".
	TagFilter  string
	LastSyncAt time.Time
	LastError  string
}

// Snapshot reasons. They mirror the CHECK constraint of app_snapshots.reason.
const (
	SnapshotReasonSync          = "sync"
	SnapshotReasonManual        = "manual"
	SnapshotReasonBeforeRestore = "before-restore"
)

// AppSnapshot is the indexer list of one application at a point in time. The
// application API key is never part of Payload.
type AppSnapshot struct {
	ID           int64
	AppID        int64
	CreatedAt    time.Time
	Reason       string
	IndexerCount int
	Payload      string
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
	config_json, tags, proxy_id, query_limit, grab_limit,
	min_seeders, seed_ratio, seed_time_minutes, season_pack_seed_time_minutes,
	interval_seconds, created_at, updated_at`

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

// SetDefinitionState changes only the catalog state of one definition. The
// body, hash and updated_at stay untouched: a health transition must never
// lose or rewrite the definition, and changing updated_at would needlessly
// invalidate every cached connector.
func (s *Store) SetDefinitionState(ctx context.Context, id, state string) error {
	state = strings.ToLower(strings.TrimSpace(state))
	switch state {
	case "bozza", "verificata", "stabile", "degradata":
	default:
		return fmt.Errorf("definition %s: invalid state %q", id, state)
	}
	result, err := s.db.ExecContext(ctx,
		`UPDATE definitions SET state = ? WHERE id = ?`, state, id)
	if err != nil {
		return fmt.Errorf("set definition state %s: %w", id, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("set definition state %s: %w", id, err)
	}
	if affected == 0 {
		return fmt.Errorf("definition %s: %w", id, sql.ErrNoRows)
	}
	return nil
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
	id, err := createIndexer(ctx, s.db, in)
	if err == nil {
		s.InvalidateCounts()
	}
	return id, err
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
	var id int64
	err := db.QueryRowContext(ctx, `
		INSERT INTO indexers(definition_id, protocol, slug, name, enabled, priority,
			config_json, proxy_id, tags, query_limit, grab_limit,
			min_seeders, seed_ratio, seed_time_minutes, season_pack_seed_time_minutes,
			interval_seconds, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		RETURNING id`,
		in.DefinitionID, in.Protocol, strings.TrimSpace(in.Slug), strings.TrimSpace(in.Name),
		boolInt(in.Enabled), in.Priority, in.ConfigJSON, proxy, in.Tags,
		in.QueryLimit, in.GrabLimit, in.MinSeeders, in.SeedRatio,
		in.SeedTimeMinutes, in.SeasonPackSeedTimeMinutes,
		in.IntervalSeconds, created.Unix(), updated.Unix()).Scan(&id)
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
	var proxy sql.NullInt64
	var created, updated int64
	if err := scanner.Scan(&out.ID, &out.DefinitionID, &out.Protocol, &out.Slug,
		&out.Name, &enabled, &out.Priority, &out.ConfigJSON, &out.Tags, &proxy,
		&out.QueryLimit, &out.GrabLimit, &out.MinSeeders, &out.SeedRatio,
		&out.SeedTimeMinutes, &out.SeasonPackSeedTimeMinutes,
		&out.IntervalSeconds, &created, &updated); err != nil {
		return Indexer{}, err
	}
	out.Enabled = enabled != 0
	if proxy.Valid {
		id := proxy.Int64
		out.ProxyID = &id
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
	res, err := s.db.ExecContext(ctx, `
		UPDATE indexers SET definition_id = ?, protocol = ?, slug = ?, name = ?,
			enabled = ?, priority = ?, config_json = ?, proxy_id = ?, tags = ?,
			query_limit = ?, grab_limit = ?,
			min_seeders = ?, seed_ratio = ?, seed_time_minutes = ?,
			season_pack_seed_time_minutes = ?, interval_seconds = ?, updated_at = ?
		WHERE id = ?`,
		in.DefinitionID, in.Protocol, strings.TrimSpace(in.Slug), strings.TrimSpace(in.Name),
		boolInt(in.Enabled), in.Priority, in.ConfigJSON, proxy, in.Tags,
		in.QueryLimit, in.GrabLimit, in.MinSeeders, in.SeedRatio,
		in.SeedTimeMinutes, in.SeasonPackSeedTimeMinutes,
		in.IntervalSeconds, time.Now().Unix(), in.ID)
	if err != nil {
		return fmt.Errorf("update indexer %d: %w", in.ID, err)
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return fmt.Errorf("indexer %d: %w", in.ID, sql.ErrNoRows)
	}
	s.InvalidateCounts()
	return nil
}

// ErrIndexerGone means provenance arrived after its indexer was deleted. The
// live worker treats it as a normal end, not as a database failure.
var ErrIndexerGone = errors.New("indexer no longer exists")

// DeleteIndexer removes an indexer. Secrets, state and provenance rows
// cascade; history keeps its rows with a NULL indexer_id.
func (s *Store) DeleteIndexer(ctx context.Context, id int64) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM indexers WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete indexer %d: %w", id, err)
	}
	s.InvalidateCounts()
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
	s.InvalidateCounts()
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
		if strings.Contains(err.Error(), "FOREIGN KEY constraint failed") {
			var exists int
			lookupErr := s.db.QueryRowContext(ctx, `SELECT 1 FROM indexers WHERE id = ?`, in.IndexerID).Scan(&exists)
			if lookupErr == sql.ErrNoRows {
				return ErrIndexerGone
			}
		}
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

// RunRetryState is the scheduling picture of one indexer: when it last ran,
// the current streak of failed runs, and the newest success of any check.
type RunRetryState struct {
	LastRun     time.Time
	LastSuccess time.Time
	Failures    int
}

// RunRetryStates returns the retry picture for every indexer. The scheduler
// uses it to space failed runs at 1, 2, 3, 6, 12, 24, 48 and 96 hours and to
// stop retrying after the last step.
func (s *Store) RunRetryStates(ctx context.Context) (map[int64]RunRetryState, error) {
	out := map[int64]RunRetryState{}
	rows, err := s.db.QueryContext(ctx, `
		SELECT indexer_id, status, started_at FROM (
			SELECT indexer_id, status, started_at,
				ROW_NUMBER() OVER (PARTITION BY indexer_id ORDER BY started_at DESC, id DESC) AS rn
			FROM indexer_history
			WHERE kind = 'run' AND indexer_id IS NOT NULL
		) WHERE rn <= 9
		ORDER BY indexer_id, started_at DESC, rn`)
	if err != nil {
		return nil, fmt.Errorf("run retry states: %w", err)
	}
	defer rows.Close()
	counting := map[int64]bool{}
	for rows.Next() {
		var id, started int64
		var status string
		if err := rows.Scan(&id, &status, &started); err != nil {
			return nil, err
		}
		state := out[id]
		if state.LastRun.IsZero() {
			state.LastRun = unixTime(started)
			counting[id] = true
		}
		if counting[id] {
			if strings.EqualFold(strings.TrimSpace(status), "error") {
				if state.Failures < 9 {
					state.Failures++
				}
			} else {
				counting[id] = false
			}
		}
		out[id] = state
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	successRows, err := s.db.QueryContext(ctx, `
		SELECT indexer_id, MAX(started_at)
		FROM indexer_history
		WHERE indexer_id IS NOT NULL AND status = 'ok'
			AND kind IN ('run', 'search', 'latest', 'health', 'test')
		GROUP BY indexer_id`)
	if err != nil {
		return nil, fmt.Errorf("run retry successes: %w", err)
	}
	defer successRows.Close()
	for successRows.Next() {
		var id, started int64
		if err := successRows.Scan(&id, &started); err != nil {
			return nil, err
		}
		state := out[id]
		state.LastSuccess = unixTime(started)
		out[id] = state
	}
	if err := successRows.Err(); err != nil {
		return nil, err
	}
	for id, state := range out {
		if state.LastSuccess.After(state.LastRun) {
			state.Failures = 0
			out[id] = state
		}
	}
	return out, nil
}

// LastRunTimes returns the most recent successful or failed scheduled run per
// indexer. The scheduler uses it to honour per-indexer intervals without an
// N+1 query per row.
func (s *Store) LastRunTimes(ctx context.Context) (map[int64]time.Time, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT indexer_id, MAX(started_at)
		FROM indexer_history
		WHERE kind = 'run' AND indexer_id IS NOT NULL
		GROUP BY indexer_id`)
	if err != nil {
		return nil, fmt.Errorf("last run times: %w", err)
	}
	defer rows.Close()
	out := map[int64]time.Time{}
	for rows.Next() {
		var id, started int64
		if err := rows.Scan(&id, &started); err != nil {
			return nil, err
		}
		out[id] = unixTime(started)
	}
	return out, rows.Err()
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
		SELECT h.id, h.indexer_id, h.kind, h.query, h.status, h.latency_ms,
			h.bytes, h.error, h.app, h.started_at, `+indexerMetricsColumns+`
		FROM indexer_history h
		LEFT JOIN indexer_history_metrics m ON m.history_id = h.id
		WHERE h.indexer_id = ?
		ORDER BY h.started_at DESC, h.id DESC LIMIT ?`, indexerID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []IndexerHistory
	for rows.Next() {
		var item IndexerHistory
		var indexer sql.NullInt64
		var started int64
		var metrics RunMetrics
		if err := rows.Scan(&item.ID, &indexer, &item.Kind, &item.Query,
			&item.Status, &item.LatencyMS, &item.Bytes, &item.Error, &item.App,
			&started, &metrics.Seen, &metrics.ItemsNew, &metrics.Updated,
			&metrics.Duplicates, &metrics.Discarded, &metrics.DiscardNoLink,
			&metrics.DiscardBlocked, &metrics.DiscardTooOld,
			&metrics.DiscardCategory, &metrics.DiscardParse,
			&metrics.DiscardOther, &metrics.FieldTotal, &metrics.FieldSize,
			&metrics.FieldSeeders, &metrics.FieldCategory, &metrics.FieldDate,
			&metrics.FieldHash); err != nil {
			return nil, err
		}
		if indexer.Valid {
			item.IndexerID = indexer.Int64
		}
		metrics.HistoryID = item.ID
		item.Metrics = &metrics
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
			tag_filter, last_sync_at, last_error, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		RETURNING id`,
		in.Kind, in.Name, in.URL, in.APIKeyEnc, in.SyncLevel,
		in.TagFilter, in.LastSyncAt.Unix(), in.LastError,
		time.Now().Unix(), time.Now().Unix()).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("create application: %w", err)
	}
	s.InvalidateCounts()
	return id, nil
}

// GetApplication loads one application by id.
func (s *Store) GetApplication(ctx context.Context, id int64) (Application, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, kind, name, url, api_key_enc, sync_level, tag_filter,
			last_sync_at, last_error
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
		SELECT id, kind, name, url, api_key_enc, sync_level, tag_filter,
			last_sync_at, last_error
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
		&out.APIKeyEnc, &out.SyncLevel, &out.TagFilter, &lastSync, &out.LastError); err != nil {
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
			sync_level = ?, tag_filter = ?, last_sync_at = ?, last_error = ?, updated_at = ?
		WHERE id = ?`,
		in.Kind, in.Name, in.URL, in.APIKeyEnc, in.SyncLevel, in.TagFilter,
		in.LastSyncAt.Unix(), in.LastError, time.Now().Unix(), in.ID)
	if err != nil {
		return fmt.Errorf("update application %d: %w", in.ID, err)
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return fmt.Errorf("application %d: %w", in.ID, sql.ErrNoRows)
	}
	s.InvalidateCounts()
	return nil
}

// DeleteApplication removes an application, its app_indexers rows and its
// snapshots (both through ON DELETE CASCADE).
func (s *Store) DeleteApplication(ctx context.Context, id int64) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM applications WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete application %d: %w", id, err)
	}
	s.InvalidateCounts()
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

// AppSnapshotRetention is the number of snapshots kept per application. Older
// rows are pruned by CreateAppSnapshot inside the same transaction.
const AppSnapshotRetention = 20

// CreateAppSnapshot stores one snapshot and keeps only the newest
// AppSnapshotRetention rows for the application. It returns the new id.
func (s *Store) CreateAppSnapshot(ctx context.Context, in AppSnapshot) (int64, error) {
	reason := strings.TrimSpace(in.Reason)
	switch reason {
	case SnapshotReasonSync, SnapshotReasonManual, SnapshotReasonBeforeRestore:
	case "":
		reason = SnapshotReasonManual
	default:
		return 0, fmt.Errorf("app snapshot: invalid reason %q", in.Reason)
	}
	if in.Payload == "" {
		in.Payload = "[]"
	}
	if in.IndexerCount < 0 {
		in.IndexerCount = 0
	}
	if in.CreatedAt.IsZero() {
		in.CreatedAt = time.Now()
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("app snapshot: begin: %w", err)
	}
	defer tx.Rollback()

	var id int64
	if err := tx.QueryRowContext(ctx, `
		INSERT INTO app_snapshots(app_id, created_at, reason, indexer_count, payload)
		VALUES(?, ?, ?, ?, ?)
		RETURNING id`,
		in.AppID, in.CreatedAt.Unix(), reason, in.IndexerCount, in.Payload).Scan(&id); err != nil {
		return 0, fmt.Errorf("app snapshot: insert: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM app_snapshots
		WHERE app_id = ? AND id NOT IN (
			SELECT id FROM app_snapshots WHERE app_id = ?
			ORDER BY created_at DESC, id DESC LIMIT ?
		)`, in.AppID, in.AppID, AppSnapshotRetention); err != nil {
		return 0, fmt.Errorf("app snapshot: prune: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("app snapshot: commit: %w", err)
	}
	return id, nil
}

const appSnapshotColumns = `id, app_id, created_at, reason, indexer_count, payload`

// ListAppSnapshots returns the snapshots of one application, newest first.
func (s *Store) ListAppSnapshots(ctx context.Context, applicationID int64) ([]AppSnapshot, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+appSnapshotColumns+` FROM app_snapshots
		WHERE app_id = ? ORDER BY created_at DESC, id DESC`, applicationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AppSnapshot
	for rows.Next() {
		item, err := scanAppSnapshot(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// GetAppSnapshot loads one snapshot, scoped to its application so a crafted id
// can never read another app's state.
func (s *Store) GetAppSnapshot(ctx context.Context, applicationID, snapshotID int64) (AppSnapshot, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT `+appSnapshotColumns+` FROM app_snapshots
		WHERE app_id = ? AND id = ?`, applicationID, snapshotID)
	item, err := scanAppSnapshot(row)
	if err != nil {
		if err == sql.ErrNoRows {
			return AppSnapshot{}, fmt.Errorf("app snapshot %d: %w", snapshotID, sql.ErrNoRows)
		}
		return AppSnapshot{}, err
	}
	return item, nil
}

func scanAppSnapshot(scanner interface{ Scan(...any) error }) (AppSnapshot, error) {
	var out AppSnapshot
	var created int64
	if err := scanner.Scan(&out.ID, &out.AppID, &created, &out.Reason,
		&out.IndexerCount, &out.Payload); err != nil {
		return AppSnapshot{}, err
	}
	out.CreatedAt = unixTime(created)
	return out, nil
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

// SetIndexerProtocol updates the protocol inferred from a generic RSS feed.
// The next run and the application synchroniser read the new value.
func (s *Store) SetIndexerProtocol(ctx context.Context, id int64, protocol string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE indexers SET protocol = ?, updated_at = ? WHERE id = ?`,
		protocol, time.Now().Unix(), id)
	if err == nil {
		s.InvalidateCounts()
	}
	return err
}
