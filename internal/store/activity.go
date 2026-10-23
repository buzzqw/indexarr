package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// RunMetrics is the per-execution view of what an indexer returned. It is
// stored in indexer_history_metrics, one row per history row.
//
// Seen counts the normalized items returned by the connector. The discarded
// counters partition the items that were not persisted, except duplicates:
// duplicates are reported separately and are not a discard reason.
type RunMetrics struct {
	HistoryID  int64
	Seen       int64
	ItemsNew   int64
	Updated    int64
	Duplicates int64
	Discarded  int64

	DiscardNoLink   int64
	DiscardBlocked  int64
	DiscardTooOld   int64
	DiscardCategory int64
	DiscardParse    int64
	DiscardOther    int64

	FieldTotal    int64
	FieldSize     int64
	FieldSeeders  int64
	FieldCategory int64
	FieldDate     int64
	FieldHash     int64
}

// Percent returns the completeness of one field as an integer percentage. A
// run with no normalized items reports zero instead of dividing by zero.
func (m RunMetrics) Percent(field int64) int64 {
	if m.FieldTotal <= 0 {
		return 0
	}
	if field < 0 {
		field = 0
	}
	if field > m.FieldTotal {
		field = m.FieldTotal
	}
	return field * 100 / m.FieldTotal
}

// SampleItem is one normalized release in the last sample of an indexer.
// Download and details URLs are already redacted; Fields records which values
// the site actually provided, so the UI can show found and missing side by
// side without re-parsing the raw response.
type SampleItem struct {
	Title       string          `json:"title"`
	GUID        string          `json:"guid,omitempty"`
	DetailsURL  string          `json:"details_url,omitempty"`
	DownloadURL string          `json:"download_url,omitempty"`
	Magnet      string          `json:"magnet,omitempty"`
	InfoHash    string          `json:"infohash,omitempty"`
	Size        int64           `json:"size"`
	Seeders     int64           `json:"seeders"`
	Leechers    int64           `json:"leechers"`
	Categories  []int           `json:"categories,omitempty"`
	Published   string          `json:"published,omitempty"`
	IMDbID      string          `json:"imdbid,omitempty"`
	TMDbID      string          `json:"tmdbid,omitempty"`
	TVDbID      string          `json:"tvdbid,omitempty"`
	Fields      map[string]bool `json:"fields,omitempty"`
}

// IndexerSample is the last successful request/response observed for one
// indexer. IndexerID is the primary key: an UPSERT replaces the previous row.
type IndexerSample struct {
	IndexerID  int64
	RequestURL string
	Method     string
	Status     int
	LatencyMS  int64
	Body       string
	BodyBytes  int64
	Truncated  bool
	Items      []SampleItem
	ItemsCount int
	FetchedAt  time.Time
}

// AppSearch is one search request received from a Torznab/Newznab client.
type AppSearch struct {
	ID           int64
	App          string
	Mode         string
	Query        string
	IndexerIDs   []int64
	IndexerNames []string
	Results      map[string]int64
	TotalResults int64
	DurationMS   int64
	Error        string
	CreatedAt    time.Time
}

// RunMetricRow is one history row joined with its metrics.
type RunMetricRow struct {
	IndexerHistory
	Metrics RunMetrics
}

// RunSeriesPoint is one run of the 7-day chart. New and Discarded are the two
// series; StartedAt orders the points.
type RunSeriesPoint struct {
	HistoryID int64
	StartedAt time.Time
	New       int64
	Discarded int64
}

// FeedRelease is a release in the "new releases" stream together with its
// provenance: how many indexers saw it and which ones.
type FeedRelease struct {
	Release
	IndexerCount int
	IndexerNames []string
}

// RecordIndexerHistory appends one history row and returns its id. When
// in.Metrics is not nil the metrics row is written in the same transaction, so
// a history row never appears without the measurements it was recorded with.
func (s *Store) RecordIndexerHistory(ctx context.Context, in IndexerHistory) (int64, error) {
	if in.StartedAt.IsZero() {
		in.StartedAt = time.Now()
	}
	var indexer any
	if in.IndexerID > 0 {
		indexer = in.IndexerID
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("record indexer history: begin: %w", err)
	}
	defer tx.Rollback()

	var id int64
	err = tx.QueryRowContext(ctx, `
		INSERT INTO indexer_history(indexer_id, kind, query, status, latency_ms,
			bytes, error, app, started_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)
		RETURNING id`,
		indexer, in.Kind, in.Query, in.Status, in.LatencyMS, in.Bytes, in.Error,
		in.App, in.StartedAt.Unix()).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("record indexer history: %w", err)
	}
	if in.Metrics != nil {
		metrics := *in.Metrics
		metrics.HistoryID = id
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO indexer_history_metrics(
				history_id, seen, items_new, updated, duplicates, discarded,
				discarded_no_link, discarded_blocked, discarded_too_old,
				discarded_category, discarded_parse, discarded_other,
				field_total, field_size, field_seeders, field_category,
				field_date, field_hash)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			metrics.HistoryID, metrics.Seen, metrics.ItemsNew, metrics.Updated,
			metrics.Duplicates, metrics.Discarded, metrics.DiscardNoLink,
			metrics.DiscardBlocked, metrics.DiscardTooOld, metrics.DiscardCategory,
			metrics.DiscardParse, metrics.DiscardOther, metrics.FieldTotal,
			metrics.FieldSize, metrics.FieldSeeders, metrics.FieldCategory,
			metrics.FieldDate, metrics.FieldHash); err != nil {
			return 0, fmt.Errorf("record indexer history metrics: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("record indexer history: commit: %w", err)
	}
	return id, nil
}

const indexerMetricsColumns = `COALESCE(m.seen, 0), COALESCE(m.items_new, 0),
	COALESCE(m.updated, 0), COALESCE(m.duplicates, 0), COALESCE(m.discarded, 0),
	COALESCE(m.discarded_no_link, 0), COALESCE(m.discarded_blocked, 0),
	COALESCE(m.discarded_too_old, 0), COALESCE(m.discarded_category, 0),
	COALESCE(m.discarded_parse, 0), COALESCE(m.discarded_other, 0),
	COALESCE(m.field_total, 0), COALESCE(m.field_size, 0),
	COALESCE(m.field_seeders, 0), COALESCE(m.field_category, 0),
	COALESCE(m.field_date, 0), COALESCE(m.field_hash, 0)`

// ListRunMetrics returns the newest history rows of one indexer (or every
// indexer when indexerID is zero) with their metrics. limit defaults to 20 and
// is capped at 500.
func (s *Store) ListRunMetrics(ctx context.Context, indexerID int64, limit int) ([]RunMetricRow, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 500 {
		limit = 500
	}
	query := `SELECT h.id, COALESCE(h.indexer_id, 0), h.kind, h.query, h.status,
			h.latency_ms, h.bytes, h.error, h.app, h.started_at, ` +
		indexerMetricsColumns + `
		FROM indexer_history h
		LEFT JOIN indexer_history_metrics m ON m.history_id = h.id`
	args := []any{}
	if indexerID > 0 {
		query += ` WHERE h.indexer_id = ?`
		args = append(args, indexerID)
	}
	query += ` ORDER BY h.started_at DESC, h.id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list run metrics: %w", err)
	}
	defer rows.Close()
	var out []RunMetricRow
	for rows.Next() {
		var row RunMetricRow
		var indexer sql.NullInt64
		var started int64
		if err := rows.Scan(&row.ID, &indexer, &row.Kind, &row.Query,
			&row.Status, &row.LatencyMS, &row.Bytes, &row.Error, &row.App,
			&started, &row.Metrics.Seen, &row.Metrics.ItemsNew,
			&row.Metrics.Updated, &row.Metrics.Duplicates, &row.Metrics.Discarded,
			&row.Metrics.DiscardNoLink, &row.Metrics.DiscardBlocked,
			&row.Metrics.DiscardTooOld, &row.Metrics.DiscardCategory,
			&row.Metrics.DiscardParse, &row.Metrics.DiscardOther,
			&row.Metrics.FieldTotal, &row.Metrics.FieldSize,
			&row.Metrics.FieldSeeders, &row.Metrics.FieldCategory,
			&row.Metrics.FieldDate, &row.Metrics.FieldHash); err != nil {
			return nil, fmt.Errorf("scan run metrics: %w", err)
		}
		if indexer.Valid {
			row.IndexerID = indexer.Int64
		}
		row.StartedAt = unixTime(started)
		row.Metrics.HistoryID = row.ID
		out = append(out, row)
	}
	return out, rows.Err()
}

// RunSeries returns the runs that started at or after since, oldest first, for
// the 7-day chart. A zero indexerID covers every indexer.
func (s *Store) RunSeries(ctx context.Context, indexerID int64, since time.Time) ([]RunSeriesPoint, error) {
	query := `SELECT h.id, h.started_at, COALESCE(m.items_new, 0),
			COALESCE(m.discarded, 0)
		FROM indexer_history h
		LEFT JOIN indexer_history_metrics m ON m.history_id = h.id
		WHERE h.kind = 'run' AND h.started_at >= ?`
	args := []any{since.Unix()}
	if indexerID > 0 {
		query += ` AND h.indexer_id = ?`
		args = append(args, indexerID)
	}
	query += ` ORDER BY h.started_at, h.id`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("run series: %w", err)
	}
	defer rows.Close()
	var out []RunSeriesPoint
	for rows.Next() {
		var point RunSeriesPoint
		var started int64
		if err := rows.Scan(&point.HistoryID, &started, &point.New,
			&point.Discarded); err != nil {
			return nil, fmt.Errorf("scan run series: %w", err)
		}
		point.StartedAt = unixTime(started)
		out = append(out, point)
	}
	return out, rows.Err()
}

// PutIndexerSample inserts or replaces the last sample of one indexer.
func (s *Store) PutIndexerSample(ctx context.Context, in IndexerSample) error {
	if in.IndexerID <= 0 {
		return errors.New("store: sample without indexer id")
	}
	if in.FetchedAt.IsZero() {
		in.FetchedAt = time.Now()
	}
	method := strings.TrimSpace(in.Method)
	if method == "" {
		method = "GET"
	}
	items := in.Items
	if items == nil {
		items = []SampleItem{}
	}
	encoded, err := json.Marshal(items)
	if err != nil {
		return fmt.Errorf("encode indexer sample items: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO indexer_samples(indexer_id, request_url, method, status,
			latency_ms, body, body_bytes, truncated, items_json, items_count,
			fetched_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(indexer_id) DO UPDATE SET
			request_url = excluded.request_url,
			method = excluded.method,
			status = excluded.status,
			latency_ms = excluded.latency_ms,
			body = excluded.body,
			body_bytes = excluded.body_bytes,
			truncated = excluded.truncated,
			items_json = excluded.items_json,
			items_count = excluded.items_count,
			fetched_at = excluded.fetched_at`,
		in.IndexerID, in.RequestURL, method, in.Status, in.LatencyMS, in.Body,
		in.BodyBytes, boolInt(in.Truncated), string(encoded), len(items),
		in.FetchedAt.Unix())
	if err != nil {
		return fmt.Errorf("put indexer sample: %w", err)
	}
	return nil
}

// GetIndexerSample returns the last sample of one indexer.
func (s *Store) GetIndexerSample(ctx context.Context, indexerID int64) (IndexerSample, bool, error) {
	var out IndexerSample
	var truncated int
	var encoded string
	var fetched int64
	err := s.db.QueryRowContext(ctx, `
		SELECT indexer_id, request_url, method, status, latency_ms, body,
			body_bytes, truncated, items_json, items_count, fetched_at
		FROM indexer_samples WHERE indexer_id = ?`, indexerID).Scan(
		&out.IndexerID, &out.RequestURL, &out.Method, &out.Status,
		&out.LatencyMS, &out.Body, &out.BodyBytes, &truncated, &encoded,
		&out.ItemsCount, &fetched)
	if errors.Is(err, sql.ErrNoRows) {
		return IndexerSample{}, false, nil
	}
	if err != nil {
		return IndexerSample{}, false, fmt.Errorf("get indexer sample: %w", err)
	}
	if strings.TrimSpace(encoded) != "" {
		if err := json.Unmarshal([]byte(encoded), &out.Items); err != nil {
			return IndexerSample{}, false, fmt.Errorf("decode indexer sample items: %w", err)
		}
	}
	out.Truncated = truncated != 0
	out.FetchedAt = unixTime(fetched)
	return out, true, nil
}

// RecordAppSearch appends one search received from a downstream application.
func (s *Store) RecordAppSearch(ctx context.Context, in AppSearch) (int64, error) {
	if in.CreatedAt.IsZero() {
		in.CreatedAt = time.Now()
	}
	ids, err := json.Marshal(int64Slice(in.IndexerIDs))
	if err != nil {
		return 0, fmt.Errorf("encode app search indexers: %w", err)
	}
	names, err := json.Marshal(stringSlice(in.IndexerNames))
	if err != nil {
		return 0, fmt.Errorf("encode app search names: %w", err)
	}
	results := in.Results
	if results == nil {
		results = map[string]int64{}
	}
	encodedResults, err := json.Marshal(results)
	if err != nil {
		return 0, fmt.Errorf("encode app search results: %w", err)
	}
	var id int64
	err = s.db.QueryRowContext(ctx, `
		INSERT INTO app_searches(app, mode, query, indexer_ids, indexer_names,
			results_json, total_results, duration_ms, error, created_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		RETURNING id`,
		strings.TrimSpace(in.App), in.Mode, in.Query, string(ids),
		string(names), string(encodedResults), in.TotalResults, in.DurationMS,
		in.Error, in.CreatedAt.Unix()).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("record app search: %w", err)
	}
	return id, nil
}

// ListAppSearches returns the newest app searches, newest first.
func (s *Store) ListAppSearches(ctx context.Context, limit, offset int) ([]AppSearch, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, app, mode, query, indexer_ids, indexer_names, results_json,
			total_results, duration_ms, error, created_at
		FROM app_searches ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?`,
		limit, offset)
	if err != nil {
		return nil, fmt.Errorf("list app searches: %w", err)
	}
	defer rows.Close()
	var out []AppSearch
	for rows.Next() {
		var item AppSearch
		var ids, names, results string
		var created int64
		if err := rows.Scan(&item.ID, &item.App, &item.Mode, &item.Query,
			&ids, &names, &results, &item.TotalResults, &item.DurationMS,
			&item.Error, &created); err != nil {
			return nil, fmt.Errorf("scan app search: %w", err)
		}
		_ = json.Unmarshal([]byte(ids), &item.IndexerIDs)
		_ = json.Unmarshal([]byte(names), &item.IndexerNames)
		_ = json.Unmarshal([]byte(results), &item.Results)
		item.CreatedAt = unixTime(created)
		out = append(out, item)
	}
	return out, rows.Err()
}

// CountAppSearches returns how many app searches are stored.
func (s *Store) CountAppSearches(ctx context.Context) (int64, error) {
	var count int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM app_searches`).Scan(&count); err != nil {
		return 0, fmt.Errorf("count app searches: %w", err)
	}
	return count, nil
}

// PrunnedActivity reports how many rows a retention pass removed.
type PrunedActivity struct {
	History  int64
	Samples  int64
	Searches int64
}

// PruneActivity applies the activity retention to run history (metrics follow
// the cascade), indexer samples and app searches. before is exclusive.
func (s *Store) PruneActivity(ctx context.Context, before time.Time) (PrunedActivity, error) {
	var out PrunedActivity
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return out, fmt.Errorf("prune activity: begin: %w", err)
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx,
		`DELETE FROM indexer_history WHERE started_at < ?`, before.Unix())
	if err != nil {
		return out, fmt.Errorf("prune activity history: %w", err)
	}
	out.History, _ = res.RowsAffected()

	res, err = tx.ExecContext(ctx,
		`DELETE FROM indexer_samples WHERE fetched_at < ?`, before.Unix())
	if err != nil {
		return out, fmt.Errorf("prune activity samples: %w", err)
	}
	out.Samples, _ = res.RowsAffected()

	res, err = tx.ExecContext(ctx,
		`DELETE FROM app_searches WHERE created_at < ?`, before.Unix())
	if err != nil {
		return out, fmt.Errorf("prune activity searches: %w", err)
	}
	out.Searches, _ = res.RowsAffected()

	if err := tx.Commit(); err != nil {
		return out, fmt.Errorf("prune activity: commit: %w", err)
	}
	return out, nil
}

// ListFeedReleases returns the newest releases for the "new releases" stream,
// optionally limited to one indexer, with provenance counts. It is paginated on
// the server and reads hidden releases out.
func (s *Store) ListFeedReleases(ctx context.Context, indexerID int64, limit, offset int) ([]FeedRelease, int64, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	if offset < 0 {
		offset = 0
	}
	where := `WHERE r.hidden = 0`
	args := []any{}
	if indexerID > 0 {
		where += ` AND EXISTS (SELECT 1 FROM release_sources f WHERE f.release_id = r.id AND f.indexer_id = ?)`
		args = append(args, indexerID)
	}
	var total int64
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM releases r `+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count feed releases: %w", err)
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+prefixedReleaseColumns("r")+`,
			(SELECT COUNT(*) FROM release_sources rs WHERE rs.release_id = r.id),
			(SELECT GROUP_CONCAT(i.name, '|') FROM release_sources rs
				JOIN indexers i ON i.id = rs.indexer_id
				WHERE rs.release_id = r.id ORDER BY i.name COLLATE NOCASE, i.id)
		FROM releases r `+where+`
		ORDER BY r.first_seen_at DESC, r.id DESC LIMIT ? OFFSET ?`,
		append(args, limit, offset)...)
	if err != nil {
		return nil, 0, fmt.Errorf("list feed releases: %w", err)
	}
	defer rows.Close()
	var out []FeedRelease
	for rows.Next() {
		var item FeedRelease
		var names sql.NullString
		if err := scanReleaseWithProvenance(rows, &item.Release, &item.IndexerCount, &names); err != nil {
			return nil, 0, fmt.Errorf("scan feed release: %w", err)
		}
		if names.Valid && names.String != "" {
			item.IndexerNames = strings.Split(names.String, "|")
		}
		out = append(out, item)
	}
	return out, total, rows.Err()
}

func prefixedReleaseColumns(alias string) string {
	columns := strings.Split(releaseColumns, ",")
	for i, column := range columns {
		columns[i] = alias + "." + strings.TrimSpace(column)
	}
	return strings.Join(columns, ", ")
}

func scanReleaseWithProvenance(rows *sql.Rows, rel *Release, count *int, names *sql.NullString) error {
	var published, torrentFetched, firstSeen, lastSeen int64
	var favorite, hidden int
	var torrent, payload []byte
	err := rows.Scan(
		&rel.ID, &rel.DedupKey, &rel.InfoHash, &rel.IMDbID, &rel.TMDbID,
		&rel.TVDbID, &rel.Title, &rel.SearchTitle, &rel.Size, &rel.Seeders,
		&rel.Leechers, &rel.Category, &published, &rel.GUID, &rel.Magnet,
		&torrent, &rel.TorrentSize, &torrentFetched, &firstSeen,
		&lastSeen, &rel.TimesSeen, &favorite, &hidden,
		&rel.Protocol, &payload, &rel.PayloadType, count, names)
	if err != nil {
		return err
	}
	rel.PublishedAt = unixTime(published)
	rel.TorrentFetchedAt = unixTime(torrentFetched)
	rel.FirstSeenAt = unixTime(firstSeen)
	rel.LastSeenAt = unixTime(lastSeen)
	rel.Torrent = torrent
	rel.Payload = payload
	if strings.TrimSpace(rel.Protocol) == "" {
		rel.Protocol = "torrent"
	}
	rel.Favorite = favorite != 0
	rel.Hidden = hidden != 0
	return nil
}

func int64Slice(in []int64) []int64 {
	if in == nil {
		return []int64{}
	}
	return in
}

func stringSlice(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}
