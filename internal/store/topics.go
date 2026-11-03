package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/buzzqw/indexarr/internal/textutil"
)

// Topic is one row of a forum's local history: a thread or a series with its
// address. It is not a release: the magnets are read only when a client grabs
// the topic, exactly as the site's own search does.
type Topic struct {
	ID          int64
	IndexerID   int64
	ForumID     string
	Title       string
	TitleKey    string
	URL         string
	Category    int
	FirstSeenAt time.Time
	LastSeenAt  time.Time
}

// TopicCursor is how far the walk of one forum has gone. NextStart is the
// first row of the next page; DoneAt is set when the walk reached the end of
// the listing and next_start went back to zero.
type TopicCursor struct {
	IndexerID int64
	ForumID   string
	NextStart int
	LastRunAt time.Time
	DoneAt    time.Time
}

func topicIndexMigration(tx *sql.Tx) error {
	for _, statement := range []string{
		`CREATE TABLE IF NOT EXISTS topic_index (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			indexer_id    INTEGER NOT NULL REFERENCES indexers(id) ON DELETE CASCADE,
			forum_id      TEXT    NOT NULL DEFAULT '',
			title         TEXT    NOT NULL,
			title_key     TEXT    NOT NULL DEFAULT '',
			url           TEXT    NOT NULL,
			category      INTEGER NOT NULL DEFAULT 0,
			first_seen_at INTEGER NOT NULL DEFAULT 0,
			last_seen_at  INTEGER NOT NULL DEFAULT 0,
			UNIQUE(indexer_id, url)
		)`,
		`CREATE INDEX IF NOT EXISTS topic_index_key ON topic_index(indexer_id, title_key)`,
		`CREATE INDEX IF NOT EXISTS topic_index_forum ON topic_index(indexer_id, forum_id)`,
		`CREATE TABLE IF NOT EXISTS topic_index_cursors (
			indexer_id  INTEGER NOT NULL REFERENCES indexers(id) ON DELETE CASCADE,
			forum_id    TEXT    NOT NULL,
			next_start  INTEGER NOT NULL DEFAULT 0,
			last_run_at INTEGER NOT NULL DEFAULT 0,
			done_at     INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (indexer_id, forum_id)
		)`,
	} {
		if _, err := tx.Exec(statement); err != nil {
			return err
		}
	}
	return nil
}

// UpsertTopics stores the topics of one listing page. A topic already known
// keeps its first_seen_at and gets a new last_seen_at, so the history never
// loses an old thread and the list can show what is still published. It
// returns how many rows were new.
func (s *Store) UpsertTopics(ctx context.Context, indexerID int64, topics []Topic) (int, error) {
	if indexerID <= 0 || len(topics) == 0 {
		return 0, nil
	}
	added := 0
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	upsert := `INSERT INTO topic_index
		(indexer_id, forum_id, title, title_key, url, category, first_seen_at, last_seen_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(indexer_id, url) DO UPDATE SET
			title = excluded.title,
			title_key = excluded.title_key,
			forum_id = excluded.forum_id,
			category = excluded.category,
			last_seen_at = excluded.last_seen_at`
	for _, topic := range topics {
		title := strings.TrimSpace(topic.Title)
		url := strings.TrimSpace(topic.URL)
		if title == "" || url == "" {
			continue
		}
		key := strings.TrimSpace(topic.TitleKey)
		if key == "" {
			key = textutil.NormalizeTitle(title)
		}
		first := topic.FirstSeenAt
		if first.IsZero() {
			first = time.Now()
		}
		seen := topic.LastSeenAt
		if seen.IsZero() {
			seen = time.Now()
		}
		var existing int64
		if err := tx.QueryRowContext(ctx,
			`SELECT id FROM topic_index WHERE indexer_id = ? AND url = ?`, indexerID, url).Scan(&existing); err != nil && err != sql.ErrNoRows {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, upsert,
			indexerID, strings.TrimSpace(topic.ForumID), title, key, url, topic.Category,
			first.Unix(), seen.Unix()); err != nil {
			return 0, fmt.Errorf("store topic %q: %w", title, err)
		}
		if existing == 0 {
			added++
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return added, nil
}

// SearchTopics finds the stored topics whose title carries every word of the
// query, newest sighting first. The catalogue is a fallback that always
// answers, so a missing word simply narrows the result.
func (s *Store) SearchTopics(ctx context.Context, indexerID int64, terms []string, limit int) ([]Topic, error) {
	if indexerID <= 0 {
		return nil, nil
	}
	if limit <= 0 {
		limit = 50
	}
	var clauses []string
	var args []any
	clauses = append(clauses, `indexer_id = ?`)
	args = append(args, indexerID)
	for _, term := range terms {
		term = strings.TrimSpace(term)
		if term == "" {
			continue
		}
		clauses = append(clauses, `title_key LIKE ? ESCAPE '\'`)
		args = append(args, "%"+escapeLike(term)+"%")
	}
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, `SELECT id, indexer_id, forum_id, title, title_key, url, category,
			first_seen_at, last_seen_at
		FROM topic_index WHERE `+strings.Join(clauses, " AND ")+`
		ORDER BY last_seen_at DESC, id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Topic
	for rows.Next() {
		var topic Topic
		var first, seen int64
		if err := rows.Scan(&topic.ID, &topic.IndexerID, &topic.ForumID, &topic.Title, &topic.TitleKey,
			&topic.URL, &topic.Category, &first, &seen); err != nil {
			return nil, err
		}
		topic.FirstSeenAt = time.Unix(first, 0)
		topic.LastSeenAt = time.Unix(seen, 0)
		out = append(out, topic)
	}
	return out, rows.Err()
}

// TopicCount returns how many topics the history holds for one indexer.
func (s *Store) TopicCount(ctx context.Context, indexerID int64) (int64, error) {
	var count int64
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM topic_index WHERE indexer_id = ?`, indexerID).Scan(&count)
	return count, err
}

// TopicCursors returns the walk state of every forum of one indexer.
func (s *Store) TopicCursors(ctx context.Context, indexerID int64) (map[string]TopicCursor, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT forum_id, next_start, last_run_at, done_at
		FROM topic_index_cursors WHERE indexer_id = ?`, indexerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]TopicCursor)
	for rows.Next() {
		var cursor TopicCursor
		var last, done int64
		cursor.IndexerID = indexerID
		if err := rows.Scan(&cursor.ForumID, &cursor.NextStart, &last, &done); err != nil {
			return nil, err
		}
		cursor.LastRunAt = time.Unix(last, 0)
		cursor.DoneAt = time.Unix(done, 0)
		out[cursor.ForumID] = cursor
	}
	return out, rows.Err()
}

// SaveTopicCursor stores the walk state of one forum.
func (s *Store) SaveTopicCursor(ctx context.Context, cursor TopicCursor) error {
	if cursor.IndexerID <= 0 || strings.TrimSpace(cursor.ForumID) == "" {
		return nil
	}
	last := cursor.LastRunAt
	if last.IsZero() {
		last = time.Now()
	}
	done := int64(0)
	if !cursor.DoneAt.IsZero() {
		done = cursor.DoneAt.Unix()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO topic_index_cursors
		(indexer_id, forum_id, next_start, last_run_at, done_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(indexer_id, forum_id) DO UPDATE SET
			next_start = excluded.next_start,
			last_run_at = excluded.last_run_at,
			done_at = excluded.done_at`,
		cursor.IndexerID, strings.TrimSpace(cursor.ForumID), cursor.NextStart, last.Unix(), done)
	return err
}

// DeleteTopics drops the history of one indexer. It is used when the indexer
// stops keeping a catalogue, so a stale list never answers a search.
func (s *Store) DeleteTopics(ctx context.Context, indexerID int64) error {
	if indexerID <= 0 {
		return nil
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM topic_index WHERE indexer_id = ?`, indexerID); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM topic_index_cursors WHERE indexer_id = ?`, indexerID)
	return err
}
