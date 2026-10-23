package store

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

// testCodec is a reversible stand-in for the vault. It keeps the store tests
// independent of internal/vault while still making ciphertext-at-rest visible.
type testCodec struct {
	salt string
}

func (c testCodec) SealString(plaintext string) (string, error) {
	return base64.StdEncoding.EncodeToString([]byte(c.salt + plaintext)), nil
}

func (c testCodec) OpenString(blob string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(blob)
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(string(raw), c.salt) {
		return "", errWrongTestKey
	}
	return strings.TrimPrefix(string(raw), c.salt), nil
}

var errWrongTestKey = errors.New("wrong test key")

func rawSourceSecrets(t *testing.T, st *Store, id int64) (string, string, string) {
	t.Helper()
	var url, apiKey, password string
	if err := st.DB().QueryRow(
		`SELECT url, api_key, password FROM sources WHERE id = ?`, id).
		Scan(&url, &apiKey, &password); err != nil {
		t.Fatalf("read raw source secrets: %v", err)
	}
	return url, apiKey, password
}

func TestSourceSecretsCiphertextAtRest(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	st.SetSecretCodec(testCodec{salt: "c1:"})

	const (
		secretURL      = "https://tracker.example/rss/0123456789abcdef0123456789abcdef"
		secretAPIKey   = "api-key-very-secret-value"
		secretPassword = "http-password-very-secret"
	)
	id, err := st.CreateSource(ctx, Source{
		Name: "sealed", Kind: "rss", Manager: "auto", URL: secretURL,
		APIKey: secretAPIKey, Password: secretPassword, Enabled: true,
	})
	if err != nil {
		t.Fatalf("CreateSource: %v", err)
	}

	url, apiKey, password := rawSourceSecrets(t, st, id)
	for name, value := range map[string]string{"url": url, "api_key": apiKey, "password": password} {
		if !strings.HasPrefix(value, sealedPrefix) {
			t.Errorf("%s is not sealed at rest", name)
		}
	}
	for name, value := range map[string]string{"url": url, "api_key": apiKey, "password": password} {
		if strings.Contains(value, secretAPIKey) || strings.Contains(value, secretPassword) ||
			strings.Contains(value, "0123456789abcdef0123456789abcdef") {
			t.Errorf("%s leaks plaintext", name)
		}
	}

	source, err := st.GetSource(ctx, id)
	if err != nil {
		t.Fatalf("GetSource: %v", err)
	}
	if source.URL != secretURL || source.APIKey != secretAPIKey || source.Password != secretPassword {
		t.Fatal("GetSource did not return the plaintext credentials")
	}

	// A source created while the codec is active needs no migration.
	if sealed, err := st.SealLegacySecrets(ctx); err != nil || sealed != 0 {
		t.Fatalf("SealLegacySecrets = %d, %v; want 0, nil", sealed, err)
	}
}

func TestSourceSecretsPlaintextWithoutCodec(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	const secret = "legacy-plain-secret"
	id, err := st.CreateSource(ctx, Source{
		Name: "plain", Kind: "torznab", URL: "https://host.example/api",
		APIKey: secret, Password: secret, Enabled: true,
	})
	if err != nil {
		t.Fatalf("CreateSource: %v", err)
	}
	url, apiKey, password := rawSourceSecrets(t, st, id)
	if url != "https://host.example/api" || apiKey != secret || password != secret {
		t.Fatal("without a codec the legacy plaintext layout must be unchanged")
	}
	if sealed, err := st.SealLegacySecrets(ctx); err != nil || sealed != 0 {
		t.Fatalf("SealLegacySecrets without codec = %d, %v; want 0, nil", sealed, err)
	}
}

func TestSealLegacySecretsIsIdempotent(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	const (
		secretURL  = "https://tracker.example/rss/abcdefabcdefabcdefabcdefabcdefab"
		secretKey  = "legacy-api-key-secret"
		secretPass = "legacy-password-secret"
	)
	id, err := st.CreateSource(ctx, Source{
		Name: "legacy", Kind: "rss", URL: secretURL, APIKey: secretKey,
		Password: secretPass, Enabled: true,
	})
	if err != nil {
		t.Fatalf("CreateSource: %v", err)
	}
	st.SetSecretCodec(testCodec{salt: "c2:"})
	sealed, err := st.SealLegacySecrets(ctx)
	if err != nil {
		t.Fatalf("SealLegacySecrets: %v", err)
	}
	if sealed != 3 {
		t.Fatalf("sealed fields = %d, want 3", sealed)
	}
	firstURL, firstKey, firstPass := rawSourceSecrets(t, st, id)
	for name, value := range map[string]string{"url": firstURL, "api_key": firstKey, "password": firstPass} {
		if !strings.HasPrefix(value, sealedPrefix) {
			t.Errorf("%s is not sealed", name)
		}
	}
	source, err := st.GetSource(ctx, id)
	if err != nil {
		t.Fatalf("GetSource after migration: %v", err)
	}
	if source.URL != secretURL || source.APIKey != secretKey || source.Password != secretPass {
		t.Fatal("migration changed the plaintext credentials")
	}

	again, err := st.SealLegacySecrets(ctx)
	if err != nil || again != 0 {
		t.Fatalf("second SealLegacySecrets = %d, %v; want 0, nil", again, err)
	}
	secondURL, secondKey, secondPass := rawSourceSecrets(t, st, id)
	if firstURL != secondURL || firstKey != secondKey || firstPass != secondPass {
		t.Fatal("second migration rewrote sealed values")
	}
}

func TestOpenSealedSecretWithoutCodecFails(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	const secret = "sealed-secret-value"
	id, err := st.CreateSource(ctx, Source{Name: "sealed", Kind: "rss", APIKey: secret, Enabled: true})
	if err != nil {
		t.Fatalf("CreateSource: %v", err)
	}
	st.SetSecretCodec(testCodec{salt: "c3:"})
	if _, err := st.SealLegacySecrets(ctx); err != nil {
		t.Fatalf("SealLegacySecrets: %v", err)
	}
	st.SetSecretCodec(nil)
	if _, err := st.GetSource(ctx, id); err == nil {
		t.Fatal("GetSource returned a sealed secret while no codec was configured")
	} else if strings.Contains(err.Error(), secret) {
		t.Fatal("error leaks the secret value")
	}
}

func TestVerifySecretCodec(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	const secret = "verify-me-secret-value"
	if _, err := st.CreateSource(ctx, Source{Name: "verify", Kind: "rss", APIKey: secret, Enabled: true}); err != nil {
		t.Fatalf("CreateSource: %v", err)
	}
	st.SetSecretCodec(testCodec{salt: "right:"})
	if _, err := st.SealLegacySecrets(ctx); err != nil {
		t.Fatalf("SealLegacySecrets: %v", err)
	}
	opened, err := st.VerifySecretCodec(ctx)
	if err != nil || opened == 0 {
		t.Fatalf("VerifySecretCodec = %d, %v; want a positive count", opened, err)
	}

	st.SetSecretCodec(testCodec{salt: "wrong:"})
	opened, err = st.VerifySecretCodec(ctx)
	if err == nil {
		t.Fatal("VerifySecretCodec accepted the wrong key")
	}
	if opened != 0 {
		t.Errorf("opened = %d after the first failure, want 0", opened)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatal("verification error leaks the secret value")
	}
}

func TestSealedSecretCount(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	if _, err := st.CreateSource(ctx, Source{Name: "count", Kind: "rss", APIKey: "k", Password: "p", Enabled: true}); err != nil {
		t.Fatalf("CreateSource: %v", err)
	}
	count, err := st.SealedSecretCount(ctx)
	if err != nil || count != 0 {
		t.Fatalf("SealedSecretCount before migration = %d, %v", count, err)
	}
	st.SetSecretCodec(testCodec{salt: "c4:"})
	if _, err := st.SealLegacySecrets(ctx); err != nil {
		t.Fatalf("SealLegacySecrets: %v", err)
	}
	count, err = st.SealedSecretCount(ctx)
	if err != nil || count != 2 {
		t.Fatalf("SealedSecretCount after migration = %d, %v; want 2", count, err)
	}
}
