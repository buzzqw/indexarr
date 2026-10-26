package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

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
	GUID             string
	Magnet           string
	Torrent          []byte
	TorrentSize      int64
	TorrentFetchedAt time.Time
	Protocol         string
	Payload          []byte
	PayloadType      string
	FirstSeenAt      time.Time
	LastSeenAt       time.Time
	TimesSeen        int64
	Favorite         bool
	Hidden           bool
	// DownloadedAt is set when a client grabbed this release. A stored-source
	// retention pass never removes a downloaded release.
	DownloadedAt time.Time
}

// Filter selects releases for the web UI and the Torznab endpoint.
type Filter struct {
	Query         string
	Include       []string // parsed include terms (normal: normalized; regex: patterns)
	Exclude       []string // parsed exclude terms, applied with NOT
	Regex         bool
	IndexerID     int64
	IndexerIDs    []int64 // native provenance selection; takes effect when IndexerID is zero
	Categories    []string
	Season        int
	Episode       int
	Year          int
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
	// FilesOnly searches the file names parsed from stored .torrent files
	// instead of release titles.
	FilesOnly bool
	// ExcludeAdult removes the XXX category (6xxx) from the results.
	ExcludeAdult bool
}

const releaseColumns = `id, dedup_key, infohash, imdb_id, tmdb_id, tvdb_id, title,
	search_title, size, seeders, leechers, category, published_at,
	guid, magnet, torrent, torrent_size, torrent_fetched_at,
	first_seen_at, last_seen_at, times_seen, favorite, hidden,
	protocol, payload, payload_type, downloaded_at`

// ErrNoPayload reports that a release has neither a torrent nor a generic
// payload stored.
var ErrNoPayload = errors.New("store: release has no payload")

func normalizeReleaseProtocol(protocol string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(protocol)) {
	case "", "torrent":
		return "torrent", nil
	case "usenet":
		return "usenet", nil
	default:
		return "", fmt.Errorf("store: invalid release protocol %q", protocol)
	}
}

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
	protocolProvided := strings.TrimSpace(rel.Protocol) != ""
	protocol, err := normalizeReleaseProtocol(rel.Protocol)
	if err != nil {
		return false, err
	}
	rel.Protocol = protocol
	// A row without an infohash is matched conservatively against the rows
	// already in the cache (same normalized title, rounding-compatible size,
	// every identifying feature equal). Reusing the keeper's key turns the
	// insert into an update and records the new indexer in release_sources,
	// so a live search cannot grow a second copy between cleanup passes.
	if strings.TrimSpace(rel.InfoHash) == "" {
		if keeper, found, sameErr := s.FindSameRelease(ctx, rel); sameErr != nil {
			return false, sameErr
		} else if found {
			rel.DedupKey = keeper.DedupKey
		}
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
	var torrent any
	if len(rel.Torrent) > 0 {
		torrent = rel.Torrent
	}
	var payload any
	if len(rel.Payload) > 0 {
		payload = rel.Payload
	}
	var id int64
	var timesSeen int64
	err = s.db.QueryRowContext(ctx, `
		INSERT INTO releases(dedup_key, infohash, imdb_id, tmdb_id, tvdb_id, title,
			search_title, size, seeders, leechers, category, published_at,
			guid, magnet, torrent, torrent_size, torrent_fetched_at,
			first_seen_at, last_seen_at, protocol, payload, payload_type, times_seen)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1)
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
			guid = CASE WHEN excluded.guid <> '' THEN excluded.guid ELSE releases.guid END,
			magnet = CASE WHEN excluded.magnet <> '' THEN excluded.magnet ELSE releases.magnet END,
			torrent = COALESCE(excluded.torrent, releases.torrent),
			torrent_size = CASE WHEN excluded.torrent IS NOT NULL THEN excluded.torrent_size ELSE releases.torrent_size END,
			torrent_fetched_at = CASE WHEN excluded.torrent IS NOT NULL THEN excluded.torrent_fetched_at ELSE releases.torrent_fetched_at END,
			protocol = CASE WHEN ? <> 0 THEN excluded.protocol ELSE releases.protocol END,
			payload = COALESCE(excluded.payload, releases.payload),
			payload_type = CASE WHEN excluded.payload_type <> '' THEN excluded.payload_type ELSE releases.payload_type END,
			last_seen_at = excluded.last_seen_at,
			times_seen = releases.times_seen + 1
		RETURNING id, times_seen`,
		rel.DedupKey, rel.InfoHash, rel.IMDbID, rel.TMDbID, rel.TVDbID,
		rel.Title, rel.SearchTitle, rel.Size, rel.Seeders, rel.Leechers,
		rel.Category, rel.PublishedAt.Unix(),
		rel.GUID, rel.Magnet, torrent, rel.TorrentSize,
		rel.TorrentFetchedAt.Unix(), rel.FirstSeenAt.Unix(), rel.LastSeenAt.Unix(),
		rel.Protocol, payload, rel.PayloadType, boolInt(protocolProvided)).Scan(&id, &timesSeen)
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

// ReleaseByExternalID resolves a Torznab item identifier that is not a
// numeric row id: clients may call t=get with the <guid> (an infohash or the
// fallback dedup key), or with the infohash itself.
func (s *Store) ReleaseByExternalID(ctx context.Context, id string) (Release, bool, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return Release{}, false, nil
	}
	lower := strings.ToLower(id)
	// Torznab items without an infohash use "indexarr-<dedup_key>" as guid.
	key := strings.TrimPrefix(id, "indexarr-")
	row := s.db.QueryRowContext(ctx,
		`SELECT `+releaseColumns+` FROM releases
		 WHERE guid = ? OR infohash = ? OR dedup_key = ? OR dedup_key = ? OR dedup_key = ?
		 ORDER BY id LIMIT 1`,
		id, lower, id, key, "indexarr-"+key)
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

// PayloadByReleaseID resolves the downloadable bytes of a release. It prefers
// the legacy torrent column, falls back to the generic payload and defaults the
// content type to application/x-bittorrent (application/x-nzb for usenet).
func (s *Store) PayloadByReleaseID(ctx context.Context, id int64) ([]byte, string, string, error) {
	var torrent, payload []byte
	var payloadType, protocol string
	err := s.db.QueryRowContext(ctx,
		`SELECT torrent, payload, payload_type, protocol FROM releases WHERE id = ?`, id).
		Scan(&torrent, &payload, &payloadType, &protocol)
	if err != nil {
		return nil, "", "", err
	}
	protocol = strings.ToLower(strings.TrimSpace(protocol))
	if protocol == "" {
		protocol = "torrent"
	}
	if len(torrent) > 0 {
		contentType := strings.TrimSpace(payloadType)
		if contentType == "" || contentType == "application/x-nzb" {
			contentType = "application/x-bittorrent"
		}
		return torrent, contentType, protocol, nil
	}
	if len(payload) > 0 {
		contentType := strings.TrimSpace(payloadType)
		if contentType == "" {
			contentType = "application/x-bittorrent"
			if protocol == "usenet" {
				contentType = "application/x-nzb"
			}
		}
		return payload, contentType, protocol, nil
	}
	return nil, "", protocol, ErrNoPayload
}

// SavePayload stores a payload fetched by the opaque download resolver. A
// torrent payload goes to the legacy torrent column so the rest of indexarr
// keeps reading it; a usenet payload goes to the generic payload column.
func (s *Store) SavePayload(ctx context.Context, id int64, body []byte, contentType, protocol string) error {
	if len(body) == 0 {
		return errors.New("store: empty payload")
	}
	protocol, err := normalizeReleaseProtocol(protocol)
	if err != nil {
		return err
	}
	now := time.Now().Unix()
	var res sql.Result
	if protocol == "usenet" {
		if strings.TrimSpace(contentType) == "" {
			contentType = "application/x-nzb"
		}
		res, err = s.db.ExecContext(ctx, `
			UPDATE releases SET protocol = 'usenet', payload = ?, payload_type = ?,
				torrent = NULL, torrent_size = 0, torrent_fetched_at = 0
			WHERE id = ?`, body, contentType, id)
	} else {
		res, err = s.db.ExecContext(ctx, `
			UPDATE releases SET protocol = 'torrent', torrent = ?, torrent_size = ?,
				torrent_fetched_at = ?, payload = NULL, payload_type = ''
			WHERE id = ?`, body, len(body), now, id)
	}
	if err != nil {
		return fmt.Errorf("store release payload: %w", err)
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// SearchReleases returns one page of releases and the total matching count.
func (s *Store) SearchReleases(ctx context.Context, f Filter) ([]Release, int64, error) {
	where := buildWhere(f)
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
	if where.fts {
		if releases, total, ok, err := s.searchTextFast(ctx, f, where, limit, offset); err != nil {
			return nil, 0, err
		} else if ok {
			return releases, total, nil
		}
	}
	total, err := s.countReleases(ctx, f, where)
	if err != nil {
		return nil, 0, err
	}
	// A text query is sorted in memory: selecting the page ids first keeps the
	// wide columns (magnet and payload blobs) out of the sorter, then one
	// primary-key lookup loads the page.
	if where.fts {
		ids, err := s.searchIDs(ctx, where, orderBy(f.Sort), limit, offset)
		if err != nil {
			return nil, 0, err
		}
		out, err := s.releasesByIDs(ctx, ids)
		return out, total, err
	}
	query := `SELECT ` + releaseColumns + ` FROM releases` + where.sql +
		` ORDER BY ` + orderBy(f.Sort) + ` LIMIT ? OFFSET ?`
	rows, err := s.db.QueryContext(ctx, query, append(where.args, limit, offset)...)
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

const (
	// fastTextMinMatches is the point where the SQL join starts building a
	// large ephemeral rowid index. Below it the regular query is already fast.
	fastTextMinMatches = 2000
	// fastTextMaxMatches bounds the rowids kept in memory for one request.
	fastTextMaxMatches = 200_000
)

// searchTextFast serves an unselective text query by intersecting the FTS
// rowids with the release ids in result order. The regular query materialises
// every match into an ephemeral SQLite index (about 30 ms at 50k rows); here
// the FTS index is read once and only a bounded prefix of the ordered table is
// scanned. It reports ok=false whenever the regular path should run instead.
func (s *Store) searchTextFast(ctx context.Context, f Filter, where whereClause, limit, offset int) ([]Release, int64, bool, error) {
	// Only queries whose ORDER BY is backed by an index can scan a prefix; a
	// sort of the whole table would be slower than the regular query. The id
	// OR group (imdbid/tmdbid/tvdbid) changes the predicate shape, so it keeps
	// the regular path too.
	if !where.fts || where.hasIDs || f.IncludeHidden {
		return nil, 0, false, nil
	}
	index, ok := orderedIndex(f.Sort)
	if !ok {
		// Only queries whose ORDER BY is backed by an index can scan a
		// prefix; sorting the whole table would be slower than the regular
		// query. The id OR group (imdbid/tmdbid/tvdbid) changes the predicate
		// shape, so it keeps the regular path too.
		return nil, 0, false, nil
	}
	ids, err := s.ftsRowIDs(ctx, where.ftsMatch, fastTextMaxMatches+1)
	if err != nil {
		return nil, 0, false, err
	}
	raw := int64(len(ids))
	if raw < fastTextMinMatches || raw > fastTextMaxMatches {
		return nil, 0, false, nil
	}
	// FTS returns rowids in index order, which is (almost always) ascending,
	// but the binary search below must not depend on that.
	slices.Sort(ids)

	nonText := buildWhereMode(f, false)
	var (
		total     int64
		candidate int64 // size of the non-text set the prefix is drawn from
	)
	if where.pureText && !f.FavoritesOnly {
		hidden, ok, err := s.hiddenMatches(ctx, ids)
		if err != nil {
			return nil, 0, false, err
		}
		if !ok {
			return nil, 0, false, nil
		}
		total = raw - hidden
		visible, err := s.visibleCount(ctx)
		if err != nil {
			return nil, 0, false, err
		}
		candidate = visible
	} else {
		// Count the intersection once, without an ephemeral SQL IN index: the
		// non-text ids are read in one aggregate and tested against the FTS set.
		if err := s.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM releases`+nonText.sql, nonText.args...).Scan(&candidate); err != nil {
			return nil, 0, false, err
		}
		if candidate > fastTextMaxMatches {
			// Keeping the ordered ids in memory is only worth it while the
			// table is modest; a very large cache keeps the streaming SQL path.
			return nil, 0, false, nil
		}
		if candidate > 0 {
			all, err := s.idsForWhere(ctx, nonText)
			if err != nil {
				return nil, 0, false, err
			}
			total = countIntersection(ids, all)
		}
	}
	if total == 0 || candidate == 0 {
		return nil, 0, true, nil
	}

	need := int64(offset + limit)
	// A prefix long enough to contain "need" matches under a uniform date
	// distribution, with a safety margin for skewed data.
	prefix := need * candidate / total * 2
	if prefix < need+16 {
		prefix = need + 16
	}
	if prefix > candidate {
		prefix = candidate
	}
	order := orderBy(f.Sort)
	candidates, err := s.orderedIDs(ctx, nonText, index, order, int(prefix))
	if err != nil {
		return nil, 0, false, err
	}
	page := intersectIDs(ids, candidates, offset, limit)
	if len(page) < limit && int64(len(candidates)) < candidate {
		// The prefix was too short (the newest rows did not contain enough
		// matches): read the whole candidate set once. Still cheaper than the
		// ephemeral index for the sizes this path accepts.
		candidates, err = s.orderedIDs(ctx, nonText, index, order, int(candidate))
		if err != nil {
			return nil, 0, false, err
		}
		page = intersectIDs(ids, candidates, offset, limit)
	}
	releases, err := s.releasesByIDs(ctx, page)
	return releases, total, true, err
}

// visibleCount returns how many releases are visible, that is not hidden by
// curation. Counting with a predicate (WHERE hidden = 0) makes SQLite visit
// every matching entry of the covering index: about 4 ms at 50k rows, which
// dominated an unselective text search. The unfiltered COUNT(*) uses the
// table page counter instead, and the hidden set is a curated minority, so
// counting it apart and subtracting is far cheaper. If curation ever grows
// past the visible set the cost stays bounded by the same index walk the
// filtered count always paid.
func (s *Store) visibleCount(ctx context.Context) (int64, error) {
	var all, hidden int64
	if err := s.db.QueryRowContext(ctx, `
		SELECT (SELECT COUNT(*) FROM releases),
		       (SELECT COUNT(*) FROM releases WHERE hidden = 1)`).Scan(&all, &hidden); err != nil {
		return 0, err
	}
	if hidden > all {
		return 0, nil
	}
	return all - hidden, nil
}

// hiddenMatches counts how many of the given FTS matches are hidden. Reading
// the hidden ids is cheap because curation is normally a small subset. The
// second return value is false when curation is too large to hold in memory,
// in which case the caller should use the regular query.
func (s *Store) hiddenMatches(ctx context.Context, matched []int64) (int64, bool, error) {
	where := whereClause{sql: " WHERE hidden = 1"}
	hidden, err := s.idsForWhere(ctx, where)
	if err != nil {
		return 0, false, err
	}
	if len(hidden) > fastTextMaxMatches {
		return 0, false, nil
	}
	return countIntersection(matched, hidden), true, nil
}

// ftsRowIDs returns at most limit matching release ids. The caller sorts them.
// The rowids are concatenated in SQLite and parsed here: one row crossing the
// driver is much cheaper than 20k+ individually scanned rows.
func (s *Store) ftsRowIDs(ctx context.Context, match string, limit int) ([]int64, error) {
	var joined sql.NullString
	if err := s.db.QueryRowContext(ctx, `SELECT group_concat(rowid, ',') FROM (
		SELECT rowid FROM releases_fts WHERE releases_fts MATCH ? LIMIT ?
	)`, match, limit).Scan(&joined); err != nil {
		return nil, err
	}
	return parseInts(joined)
}

// idsForWhere returns every id matching the predicate, in no particular order.
func (s *Store) idsForWhere(ctx context.Context, where whereClause) ([]int64, error) {
	var joined sql.NullString
	if err := s.db.QueryRowContext(ctx,
		`SELECT group_concat(id, ',') FROM releases`+where.sql, where.args...).Scan(&joined); err != nil {
		return nil, err
	}
	return parseInts(joined)
}

// orderedIDs reads the first limit ids in result order, forcing the index that
// provides that order so SQLite cannot prefer a filter index and sort instead.
func (s *Store) orderedIDs(ctx context.Context, where whereClause, index, order string, limit int) ([]int64, error) {
	var joined sql.NullString
	query := `SELECT group_concat(id, ',') FROM (
		SELECT id FROM releases INDEXED BY ` + index + where.sql + ` ORDER BY ` + order + ` LIMIT ?
	)`
	if err := s.db.QueryRowContext(ctx, query, append(where.args, limit)...).Scan(&joined); err != nil {
		return nil, err
	}
	return parseInts(joined)
}

func parseInts(joined sql.NullString) ([]int64, error) {
	if !joined.Valid || joined.String == "" {
		return nil, nil
	}
	raw := joined.String
	ids := make([]int64, 0, len(raw)/7+1)
	for i := 0; i < len(raw); {
		end := strings.IndexByte(raw[i:], ',')
		part := raw[i:]
		if end >= 0 {
			part = raw[i : i+end]
			i += end + 1
		} else {
			i = len(raw)
		}
		id, err := strconv.ParseInt(part, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse rowid %q: %w", part, err)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// countIntersection returns how many candidates appear in the sorted set.
func countIntersection(sorted, candidates []int64) int64 {
	var count int64
	for _, id := range candidates {
		i := sort.Search(len(sorted), func(i int) bool { return sorted[i] >= id })
		if i < len(sorted) && sorted[i] == id {
			count++
		}
	}
	return count
}

// intersectIDs keeps the candidates that appear in the sorted set, applying
// offset and limit.
func intersectIDs(sorted, candidates []int64, offset, limit int) []int64 {
	out := make([]int64, 0, limit)
	matched := 0
	for _, id := range candidates {
		i := sort.Search(len(sorted), func(i int) bool { return sorted[i] >= id })
		if i == len(sorted) || sorted[i] != id {
			continue
		}
		if matched < offset {
			matched++
			continue
		}
		if len(out) == limit {
			break
		}
		out = append(out, id)
		matched++
	}
	return out
}

// orderedIndex names the index that provides the requested order.
func orderedIndex(sort string) (string, bool) {
	switch sort {
	case "", "recent":
		return "releases_visible_published_id", true
	case "seen":
		return "releases_visible_seen_id", true
	default:
		return "", false
	}
}

// countReleases counts the matches. For a pure text query it reads the FTS
// index alone (no row lookups) and subtracts the matches hidden by curation;
// the general query has to visit every matching row.
func (s *Store) countReleases(ctx context.Context, f Filter, where whereClause) (int64, error) {
	if where.pureText {
		var total int64
		if err := s.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM releases_fts WHERE releases_fts MATCH ?`, where.ftsMatch).
			Scan(&total); err != nil {
			return 0, err
		}
		if !f.IncludeHidden {
			var hidden int64
			if err := s.db.QueryRowContext(ctx, `
				SELECT COUNT(*) FROM releases
				WHERE hidden = 1
				  AND id IN (SELECT rowid FROM releases_fts WHERE releases_fts MATCH ?)`,
				where.ftsMatch).Scan(&hidden); err != nil {
				return 0, err
			}
			total -= hidden
		}
		if f.FavoritesOnly {
			var notFavorite int64
			if err := s.db.QueryRowContext(ctx, `
				SELECT COUNT(*) FROM releases
				WHERE favorite = 0
				  AND id IN (SELECT rowid FROM releases_fts WHERE releases_fts MATCH ?)`,
				where.ftsMatch).Scan(&notFavorite); err != nil {
				return 0, err
			}
			total -= notFavorite
		}
		if total < 0 {
			total = 0
		}
		return total, nil
	}
	var total int64
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM releases`+where.sql, where.args...).Scan(&total); err != nil {
		return 0, err
	}
	return total, nil
}

// searchIDs returns one page of ids in result order.
func (s *Store) searchIDs(ctx context.Context, where whereClause, order string, limit, offset int) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id FROM releases`+where.sql+` ORDER BY `+order+` LIMIT ? OFFSET ?`,
		append(where.args, limit, offset)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// releasesByIDs loads a page of releases preserving the caller's id order.
func (s *Store) releasesByIDs(ctx context.Context, ids []int64) ([]Release, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+releaseColumns+` FROM releases WHERE id IN (`+strings.Join(placeholders, ",")+`)`,
		args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byID := make(map[int64]Release, len(ids))
	for rows.Next() {
		rel, err := scanRelease(rows)
		if err != nil {
			return nil, err
		}
		byID[rel.ID] = rel
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]Release, 0, len(ids))
	for _, id := range ids {
		if rel, ok := byID[id]; ok {
			out = append(out, rel)
		}
	}
	return out, nil
}

// ListIndexerReleases returns the newest releases seen from one indexer. It
// backs the "recent releases" strip of the source detail page: that page shows
// ten rows and never needs the total, so it skips the COUNT that would scan
// every visible release and probe the provenance table for each one.
func (s *Store) ListIndexerReleases(ctx context.Context, indexerID int64, limit int) ([]Release, error) {
	if indexerID <= 0 {
		return nil, nil
	}
	if limit <= 0 || limit > 100 {
		limit = 10
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+releaseColumns+` FROM releases
		WHERE hidden = 0
		  AND EXISTS (SELECT 1 FROM release_sources rs
		              WHERE rs.release_id = releases.id AND rs.indexer_id = ?)
		ORDER BY last_seen_at DESC, id DESC LIMIT ?`, indexerID, limit)
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
	Releases        int64
	Torrents        int64
	TorrentBytes    int64
	Indexers        int64
	EnabledIndexers int64
	Runs24h         int64
	Errors24h       int64
	OldestSeen      time.Time
	NewestSeen      time.Time
	DBBytes         int64
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
		FROM indexers`).Scan(&out.Indexers, &out.EnabledIndexers); err != nil {
		return out, err
	}
	cutoff := time.Now().Add(-24 * time.Hour).Unix()
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*), COALESCE(SUM(CASE WHEN status = 'error' THEN 1 ELSE 0 END), 0)
		FROM indexer_history WHERE kind = 'run' AND started_at >= ?`, cutoff).
		Scan(&out.Runs24h, &out.Errors24h); err != nil {
		return out, err
	}
	out.OldestSeen = unixTime(oldest)
	out.NewestSeen = unixTime(newest)
	out.DBBytes, _ = s.SizeBytes()
	return out, nil
}

// whereClause is a built SQL predicate. ftsMatch is the combined FTS5 MATCH
// expression and pureText reports that the only predicates are that expression
// plus the visibility filters, which allows counting from the index alone.
type whereClause struct {
	sql      string
	args     []any
	fts      bool
	ftsMatch string
	pureText bool
	hasIDs   bool
}

func buildWhere(f Filter) whereClause {
	return buildWhereMode(f, true)
}

// buildWhereMode builds the full predicate or, with withText=false, the same
// predicate without the FTS term. The fast text path intersects the two sets.
func buildWhereMode(f Filter, withText bool) whereClause {
	var clauses []string
	var args []any
	// nonText is set by every predicate that needs a row lookup. A pure text
	// query keeps it false so its total can be counted without touching rows.
	nonText := false

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
	if len(idClauses) > 0 {
		nonText = true
	}
	includes := f.Include
	excludes := f.Exclude
	if len(includes) == 0 && len(excludes) == 0 && strings.TrimSpace(f.Query) != "" {
		includes = searchTokens(f.Query)
	}
	// Several FTS terms are combined into one MATCH expression: FTS evaluates
	// them in a single index pass instead of one subquery per term.
	var ftsPhrases []string
	var includeClauses []string
	if f.FilesOnly {
		nonText = true
		var filePhrases []string
		var fileLikes []string
		for _, token := range includes {
			if phrase, ok := ftsPhrase(token); ok {
				filePhrases = append(filePhrases, phrase)
				continue
			}
			fileLikes = append(fileLikes, "%"+escapeLike(token)+"%")
		}
		if len(filePhrases) > 0 {
			includeClauses = append(includeClauses,
				`id IN (SELECT release_id FROM release_files_fts WHERE release_files_fts MATCH ?)`)
			args = append(args, strings.Join(filePhrases, " AND "))
		}
		for _, like := range fileLikes {
			includeClauses = append(includeClauses,
				`EXISTS (SELECT 1 FROM release_files rf WHERE rf.release_id = releases.id AND rf.path LIKE ? ESCAPE '\')`)
			args = append(args, like)
		}
	} else if f.Regex {
		nonText = true
		for _, pattern := range includes {
			includeClauses = append(includeClauses, `title REGEXP ?`)
			args = append(args, pattern)
		}
	} else {
		for _, token := range includes {
			if phrase, ok := ftsPhrase(token); ok {
				if withText {
					ftsPhrases = append(ftsPhrases, phrase)
				}
				continue
			}
			// The trigram tokenizer needs at least three characters; keep the
			// old substring scan for shorter terms such as "4k" or "tv".
			nonText = true
			includeClauses = append(includeClauses, `search_title LIKE ? ESCAPE '\'`)
			args = append(args, "%"+escapeLike(token)+"%")
		}
	}
	ftsMatch := strings.Join(ftsPhrases, " AND ")
	if len(ftsPhrases) > 0 {
		includeClauses = append(includeClauses,
			`id IN (SELECT rowid FROM releases_fts WHERE releases_fts MATCH ?)`)
		args = append(args, ftsMatch)
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
		nonText = true
		for _, pattern := range excludes {
			clauses = append(clauses, `title NOT REGEXP ?`)
			args = append(args, pattern)
		}
	} else {
		for _, token := range excludes {
			nonText = true
			if phrase, ok := ftsPhrase(token); ok {
				clauses = append(clauses,
					`id NOT IN (SELECT rowid FROM releases_fts WHERE releases_fts MATCH ?)`)
				args = append(args, phrase)
				continue
			}
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
	if f.ExcludeAdult {
		nonText = true
		clauses = append(clauses, `(category = '' OR category NOT LIKE '6%')`)
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
	if f.MinSeeders > 0 || f.MinSize > 0 || f.MaxSize > 0 ||
		f.Quality != "" || f.SourceQuality != "" || f.Language != "" {
		nonText = true
	}

	if f.IndexerID > 0 {
		clauses = append(clauses, `EXISTS (SELECT 1 FROM release_sources rs
			WHERE rs.release_id = releases.id AND rs.indexer_id = ?)`)
		args = append(args, f.IndexerID)
		nonText = true
	} else if len(f.IndexerIDs) > 0 {
		placeholders := make([]string, 0, len(f.IndexerIDs))
		for _, id := range f.IndexerIDs {
			if id <= 0 {
				continue
			}
			placeholders = append(placeholders, "?")
			args = append(args, id)
		}
		if len(placeholders) > 0 {
			clauses = append(clauses, `EXISTS (SELECT 1 FROM release_sources rs
				WHERE rs.release_id = releases.id AND rs.indexer_id IN (`+strings.Join(placeholders, ",")+`))`)
			nonText = true
		}
	}
	if len(f.Categories) > 0 {
		var catClauses []string
		for _, category := range f.Categories {
			clause, catArgs, ok := categoryClause(category)
			if !ok {
				continue
			}
			catClauses = append(catClauses, clause)
			args = append(args, catArgs...)
		}
		if len(catClauses) > 0 {
			clauses = append(clauses, "("+strings.Join(catClauses, " OR ")+")")
			nonText = true
		}
	}
	if f.Season > 0 {
		nonText = true
		var seasonClauses []string
		if f.Episode > 0 {
			for _, pattern := range []string{
				fmt.Sprintf("%%s%02de%02d%%", f.Season, f.Episode),
				fmt.Sprintf("%%s%02d e%02d%%", f.Season, f.Episode),
				fmt.Sprintf("%%%dx%02d%%", f.Season, f.Episode),
			} {
				seasonClauses = append(seasonClauses, `search_title LIKE ? ESCAPE '\'`)
				args = append(args, pattern)
			}
			// A season pack satisfies an episode request, but an episode from
			// another week must not flood the result list. A pack has the
			// season marker and no episode marker.
			packPatterns := []string{
				fmt.Sprintf("%%s%02d%%", f.Season),
				fmt.Sprintf("%%season %d%%", f.Season),
				fmt.Sprintf("%%season %02d%%", f.Season),
			}
			var packLike []string
			for _, pattern := range packPatterns {
				packLike = append(packLike, `search_title LIKE ? ESCAPE '\'`)
				args = append(args, pattern)
			}
			packClause := "(" + strings.Join(packLike, " OR ") + ")" +
				` AND search_title NOT LIKE ? ESCAPE '\' AND search_title NOT LIKE ? ESCAPE '\'`
			args = append(args, fmt.Sprintf("%%s%02de%%", f.Season), fmt.Sprintf("%%%dx%%", f.Season))
			seasonClauses = append(seasonClauses, "("+packClause+")")
		} else {
			for _, pattern := range []string{
				fmt.Sprintf("%%s%02d%%", f.Season),
				fmt.Sprintf("%%season %d%%", f.Season),
				fmt.Sprintf("%%season %02d%%", f.Season),
				fmt.Sprintf("%%%dx%%", f.Season),
			} {
				seasonClauses = append(seasonClauses, `search_title LIKE ? ESCAPE '\'`)
				args = append(args, pattern)
			}
		}
		clauses = append(clauses, "("+strings.Join(seasonClauses, " OR ")+")")
	}
	if len(clauses) == 0 {
		return whereClause{}
	}
	return whereClause{
		sql:      " WHERE " + strings.Join(clauses, " AND "),
		args:     args,
		fts:      len(ftsPhrases) > 0,
		ftsMatch: ftsMatch,
		pureText: len(ftsPhrases) > 0 && !nonText,
		hasIDs:   len(idClauses) > 0,
	}
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
	case "popular":
		return "times_seen DESC, published_at DESC, id DESC"
	case "grabs":
		return `COALESCE((SELECT SUM(grab_count) FROM release_sources
			WHERE release_sources.release_id = releases.id), 0) DESC,
			published_at DESC, id DESC`
	default:
		return "published_at DESC, id DESC"
	}
}

// categoryClause turns a Torznab category into an index-friendly predicate. A
// top-level category (5000) selects its whole subtree and becomes a range on
// the first digit; a leaf category is an exact match. Both forms let SQLite use
// releases_visible_category_published, while the previous LIKE form always
// scanned because LIKE is case-insensitive by default.
func categoryClause(raw string) (string, []any, bool) {
	category := strings.TrimSpace(raw)
	if category == "" {
		return "", nil, false
	}
	for _, r := range category {
		if r < '0' || r > '9' {
			return "", nil, false
		}
	}
	// A top-level Torznab category (5000) selects its whole subtree (5xxx).
	if len(category) == 4 && strings.HasSuffix(category, "000") {
		prefix := category[:1]
		next := string(prefix[0] + 1)
		return `(category >= ? AND category < ?)`, []any{prefix, next}, true
	}
	return `category = ?`, []any{category}, true
}

// ftsPhrase turns one normalized search term into an FTS5 phrase. A phrase
// keeps FTS operators literal and matches the term as a substring with the
// trigram tokenizer. Terms shorter than three characters cannot be indexed by
// trigram and are handled by the LIKE fallback.
func ftsPhrase(token string) (string, bool) {
	token = strings.TrimSpace(token)
	if utf8.RuneCountInString(token) < 3 {
		return "", false
	}
	token = strings.ReplaceAll(token, `"`, `""`)
	return `"` + token + `"`, true
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
	var published, torrentFetched, firstSeen, lastSeen, downloadedAt int64
	var torrent, payload []byte
	var favorite, hidden int
	dest := []any{&out.ID, &out.DedupKey, &out.InfoHash, &out.IMDbID, &out.TMDbID,
		&out.TVDbID, &out.Title, &out.SearchTitle, &out.Size, &out.Seeders,
		&out.Leechers, &out.Category, &published,
		&out.GUID, &out.Magnet, &torrent, &out.TorrentSize, &torrentFetched,
		&firstSeen, &lastSeen, &out.TimesSeen, &favorite, &hidden,
		&out.Protocol, &payload, &out.PayloadType, &downloadedAt}
	if err := scanner.Scan(dest...); err != nil {
		return Release{}, err
	}
	out.PublishedAt = unixTime(published)
	out.TorrentFetchedAt = unixTime(torrentFetched)
	out.FirstSeenAt = unixTime(firstSeen)
	out.LastSeenAt = unixTime(lastSeen)
	out.DownloadedAt = unixTime(downloadedAt)
	out.Torrent = torrent
	out.Payload = payload
	if strings.TrimSpace(out.Protocol) == "" {
		out.Protocol = "torrent"
	}
	out.Favorite = favorite != 0
	out.Hidden = hidden != 0
	return out, nil
}
