package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/buzzqw/indexarr/internal/textutil"
)

// Source is one periodic input: a Torznab endpoint (Jackett, Prowlarr or a
// direct indexer) or an RSS/Atom feed.
type Source struct {
	ID                int64
	Name              string
	Kind              string // "torznab" or "rss"
	Manager           string // "auto", "jackett", "prowlarr", "torznab"
	URL               string
	APIKey            string
	Username          string // optional HTTP Basic credentials
	Password          string
	Categories        string // comma-separated Torznab category ids
	DefaultCategory   string // fallback category for feeds/lists without one
	Query             string // optional search terms for Torznab sources
	Enabled           bool
	IntervalSeconds   int
	LastRunAt         time.Time
	LastOKAt          time.Time
	LastError         string
	LastDurationMS    int64
	LastItems         int64
	TotalItems        int64
	ConsecutiveErrors int64
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// Interval returns the configured interval, or the default when the source
// does not override it. It never goes below one minute.
func (s Source) Interval(defaultInterval time.Duration) time.Duration {
	seconds := s.IntervalSeconds
	if seconds < 60 {
		return defaultInterval
	}
	return time.Duration(seconds) * time.Second
}

// CategoryIDs parses the configured comma-separated Torznab categories.
func (s Source) CategoryIDs() []int {
	var out []int
	seen := map[int]bool{}
	for _, part := range strings.Split(s.Categories, ",") {
		id, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil || id <= 0 || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

const sourceColumns = `id, name, kind, manager, url, api_key, username, password,
	categories, default_category, query, enabled, interval_seconds, last_run_at, last_ok_at,
	last_error, last_duration_ms, last_items, total_items, created_at, updated_at,
	consecutive_errors`

// ListSources returns every source, ordered by name.
func (s *Store) ListSources(ctx context.Context) ([]Source, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+sourceColumns+` FROM sources ORDER BY name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Source
	for rows.Next() {
		source, err := s.scanSource(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, source)
	}
	return out, rows.Err()
}

// GetSource loads one source by id.
func (s *Store) GetSource(ctx context.Context, id int64) (Source, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+sourceColumns+` FROM sources WHERE id = ?`, id)
	source, err := s.scanSource(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Source{}, fmt.Errorf("source %d: %w", id, sql.ErrNoRows)
	}
	return source, err
}

// CreateSource inserts a source and returns its id.
func (s *Store) CreateSource(ctx context.Context, in Source) (int64, error) {
	now := time.Now().Unix()
	url, err := s.sealField(strings.TrimSpace(in.URL))
	if err != nil {
		return 0, fmt.Errorf("create source: %w", err)
	}
	apiKey, err := s.sealField(in.APIKey)
	if err != nil {
		return 0, fmt.Errorf("create source: %w", err)
	}
	password, err := s.sealField(in.Password)
	if err != nil {
		return 0, fmt.Errorf("create source: %w", err)
	}
	var id int64
	err = s.db.QueryRowContext(ctx, `
		INSERT INTO sources(name, kind, manager, url, api_key, username, password,
			categories, default_category, query, enabled, interval_seconds, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		RETURNING id`,
		strings.TrimSpace(in.Name), in.Kind, in.Manager, url,
		apiKey, in.Username, password,
		strings.TrimSpace(in.Categories), strings.TrimSpace(in.DefaultCategory),
		strings.TrimSpace(in.Query), boolInt(in.Enabled), in.IntervalSeconds, now, now).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("create source: %w", err)
	}
	return id, nil
}

// UpdateSource saves the editable fields of a source.
func (s *Store) UpdateSource(ctx context.Context, in Source) error {
	url, err := s.sealField(strings.TrimSpace(in.URL))
	if err != nil {
		return fmt.Errorf("update source: %w", err)
	}
	apiKey, err := s.sealField(in.APIKey)
	if err != nil {
		return fmt.Errorf("update source: %w", err)
	}
	password, err := s.sealField(in.Password)
	if err != nil {
		return fmt.Errorf("update source: %w", err)
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE sources SET name = ?, kind = ?, manager = ?, url = ?, api_key = ?,
			username = ?, password = ?, categories = ?, default_category = ?,
			query = ?, enabled = ?,
			consecutive_errors = CASE WHEN ? = 1 THEN 0 ELSE consecutive_errors END,
			interval_seconds = ?, updated_at = ?
		WHERE id = ?`,
		strings.TrimSpace(in.Name), in.Kind, in.Manager, url,
		apiKey, in.Username, password, strings.TrimSpace(in.Categories),
		strings.TrimSpace(in.DefaultCategory), strings.TrimSpace(in.Query),
		boolInt(in.Enabled), boolInt(in.Enabled),
		in.IntervalSeconds, time.Now().Unix(), in.ID)
	if err != nil {
		return fmt.Errorf("update source: %w", err)
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// DeleteSource removes a source. Its releases stay in the database with a NULL
// source_id, so history survives the removal of an indexer.
func (s *Store) DeleteSource(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sources WHERE id = ?`, id)
	return err
}

// SetSourceEnabled enables or disables one source. Enabling clears the
// consecutive-failure counter.
func (s *Store) SetSourceEnabled(ctx context.Context, id int64, enabled bool) error {
	if enabled {
		_, err := s.db.ExecContext(ctx,
			`UPDATE sources SET enabled = 1, consecutive_errors = 0, updated_at = ? WHERE id = ?`,
			time.Now().Unix(), id)
		return err
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE sources SET enabled = 0, updated_at = ? WHERE id = ?`,
		time.Now().Unix(), id)
	return err
}

// SetSourcesEnabled enables or disables a set of sources.
func (s *Store) SetSourcesEnabled(ctx context.Context, ids []int64, enabled bool) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	placeholders, idArgs := idPlaceholders(ids)
	query := `UPDATE sources SET enabled = 0, updated_at = ? WHERE id IN (` + placeholders + `)`
	args := append([]any{time.Now().Unix()}, idArgs...)
	if enabled {
		query = `UPDATE sources SET enabled = 1, consecutive_errors = 0, updated_at = ? WHERE id IN (` + placeholders + `)`
	}
	res, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// SetSourcesInterval changes the schedule of a set of sources. Zero means the
// default interval.
func (s *Store) SetSourcesInterval(ctx context.Context, ids []int64, seconds int) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	placeholders, idArgs := idPlaceholders(ids)
	args := append([]any{seconds, time.Now().Unix()}, idArgs...)
	res, err := s.db.ExecContext(ctx,
		`UPDATE sources SET interval_seconds = ?, updated_at = ? WHERE id IN (`+placeholders+`)`, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// SetSourcesCategories changes the Torznab categories of a set of sources.
func (s *Store) SetSourcesCategories(ctx context.Context, ids []int64, categories string) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	placeholders, idArgs := idPlaceholders(ids)
	args := append([]any{categories, time.Now().Unix()}, idArgs...)
	res, err := s.db.ExecContext(ctx,
		`UPDATE sources SET categories = ?, updated_at = ? WHERE id IN (`+placeholders+`)`, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// DeleteSources removes a set of sources, keeping their releases.
func (s *Store) DeleteSources(ctx context.Context, ids []int64) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	placeholders, idArgs := idPlaceholders(ids)
	res, err := s.db.ExecContext(ctx, `DELETE FROM sources WHERE id IN (`+placeholders+`)`, idArgs...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func idPlaceholders(ids []int64) (string, []any) {
	parts := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		parts[i] = "?"
		args[i] = id
	}
	return strings.Join(parts, ","), args
}

// BeginSourceRun records the start of a run and marks the source as started.
func (s *Store) BeginSourceRun(ctx context.Context, sourceID int64) (int64, time.Time, error) {
	now := time.Now()
	if _, err := s.db.ExecContext(ctx, `UPDATE sources SET last_run_at = ? WHERE id = ?`, now.Unix(), sourceID); err != nil {
		return 0, time.Time{}, err
	}
	var runID int64
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO runs(source_id, source_name, started_at, status)
		SELECT id, name, ?, 'running' FROM sources WHERE id = ?
		RETURNING id`, now.Unix(), sourceID).Scan(&runID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, time.Time{}, fmt.Errorf("source %d: %w", sourceID, sql.ErrNoRows)
	}
	if err != nil {
		return 0, time.Time{}, err
	}
	return runID, now, nil
}

// FinishSourceRun records the outcome of a run on both the run row and the
// source summary.
func (s *Store) FinishSourceRun(ctx context.Context, runID int64, source Source, startedAt time.Time, res RunResult, runErr error) error {
	now := time.Now()
	status := "ok"
	errorText := ""
	if runErr != nil {
		status = "error"
		errorText = truncate(runErr.Error(), 4000)
	}
	duration := now.Sub(startedAt).Milliseconds()
	if duration < 0 {
		duration = 0
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
		UPDATE runs SET finished_at = ?, status = ?, items_seen = ?, items_new = ?,
			items_updated = ?, items_blocked = ?, torrents_fetched = ?, error = ?, duration_ms = ?
		WHERE id = ?`,
		now.Unix(), status, res.Seen, res.New, res.Updated, res.Blocked, res.Torrents,
		errorText, duration, runID); err != nil {
		return fmt.Errorf("finish run: %w", err)
	}
	if source.ID > 0 {
		if runErr != nil {
			_, err = tx.ExecContext(ctx, `
				UPDATE sources SET last_error = ?, last_duration_ms = ?, last_items = ?,
					total_items = total_items + ?, consecutive_errors = consecutive_errors + 1
				WHERE id = ?`,
				errorText, duration, res.Seen, res.New, source.ID)
		} else {
			_, err = tx.ExecContext(ctx, `
				UPDATE sources SET last_ok_at = ?, last_error = '', last_duration_ms = ?,
					last_items = ?, total_items = total_items + ?, consecutive_errors = 0
				WHERE id = ?`,
				now.Unix(), duration, res.Seen, res.New, source.ID)
		}
		if err != nil {
			return fmt.Errorf("update source status: %w", err)
		}
	}
	return tx.Commit()
}

func (s *Store) scanSource(scanner interface{ Scan(...any) error }) (Source, error) {
	var out Source
	var enabled int
	var lastRun, lastOK, created, updated int64
	err := scanner.Scan(&out.ID, &out.Name, &out.Kind, &out.Manager, &out.URL, &out.APIKey,
		&out.Username, &out.Password, &out.Categories, &out.DefaultCategory, &out.Query, &enabled,
		&out.IntervalSeconds, &lastRun, &lastOK, &out.LastError, &out.LastDurationMS,
		&out.LastItems, &out.TotalItems, &created, &updated, &out.ConsecutiveErrors)
	if err != nil {
		return Source{}, err
	}
	if out.URL, err = s.openField(out.URL); err != nil {
		return Source{}, fmt.Errorf("source %d url: %w", out.ID, err)
	}
	if out.APIKey, err = s.openField(out.APIKey); err != nil {
		return Source{}, fmt.Errorf("source %d api key: %w", out.ID, err)
	}
	if out.Password, err = s.openField(out.Password); err != nil {
		return Source{}, fmt.Errorf("source %d password: %w", out.ID, err)
	}
	out.Enabled = enabled != 0
	out.LastRunAt = unixTime(lastRun)
	out.LastOKAt = unixTime(lastOK)
	out.CreatedAt = unixTime(created)
	out.UpdatedAt = unixTime(updated)
	return out, nil
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func unixTime(value int64) time.Time {
	if value <= 0 {
		return time.Time{}
	}
	return time.Unix(value, 0)
}

func truncate(text string, max int) string {
	return textutil.Truncate(text, max)
}
