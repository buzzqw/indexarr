package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// APIKey is an additional Torznab key. The primary key stays in settings so
// existing clients and the endpoint shown in the UI keep working.
type APIKey struct {
	ID         int64
	Name       string
	Key        string
	CreatedAt  time.Time
	LastUsedAt time.Time
	Requests   int64
}

// ListAPIKeys returns the additional keys, newest first.
func (s *Store) ListAPIKeys(ctx context.Context) ([]APIKey, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, key, created_at, last_used_at, requests
		 FROM api_keys ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []APIKey
	for rows.Next() {
		var key APIKey
		var created, lastUsed int64
		if err := rows.Scan(&key.ID, &key.Name, &key.Key, &created, &lastUsed, &key.Requests); err != nil {
			return nil, err
		}
		key.CreatedAt = unixTime(created)
		key.LastUsedAt = unixTime(lastUsed)
		out = append(out, key)
	}
	return out, rows.Err()
}

// AddAPIKey creates an additional key with a human-readable name.
func (s *Store) AddAPIKey(ctx context.Context, name string) (APIKey, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "client"
	}
	if len(name) > 80 {
		name = name[:80]
	}
	key, err := NewAPIKey()
	if err != nil {
		return APIKey{}, err
	}
	now := time.Now()
	var id int64
	err = s.db.QueryRowContext(ctx,
		`INSERT INTO api_keys(name, key, created_at) VALUES(?, ?, ?) RETURNING id`,
		name, key, now.Unix()).Scan(&id)
	if err != nil {
		return APIKey{}, fmt.Errorf("add API key: %w", err)
	}
	return APIKey{ID: id, Name: name, Key: key, CreatedAt: now}, nil
}

// DeleteAPIKey revokes one additional key.
func (s *Store) DeleteAPIKey(ctx context.Context, id int64) (bool, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM api_keys WHERE id = ?`, id)
	if err != nil {
		return false, err
	}
	affected, _ := res.RowsAffected()
	return affected > 0, nil
}

// LookupAPIKey finds an additional key by its secret value.
func (s *Store) LookupAPIKey(ctx context.Context, key string) (APIKey, bool, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return APIKey{}, false, nil
	}
	var out APIKey
	var created, lastUsed int64
	err := s.db.QueryRowContext(ctx,
		`SELECT id, name, key, created_at, last_used_at, requests
		 FROM api_keys WHERE key = ?`, key).
		Scan(&out.ID, &out.Name, &out.Key, &created, &lastUsed, &out.Requests)
	if errors.Is(err, sql.ErrNoRows) {
		return APIKey{}, false, nil
	}
	if err != nil {
		return APIKey{}, false, err
	}
	out.CreatedAt = unixTime(created)
	out.LastUsedAt = unixTime(lastUsed)
	return out, true, nil
}

// TouchAPIKey records a successful use of an additional key.
func (s *Store) TouchAPIKey(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE api_keys SET last_used_at = ?, requests = requests + 1 WHERE id = ?`,
		time.Now().Unix(), id)
	return err
}
