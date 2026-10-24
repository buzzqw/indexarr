package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// DownloadClient is one configured torrent or Usenet client. Secrets are kept
// sealed by the vault in PasswordEnc/APIKeyEnc; the store never sees plaintext.
type DownloadClient struct {
	ID          int64
	Name        string
	Kind        string
	Protocol    string
	URL         string
	Username    string
	PasswordEnc string
	APIKeyEnc   string
	// ClearPassword and ClearAPIKey are request-only flags: an update with an
	// empty secret means "keep the stored value" unless the operator ticked the
	// remove checkbox.
	ClearPassword bool
	ClearAPIKey   bool
	Category      string
	SavePath      string
	Paused        bool
	Enabled       bool
	IsDefault     bool
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// Download clients share the protocol vocabulary of indexers.
const (
	ProtocolTorrent = "torrent"
	ProtocolUsenet  = "usenet"
)

// DefaultDownloadClientProtocol derives the protocol from the client kind.
func DefaultDownloadClientProtocol(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "sabnzbd", "nzbget":
		return ProtocolUsenet
	default:
		return ProtocolTorrent
	}
}

func normalizeDownloadClient(in DownloadClient) DownloadClient {
	in.Name = strings.TrimSpace(in.Name)
	in.Kind = strings.ToLower(strings.TrimSpace(in.Kind))
	if in.Kind == "" || in.Kind == "none" {
		in.Kind = "qbittorrent"
	}
	in.Protocol = strings.ToLower(strings.TrimSpace(in.Protocol))
	if in.Protocol != ProtocolUsenet {
		in.Protocol = ProtocolTorrent
	}
	in.URL = strings.TrimRight(strings.TrimSpace(in.URL), "/")
	in.Username = strings.TrimSpace(in.Username)
	in.Category = strings.TrimSpace(in.Category)
	in.SavePath = strings.TrimSpace(in.SavePath)
	return in
}

// ListDownloadClients returns every configured client, oldest first.
func (s *Store) ListDownloadClients(ctx context.Context) ([]DownloadClient, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, kind, protocol, url,
		username, COALESCE(password_enc, ''), COALESCE(apikey_enc, ''), category,
		save_path, paused, enabled, is_default, created_at, updated_at
		FROM download_clients ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list download clients: %w", err)
	}
	defer rows.Close()
	var out []DownloadClient
	for rows.Next() {
		var c DownloadClient
		var paused, enabled, isDefault int
		var created, updated int64
		if err := rows.Scan(&c.ID, &c.Name, &c.Kind, &c.Protocol, &c.URL,
			&c.Username, &c.PasswordEnc, &c.APIKeyEnc, &c.Category,
			&c.SavePath, &paused, &enabled, &isDefault, &created, &updated); err != nil {
			return nil, err
		}
		c.Paused = paused != 0
		c.Enabled = enabled != 0
		c.IsDefault = isDefault != 0
		c.CreatedAt = unixTime(created)
		c.UpdatedAt = unixTime(updated)
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetDownloadClient reads one client by id.
func (s *Store) GetDownloadClient(ctx context.Context, id int64) (DownloadClient, error) {
	var c DownloadClient
	var paused, enabled, isDefault int
	var created, updated int64
	err := s.db.QueryRowContext(ctx, `SELECT id, name, kind, protocol, url,
		username, COALESCE(password_enc, ''), COALESCE(apikey_enc, ''), category,
		save_path, paused, enabled, is_default, created_at, updated_at
		FROM download_clients WHERE id = ?`, id).
		Scan(&c.ID, &c.Name, &c.Kind, &c.Protocol, &c.URL, &c.Username,
			&c.PasswordEnc, &c.APIKeyEnc, &c.Category, &c.SavePath, &paused,
			&enabled, &isDefault, &created, &updated)
	if err != nil {
		if err == sql.ErrNoRows {
			return c, fmt.Errorf("download client not found")
		}
		return c, err
	}
	c.Paused = paused != 0
	c.Enabled = enabled != 0
	c.IsDefault = isDefault != 0
	c.CreatedAt = unixTime(created)
	c.UpdatedAt = unixTime(updated)
	return c, nil
}

// CreateDownloadClient inserts a client and makes it the default when asked or
// when it is the first row.
func (s *Store) CreateDownloadClient(ctx context.Context, in DownloadClient) (int64, error) {
	in = normalizeDownloadClient(in)
	now := time.Now().Unix()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM download_clients`).Scan(&count); err != nil {
		return 0, err
	}
	isDefault := in.IsDefault || count == 0
	if isDefault {
		if _, err := tx.ExecContext(ctx, `UPDATE download_clients SET is_default = 0`); err != nil {
			return 0, err
		}
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO download_clients
		(name, kind, protocol, url, username, password_enc, apikey_enc, category,
		 save_path, paused, enabled, is_default, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), ?, ?, ?, ?, ?, ?, ?)`,
		in.Name, in.Kind, in.Protocol, in.URL, in.Username, in.PasswordEnc,
		in.APIKeyEnc, in.Category, in.SavePath, boolInt(in.Paused),
		boolInt(in.Enabled), boolInt(isDefault), now, now)
	if err != nil {
		return 0, fmt.Errorf("create download client: %w", err)
	}
	id, _ := res.LastInsertId()
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return id, nil
}

// UpdateDownloadClient replaces the editable fields. Empty secret values keep
// the stored ones, matching the "empty means unchanged" rule of the forms.
func (s *Store) UpdateDownloadClient(ctx context.Context, in DownloadClient) error {
	in = normalizeDownloadClient(in)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if in.IsDefault {
		if _, err := tx.ExecContext(ctx, `UPDATE download_clients SET is_default = 0`); err != nil {
			return err
		}
	}
	password := interface{}(nil)
	if in.PasswordEnc != "" {
		password = in.PasswordEnc
	}
	apiKey := interface{}(nil)
	if in.APIKeyEnc != "" {
		apiKey = in.APIKeyEnc
	}
	res, err := tx.ExecContext(ctx, `UPDATE download_clients SET
		name = ?, kind = ?, protocol = ?, url = ?, username = ?,
		password_enc = CASE WHEN ? = 1 THEN NULL ELSE COALESCE(?, password_enc) END,
		apikey_enc = CASE WHEN ? = 1 THEN NULL ELSE COALESCE(?, apikey_enc) END,
		category = ?, save_path = ?, paused = ?, enabled = ?,
		is_default = ?, updated_at = ?
		WHERE id = ?`,
		in.Name, in.Kind, in.Protocol, in.URL, in.Username,
		boolInt(in.ClearPassword), password,
		boolInt(in.ClearAPIKey), apiKey,
		in.Category, in.SavePath, boolInt(in.Paused), boolInt(in.Enabled),
		boolInt(in.IsDefault), time.Now().Unix(), in.ID)
	if err != nil {
		return fmt.Errorf("update download client: %w", err)
	}
	if affected, _ := res.RowsAffected(); affected == 0 {
		return fmt.Errorf("download client not found")
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	// Never leave the list without a default: promote the oldest row.
	return s.ensureDownloadClientDefault(ctx)
}

// ensureDownloadClientDefault promotes the first enabled client when no row is
// marked as the default.
func (s *Store) ensureDownloadClientDefault(ctx context.Context) error {
	var defaults int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM download_clients WHERE is_default = 1`).Scan(&defaults); err != nil {
		return err
	}
	if defaults > 0 {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `UPDATE download_clients SET is_default = 1
		WHERE id = (SELECT id FROM download_clients ORDER BY enabled DESC, id LIMIT 1)`)
	return err
}

// SetDefaultDownloadClient marks one client as the default and clears the flag
// on the others.
func (s *Store) SetDefaultDownloadClient(ctx context.Context, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE download_clients SET is_default = 0`); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE download_clients SET is_default = 1, updated_at = ? WHERE id = ?`,
		time.Now().Unix(), id)
	if err != nil {
		return err
	}
	if affected, _ := res.RowsAffected(); affected == 0 {
		return fmt.Errorf("download client not found")
	}
	return tx.Commit()
}

// DeleteDownloadClient removes one client and keeps a default when others
// remain.
func (s *Store) DeleteDownloadClient(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM download_clients WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if affected, _ := res.RowsAffected(); affected == 0 {
		return fmt.Errorf("download client not found")
	}
	return s.ensureDownloadClientDefault(ctx)
}

// DownloadClientForProtocol resolves the rule torrent → torrent client,
// usenet → usenet client. An explicit default inside the protocol wins; the
// oldest enabled client of that protocol is the fallback.
func (s *Store) DownloadClientForProtocol(ctx context.Context, protocol string) (DownloadClient, error) {
	protocol = strings.ToLower(strings.TrimSpace(protocol))
	if protocol != ProtocolUsenet {
		protocol = ProtocolTorrent
	}
	var c DownloadClient
	var paused, enabled, isDefault int
	var created, updated int64
	err := s.db.QueryRowContext(ctx, `SELECT id, name, kind, protocol, url,
		username, COALESCE(password_enc, ''), COALESCE(apikey_enc, ''), category,
		save_path, paused, enabled, is_default, created_at, updated_at
		FROM download_clients WHERE enabled = 1 AND protocol = ?
		ORDER BY is_default DESC, id LIMIT 1`, protocol).
		Scan(&c.ID, &c.Name, &c.Kind, &c.Protocol, &c.URL, &c.Username,
			&c.PasswordEnc, &c.APIKeyEnc, &c.Category, &c.SavePath, &paused,
			&enabled, &isDefault, &created, &updated)
	if err == nil {
		c.Paused = paused != 0
		c.Enabled = enabled != 0
		c.IsDefault = isDefault != 0
		c.CreatedAt = unixTime(created)
		c.UpdatedAt = unixTime(updated)
		return c, nil
	}
	if err != sql.ErrNoRows {
		return c, err
	}
	return c, fmt.Errorf("no %s download client configured", protocol)
}
