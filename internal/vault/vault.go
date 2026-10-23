// Package vault implements the AES-256-GCM secrets vault of indexarr.
//
// It protects credentials, cookies and application API keys stored in SQLite
// and in backups. The master key lives outside the database: it is read from
// an environment variable or from a separate file, so a copy of the database
// alone is not enough to decrypt the secrets. The vault does not protect
// against an attacker with full access to the host.
package vault

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	// keySize is the AES-256 key length in bytes.
	keySize = 32
	// nonceSize is the standard GCM nonce length in bytes.
	nonceSize = 12
)

// ErrKeyRequired is returned when no master key was supplied at all.
var ErrKeyRequired = errors.New("vault: master key required")

// Vault seals and opens secrets with AES-256-GCM. A Vault is safe for
// concurrent use.
type Vault struct {
	aead cipher.AEAD
}

// New returns a Vault for a 32-byte AES-256 key. The key is not retained: only
// the expanded AES key schedule is kept.
func New(key []byte) (*Vault, error) {
	if len(key) == 0 {
		return nil, ErrKeyRequired
	}
	if len(key) != keySize {
		return nil, fmt.Errorf("vault: master key must be %d bytes, got %d", keySize, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("vault: create cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("vault: create GCM: %w", err)
	}
	return &Vault{aead: aead}, nil
}

// FromEnvOrFile builds a Vault from the master key in envVar, falling back to
// keyPath. A non-empty environment variable wins. The key may be encoded as
// standard base64, raw (unpadded) base64 or 64-character hex. When the file is
// missing it is created with 32 random bytes and 0600 permissions. A file
// written by this function contains the raw 32 bytes; files written by hand
// may use any of the text encodings above.
func FromEnvOrFile(envVar, keyPath string) (*Vault, error) {
	if raw := strings.TrimSpace(os.Getenv(envVar)); raw != "" {
		key, err := decodeKey(raw)
		if err != nil {
			return nil, fmt.Errorf("vault: master key from %s: %w", envVar, err)
		}
		return New(key)
	}
	if strings.TrimSpace(keyPath) == "" {
		return nil, ErrKeyRequired
	}
	key, err := loadOrCreateKey(keyPath)
	if err != nil {
		return nil, err
	}
	return New(key)
}

// Seal encrypts plaintext with a fresh random nonce. The nonce and ciphertext
// are returned separately; both are needed to Open. An empty plaintext is
// valid and produces a ciphertext containing only the authentication tag.
func (v *Vault) Seal(plaintext string) (nonce, ciphertext []byte, err error) {
	if v == nil || v.aead == nil {
		return nil, nil, errors.New("vault: uninitialized vault")
	}
	nonce = make([]byte, v.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, nil, fmt.Errorf("vault: generate nonce: %w", err)
	}
	ciphertext = v.aead.Seal(nil, nonce, []byte(plaintext), nil)
	return nonce, ciphertext, nil
}

// Open authenticates and decrypts a ciphertext produced by Seal. It fails on a
// tampered nonce or ciphertext, a wrong key and truncated input.
func (v *Vault) Open(nonce, ciphertext []byte) (string, error) {
	if v == nil || v.aead == nil {
		return "", errors.New("vault: uninitialized vault")
	}
	if len(nonce) != v.aead.NonceSize() {
		return "", fmt.Errorf("vault: invalid nonce length %d", len(nonce))
	}
	if len(ciphertext) < v.aead.Overhead() {
		return "", errors.New("vault: ciphertext too short")
	}
	plaintext, err := v.aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("vault: decrypt: %w", err)
	}
	return string(plaintext), nil
}

// SealString seals plaintext and returns base64(nonce || ciphertext).
func (v *Vault) SealString(plaintext string) (string, error) {
	nonce, ciphertext, err := v.Seal(plaintext)
	if err != nil {
		return "", err
	}
	blob := make([]byte, 0, len(nonce)+len(ciphertext))
	blob = append(blob, nonce...)
	blob = append(blob, ciphertext...)
	return base64.StdEncoding.EncodeToString(blob), nil
}

// OpenString opens a blob produced by SealString. Malformed base64 and blobs
// shorter than a nonce are rejected.
func (v *Vault) OpenString(blob string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(blob)
	if err != nil {
		return "", fmt.Errorf("vault: decode blob: %w", err)
	}
	nonceLength := nonceSize
	if v != nil && v.aead != nil {
		nonceLength = v.aead.NonceSize()
	}
	if len(raw) < nonceLength {
		return "", errors.New("vault: blob too short")
	}
	return v.Open(raw[:nonceLength], raw[nonceLength:])
}

// decodeKey accepts standard base64, raw base64 and 64-character hex keys. The
// key material never appears in the returned error.
func decodeKey(raw string) ([]byte, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, ErrKeyRequired
	}
	// Hex must be tried first: a 64-character hex string is also valid base64
	// and would otherwise decode to 48 bytes.
	if len(raw) == hex.EncodedLen(keySize) {
		if key, err := hex.DecodeString(raw); err == nil {
			return key, nil
		}
	}
	for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding} {
		key, err := encoding.DecodeString(raw)
		if err != nil {
			continue
		}
		if len(key) != keySize {
			return nil, fmt.Errorf("vault: master key must decode to %d bytes, got %d", keySize, len(key))
		}
		return key, nil
	}
	return nil, errors.New("vault: master key must be 64-character hex or base64")
}

// loadOrCreateKey reads the key file, creating it with 32 random bytes and
// 0600 permissions when it does not exist.
func loadOrCreateKey(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		if len(data) == keySize {
			return data, nil
		}
		key, decodeErr := decodeKey(string(data))
		if decodeErr != nil {
			return nil, fmt.Errorf("vault: master key file %s: %w", path, decodeErr)
		}
		return key, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("vault: read master key file %s: %w", path, err)
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("vault: create master key directory: %w", err)
		}
	}
	key := make([]byte, keySize)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("vault: generate master key: %w", err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		// Another process may have won the creation race: use its key.
		if errors.Is(err, os.ErrExist) {
			return loadOrCreateKey(path)
		}
		return nil, fmt.Errorf("vault: create master key file %s: %w", path, err)
	}
	if _, err := file.Write(key); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("vault: write master key file %s: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("vault: write master key file %s: %w", path, err)
	}
	return key, nil
}
