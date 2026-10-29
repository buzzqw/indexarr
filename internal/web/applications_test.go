package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/buzzqw/indexarr/internal/store"
	"github.com/buzzqw/indexarr/internal/syncjob"
	"github.com/buzzqw/indexarr/internal/vault"
)

// newApplicationEnv builds a web server with a vault (and optionally the sync
// service) for the application pages.
func newApplicationEnv(t *testing.T, withSync bool) (*testEnv, *vault.Vault) {
	t.Helper()
	st, err := store.Open(t.TempDir() + "/applications.db")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	v, err := vault.New(key)
	if err != nil {
		t.Fatalf("vault.New: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := Config{
		Store:   st,
		Logger:  logger,
		Version: "test",
		Started: time.Now(),
		Vault:   v,
	}
	if withSync {
		sync, err := syncjob.New(syncjob.Config{Store: st, Vault: v, Logger: logger})
		if err != nil {
			t.Fatalf("syncjob.New: %v", err)
		}
		cfg.Sync = sync
	}
	server := New(cfg)
	return &testEnv{store: st, server: server, handler: server.Handler()}, v
}

// seedApplication stores an application directly, sealing key when non-empty.
func seedApplication(t *testing.T, st *store.Store, v *vault.Vault, name, syncLevel, key string) store.Application {
	t.Helper()
	app := store.Application{
		Kind:      "sonarr",
		Name:      name,
		URL:       "http://sonarr:8989",
		SyncLevel: syncLevel,
	}
	if key != "" {
		sealed, err := v.SealString(key)
		if err != nil {
			t.Fatalf("SealString: %v", err)
		}
		app.APIKeyEnc = []byte(sealed)
	}
	id, err := st.CreateApplication(context.Background(), app)
	if err != nil {
		t.Fatalf("CreateApplication: %v", err)
	}
	stored, err := st.GetApplication(context.Background(), id)
	if err != nil {
		t.Fatalf("GetApplication: %v", err)
	}
	return stored
}

func applicationValues(token, name, rawURL, apiKey, level string) url.Values {
	return url.Values{
		"csrf":                      {token},
		"kind":                      {"sonarr"},
		"name":                      {name},
		"url":                       {rawURL},
		"api_key":                   {apiKey},
		"sync_level":                {level},
		"profile_fields":            {"1"},
		"enable_rss":                {"on"},
		"enable_automatic_search":   {"on"},
		"enable_interactive_search": {"on"},
	}
}

func TestApplicationPagesRender(t *testing.T) {
	env, _ := newApplicationEnv(t, false)

	list := get(t, env.handler, "/applications")
	if list.Code != http.StatusOK {
		t.Fatalf("GET /applications = %d: %s", list.Code, list.Body.String())
	}
	body := list.Body.String()
	for _, want := range []string{"App", "Download clients", "Connect an app", "No app connected", "No download client"} {
		if !strings.Contains(body, want) {
			t.Errorf("applications page missing %q", want)
		}
	}
	for _, nav := range []string{`href="/applications"`, `href="/settings"`} {
		if !strings.Contains(body, nav) {
			t.Errorf("layout missing nav link %s", nav)
		}
	}

	form := get(t, env.handler, "/applications/new")
	if form.Code != http.StatusOK {
		t.Fatalf("GET /applications/new = %d: %s", form.Code, form.Body.String())
	}
	for _, want := range []string{"Connect an app", "Full sync", "API key"} {
		if !strings.Contains(form.Body.String(), want) {
			t.Errorf("application form missing %q", want)
		}
	}
}

func TestApplicationCreateSealsAPIKey(t *testing.T) {
	env, v := newApplicationEnv(t, false)
	token, cookie := csrfFrom(t, env.handler, "/applications/new")

	const secret = "super-secret-api-key"
	recorder := postForm(t, env.handler, "/applications",
		applicationValues(token, "Sonarr principale", "http://sonarr:8989/", secret, "full"), cookie)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("create status = %d: %s", recorder.Code, recorder.Body.String())
	}
	apps, err := env.store.ListApplications(context.Background())
	if err != nil || len(apps) != 1 {
		t.Fatalf("applications = %#v, %v", apps, err)
	}
	app := apps[0]
	if app.Kind != "sonarr" || app.Name != "Sonarr principale" || app.URL != "http://sonarr:8989" || app.SyncLevel != "full" {
		t.Errorf("created application = %#v", app)
	}
	if len(app.APIKeyEnc) == 0 {
		t.Fatal("the API key was not sealed")
	}
	plaintext, err := v.OpenString(string(app.APIKeyEnc))
	if err != nil || plaintext != secret {
		t.Fatalf("sealed key = %q, %v", plaintext, err)
	}

	list := get(t, env.handler, "/applications").Body.String()
	edit := get(t, env.handler, "/applications/"+strconv.FormatInt(app.ID, 10)+"/edit").Body.String()
	if strings.Contains(list, secret) || strings.Contains(edit, secret) {
		t.Fatal("a page leaked the API key")
	}
	if !strings.Contains(edit, "configured") || !strings.Contains(edit, "Remove the stored API key") {
		t.Error("the edit form does not show the stored-key state")
	}
}

func TestApplicationCreateSavesSyncProfile(t *testing.T) {
	env, v := newApplicationEnv(t, false)
	token, cookie := csrfFrom(t, env.handler, "/applications/new")

	values := applicationValues(token, "Sonarr profilo", "http://sonarr:8989", "secret", "full")
	values.Del("enable_rss")
	values.Del("enable_interactive_search")
	values.Set("min_seeders", "9")
	values.Set("categories", " 5000, 2000,5000 , ")
	recorder := postForm(t, env.handler, "/applications", values, cookie)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("create status = %d: %s", recorder.Code, recorder.Body.String())
	}
	apps, err := env.store.ListApplications(context.Background())
	if err != nil || len(apps) != 1 {
		t.Fatalf("applications = %#v, %v", apps, err)
	}
	app := apps[0]
	if app.EnableRSS || !app.EnableAutomaticSearch || app.EnableInteractiveSearch {
		t.Errorf("search flags = rss:%v automatic:%v interactive:%v",
			app.EnableRSS, app.EnableAutomaticSearch, app.EnableInteractiveSearch)
	}
	if app.MinSeeders != 9 {
		t.Errorf("min seeders = %d, want 9", app.MinSeeders)
	}
	if app.Categories != "2000,5000" {
		t.Errorf("categories = %q, want the normalized list", app.Categories)
	}
	if len(app.APIKeyEnc) == 0 {
		t.Fatal("the API key was not sealed")
	}
	if plaintext, err := v.OpenString(string(app.APIKeyEnc)); err != nil || plaintext != "secret" {
		t.Fatalf("sealed key = %q, %v", plaintext, err)
	}

	edit := get(t, env.handler, "/applications/"+strconv.FormatInt(app.ID, 10)+"/edit").Body.String()
	for _, want := range []string{
		`name="enable_rss"`, `name="enable_automatic_search"`,
		`name="enable_interactive_search"`, `name="min_seeders"`, `name="categories"`,
		`value="2000,5000"`, `value="9"`,
	} {
		if !strings.Contains(edit, want) {
			t.Errorf("edit form missing %q", want)
		}
	}
	if strings.Contains(edit, `name="enable_rss" checked`) {
		t.Error("the stored disabled RSS flag is shown as enabled")
	}
}

func TestApplicationLegacyFormGetsProfileDefaults(t *testing.T) {
	env, _ := newApplicationEnv(t, false)
	token, cookie := csrfFrom(t, env.handler, "/applications/new")
	values := url.Values{
		"csrf":       {token},
		"kind":       {"sonarr"},
		"name":       {"Sonarr"},
		"url":        {"http://sonarr:8989"},
		"api_key":    {"secret"},
		"sync_level": {"full"},
	}
	recorder := postForm(t, env.handler, "/applications", values, cookie)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("create status = %d: %s", recorder.Code, recorder.Body.String())
	}
	apps, err := env.store.ListApplications(context.Background())
	if err != nil || len(apps) != 1 {
		t.Fatalf("applications = %#v, %v", apps, err)
	}
	app := apps[0]
	if !app.EnableRSS || !app.EnableAutomaticSearch || !app.EnableInteractiveSearch {
		t.Errorf("legacy form profile = %#v, want the default search flags", app)
	}
	if app.MinSeeders != 0 || app.Categories != "" {
		t.Errorf("legacy form profile = %#v, want empty overrides", app)
	}
}

func TestApplicationSyncProfileValidation(t *testing.T) {
	env, _ := newApplicationEnv(t, false)
	token, cookie := csrfFrom(t, env.handler, "/applications/new")

	cases := []struct {
		name   string
		field  string
		value  string
		status int
	}{
		{"non-numeric category", "categories", "5000,abc", http.StatusBadRequest},
		{"negative category", "categories", "5000,-1", http.StatusBadRequest},
		{"negative seeders", "min_seeders", "-1", http.StatusBadRequest},
		{"non-numeric seeders", "min_seeders", "many", http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			values := applicationValues(token, "Sonarr", "http://sonarr:8989", "secret", "full")
			values.Set(tc.field, tc.value)
			recorder := postForm(t, env.handler, "/applications", values, cookie)
			if recorder.Code != tc.status {
				t.Fatalf("status = %d, want %d: %s", recorder.Code, tc.status, recorder.Body.String())
			}
		})
	}
	if apps, err := env.store.ListApplications(context.Background()); err != nil || len(apps) != 0 {
		t.Fatalf("invalid profiles created applications: %#v, %v", apps, err)
	}
}

func TestNormalizeApplicationCategories(t *testing.T) {
	cases := map[string]struct {
		want string
		err  bool
	}{
		"":             {want: ""},
		"5000":         {want: "5000"},
		" 5000, 2000":  {want: "2000,5000"},
		"5000,5000":    {want: "5000"},
		"2000,,5000 ,": {want: "2000,5000"},
		"0":            {want: "0"},
		"abc":          {err: true},
		"5000,-1":      {err: true},
		"5000,2.5":     {err: true},
	}
	for raw, tc := range cases {
		got, err := normalizeApplicationCategories(raw)
		if tc.err {
			if err == nil {
				t.Errorf("normalizeApplicationCategories(%q) = %q, want an error", raw, got)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("normalizeApplicationCategories(%q) = %q, %v, want %q", raw, got, err, tc.want)
		}
	}
}

func TestApplicationValidation(t *testing.T) {
	env, _ := newApplicationEnv(t, false)
	token, cookie := csrfFrom(t, env.handler, "/applications/new")

	recorder := postForm(t, env.handler, "/applications",
		applicationValues(token, "", "ftp://not-http", "", "full"), cookie)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	for _, want := range []string{"http:// or https://", "The API key is required."} {
		if !strings.Contains(body, want) {
			t.Errorf("validation page missing %q", want)
		}
	}
	if apps, err := env.store.ListApplications(context.Background()); err != nil || len(apps) != 0 {
		t.Fatalf("applications after invalid create = %#v, %v", apps, err)
	}
}

func TestApplicationUpdateKeepsAndClearsAPIKey(t *testing.T) {
	env, v := newApplicationEnv(t, false)
	token, cookie := csrfFrom(t, env.handler, "/applications/new")
	recorder := postForm(t, env.handler, "/applications",
		applicationValues(token, "Radarr", "http://radarr:7878", "keep-me", "full"), cookie)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("create status = %d: %s", recorder.Code, recorder.Body.String())
	}
	apps, _ := env.store.ListApplications(context.Background())
	if len(apps) != 1 {
		t.Fatalf("applications = %#v", apps)
	}
	id := apps[0].ID
	editPath := "/applications/" + strconv.FormatInt(id, 10) + "/edit"

	token, cookie = csrfFrom(t, env.handler, editPath)
	values := applicationValues(token, "Radarr rinominato", "http://radarr:7878", "", "add-remove")
	recorder = postForm(t, env.handler, "/applications/"+strconv.FormatInt(id, 10), values, cookie)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("update status = %d: %s", recorder.Code, recorder.Body.String())
	}
	stored, err := env.store.GetApplication(context.Background(), id)
	if err != nil || stored.Name != "Radarr rinominato" || stored.SyncLevel != "add-remove" {
		t.Fatalf("updated application = %#v, %v", stored, err)
	}
	plaintext, err := v.OpenString(string(stored.APIKeyEnc))
	if err != nil || plaintext != "keep-me" {
		t.Fatalf("key after empty update = %q, %v", plaintext, err)
	}

	token, cookie = csrfFrom(t, env.handler, editPath)
	values = applicationValues(token, "Radarr rinominato", "http://radarr:7878", "", "full")
	values.Set("clear_api_key", "on")
	recorder = postForm(t, env.handler, "/applications/"+strconv.FormatInt(id, 10), values, cookie)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("clear status = %d: %s", recorder.Code, recorder.Body.String())
	}
	stored, err = env.store.GetApplication(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.APIKeyEnc) != 0 {
		t.Fatalf("API key was not cleared: %d bytes", len(stored.APIKeyEnc))
	}

	token, cookie = csrfFrom(t, env.handler, editPath)
	recorder = postForm(t, env.handler, "/applications/"+strconv.FormatInt(id, 10)+"/delete",
		url.Values{"csrf": {token}}, cookie)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("delete status = %d: %s", recorder.Code, recorder.Body.String())
	}
	if remaining, err := env.store.ListApplications(context.Background()); err != nil || len(remaining) != 0 {
		t.Fatalf("applications after delete = %#v, %v", remaining, err)
	}
}

func TestApplicationSyncDisabled(t *testing.T) {
	env, v := newApplicationEnv(t, true)
	app := seedApplication(t, env.store, v, "Sonarr spento", "disabled", "some-key")

	token, cookie := csrfFrom(t, env.handler, "/applications")
	recorder := postForm(t, env.handler, "/applications/"+strconv.FormatInt(app.ID, 10)+"/sync",
		url.Values{"csrf": {token}}, cookie)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("sync status = %d: %s", recorder.Code, recorder.Body.String())
	}
	page := get(t, env.handler, recorder.Header().Get("Location"))
	if page.Code != http.StatusOK {
		t.Fatalf("flash page = %d: %s", page.Code, page.Body.String())
	}
	body := page.Body.String()
	for _, want := range []string{"application disabled", "0 added", "0 updated", "0 deleted", "0 skipped"} {
		if !strings.Contains(body, want) {
			t.Errorf("sync flash missing %q", want)
		}
	}
}

func TestApplicationSyncWithoutService(t *testing.T) {
	env, v := newApplicationEnv(t, false)
	app := seedApplication(t, env.store, v, "Sonarr", "full", "some-key")

	token, cookie := csrfFrom(t, env.handler, "/applications")
	recorder := postForm(t, env.handler, "/applications/"+strconv.FormatInt(app.ID, 10)+"/sync",
		url.Values{"csrf": {token}}, cookie)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("sync status = %d: %s", recorder.Code, recorder.Body.String())
	}
	page := get(t, env.handler, recorder.Header().Get("Location"))
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "not configured") {
		t.Fatalf("missing degradation message: %d\n%s", page.Code, page.Body.String())
	}

	token, cookie = csrfFrom(t, env.handler, "/applications")
	recorder = postForm(t, env.handler, "/applications/sync", url.Values{"csrf": {token}}, cookie)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("sync all status = %d: %s", recorder.Code, recorder.Body.String())
	}
	page = get(t, env.handler, recorder.Header().Get("Location"))
	if !strings.Contains(page.Body.String(), "not configured") {
		t.Fatal("sync all did not degrade with a clear message")
	}
}

// TestApplicationSnapshotsPanel renders the snapshot list and the restore
// action in every language, with the confirmation text in place.
func TestApplicationSnapshotsPanel(t *testing.T) {
	env, v := newApplicationEnv(t, false)
	app := seedApplication(t, env.store, v, "Sonarr", "full", "key")
	snapshotID, err := env.store.CreateAppSnapshot(context.Background(), store.AppSnapshot{
		AppID: app.ID, Reason: store.SnapshotReasonSync, IndexerCount: 3,
		Payload: `[{"name":"indexarr · A"}]`,
	})
	if err != nil {
		t.Fatalf("CreateAppSnapshot: %v", err)
	}
	app.TagFilter = "tv, 4k"
	if err := env.store.UpdateApplication(context.Background(), app); err != nil {
		t.Fatalf("UpdateApplication: %v", err)
	}

	for _, lang := range []string{"en", "it", "de", "fr", "es", "pl", "zh"} {
		body := get(t, env.handler, "/applications?lang="+lang).Body.String()
		for _, want := range []string{
			"snapshots", "/applications/" + strconvI(app.ID) + "/restore/" + strconvI(snapshotID), "name=\"csrf\"",
			"tv, 4k", "data-confirm=",
		} {
			if !strings.Contains(body, want) {
				t.Errorf("%s: applications page missing %q", lang, want)
			}
		}
		if strings.Contains(body, ">applications.") || strings.Contains(body, ">snapshot.") ||
			strings.Contains(body, ">tip.") {
			t.Errorf("%s: unresolved i18n key in the applications page", lang)
		}
	}

	form := get(t, env.handler, "/applications/"+strconvI(app.ID)+"/edit").Body.String()
	for _, want := range []string{`name="tag_filter"`, `value="tv, 4k"`, "Tags"} {
		if !strings.Contains(form, want) {
			t.Errorf("application form missing %q", want)
		}
	}
}

// TestApplicationSnapshotActionsGuarded covers the two snapshot endpoints when
// the sync service is not configured and when the snapshot id is invalid.
func TestApplicationSnapshotActionsGuarded(t *testing.T) {
	env, v := newApplicationEnv(t, false)
	app := seedApplication(t, env.store, v, "Sonarr", "full", "key")
	token, cookie := csrfFrom(t, env.handler, "/applications")

	for _, path := range []string{
		"/applications/" + strconvI(app.ID) + "/snapshot",
		"/applications/" + strconvI(app.ID) + "/restore/1",
		"/applications/" + strconvI(app.ID) + "/restore/not-a-number",
	} {
		recorder := postForm(t, env.handler, path, url.Values{"csrf": {token}}, cookie)
		if recorder.Code != http.StatusSeeOther {
			t.Fatalf("POST %s = %d, want a redirect", path, recorder.Code)
		}
		location := recorder.Header().Get("Location")
		if !strings.HasPrefix(location, "/applications") {
			t.Errorf("POST %s Location = %q", path, location)
		}
	}
}

func TestNormalizeTagFilter(t *testing.T) {
	cases := map[string]string{
		"":              "",
		" tv, TV , 4k ": "tv, 4k",
		"tv,,anime,,tv": "tv, anime",
		"4K":            "4K",
		" tv ,  tv  ":   "tv",
		"one,two,three": "one, two, three",
	}
	for raw, want := range cases {
		if got := normalizeTagFilter(raw); got != want {
			t.Errorf("normalizeTagFilter(%q) = %q, want %q", raw, got, want)
		}
	}
	long := strings.Repeat("à", 400) + "," + strings.Repeat("b", 400)
	got := normalizeTagFilter(long)
	if len([]rune(got)) > 200 {
		t.Errorf("filter has %d runes, want at most 200", len([]rune(got)))
	}
	if !utf8.ValidString(got) {
		t.Error("filter is not valid UTF-8")
	}
}
