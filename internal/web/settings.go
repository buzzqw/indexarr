package web

import (
	"context"
	"encoding/json"
	"fmt"
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
)

type settingsAPIKey struct {
	ID         int64
	Name       string
	Masked     string
	CreatedAt  time.Time
	LastUsedAt time.Time
	Requests   int64
}

type settingsPage struct {
	base
	Settings       store.Settings
	MaxTorrentMB   int64
	TorznabBase    string
	TorznabMasked  string
	AdditionalKeys []settingsAPIKey
	BackupDir      string
}

// settingsData builds the page model without the API key: the key is only
// available through /settings/secrets/* after an explicit fetch. The masked
// URL contains no secret either.
func (s *Server) settingsData(r *http.Request, settings store.Settings) settingsPage {
	keys, _ := s.store.ListAPIKeys(r.Context())
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
	return settingsPage{
		base:           s.newBaseT(r, "shell.settings", "settings"),
		Settings:       public,
		MaxTorrentMB:   settings.MaxTorrentBytes / (1 << 20),
		TorznabBase:    baseURL,
		TorznabMasked:  baseURL + "?apikey=••••••",
		AdditionalKeys: views,
		BackupDir:      backup.Dir(s.store.Path()),
	}
}

func (s *Server) settingsPage(w http.ResponseWriter, r *http.Request) {
	settings, err := s.store.LoadSettings(r.Context())
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	s.render(w, r, "settings", s.settingsData(r, settings), http.StatusOK)
}

func (s *Server) settingsSave(w http.ResponseWriter, r *http.Request) {
	current, err := s.store.LoadSettings(r.Context())
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	in := current
	var parseErr error
	renderInvalid := func(message string) {
		page := s.settingsData(r, in)
		page.Error = message
		s.render(w, r, "settings", page, http.StatusBadRequest)
	}

	in.PublicURL = strings.TrimRight(strings.TrimSpace(r.FormValue("public_url")), "/")
	if in.PublicURL != "" {
		parsed, err := url.Parse(in.PublicURL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			renderInvalid(s.translate(r, "settings.error.publicURL"))
			return
		}
	}
	in.FlareSolverrURL = strings.TrimRight(strings.TrimSpace(r.FormValue("flaresolverr_url")), "/")
	if in.FlareSolverrURL != "" {
		parsed, err := url.Parse(in.FlareSolverrURL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			renderInvalid(s.translate(r, "settings.error.flaresolverrURL"))
			return
		}
	}
	if in.DefaultIntervalSeconds, parseErr = s.formInt(r, "default_interval_seconds", 3600, 60, 30*24*3600); parseErr != nil {
		renderInvalid(parseErr.Error())
		return
	}
	if in.RetentionDays, parseErr = s.formInt(r, "retention_days", 0, 0, 3650); parseErr != nil {
		renderInvalid(parseErr.Error())
		return
	}
	if in.FetchTimeoutSeconds, parseErr = s.formInt(r, "fetch_timeout_seconds", 45, 5, 300); parseErr != nil {
		renderInvalid(parseErr.Error())
		return
	}
	if in.HostRate, parseErr = s.formFloat(r, "host_rate", 1, 0, 100); parseErr != nil {
		renderInvalid(parseErr.Error())
		return
	}
	if in.HostBurst, parseErr = s.formInt(r, "host_burst", 4, 1, 100); parseErr != nil {
		renderInvalid(parseErr.Error())
		return
	}
	if in.BreakerThreshold, parseErr = s.formInt(r, "breaker_threshold", 5, 1, 100); parseErr != nil {
		renderInvalid(parseErr.Error())
		return
	}
	if in.BreakerCooldownSeconds, parseErr = s.formInt(r, "breaker_cooldown", 60, 1, 3600); parseErr != nil {
		renderInvalid(parseErr.Error())
		return
	}
	if in.Workers, parseErr = s.formInt(r, "workers", 2, 1, 16); parseErr != nil {
		renderInvalid(parseErr.Error())
		return
	}
	if in.MaxTorrentsPerRun, parseErr = s.formInt(r, "max_torrents_per_run", 30, 0, 200); parseErr != nil {
		renderInvalid(parseErr.Error())
		return
	}
	maxMB, err := s.formInt(r, "max_torrent_mb", 8, 1, 64)
	if err != nil {
		renderInvalid(err.Error())
		return
	}
	in.MaxTorrentBytes = int64(maxMB) << 20
	if in.TorznabDownloadFactor, parseErr = s.formInt(r, "torznab_download_factor", 0, 0, 100); parseErr != nil {
		renderInvalid(parseErr.Error())
		return
	}
	if in.TorznabUploadFactor, parseErr = s.formInt(r, "torznab_upload_factor", 1, 0, 100); parseErr != nil {
		renderInvalid(parseErr.Error())
		return
	}
	in.FetchTorrents = r.FormValue("fetch_torrents") != ""
	in.UserAgent = strings.TrimSpace(r.FormValue("user_agent"))
	if in.UserAgent == "" {
		in.UserAgent = store.DefaultSettings().UserAgent
	}
	in.NotifyWebhookURL = strings.TrimRight(strings.TrimSpace(r.FormValue("notify_webhook_url")), "/")
	if in.NotifyWebhookURL != "" {
		parsed, err := url.Parse(in.NotifyWebhookURL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			renderInvalid(s.translate(r, "settings.error.webhookURL"))
			return
		}
	}
	if token := strings.TrimSpace(r.FormValue("notify_telegram_token")); token != "" {
		in.NotifyTelegramToken = token
	}
	in.NotifyTelegramChatID = strings.TrimSpace(r.FormValue("notify_telegram_chat_id"))
	if (in.NotifyTelegramToken == "") != (in.NotifyTelegramChatID == "") {
		renderInvalid(s.translate(r, "settings.error.telegramPair"))
		return
	}
	in.NotifyNewReleases = r.FormValue("notify_new_releases") != ""
	in.NotifyFailures = r.FormValue("notify_failures") != ""
	if in.AutoDisableAfter, parseErr = s.formInt(r, "auto_disable_after", 0, 0, 100); parseErr != nil {
		renderInvalid(parseErr.Error())
		return
	}
	if in.APIRateLimit, parseErr = s.formInt(r, "api_rate_limit_per_minute", 0, 0, 100000); parseErr != nil {
		renderInvalid(parseErr.Error())
		return
	}
	in.BackupEnabled = r.FormValue("backup_enabled") != ""
	if in.BackupIntervalHours, parseErr = s.formInt(r, "backup_interval_hours", 24, 6, 168); parseErr != nil {
		renderInvalid(parseErr.Error())
		return
	}
	if in.BackupKeep, parseErr = s.formInt(r, "backup_keep", 7, 1, 60); parseErr != nil {
		renderInvalid(parseErr.Error())
		return
	}
	in.TorznabLiveSearch = r.FormValue("torznab_live_search") != ""
	if in.TorznabLiveTimeout, parseErr = s.formInt(r, "torznab_live_timeout_seconds", 20, 5, 120); parseErr != nil {
		renderInvalid(parseErr.Error())
		return
	}
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
	if password := r.FormValue("download_client_password"); password != "" {
		in.DownloadClientPassword = password
	}
	in.DownloadClientCategory = strings.TrimSpace(r.FormValue("download_client_category"))
	in.DownloadClientSavePath = strings.TrimSpace(r.FormValue("download_client_save_path"))
	in.DownloadClientPaused = r.FormValue("download_client_paused") != ""

	user := strings.TrimSpace(r.FormValue("ui_user"))
	password := r.FormValue("ui_password")
	confirm := r.FormValue("ui_password_confirm")
	switch {
	case password != "" && user == "":
		renderInvalid(s.translate(r, "settings.error.userRequired"))
		return
	case password != "" && len(password) < 8:
		renderInvalid(s.translate(r, "settings.error.passwordShort"))
		return
	case password != "" && password != confirm:
		renderInvalid(s.translate(r, "settings.error.passwordMismatch"))
		return
	case password == "" && user != "" && in.UIPasswordHash == "":
		renderInvalid(s.translate(r, "settings.error.passwordRequired"))
		return
	}
	if password != "" {
		hash, err := hashPassword(password)
		if err != nil {
			renderInvalid(err.Error())
			return
		}
		in.UIPasswordHash = hash
	}
	if user == "" {
		in.UIPasswordHash = ""
	}
	in.UIUser = user

	if err := s.store.SaveSettings(r.Context(), in); err != nil {
		renderInvalid(err.Error())
		return
	}
	redirect(w, r, "/settings", s.translate(r, "settings.saved"), "")
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
	endpoint := strings.TrimRight(strings.TrimSpace(r.FormValue("flaresolverr_url")), "/")
	if endpoint == "" {
		redirect(w, r, "/settings", "", s.translate(r, "settings.error.flaresolverrEmpty"))
		return
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		redirect(w, r, "/settings", "", s.translate(r, "settings.error.flaresolverrInvalid"))
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 100*time.Second)
	defer cancel()
	client := &http.Client{Timeout: 100 * time.Second}
	body, status, err := flaresolverr.Get(ctx, client, endpoint, "https://example.com/", flaresolverr.DefaultTimeout)
	if err != nil {
		s.log.Warn("FlareSolverr test failed", "component", "indexarr", "subsystem", "flaresolverr", "endpoint", endpoint, "error", err)
		redirect(w, r, "/settings", "", s.translate(r, "settings.flaresolverrTestFailed", err.Error()))
		return
	}
	if status != http.StatusOK {
		s.log.Warn("FlareSolverr test returned an HTTP error", "component", "indexarr", "subsystem", "flaresolverr", "endpoint", endpoint, "status", status)
		redirect(w, r, "/settings", "", s.translate(r, "settings.flaresolverrHTTP", status))
		return
	}
	s.log.Info("FlareSolverr test ok", "endpoint", endpoint, "bytes", len(body))
	redirect(w, r, "/settings", s.translate(r, "settings.flaresolverrOK", len(body)), "")
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
	Runs       []store.Run
	Sources    []store.Source
	SourceID   int64
	OnlyErrors bool
}

// runsRedirect keeps /runs working and folds the old page into Attività.
func (s *Server) runsRedirect(w http.ResponseWriter, r *http.Request) {
	redirectTab(w, r, "/activity", "runs")
}

func (s *Server) runsData(r *http.Request) (runsPage, error) {
	sourceID := int64Param(r.URL.Query().Get("source"), 0)
	onlyErrors := r.URL.Query().Get("errors") == "1"
	runs, err := s.store.ListRuns(r.Context(), sourceID, onlyErrors, 200)
	if err != nil {
		return runsPage{}, err
	}
	sources, _ := s.store.ListSources(r.Context())
	return runsPage{
		base:       s.newBase(r, "Run", "activity"),
		Runs:       runs,
		Sources:    sources,
		SourceID:   sourceID,
		OnlyErrors: onlyErrors,
	}, nil
}

func (s *Server) runsClear(w http.ResponseWriter, r *http.Request) {
	removed, err := s.store.ClearRuns(r.Context(), time.Now().AddDate(0, 0, -30))
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
	FailedSources       []store.Source
	Sources             []store.Source
	SourceErrors        int
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
	sources, _ := s.store.ListSources(r.Context())
	purgeDays := settings.RetentionDays
	if purgeDays <= 0 {
		purgeDays = 30
	}
	var failed []store.Source
	sourceErrors := 0
	for _, source := range sources {
		if source.LastError != "" {
			sourceErrors++
			if len(failed) < 20 {
				failed = append(failed, source)
			}
		}
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
		FailedSources:       failed,
		Sources:             sources,
		SourceErrors:        sourceErrors,
	}, http.StatusOK)
}

func (s *Server) maintenanceAction(w http.ResponseWriter, r *http.Request) {
	action := strings.TrimSpace(r.FormValue("action"))
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	switch action {
	case "vacuum":
		if err := s.store.Vacuum(ctx); err != nil {
			redirect(w, r, "/maintenance", "", err.Error())
			return
		}
		redirect(w, r, "/maintenance", "Database vacuumed", "")
	case "integrity":
		result, err := s.store.IntegrityCheck(ctx)
		if err != nil {
			redirect(w, r, "/maintenance", "", err.Error())
			return
		}
		redirect(w, r, "/maintenance", "Integrity check: "+result, "")
	case "quick_check":
		result, err := s.store.QuickCheck(ctx)
		if err != nil {
			redirect(w, r, "/maintenance", "", err.Error())
			return
		}
		redirect(w, r, "/maintenance", "Quick check: "+result, "")
	case "foreign_keys":
		violations, err := s.store.ForeignKeyCheck(ctx)
		if err != nil {
			redirect(w, r, "/maintenance", "", err.Error())
			return
		}
		message := "Foreign key check: ok"
		if violations > 0 {
			message = fmt.Sprintf("Foreign key check: %d violation(s) found", violations)
		}
		redirect(w, r, "/maintenance", message, "")
	case "optimize":
		if err := s.store.Optimize(ctx); err != nil {
			redirect(w, r, "/maintenance", "", err.Error())
			return
		}
		redirect(w, r, "/maintenance", "Planner statistics refreshed and WAL checkpointed", "")
	case "checkpoint":
		busy, logFrames, checkpointed, err := s.store.Checkpoint(ctx)
		if err != nil {
			redirect(w, r, "/maintenance", "", err.Error())
			return
		}
		message := fmt.Sprintf("WAL checkpoint: %d frame(s) written and WAL truncated", checkpointed)
		if busy != 0 {
			message = fmt.Sprintf("WAL checkpoint incomplete: %d of %d frame(s) still pending",
				logFrames-checkpointed, logFrames)
		}
		redirect(w, r, "/maintenance", message, "")
	case "analyze":
		if err := s.store.Analyze(ctx); err != nil {
			redirect(w, r, "/maintenance", "", err.Error())
			return
		}
		redirect(w, r, "/maintenance", "Planner statistics rebuilt (ANALYZE)", "")
	case "reindex":
		if err := s.store.Reindex(ctx); err != nil {
			redirect(w, r, "/maintenance", "", err.Error())
			return
		}
		redirect(w, r, "/maintenance", "Indexes rebuilt", "")
	case "reset_errors":
		removed, err := s.store.ResetSourceErrors(ctx)
		if err != nil {
			redirect(w, r, "/maintenance", "", err.Error())
			return
		}
		redirect(w, r, "/maintenance", fmt.Sprintf("Cleared errors for %d source(s)", removed), "")
	case "prune":
		settings, err := s.store.LoadSettings(ctx)
		if err != nil {
			redirect(w, r, "/maintenance", "", err.Error())
			return
		}
		if settings.RetentionDays <= 0 {
			redirect(w, r, "/maintenance", "", "Set retention days in Settings first")
			return
		}
		before := time.Now().AddDate(0, 0, -settings.RetentionDays)
		removed, err := s.store.PruneReleases(ctx, before)
		if err != nil {
			redirect(w, r, "/maintenance", "", err.Error())
			return
		}
		redirect(w, r, "/maintenance", fmt.Sprintf("%d release(s) older than %d days removed", removed, settings.RetentionDays), "")
	case "clear_torrents":
		removed, err := s.store.ClearTorrents(ctx)
		if err != nil {
			redirect(w, r, "/maintenance", "", err.Error())
			return
		}
		redirect(w, r, "/maintenance", fmt.Sprintf("%d stored .torrent file(s) removed; magnets were kept", removed), "")
	case "dedupe":
		removed, err := s.store.DedupeByInfoHash(ctx)
		if err != nil {
			redirect(w, r, "/maintenance", "", err.Error())
			return
		}
		redirect(w, r, "/maintenance", fmt.Sprintf("%d duplicate release(s) merged", removed), "")
	case "clear_runs":
		removed, err := s.store.ClearRuns(ctx, time.Now().AddDate(0, 0, -30))
		if err != nil {
			redirect(w, r, "/maintenance", "", err.Error())
			return
		}
		redirect(w, r, "/maintenance", fmt.Sprintf("%d old run(s) removed", removed), "")
	case "purge_days":
		days, err := intInRange(r.FormValue("days"), 30, 36500)
		if err != nil {
			redirect(w, r, "/maintenance", "", err.Error())
			return
		}
		removed, err := s.store.PruneReleases(ctx, time.Now().AddDate(0, 0, -days))
		if err != nil {
			redirect(w, r, "/maintenance", "", err.Error())
			return
		}
		redirect(w, r, "/maintenance",
			fmt.Sprintf("%d release(s) not seen in the last %d day(s) removed", removed, days), "")
	case "purge_source":
		sourceID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("source_id")), 10, 64)
		if err != nil || sourceID <= 0 {
			redirect(w, r, "/maintenance", "", "Choose a source to purge")
			return
		}
		source, err := s.store.GetSource(ctx, sourceID)
		if err != nil {
			redirect(w, r, "/maintenance", "", "Choose a source to purge")
			return
		}
		removed, err := s.store.DeleteReleasesBySource(ctx, sourceID)
		if err != nil {
			redirect(w, r, "/maintenance", "", err.Error())
			return
		}
		redirect(w, r, "/maintenance",
			fmt.Sprintf("%d release(s) from %s removed", removed, source.Name), "")
	case "purge_no_links":
		removed, err := s.store.DeleteReleasesWithoutLinks(ctx)
		if err != nil {
			redirect(w, r, "/maintenance", "", err.Error())
			return
		}
		redirect(w, r, "/maintenance",
			fmt.Sprintf("%d release(s) without magnet or .torrent removed", removed), "")
	case "purge_hidden":
		removed, err := s.store.DeleteHiddenReleases(ctx)
		if err != nil {
			redirect(w, r, "/maintenance", "", err.Error())
			return
		}
		redirect(w, r, "/maintenance", fmt.Sprintf("%d hidden release(s) removed", removed), "")
	case "clear_runs_days":
		days, err := intInRange(r.FormValue("days"), 30, 36500)
		if err != nil {
			redirect(w, r, "/maintenance", "", err.Error())
			return
		}
		removed, err := s.store.ClearRuns(ctx, time.Now().AddDate(0, 0, -days))
		if err != nil {
			redirect(w, r, "/maintenance", "", err.Error())
			return
		}
		redirect(w, r, "/maintenance",
			fmt.Sprintf("%d run(s) older than %d day(s) removed", removed, days), "")
	case "reset_api_usage":
		removed, err := s.store.ResetAPIUsage(ctx)
		if err != nil {
			redirect(w, r, "/maintenance", "", err.Error())
			return
		}
		redirect(w, r, "/maintenance",
			fmt.Sprintf("API usage counters reset for %d key(s)", removed), "")
	case "recount_sources":
		updated, err := s.store.RecalculateSourceCounts(ctx)
		if err != nil {
			redirect(w, r, "/maintenance", "", err.Error())
			return
		}
		redirect(w, r, "/maintenance",
			fmt.Sprintf("Cached item counters recalculated for %d source(s)", updated), "")
	case "delete_all":
		if r.FormValue("confirm") != "DELETE" {
			redirect(w, r, "/maintenance", "", `Type DELETE in the confirmation field`)
			return
		}
		removed, err := s.store.DeleteAllReleases(ctx)
		if err != nil {
			redirect(w, r, "/maintenance", "", err.Error())
			return
		}
		redirect(w, r, "/maintenance", fmt.Sprintf("All %d release(s) deleted", removed), "")
	default:
		redirect(w, r, "/maintenance", "", "Unknown maintenance action")
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
