package backup

import (
	"context"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/buzzqw/indexarr/internal/store"
	"github.com/buzzqw/indexarr/internal/vault"
)

func TestBackupCreateValidateApplyAndPrune(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "indexarr.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting(ctx, store.KeyAPIKey, "from-backup"); err != nil {
		t.Fatal(err)
	}
	first, err := Create(ctx, st, dbPath)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := Validate(filepath.Join(Dir(dbPath), first.Name)); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if _, err := Create(ctx, st, dbPath); err != nil {
		t.Fatalf("Create second: %v", err)
	}
	list, err := List(dbPath)
	if err != nil || len(list) != 2 {
		t.Fatalf("list = %#v, %v", list, err)
	}
	if _, err := Prune(dbPath, 1); err != nil {
		t.Fatalf("Prune: %v", err)
	}
	list, _ = List(dbPath)
	if len(list) != 1 {
		t.Errorf("backups after prune = %d", len(list))
	}
	if _, err := Resolve(dbPath, "../indexarr.db"); err == nil {
		t.Error("path traversal accepted")
	}
	retained := filepath.Join(Dir(dbPath), list[0].Name)

	// The backup carries the old API key; after applying it the store must see
	// that value again.
	if err := st.SetSetting(ctx, store.KeyAPIKey, "current"); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if err := Stage(dbPath, retained); err != nil {
		t.Fatalf("Stage: %v", err)
	}
	applied, err := ApplyPending(dbPath)
	if err != nil || !applied {
		t.Fatalf("ApplyPending = %v, %v", applied, err)
	}
	entries, _ := os.ReadDir(Dir(dbPath))
	preRestore := false
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) == ".db" && strings.HasPrefix(entry.Name(), "pre-restore-") {
			preRestore = true
		}
	}
	if !preRestore {
		t.Error("pre-restore copy missing")
	}
	reopened, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()
	settings, err := reopened.LoadSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if settings.APIKey != "from-backup" {
		t.Errorf("restored API key = %q", settings.APIKey)
	}
}

func TestApplyPendingNoFile(t *testing.T) {
	applied, err := ApplyPending(filepath.Join(t.TempDir(), "none.db"))
	if err != nil || applied {
		t.Fatalf("ApplyPending = %v, %v", applied, err)
	}
}

func testVaultKey(fill byte) []byte {
	key := make([]byte, 32)
	for i := range key {
		key[i] = fill
	}
	return key
}

func TestBackupRestoreSecretsWithVaultKey(t *testing.T) {
	ctx := context.Background()
	key := testVaultKey(0x5a)
	v, err := vault.New(key)
	if err != nil {
		t.Fatal(err)
	}

	sourceDir := t.TempDir()
	sourceDB := filepath.Join(sourceDir, "indexarr.db")
	st, err := store.Open(sourceDB)
	if err != nil {
		t.Fatal(err)
	}
	st.SetSecretCodec(v)
	const (
		secretURL  = "https://tracker.example/rss/0123456789abcdef0123456789abcdef"
		secretKey  = "restore-api-key-secret"
		secretPass = "restore-password-secret"
	)
	if _, err := st.CreateSource(ctx, store.Source{
		Name: "restored", Kind: "rss", URL: secretURL, APIKey: secretKey,
		Password: secretPass, Enabled: true,
	}); err != nil {
		t.Fatalf("CreateSource: %v", err)
	}
	info, err := Create(ctx, st, sourceDB)
	if err != nil {
		t.Fatalf("Create backup: %v", err)
	}
	backupPath := filepath.Join(Dir(sourceDB), info.Name)

	raw, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	for name, secret := range map[string]string{
		"api key": secretKey, "password": secretPass, "url path": "0123456789abcdef0123456789abcdef",
	} {
		if strings.Contains(string(raw), secret) {
			t.Errorf("backup file contains the plaintext %s", name)
		}
	}
	for name, encoded := range map[string]string{
		"raw key": string(key), "hex key": hex.EncodeToString(key),
	} {
		if strings.Contains(string(raw), encoded) {
			t.Errorf("backup file contains the %s", name)
		}
	}
	entries, err := os.ReadDir(Dir(sourceDB))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".key") {
			t.Errorf("backup directory contains a key file: %s", entry.Name())
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	t.Run("same key opens restored secrets", func(t *testing.T) {
		destDir := t.TempDir()
		destDB := filepath.Join(destDir, "indexarr.db")
		if err := Stage(destDB, backupPath); err != nil {
			t.Fatalf("Stage: %v", err)
		}
		applied, err := ApplyPending(destDB)
		if err != nil || !applied {
			t.Fatalf("ApplyPending = %v, %v", applied, err)
		}
		restored, err := store.Open(destDB)
		if err != nil {
			t.Fatalf("open restored store: %v", err)
		}
		defer restored.Close()
		restored.SetSecretCodec(v)
		opened, err := restored.VerifySecretCodec(ctx)
		if err != nil || opened == 0 {
			t.Fatalf("VerifySecretCodec = %d, %v; want a positive count", opened, err)
		}
		sources, err := restored.ListSources(ctx)
		if err != nil {
			t.Fatalf("ListSources: %v", err)
		}
		if len(sources) != 1 || sources[0].URL != secretURL ||
			sources[0].APIKey != secretKey || sources[0].Password != secretPass {
			t.Fatal("restored source secrets are not readable with the original key")
		}
	})

	t.Run("wrong key reports an explicit error", func(t *testing.T) {
		destDir := t.TempDir()
		destDB := filepath.Join(destDir, "indexarr.db")
		if err := Stage(destDB, backupPath); err != nil {
			t.Fatalf("Stage: %v", err)
		}
		if _, err := ApplyPending(destDB); err != nil {
			t.Fatalf("ApplyPending: %v", err)
		}
		restored, err := store.Open(destDB)
		if err != nil {
			t.Fatalf("open restored store: %v", err)
		}
		defer restored.Close()
		wrong, err := vault.New(testVaultKey(0x6b))
		if err != nil {
			t.Fatal(err)
		}
		restored.SetSecretCodec(wrong)
		if _, err := restored.VerifySecretCodec(ctx); err == nil {
			t.Fatal("VerifySecretCodec accepted the wrong key")
		} else if strings.Contains(err.Error(), secretKey) || strings.Contains(err.Error(), secretPass) {
			t.Fatal("verification error leaks a secret value")
		}
		if _, err := restored.GetSource(ctx, 1); err == nil {
			t.Fatal("GetSource decrypted a secret with the wrong key")
		}
	})
}
