package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/buzzqw/indexarr/internal/notify"
	"github.com/buzzqw/indexarr/internal/store"
)

func TestNotificationsSectionKeepsSecretsMasked(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer hook.Close()
	webhookURL := hook.URL + "/indexarr/TOPSECRETHOOK"

	token, cookie := csrfFrom(t, env.handler, "/settings?section=notifications")
	recorder := postForm(t, env.handler, "/settings", url.Values{
		"csrf":                    {token},
		"section":                 {"notifications"},
		"notify_webhook_url":      {webhookURL},
		"notify_telegram_token":   {"999:SECRETTELEGRAM"},
		"notify_telegram_chat_id": {"42"},
		"notify_new_releases":     {"on"},
		"notify_failures":         {"on"},
		"notify_grab_failed":      {"on"},
		"notify_catalog_updated":  {"on"},
		"notify_backup_failed":    {"on"},
		"notify_cooldown_minutes": {"45"},
	}, cookie)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("notifications save = %d: %s", recorder.Code, recorder.Body.String())
	}
	stored, err := env.store.LoadSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stored.NotifyWebhookURL != webhookURL {
		t.Errorf("stored webhook = %q", stored.NotifyWebhookURL)
	}
	if stored.NotifyCooldownMinutes != 45 {
		t.Errorf("stored cooldown = %d", stored.NotifyCooldownMinutes)
	}
	// The settings table holds neither secret in clear.
	var plaintext int
	if err := env.store.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM settings WHERE key IN (?, ?)`,
		store.KeyNotifyWebhook, store.KeyNotifyTelegramToken).Scan(&plaintext); err != nil {
		t.Fatal(err)
	}
	if plaintext != 0 {
		t.Errorf("plaintext notification settings in the database = %d", plaintext)
	}

	page := get(t, env.handler, "/settings?section=notifications").Body.String()
	if strings.Contains(page, "TOPSECRETHOOK") || strings.Contains(page, "SECRETTELEGRAM") {
		t.Error("the notifications page contains a stored secret")
	}
	for _, want := range []string{
		`id="notify_webhook_url"`, `id="notify_telegram_token"`,
		`name="notify_grab_failed"`, `name="notify_catalog_updated"`,
		`name="notify_backup_failed"`, `name="notify_cooldown_minutes"`,
		`action="/settings/notifications/test"`, "••••••••",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("notifications page missing %q", want)
		}
	}

	request, _ := http.NewRequest(http.MethodGet, "/settings/secrets/webhook", nil)
	request.Header.Set("X-Requested-With", "fetch")
	secret := getWith(t, env.handler, request)
	if secret.Code != http.StatusOK || !strings.Contains(secret.Body.String(), "TOPSECRETHOOK") {
		t.Errorf("webhook show = %d: %s", secret.Code, secret.Body.String())
	}
	if secret.Header().Get("Cache-Control") != "no-store" {
		t.Error("the webhook secret response is cacheable")
	}
	plain, _ := http.NewRequest(http.MethodGet, "/settings/secrets/webhook", nil)
	if code := getWith(t, env.handler, plain).Code; code != http.StatusNotFound {
		t.Errorf("webhook secret without the fetch header = %d, want 404", code)
	}
}

// TestNotificationChannelsSaveSealAndRender covers the new channels end to
// end: the form accepts them, the store seals the secrets and the page exposes
// the fields without their values.
func TestNotificationChannelsSaveSealAndRender(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	token, cookie := csrfFrom(t, env.handler, "/settings?section=notifications")
	secrets := map[string]string{
		"notify_apprise_key":    "APPRISESECRET",
		"notify_discord_url":    "https://discord.example/api/webhooks/DISCORDSECRET",
		"notify_slack_url":      "https://hooks.slack.example/services/SLACKSECRET",
		"notify_gotify_token":   "GOTIFYSECRET",
		"notify_ntfy_token":     "NTFYSECRET",
		"notify_pushover_token": "PUSHOVERSECRET",
		"notify_pushover_user":  "PUSHOVERUSER",
		"notify_smtp_password":  "SMTPPASSWORD",
	}
	values := url.Values{
		"csrf":                    {token},
		"section":                 {"notifications"},
		"notify_apprise_url":      {"http://apprise.example"},
		"notify_gotify_url":       {"https://gotify.example"},
		"notify_ntfy_url":         {"https://ntfy.example"},
		"notify_ntfy_topic":       {"indexarr"},
		"notify_smtp_host":        {"smtp.example.com:587"},
		"notify_smtp_starttls":    {"on"},
		"notify_smtp_username":    {"indexarr"},
		"notify_smtp_from":        {"indexarr@example.com"},
		"notify_smtp_to":          {"ops@example.com"},
		"notify_script_path":      {"/usr/local/bin/indexarr-notify"},
		"notify_script_args":      {"--flag value"},
		"notify_new_releases":     {"on"},
		"notify_grab_failed":      {"on"},
		"notify_grab_success":     {"on"},
		"notify_catalog_updated":  {"on"},
		"notify_backup_failed":    {"on"},
		"notify_cooldown_minutes": {"30"},
	}
	for key, value := range secrets {
		values.Set(key, value)
	}
	recorder := postForm(t, env.handler, "/settings", values, cookie)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("channels save = %d: %s", recorder.Code, recorder.Body.String())
	}
	stored, err := env.store.LoadSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stored.NotifyAppriseKey != secrets["notify_apprise_key"] ||
		stored.NotifyDiscordURL != secrets["notify_discord_url"] ||
		stored.NotifySlackURL != secrets["notify_slack_url"] ||
		stored.NotifyGotifyToken != secrets["notify_gotify_token"] ||
		stored.NotifyNtfyToken != secrets["notify_ntfy_token"] ||
		stored.NotifyPushoverToken != secrets["notify_pushover_token"] ||
		stored.NotifyPushoverUser != secrets["notify_pushover_user"] ||
		stored.NotifySMTPPassword != secrets["notify_smtp_password"] {
		t.Errorf("secrets round trip = %#v", stored)
	}
	if stored.NotifyAppriseURL != "http://apprise.example" ||
		stored.NotifyGotifyURL != "https://gotify.example" ||
		stored.NotifyNtfyTopic != "indexarr" ||
		stored.NotifySMTPHost != "smtp.example.com:587" ||
		!stored.NotifySMTPStartTLS ||
		stored.NotifyScriptPath != "/usr/local/bin/indexarr-notify" ||
		!stored.NotifyGrabSuccess {
		t.Errorf("channel settings = %#v", stored)
	}
	page := get(t, env.handler, "/settings?section=notifications").Body.String()
	for key, value := range secrets {
		if strings.Contains(page, value) {
			t.Errorf("the notifications page leaks %s", key)
		}
	}
	for _, want := range []string{
		`id="notify_apprise_url"`, `id="notify_apprise_key"`,
		`id="notify_discord_url"`, `id="notify_slack_url"`,
		`id="notify_gotify_url"`, `id="notify_gotify_token"`,
		`id="notify_ntfy_url"`, `id="notify_ntfy_topic"`, `id="notify_ntfy_token"`,
		`id="notify_pushover_token"`, `id="notify_pushover_user"`,
		`id="notify_smtp_host"`, `id="notify_smtp_starttls"`, `id="notify_smtp_username"`,
		`id="notify_smtp_password"`, `id="notify_smtp_from"`, `id="notify_smtp_to"`,
		`id="notify_script_path"`, `id="notify_script_args"`,
		`name="notify_grab_success"`, "••••••••",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("notifications page missing %q", want)
		}
	}

	request, _ := http.NewRequest(http.MethodGet, "/settings/secrets/discord_webhook", nil)
	request.Header.Set("X-Requested-With", "fetch")
	secret := getWith(t, env.handler, request)
	if secret.Code != http.StatusOK || !strings.Contains(secret.Body.String(), "DISCORDSECRET") {
		t.Errorf("discord secret = %d: %s", secret.Code, secret.Body.String())
	}
}

func TestTestNotificationButtonUsesEveryConfiguredChannel(t *testing.T) {
	env := newTestEnv(t)
	var discordCalls, slackCalls atomic.Int64
	discord := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		discordCalls.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer discord.Close()
	slack := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		slackCalls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer slack.Close()

	ctx := context.Background()
	settings, _ := env.store.LoadSettings(ctx)
	settings.NotifyDiscordURL = discord.URL
	settings.NotifySlackURL = slack.URL
	if err := env.store.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	token, cookie := csrfFrom(t, env.handler, "/settings?section=notifications")
	recorder := postForm(t, env.handler, "/settings/notifications/test", url.Values{
		"csrf": {token},
	}, cookie)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("test button = %d: %s", recorder.Code, recorder.Body.String())
	}
	if location := recorder.Header().Get("Location"); !strings.Contains(location, "Test+notification+sent") {
		t.Errorf("test redirect = %q", location)
	}
	if discordCalls.Load() != 1 || slackCalls.Load() != 1 {
		t.Errorf("test deliveries = discord %d, slack %d, want 1 and 1",
			discordCalls.Load(), slackCalls.Load())
	}
}

func TestTestNotificationButtonUsesTheWebhook(t *testing.T) {
	env := newTestEnv(t)
	var received atomic.Int64
	var payload notify.Event
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&payload)
		received.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer hook.Close()
	ctx := context.Background()
	settings, _ := env.store.LoadSettings(ctx)
	settings.NotifyWebhookURL = hook.URL
	if err := env.store.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}

	token, cookie := csrfFrom(t, env.handler, "/settings?section=notifications")
	recorder := postForm(t, env.handler, "/settings/notifications/test", url.Values{
		"csrf": {token},
	}, cookie)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("test button = %d: %s", recorder.Code, recorder.Body.String())
	}
	if location := recorder.Header().Get("Location"); !strings.Contains(location, "msg=") ||
		!strings.Contains(location, "Test+notification+sent") {
		t.Errorf("test button redirect = %q", location)
	}
	if received.Load() != 1 {
		t.Fatalf("webhook calls = %d, want 1", received.Load())
	}
	if payload.Kind != notify.KindTest {
		t.Errorf("test payload kind = %q", payload.Kind)
	}
}

func TestTestNotificationButtonUsesTelegramAndHidesTheToken(t *testing.T) {
	env := newTestEnv(t)
	var path, text string
	telegram := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		path = r.URL.Path
		text = r.Form.Get("text")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer telegram.Close()
	oldBase := notify.TelegramAPIBase
	notify.TelegramAPIBase = telegram.URL
	defer func() { notify.TelegramAPIBase = oldBase }()

	const token = "123456:TOPSECRETBOT"
	ctx := context.Background()
	settings, _ := env.store.LoadSettings(ctx)
	settings.NotifyWebhookURL = ""
	settings.NotifyTelegramToken = token
	settings.NotifyTelegramChatID = "-100"
	if err := env.store.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}

	csrf, cookie := csrfFrom(t, env.handler, "/settings?section=notifications")
	recorder := postForm(t, env.handler, "/settings/notifications/test", url.Values{"csrf": {csrf}}, cookie)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("telegram test = %d: %s", recorder.Code, recorder.Body.String())
	}
	if path != "/bot"+token+"/sendMessage" {
		t.Errorf("Telegram path = %q", path)
	}
	if !strings.Contains(text, "indexarr") {
		t.Errorf("Telegram text = %q", text)
	}
	if body := recorder.Body.String(); strings.Contains(body, token) {
		t.Error("the test response contains the Telegram token")
	}
	if dumped := env.logs.Dump(0, ""); strings.Contains(dumped, token) {
		t.Errorf("the application log contains the Telegram token: %s", dumped)
	}
}

func TestTestNotificationWithoutChannelsReportsTheReason(t *testing.T) {
	env := newTestEnv(t)
	token, cookie := csrfFrom(t, env.handler, "/settings?section=notifications")
	recorder := postForm(t, env.handler, "/settings/notifications/test", url.Values{"csrf": {token}}, cookie)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("test without channels = %d", recorder.Code)
	}
	if location := recorder.Header().Get("Location"); !strings.Contains(location, "err=") ||
		!strings.Contains(location, "no+notification+channel") {
		t.Errorf("missing-channel redirect = %q", location)
	}
}

func TestNotificationLanguageFollowsTheInterface(t *testing.T) {
	env := newTestEnv(t)
	if code := get(t, env.handler, "/language/it").Code; code != http.StatusFound && code != http.StatusSeeOther {
		t.Fatalf("language redirect = %d", code)
	}
	settings, _ := env.store.LoadSettings(context.Background())
	if settings.UILanguage != "it" {
		t.Errorf("stored notification language = %q, want it", settings.UILanguage)
	}
}

// TestNotificationLanguageHealsFromTheBrowserChoice is the regression for a
// message arriving in Chinese while the interface was Italian: the saved
// language had been left behind by another device, and nothing brought it back
// in line with the language the operator was actually reading.
func TestNotificationLanguageHealsFromTheBrowserChoice(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	if err := env.store.SetSetting(ctx, store.KeyUILanguage, "zh"); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodGet, "/settings?section=notifications", nil)
	request.AddCookie(&http.Cookie{Name: languageCookie, Value: "it"})
	recorder := httptest.NewRecorder()
	env.handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("settings page = %d", recorder.Code)
	}

	settings, err := env.store.LoadSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if settings.UILanguage != "it" {
		t.Errorf("stored language = %q, want it", settings.UILanguage)
	}
	if lang := notify.ConfigFrom(settings).Language; lang != "it" {
		t.Errorf("notification language = %q, want it", lang)
	}
	// The page says aloud which language the messages use. Look inside the
	// select: "Italiano" also appears in the header language choices.
	selected := selectedOption(messageLanguageSelect(recorder.Body.String()))
	if !strings.Contains(selected, "Italiano") || strings.Contains(selected, "中文") {
		t.Errorf("the selected message language = %q, want Italian", selected)
	}

	// A bare ?lang= is a preview link, not a choice: it must not move the
	// language the background jobs use.
	request = httptest.NewRequest(http.MethodGet, "/?lang=fr", nil)
	request.Header.Set("Accept-Language", "fr-FR")
	recorder = httptest.NewRecorder()
	env.handler.ServeHTTP(recorder, request)
	if settings, err = env.store.LoadSettings(ctx); err != nil {
		t.Fatal(err)
	}
	if settings.UILanguage != "it" {
		t.Errorf("a preview link changed the notification language to %q", settings.UILanguage)
	}
}

// TestNotificationLanguageIsChosenInTheNotificationsForm guards the explicit
// choice: the operator picks the message language in Settings → Notifications
// and the choice survives even when the interface language changes.
func TestNotificationLanguageIsChosenInTheNotificationsForm(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	page := get(t, env.handler, "/settings?section=notifications").Body.String()
	selectHTML := messageLanguageSelect(page)
	if selectHTML == "" {
		t.Fatal("the notifications page has no message-language select")
	}
	for _, want := range []string{"Italiano", "English", "中文", "same as the interface"} {
		if !strings.Contains(strings.ToLower(selectHTML), strings.ToLower(want)) {
			t.Errorf("the select is missing the choice %q", want)
		}
	}

	token, cookie := csrfFrom(t, env.handler, "/settings?section=notifications")
	recorder := postForm(t, env.handler, "/settings", url.Values{
		"csrf": {token}, "section": {"notifications"},
		"notify_language": {"it"}, "notify_cooldown_minutes": {"30"},
	}, cookie)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("save = %d: %s", recorder.Code, recorder.Body.String())
	}
	settings, err := env.store.LoadSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if settings.NotifyLanguage != "it" {
		t.Errorf("stored message language = %q, want it", settings.NotifyLanguage)
	}
	if lang := notify.ConfigFrom(settings).Language; lang != "it" {
		t.Errorf("notification language = %q, want it", lang)
	}

	// Back to the automatic choice.
	token, cookie = csrfFrom(t, env.handler, "/settings?section=notifications")
	postForm(t, env.handler, "/settings", url.Values{
		"csrf": {token}, "section": {"notifications"},
		"notify_language": {""}, "notify_cooldown_minutes": {"30"},
	}, cookie)
	if settings, err = env.store.LoadSettings(ctx); err != nil {
		t.Fatal(err)
	}
	if settings.NotifyLanguage != "" {
		t.Errorf("automatic message language stored as %q", settings.NotifyLanguage)
	}
}

func messageLanguageSelect(body string) string {
	start := strings.Index(body, `id="notify_language"`)
	if start < 0 {
		return ""
	}
	end := strings.Index(body[start:], "</select>")
	if end < 0 {
		return ""
	}
	return body[start : start+end]
}

func selectedOption(selectHTML string) string {
	start := strings.Index(selectHTML, "<option")
	if start < 0 {
		return ""
	}
	end := strings.Index(selectHTML[start:], "</option>")
	if end < 0 {
		return ""
	}
	return selectHTML[start : start+end]
}
