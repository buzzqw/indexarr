package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/buzzqw/indexarr/internal/store"
)

func getWith(t *testing.T, handler http.Handler, request *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func seedSettingsSecrets(t *testing.T, env *testEnv) {
	t.Helper()
	ctx := context.Background()
	settings, err := env.store.LoadSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	settings.PublicURL = "https://idx.example"
	settings.NotifyTelegramToken = "SYNTHETICTELEGRAMTOKEN"
	settings.NotifyTelegramChatID = "4242"
	settings.DownloadClient = "qbittorrent"
	settings.DownloadClientURL = "http://127.0.0.1:8090"
	settings.DownloadClientUser = "synthetic"
	settings.DownloadClientPassword = "SYNTHETICDOWNLOADPASSWORD"
	if err := env.store.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
}

// TestSettingsSecretsStayOutOfInitialHTML covers the Block B rule: the API key,
// Telegram token and download-client password are never part of /settings, and
// the endpoint keeps the host/path visible while masking only the key.
func TestSettingsSecretsStayOutOfInitialHTML(t *testing.T) {
	env := newTestEnv(t)
	seedSettingsSecrets(t, env)

	body := get(t, env.handler, "/settings?section=security").Body.String()
	general := get(t, env.handler, "/settings?section=notifications").Body.String()
	for _, secret := range []string{
		env.apiKey,
		"SYNTHETICTELEGRAMTOKEN",
		"apikey=" + env.apiKey,
	} {
		if secret == "" {
			continue
		}
		if strings.Contains(body, secret) || strings.Contains(general, secret) {
			t.Errorf("initial /settings HTML contains the secret %q", secret)
		}
	}
	if !strings.Contains(body, "https://idx.example/api?apikey=••••••") {
		t.Error("the Torznab endpoint does not show the host/path with a masked key")
	}
	field := regexp.MustCompile(`id="notify_telegram_token"[^>]*value="([^"]*)"`).FindStringSubmatch(general)
	if field == nil {
		t.Error("secret field notify_telegram_token is missing")
	} else if field[1] != "" {
		t.Error("secret field notify_telegram_token starts with a value")
	}
	// Download clients moved to App: their form never starts with a secret.
	clientForm := get(t, env.handler, "/download-clients/new").Body.String()
	for _, id := range []string{"client_password", "client_api_key"} {
		field := regexp.MustCompile(`id="` + id + `"[^>]*value="([^"]*)"`).FindStringSubmatch(clientForm)
		if field == nil {
			t.Errorf("secret field %s is missing from the client form", id)
			continue
		}
		if field[1] != "" {
			t.Errorf("secret field %s starts with a value", id)
		}
	}
	if strings.Contains(clientForm, "SYNTHETICDOWNLOADPASSWORD") {
		t.Error("the client form contains the stored password")
	}
	if !strings.Contains(general, `data-secret-change`) {
		t.Error("the Telegram secret field has no Change button")
	}
	if !strings.Contains(general, `name="notify_telegram_token_remove"`) {
		t.Error("the explicit Telegram Remove action is missing")
	}
	if !strings.Contains(clientForm, `id="client_password"`) {
		t.Error("the client form has no password field")
	}

	request, _ := http.NewRequest(http.MethodGet, "/settings/secrets/torznab", nil)
	request.Header.Set("X-Requested-With", "fetch")
	recorder := getWith(t, env.handler, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("/settings/secrets/torznab = %d: %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "https://idx.example/api?apikey="+url.QueryEscape(env.apiKey)) {
		t.Error("the explicit fetch does not return the complete Torznab URL")
	}
	if recorder.Header().Get("Cache-Control") != "no-store" {
		t.Error("the secret response is cacheable")
	}

	plain, _ := http.NewRequest(http.MethodGet, "/settings/secrets/torznab", nil)
	if code := getWith(t, env.handler, plain).Code; code != http.StatusNotFound {
		t.Errorf("Torznab secret without the fetch header = %d, want 404", code)
	}
}

// TestSettingsSecretFieldsPreserveAndReplace checks the empty-means-unchanged
// contract directly on the stored settings.
func TestSettingsSecretFieldsPreserveAndReplace(t *testing.T) {
	env := newTestEnv(t)
	seedSettingsSecrets(t, env)
	token, cookie := csrfFrom(t, env.handler, "/settings")

	base := url.Values{
		"csrf":                         {token},
		"notify_telegram_token":        {""},
		"notify_telegram_chat_id":      {"4242"},
		"download_client":              {"qbittorrent"},
		"download_client_url":          {"http://127.0.0.1:8090"},
		"download_client_password":     {""},
		"download_client_user":         {"synthetic"},
		"default_interval_seconds":     {"3600"},
		"retention_days":               {"0"},
		"fetch_timeout_seconds":        {"45"},
		"workers":                      {"2"},
		"max_torrents_per_run":         {"30"},
		"max_torrent_mb":               {"8"},
		"fetch_torrents":               {"on"},
		"user_agent":                   {"indexarr/test"},
		"torznab_download_factor":      {"0"},
		"torznab_upload_factor":        {"1"},
		"api_rate_limit_per_minute":    {"0"},
		"backup_interval_hours":        {"24"},
		"backup_keep":                  {"7"},
		"torznab_live_timeout_seconds": {"20"},
	}
	recorder := postForm(t, env.handler, "/settings", base, cookie)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("empty secret save = %d: %s", recorder.Code, recorder.Body.String())
	}
	stored, err := env.store.LoadSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stored.NotifyTelegramToken != "SYNTHETICTELEGRAMTOKEN" {
		t.Errorf("Telegram token after an empty field = %q", stored.NotifyTelegramToken)
	}
	if stored.DownloadClientPassword != "SYNTHETICDOWNLOADPASSWORD" {
		t.Errorf("download password after an empty field = %q", stored.DownloadClientPassword)
	}

	replaced := url.Values{}
	for key, values := range base {
		replaced[key] = values
	}
	replaced.Set("notify_telegram_token", "REPLACEDTELEGRAMTOKEN")
	replaced.Set("download_client_password", "REPLACEDDOWNLOADPASSWORD")
	recorder = postForm(t, env.handler, "/settings", replaced, cookie)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("replacement save = %d: %s", recorder.Code, recorder.Body.String())
	}
	stored, err = env.store.LoadSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stored.NotifyTelegramToken != "REPLACEDTELEGRAMTOKEN" {
		t.Errorf("Telegram token was not replaced: %q", stored.NotifyTelegramToken)
	}
	if stored.DownloadClientPassword != "REPLACEDDOWNLOADPASSWORD" {
		t.Errorf("download password was not replaced: %q", stored.DownloadClientPassword)
	}
}

// TestSettingsSecretFieldsCanBeRemoved checks the explicit Remove action,
// which is separate from Change and deletes the stored value.
func TestSettingsSecretFieldsCanBeRemoved(t *testing.T) {
	env := newTestEnv(t)
	seedSettingsSecrets(t, env)
	token, cookie := csrfFrom(t, env.handler, "/settings")
	values := url.Values{
		"csrf":                            {token},
		"notify_telegram_token":           {""},
		"notify_telegram_token_remove":    {"1"},
		"notify_telegram_chat_id":         {"4242"},
		"download_client":                 {"qbittorrent"},
		"download_client_url":             {"http://127.0.0.1:8090"},
		"download_client_password":        {""},
		"download_client_password_remove": {"1"},
		"download_client_user":            {"synthetic"},
		"default_interval_seconds":        {"3600"},
		"retention_days":                  {"0"},
		"fetch_timeout_seconds":           {"45"},
		"workers":                         {"2"},
		"max_torrents_per_run":            {"30"},
		"max_torrent_mb":                  {"8"},
		"fetch_torrents":                  {"on"},
		"user_agent":                      {"indexarr/test"},
		"torznab_download_factor":         {"0"},
		"torznab_upload_factor":           {"1"},
		"api_rate_limit_per_minute":       {"0"},
		"backup_interval_hours":           {"24"},
		"backup_keep":                     {"7"},
		"torznab_live_timeout_seconds":    {"20"},
	}
	recorder := postForm(t, env.handler, "/settings", values, cookie)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("remove save = %d: %s", recorder.Code, recorder.Body.String())
	}
	stored, err := env.store.LoadSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stored.NotifyTelegramToken != "" {
		t.Errorf("Telegram token after removal = %q, want empty", stored.NotifyTelegramToken)
	}
	if stored.DownloadClientPassword != "" {
		t.Errorf("download password after removal = %q, want empty", stored.DownloadClientPassword)
	}
}

// TestReleaseTableLayoutModel checks the rendered row model (second line,
// relative date, Download action) and the CSS that keeps the fixed table inside
// its container with an auto-width title column.
func TestReleaseTableLayoutModel(t *testing.T) {
	env := newTestEnv(t)
	release := store.Release{
		DedupKey:    "phase1c-layout",
		Title:       "A Very Long Release Title That Must Stay In The Widest Column 2026 2160p WEB-DL",
		SearchTitle: store.NormalizeSearch("A Very Long Release Title That Must Stay In The Widest Column 2026 2160p WEB-DL"),
		Size:        18 << 30,
		Seeders:     1204,
		Leechers:    35,
		Category:    "5040",
		PublishedAt: time.Now().Add(-2 * time.Hour),
		Magnet:      "magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567",
		FirstSeenAt: time.Now(),
		LastSeenAt:  time.Now(),
	}
	if _, err := env.store.UpsertRelease(context.Background(), &release); err != nil {
		t.Fatal(err)
	}
	body := get(t, env.handler, "/releases?q=very+long+release").Body.String()
	if strings.Count(body, `<th scope="col"`) != 6 {
		t.Errorf("the release table has %d columns, want 6", strings.Count(body, `<th scope="col"`))
	}
	for _, want := range []string{
		`class="release-title"`,
		`class="release-meta"`,
		"35 leechers · TV/HD",
		`title="A Very Long Release Title That Must Stay In The Widest Column 2026 2160p WEB-DL"`,
		`class="btn small primary" href="magnet:`,
		">Download</a>",
		"2h ago",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("release table model missing %q", want)
		}
	}
	if strings.Contains(body, ">magnet</a>") {
		t.Error("the truncated magnet link is still rendered")
	}
	if strings.Contains(body, ">Leechers</th>") || strings.Contains(body, ">Category</th>") {
		t.Error("leechers and category must move to the second line, not stay as columns")
	}
	if !regexp.MustCompile(`title="\d{4}-\d{2}-\d{2} \d{2}:\d{2}"`).MatchString(body) {
		t.Error("the relative date has no full date in its title")
	}

	css := get(t, env.handler, "/static/app.css").Body.String()
	for _, want := range []string{
		".releases-table { width: 100%; table-layout: fixed; }",
		".releases-table th { overflow: visible; text-overflow: clip; }",
		".releases-table .release-meta",
		"@media (max-width: 700px)",
	} {
		if !strings.Contains(css, want) {
			t.Errorf("release table CSS missing %q", want)
		}
	}
	if strings.Contains(css, ".releases-table th:nth-child(2) { width:") {
		t.Error("the title column has a fixed width instead of taking the remaining space")
	}
}

func TestReleaseNumericFiltersStartEmpty(t *testing.T) {
	env := newTestEnv(t)
	body := get(t, env.handler, "/releases").Body.String()
	for _, id := range []string{"min_seeders", "min_size_mb", "max_size_mb"} {
		field := regexp.MustCompile(`id="` + id + `"[^>]*value="([^"]*)"`).FindStringSubmatch(body)
		if field == nil {
			t.Errorf("numeric filter %s is missing", id)
			continue
		}
		if field[1] != "" {
			t.Errorf("numeric filter %s starts at %q, want empty", id, field[1])
		}
	}

	filtered := get(t, env.handler, "/releases?min_seeders=5&min_size_mb=10&max_size_mb=20").Body.String()
	for id, want := range map[string]string{
		"min_seeders": "5", "min_size_mb": "10", "max_size_mb": "20",
	} {
		field := regexp.MustCompile(`id="` + id + `"[^>]*value="([^"]*)"`).FindStringSubmatch(filtered)
		if field == nil || field[1] != want {
			t.Errorf("numeric filter %s = %v, want %q", id, field, want)
		}
	}
}

func TestReleaseAdvancedFiltersPanel(t *testing.T) {
	env := newTestEnv(t)
	body := get(t, env.handler, "/releases").Body.String()
	for _, want := range []string{
		`id="release-advanced-filters"`,
		`<details class="filter-advanced"`,
		">Filters</span>",
		"All indexers",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("advanced filter panel missing %q", want)
		}
	}
	if strings.Contains(body, "All sources") {
		t.Error("the indexer filter still says All sources")
	}

	active := get(t, env.handler, "/releases?min_seeders=5&quality=1080p&favorites=1").Body.String()
	if !strings.Contains(active, "3 active filters") {
		t.Error("the closed panel does not count three active filters")
	}
	italian := get(t, env.handler, "/releases?lang=it&min_seeders=5").Body.String()
	if !strings.Contains(italian, "1 filtro attivo") || !strings.Contains(italian, "Tutti gli indexer") {
		t.Error("the Italian active-filter count or indexer label is missing")
	}
}

// TestValidationAndSavedMessagesUseCatalog checks the messages moved from Go
// handlers into the catalogs, in one non-English language.
func TestValidationAndSavedMessagesUseCatalog(t *testing.T) {
	env := newTestEnv(t)
	token, cookie := csrfFrom(t, env.handler, "/settings?lang=it")
	invalid := postForm(t, env.handler, "/settings?lang=it", url.Values{
		"csrf":    {token},
		"workers": {"not-a-number"},
	}, cookie)
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid workers = %d, want 400", invalid.Code)
	}
	if !strings.Contains(invalid.Body.String(), "workers deve essere un numero") {
		t.Error("the formInt validation message is not translated")
	}

	saved := postForm(t, env.handler, "/releases/saved?lang=it", url.Values{
		"csrf":  {token},
		"name":  {""},
		"query": {"q=x"},
	}, cookie)
	if saved.Code != http.StatusSeeOther {
		t.Fatalf("empty saved search = %d", saved.Code)
	}
	if location := saved.Header().Get("Location"); !strings.Contains(location, "err=Una+ricerca+salvata+richiede+un+nome") {
		t.Errorf("saved-search validation flash = %q", location)
	}

	created := postForm(t, env.handler, "/releases/saved?lang=it", url.Values{
		"csrf":  {token},
		"name":  {"TV"},
		"query": {"q=x"},
	}, cookie)
	if location := created.Header().Get("Location"); !strings.Contains(location, "msg=Ricerca+salvata") {
		t.Errorf("saved flash = %q", location)
	}
}
