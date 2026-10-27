package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadCreatesDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "indexarr.json")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Listen != ":8788" || cfg.LogLevel != "info" || cfg.MemoryLimitMB != 64 {
		t.Errorf("cfg = %#v", cfg)
	}
	wantDB := filepath.Join(filepath.Dir(path), "indexarr.db")
	if cfg.DBPath != wantDB {
		t.Errorf("db = %q, want %q", cfg.DBPath, wantDB)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("config file not created: %v", err)
	}
}

func TestLoadCustom(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "custom.json")
	if err := os.WriteFile(path, []byte(`{"listen":":9000","db_path":"data/cache.db","log_level":"debug","vault_key_path":"secrets/master.key"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Listen != ":9000" || cfg.LogLevel != "debug" {
		t.Errorf("cfg = %#v", cfg)
	}
	if cfg.DBPath != filepath.Join(dir, "data/cache.db") {
		t.Errorf("db = %q", cfg.DBPath)
	}
	if cfg.VaultKeyPath != filepath.Join(dir, "secrets/master.key") {
		t.Errorf("vault key path = %q", cfg.VaultKeyPath)
	}
	if cfg.MemoryLimitMB != 64 {
		t.Errorf("memory limit = %d, want default 64", cfg.MemoryLimitMB)
	}
}

func TestLoadRejectsMalformedJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(path, []byte(`{`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected an error")
	}
}

func TestLoadNormalisesUnknownLevel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.json")
	if err := os.WriteFile(path, []byte(`{"log_level":"verbose"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LogLevel != "info" {
		t.Errorf("level = %q", cfg.LogLevel)
	}
}

func TestLoadLogFileDefaultsAndOff(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "indexarr.json")
	if err := os.WriteFile(path, []byte(`{"listen":":8788"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LogFile != filepath.Join(dir, "logs", "indexarr.log") || cfg.LogMaxMB != 10 || cfg.LogMaxFiles != 5 {
		t.Errorf("an old config without log fields got %q, %d MB, %d files", cfg.LogFile, cfg.LogMaxMB, cfg.LogMaxFiles)
	}
	if err := os.WriteFile(path, []byte(`{"log_file":"OFF","log_max_mb":-1,"log_max_files":0}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LogFile != LogFileOff || cfg.LogMaxMB != 10 || cfg.LogMaxFiles != 5 {
		t.Errorf("off config = %q, %d MB, %d files", cfg.LogFile, cfg.LogMaxMB, cfg.LogMaxFiles)
	}
}
