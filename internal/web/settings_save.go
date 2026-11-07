package web

import (
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/buzzqw/indexarr/internal/i18n"
	"github.com/buzzqw/indexarr/internal/store"
)

// settingsProblem is a refused value of the Settings form: the message shown
// at the top of the page and, when it concerns one input, the field it is
// shown next to.
type settingsProblem struct {
	field   string
	message string
}

func (p *settingsProblem) Error() string { return p.message }

func formProblem(message string) error { return &settingsProblem{message: message} }

func fieldProblem(field, message string) error {
	return &settingsProblem{field: field, message: message}
}

// settingsSaveStep is one panel of the Settings page and the function that
// reads its fields into the settings being saved. current holds the stored
// values, used as the fallback of a field the form leaves empty.
type settingsSaveStep struct {
	id    string
	apply func(s *Server, r *http.Request, in *store.Settings, current store.Settings) error
}

var settingsSaveSteps = []settingsSaveStep{
	{"general", (*Server).applyGeneralSettings},
	{"notifications", (*Server).applyNotificationSettings},
	{"security", (*Server).applySecuritySettings},
	{"network", (*Server).applyNetworkSettings},
	{"updates", (*Server).applyUpdateSettings},
	{"backup", (*Server).applyBackupSettings},
	{"maintenance", (*Server).applyMaintenanceSettings},
	{"advanced", (*Server).applyAdvancedSettings},
}

func (s *Server) settingsSave(w http.ResponseWriter, r *http.Request) {
	current, err := s.store.LoadSettings(r.Context())
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	in := current
	rawSection := strings.ToLower(strings.TrimSpace(r.FormValue("section")))
	// An empty section is a form written before the section split: keep
	// accepting it, including the legacy single download client, so old
	// bookmarks, API clients and tests keep working.
	all := rawSection == ""
	section := settingsSectionID(rawSection)

	refuse := func(err error) {
		problem := &settingsProblem{message: err.Error()}
		if typed, ok := err.(*settingsProblem); ok {
			problem = typed
		}
		s.renderSettingsInvalid(w, r, in, section, problem.field, problem.message)
	}
	for _, step := range settingsSaveSteps {
		if !all && step.id != section {
			continue
		}
		if err := step.apply(s, r, &in, current); err != nil {
			refuse(err)
			return
		}
	}
	if all {
		if err := s.applyLegacyDownloadClient(r, &in); err != nil {
			refuse(err)
			return
		}
	}

	if err := s.store.SaveSettings(r.Context(), in); err != nil {
		refuse(err)
		return
	}
	s.invalidateTrustedHosts()
	if section == "network" && s.native != nil {
		// FlareSolverr URL and timeout are read when a connector is built.
		// Drop the cached connectors so a saved change applies immediately.
		s.native.InvalidateAll()
	}
	redirect(w, r, "/settings?section="+section, s.translate(r, "settings.saved"), "")
}

// intSetting is one whole-number field: the form key, where the value goes,
// the value of an empty field and the accepted range.
type intSetting struct {
	key           string
	target        *int
	fallback      int
	minimum, most int
}

// readIntSettings reads the fields in order and stops at the first refused
// one. A refused field is left at zero, as the single reads always did.
func (s *Server) readIntSettings(r *http.Request, fields []intSetting) error {
	for _, field := range fields {
		value, err := s.formInt(r, field.key, field.fallback, field.minimum, field.most)
		*field.target = value
		if err != nil {
			return formProblem(err.Error())
		}
	}
	return nil
}

// readURL reads an optional http(s) address. A value that is not one is kept
// in the form, so the operator can correct it, and refused next to its field.
func (s *Server) readURL(r *http.Request, field, errorKey string, target *string) error {
	raw := strings.TrimSpace(r.FormValue(field))
	*target, _ = normalizeHTTPURL(raw)
	if raw != "" && *target == "" {
		*target = raw
		return fieldProblem(field, s.translate(r, errorKey))
	}
	return nil
}

func (s *Server) applyGeneralSettings(r *http.Request, in *store.Settings, _ store.Settings) error {
	return s.readURL(r, "public_url", "settings.error.publicURL", &in.PublicURL)
}

func (s *Server) applyNotificationSettings(r *http.Request, in *store.Settings, current store.Settings) error {
	if err := s.readNotificationChannels(r, in); err != nil {
		return err
	}
	if err := s.checkNotificationPairs(r, in); err != nil {
		return err
	}

	in.NotifyNewReleases = r.FormValue("notify_new_releases") != ""
	in.NotifyFailures = r.FormValue("notify_failures") != ""
	in.NotifyGrabFailed = r.FormValue("notify_grab_failed") != ""
	in.NotifyGrabSuccess = r.FormValue("notify_grab_success") != ""
	in.NotifyCatalogUpdated = r.FormValue("notify_catalog_updated") != ""
	in.NotifyBackupFailed = r.FormValue("notify_backup_failed") != ""
	// An unknown code falls back to "same as the interface" (the stored
	// values are validated again when the settings are read).
	in.NotifyLanguage = ""
	if lang, ok := i18n.Lookup(strings.ToLower(strings.TrimSpace(r.FormValue("notify_language")))); ok {
		in.NotifyLanguage = string(lang)
	}
	return s.readIntSettings(r, []intSetting{
		{"notify_cooldown_minutes", &in.NotifyCooldownMinutes, current.NotifyCooldownMinutes, 1, 7 * 24 * 60},
	})
}

// readNotificationChannels reads the address and the credentials of every
// channel.
//
// Visible channel addresses are saved as they are; an empty one clears the
// channel. Secret addresses and tokens are never shown back: an empty field
// keeps the stored value and the matching "remove" box clears it. A form
// written before a field existed keeps the stored value instead of clearing
// it.
func (s *Server) readNotificationChannels(r *http.Request, in *store.Settings) error {
	has := func(field string) bool { return r.Form.Has(field) }
	setSecret := func(removeField, inputField string, target *string) {
		if r.FormValue(removeField) != "" {
			*target = ""
			return
		}
		if value := strings.TrimSpace(r.FormValue(inputField)); value != "" {
			*target = value
		}
	}
	setSecretURL := func(removeField, inputField string, target *string) error {
		if r.FormValue(removeField) != "" {
			*target = ""
			return nil
		}
		raw := strings.TrimSpace(r.FormValue(inputField))
		if raw == "" {
			return nil
		}
		normalized, ok := normalizeHTTPURL(raw)
		if !ok {
			*target = raw
			return fieldProblem(inputField, s.translate(r, "settings.error.channelURL"))
		}
		*target = normalized
		return nil
	}
	setVisibleURL := func(field string, target *string) error {
		if !has(field) {
			return nil
		}
		return s.readURL(r, field, "settings.error.channelURL", target)
	}
	setText := func(field string, target *string) {
		if has(field) {
			*target = strings.TrimSpace(r.FormValue(field))
		}
	}

	rawWebhook := strings.TrimSpace(r.FormValue("notify_webhook_url"))
	switch {
	case r.FormValue("notify_webhook_remove") != "":
		in.NotifyWebhookURL = ""
	case rawWebhook != "":
		normalized, ok := normalizeHTTPURL(rawWebhook)
		if !ok {
			in.NotifyWebhookURL = rawWebhook
			return fieldProblem("notify_webhook_url", s.translate(r, "settings.error.webhookURL"))
		}
		in.NotifyWebhookURL = normalized
	}
	setSecret("notify_telegram_token_remove", "notify_telegram_token", &in.NotifyTelegramToken)
	in.NotifyTelegramChatID = strings.TrimSpace(r.FormValue("notify_telegram_chat_id"))
	if in.NotifyTelegramToken != "" && in.NotifyTelegramChatID == "" {
		return formProblem(s.translate(r, "settings.error.telegramPair"))
	}

	if err := setVisibleURL("notify_apprise_url", &in.NotifyAppriseURL); err != nil {
		return err
	}
	setSecret("notify_apprise_key_remove", "notify_apprise_key", &in.NotifyAppriseKey)
	if err := setSecretURL("notify_discord_url_remove", "notify_discord_url", &in.NotifyDiscordURL); err != nil {
		return err
	}
	if err := setSecretURL("notify_slack_url_remove", "notify_slack_url", &in.NotifySlackURL); err != nil {
		return err
	}
	if err := setVisibleURL("notify_gotify_url", &in.NotifyGotifyURL); err != nil {
		return err
	}
	setSecret("notify_gotify_token_remove", "notify_gotify_token", &in.NotifyGotifyToken)
	if err := setVisibleURL("notify_ntfy_url", &in.NotifyNtfyURL); err != nil {
		return err
	}
	if has("notify_ntfy_topic") {
		in.NotifyNtfyTopic = strings.Trim(strings.TrimSpace(r.FormValue("notify_ntfy_topic")), "/")
	}
	setSecret("notify_ntfy_token_remove", "notify_ntfy_token", &in.NotifyNtfyToken)
	setSecret("notify_pushover_token_remove", "notify_pushover_token", &in.NotifyPushoverToken)
	setSecret("notify_pushover_user_remove", "notify_pushover_user", &in.NotifyPushoverUser)
	if has("notify_smtp_host") {
		in.NotifySMTPHost = strings.TrimSpace(r.FormValue("notify_smtp_host"))
		// The checkbox is absent when unchecked: a form that carries the
		// SMTP host is the new form, so absence means false.
		in.NotifySMTPStartTLS = r.FormValue("notify_smtp_starttls") != ""
	}
	setText("notify_smtp_username", &in.NotifySMTPUsername)
	setSecret("notify_smtp_password_remove", "notify_smtp_password", &in.NotifySMTPPassword)
	setText("notify_smtp_from", &in.NotifySMTPFrom)
	setText("notify_smtp_to", &in.NotifySMTPTo)
	setText("notify_script_path", &in.NotifyScriptPath)
	setText("notify_script_args", &in.NotifyScriptArgs)
	return nil
}

// checkNotificationPairs refuses a channel configured by half: a key without
// its address, a token without its user, an e-mail without a recipient.
func (s *Server) checkNotificationPairs(r *http.Request, in *store.Settings) error {
	if in.NotifyAppriseKey != "" && in.NotifyAppriseURL == "" {
		return formProblem(s.translate(r, "settings.error.apprisePair"))
	}
	if (in.NotifyGotifyURL == "") != (in.NotifyGotifyToken == "") {
		return formProblem(s.translate(r, "settings.error.gotifyPair"))
	}
	if (in.NotifyNtfyURL == "") != (in.NotifyNtfyTopic == "") {
		return formProblem(s.translate(r, "settings.error.ntfyPair"))
	}
	if (in.NotifyPushoverToken == "") != (in.NotifyPushoverUser == "") {
		return formProblem(s.translate(r, "settings.error.pushoverPair"))
	}
	if in.NotifySMTPHost != "" || in.NotifySMTPFrom != "" || in.NotifySMTPTo != "" ||
		in.NotifySMTPUsername != "" || in.NotifySMTPPassword != "" {
		if in.NotifySMTPHost == "" || in.NotifySMTPFrom == "" || in.NotifySMTPTo == "" {
			return formProblem(s.translate(r, "settings.error.smtpIncomplete"))
		}
		if _, _, err := net.SplitHostPort(in.NotifySMTPHost); err != nil {
			return fieldProblem("notify_smtp_host", s.translate(r, "settings.error.smtpHost"))
		}
		if err := validEmailList(in.NotifySMTPFrom); err != nil {
			return fieldProblem("notify_smtp_from", s.translate(r, "settings.error.smtpAddress"))
		}
		if err := validEmailList(in.NotifySMTPTo); err != nil {
			return fieldProblem("notify_smtp_to", s.translate(r, "settings.error.smtpAddress"))
		}
		if (in.NotifySMTPUsername == "") != (in.NotifySMTPPassword == "") {
			return formProblem(s.translate(r, "settings.error.smtpCredentials"))
		}
	}
	if in.NotifyScriptPath == "" && in.NotifyScriptArgs != "" {
		return fieldProblem("notify_script_path", s.translate(r, "settings.error.scriptPath"))
	}
	return nil
}

func (s *Server) applySecuritySettings(r *http.Request, in *store.Settings, _ store.Settings) error {
	user := strings.TrimSpace(r.FormValue("ui_user"))
	password := r.FormValue("ui_password")
	confirm := r.FormValue("ui_password_confirm")
	switch {
	case password != "" && user == "":
		return formProblem(s.translate(r, "settings.error.userRequired"))
	case password != "" && len(password) < 8:
		return formProblem(s.translate(r, "settings.error.passwordShort"))
	case password != "" && password != confirm:
		return formProblem(s.translate(r, "settings.error.passwordMismatch"))
	case password == "" && user != "" && in.UIPasswordHash == "":
		return formProblem(s.translate(r, "settings.error.passwordRequired"))
	}
	if password != "" {
		hash, err := hashPassword(password)
		if err != nil {
			return formProblem(err.Error())
		}
		in.UIPasswordHash = hash
	}
	if user == "" {
		in.UIPasswordHash = ""
	}
	in.UIUser = user
	if r.Form.Has("trusted_hosts") {
		normalized, ok := normalizeTrustedHosts(strings.TrimSpace(r.FormValue("trusted_hosts")))
		if !ok {
			return fieldProblem("trusted_hosts", s.translate(r, "settings.error.trustedHosts"))
		}
		in.TrustedHosts = normalized
	}
	return nil
}

func (s *Server) applyNetworkSettings(r *http.Request, in *store.Settings, current store.Settings) error {
	if err := s.readURL(r, "flaresolverr_url", "settings.error.flaresolverrURL", &in.FlareSolverrURL); err != nil {
		return err
	}
	if err := s.readIntSettings(r, []intSetting{
		{"flaresolverr_timeout_seconds", &in.FlareSolverrTimeoutSeconds, current.FlareSolverrTimeoutSeconds, 5, 300},
	}); err != nil {
		return err
	}
	rate, err := s.formFloat(r, "host_rate", 1, 0, 100)
	in.HostRate = rate
	if err != nil {
		return formProblem(err.Error())
	}
	return s.readIntSettings(r, []intSetting{
		{"host_burst", &in.HostBurst, 4, 1, 100},
	})
}

func (s *Server) applyUpdateSettings(r *http.Request, in *store.Settings, current store.Settings) error {
	return s.readIntSettings(r, []intSetting{
		{"default_interval_seconds", &in.DefaultIntervalSeconds, 3600, 60, 30 * 24 * 3600},
		{"retention_days", &in.RetentionDays, 0, 0, 3650},
		{"history_retention_days", &in.HistoryRetentionDays, current.HistoryRetentionDays, 0, 3650},
		{"seeders_ttl_hours", &in.SeedersTTLHours, current.SeedersTTLHours, 1, 168},
	})
}

func (s *Server) applyBackupSettings(r *http.Request, in *store.Settings, _ store.Settings) error {
	in.BackupEnabled = r.FormValue("backup_enabled") != ""
	return s.readIntSettings(r, []intSetting{
		{"backup_interval_hours", &in.BackupIntervalHours, 24, 6, 168},
		{"backup_keep", &in.BackupKeep, 7, 1, 60},
	})
}

func (s *Server) applyAdvancedSettings(r *http.Request, in *store.Settings, current store.Settings) error {
	maxTorrentMB := 0
	if err := s.readIntSettings(r, []intSetting{
		{"fetch_timeout_seconds", &in.FetchTimeoutSeconds, 45, 5, 300},
		{"breaker_threshold", &in.BreakerThreshold, 5, 1, 100},
		{"breaker_cooldown", &in.BreakerCooldownSeconds, 60, 1, 3600},
		{"workers", &in.Workers, 2, 1, 16},
		{"max_torrents_per_run", &in.MaxTorrentsPerRun, 30, 0, 200},
		{"max_torrent_mb", &maxTorrentMB, 8, 1, 64},
	}); err != nil {
		return err
	}
	in.MaxTorrentBytes = int64(maxTorrentMB) << 20
	if err := s.readIntSettings(r, []intSetting{
		{"torznab_download_factor", &in.TorznabDownloadFactor, 0, 0, 100},
		{"torznab_upload_factor", &in.TorznabUploadFactor, 1, 0, 100},
	}); err != nil {
		return err
	}
	in.FetchTorrents = r.FormValue("fetch_torrents") != ""
	in.UserAgent = strings.TrimSpace(r.FormValue("user_agent"))
	if in.UserAgent == "" {
		in.UserAgent = store.DefaultSettings().UserAgent
	}
	if err := s.readIntSettings(r, []intSetting{
		{"auto_disable_after", &in.AutoDisableAfter, 0, 0, 100},
		{"api_rate_limit_per_minute", &in.APIRateLimit, 0, 0, 100000},
	}); err != nil {
		return err
	}
	in.TorznabLiveSearch = r.FormValue("torznab_live_search") != ""
	in.TorznabProxyMagnets = r.FormValue("torznab_proxy_magnets") != ""
	in.TorznabProxyEd2k = r.FormValue("torznab_proxy_ed2k") != ""
	return s.readIntSettings(r, []intSetting{
		{"torznab_live_timeout_seconds", &in.TorznabLiveTimeout, 20, 5, 120},
		{"torznab_live_cache_seconds", &in.TorznabLiveCache, 300, 0, 3600},
		{"torznab_live_wait_seconds", &in.TorznabLiveWait, 5, 1, 120},
		{"stored_source_limit", &in.StoredSourceLimit, current.StoredSourceLimit, 0, 20},
		{"stored_retention_days", &in.StoredRetentionDays, current.StoredRetentionDays, 0, 3650},
	})
}

// applyLegacyDownloadClient reads the single download client of the old
// all-in-one Settings form. Download clients now have their own page; the
// fields stay accepted for forms and API clients written before it.
func (s *Server) applyLegacyDownloadClient(r *http.Request, in *store.Settings) error {
	in.DownloadClient = strings.ToLower(strings.TrimSpace(r.FormValue("download_client")))
	switch in.DownloadClient {
	case "", "none":
		in.DownloadClient = "none"
	case "qbittorrent", "transmission":
	default:
		return formProblem(s.translate(r, "settings.error.downloadClient"))
	}
	in.DownloadClientURL = strings.TrimRight(strings.TrimSpace(r.FormValue("download_client_url")), "/")
	if in.DownloadClient != "none" {
		parsed, err := url.Parse(in.DownloadClientURL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return formProblem(s.translate(r, "settings.error.downloadClientURL"))
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
	return nil
}
