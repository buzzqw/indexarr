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

func writeTestKey(t *testing.T, path string, key []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(hex.EncodeToString(key)), 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertKey(t *testing.T, v *Vault, key []byte, marker string) {
	t.Helper()
	blob, err := v.SealString(marker)
	if err != nil {
		t.Fatalf("SealString: %v", err)
	}
	expected, err := New(key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := expected.OpenString(blob); err != nil {
		t.Fatalf("resolved vault does not use the expected key source")
	}
}

func TestResolveKeySources(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "data", "indexarr.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		t.Fatal(err)
	}

	t.Run("env value wins", func(t *testing.T) {
		envKey := testKey(21)
		fileKey := testKey(22)
		envFile := filepath.Join(dir, "env-file.key")
		configFile := filepath.Join(dir, "config.key")
		writeTestKey(t, envFile, fileKey)
		writeTestKey(t, configFile, fileKey)
		writeTestKey(t, dbPath+".key", fileKey)

		v, source, err := Resolve(ResolveOptions{
			EnvKey:        hex.EncodeToString(envKey),
			EnvKeyFile:    envFile,
			ConfigPath:    configFile,
			DBPath:        dbPath,
			UserConfigDir: func() (string, error) { return filepath.Join(dir, "config-home"), nil },
		})
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if source.Kind != keyKindEnv || source.Path != "" || source.InDataDir {
			t.Fatalf("unexpected source: %#v", source)
		}
		assertKey(t, v, envKey, "env")
	})

	t.Run("env file wins over config", func(t *testing.T) {
		envKey := testKey(23)
		otherKey := testKey(24)
		envFile := filepath.Join(dir, "docker-secret.key")
		configFile := filepath.Join(dir, "config-file.key")
		writeTestKey(t, envFile, envKey)
		writeTestKey(t, configFile, otherKey)

		v, source, err := Resolve(ResolveOptions{
			EnvKeyFile:    envFile,
			ConfigPath:    configFile,
			DBPath:        dbPath,
			UserConfigDir: func() (string, error) { return filepath.Join(dir, "config-home"), nil },
		})
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if source.Kind != keyKindEnvFile || source.Path != envFile {
			t.Fatalf("unexpected source: %#v", source)
		}
		assertKey(t, v, envKey, "env-file")
	})

	t.Run("config path", func(t *testing.T) {
		key := testKey(25)
		configFile := filepath.Join(dir, "configured.key")
		writeTestKey(t, configFile, key)

		v, source, err := Resolve(ResolveOptions{
			ConfigPath:    configFile,
			DBPath:        dbPath,
			UserConfigDir: func() (string, error) { return filepath.Join(dir, "config-home"), nil },
		})
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if source.Kind != keyKindConfig || source.Path != configFile {
			t.Fatalf("unexpected source: %#v", source)
		}
		assertKey(t, v, key, "config")
	})

	t.Run("legacy data dir", func(t *testing.T) {
		key := testKey(26)
		writeTestKey(t, dbPath+".key", key)

		v, source, err := Resolve(ResolveOptions{
			DBPath: dbPath,
			UserConfigDir: func() (string, error) {
				return "", errors.New("must not be called while a legacy key exists")
			},
		})
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if source.Kind != keyKindDataDirKey || !source.InDataDir || source.Path != dbPath+".key" {
			t.Fatalf("unexpected source: %#v", source)
		}
		assertKey(t, v, key, "legacy")
		if err := os.Remove(dbPath + ".key"); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("user config default", func(t *testing.T) {
		configHome := filepath.Join(dir, "xdg-config")
		v, source, err := Resolve(ResolveOptions{
			DBPath:        dbPath,
			UserConfigDir: func() (string, error) { return configHome, nil },
		})
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		wantPath := filepath.Join(configHome, "indexarr", "master.key")
		if source.Kind != keyKindUserConfig || source.Path != wantPath || source.InDataDir {
			t.Fatalf("unexpected source: %#v", source)
		}
		info, err := os.Stat(wantPath)
		if err != nil {
			t.Fatalf("default key was not created: %v", err)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
			t.Errorf("key permissions = %o, want 600", info.Mode().Perm())
		}
		data, err := os.ReadFile(wantPath)
		if err != nil {
			t.Fatal(err)
		}
		assertKey(t, v, data, "user-config")
	})

	t.Run("user config unavailable falls back to data dir", func(t *testing.T) {
		fallbackDB := filepath.Join(dir, "fallback", "indexarr.db")
		v, source, err := Resolve(ResolveOptions{
			DBPath:        fallbackDB,
			UserConfigDir: func() (string, error) { return "", errors.New("no home") },
		})
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if source.Kind != keyKindDataDirFallback || source.Path != fallbackDB+".key" || !source.InDataDir {
			t.Fatalf("unexpected source: %#v", source)
		}
		data, err := os.ReadFile(fallbackDB + ".key")
		if err != nil {
			t.Fatalf("fallback key was not created: %v", err)
		}
		assertKey(t, v, data, "fallback")
	})
}

func TestResolveEnvFileMissingDoesNotCreateKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.key")
	_, source, err := Resolve(ResolveOptions{
		EnvKeyFile: path,
		DBPath:     filepath.Join(t.TempDir(), "indexarr.db"),
		UserConfigDir: func() (string, error) {
			return t.TempDir(), nil
		},
	})
	if err == nil {
		t.Fatal("Resolve accepted a missing env key file")
	}
	if source.Kind != keyKindEnvFile {
		t.Fatalf("unexpected source: %#v", source)
	}
	if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("missing env key file was created: %v", statErr)
	}
}

func TestExportKey(t *testing.T) {
	dir := t.TempDir()
	key := testKey(27)
	path := filepath.Join(dir, "master.key")
	writeTestKey(t, path, key)
	encoded, source, err := ExportKey(ResolveOptions{ConfigPath: path, DBPath: filepath.Join(dir, "indexarr.db")})
	if err != nil {
		t.Fatalf("ExportKey: %v", err)
	}
	if source.Kind != keyKindConfig {
		t.Fatalf("unexpected source: %#v", source)
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("export is not base64: %v", err)
	}
	if string(decoded) != string(key) {
		t.Fatal("exported key does not match the resolved key")
	}
}

func TestMoveLegacyKey(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "data", "indexarr.db")
	key := testKey(28)
	writeTestKey(t, dbPath+".key", key)
	target := filepath.Join(dir, "secrets", "vault.key")

	moved, err := MoveLegacyKey(dbPath, target)
	if err != nil {
		t.Fatalf("MoveLegacyKey: %v", err)
	}
	if moved != target {
		t.Fatalf("moved = %q, want %q", moved, target)
	}
	if _, err := os.Stat(dbPath + ".key"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy key still present: %v", err)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != hex.EncodeToString(key) {
		t.Fatal("moved key content changed")
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("moved key permissions = %o, want 600", info.Mode().Perm())
	}
	if _, err := MoveLegacyKey(dbPath, target); err == nil {
		t.Fatal("second move of a missing legacy key succeeded")
	}
}

func TestMoveLegacyKeyRefusesToOverwrite(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "indexarr.db")
	writeTestKey(t, dbPath+".key", testKey(29))
	target := filepath.Join(dir, "existing.key")
	if err := os.WriteFile(target, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := MoveLegacyKey(dbPath, target); err == nil {
		t.Fatal("MoveLegacyKey overwrote an existing key")
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "existing" {
		t.Fatal("existing target key was modified")
	}
	if _, err := os.Stat(dbPath + ".key"); err != nil {
		t.Fatalf("legacy key disappeared after refused move: %v", err)
	}
}

func TestKeySourceInfoHasNoKeyMaterial(t *testing.T) {
	source := KeySource{
		Kind:      keyKindDataDirKey,
		Path:      "/srv/indexarr/indexarr.db.key",
		InDataDir: true,
	}
	info := source.Info()
	if !info.Available || info.Kind != keyKindDataDirKey || info.Path != source.Path || !info.InDataDir {
		t.Fatalf("unexpected info: %#v", info)
	}
	if !strings.Contains(info.Hint, "move-key") {
		t.Errorf("hint does not explain how to move the key: %q", info.Hint)
	}
}
