package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Setting keys. They are stored one row per key so a new option can be added
// without a schema migration.
const (
	KeyAPIKey              = "api_key"
	KeyPublicURL           = "public_url"
	KeyFlareSolverr        = "flaresolverr_url"
	KeyDefaultInterval     = "default_interval_seconds"
	KeyRetentionDays       = "retention_days"
	KeyMaxTorrentBytes     = "max_torrent_bytes"
	KeyFetchTimeout        = "fetch_timeout_seconds"
	KeyWorkers             = "workers"
	KeyMaxTorrentsPerRun   = "max_torrents_per_run"
	KeyFetchTorrents       = "fetch_torrents"
	KeyUserAgent           = "user_agent"
	KeyUIUser              = "ui_user"
	KeyUIPasswordHash      = "ui_password_hash"
	KeyTorznabDownloadFac  = "torznab_download_factor"
	KeyTorznabUploadFactor = "torznab_upload_factor"
	KeyNotifyWebhook       = "notify_webhook_url"
	KeyNotifyTelegramToken = "notify_telegram_token"
	KeyNotifyTelegramChat  = "notify_telegram_chat_id"
	KeyNotifyNewReleases   = "notify_new_releases"
	KeyNotifyFailures      = "notify_failures"
	KeyAutoDisableAfter    = "auto_disable_after"
	KeyLogLevel            = "log_level"
	KeyAPIRateLimit        = "api_rate_limit_per_minute"
	KeyBackupEnabled       = "backup_enabled"
	KeyBackupInterval      = "backup_interval_hours"
	KeyBackupKeep          = "backup_keep"
	KeyTorznabLive         = "torznab_live_search"
	KeyTorznabLiveTimeout  = "torznab_live_timeout_seconds"
	KeyDownloadClient      = "download_client"
	KeyDownloadClientURL   = "download_client_url"
	KeyDownloadClientUser  = "download_client_user"
	KeyDownloadClientPass  = "download_client_password"
	KeyDownloadClientCat   = "download_client_category"
	KeyDownloadClientPath  = "download_client_save_path"
	KeyDownloadClientPause = "download_client_paused"
	KeyHostRate            = "host_rate"
	KeyHostBurst           = "host_burst"
	KeyBreakerThreshold    = "breaker_threshold"
	KeyBreakerCooldown     = "breaker_cooldown"
)

// Settings are the runtime options edited from the web interface.
type Settings struct {
	APIKey                 string
	PublicURL              string
	FlareSolverrURL        string
	DefaultIntervalSeconds int
	RetentionDays          int
	MaxTorrentBytes        int64
	FetchTimeoutSeconds    int
	Workers                int
	MaxTorrentsPerRun      int
	FetchTorrents          bool
	UserAgent              string
	UIUser                 string
	UIPasswordHash         string
	TorznabDownloadFactor  int
	TorznabUploadFactor    int
	NotifyWebhookURL       string
	NotifyTelegramToken    string
	NotifyTelegramChatID   string
	NotifyNewReleases      bool
	NotifyFailures         bool
	AutoDisableAfter       int
	LogLevel               string
	APIRateLimit           int
	BackupEnabled          bool
	BackupIntervalHours    int
	BackupKeep             int
	TorznabLiveSearch      bool
	TorznabLiveTimeout     int
	DownloadClient         string
	DownloadClientURL      string
	DownloadClientUser     string
	DownloadClientPassword string
	DownloadClientCategory string
	DownloadClientSavePath string
	DownloadClientPaused   bool
	HostRate               float64
	HostBurst              int
	BreakerThreshold       int
	BreakerCooldownSeconds int
}

// DefaultSettings returns the options used before the first save.
func DefaultSettings() Settings {
	return Settings{
		DefaultIntervalSeconds: 3600,
		RetentionDays:          0,
		MaxTorrentBytes:        8 << 20,
		FetchTimeoutSeconds:    45,
		Workers:                2,
		MaxTorrentsPerRun:      30,
		FetchTorrents:          true,
		UserAgent:              "indexarr/1.0",
		TorznabDownloadFactor:  0,
		TorznabUploadFactor:    1,
		NotifyNewReleases:      true,
		NotifyFailures:         true,
		AutoDisableAfter:       0,
		BackupEnabled:          true,
		BackupIntervalHours:    24,
		BackupKeep:             7,
		TorznabLiveSearch:      false,
		TorznabLiveTimeout:     20,
		DownloadClient:         "none",
		HostRate:               1,
		HostBurst:              4,
		BreakerThreshold:       5,
		BreakerCooldownSeconds: 60,
	}
}

// DefaultInterval is the scheduler interval when a source does not override it.
func (s Settings) DefaultInterval() time.Duration {
	seconds := s.DefaultIntervalSeconds
	if seconds < 60 {
		seconds = 60
	}
	return time.Duration(seconds) * time.Second
}

// FetchTimeout is the per-request timeout towards an indexer or feed.
func (s Settings) FetchTimeout() time.Duration {
	seconds := s.FetchTimeoutSeconds
	if seconds < 5 {
		seconds = 5
	}
	return time.Duration(seconds) * time.Second
}

// BreakerCooldown is how long a host stays blocked after repeated failures or
// a Retry-After response.
func (s Settings) BreakerCooldown() time.Duration {
	seconds := s.BreakerCooldownSeconds
	if seconds < 1 {
		seconds = 60
	}
	return time.Duration(seconds) * time.Second
}

// LoadSettings reads every option, falling back to the defaults for missing or
// malformed rows.
func (s *Store) LoadSettings(ctx context.Context) (Settings, error) {
	out := DefaultSettings()
	rows, err := s.db.QueryContext(ctx, `SELECT key, value FROM settings`)
	if err != nil {
		return out, fmt.Errorf("load settings: %w", err)
	}
	defer rows.Close()
	values := map[string]string{}
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return out, err
		}
		values[key] = value
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	out.APIKey = strings.TrimSpace(values[KeyAPIKey])
	out.PublicURL = strings.TrimRight(strings.TrimSpace(values[KeyPublicURL]), "/")
	out.FlareSolverrURL = strings.TrimRight(strings.TrimSpace(values[KeyFlareSolverr]), "/")
	out.UIUser = strings.TrimSpace(values[KeyUIUser])
	out.UIPasswordHash = values[KeyUIPasswordHash]
	out.UserAgent = strings.TrimSpace(values[KeyUserAgent])
	if out.UserAgent == "" {
		out.UserAgent = DefaultSettings().UserAgent
	}
	out.DefaultIntervalSeconds = intValue(values, KeyDefaultInterval, 3600, 60, 30*24*3600)
	out.RetentionDays = intValue(values, KeyRetentionDays, 0, 0, 3650)
	out.MaxTorrentBytes = int64(intValue(values, KeyMaxTorrentBytes, 8<<20, 64<<10, 64<<20))
	out.FetchTimeoutSeconds = intValue(values, KeyFetchTimeout, 45, 5, 300)
	out.Workers = intValue(values, KeyWorkers, 2, 1, 16)
	out.MaxTorrentsPerRun = intValue(values, KeyMaxTorrentsPerRun, 30, 0, 200)
	out.TorznabDownloadFactor = intValue(values, KeyTorznabDownloadFac, 0, 0, 100)
	out.TorznabUploadFactor = intValue(values, KeyTorznabUploadFactor, 1, 0, 100)
	out.NotifyWebhookURL = strings.TrimSpace(values[KeyNotifyWebhook])
	out.NotifyTelegramToken = strings.TrimSpace(values[KeyNotifyTelegramToken])
	out.NotifyTelegramChatID = strings.TrimSpace(values[KeyNotifyTelegramChat])
	out.NotifyNewReleases = boolValue(values, KeyNotifyNewReleases, true)
	out.NotifyFailures = boolValue(values, KeyNotifyFailures, true)
	out.AutoDisableAfter = intValue(values, KeyAutoDisableAfter, 0, 0, 100)
	out.LogLevel = strings.ToLower(strings.TrimSpace(values[KeyLogLevel]))
	switch out.LogLevel {
	case "", "debug", "info", "warn", "error":
	default:
		out.LogLevel = ""
	}
	out.APIRateLimit = intValue(values, KeyAPIRateLimit, 0, 0, 100000)
	out.BackupEnabled = boolValue(values, KeyBackupEnabled, true)
	out.BackupIntervalHours = intValue(values, KeyBackupInterval, 24, 6, 168)
	out.BackupKeep = intValue(values, KeyBackupKeep, 7, 1, 60)
	out.TorznabLiveSearch = boolValue(values, KeyTorznabLive, false)
	out.TorznabLiveTimeout = intValue(values, KeyTorznabLiveTimeout, 20, 5, 120)
	out.DownloadClient = strings.ToLower(strings.TrimSpace(values[KeyDownloadClient]))
	switch out.DownloadClient {
	case "qbittorrent", "transmission":
	case "", "none":
		out.DownloadClient = "none"
	default:
		out.DownloadClient = "none"
	}
	out.DownloadClientURL = strings.TrimRight(strings.TrimSpace(values[KeyDownloadClientURL]), "/")
	out.DownloadClientUser = strings.TrimSpace(values[KeyDownloadClientUser])
	out.DownloadClientPassword = values[KeyDownloadClientPass]
	out.DownloadClientCategory = strings.TrimSpace(values[KeyDownloadClientCat])
	out.DownloadClientSavePath = strings.TrimSpace(values[KeyDownloadClientPath])
	out.DownloadClientPaused = boolValue(values, KeyDownloadClientPause, false)
	out.HostRate = floatValue(values, KeyHostRate, 1, 0, 100)
	out.HostBurst = intValue(values, KeyHostBurst, 4, 1, 100)
	out.BreakerThreshold = intValue(values, KeyBreakerThreshold, 5, 1, 100)
	out.BreakerCooldownSeconds = intValue(values, KeyBreakerCooldown, 60, 1, 3600)
	if raw, ok := values[KeyFetchTorrents]; ok {
		out.FetchTorrents = raw == "1" || strings.EqualFold(raw, "true")
	}
	return out, nil
}

// SaveSettings writes every option.
func (s *Store) SaveSettings(ctx context.Context, in Settings) error {
	pairs := map[string]string{
		KeyAPIKey:              in.APIKey,
		KeyPublicURL:           strings.TrimRight(strings.TrimSpace(in.PublicURL), "/"),
		KeyFlareSolverr:        strings.TrimRight(strings.TrimSpace(in.FlareSolverrURL), "/"),
		KeyDefaultInterval:     strconv.Itoa(in.DefaultIntervalSeconds),
		KeyRetentionDays:       strconv.Itoa(in.RetentionDays),
		KeyMaxTorrentBytes:     strconv.FormatInt(in.MaxTorrentBytes, 10),
		KeyFetchTimeout:        strconv.Itoa(in.FetchTimeoutSeconds),
		KeyWorkers:             strconv.Itoa(in.Workers),
		KeyMaxTorrentsPerRun:   strconv.Itoa(in.MaxTorrentsPerRun),
		KeyUserAgent:           strings.TrimSpace(in.UserAgent),
		KeyUIUser:              strings.TrimSpace(in.UIUser),
		KeyUIPasswordHash:      in.UIPasswordHash,
		KeyTorznabDownloadFac:  strconv.Itoa(in.TorznabDownloadFactor),
		KeyTorznabUploadFactor: strconv.Itoa(in.TorznabUploadFactor),
		KeyNotifyWebhook:       strings.TrimSpace(in.NotifyWebhookURL),
		KeyNotifyTelegramToken: strings.TrimSpace(in.NotifyTelegramToken),
		KeyNotifyTelegramChat:  strings.TrimSpace(in.NotifyTelegramChatID),
		KeyNotifyNewReleases:   boolString(in.NotifyNewReleases),
		KeyNotifyFailures:      boolString(in.NotifyFailures),
		KeyAutoDisableAfter:    strconv.Itoa(in.AutoDisableAfter),
		KeyLogLevel:            strings.ToLower(strings.TrimSpace(in.LogLevel)),
		KeyAPIRateLimit:        strconv.Itoa(in.APIRateLimit),
		KeyBackupEnabled:       boolString(in.BackupEnabled),
		KeyBackupInterval:      strconv.Itoa(in.BackupIntervalHours),
		KeyBackupKeep:          strconv.Itoa(in.BackupKeep),
		KeyTorznabLive:         boolString(in.TorznabLiveSearch),
		KeyTorznabLiveTimeout:  strconv.Itoa(in.TorznabLiveTimeout),
		KeyDownloadClient:      strings.ToLower(strings.TrimSpace(in.DownloadClient)),
		KeyDownloadClientURL:   strings.TrimRight(strings.TrimSpace(in.DownloadClientURL), "/"),
		KeyDownloadClientUser:  strings.TrimSpace(in.DownloadClientUser),
		KeyDownloadClientPass:  in.DownloadClientPassword,
		KeyDownloadClientCat:   strings.TrimSpace(in.DownloadClientCategory),
		KeyDownloadClientPath:  strings.TrimSpace(in.DownloadClientSavePath),
		KeyDownloadClientPause: boolString(in.DownloadClientPaused),
		KeyHostRate:            strconv.FormatFloat(in.HostRate, 'f', -1, 64),
		KeyHostBurst:           strconv.Itoa(in.HostBurst),
		KeyBreakerThreshold:    strconv.Itoa(in.BreakerThreshold),
		KeyBreakerCooldown:     strconv.Itoa(in.BreakerCooldownSeconds),
	}
	if in.FetchTorrents {
		pairs[KeyFetchTorrents] = "1"
	} else {
		pairs[KeyFetchTorrents] = "0"
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for key, value := range pairs {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO settings(key, value) VALUES(?, ?)
			 ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value); err != nil {
			return fmt.Errorf("save setting %s: %w", key, err)
		}
	}
	return tx.Commit()
}

// SetSetting writes one option, leaving the others untouched.
func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO settings(key, value) VALUES(?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// EnsureAPIKey returns the Torznab key, generating one on first start.
func (s *Store) EnsureAPIKey(ctx context.Context) (string, bool, error) {
	settings, err := s.LoadSettings(ctx)
	if err != nil {
		return "", false, err
	}
	if settings.APIKey != "" {
		return settings.APIKey, false, nil
	}
	key, err := NewAPIKey()
	if err != nil {
		return "", false, err
	}
	if err := s.SetSetting(ctx, KeyAPIKey, key); err != nil {
		return "", false, err
	}
	return key, true, nil
}

// NewAPIKey returns a random 64-character hexadecimal key.
func NewAPIKey() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate api key: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

func intValue(values map[string]string, key string, fallback, min, max int) int {
	raw, ok := values[key]
	if !ok {
		return fallback
	}
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value < min || value > max {
		return fallback
	}
	return value
}

func floatValue(values map[string]string, key string, fallback, min, max float64) float64 {
	raw, ok := values[key]
	if !ok {
		return fallback
	}
	value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || value < min || value > max {
		return fallback
	}
	return value
}

func boolValue(values map[string]string, key string, fallback bool) bool {
	raw, ok := values[key]
	if !ok {
		return fallback
	}
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "on", "yes":
		return true
	case "0", "false", "off", "no":
		return false
	default:
		return fallback
	}
}

func boolString(value bool) string {
	if value {
		return "1"
	}
	return "0"
}
