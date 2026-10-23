package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/buzzqw/indexarr/internal/ingest"
	"github.com/buzzqw/indexarr/internal/logbuffer"
	"github.com/buzzqw/indexarr/internal/store"
)

var csrfPattern = regexp.MustCompile(`name="csrf" value="([0-9a-f]{64})"`)

type testEnv struct {
	store   *store.Store
	server  *Server
	handler http.Handler
	apiKey  string
	logs    *logbuffer.Buffer
	level   *slog.LevelVar
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	st, err := store.Open(t.TempDir() + "/web.db")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	key, _, err := st.EnsureAPIKey(ctx)
	if err != nil {
		t.Fatal(err)
	}
	logs := logbuffer.New(100)
	level := new(slog.LevelVar)
	level.Set(slog.LevelInfo)
	logger := slog.New(logbuffer.NewHandler(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: level}), logs))
	engine := ingest.New(st, logger)
	server := New(Config{Store: st, Engine: engine, Logger: logger, Logs: logs, Level: level, Version: "test", Started: time.Now()})
	return &testEnv{store: st, server: server, handler: server.Handler(), apiKey: key, logs: logs, level: level}
}

func csrfFrom(t *testing.T, handler http.Handler, path string) (string, *http.Cookie) {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET %s = %d: %s", path, recorder.Code, recorder.Body.String())
	}
	match := csrfPattern.FindStringSubmatch(recorder.Body.String())
	if match == nil {
		t.Fatalf("no CSRF token in %s", path)
	}
	cookies := recorder.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatalf("no CSRF cookie in %s", path)
	}
	return match[1], cookies[0]
}

func postForm(t *testing.T, handler http.Handler, path string, values url.Values, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(values.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if cookie != nil {
		request.AddCookie(cookie)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func get(t *testing.T, handler http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func TestDashboard(t *testing.T) {
	env := newTestEnv(t)
	recorder := get(t, env.handler, "/")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	for _, want := range []string{"Dashboard", "No sources yet", "Torznab endpoint", "indexarr"} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard missing %q", want)
		}
	}
}

func TestStaticAndHealth(t *testing.T) {
	env := newTestEnv(t)
	if recorder := get(t, env.handler, "/static/app.css"); recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "--bg") {
		t.Errorf("static CSS: %d", recorder.Code)
	}
	health := get(t, env.handler, "/healthz")
	if health.Code != http.StatusOK || !strings.Contains(health.Body.String(), `"status":"ok"`) {
		t.Errorf("health: %d %s", health.Code, health.Body.String())
	}
	page := get(t, env.handler, "/")
	if page.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("HTML cache control = %q", page.Header().Get("Cache-Control"))
	}
	for _, asset := range []string{"/static/app.css?v=", "/static/app.js?v="} {
		if !strings.Contains(page.Body.String(), asset) {
			t.Errorf("asset URL without version: %s", asset)
		}
	}
}

func TestCreateSource(t *testing.T) {
	env := newTestEnv(t)
	token, cookie := csrfFrom(t, env.handler, "/sources/new")
	values := url.Values{
		"csrf":             {token},
		"name":             {"My indexer"},
		"kind":             {"torznab"},
		"manager":          {"auto"},
		"url":              {"http://indexer.test"},
		"api_key":          {"k"},
		"username":         {"feeduser"},
		"password":         {"feedpass"},
		"categories":       {"5000, 2000"},
		"default_category": {"5040"},
		"query":            {""},
		"interval_seconds": {"600"},
		"enabled":          {"on"},
		"then":             {"run"},
	}
	recorder := postForm(t, env.handler, "/sources", values, cookie)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
	sources, err := env.store.ListSources(context.Background())
	if err != nil || len(sources) != 1 {
		t.Fatalf("sources = %#v, %v", sources, err)
	}
	if sources[0].Name != "My indexer" || sources[0].Categories != "5000,2000" ||
		sources[0].IntervalSeconds != 600 || sources[0].DefaultCategory != "5040" {
		t.Errorf("source = %#v", sources[0])
	}
	if sources[0].Username != "feeduser" || sources[0].Password != "feedpass" {
		t.Errorf("HTTP credentials not stored: %#v", sources[0])
	}
	if body := get(t, env.handler, "/sources").Body.String(); !strings.Contains(body, "My indexer") {
		t.Error("sources page does not list the new source")
	}
}

func TestCSRFRequired(t *testing.T) {
	env := newTestEnv(t)
	recorder := postForm(t, env.handler, "/sources", url.Values{"name": {"x"}}, nil)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", recorder.Code)
	}
}

func TestSettingsSave(t *testing.T) {
	env := newTestEnv(t)
	token, cookie := csrfFrom(t, env.handler, "/settings")
	values := url.Values{
		"csrf":                         {token},
		"public_url":                   {"http://idx.local"},
		"flaresolverr_url":             {"http://127.0.0.1:8191/"},
		"default_interval_seconds":     {"1800"},
		"retention_days":               {"7"},
		"fetch_timeout_seconds":        {"30"},
		"workers":                      {"3"},
		"max_torrents_per_run":         {"10"},
		"max_torrent_mb":               {"4"},
		"fetch_torrents":               {"on"},
		"user_agent":                   {"unit-test"},
		"torznab_download_factor":      {"0"},
		"torznab_upload_factor":        {"1"},
		"api_rate_limit_per_minute":    {"120"},
		"backup_enabled":               {"on"},
		"backup_interval_hours":        {"12"},
		"backup_keep":                  {"3"},
		"torznab_live_search":          {"on"},
		"torznab_live_timeout_seconds": {"30"},
		"download_client":              {"qbittorrent"},
		"download_client_url":          {"http://127.0.0.1:8080"},
		"download_client_user":         {"admin"},
		"download_client_password":     {"secret"},
		"download_client_category":     {"indexarr"},
		"download_client_save_path":    {"/downloads"},
		"download_client_paused":       {"on"},
		"notify_webhook_url":           {"http://127.0.0.1:9999/hook"},
		"notify_telegram_token":        {""},
		"notify_telegram_chat_id":      {""},
		"notify_new_releases":          {"on"},
		"notify_failures":              {"on"},
		"auto_disable_after":           {"3"},
		"ui_user":                      {""},
		"ui_password":                  {""},
		"ui_password_confirm":          {""},
	}
	recorder := postForm(t, env.handler, "/settings", values, cookie)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
	settings, err := env.store.LoadSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if settings.PublicURL != "http://idx.local" || settings.DefaultIntervalSeconds != 1800 ||
		settings.RetentionDays != 7 || settings.Workers != 3 || settings.MaxTorrentBytes != 4<<20 {
		t.Errorf("settings = %#v", settings)
	}
	if settings.FlareSolverrURL != "http://127.0.0.1:8191" {
		t.Errorf("flaresolverr URL = %q", settings.FlareSolverrURL)
	}
	if settings.NotifyWebhookURL != "http://127.0.0.1:9999/hook" || !settings.NotifyNewReleases ||
		!settings.NotifyFailures || settings.AutoDisableAfter != 3 {
		t.Errorf("notification settings = %#v", settings)
	}
	if settings.APIRateLimit != 120 {
		t.Errorf("API rate limit = %d", settings.APIRateLimit)
	}
	if !settings.BackupEnabled || settings.BackupIntervalHours != 12 || settings.BackupKeep != 3 {
		t.Errorf("backup settings = %#v", settings)
	}
	if !settings.TorznabLiveSearch || settings.TorznabLiveTimeout != 30 {
		t.Errorf("live search settings = %#v", settings)
	}
	if settings.DownloadClient != "qbittorrent" || settings.DownloadClientURL != "http://127.0.0.1:8080" ||
		settings.DownloadClientCategory != "indexarr" || !settings.DownloadClientPaused {
		t.Errorf("download client settings = %#v", settings)
	}
}

func TestSettingsRejectsShortPassword(t *testing.T) {
	env := newTestEnv(t)
	token, cookie := csrfFrom(t, env.handler, "/settings")
	values := url.Values{
		"csrf":                     {token},
		"public_url":               {""},
		"default_interval_seconds": {"3600"},
		"retention_days":           {"0"},
		"fetch_timeout_seconds":    {"45"},
		"workers":                  {"2"},
		"max_torrents_per_run":     {"30"},
		"max_torrent_mb":           {"8"},
		"fetch_torrents":           {"on"},
		"user_agent":               {"x"},
		"torznab_download_factor":  {"0"},
		"torznab_upload_factor":    {"1"},
		"ui_user":                  {"admin"},
		"ui_password":              {"short"},
		"ui_password_confirm":      {"short"},
	}
	recorder := postForm(t, env.handler, "/settings", values, cookie)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "at least 8 characters") {
		t.Error("the validation message is missing")
	}
}

func TestMaintenanceIntegrity(t *testing.T) {
	env := newTestEnv(t)
	token, cookie := csrfFrom(t, env.handler, "/maintenance")
	recorder := postForm(t, env.handler, "/maintenance", url.Values{
		"csrf":   {token},
		"action": {"integrity"},
	}, cookie)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
	location := recorder.Header().Get("Location")
	if !strings.Contains(location, "/maintenance?") {
		t.Fatalf("location = %q", location)
	}
	page := get(t, env.handler, location)
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "Integrity check: ok") {
		t.Errorf("maintenance page = %d\n%s", page.Code, page.Body.String())
	}
}

func TestReleasesPage(t *testing.T) {
	env := newTestEnv(t)
	release := store.Release{
		DedupKey: "k1", Title: "The Show S01E01 1080p",
		SearchTitle: store.NormalizeSearch("The Show S01E01 1080p"),
		Size:        1024, Seeders: 3, Category: "5040", SourceName: "src",
		Magnet:      "magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567",
		FirstSeenAt: time.Now(), LastSeenAt: time.Now(),
	}
	if _, err := env.store.UpsertRelease(context.Background(), &release); err != nil {
		t.Fatal(err)
	}
	recorder := get(t, env.handler, "/releases?q=Show")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "The Show S01E01 1080p") || !strings.Contains(body, "1 release(s) found") {
		t.Errorf("releases page:\n%s", body)
	}
	detail := get(t, env.handler, "/releases/"+strconv.FormatInt(release.ID, 10))
	if detail.Code != http.StatusOK || !strings.Contains(detail.Body.String(), "The Show S01E01 1080p") {
		t.Errorf("detail page = %d", detail.Code)
	}
}

func TestUIAuthentication(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	settings, err := env.store.LoadSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := hashPassword("secret123")
	if err != nil {
		t.Fatal(err)
	}
	settings.UIUser = "admin"
	settings.UIPasswordHash = hash
	if err := env.store.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}

	if recorder := get(t, env.handler, "/"); recorder.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated status = %d", recorder.Code)
	}
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.SetBasicAuth("admin", "secret123")
	recorder := httptest.NewRecorder()
	env.handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Errorf("authenticated status = %d", recorder.Code)
	}
	// Torznab has its own key and must not inherit Basic authentication.
	if recorder := get(t, env.handler, "/api?t=caps"); recorder.Code != http.StatusOK {
		t.Errorf("Torznab caps status = %d", recorder.Code)
	}
}
