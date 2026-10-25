package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/buzzqw/indexarr/internal/vault"
)

// Settings-level secret names. They are sealed in app_secrets, never in the
// settings table, so a database dump does not contain a usable Telegram token
// or webhook URL.
const (
	AppSecretNotifyWebhook       = "notify_webhook_url"
	AppSecretNotifyTelegramToken = "notify_telegram_token"
)

// AttachVault makes the store able to open and seal the settings-level
// secrets. It is normally called once, right after Open. Calling it twice is
// harmless and the last vault wins.
func (s *Store) AttachVault(v *vault.Vault) {
	s.vault = v
}

// HasVault reports whether secrets can be sealed and opened.
func (s *Store) HasVault() bool { return s != nil && s.vault != nil }

// PutAppSecret stores one sealed secret, replacing the previous value.
func (s *Store) PutAppSecret(ctx context.Context, key string, nonce, ciphertext []byte) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO app_secrets(key, nonce, ciphertext, updated_at)
		VALUES(?, ?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET
			nonce = excluded.nonce,
			ciphertext = excluded.ciphertext,
			updated_at = excluded.updated_at`,
		key, nonce, ciphertext, time.Now().Unix())
	if err != nil {
		return fmt.Errorf("put app secret %q: %w", key, err)
	}
	return nil
}

// GetAppSecret loads one sealed secret.
func (s *Store) GetAppSecret(ctx context.Context, key string) (nonce, ciphertext []byte, err error) {
	err = s.db.QueryRowContext(ctx,
		`SELECT nonce, ciphertext FROM app_secrets WHERE key = ?`, key).
		Scan(&nonce, &ciphertext)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, fmt.Errorf("app secret %q: %w", key, sql.ErrNoRows)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("get app secret %q: %w", key, err)
	}
	return nonce, ciphertext, nil
}

// DeleteAppSecret removes one secret. Deleting a missing key is not an error.
func (s *Store) DeleteAppSecret(ctx context.Context, key string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM app_secrets WHERE key = ?`, key); err != nil {
		return fmt.Errorf("delete app secret %q: %w", key, err)
	}
	return nil
}

func (s *Store) sealAppSecret(ctx context.Context, key, plaintext string) error {
	if s.vault == nil {
		return errors.New("store: vault unavailable")
	}
	nonce, ciphertext, err := s.vault.Seal(plaintext)
	if err != nil {
		return fmt.Errorf("seal app secret %q: %w", key, err)
	}
	return s.PutAppSecret(ctx, key, nonce, ciphertext)
}

// openAppSecret returns the plaintext of a sealed secret. A missing row or a
// wrong master key is reported as false, never as an error that could leak the
// ciphertext.
func (s *Store) openAppSecret(ctx context.Context, key string) (string, bool) {
	if s.vault == nil {
		return "", false
	}
	nonce, ciphertext, err := s.GetAppSecret(ctx, key)
	if err != nil {
		return "", false
	}
	plaintext, err := s.vault.Open(nonce, ciphertext)
	if err != nil {
		return "", false
	}
	return plaintext, true
}

// MigrateSettingsSecrets moves the values an older installation stored in
// clear in the settings table into the sealed app_secrets table, then deletes
// the plaintext rows. It is idempotent and does nothing without a vault.
func (s *Store) MigrateSettingsSecrets(ctx context.Context) (int, error) {
	if s.vault == nil {
		return 0, nil
	}
	moved := 0
	for _, item := range []struct{ setting, secret string }{
		{KeyNotifyWebhook, AppSecretNotifyWebhook},
		{KeyNotifyTelegramToken, AppSecretNotifyTelegramToken},
	} {
		var value string
		err := s.db.QueryRowContext(ctx,
			`SELECT value FROM settings WHERE key = ?`, item.setting).Scan(&value)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return moved, fmt.Errorf("migrate setting %s: %w", item.setting, err)
		}
		if value != "" {
			if err := s.sealAppSecret(ctx, item.secret, value); err != nil {
				return moved, err
			}
			moved++
		}
		if _, err := s.db.ExecContext(ctx, `DELETE FROM settings WHERE key = ?`, item.setting); err != nil {
			return moved, fmt.Errorf("drop plaintext setting %s: %w", item.setting, err)
		}
	}
	return moved, nil
}

// NotificationSentAt returns the last successful delivery of one event kind
// and source. Unknown pairs return the zero time.
func (s *Store) NotificationSentAt(ctx context.Context, kind, source string) (time.Time, error) {
	var unix int64
	err := s.db.QueryRowContext(ctx,
		`SELECT last_sent_at FROM notify_events WHERE kind = ? AND source = ?`,
		kind, source).Scan(&unix)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("notification timestamp: %w", err)
	}
	return time.Unix(unix, 0).UTC(), nil
}

// RecordNotification records a successful delivery for the anti-spam window.
func (s *Store) RecordNotification(ctx context.Context, kind, source string, at time.Time) error {
	if at.IsZero() {
		at = time.Now()
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO notify_events(kind, source, last_sent_at)
		VALUES(?, ?, ?)
		ON CONFLICT(kind, source) DO UPDATE SET last_sent_at = excluded.last_sent_at`,
		kind, source, at.Unix())
	if err != nil {
		return fmt.Errorf("record notification: %w", err)
	}
	return nil
}

// IndexerNotifyState returns the last condition notified for an indexer, or an
// empty string when no notification was ever evaluated.
func (s *Store) IndexerNotifyState(ctx context.Context, indexerID int64) (string, error) {
	var state string
	err := s.db.QueryRowContext(ctx,
		`SELECT state FROM indexer_notify_state WHERE indexer_id = ?`, indexerID).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("indexer notify state: %w", err)
	}
	return state, nil
}

// SetIndexerNotifyState remembers the condition of one indexer.
func (s *Store) SetIndexerNotifyState(ctx context.Context, indexerID int64, state string) error {
	if indexerID <= 0 {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO indexer_notify_state(indexer_id, state, updated_at)
		VALUES(?, ?, ?)
		ON CONFLICT(indexer_id) DO UPDATE SET
			state = excluded.state,
			updated_at = excluded.updated_at`,
		indexerID, state, time.Now().Unix())
	if err != nil {
		return fmt.Errorf("set indexer notify state: %w", err)
	}
	return nil
}
