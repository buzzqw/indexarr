package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// TitleForExternalID returns the title of the most recently seen cached row
// carrying one of the given identifiers. A client that sends only an IMDb,
// TMDb or TVDb id cannot be answered by a source that accepts only keywords;
// the cached title lets that source join the live fan-out.
func (s *Store) TitleForExternalID(ctx context.Context, imdb, tmdb, tvdb string) (string, bool, error) {
	var clauses []string
	var args []any
	for _, candidate := range []struct{ column, value string }{
		{"imdb_id", imdb}, {"tmdb_id", tmdb}, {"tvdb_id", tvdb},
	} {
		if id := normalizeID(candidate.value); id != "" {
			clauses = append(clauses, candidate.column+" = ?")
			args = append(args, id)
		}
	}
	if len(clauses) == 0 {
		return "", false, nil
	}
	query := `SELECT title FROM releases WHERE (` + strings.Join(clauses, " OR ") +
		`) AND title <> '' ORDER BY last_seen_at DESC, id DESC LIMIT 1`
	var title string
	if err := s.db.QueryRowContext(ctx, query, args...).Scan(&title); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", false, nil
		}
		return "", false, err
	}
	return strings.TrimSpace(title), true, nil
}
