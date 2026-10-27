// Package config loads the small bootstrap configuration of indexarr. Everything
// that can change at runtime lives in the database (see store.Settings); the
// file only says where the database is and where the HTTP server listens, so a
// broken or missing file can never lock the operator out of the web interface.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Config is the content of indexarr.json.
type Config struct {
	Listen        string `json:"listen"`
	DBPath        string `json:"db_path"`
	LogLevel      string `json:"log_level"`
	VaultKeyPath  string `json:"vault_key_path"`
	MemoryLimitMB int    `json:"memory_limit_mb"`
	// LogFile is the rotating log file, relative to the configuration
	// directory; "off" writes the log to stdout only.
	LogFile     string `json:"log_file"`
	LogMaxMB    int    `json:"log_max_mb"`
	LogMaxFiles int    `json:"log_max_files"`
}

// LogFileOff is the LogFile value that disables the log file.
const LogFileOff = "off"

// Default returns the configuration used on a first start.
func Default() Config {
	return Config{
		Listen:        ":8788",
		DBPath:        "indexarr.db",
		LogLevel:      "info",
		MemoryLimitMB: 64,
		LogFile:       "logs/indexarr.log",
		LogMaxMB:      10,
		LogMaxFiles:   5,
	}
}

// Load reads path, creating it with the defaults when it does not exist. A
// relative database path is resolved against the configuration file directory
// so a config in /etc does not write the database into the process CWD.
func Load(path string) (Config, error) {
	cfg := Default()
	if strings.TrimSpace(path) == "" {
		path = "indexarr.json"
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return cfg, fmt.Errorf("resolve config path: %w", err)
	}
	data, err := os.ReadFile(abs)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if err := writeDefault(abs, cfg); err != nil {
			return cfg, err
		}
	case err != nil:
		return cfg, fmt.Errorf("read %s: %w", abs, err)
	default:
		if err := json.Unmarshal(data, &cfg); err != nil {
			return cfg, fmt.Errorf("parse %s: %w", abs, err)
		}
	}
	cfg.Listen = strings.TrimSpace(cfg.Listen)
	if cfg.Listen == "" {
		cfg.Listen = Default().Listen
	}
	cfg.DBPath = strings.TrimSpace(cfg.DBPath)
	if cfg.DBPath == "" {
		cfg.DBPath = Default().DBPath
	}
	if !filepath.IsAbs(cfg.DBPath) {
		cfg.DBPath = filepath.Join(filepath.Dir(abs), cfg.DBPath)
	}
	if cfg.MemoryLimitMB < 0 {
		cfg.MemoryLimitMB = Default().MemoryLimitMB
	}
	cfg.VaultKeyPath = strings.TrimSpace(cfg.VaultKeyPath)
	if cfg.VaultKeyPath != "" && !filepath.IsAbs(cfg.VaultKeyPath) {
		cfg.VaultKeyPath = filepath.Join(filepath.Dir(abs), cfg.VaultKeyPath)
	}
	cfg.LogFile = strings.TrimSpace(cfg.LogFile)
	switch {
	case cfg.LogFile == "":
		cfg.LogFile = Default().LogFile
		fallthrough
	case !strings.EqualFold(cfg.LogFile, LogFileOff) && !filepath.IsAbs(cfg.LogFile):
		cfg.LogFile = filepath.Join(filepath.Dir(abs), cfg.LogFile)
	case strings.EqualFold(cfg.LogFile, LogFileOff):
		cfg.LogFile = LogFileOff
	}
	if cfg.LogMaxMB <= 0 {
		cfg.LogMaxMB = Default().LogMaxMB
	}
	if cfg.LogMaxFiles <= 0 {
		cfg.LogMaxFiles = Default().LogMaxFiles
	}
	cfg.LogLevel = strings.ToLower(strings.TrimSpace(cfg.LogLevel))
	switch cfg.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		cfg.LogLevel = "info"
	}
	return cfg, nil
}

func writeDefault(path string, cfg Config) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
