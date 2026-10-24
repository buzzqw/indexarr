package web

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/buzzqw/indexarr/internal/backup"
	"github.com/buzzqw/indexarr/internal/flaresolverr"
	"github.com/buzzqw/indexarr/internal/store"
	"github.com/buzzqw/indexarr/internal/torznabapi"
	"github.com/buzzqw/indexarr/internal/vault"
)

type settingsAPIKey struct {
	ID         int64
	Name       string
	Masked     string
	CreatedAt  time.Time
	LastUsedAt time.Time
	Requests   int64
}

type settingsSection struct {
	ID     string
	Label  string
	URL    string
	Active bool
}

type settingsPage struct {
	base
	Section        string
	Sections       []settingsSection
	Settings       store.Settings
	FieldErrors    map[string]string
	MaxTorrentMB   int64
	TorznabBase    string
	TorznabMasked  string
	AdditionalKeys []settingsAPIKey
	BackupDir      string
	VaultKey       vault.Info
	LogLevel       string
	// Rules
	Rules []store.BlocklistRule
	// Maintenance and backups
	Stats               store.Stats
	Report              store.DBReport
	DBPath              string
	SQLiteVersion       string
	RetentionDays       int
	DefaultPurgeDays    int
	Backups             []backup.Info
	BackupEnabled       bool
	BackupIntervalHours int
	BackupKeep          int
	Indexers            []store.Indexer
	// Advanced
	Definitions []store.Definition
	CatalogRows []definitionRow
	LocalRows   []definitionRow
}

var settingsSections = []settingsSection{
	{ID: "general", Label: "settings.section.general"},
	{ID: "security", Label: "settings.section.security"},
	{ID: "network", Label: "settings.section.network"},
	{ID: "updates", Label: "settings.section.updates"},
	{ID: "rules", Label: "settings.section.rules"},
	{ID: "backup", Label: "settings.section.backup"},
	{ID: "maintenance", Label: "settings.section.maintenance"},
	{ID: "advanced", Label: "settings.section.advanced"},
}

func settingsSectionID(raw string) string {
	raw = strings.ToLower(strings.TrimSpace(raw))
	for _, section := range settingsSections {
		if section.ID == raw {
			return raw
		}
	}
	return "general"
}

// settingsData builds the page model without the API key: the key is only
// available through /settings/secrets/* after an explicit fetch. The masked
// URL contains no secret either.
func (s *Server) settingsData(r *http.Request, settings store.Settings, section string) settingsPage {
	ctx := r.Context()
	keys, _ := s.store.ListAPIKeys(ctx)
	views := make([]settingsAPIKey, 0, len(keys))
	for _, key := range keys {
		views = append(views, settingsAPIKey{
			ID: key.ID, Name: key.Name, Masked: "••••••••",
			CreatedAt: key.CreatedAt, LastUsedAt: key.LastUsedAt, Requests: key.Requests,
		})
	}
	baseURL := torznabapi.BaseURL(r, settings) + "/api"
	public := settings
	// Secrets never travel in the initial HTML. The API key, Telegram token
	// and download-client password are only submitted when the operator enters
	// a new value; an empty field leaves the stored one untouched.
	public.APIKey = ""
	public.NotifyTelegramToken = ""
	public.DownloadClientPassword = ""
	section = settingsSectionID(section)
	sections := make([]settingsSection, 0, len(settingsSections))
	for _, item := range settingsSections {
		item.URL = "/settings?section=" + item.ID
		item.Active = item.ID == section
		sections = append(sections, item)
	}
	data := settingsPage{
		base:           s.newBaseT(r, "shell.settings", "settings"),
		Section:        section,
		Sections:       sections,
		Settings:       public,
		FieldErrors:    map[string]string{},
		MaxTorrentMB:   settings.MaxTorrentBytes / (1 << 20),
		TorznabBase:    baseURL,
		TorznabMasked:  baseURL + "?apikey=••••••",
		AdditionalKeys: views,
		BackupDir:      backup.Dir(s.store.Path()),
		VaultKey:       s.vaultKey,
		LogLevel:       s.levelName(),
		Definitions:    nil,
	}
	if section == "rules" {
		data.Rules, _ = s.store.ListBlocklist(ctx)
	}
	if section == "backup" || section == "maintenance" {
		data.Stats, _ = s.store.LoadStats(ctx)
		data.Report, _ = s.store.LoadDBReport(ctx)
		data.DBPath = s.store.Path()
		_ = s.store.DB().QueryRowContext(ctx, "SELECT sqlite_version()").Scan(&data.SQLiteVersion)
		data.Backups, _ = backup.List(s.store.Path())
		data.Indexers, _ = s.store.ListIndexers(ctx)
		data.RetentionDays = settings.RetentionDays
		data.DefaultPurgeDays = settings.RetentionDays
		if data.DefaultPurgeDays <= 0 {
			data.DefaultPurgeDays = 30
		}
		data.BackupEnabled = settings.BackupEnabled
		data.BackupIntervalHours = settings.BackupIntervalHours
		data.BackupKeep = settings.BackupKeep
	}
	if section == "advanced" {
		data.Definitions, _ = s.store.ListDefinitions(ctx)
		data.CatalogRows, data.LocalRows = s.definitionRows(ctx, nil)
	}
	return data
}

// levelName exposes the runtime log level without importing slog in templates.
func (s *Server) levelName() string {
	if s.level == nil {
		return "info"
	}
	switch s.level.Level() {
	case slog.LevelDebug:
		return "debug"
	case slog.LevelWarn:
		return "warn"
	case slog.LevelError:
		return "error"
	default:
		return "info"
	}
}

func (s *Server) settingsPage(w http.ResponseWriter, r *http.Request) {
	settings, err := s.store.LoadSettings(r.Context())
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	section := settingsSectionID(r.URL.Query().Get("section"))
	s.render(w, r, "settings", s.settingsData(r, settings, section), http.StatusOK)
}

// settingsPublicURL accepts the address proposed from r.Host. It is a POST so
// a page view never changes the configuration.
func (s *Server) settingsPublicURL(w http.ResponseWriter, r *http.Request) {
	raw := strings.TrimRight(strings.TrimSpace(r.FormValue("public_url")), "/")
	if raw == "" {
		raw = suggestedPublicURL(r)
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		redirect(w, r, "/", "", s.translate(r, "settings.error.publicURL"))
		return
	}
	settings, err := s.store.LoadSettings(r.Context())
	if err != nil {
		redirect(w, r, "/", "", err.Error())
		return
	}
	settings.PublicURL = raw
	if err := s.store.SaveSettings(r.Context(), settings); err != nil {
		redirect(w, r, "/", "", err.Error())
		return
	}
	redirect(w, r, "/", s.translate(r, "settings.publicURLSaved"), "")
}

// definitionsRedirect folds the old top-level tool into Settings → Advanced.
func (s *Server) definitionsRedirect(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/settings?section=advanced#definitions", http.StatusFound)
}

func (s *Server) rulesRedirect(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/settings?section=rules", http.StatusFound)
}

func (s *Server) maintenanceRedirect(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/settings?section=maintenance", http.StatusFound)
}

// normalizeHTTPURL accepts a full http(s) URL or a bare host:port, which is
// what people type for a LAN service such as FlareSolverr. It returns the
// cleaned value and false when the input is not a usable absolute URL.
func normalizeHTTPURL(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", true
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	if strings.ContainsAny(raw, " \t\r\n") {
		return "", false
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return "", false
	}
	return strings.TrimRight(raw, "/"), true
}

// renderSettingsInvalid re-renders the section that failed, keeping the values
// the operator typed. A field key puts the same message next to the input.
func (s *Server) renderSettingsInvalid(w http.ResponseWriter, r *http.Request, settings store.Settings, section, field, message string) {
	page := s.settingsData(r, settings, section)
	page.Error = message
	if field != "" {
		page.FieldErrors[field] = message
	}
	s.render(w, r, "settings", page, http.StatusBadRequest)
}

func (s *Server) settingsSave(w http.ResponseWriter, r *http.Request) {
	current, err := s.store.LoadSettings(r.Context())
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	in := current
	rawSection := strings.ToLower(strings.TrimSpace(r.FormValue("section")))
	all := rawSection == ""
	section := settingsSectionID(rawSection)
	renderInvalid := func(message string) {
		s.renderSettingsInvalid(w, r, in, section, "", message)
	}
	renderInvalidField := func(field, message string) {
		s.renderSettingsInvalid(w, r, in, section, field, message)
	}
	applyGeneral := func() bool {
		rawPublic := strings.TrimSpace(r.FormValue("public_url"))
		in.PublicURL, _ = normalizeHTTPURL(rawPublic)
		if rawPublic != "" && in.PublicURL == "" {
			in.PublicURL = rawPublic
			renderInvalidField("public_url", s.translate(r, "settings.error.publicURL"))
			return false
		}
		rawWebhook := strings.TrimSpace(r.FormValue("notify_webhook_url"))
		in.NotifyWebhookURL, _ = normalizeHTTPURL(rawWebhook)
		if rawWebhook != "" && in.NotifyWebhookURL == "" {
			in.NotifyWebhookURL = rawWebhook
			renderInvalidField("notify_webhook_url", s.translate(r, "settings.error.webhookURL"))
			return false
		}
		if r.FormValue("notify_telegram_token_remove") != "" {
			in.NotifyTelegramToken = ""
		} else if token := strings.TrimSpace(r.FormValue("notify_telegram_token")); token != "" {
			in.NotifyTelegramToken = token
		}
		in.NotifyTelegramChatID = strings.TrimSpace(r.FormValue("notify_telegram_chat_id"))
		if in.NotifyTelegramToken != "" && in.NotifyTelegramChatID == "" {
			renderInvalid(s.translate(r, "settings.error.telegramPair"))
			return false
		}
		in.NotifyNewReleases = r.FormValue("notify_new_releases") != ""
		in.NotifyFailures = r.FormValue("notify_failures") != ""
		return true
	}
	applySecurity := func() bool {
		user := strings.TrimSpace(r.FormValue("ui_user"))
		password := r.FormValue("ui_password")
		confirm := r.FormValue("ui_password_confirm")
		switch {
		case password != "" && user == "":
			renderInvalid(s.translate(r, "settings.error.userRequired"))
			return false
		case password != "" && len(password) < 8:
			renderInvalid(s.translate(r, "settings.error.passwordShort"))
			return false
		case password != "" && password != confirm:
			renderInvalid(s.translate(r, "settings.error.passwordMismatch"))
			return false
		case password == "" && user != "" && in.UIPasswordHash == "":
			renderInvalid(s.translate(r, "settings.error.passwordRequired"))
			return false
		}
		if password != "" {
			hash, err := hashPassword(password)
			if err != nil {
				renderInvalid(err.Error())
				return false
			}
			in.UIPasswordHash = hash
		}
		if user == "" {
			in.UIPasswordHash = ""
		}
		in.UIUser = user
		return true
	}
	applyNetwork := func() bool {
		rawFlare := strings.TrimSpace(r.FormValue("flaresolverr_url"))
		in.FlareSolverrURL, _ = normalizeHTTPURL(rawFlare)
		if rawFlare != "" && in.FlareSolverrURL == "" {
			in.FlareSolverrURL = rawFlare
			renderInvalidField("flaresolverr_url", s.translate(r, "settings.error.flaresolverrURL"))
			return false
		}
		var parseErr error
		if in.FlareSolverrTimeoutSeconds, parseErr = s.formInt(r, "flaresolverr_timeout_seconds",
			current.FlareSolverrTimeoutSeconds, 5, 300); parseErr != nil {
			renderInvalid(parseErr.Error())
			return false
		}
		if in.HostRate, parseErr = s.formFloat(r, "host_rate", 1, 0, 100); parseErr != nil {
			renderInvalid(parseErr.Error())
			return false
		}
		if in.HostBurst, parseErr = s.formInt(r, "host_burst", 4, 1, 100); parseErr != nil {
			renderInvalid(parseErr.Error())
			return false
		}
		return true
	}
	applyUpdates := func() bool {
		var parseErr error
		if in.DefaultIntervalSeconds, parseErr = s.formInt(r, "default_interval_seconds", 3600, 60, 30*24*3600); parseErr != nil {
			renderInvalid(parseErr.Error())
			return false
		}
		if in.RetentionDays, parseErr = s.formInt(r, "retention_days", 0, 0, 3650); parseErr != nil {
			renderInvalid(parseErr.Error())
			return false
		}
		if in.HistoryRetentionDays, parseErr = s.formInt(r, "history_retention_days", current.HistoryRetentionDays, 0, 3650); parseErr != nil {
			renderInvalid(parseErr.Error())
			return false
		}
		return true
	}
	applyBackup := func() bool {
		var parseErr error
		in.BackupEnabled = r.FormValue("backup_enabled") != ""
		if in.BackupIntervalHours, parseErr = s.formInt(r, "backup_interval_hours", 24, 6, 168); parseErr != nil {
			renderInvalid(parseErr.Error())
			return false
		}
		if in.BackupKeep, parseErr = s.formInt(r, "backup_keep", 7, 1, 60); parseErr != nil {
			renderInvalid(parseErr.Error())
			return false
		}
		return true
	}
	applyAdvanced := func() bool {
		var parseErr error
		if in.FetchTimeoutSeconds, parseErr = s.formInt(r, "fetch_timeout_seconds", 45, 5, 300); parseErr != nil {
			renderInvalid(parseErr.Error())
			return false
		}
		if in.BreakerThreshold, parseErr = s.formInt(r, "breaker_threshold", 5, 1, 100); parseErr != nil {
			renderInvalid(parseErr.Error())
			return false
		}
		if in.BreakerCooldownSeconds, parseErr = s.formInt(r, "breaker_cooldown", 60, 1, 3600); parseErr != nil {
			renderInvalid(parseErr.Error())
			return false
		}
		if in.Workers, parseErr = s.formInt(r, "workers", 2, 1, 16); parseErr != nil {
			renderInvalid(parseErr.Error())
			return false
		}
		if in.MaxTorrentsPerRun, parseErr = s.formInt(r, "max_torrents_per_run", 30, 0, 200); parseErr != nil {
			renderInvalid(parseErr.Error())
			return false
		}
		maxMB, err := s.formInt(r, "max_torrent_mb", 8, 1, 64)
		if err != nil {
			renderInvalid(err.Error())
			return false
		}
		in.MaxTorrentBytes = int64(maxMB) << 20
		if in.TorznabDownloadFactor, parseErr = s.formInt(r, "torznab_download_factor", 0, 0, 100); parseErr != nil {
			renderInvalid(parseErr.Error())
			return false
		}
		if in.TorznabUploadFactor, parseErr = s.formInt(r, "torznab_upload_factor", 1, 0, 100); parseErr != nil {
			renderInvalid(parseErr.Error())
			return false
		}
		in.FetchTorrents = r.FormValue("fetch_torrents") != ""
		in.UserAgent = strings.TrimSpace(r.FormValue("user_agent"))
		if in.UserAgent == "" {
			in.UserAgent = store.DefaultSettings().UserAgent
		}
		if in.AutoDisableAfter, parseErr = s.formInt(r, "auto_disable_after", 0, 0, 100); parseErr != nil {
			renderInvalid(parseErr.Error())
			return false
		}
		if in.APIRateLimit, parseErr = s.formInt(r, "api_rate_limit_per_minute", 0, 0, 100000); parseErr != nil {
			renderInvalid(parseErr.Error())
			return false
		}
		in.TorznabLiveSearch = r.FormValue("torznab_live_search") != ""
		if in.TorznabLiveTimeout, parseErr = s.formInt(r, "torznab_live_timeout_seconds", 20, 5, 120); parseErr != nil {
			renderInvalid(parseErr.Error())
			return false
		}
		if in.TorznabLiveCache, parseErr = s.formInt(r, "torznab_live_cache_seconds", 300, 0, 3600); parseErr != nil {
			renderInvalid(parseErr.Error())
			return false
		}
		if in.TorznabLiveWait, parseErr = s.formInt(r, "torznab_live_wait_seconds", 5, 1, 120); parseErr != nil {
			renderInvalid(parseErr.Error())
			return false
		}
		return true
	}

	// An empty section is a form written before the section split: keep
	// accepting it, including the legacy single download client, so old
	// bookmarks, API clients and tests keep working.
	if all || section == "general" {
		if !applyGeneral() {
			return
		}
	}
	if all || section == "security" {
		if !applySecurity() {
			return
		}
	}
	if all || section == "network" {
		if !applyNetwork() {
			return
		}
	}
	if all || section == "updates" {
		if !applyUpdates() {
			return
		}
	}
	if all || section == "backup" {
		if !applyBackup() {
			return
		}
	}
	if all || section == "advanced" {
		if !applyAdvanced() {
			return
		}
	}
	if all {
		// Legacy fields of the old all-in-one Settings form.
		in.DownloadClient = strings.ToLower(strings.TrimSpace(r.FormValue("download_client")))
		switch in.DownloadClient {
		case "", "none":
			in.DownloadClient = "none"
		case "qbittorrent", "transmission":
		default:
			renderInvalid(s.translate(r, "settings.error.downloadClient"))
			return
		}
		in.DownloadClientURL = strings.TrimRight(strings.TrimSpace(r.FormValue("download_client_url")), "/")
		if in.DownloadClient != "none" {
			parsed, err := url.Parse(in.DownloadClientURL)
			if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
				renderInvalid(s.translate(r, "settings.error.downloadClientURL"))
				return
			}
		}
		in.DownloadClientUser = strings.TrimSpace(r.FormValue("download_client_user"))
		if r.FormValue("download_client_password_remove") != "" {
			in.DownloadClientPassword = ""
		} else if password := r.FormValue("download_client_password"); password != "" {
			in.DownloadClientPassword = password
		}
		in.DownloadClientCategory = strings.TrimSpace(r.FormValue("download_client_category"))
		in.DownloadClientSavePath = strings.TrimSpace(r.FormValue("download_client_save_path"))
		in.DownloadClientPaused = r.FormValue("download_client_paused") != ""
	}

	if err := s.store.SaveSettings(r.Context(), in); err != nil {
		renderInvalid(err.Error())
		return
	}
	if section == "network" && s.native != nil {
		// FlareSolverr URL and timeout are read when a connector is built.
		// Drop the cached connectors so a saved change applies immediately.
		s.native.InvalidateAll()
	}
	target := "/settings?section=" + section
	redirect(w, r, target, s.translate(r, "settings.saved"), "")
}

func (s *Server) settingsRegenerateKey(w http.ResponseWriter, r *http.Request) {
	key, err := store.NewAPIKey()
	if err != nil {
		redirect(w, r, "/settings", "", err.Error())
		return
	}
	if err := s.store.SetSetting(r.Context(), store.KeyAPIKey, key); err != nil {
		redirect(w, r, "/settings", "", err.Error())
		return
	}
	redirect(w, r, "/settings", s.translate(r, "settings.keyRegenerated"), "")
}

func (s *Server) settingsAPIKeyAdd(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.FormValue("name"))
	key, err := s.store.AddAPIKey(r.Context(), name)
	if err != nil {
		redirect(w, r, "/settings", "", err.Error())
		return
	}
	s.log.Info("additional API key created", "name", key.Name)
	redirect(w, r, "/settings", s.translate(r, "settings.keyCreated", key.Name), "")
}

func (s *Server) settingsAPIKeyDelete(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		redirect(w, r, "/settings", "", s.translate(r, "settings.error.invalidKeyID"))
		return
	}
	if _, err := s.store.DeleteAPIKey(r.Context(), id); err != nil {
		redirect(w, r, "/settings", "", err.Error())
		return
	}
	redirect(w, r, "/settings", s.translate(r, "settings.keyRevoked"), "")
}

func (s *Server) settingsTestFlareSolverr(w http.ResponseWriter, r *http.Request) {
	current, err := s.store.LoadSettings(r.Context())
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	in := current
	raw := strings.TrimSpace(r.FormValue("flaresolverr_url"))
	if raw == "" {
		in.FlareSolverrURL = ""
		s.renderSettingsInvalid(w, r, in, "network", "flaresolverr_url", s.translate(r, "settings.error.flaresolverrEmpty"))
		return
	}
	endpoint, ok := normalizeHTTPURL(raw)
	if !ok {
		in.FlareSolverrURL = raw
		s.renderSettingsInvalid(w, r, in, "network", "flaresolverr_url", s.translate(r, "settings.error.flaresolverrInvalid"))
		return
	}
	in.FlareSolverrURL = endpoint
	timeout := flaresolverr.DefaultTimeout
	if value, parseErr := strconv.Atoi(strings.TrimSpace(r.FormValue("flaresolverr_timeout_seconds"))); parseErr == nil && value >= 5 && value <= 300 {
		timeout = time.Duration(value) * time.Second
		in.FlareSolverrTimeoutSeconds = value
	}
	// The test shares the network form: keep the other fields the operator
	// typed instead of silently restoring the stored values after the redirect.
	if value, parseErr := s.formFloat(r, "host_rate", current.HostRate, 0, 100); parseErr == nil {
		in.HostRate = value
	}
	if value, parseErr := s.formInt(r, "host_burst", current.HostBurst, 1, 100); parseErr == nil {
		in.HostBurst = value
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), timeout+40*time.Second)
	defer cancel()
	client := &http.Client{Timeout: timeout + 40*time.Second}
	body, status, err := flaresolverr.Get(ctx, client, endpoint, "https://example.com/", timeout)
	if err != nil {
		s.log.Warn("FlareSolverr test failed", "component", "indexarr", "subsystem", "flaresolverr", "endpoint", endpoint, "error", err)
		s.renderSettingsInvalid(w, r, in, "network", "flaresolverr_url", s.translate(r, "settings.flaresolverrTestFailed", err.Error()))
		return
	}
	if status != http.StatusOK {
		s.log.Warn("FlareSolverr test returned an HTTP error", "component", "indexarr", "subsystem", "flaresolverr", "endpoint", endpoint, "status", status)
		s.renderSettingsInvalid(w, r, in, "network", "flaresolverr_url", s.translate(r, "settings.flaresolverrHTTP", status))
		return
	}
	// A successful test saves the address it just verified. The operator never
	// has to remember a second Save after an explicit Test click, and pressing
	// Enter in the field follows the normal Save path instead.
	if err := s.store.SaveSettings(r.Context(), in); err != nil {
		s.renderSettingsInvalid(w, r, in, "network", "flaresolverr_url", err.Error())
		return
	}
	if s.native != nil {
		s.native.InvalidateAll()
	}
	s.log.Info("FlareSolverr test ok", "endpoint", endpoint, "bytes", len(body))
	redirect(w, r, "/settings?section=network", s.translate(r, "settings.flaresolverrOK", len(body)), "")
}

func (s *Server) formInt(r *http.Request, key string, fallback, min, max int) (int, error) {
	raw := strings.TrimSpace(r.FormValue(key))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s", s.translate(r, "settings.error.number", key))
	}
	if value < min || value > max {
		return 0, fmt.Errorf("%s", s.translate(r, "settings.error.intRange", key, min, max))
	}
	return value, nil
}

func (s *Server) formFloat(r *http.Request, key string, fallback, min, max float64) (float64, error) {
	raw := strings.TrimSpace(r.FormValue(key))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, fmt.Errorf("%s", s.translate(r, "settings.error.number", key))
	}
	if value < min || value > max {
		return 0, fmt.Errorf("%s", s.translate(r, "settings.error.floatRange", key, min, max))
	}
	return value, nil
}

type runsPage struct {
	base
	Runs       []store.RunHistory
	Indexers   []store.Indexer
	IndexerID  int64
	OnlyErrors bool
}

// runsRedirect keeps /runs working and folds the old page into Attività.
func (s *Server) runsRedirect(w http.ResponseWriter, r *http.Request) {
	redirectTab(w, r, "/activity", "runs")
}

func (s *Server) runsData(r *http.Request) (runsPage, error) {
	indexerID := int64Param(r.URL.Query().Get("indexer"), 0)
	if indexerID == 0 {
		indexerID = int64Param(r.URL.Query().Get("source"), 0)
	}
	onlyErrors := r.URL.Query().Get("errors") == "1"
	runs, err := s.store.ListRunHistory(r.Context(), indexerID, onlyErrors, 200)
	if err != nil {
		return runsPage{}, err
	}
	indexers, _ := s.store.ListIndexers(r.Context())
	return runsPage{
		base:       s.newBase(r, "Run", "activity"),
		Runs:       runs,
		Indexers:   indexers,
		IndexerID:  indexerID,
		OnlyErrors: onlyErrors,
	}, nil
}

func (s *Server) runsClear(w http.ResponseWriter, r *http.Request) {
	removed, err := s.store.ClearRunHistory(r.Context(), time.Now().AddDate(0, 0, -30))
	if err != nil {
		redirect(w, r, "/runs", "", err.Error())
		return
	}
	redirect(w, r, "/runs", fmt.Sprintf("%d run(s) older than 30 days removed", removed), "")
}

type maintenancePage struct {
	base
	Stats               store.Stats
	Report              store.DBReport
	DBPath              string
	SQLiteVersion       string
	RetentionDays       int
	DefaultPurgeDays    int
	Backups             []backup.Info
	BackupDir           string
	BackupEnabled       bool
	BackupIntervalHours int
	BackupKeep          int
	Indexers            []store.Indexer
}

func (s *Server) maintenancePage(w http.ResponseWriter, r *http.Request) {
	stats, err := s.store.LoadStats(r.Context())
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	report, err := s.store.LoadDBReport(r.Context())
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	settings, _ := s.store.LoadSettings(r.Context())
	backups, _ := backup.List(s.store.Path())
	indexers, _ := s.store.ListIndexers(r.Context())
	purgeDays := settings.RetentionDays
	if purgeDays <= 0 {
		purgeDays = 30
	}
	version := ""
	_ = s.store.DB().QueryRowContext(r.Context(), "SELECT sqlite_version()").Scan(&version)
	s.render(w, r, "maintenance", maintenancePage{
		base:                s.newBase(r, "Manutenzione", "maintenance"),
		Stats:               stats,
		Report:              report,
		DBPath:              s.store.Path(),
		SQLiteVersion:       version,
		RetentionDays:       settings.RetentionDays,
		DefaultPurgeDays:    purgeDays,
		Backups:             backups,
		BackupDir:           backup.Dir(s.store.Path()),
		BackupEnabled:       settings.BackupEnabled,
		BackupIntervalHours: settings.BackupIntervalHours,
		BackupKeep:          settings.BackupKeep,
		Indexers:            indexers,
	}, http.StatusOK)
}

func (s *Server) maintenanceAction(w http.ResponseWriter, r *http.Request) {
	action := strings.TrimSpace(r.FormValue("action"))
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	switch action {
	case "vacuum":
		if err := s.store.Vacuum(ctx); err != nil {
			redirect(w, r, "/settings?section=maintenance", "", err.Error())
			return
		}
		redirect(w, r, "/settings?section=maintenance", "Database vacuumed", "")
	case "integrity":
		result, err := s.store.IntegrityCheck(ctx)
		if err != nil {
			redirect(w, r, "/settings?section=maintenance", "", err.Error())
			return
		}
		redirect(w, r, "/settings?section=maintenance", "Integrity check: "+result, "")
	case "quick_check":
		result, err := s.store.QuickCheck(ctx)
		if err != nil {
			redirect(w, r, "/settings?section=maintenance", "", err.Error())
			return
		}
		redirect(w, r, "/settings?section=maintenance", "Quick check: "+result, "")
	case "foreign_keys":
		violations, err := s.store.ForeignKeyCheck(ctx)
		if err != nil {
			redirect(w, r, "/settings?section=maintenance", "", err.Error())
			return
		}
		message := "Foreign key check: ok"
		if violations > 0 {
			message = fmt.Sprintf("Foreign key check: %d violation(s) found", violations)
		}
		redirect(w, r, "/settings?section=maintenance", message, "")
	case "optimize":
		if err := s.store.Optimize(ctx); err != nil {
			redirect(w, r, "/settings?section=maintenance", "", err.Error())
			return
		}
		redirect(w, r, "/settings?section=maintenance", "Planner statistics refreshed and WAL checkpointed", "")
	case "checkpoint":
		busy, logFrames, checkpointed, err := s.store.Checkpoint(ctx)
		if err != nil {
			redirect(w, r, "/settings?section=maintenance", "", err.Error())
			return
		}
		message := fmt.Sprintf("WAL checkpoint: %d frame(s) written and WAL truncated", checkpointed)
		if busy != 0 {
			message = fmt.Sprintf("WAL checkpoint incomplete: %d of %d frame(s) still pending",
				logFrames-checkpointed, logFrames)
		}
		redirect(w, r, "/settings?section=maintenance", message, "")
	case "analyze":
		if err := s.store.Analyze(ctx); err != nil {
			redirect(w, r, "/settings?section=maintenance", "", err.Error())
			return
		}
		redirect(w, r, "/settings?section=maintenance", "Planner statistics rebuilt (ANALYZE)", "")
	case "reindex":
		if err := s.store.Reindex(ctx); err != nil {
			redirect(w, r, "/settings?section=maintenance", "", err.Error())
			return
		}
		redirect(w, r, "/settings?section=maintenance", "Indexes rebuilt", "")
	case "reset_errors":
		removed, err := s.store.ResetIndexerErrors(ctx)
		if err != nil {
			redirect(w, r, "/settings?section=maintenance", "", err.Error())
			return
		}
		redirect(w, r, "/settings?section=maintenance", fmt.Sprintf("Cleared errors for %d indexer(s)", removed), "")
	case "prune":
		settings, err := s.store.LoadSettings(ctx)
		if err != nil {
			redirect(w, r, "/settings?section=maintenance", "", err.Error())
			return
		}
		if settings.RetentionDays <= 0 {
			redirect(w, r, "/settings?section=maintenance", "", "Set retention days in Settings first")
			return
		}
		before := time.Now().AddDate(0, 0, -settings.RetentionDays)
		removed, err := s.store.PruneReleases(ctx, before)
		if err != nil {
			redirect(w, r, "/settings?section=maintenance", "", err.Error())
			return
		}
		redirect(w, r, "/settings?section=maintenance", fmt.Sprintf("%d release(s) older than %d days removed", removed, settings.RetentionDays), "")
	case "clear_torrents":
		removed, err := s.store.ClearTorrents(ctx)
		if err != nil {
			redirect(w, r, "/settings?section=maintenance", "", err.Error())
			return
		}
		redirect(w, r, "/settings?section=maintenance", fmt.Sprintf("%d stored .torrent file(s) removed; magnets were kept", removed), "")
	case "dedupe":
		removed, err := s.store.DedupeByInfoHash(ctx)
		if err != nil {
			redirect(w, r, "/settings?section=maintenance", "", err.Error())
			return
		}
		redirect(w, r, "/settings?section=maintenance", fmt.Sprintf("%d duplicate release(s) merged", removed), "")
	case "clear_runs":
		removed, err := s.store.ClearRunHistory(ctx, time.Now().AddDate(0, 0, -30))
		if err != nil {
			redirect(w, r, "/settings?section=maintenance", "", err.Error())
			return
		}
		redirect(w, r, "/settings?section=maintenance", fmt.Sprintf("%d old run(s) removed", removed), "")
	case "purge_days":
		days, err := intInRange(r.FormValue("days"), 30, 36500)
		if err != nil {
			redirect(w, r, "/settings?section=maintenance", "", err.Error())
			return
		}
		removed, err := s.store.PruneReleases(ctx, time.Now().AddDate(0, 0, -days))
		if err != nil {
			redirect(w, r, "/settings?section=maintenance", "", err.Error())
			return
		}
		redirect(w, r, "/settings?section=maintenance",
			fmt.Sprintf("%d release(s) not seen in the last %d day(s) removed", removed, days), "")
	case "purge_indexer":
		indexerID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("indexer_id")), 10, 64)
		if err != nil || indexerID <= 0 {
			redirect(w, r, "/settings?section=maintenance", "", "Choose an indexer to purge")
			return
		}
		indexerRow, err := s.store.GetIndexer(ctx, indexerID)
		if err != nil {
			redirect(w, r, "/settings?section=maintenance", "", "Choose an indexer to purge")
			return
		}
		removed, err := s.store.DeleteReleasesByIndexer(ctx, indexerID)
		if err != nil {
			redirect(w, r, "/settings?section=maintenance", "", err.Error())
			return
		}
		redirect(w, r, "/settings?section=maintenance",
			fmt.Sprintf("%d release(s) from %s removed", removed, indexerRow.Name), "")
	case "purge_no_links":
		removed, err := s.store.DeleteReleasesWithoutLinks(ctx)
		if err != nil {
			redirect(w, r, "/settings?section=maintenance", "", err.Error())
			return
		}
		redirect(w, r, "/settings?section=maintenance",
			fmt.Sprintf("%d release(s) without magnet or .torrent removed", removed), "")
	case "purge_hidden":
		removed, err := s.store.DeleteHiddenReleases(ctx)
		if err != nil {
			redirect(w, r, "/settings?section=maintenance", "", err.Error())
			return
		}
		redirect(w, r, "/settings?section=maintenance", fmt.Sprintf("%d hidden release(s) removed", removed), "")
	case "clear_runs_days":
		days, err := intInRange(r.FormValue("days"), 30, 36500)
		if err != nil {
			redirect(w, r, "/settings?section=maintenance", "", err.Error())
			return
		}
		removed, err := s.store.ClearRunHistory(ctx, time.Now().AddDate(0, 0, -days))
		if err != nil {
			redirect(w, r, "/settings?section=maintenance", "", err.Error())
			return
		}
		redirect(w, r, "/settings?section=maintenance",
			fmt.Sprintf("%d run(s) older than %d day(s) removed", removed, days), "")
	case "reset_api_usage":
		removed, err := s.store.ResetAPIUsage(ctx)
		if err != nil {
			redirect(w, r, "/settings?section=maintenance", "", err.Error())
			return
		}
		redirect(w, r, "/settings?section=maintenance",
			fmt.Sprintf("API usage counters reset for %d key(s)", removed), "")
	case "delete_all":
		if r.FormValue("confirm") != "DELETE" {
			redirect(w, r, "/settings?section=maintenance", "", `Type DELETE in the confirmation field`)
			return
		}
		removed, err := s.store.DeleteAllReleases(ctx)
		if err != nil {
			redirect(w, r, "/settings?section=maintenance", "", err.Error())
			return
		}
		redirect(w, r, "/settings?section=maintenance", fmt.Sprintf("All %d release(s) deleted", removed), "")
	default:
		redirect(w, r, "/settings?section=maintenance", "", "Unknown maintenance action")
	}
}

func (s *Server) maintenanceBackup(w http.ResponseWriter, r *http.Request) {
	file, err := os.CreateTemp("", "indexarr-backup-*.db")
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	target := file.Name()
	_ = file.Close()
	_ = os.Remove(target)
	defer os.Remove(target)
	if err := s.store.BackupTo(r.Context(), target); err != nil {
		s.renderError(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	name := "indexarr-backup-" + time.Now().Format("20060102-150405") + ".db"
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Cache-Control", "no-store")
	http.ServeFile(w, r, filepath.Clean(target))
}

// intInRange parses an optional positive integer form field. An empty value
// uses fallback; anything outside 1..max is rejected.
func intInRange(raw string, fallback, max int) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 || value > max {
		return 0, fmt.Errorf("enter a number between 1 and %d", max)
	}
	return value, nil
}

// settingsSecret returns a secret only after an explicit same-origin fetch.
// The value is never part of the server-rendered settings HTML.
func (s *Server) settingsSecret(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if strings.TrimSpace(r.Header.Get("X-Requested-With")) != "fetch" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	settings, err := s.store.LoadSettings(r.Context())
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	name := strings.TrimSpace(r.PathValue("name"))
	idValue := strings.TrimSpace(r.PathValue("id"))
	var value string
	switch {
	case name == "apikey":
		value = settings.APIKey
	case name == "torznab":
		if settings.APIKey == "" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		value = torznabapi.BaseURL(r, settings) + "/api?apikey=" + url.QueryEscape(settings.APIKey)
	case name == "key" || idValue != "":
		id, parseErr := strconv.ParseInt(idValue, 10, 64)
		if parseErr != nil || id <= 0 {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		keys, listErr := s.store.ListAPIKeys(r.Context())
		if listErr != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		for _, key := range keys {
			if key.ID == id {
				value = key.Key
				break
			}
		}
	default:
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if value == "" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_ = json.NewEncoder(w).Encode(struct {
		Value string `json:"value"`
	}{Value: value})
}
