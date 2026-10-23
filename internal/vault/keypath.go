package vault

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// KeySource describes where the master key came from, for the UI and logs. It
// never contains key material.
type KeySource struct {
	// Kind is one of: "env", "env-file", "config", "legacy-data-dir",
	// "user-config", "data-dir-fallback".
	Kind string
	// Path is empty for "env" and the key file path otherwise.
	Path string
	// InDataDir is true when the key sits next to the database.
	InDataDir bool
}

// Info is the safe representation of the key location exposed to the UI. It
// never contains key material.
type Info struct {
	Available bool
	Kind      string
	Path      string
	InDataDir bool
	// Hint is a short, non-secret explanation for the operator.
	Hint string
}

// Info returns the UI-safe description of this key source.
func (k KeySource) Info() Info {
	info := Info{
		Available: k.Kind != "",
		Kind:      k.Kind,
		Path:      k.Path,
		InDataDir: k.InDataDir,
	}
	switch k.Kind {
	case "env":
		info.Hint = "The master key is supplied by INDEXARR_MASTER_KEY. Back it up in your secret manager."
	case "env-file":
		info.Hint = "The master key is read from INDEXARR_MASTER_KEY_FILE. Back up that file."
	case "config":
		info.Hint = "The master key is read from vault_key_path. Back up that file."
	case "legacy-data-dir":
		info.Hint = "The master key is still next to the database: run 'indexarr vault move-key -to <path>'."
	case "user-config":
		info.Hint = "The master key lives in the user configuration directory. Back up that file."
	case "data-dir-fallback":
		info.Hint = "No user configuration directory is available, so the key lives next to the database. Back it up separately."
	}
	if k.InDataDir && k.Kind != "legacy-data-dir" && k.Kind != "data-dir-fallback" {
		info.Hint = "The master key is in the data directory: move it outside before copying the data volume."
	}
	return info
}

const (
	keyKindEnv             = "env"
	keyKindEnvFile         = "env-file"
	keyKindConfig          = "config"
	keyKindLegacyDataDir   = "legacy-data-dir"
	keyKindUserConfig      = "user-config"
	keyKindDataDirFallback = "data-dir-fallback"
)

// ResolveOptions describes the inputs of the documented key resolution order.
// EnvKey and EnvKeyFile are the values of INDEXARR_MASTER_KEY and
// INDEXARR_MASTER_KEY_FILE. UserConfigDir is injectable so tests never depend on
// the host environment.
type ResolveOptions struct {
	EnvKey        string
	EnvKeyFile    string
	ConfigPath    string
	DBPath        string
	UserConfigDir func() (string, error)
}

// Resolve applies the documented order and returns the vault and where its key
// lives. It never logs or returns key material.
func Resolve(opts ResolveOptions) (*Vault, KeySource, error) {
	key, source, err := resolveKey(opts)
	if err != nil {
		return nil, source, err
	}
	v, err := New(key)
	if err != nil {
		return nil, source, err
	}
	return v, source, nil
}

// ExportKey returns the resolved key as standard base64. It is used by the
// `vault export-key` command only; callers must treat the result as key
// material and never log it.
func ExportKey(opts ResolveOptions) (string, KeySource, error) {
	key, source, err := resolveKey(opts)
	if err != nil {
		return "", source, err
	}
	return base64.StdEncoding.EncodeToString(key), source, nil
}

func resolveKey(opts ResolveOptions) ([]byte, KeySource, error) {
	if raw := strings.TrimSpace(opts.EnvKey); raw != "" {
		key, err := decodeKey(raw)
		if err != nil {
			return nil, KeySource{Kind: keyKindEnv}, err
		}
		return key, KeySource{Kind: keyKindEnv}, nil
	}
	if path := strings.TrimSpace(opts.EnvKeyFile); path != "" {
		key, err := readKeyFile(path)
		if err != nil {
			return nil, KeySource{Kind: keyKindEnvFile, Path: path}, err
		}
		return key, KeySource{
			Kind:      keyKindEnvFile,
			Path:      path,
			InDataDir: inDataDir(path, opts.DBPath),
		}, nil
	}
	if path := strings.TrimSpace(opts.ConfigPath); path != "" {
		key, err := loadOrCreateKey(path)
		if err != nil {
			return nil, KeySource{Kind: keyKindConfig, Path: path}, err
		}
		return key, KeySource{
			Kind:      keyKindConfig,
			Path:      path,
			InDataDir: inDataDir(path, opts.DBPath),
		}, nil
	}
	if legacy := legacyKeyPath(opts.DBPath); legacy != "" {
		if _, err := os.Stat(legacy); err == nil {
			key, readErr := readKeyFile(legacy)
			if readErr != nil {
				return nil, KeySource{
					Kind:      keyKindLegacyDataDir,
					Path:      legacy,
					InDataDir: true,
				}, readErr
			}
			return key, KeySource{
				Kind:      keyKindLegacyDataDir,
				Path:      legacy,
				InDataDir: true,
			}, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, KeySource{
				Kind:      keyKindLegacyDataDir,
				Path:      legacy,
				InDataDir: true,
			}, fmt.Errorf("vault: inspect legacy master key file %s: %w", legacy, err)
		}
	}

	userConfigDir := opts.UserConfigDir
	if userConfigDir == nil {
		userConfigDir = os.UserConfigDir
	}
	if dir, err := userConfigDir(); err == nil && strings.TrimSpace(dir) != "" {
		path := filepath.Join(dir, "indexarr", "master.key")
		key, loadErr := loadOrCreateKey(path)
		if loadErr != nil {
			return nil, KeySource{
				Kind:      keyKindUserConfig,
				Path:      path,
				InDataDir: inDataDir(path, opts.DBPath),
			}, loadErr
		}
		return key, KeySource{
			Kind:      keyKindUserConfig,
			Path:      path,
			InDataDir: inDataDir(path, opts.DBPath),
		}, nil
	}

	fallback := legacyKeyPath(opts.DBPath)
	if fallback == "" {
		return nil, KeySource{}, ErrKeyRequired
	}
	key, err := loadOrCreateKey(fallback)
	if err != nil {
		return nil, KeySource{
			Kind:      keyKindDataDirFallback,
			Path:      fallback,
			InDataDir: true,
		}, err
	}
	return key, KeySource{
		Kind:      keyKindDataDirFallback,
		Path:      fallback,
		InDataDir: true,
	}, nil
}

// MoveLegacyKey moves <db>.key to target without ever printing key material.
// It refuses to overwrite an existing target and always creates the new file
// with 0600 permissions.
func MoveLegacyKey(dbPath, target string) (string, error) {
	source := legacyKeyPath(dbPath)
	if source == "" {
		return "", errors.New("vault: no database path given")
	}
	target = strings.TrimSpace(target)
	if target == "" {
		return "", errors.New("vault: target key path is required")
	}
	absoluteTarget, err := filepath.Abs(target)
	if err != nil {
		return "", fmt.Errorf("vault: resolve target key path: %w", err)
	}
	absoluteSource, err := filepath.Abs(source)
	if err != nil {
		return "", fmt.Errorf("vault: resolve source key path: %w", err)
	}
	if absoluteSource == absoluteTarget {
		return "", errors.New("vault: source and target key paths are the same")
	}
	info, err := os.Stat(absoluteSource)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("vault: no legacy key at %s", absoluteSource)
		}
		return "", fmt.Errorf("vault: inspect legacy key: %w", err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("vault: legacy key path %s is a directory", absoluteSource)
	}
	if _, err := os.Stat(absoluteTarget); err == nil {
		return "", fmt.Errorf("vault: refusing to overwrite existing key file %s", absoluteTarget)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("vault: inspect target key file: %w", err)
	}
	if dir := filepath.Dir(absoluteTarget); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return "", fmt.Errorf("vault: create target key directory: %w", err)
		}
	}
	data, err := os.ReadFile(absoluteSource)
	if err != nil {
		return "", fmt.Errorf("vault: read legacy key: %w", err)
	}
	out, err := os.OpenFile(absoluteTarget, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", fmt.Errorf("vault: create target key file: %w", err)
	}
	if _, err := out.Write(data); err != nil {
		_ = out.Close()
		_ = os.Remove(absoluteTarget)
		return "", fmt.Errorf("vault: write target key file: %w", err)
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		_ = os.Remove(absoluteTarget)
		return "", fmt.Errorf("vault: sync target key file: %w", err)
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(absoluteTarget)
		return "", fmt.Errorf("vault: close target key file: %w", err)
	}
	if err := os.Chmod(absoluteTarget, 0o600); err != nil {
		return absoluteTarget, fmt.Errorf("vault: set target key permissions: %w", err)
	}
	if err := os.Remove(absoluteSource); err != nil {
		return absoluteTarget, fmt.Errorf("vault: remove legacy key after copy: %w", err)
	}
	return absoluteTarget, nil
}

// readKeyFile reads an existing key file; unlike loadOrCreateKey it never
// creates one, so a missing Docker secret is a clear error instead of a new key
// that silently makes the old secrets unreadable.
func readKeyFile(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("vault: read master key file %s: %w", path, err)
	}
	if len(data) == keySize {
		return data, nil
	}
	key, err := decodeKey(string(data))
	if err != nil {
		return nil, fmt.Errorf("vault: master key file %s: %w", path, err)
	}
	return key, nil
}

func legacyKeyPath(dbPath string) string {
	dbPath = strings.TrimSpace(dbPath)
	if dbPath == "" {
		return ""
	}
	return dbPath + ".key"
}

func inDataDir(keyPath, dbPath string) bool {
	keyPath = strings.TrimSpace(keyPath)
	dbPath = strings.TrimSpace(dbPath)
	if keyPath == "" || dbPath == "" {
		return false
	}
	keyAbs, keyErr := filepath.Abs(keyPath)
	dbAbs, dbErr := filepath.Abs(dbPath)
	if keyErr != nil || dbErr != nil {
		return false
	}
	return filepath.Dir(keyAbs) == filepath.Dir(dbAbs)
}
