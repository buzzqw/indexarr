package store

import (
	"context"
	"fmt"
	"strings"
)

// sealedPrefix marks a source credential that was encrypted by the vault. The
// versioned prefix allows a future format change without guessing: values
// without the prefix are legacy plaintext and are returned as-is.
const sealedPrefix = "vault:v1:"

// SecretCodec seals legacy source credentials at rest. *vault.Vault satisfies
// it; the store never imports vault so the SQLite layer stays independent.
type SecretCodec interface {
	SealString(plaintext string) (string, error)
	OpenString(blob string) (string, error)
}

// SetSecretCodec installs the codec used for source secrets. nil keeps the
// legacy plaintext behaviour.
func (s *Store) SetSecretCodec(c SecretCodec) {
	s.codec = c
}

// sealField encrypts a value with the configured codec and adds the versioned
// prefix. Empty values, values already sealed and stores without a codec are
// left untouched, so migration is idempotent and the legacy path keeps working.
func (s *Store) sealField(value string) (string, error) {
	if s.codec == nil || value == "" || strings.HasPrefix(value, sealedPrefix) {
		return value, nil
	}
	blob, err := s.codec.SealString(value)
	if err != nil {
		return "", fmt.Errorf("seal source secret: %w", err)
	}
	return sealedPrefix + blob, nil
}

// openField decrypts a prefixed value. Plaintext is returned unchanged. A
// sealed value without a codec is an error: returning the ciphertext would send
// unusable credentials upstream, and the error never contains the value.
func (s *Store) openField(value string) (string, error) {
	if value == "" || !strings.HasPrefix(value, sealedPrefix) {
		return value, nil
	}
	if s.codec == nil {
		return "", fmt.Errorf("sealed source secret present but no vault key is available")
	}
	plaintext, err := s.codec.OpenString(strings.TrimPrefix(value, sealedPrefix))
	if err != nil {
		return "", fmt.Errorf("open source secret: %w", err)
	}
	return plaintext, nil
}

// SealLegacySecrets encrypts every plaintext source URL, API key and password.
// It is idempotent: prefixed values are skipped and a second call returns 0.
func (s *Store) SealLegacySecrets(ctx context.Context) (int, error) {
	if s.codec == nil {
		return 0, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, url, api_key, password FROM sources`)
	if err != nil {
		return 0, fmt.Errorf("list sources for secret migration: %w", err)
	}
	type pending struct {
		id                             int64
		url, apiKey, password          string
		oldURL, oldAPIKey, oldPassword string
	}
	var updates []pending
	for rows.Next() {
		var item pending
		if err := rows.Scan(&item.id, &item.url, &item.apiKey, &item.password); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scan source for secret migration: %w", err)
		}
		sealedURL, err := s.sealField(item.url)
		if err != nil {
			rows.Close()
			return 0, fmt.Errorf("source %d: %w", item.id, err)
		}
		sealedAPIKey, err := s.sealField(item.apiKey)
		if err != nil {
			rows.Close()
			return 0, fmt.Errorf("source %d: %w", item.id, err)
		}
		sealedPassword, err := s.sealField(item.password)
		if err != nil {
			rows.Close()
			return 0, fmt.Errorf("source %d: %w", item.id, err)
		}
		if sealedURL == item.url && sealedAPIKey == item.apiKey && sealedPassword == item.password {
			continue
		}
		updates = append(updates, pending{
			id: item.id, url: sealedURL, apiKey: sealedAPIKey, password: sealedPassword,
			oldURL: item.url, oldAPIKey: item.apiKey, oldPassword: item.password,
		})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, fmt.Errorf("list sources for secret migration: %w", err)
	}
	rows.Close()

	if len(updates) == 0 {
		return 0, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	sealed := 0
	for _, item := range updates {
		if _, err := tx.ExecContext(ctx,
			`UPDATE sources SET url = ?, api_key = ?, password = ? WHERE id = ?`,
			item.url, item.apiKey, item.password, item.id); err != nil {
			return 0, fmt.Errorf("seal source %d secrets: %w", item.id, err)
		}
		if item.url != item.oldURL {
			sealed++
		}
		if item.apiKey != item.oldAPIKey {
			sealed++
		}
		if item.password != item.oldPassword {
			sealed++
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return sealed, nil
}

// VerifySecretCodec checks that the configured codec can open every sealed
// source secret. It returns how many sealed values it opened. A database
// without sealed source secrets returns (0, nil): there is nothing to verify.
func (s *Store) VerifySecretCodec(ctx context.Context) (int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, url, api_key, password FROM sources`)
	if err != nil {
		return 0, fmt.Errorf("list sources for secret verification: %w", err)
	}
	defer rows.Close()
	opened := 0
	for rows.Next() {
		var id int64
		var url, apiKey, password string
		if err := rows.Scan(&id, &url, &apiKey, &password); err != nil {
			return opened, fmt.Errorf("scan source for secret verification: %w", err)
		}
		for _, value := range []string{url, apiKey, password} {
			if !strings.HasPrefix(value, sealedPrefix) {
				continue
			}
			if _, err := s.openField(value); err != nil {
				return opened, fmt.Errorf("source %d secrets cannot be opened with the configured key: %w", id, err)
			}
			opened++
		}
	}
	if err := rows.Err(); err != nil {
		return opened, fmt.Errorf("list sources for secret verification: %w", err)
	}
	return opened, nil
}

// SealedSecretCount reports how many source fields are currently sealed. It
// reads the ciphertext columns only and never decrypts; the UI uses it to show
// whether a key is required.
func (s *Store) SealedSecretCount(ctx context.Context) (int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT url, api_key, password FROM sources`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var url, apiKey, password string
		if err := rows.Scan(&url, &apiKey, &password); err != nil {
			return count, err
		}
		for _, value := range []string{url, apiKey, password} {
			if strings.HasPrefix(value, sealedPrefix) {
				count++
			}
		}
	}
	return count, rows.Err()
}
