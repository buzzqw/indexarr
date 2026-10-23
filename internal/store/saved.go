package store

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// SavedSearch is a named query string for the Releases page.
type SavedSearch struct {
	ID        int64
	Name      string
	Query     string
	CreatedAt time.Time
}

// ListSavedSearches returns saved searches ordered by name.
func (s *Store) ListSavedSearches(ctx context.Context) ([]SavedSearch, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, query, created_at FROM saved_searches ORDER BY name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SavedSearch
	for rows.Next() {
		var search SavedSearch
		var created int64
		if err := rows.Scan(&search.ID, &search.Name, &search.Query, &created); err != nil {
			return nil, err
		}
		search.CreatedAt = unixTime(created)
		out = append(out, search)
	}
	return out, rows.Err()
}

// AddSavedSearch stores a named query, updating it when the name exists.
func (s *Store) AddSavedSearch(ctx context.Context, name, query string) (int64, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return 0, fmt.Errorf("saved search name is required")
	}
	var id int64
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO saved_searches(name, query, created_at) VALUES(?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET query = excluded.query
		RETURNING id`, name, strings.TrimSpace(query), time.Now().Unix()).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("save search: %w", err)
	}
	return id, nil
}

// DeleteSavedSearch removes one saved search.
func (s *Store) DeleteSavedSearch(ctx context.Context, id int64) (bool, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM saved_searches WHERE id = ?`, id)
	if err != nil {
		return false, err
	}
	affected, _ := res.RowsAffected()
	return affected > 0, nil
}
