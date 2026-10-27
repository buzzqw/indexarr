package web

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/buzzqw/indexarr/internal/backup"
	"github.com/buzzqw/indexarr/internal/store"
)

func seedHomeDefinition(t *testing.T, env *testEnv, id string) {
	t.Helper()
	if err := env.store.UpsertDefinition(context.Background(), store.Definition{
		ID: id, SchemaVersion: 1, Family: "torznab", State: "verificata",
		Origin: "local", Author: "test",
		Body: "schema: 1\nconnector: torznab\n", Hash: strings.Repeat("b", 64),
	}); err != nil {
		t.Fatalf("UpsertDefinition: %v", err)
	}
}

func seedBrokenIndexer(t *testing.T, env *testEnv, slug, message string) int64 {
	t.Helper()
	ctx := context.Background()
	seedHomeDefinition(t, env, slug+"-def")
	id, err := env.store.CreateIndexer(ctx, store.Indexer{
		DefinitionID: slug + "-def", Protocol: "torrent", Slug: slug,
		Name: slug, Enabled: true, ConfigJSON: `{}`,
	})
	if err != nil {
		t.Fatalf("CreateIndexer: %v", err)
	}
	if err := env.store.UpsertIndexerState(ctx, store.IndexerState{
		IndexerID: id, Circuit: "closed", LastError: message,
	}); err != nil {
		t.Fatalf("UpsertIndexerState: %v", err)
	}
	return id
}

// TestHomeStatusMatchesBadge is the §4.1/§8 acceptance test: the home panel and
// the sidebar badge must read the same value.
func TestHomeStatusMatchesBadge(t *testing.T) {
	env := newTestEnv(t)
	seedBrokenIndexer(t, env, "rate-limited", "HTTP 429: too many requests")

	status := env.server.HomeStatus(context.Background())
	if status.Level != "warn" || status.ProblemIndexers != 1 || len(status.Problems) != 1 {
		t.Fatalf("HomeStatus = %#v", status)
	}
	body := get(t, env.handler, "/").Body.String()
	badge := regexp.MustCompile(`<span class="nav-badge"[^>]*>(\d+)</span>`).FindStringSubmatch(body)
	if badge == nil {
		t.Fatal("no navigation badge with a current problem")
	}
	if badge[1] != strconv.Itoa(status.ProblemIndexers) {
		t.Errorf("badge = %s, HomeStatus problem indexers = %d", badge[1], status.ProblemIndexers)
	}
	for _, want := range []string{"indexers have problems", "rate-limited", "limiting requests"} {
		if !strings.Contains(body, want) {
			t.Errorf("home missing %q", want)
		}
	}
}

// TestHomeNextStepIsSingleAndOrdered checks the documented order and that the
// box never shows more than one action.
func TestHomeNextStepIsSingleAndOrdered(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	settings, _ := env.store.LoadSettings(ctx)
	status := HomeStatus{Level: "ok", ActiveIndexers: 2, Apps: 1}

	step := env.server.nextStep(ctx, status, settings, 1, 0)
	if step == nil || step.Key != "home.next.backup" {
		t.Fatalf("first step = %#v, want backup (the connect card already proposes the address)", step)
	}

	settings.PublicURL = "http://192.168.1.50:8788"
	status.Apps = 0
	step = env.server.nextStep(ctx, status, settings, 1, 0)
	if step == nil || step.Key != "home.next.app" {
		t.Fatalf("second step = %#v, want app", step)
	}
	// Apps configured by hand already search: the hint asks to register
	// them instead of claiming they cannot see the indexers.
	step = env.server.nextStep(ctx, status, settings, 1, 42)
	if step == nil || step.Key != "home.next.appManual" {
		t.Fatalf("manual-app step = %#v, want appManual", step)
	}
	status.Apps = 1
	step = env.server.nextStep(ctx, status, settings, 0, 0)
	if step == nil || step.Key != "home.next.client" {
		t.Fatalf("third step = %#v, want download client", step)
	}
	// An indexer-only first run points to the first indexer, never to a client.
	status.ActiveIndexers = 0
	step = env.server.nextStep(ctx, status, settings, 0, 0)
	if step == nil || step.Key != "home.next.indexer" {
		t.Fatalf("first-run step = %#v, want indexer", step)
	}

	// With everything present there is no box at all.
	if _, err := env.store.CreateDownloadClient(ctx, store.DownloadClient{
		Name: "qBittorrent", Kind: "qbittorrent", Protocol: "torrent",
		URL: "http://127.0.0.1:8080", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	// A backup exists, so the last step is satisfied too.
	if _, err := backup.Create(ctx, env.store, env.store.Path()); err != nil {
		t.Fatal(err)
	}
	status = HomeStatus{Level: "ok", ActiveIndexers: 1, Apps: 1}
	if step := env.server.nextStep(ctx, status, settings, 1, 0); step != nil {
		t.Errorf("unexpected next step with everything configured: %#v", step)
	}
}

// TestHomeHasNoTechnicalCounters protects the rule "one number, one source".
func TestHomeHasNoTechnicalCounters(t *testing.T) {
	env := newTestEnv(t)
	body := get(t, env.handler, "/").Body.String()
	for _, unwanted := range []string{"Errors (24h)", "Database size", "Definitions"} {
		if strings.Contains(body, unwanted) {
			t.Errorf("home still shows %q", unwanted)
		}
	}
	for _, want := range []string{"Last 24 hours", "Next step", "Connect your apps"} {
		if !strings.Contains(body, want) {
			t.Errorf("home missing %q", want)
		}
	}
}

func TestDownloadClientsProtocolRule(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	torrentID, err := env.store.CreateDownloadClient(ctx, store.DownloadClient{
		Name: "qBittorrent", Kind: "qbittorrent", Protocol: "torrent",
		URL: "http://127.0.0.1:8080", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	usenetID, err := env.store.CreateDownloadClient(ctx, store.DownloadClient{
		Name: "SABnzbd", Kind: "sabnzbd", Protocol: "usenet",
		URL: "http://127.0.0.1:8085", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	clients, err := env.store.ListDownloadClients(ctx)
	if err != nil || len(clients) != 2 {
		t.Fatalf("clients = %#v, %v", clients, err)
	}
	if !clients[0].IsDefault || clients[1].IsDefault {
		t.Errorf("default client not assigned to the first row: %#v", clients)
	}
	if err := env.store.SetDefaultDownloadClient(ctx, usenetID); err != nil {
		t.Fatal(err)
	}
	torrent, err := env.store.DownloadClientForProtocol(ctx, "torrent")
	if err != nil || torrent.ID != torrentID {
		t.Errorf("torrent client = %#v, %v", torrent, err)
	}
	usenet, err := env.store.DownloadClientForProtocol(ctx, "usenet")
	if err != nil || usenet.ID != usenetID {
		t.Errorf("usenet client = %#v, %v", usenet, err)
	}
	// The web resolver applies the same rule.
	_, name, err := env.server.resolveDownloadClient(ctx, "usenet")
	if err != nil || name != "SABnzbd" {
		t.Errorf("resolved usenet client = %q, %v", name, err)
	}

	page := get(t, env.handler, "/applications").Body.String()
	for _, want := range []string{"Download clients", "qBittorrent", "SABnzbd", "Torrent", "Usenet"} {
		if !strings.Contains(page, want) {
			t.Errorf("App page missing %q", want)
		}
	}
}

func TestDownloadClientFormLifecycle(t *testing.T) {
	env := newTestEnv(t)
	token, cookie := csrfFrom(t, env.handler, "/download-clients/new")
	recorder := postForm(t, env.handler, "/download-clients", url.Values{
		"csrf":     {token},
		"name":     {"Transmission"},
		"kind":     {"transmission"},
		"protocol": {"torrent"},
		"url":      {"http://127.0.0.1:9091"},
		"username": {"user"},
		"password": {"synthetic-client-password"},
		"enabled":  {"on"},
	}, cookie)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("create = %d: %s", recorder.Code, recorder.Body.String())
	}
	clients, _ := env.store.ListDownloadClients(context.Background())
	if len(clients) != 1 || clients[0].PasswordEnc == "" {
		t.Fatalf("clients = %#v", clients)
	}
	if strings.Contains(clients[0].PasswordEnc, "synthetic-client-password") {
		t.Fatal("the password was stored in plaintext")
	}
	page := get(t, env.handler, "/download-clients/"+strconvI(clients[0].ID)+"/edit").Body.String()
	for _, want := range []string{"Edit download client", "secret stored", "Change"} {
		if !strings.Contains(page, want) {
			t.Errorf("edit form missing %q", want)
		}
	}
	if strings.Contains(page, "synthetic-client-password") {
		t.Fatal("the edit form contains the stored password")
	}
	deleteRecorder := postForm(t, env.handler,
		"/download-clients/"+strconvI(clients[0].ID)+"/delete",
		url.Values{"csrf": {token}}, cookie)
	if deleteRecorder.Code != http.StatusSeeOther {
		t.Fatalf("delete = %d", deleteRecorder.Code)
	}
	if remaining, _ := env.store.ListDownloadClients(context.Background()); len(remaining) != 0 {
		t.Errorf("clients after delete = %#v", remaining)
	}
}

// TestLegacyDownloadClientRemains the single client in Settings keeps working
// until the operator adds a row in App.
func TestLegacyDownloadClientRemains(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	settings, _ := env.store.LoadSettings(ctx)
	settings.DownloadClient = "qbittorrent"
	settings.DownloadClientURL = "http://127.0.0.1:8080"
	if err := env.store.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	_, name, err := env.server.resolveDownloadClient(ctx, "torrent")
	if err != nil || name != "qBittorrent" {
		t.Errorf("legacy client = %q, %v", name, err)
	}
}

func TestOldURLsRedirectToNewPositions(t *testing.T) {
	env := newTestEnv(t)
	cases := map[string]string{
		"/sources":     "/indexers",
		"/sources/new": "/indexers",
		"/runs":        "/activity?",
		"/maintenance": "/settings?section=maintenance",
		"/definitions": "/settings?section=advanced",
	}
	for path, want := range cases {
		recorder := get(t, env.handler, path)
		if recorder.Code != http.StatusFound {
			t.Errorf("%s = %d, want 302", path, recorder.Code)
			continue
		}
		if location := recorder.Header().Get("Location"); !strings.Contains(location, want) {
			t.Errorf("%s -> %q, want %q", path, location, want)
		}
	}
	// /logs is now the Logs section itself: the old Activity tab remains at
	// /activity?tab=logs, and the new page answers 200.
	if recorder := get(t, env.handler, "/logs"); recorder.Code != http.StatusOK {
		t.Errorf("/logs = %d, want 200", recorder.Code)
	}
	// The definitions detail and editor stay reachable from Advanced.
	for _, path := range []string{"/definitions/new", "/definitions/example-html"} {
		if recorder := get(t, env.handler, path); recorder.Code != http.StatusOK {
			t.Errorf("%s = %d, want 200", path, recorder.Code)
		}
	}
}

func TestSettingsSectionsSaveIndependently(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	settings, _ := env.store.LoadSettings(ctx)
	settings.DefaultIntervalSeconds = 7200
	settings.Workers = 3
	if err := env.store.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}

	token, cookie := csrfFrom(t, env.handler, "/settings?section=general")
	recorder := postForm(t, env.handler, "/settings", url.Values{
		"csrf": {token}, "section": {"general"},
		"public_url": {"http://192.168.1.50:8788"},
	}, cookie)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("general save = %d: %s", recorder.Code, recorder.Body.String())
	}
	updated, _ := env.store.LoadSettings(ctx)
	if updated.PublicURL != "http://192.168.1.50:8788" {
		t.Errorf("public URL = %q", updated.PublicURL)
	}
	if updated.DefaultIntervalSeconds != 7200 || updated.Workers != 3 {
		t.Errorf("another section was overwritten: %#v", updated)
	}

	sections := map[string]string{
		"general": "public_url", "security": "settings/secrets/apikey",
		"network": "flaresolverr_url", "updates": "history_retention_days",
		"rules": "rules", "backup": "backup_interval_hours",
		"maintenance": "Database report", "advanced": "definitions",
	}
	for section, want := range sections {
		body := get(t, env.handler, "/settings?section="+section).Body.String()
		if !strings.Contains(body, want) {
			t.Errorf("settings section %s missing %q", section, want)
		}
		if !strings.Contains(body, "settings-nav") {
			t.Errorf("settings section %s has no section navigation", section)
		}
	}
	// Fields of other sections are not rendered on the same page.
	general := get(t, env.handler, "/settings?section=general").Body.String()
	if strings.Contains(general, `name="workers"`) || strings.Contains(general, `name="host_rate"`) {
		t.Error("general section shows advanced/network fields")
	}
}

func TestSetupWizardSkipAndReopen(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	settings, _ := env.store.LoadSettings(ctx)
	settings.SetupDismissed = false
	if err := env.store.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}

	body := get(t, env.handler, "/").Body.String()
	for _, want := range []string{"Welcome to indexarr", "Step 1 of 4", "Skip setup", "Public address"} {
		if !strings.Contains(body, want) {
			t.Errorf("wizard missing %q", want)
		}
	}
	for _, step := range []string{"2", "3", "4"} {
		page := get(t, env.handler, "/setup?step="+step).Body.String()
		if !strings.Contains(page, "Step "+step+" of 4") {
			t.Errorf("setup step %s not rendered", step)
		}
	}
	if !strings.Contains(get(t, env.handler, "/setup?step=3").Body.String(), "Choose the sites") {
		t.Error("step 3 does not offer the site list")
	}

	token, cookie := csrfFrom(t, env.handler, "/setup")
	skip := postForm(t, env.handler, "/setup/skip", url.Values{"csrf": {token}}, cookie)
	if skip.Code != http.StatusSeeOther {
		t.Fatalf("skip = %d", skip.Code)
	}
	if strings.Contains(get(t, env.handler, "/").Body.String(), "Welcome to indexarr") {
		t.Error("the wizard still replaces Home after skipping")
	}

	reopen := postForm(t, env.handler, "/settings/setup/reopen",
		url.Values{"csrf": {token}}, cookie)
	if reopen.Code != http.StatusSeeOther {
		t.Fatalf("reopen = %d", reopen.Code)
	}
	if !strings.Contains(get(t, env.handler, "/").Body.String(), "Welcome to indexarr") {
		t.Error("the wizard did not reopen")
	}

	// Step 1 saves the address and moves on.
	address := postForm(t, env.handler, "/setup/address", url.Values{
		"csrf": {token}, "public_url": {"http://192.168.1.50:8788"},
	}, cookie)
	if address.Code != http.StatusSeeOther || !strings.Contains(address.Header().Get("Location"), "step=2") {
		t.Fatalf("address = %d %q", address.Code, address.Header().Get("Location"))
	}
	saved, _ := env.store.LoadSettings(ctx)
	if saved.PublicURL != "http://192.168.1.50:8788" {
		t.Errorf("wizard did not save the address: %q", saved.PublicURL)
	}
}

// TestHomeStatusTurnsRedOnDatabaseFailure documents the three-level scale.
func TestHomeStatusTurnsRedOnDatabaseFailure(t *testing.T) {
	env := newTestEnv(t)
	if err := env.store.Close(); err != nil {
		t.Fatal(err)
	}
	status := env.server.HomeStatus(context.Background())
	if status.Level != "danger" {
		t.Fatalf("level with a closed database = %q, want danger", status.Level)
	}
}

// TestAmuleEd2kClient covers the third protocol: aMule is offered as a client,
// it requires the Web UI password, and an eD2k release resolves to it.
func TestAmuleEd2kClient(t *testing.T) {
	env := newTestEnv(t)
	token, cookie := csrfFrom(t, env.handler, "/download-clients/new")
	missing := postForm(t, env.handler, "/download-clients", url.Values{
		"csrf":     {token},
		"name":     {"aMule"},
		"kind":     {"amule"},
		"protocol": {"ed2k"},
		"url":      {"http://127.0.0.1:4711"},
		"enabled":  {"on"},
	}, cookie)
	if missing.Code != http.StatusBadRequest || !strings.Contains(missing.Body.String(), "aMule") {
		t.Fatalf("missing password = %d: %s", missing.Code, missing.Body.String())
	}
	created := postForm(t, env.handler, "/download-clients", url.Values{
		"csrf":     {token},
		"name":     {"aMule"},
		"kind":     {"amule"},
		"protocol": {"ed2k"},
		"url":      {"http://127.0.0.1:4711"},
		"password": {"synthetic-amule-password"},
		"enabled":  {"on"},
	}, cookie)
	if created.Code != http.StatusSeeOther {
		t.Fatalf("create = %d: %s", created.Code, created.Body.String())
	}
	cfg, name, err := env.server.resolveDownloadClient(context.Background(), "ed2k")
	if err != nil || name != "aMule" || cfg.Kind != "amule" {
		t.Errorf("resolved eD2k client = %q %q, %v", name, cfg.Kind, err)
	}
}

// An eD2k release never falls back to a torrent client: the ed2k:// link is
// not something qBittorrent or Transmission can add.
func TestEd2kReleaseDoesNotFallBackToTorrentClient(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	if _, err := env.store.CreateDownloadClient(ctx, store.DownloadClient{
		Name: "qBittorrent", Kind: "qbittorrent", Protocol: "torrent",
		URL: "http://127.0.0.1:8080", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := env.server.resolveDownloadClient(ctx, "ed2k"); err == nil {
		t.Fatal("eD2k release resolved to a torrent client")
	}
}

// A stored eD2k topic that resolves to a magnet goes to a torrent client, not
// to aMule.
func TestEd2kTopicResolvedToMagnetBecomesTorrent(t *testing.T) {
	env := newTestEnv(t)
	const magnet = "magnet:?xt=urn:btih:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const link = "ed2k://|file|Film.mkv|700|0123456789abcdef0123456789abcdef|/"
	answer := magnet
	env.server.downloadResolver = func(context.Context, int64) ([]byte, string, string, error) {
		return []byte(answer), "", "", nil
	}
	release := env.server.openEd2kTopic(context.Background(), store.Release{ID: 1, Protocol: "ed2k"})
	if release.Protocol != "torrent" || release.Magnet != magnet {
		t.Errorf("magnet topic = %q %q, want a torrent release", release.Protocol, release.Magnet)
	}
	answer = link
	release = env.server.openEd2kTopic(context.Background(), store.Release{ID: 1, Protocol: "ed2k"})
	if release.Protocol != "ed2k" || release.Ed2k != link {
		t.Errorf("eD2k topic = %q %q", release.Protocol, release.Ed2k)
	}
}
