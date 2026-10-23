package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/buzzqw/indexarr/internal/textutil"
)

// Release is one cached item: the metadata always, the magnet and/or the
// .torrent file when the source exposed them.
type Release struct {
	ID               int64
	DedupKey         string
	InfoHash         string
	IMDbID           string
	TMDbID           string
	TVDbID           string
	Title            string
	SearchTitle      string
	Size             int64
	Seeders          int64
	Leechers         int64
	Category         string
	PublishedAt      time.Time
	SourceID         int64
	SourceName       string
	GUID             string
	Magnet           string
	Torrent          []byte
	TorrentSize      int64
	TorrentFetchedAt time.Time
	FirstSeenAt      time.Time
	LastSeenAt       time.Time
	TimesSeen        int64
	Favorite         bool
	Hidden           bool
}

// Filter selects releases for the web UI and the Torznab endpoint.
type Filter struct {
	Query         string
	Include       []string // parsed include terms (normal: normalized; regex: patterns)
	Exclude       []string // parsed exclude terms, applied with NOT
	Regex         bool
	SourceID      int64
	Categories    []string
	Season        int
	Episode       int
	IMDbID        string
	TMDbID        string
	TVDbID        string
	Sort          string
	Limit         int
	Offset        int
	IncludeHidden bool
	FavoritesOnly bool
	MinSeeders    int64
	MinSize       int64
	MaxSize       int64
	Quality       string
	SourceQuality string
	Language      string
}

const releaseColumns = `id, dedup_key, infohash, imdb_id, tmdb_id, tvdb_id, title,
	search_title, size, seeders, leechers, category, published_at, source_id,
	source_name, guid, magnet, torrent, torrent_size, torrent_fetched_at,
	first_seen_at, last_seen_at, times_seen, favorite, hidden`

// UpsertRelease stores a release, merging it with an existing row that has the
// same key or infohash. It reports whether the row is new.
func (s *Store) UpsertRelease(ctx context.Context, rel *Release) (bool, error) {
	if rel == nil || strings.TrimSpace(rel.DedupKey) == "" {
		return false, errors.New("store: release without key")
	}
	now := time.Now()
	if rel.FirstSeenAt.IsZero() {
		rel.FirstSeenAt = now
	}
	if rel.LastSeenAt.IsZero() {
		rel.LastSeenAt = now
	}
	if rel.PublishedAt.IsZero() {
		rel.PublishedAt = rel.FirstSeenAt
	}
	if rel.TorrentSize <= 0 {
		rel.TorrentSize = int64(len(rel.Torrent))
	}
	// A torrent fetched after the first sighting gives the release its real
	// identity: reuse the key already stored for that infohash instead of
	// creating a second row.
	if rel.InfoHash != "" {
		var existingKey string
		err := s.db.QueryRowContext(ctx,
			`SELECT dedup_key FROM releases WHERE infohash = ? ORDER BY id LIMIT 1`, rel.InfoHash).Scan(&existingKey)
		switch {
		case err == nil:
			rel.DedupKey = existingKey
		case !errors.Is(err, sql.ErrNoRows):
			return false, err
		}
	}
	var sourceID any
	if rel.SourceID > 0 {
		sourceID = rel.SourceID
	}
	var torrent any
	if len(rel.Torrent) > 0 {
		torrent = rel.Torrent
	}
	var id int64
	var timesSeen int64
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO releases(dedup_key, infohash, imdb_id, tmdb_id, tvdb_id, title,
			search_title, size, seeders, leechers, category, published_at, source_id,
			source_name, guid, magnet, torrent, torrent_size, torrent_fetched_at,
			first_seen_at, last_seen_at, times_seen)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1)
		ON CONFLICT(dedup_key) DO UPDATE SET
			infohash = CASE WHEN excluded.infohash <> '' THEN excluded.infohash ELSE releases.infohash END,
			imdb_id = CASE WHEN excluded.imdb_id <> '' THEN excluded.imdb_id ELSE releases.imdb_id END,
			tmdb_id = CASE WHEN excluded.tmdb_id <> '' THEN excluded.tmdb_id ELSE releases.tmdb_id END,
			tvdb_id = CASE WHEN excluded.tvdb_id <> '' THEN excluded.tvdb_id ELSE releases.tvdb_id END,
			title = CASE WHEN excluded.title <> '' THEN excluded.title ELSE releases.title END,
			search_title = CASE WHEN excluded.search_title <> '' THEN excluded.search_title ELSE releases.search_title END,
			size = CASE WHEN excluded.size > 0 THEN excluded.size ELSE releases.size END,
			seeders = excluded.seeders,
			leechers = excluded.leechers,
			category = CASE WHEN excluded.category <> '' THEN excluded.category ELSE releases.category END,
			published_at = CASE WHEN excluded.published_at > 0 THEN excluded.published_at ELSE releases.published_at END,
			source_id = COALESCE(excluded.source_id, releases.source_id),
			source_name = CASE WHEN excluded.source_name <> '' THEN excluded.source_name ELSE releases.source_name END,
			guid = CASE WHEN excluded.guid <> '' THEN excluded.guid ELSE releases.guid END,
			magnet = CASE WHEN excluded.magnet <> '' THEN excluded.magnet ELSE releases.magnet END,
			torrent = COALESCE(excluded.torrent, releases.torrent),
			torrent_size = CASE WHEN excluded.torrent IS NOT NULL THEN excluded.torrent_size ELSE releases.torrent_size END,
			torrent_fetched_at = CASE WHEN excluded.torrent IS NOT NULL THEN excluded.torrent_fetched_at ELSE releases.torrent_fetched_at END,
			last_seen_at = excluded.last_seen_at,
			times_seen = releases.times_seen + 1
		RETURNING id, times_seen`,
		rel.DedupKey, rel.InfoHash, rel.IMDbID, rel.TMDbID, rel.TVDbID,
		rel.Title, rel.SearchTitle, rel.Size, rel.Seeders, rel.Leechers,
		rel.Category, rel.PublishedAt.Unix(), sourceID, rel.SourceName,
		rel.GUID, rel.Magnet, torrent, rel.TorrentSize,
		rel.TorrentFetchedAt.Unix(), rel.FirstSeenAt.Unix(), rel.LastSeenAt.Unix()).Scan(&id, &timesSeen)
	if err != nil {
		return false, fmt.Errorf("upsert release: %w", err)
	}
	rel.ID = id
	rel.TimesSeen = timesSeen
	return timesSeen <= 1, nil
}

// ReleaseByInfoHash returns a release and whether it exists.
func (s *Store) ReleaseByInfoHash(ctx context.Context, hash string) (Release, bool, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+releaseColumns+` FROM releases WHERE infohash = ? ORDER BY id LIMIT 1`, hash)
	rel, err := scanRelease(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Release{}, false, nil
	}
	return rel, err == nil, err
}

// ReleaseByKey returns a release by its deduplication key.
func (s *Store) ReleaseByKey(ctx context.Context, key string) (Release, bool, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+releaseColumns+` FROM releases WHERE dedup_key = ?`, key)
	rel, err := scanRelease(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Release{}, false, nil
	}
	return rel, err == nil, err
}

// GetRelease loads one release by id.
func (s *Store) GetRelease(ctx context.Context, id int64) (Release, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+releaseColumns+` FROM releases WHERE id = ?`, id)
	rel, err := scanRelease(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Release{}, err
	}
	return rel, err
}

// SearchReleases returns one page of releases and the total matching count.
func (s *Store) SearchReleases(ctx context.Context, f Filter) ([]Release, int64, error) {
	where, args := buildWhere(f)
	countQuery := `SELECT COUNT(*) FROM releases` + where
	var total int64
	if err := s.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	offset := f.Offset
	if offset < 0 {
		offset = 0
	}
	query := `SELECT ` + releaseColumns + ` FROM releases` + where +
		` ORDER BY ` + orderBy(f.Sort) + ` LIMIT ? OFFSET ?`
	rows, err := s.db.QueryContext(ctx, query, append(args, limit, offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []Release
	for rows.Next() {
		rel, err := scanRelease(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, rel)
	}
	return out, total, rows.Err()
}

// ListRecentReleases returns the newest releases for the dashboard.
func (s *Store) ListRecentReleases(ctx context.Context, limit int) ([]Release, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+releaseColumns+` FROM releases WHERE hidden = 0 ORDER BY first_seen_at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Release
	for rows.Next() {
		rel, err := scanRelease(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rel)
	}
	return out, rows.Err()
}

// DeleteRelease removes one release.
func (s *Store) DeleteRelease(ctx context.Context, id int64) (bool, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM releases WHERE id = ?`, id)
	if err != nil {
		return false, err
	}
	affected, _ := res.RowsAffected()
	return affected > 0, nil
}

// DeleteReleases removes the given releases.
func (s *Store) DeleteReleases(ctx context.Context, ids []int64) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM releases WHERE id IN (`+strings.Join(placeholders, ",")+`)`, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// DeleteAllReleases empties the release cache.
func (s *Store) DeleteAllReleases(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM releases`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// PruneReleases deletes releases not seen since before and returns the count.
func (s *Store) PruneReleases(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM releases WHERE last_seen_at < ? AND last_seen_at > 0`, before.Unix())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ClearTorrents removes the stored .torrent payloads, keeping metadata and
// magnets. It shrinks the database without losing the index.
func (s *Store) ClearTorrents(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		UPDATE releases SET torrent = NULL, torrent_size = 0, torrent_fetched_at = 0
		WHERE torrent IS NOT NULL`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// DedupeByInfoHash merges rows that share an infohash, keeping the oldest row
// and the best data of the duplicates, and returns how many rows were removed.
func (s *Store) DedupeByInfoHash(ctx context.Context) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `
		SELECT infohash, MIN(id) FROM releases
		WHERE infohash <> '' GROUP BY infohash HAVING COUNT(*) > 1`)
	if err != nil {
		return 0, err
	}
	type group struct {
		hash   string
		keeper int64
	}
	var groups []group
	for rows.Next() {
		var g group
		if err := rows.Scan(&g.hash, &g.keeper); err != nil {
			rows.Close()
			return 0, err
		}
		groups = append(groups, g)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	var removed int64
	for _, g := range groups {
		if _, err := tx.ExecContext(ctx, `
			UPDATE releases SET
				magnet = CASE WHEN magnet = '' THEN COALESCE((
					SELECT magnet FROM releases WHERE infohash = ? AND id <> ? AND magnet <> '' LIMIT 1), '')
					ELSE magnet END,
				torrent = COALESCE(torrent, (
					SELECT torrent FROM releases WHERE infohash = ? AND id <> ? AND torrent IS NOT NULL LIMIT 1)),
				torrent_size = MAX(torrent_size, COALESCE((
					SELECT MAX(torrent_size) FROM releases WHERE infohash = ? AND id <> ?), 0)),
				seeders = MAX(seeders, COALESCE((
					SELECT MAX(seeders) FROM releases WHERE infohash = ? AND id <> ?), 0)),
				last_seen_at = MAX(last_seen_at, COALESCE((
					SELECT MAX(last_seen_at) FROM releases WHERE infohash = ? AND id <> ?), 0)),
				times_seen = times_seen + COALESCE((
					SELECT SUM(times_seen) FROM releases WHERE infohash = ? AND id <> ?), 0)
			WHERE id = ?`,
			g.hash, g.keeper, g.hash, g.keeper, g.hash, g.keeper, g.hash, g.keeper,
			g.hash, g.keeper, g.hash, g.keeper, g.keeper); err != nil {
			return 0, err
		}
		res, err := tx.ExecContext(ctx,
			`DELETE FROM releases WHERE infohash = ? AND id <> ?`, g.hash, g.keeper)
		if err != nil {
			return 0, err
		}
		deleted, _ := res.RowsAffected()
		removed += deleted
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return removed, nil
}

// Stats is the dashboard summary.
type Stats struct {
	Releases       int64
	Torrents       int64
	TorrentBytes   int64
	Sources        int64
	EnabledSources int64
	Runs24h        int64
	Errors24h      int64
	OldestSeen     time.Time
	NewestSeen     time.Time
	DBBytes        int64
}

// LoadStats computes the dashboard summary.
func (s *Store) LoadStats(ctx context.Context) (Stats, error) {
	var out Stats
	var oldest, newest int64
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*),
			COALESCE(SUM(CASE WHEN torrent IS NOT NULL THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(torrent_size), 0),
			COALESCE(MIN(first_seen_at), 0),
			COALESCE(MAX(first_seen_at), 0)
		FROM releases`).Scan(&out.Releases, &out.Torrents, &out.TorrentBytes, &oldest, &newest)
	if err != nil {
		return out, err
	}
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*), COALESCE(SUM(CASE WHEN enabled = 1 THEN 1 ELSE 0 END), 0)
		FROM sources`).Scan(&out.Sources, &out.EnabledSources); err != nil {
		return out, err
	}
	cutoff := time.Now().Add(-24 * time.Hour).Unix()
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*), COALESCE(SUM(CASE WHEN status = 'error' THEN 1 ELSE 0 END), 0)
		FROM runs WHERE started_at >= ?`, cutoff).Scan(&out.Runs24h, &out.Errors24h); err != nil {
		return out, err
	}
	out.OldestSeen = unixTime(oldest)
	out.NewestSeen = unixTime(newest)
	out.DBBytes, _ = s.SizeBytes()
	return out, nil
}

func buildWhere(f Filter) (string, []any) {
	var clauses []string
	var args []any

	var idClauses []string
	if id := normalizeID(f.IMDbID); id != "" {
		idClauses = append(idClauses, `imdb_id = ?`)
		args = append(args, id)
	}
	if id := normalizeID(f.TMDbID); id != "" {
		idClauses = append(idClauses, `tmdb_id = ?`)
		args = append(args, id)
	}
	if id := normalizeID(f.TVDbID); id != "" {
		idClauses = append(idClauses, `tvdb_id = ?`)
		args = append(args, id)
	}
	includes := f.Include
	excludes := f.Exclude
	if len(includes) == 0 && len(excludes) == 0 && strings.TrimSpace(f.Query) != "" {
		includes = searchTokens(f.Query)
	}
	var includeClauses []string
	if f.Regex {
		for _, pattern := range includes {
			includeClauses = append(includeClauses, `title REGEXP ?`)
			args = append(args, pattern)
		}
	} else {
		for _, token := range includes {
			includeClauses = append(includeClauses, `search_title LIKE ? ESCAPE '\'`)
			args = append(args, "%"+escapeLike(token)+"%")
		}
	}
	switch {
	case len(idClauses) > 0 && len(includeClauses) > 0:
		clauses = append(clauses,
			"("+strings.Join(idClauses, " OR ")+" OR ("+strings.Join(includeClauses, " AND ")+"))")
	case len(idClauses) > 0:
		clauses = append(clauses, "("+strings.Join(idClauses, " OR ")+")")
	case len(includeClauses) > 0:
		clauses = append(clauses, "("+strings.Join(includeClauses, " AND ")+")")
	}
	if f.Regex {
		for _, pattern := range excludes {
			clauses = append(clauses, `title NOT REGEXP ?`)
			args = append(args, pattern)
		}
	} else {
		for _, token := range excludes {
			clauses = append(clauses, `search_title NOT LIKE ? ESCAPE '\'`)
			args = append(args, "%"+escapeLike(token)+"%")
		}
	}
	if !f.IncludeHidden {
		clauses = append(clauses, `hidden = 0`)
	}
	if f.FavoritesOnly {
		clauses = append(clauses, `favorite = 1`)
	}
	if f.MinSeeders > 0 {
		clauses = append(clauses, `seeders >= ?`)
		args = append(args, f.MinSeeders)
	}
	if f.MinSize > 0 {
		clauses = append(clauses, `size >= ?`)
		args = append(args, f.MinSize)
	}
	if f.MaxSize > 0 {
		clauses = append(clauses, `size <= ?`)
		args = append(args, f.MaxSize)
	}
	switch f.Quality {
	case "2160p":
		clauses = append(clauses, `(search_title LIKE '%2160p%' OR search_title LIKE '%4k%')`)
	case "1080p", "720p", "480p":
		clauses = append(clauses, `search_title LIKE ? ESCAPE '\'`)
		args = append(args, "%"+f.Quality+"%")
	}
	switch f.SourceQuality {
	case "webdl":
		clauses = append(clauses, `(search_title LIKE '%web dl%' OR search_title LIKE '%webdl%')`)
	case "bluray":
		clauses = append(clauses, `(search_title LIKE '%bluray%' OR search_title LIKE '%blu ray%')`)
	case "hdtv":
		clauses = append(clauses, `search_title LIKE '%hdtv%'`)
	}
	switch f.Language {
	case "ita":
		clauses = append(clauses, `(search_title LIKE '%ita%' OR search_title LIKE '%italian%')`)
	case "eng":
		clauses = append(clauses, `(search_title LIKE '%eng%' OR search_title LIKE '%english%')`)
	}

	if f.SourceID > 0 {
		clauses = append(clauses, `source_id = ?`)
		args = append(args, f.SourceID)
	}
	if len(f.Categories) > 0 {
		var catClauses []string
		for _, category := range f.Categories {
			pattern := categoryPattern(category)
			if pattern == "" {
				continue
			}
			catClauses = append(catClauses, `category LIKE ? ESCAPE '\'`)
			args = append(args, pattern)
		}
		if len(catClauses) > 0 {
			clauses = append(clauses, "("+strings.Join(catClauses, " OR ")+")")
		}
	}
	if f.Season > 0 {
		var patterns []string
		if f.Episode > 0 {
			patterns = []string{
				fmt.Sprintf("%%s%02de%02d%%", f.Season, f.Episode),
				fmt.Sprintf("%%%dx%02d%%", f.Season, f.Episode),
			}
		}
		// A season pack satisfies an episode request too.
		patterns = append(patterns, fmt.Sprintf("%%s%02d%%", f.Season))
		var seasonClauses []string
		for _, pattern := range patterns {
			seasonClauses = append(seasonClauses, `search_title LIKE ? ESCAPE '\'`)
			args = append(args, pattern)
		}
		clauses = append(clauses, "("+strings.Join(seasonClauses, " OR ")+")")
	}
	if len(clauses) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(clauses, " AND "), args
}

func orderBy(sort string) string {
	switch sort {
	case "seeders":
		return "seeders DESC, published_at DESC"
	case "size":
		return "size DESC, published_at DESC"
	case "title":
		return "search_title ASC"
	case "seen":
		return "last_seen_at DESC, id DESC"
	default:
		return "published_at DESC, id DESC"
	}
}

func categoryPattern(raw string) string {
	category := strings.TrimSpace(raw)
	if category == "" {
		return ""
	}
	for _, r := range category {
		if r < '0' || r > '9' {
			return ""
		}
	}
	// A top-level Torznab category (5000) selects its whole subtree (5xxx).
	if len(category) == 4 && strings.HasSuffix(category, "000") {
		return category[:1] + "%"
	}
	return category
}

func searchTokens(query string) []string {
	fields := strings.Fields(NormalizeSearch(query))
	out := make([]string, 0, len(fields))
	for _, field := range fields {
		if field != "" {
			out = append(out, field)
		}
	}
	return out
}

// NormalizeSearch turns a title into the lowercase, punctuation-free form
// stored in search_title.
func NormalizeSearch(text string) string {
	return textutil.NormalizeTitle(text)
}

func normalizeID(raw string) string {
	return textutil.NormalizeID(raw)
}

func escapeLike(token string) string {
	return textutil.EscapeLike(token)
}

func scanRelease(scanner interface{ Scan(...any) error }) (Release, error) {
	var out Release
	var published, torrentFetched, firstSeen, lastSeen int64
	var sourceID sql.NullInt64
	var torrent []byte
	var favorite, hidden int
	err := scanner.Scan(&out.ID, &out.DedupKey, &out.InfoHash, &out.IMDbID, &out.TMDbID,
		&out.TVDbID, &out.Title, &out.SearchTitle, &out.Size, &out.Seeders,
		&out.Leechers, &out.Category, &published, &sourceID, &out.SourceName,
		&out.GUID, &out.Magnet, &torrent, &out.TorrentSize, &torrentFetched,
		&firstSeen, &lastSeen, &out.TimesSeen, &favorite, &hidden)
	if err != nil {
		return Release{}, err
	}
	out.PublishedAt = unixTime(published)
	out.TorrentFetchedAt = unixTime(torrentFetched)
	out.FirstSeenAt = unixTime(firstSeen)
	out.LastSeenAt = unixTime(lastSeen)
	if sourceID.Valid {
		out.SourceID = sourceID.Int64
	}
	out.Torrent = torrent
	out.Favorite = favorite != 0
	out.Hidden = hidden != 0
	return out, nil
}
