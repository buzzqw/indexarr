package vault

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func testKey(fill byte) []byte {
	key := make([]byte, keySize)
	for i := range key {
		key[i] = fill
	}
	return key
}

func mustVault(t *testing.T, key []byte) *Vault {
	t.Helper()
	v, err := New(key)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return v
}

func TestNew(t *testing.T) {
	if _, err := New(nil); !errors.Is(err, ErrKeyRequired) {
		t.Fatalf("New(nil) error = %v, want %v", err, ErrKeyRequired)
	}
	if _, err := New([]byte{}); !errors.Is(err, ErrKeyRequired) {
		t.Fatalf("New(empty) error = %v, want %v", err, ErrKeyRequired)
	}
	short := testKey(1)[:16]
	_, err := New(short)
	if err == nil {
		t.Fatal("New(short key) succeeded")
	}
	if !strings.Contains(err.Error(), "32") {
		t.Errorf("error does not explain the expected length: %v", err)
	}
	if strings.Contains(err.Error(), hex.EncodeToString(short)) {
		t.Errorf("error leaks the key: %v", err)
	}
	if _, err := New(append(testKey(1), 0)); err == nil {
		t.Fatal("New(long key) succeeded")
	}
	if _, err := New(testKey(7)); err != nil {
		t.Fatalf("New(valid): %v", err)
	}
}

func TestSealOpenRoundTrip(t *testing.T) {
	v := mustVault(t, testKey(3))
	for _, plaintext := range []string{"", "passkey-123", "pässwörd ✓ con spazi"} {
		nonce, ciphertext, err := v.Seal(plaintext)
		if err != nil {
			t.Fatalf("Seal(%q): %v", plaintext, err)
		}
		if len(nonce) != nonceSize {
			t.Errorf("nonce length = %d, want %d", len(nonce), nonceSize)
		}
		got, err := v.Open(nonce, ciphertext)
		if err != nil {
			t.Fatalf("Open(%q): %v", plaintext, err)
		}
		if got != plaintext {
			t.Errorf("round trip = %q, want %q", got, plaintext)
		}
	}
}

func TestSealUsesFreshNonce(t *testing.T) {
	v := mustVault(t, testKey(4))
	nonce1, ciphertext1, err := v.Seal("same plaintext")
	if err != nil {
		t.Fatal(err)
	}
	nonce2, ciphertext2, err := v.Seal("same plaintext")
	if err != nil {
		t.Fatal(err)
	}
	if string(nonce1) == string(nonce2) {
		t.Error("two seals reused the same nonce")
	}
	if string(ciphertext1) == string(ciphertext2) {
		t.Error("two seals produced the same ciphertext")
	}
}

func TestOpenWrongKey(t *testing.T) {
	sealer := mustVault(t, testKey(5))
	nonce, ciphertext, err := sealer.Seal("tracker passkey")
	if err != nil {
		t.Fatal(err)
	}
	other := mustVault(t, testKey(6))
	if _, err := other.Open(nonce, ciphertext); err == nil {
		t.Fatal("Open with a wrong key succeeded")
	}
}

func TestOpenTampered(t *testing.T) {
	v := mustVault(t, testKey(7))
	nonce, ciphertext, err := v.Seal("sensitive")
	if err != nil {
		t.Fatal(err)
	}

	t.Run("ciphertext", func(t *testing.T) {
		tampered := append([]byte(nil), ciphertext...)
		tampered[0] ^= 0xff
		if _, err := v.Open(nonce, tampered); err == nil {
			t.Fatal("Open accepted tampered ciphertext")
		}
	})
	t.Run("nonce", func(t *testing.T) {
		tampered := append([]byte(nil), nonce...)
		tampered[len(tampered)-1] ^= 0xff
		if _, err := v.Open(tampered, ciphertext); err == nil {
			t.Fatal("Open accepted a tampered nonce")
		}
	})
}

func TestOpenRejectsBadInput(t *testing.T) {
	v := mustVault(t, testKey(8))
	nonce, ciphertext, err := v.Seal("value")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name       string
		nonce      []byte
		ciphertext []byte
	}{
		{"empty nonce", nil, ciphertext},
		{"short nonce", nonce[:nonceSize-1], ciphertext},
		{"long nonce", append(append([]byte(nil), nonce...), 0), ciphertext},
		{"empty ciphertext", nonce, nil},
		{"truncated ciphertext", nonce, ciphertext[:len(ciphertext)-1]},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := v.Open(tc.nonce, tc.ciphertext); err == nil {
				t.Fatalf("Open accepted %s", tc.name)
			}
		})
	}
}

func TestSealStringRoundTrip(t *testing.T) {
	v := mustVault(t, testKey(9))
	for _, plaintext := range []string{"", "cookie=abc", "multi\nline"} {
		blob, err := v.SealString(plaintext)
		if err != nil {
			t.Fatalf("SealString(%q): %v", plaintext, err)
		}
		if _, err := base64.StdEncoding.DecodeString(blob); err != nil {
			t.Fatalf("SealString(%q) is not valid base64: %v", plaintext, err)
		}
		got, err := v.OpenString(blob)
		if err != nil {
			t.Fatalf("OpenString(%q): %v", plaintext, err)
		}
		if got != plaintext {
			t.Errorf("round trip = %q, want %q", got, plaintext)
		}
	}
	first, _ := v.SealString("same")
	second, _ := v.SealString("same")
	if first == second {
		t.Error("SealString produced identical blobs for the same plaintext")
	}
}

func TestOpenStringGarbage(t *testing.T) {
	v := mustVault(t, testKey(10))
	valid, err := v.SealString("secret")
	if err != nil {
		t.Fatal(err)
	}
	tampered, err := base64.StdEncoding.DecodeString(valid)
	if err != nil {
		t.Fatal(err)
	}
	tampered[len(tampered)-1] ^= 0xff

	cases := []struct {
		name string
		blob string
	}{
		{"empty", ""},
		{"malformed", "!!! not base64 !!!"},
		{"short", base64.StdEncoding.EncodeToString([]byte("short"))},
		{"wrong length", base64.StdEncoding.EncodeToString(make([]byte, nonceSize+4))},
		{"tampered", base64.StdEncoding.EncodeToString(tampered)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := v.OpenString(tc.blob); err == nil {
				t.Fatalf("OpenString accepted %q", tc.name)
			}
		})
	}
}

func TestFromEnvOrFileEnvWins(t *testing.T) {
	const envName = "INDEXARR_TEST_MASTER_KEY_ENV_WINS"
	fileKey := testKey(11)
	envKey := testKey(12)

	path := filepath.Join(t.TempDir(), "master.key")
	if err := os.WriteFile(path, []byte(hex.EncodeToString(fileKey)), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(envName, hex.EncodeToString(envKey))

	v, err := FromEnvOrFile(envName, path)
	if err != nil {
		t.Fatalf("FromEnvOrFile: %v", err)
	}
	blob, err := v.SealString("from env")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mustVault(t, envKey).OpenString(blob); err != nil {
		t.Errorf("vault does not use the env key: %v", err)
	}
	if _, err := mustVault(t, fileKey).OpenString(blob); err == nil {
		t.Error("vault used the file key even though the env var was set")
	}
}

func TestFromEnvOrFileCreatesFile(t *testing.T) {
	const envName = "INDEXARR_TEST_MASTER_KEY_CREATE"
	t.Setenv(envName, "")

	path := filepath.Join(t.TempDir(), "master.key")
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("key file already exists: %v", err)
	}

	v, err := FromEnvOrFile(envName, path)
	if err != nil {
		t.Fatalf("FromEnvOrFile: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat created key file: %v", err)
	}
	if runtime.GOOS != "windows" {
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("key file permissions = %o, want 600", perm)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != keySize {
		t.Fatalf("created key file has %d bytes, want %d", len(data), keySize)
	}

	blob, err := v.SealString("created once")
	if err != nil {
		t.Fatal(err)
	}
	again, err := FromEnvOrFile(envName, path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if _, err := again.OpenString(blob); err != nil {
		t.Errorf("reopened vault does not share the generated key: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(data) {
		t.Error("existing key file was rewritten")
	}
}

func TestFromEnvOrFileKeyFormats(t *testing.T) {
	key := testKey(13)
	formats := []struct {
		name  string
		value string
	}{
		{"base64 standard", base64.StdEncoding.EncodeToString(key)},
		{"base64 raw", base64.RawStdEncoding.EncodeToString(key)},
		{"hex", hex.EncodeToString(key)},
	}

	for _, format := range formats {
		t.Run("env "+format.name, func(t *testing.T) {
			const envName = "INDEXARR_TEST_MASTER_KEY_FORMAT"
			t.Setenv(envName, format.value)
			v, err := FromEnvOrFile(envName, "")
			if err != nil {
				t.Fatalf("FromEnvOrFile: %v", err)
			}
			blob, err := v.SealString("format")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := mustVault(t, key).OpenString(blob); err != nil {
				t.Errorf("env key not decoded as %s: %v", format.name, err)
			}
		})
		t.Run("file "+format.name, func(t *testing.T) {
			const envName = "INDEXARR_TEST_MASTER_KEY_FORMAT"
			t.Setenv(envName, "")
			path := filepath.Join(t.TempDir(), "master.key")
			// A trailing newline is common in files written by hand.
			if err := os.WriteFile(path, []byte(format.value+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			v, err := FromEnvOrFile(envName, path)
			if err != nil {
				t.Fatalf("FromEnvOrFile: %v", err)
			}
			blob, err := v.SealString("format")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := mustVault(t, key).OpenString(blob); err != nil {
				t.Errorf("file key not decoded as %s: %v", format.name, err)
			}
		})
	}
}

func TestFromEnvOrFileRejectsMalformedKey(t *testing.T) {
	malformed := []struct {
		name  string
		value string
	}{
		{"not a key", "not-a-key!"},
		{"wrong base64 length", base64.StdEncoding.EncodeToString(testKey(14)[:16])},
		{"wrong hex length", hex.EncodeToString(testKey(15)[:15])},
		{"invalid characters", strings.Repeat("?", 44)},
	}

	for _, tc := range malformed {
		t.Run("env "+tc.name, func(t *testing.T) {
			const envName = "INDEXARR_TEST_MASTER_KEY_BAD"
			t.Setenv(envName, tc.value)
			_, err := FromEnvOrFile(envName, "")
			if err == nil {
				t.Fatal("FromEnvOrFile accepted a malformed env key")
			}
			if strings.Contains(err.Error(), tc.value) {
				t.Errorf("error leaks the key value: %v", err)
			}
		})
		t.Run("file "+tc.name, func(t *testing.T) {
			const envName = "INDEXARR_TEST_MASTER_KEY_BAD"
			t.Setenv(envName, "")
			path := filepath.Join(t.TempDir(), "master.key")
			if err := os.WriteFile(path, []byte(tc.value), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := FromEnvOrFile(envName, path)
			if err == nil {
				t.Fatal("FromEnvOrFile accepted a malformed key file")
			}
			if strings.Contains(err.Error(), tc.value) {
				t.Errorf("error leaks the key value: %v", err)
			}
		})
	}
}

func TestFromEnvOrFileRequiresKey(t *testing.T) {
	const envName = "INDEXARR_TEST_MASTER_KEY_MISSING"
	t.Setenv(envName, "")
	_, err := FromEnvOrFile(envName, "")
	if !errors.Is(err, ErrKeyRequired) {
		t.Fatalf("FromEnvOrFile error = %v, want %v", err, ErrKeyRequired)
	}
}
