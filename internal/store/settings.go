package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/buzzqw/indexarr/internal/vault"
)

// Setting keys. They are stored one row per key so a new option can be added
// without a schema migration.
const (
	KeyAPIKey               = "api_key"
	KeyPublicURL            = "public_url"
	KeyFlareSolverr         = "flaresolverr_url"
	KeyFlareSolverrTimeout  = "flaresolverr_timeout_seconds"
	KeyDefaultInterval      = "default_interval_seconds"
	KeyRetentionDays        = "retention_days"
	KeyHistoryRetentionDays = "history_retention_days"
	KeyStoredSourceLimit    = "stored_source_limit"
	KeyStoredRetentionDays  = "stored_retention_days"
	KeyMaxTorrentBytes      = "max_torrent_bytes"
	KeyFetchTimeout         = "fetch_timeout_seconds"
	KeyWorkers              = "workers"
	KeyMaxTorrentsPerRun    = "max_torrents_per_run"
	KeyFetchTorrents        = "fetch_torrents"
	KeyUserAgent            = "user_agent"
	KeyUIUser               = "ui_user"
	KeyUIPasswordHash       = "ui_password_hash"
	KeyTorznabDownloadFac   = "torznab_download_factor"
	KeyTorznabUploadFactor  = "torznab_upload_factor"
	KeyNotifyWebhook        = "notify_webhook_url"
	KeyNotifyTelegramToken  = "notify_telegram_token"
	KeyNotifyTelegramChat   = "notify_telegram_chat_id"
	KeyNotifyNewReleases    = "notify_new_releases"
	KeyNotifyFailures       = "notify_failures"
	KeyNotifyGrabFailed     = "notify_grab_failed"
	KeyNotifyCatalogUpdated = "notify_catalog_updated"
	KeyNotifyBackupFailed   = "notify_backup_failed"
	KeyNotifyCooldown       = "notify_cooldown_minutes"
	KeyUILanguage           = "ui_language"
	KeyAutoDisableAfter     = "auto_disable_after"
	KeyLogLevel             = "log_level"
	KeyAPIRateLimit         = "api_rate_limit_per_minute"
	KeyBackupEnabled        = "backup_enabled"
	KeyBackupInterval       = "backup_interval_hours"
	KeyBackupKeep           = "backup_keep"
	KeyTorznabLive          = "torznab_live_search"
	KeyTorznabLiveTimeout   = "torznab_live_timeout_seconds"
	KeyTorznabProxyMagnets  = "torznab_proxy_magnets"
	KeyTorznabLiveCache     = "torznab_live_cache_seconds"
	KeyTorznabLiveWait      = "torznab_live_wait_seconds"
	KeyDownloadClient       = "download_client"
	KeyDownloadClientURL    = "download_client_url"
	KeyDownloadClientUser   = "download_client_user"
	KeyDownloadClientPass   = "download_client_password"
	KeyDownloadClientCat    = "download_client_category"
	KeyDownloadClientPath   = "download_client_save_path"
	KeyDownloadClientPause  = "download_client_paused"
	KeyHostRate             = "host_rate"
	KeyHostBurst            = "host_burst"
	KeyBreakerThreshold     = "breaker_threshold"
	KeyBreakerCooldown      = "breaker_cooldown"
	KeySetupDismissed       = "setup_dismissed"
	KeySeedersTTLHours      = "seeders_ttl_hours"
)

// Settings are the runtime options edited from the web interface.
type Settings struct {
	APIKey                     string
	PublicURL                  string
	FlareSolverrURL            string
	FlareSolverrTimeoutSeconds int
	DefaultIntervalSeconds     int
	RetentionDays              int
	// HistoryRetentionDays is how long run history, its metrics, app
	// searches and indexer samples are kept. Zero keeps them forever.
	HistoryRetentionDays int
	// StoredSourceLimit is the soft limit of search-only sites whose new
	// releases are memorised. The fourth source warns; it is not blocked.
	StoredSourceLimit int
	// StoredRetentionDays is the retention of releases that came only from a
	// stored source. Zero keeps them forever; favorites and downloads survive.
	StoredRetentionDays   int
	MaxTorrentBytes       int64
	FetchTimeoutSeconds   int
	Workers               int
	MaxTorrentsPerRun     int
	FetchTorrents         bool
	UserAgent             string
	UIUser                string
	UIPasswordHash        string
	TorznabDownloadFactor int
	TorznabUploadFactor   int
	NotifyWebhookURL      string
	NotifyTelegramToken   string
	NotifyTelegramChatID  string
	NotifyNewReleases     bool
	// NotifyFailures gates the indexer state changes: a site that becomes
	// unreachable, blocked, degraded or healthy again.
	NotifyFailures       bool
	NotifyGrabFailed     bool
	NotifyCatalogUpdated bool
	NotifyBackupFailed   bool
	// NotifyCooldownMinutes is the anti-spam window for the same event kind
	// and source. Zero means the documented default.
	NotifyCooldownMinutes int
	// UILanguage is the language the operator uses, also used for the
	// notification messages, because background jobs have no request.
	UILanguage             string
	AutoDisableAfter       int
	LogLevel               string
	APIRateLimit           int
	BackupEnabled          bool
	BackupIntervalHours    int
	BackupKeep             int
	TorznabLiveSearch      bool
	TorznabProxyMagnets    bool // magnet grabs go through /dl and are counted
	TorznabLiveTimeout     int
	TorznabLiveCache       int
	TorznabLiveWait        int
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
	// SetupDismissed remembers that the operator skipped the first-run
	// wizard. It is reopened from Settings → General.
	SetupDismissed  bool
	SeedersTTLHours int
}

// DefaultSettings returns the options used before the first save.
func DefaultSettings() Settings {
	return Settings{
		DefaultIntervalSeconds:     3600,
		RetentionDays:              0,
		HistoryRetentionDays:       30,
		StoredSourceLimit:          3,
		StoredRetentionDays:        90,
		MaxTorrentBytes:            8 << 20,
		FlareSolverrTimeoutSeconds: 60,
		FetchTimeoutSeconds:        45,
		Workers:                    2,
		MaxTorrentsPerRun:          30,
		FetchTorrents:              true,
		UserAgent:                  "indexarr/1.0",
		TorznabDownloadFactor:      0,
		TorznabUploadFactor:        1,
		NotifyNewReleases:          true,
		NotifyFailures:             true,
		NotifyGrabFailed:           true,
		NotifyCatalogUpdated:       true,
		NotifyBackupFailed:         true,
		NotifyCooldownMinutes:      30,
		UILanguage:                 "en",
		AutoDisableAfter:           0,
		BackupEnabled:              true,
		BackupIntervalHours:        24,
		BackupKeep:                 7,
		TorznabLiveSearch:          true,
		TorznabLiveTimeout:         20,
		TorznabLiveCache:           300,
		TorznabLiveWait:            5,
		DownloadClient:             "none",
		HostRate:                   1,
		HostBurst:                  4,
		BreakerThreshold:           5,
		BreakerCooldownSeconds:     600,
		SeedersTTLHours:            6,
	}
}

// DefaultInterval is the scheduler interval when a source does not override it.
// SeedersTTL is how old a provenance row must be before the refresh job
// queries the source again.
func (s Settings) SeedersTTL() time.Duration {
	hours := s.SeedersTTLHours
	if hours < 1 {
		hours = 6
	}
	return time.Duration(hours) * time.Hour
}

// NotifyCooldown is the anti-spam window for one event kind and source.
func (s Settings) NotifyCooldown() time.Duration {
	minutes := s.NotifyCooldownMinutes
	if minutes <= 0 {
		minutes = 30
	}
	if minutes > 7*24*60 {
		minutes = 7 * 24 * 60
	}
	return time.Duration(minutes) * time.Minute
}

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
	out.HistoryRetentionDays = intValue(values, KeyHistoryRetentionDays, 30, 0, 3650)
	out.StoredSourceLimit = intValue(values, KeyStoredSourceLimit, 3, 0, 20)
	out.StoredRetentionDays = intValue(values, KeyStoredRetentionDays, 90, 0, 3650)
	out.MaxTorrentBytes = int64(intValue(values, KeyMaxTorrentBytes, 8<<20, 64<<10, 64<<20))
	out.FlareSolverrTimeoutSeconds = intValue(values, KeyFlareSolverrTimeout, 60, 5, 300)
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
	out.NotifyGrabFailed = boolValue(values, KeyNotifyGrabFailed, true)
	out.NotifyCatalogUpdated = boolValue(values, KeyNotifyCatalogUpdated, true)
	out.NotifyBackupFailed = boolValue(values, KeyNotifyBackupFailed, true)
	out.NotifyCooldownMinutes = intValue(values, KeyNotifyCooldown, 30, 1, 7*24*60)
	out.UILanguage = strings.ToLower(strings.TrimSpace(values[KeyUILanguage]))
	switch out.UILanguage {
	case "en", "it", "de", "fr", "es", "pl", "zh":
	default:
		out.UILanguage = "en"
	}
	// The channels live sealed in app_secrets. Opening them here keeps every
	// caller on the same Settings value, while the plaintext never reaches the
	// database. A missing row or a wrong master key leaves the field empty.
	if s.vault != nil {
		if value, ok := s.openAppSecret(ctx, AppSecretNotifyWebhook); ok {
			out.NotifyWebhookURL = value
		}
		if value, ok := s.openAppSecret(ctx, AppSecretNotifyTelegramToken); ok {
			out.NotifyTelegramToken = value
		}
	}
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
	out.TorznabLiveSearch = boolValue(values, KeyTorznabLive, true)
	out.TorznabProxyMagnets = boolValue(values, KeyTorznabProxyMagnets, false)
	out.TorznabLiveTimeout = intValue(values, KeyTorznabLiveTimeout, 20, 5, 120)
	out.TorznabLiveCache = intValue(values, KeyTorznabLiveCache, 300, 0, 3600)
	out.TorznabLiveWait = intValue(values, KeyTorznabLiveWait, 5, 1, 120)
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
	out.BreakerCooldownSeconds = intValue(values, KeyBreakerCooldown, 600, 1, 3600)
	out.SetupDismissed = boolValue(values, KeySetupDismissed, false)
	out.SeedersTTLHours = intValue(values, KeySeedersTTLHours, 6, 1, 168)
	if raw, ok := values[KeyFetchTorrents]; ok {
		out.FetchTorrents = raw == "1" || strings.EqualFold(raw, "true")
	}
	return out, nil
}

// SaveSettings writes every option.
func (s *Store) SaveSettings(ctx context.Context, in Settings) error {
	pairs := map[string]string{
		KeyAPIKey:               in.APIKey,
		KeyPublicURL:            strings.TrimRight(strings.TrimSpace(in.PublicURL), "/"),
		KeyFlareSolverr:         strings.TrimRight(strings.TrimSpace(in.FlareSolverrURL), "/"),
		KeyFlareSolverrTimeout:  strconv.Itoa(in.FlareSolverrTimeoutSeconds),
		KeyDefaultInterval:      strconv.Itoa(in.DefaultIntervalSeconds),
		KeyRetentionDays:        strconv.Itoa(in.RetentionDays),
		KeyHistoryRetentionDays: strconv.Itoa(in.HistoryRetentionDays),
		KeyStoredSourceLimit:    strconv.Itoa(in.StoredSourceLimit),
		KeyStoredRetentionDays:  strconv.Itoa(in.StoredRetentionDays),
		KeyMaxTorrentBytes:      strconv.FormatInt(in.MaxTorrentBytes, 10),
		KeyFetchTimeout:         strconv.Itoa(in.FetchTimeoutSeconds),
		KeyWorkers:              strconv.Itoa(in.Workers),
		KeyMaxTorrentsPerRun:    strconv.Itoa(in.MaxTorrentsPerRun),
		KeyUserAgent:            strings.TrimSpace(in.UserAgent),
		KeyUIUser:               strings.TrimSpace(in.UIUser),
		KeyUIPasswordHash:       in.UIPasswordHash,
		KeyTorznabDownloadFac:   strconv.Itoa(in.TorznabDownloadFactor),
		KeyTorznabUploadFactor:  strconv.Itoa(in.TorznabUploadFactor),
		KeyNotifyTelegramChat:   strings.TrimSpace(in.NotifyTelegramChatID),
		KeyNotifyNewReleases:    boolString(in.NotifyNewReleases),
		KeyNotifyFailures:       boolString(in.NotifyFailures),
		KeyNotifyGrabFailed:     boolString(in.NotifyGrabFailed),
		KeyNotifyCatalogUpdated: boolString(in.NotifyCatalogUpdated),
		KeyNotifyBackupFailed:   boolString(in.NotifyBackupFailed),
		KeyNotifyCooldown:       strconv.Itoa(in.NotifyCooldownMinutes),
		KeyUILanguage:           strings.ToLower(strings.TrimSpace(in.UILanguage)),
		KeyAutoDisableAfter:     strconv.Itoa(in.AutoDisableAfter),
		KeyLogLevel:             strings.ToLower(strings.TrimSpace(in.LogLevel)),
		KeyAPIRateLimit:         strconv.Itoa(in.APIRateLimit),
		KeyBackupEnabled:        boolString(in.BackupEnabled),
		KeyBackupInterval:       strconv.Itoa(in.BackupIntervalHours),
		KeyBackupKeep:           strconv.Itoa(in.BackupKeep),
		KeyTorznabLive:          boolString(in.TorznabLiveSearch),
		KeyTorznabProxyMagnets:  boolString(in.TorznabProxyMagnets),
		KeyTorznabLiveTimeout:   strconv.Itoa(in.TorznabLiveTimeout),
		KeyTorznabLiveCache:     strconv.Itoa(in.TorznabLiveCache),
		KeyTorznabLiveWait:      strconv.Itoa(in.TorznabLiveWait),
		KeyDownloadClient:       strings.ToLower(strings.TrimSpace(in.DownloadClient)),
		KeyDownloadClientURL:    strings.TrimRight(strings.TrimSpace(in.DownloadClientURL), "/"),
		KeyDownloadClientUser:   strings.TrimSpace(in.DownloadClientUser),
		KeyDownloadClientPass:   in.DownloadClientPassword,
		KeyDownloadClientCat:    strings.TrimSpace(in.DownloadClientCategory),
		KeyDownloadClientPath:   strings.TrimSpace(in.DownloadClientSavePath),
		KeyDownloadClientPause:  boolString(in.DownloadClientPaused),
		KeyHostRate:             strconv.FormatFloat(in.HostRate, 'f', -1, 64),
		KeyHostBurst:            strconv.Itoa(in.HostBurst),
		KeyBreakerThreshold:     strconv.Itoa(in.BreakerThreshold),
		KeyBreakerCooldown:      strconv.Itoa(in.BreakerCooldownSeconds),
		KeySetupDismissed:       boolString(in.SetupDismissed),
		KeySeedersTTLHours:      strconv.Itoa(in.SeedersTTLHours),
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
	// The two notification secrets are sealed in app_secrets, never written in
	// clear. Without a vault the legacy plaintext rows are kept, so an
	// installation with no master key still delivers what it can.
	if s.vault != nil {
		for _, item := range []struct{ secret, value string }{
			{AppSecretNotifyWebhook, strings.TrimSpace(in.NotifyWebhookURL)},
			{AppSecretNotifyTelegramToken, strings.TrimSpace(in.NotifyTelegramToken)},
		} {
			if err := saveSealedSetting(ctx, tx, s.vault, item.secret, item.value); err != nil {
				return err
			}
		}
		for _, legacy := range []string{KeyNotifyWebhook, KeyNotifyTelegramToken} {
			if _, err := tx.ExecContext(ctx, `DELETE FROM settings WHERE key = ?`, legacy); err != nil {
				return fmt.Errorf("drop plaintext setting %s: %w", legacy, err)
			}
		}
	} else {
		for key, value := range map[string]string{
			KeyNotifyWebhook:       strings.TrimSpace(in.NotifyWebhookURL),
			KeyNotifyTelegramToken: strings.TrimSpace(in.NotifyTelegramToken),
		} {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO settings(key, value) VALUES(?, ?)
				 ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value); err != nil {
				return fmt.Errorf("save setting %s: %w", key, err)
			}
		}
	}
	return tx.Commit()
}

// saveSealedSetting seals (or removes) one settings-level secret inside an
// open transaction. An empty value removes the row.
func saveSealedSetting(ctx context.Context, tx *sql.Tx, v *vault.Vault, key, plaintext string) error {
	if plaintext == "" {
		if _, err := tx.ExecContext(ctx, `DELETE FROM app_secrets WHERE key = ?`, key); err != nil {
			return fmt.Errorf("remove app secret %q: %w", key, err)
		}
		return nil
	}
	nonce, ciphertext, err := v.Seal(plaintext)
	if err != nil {
		return fmt.Errorf("seal app secret %q: %w", key, err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO app_secrets(key, nonce, ciphertext, updated_at)
		VALUES(?, ?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET
			nonce = excluded.nonce,
			ciphertext = excluded.ciphertext,
			updated_at = excluded.updated_at`,
		key, nonce, ciphertext, time.Now().Unix()); err != nil {
		return fmt.Errorf("save app secret %q: %w", key, err)
	}
	return nil
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
