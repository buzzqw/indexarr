package backup

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/buzzqw/indexarr/internal/store"
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
