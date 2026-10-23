package web

import (
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/buzzqw/indexarr/internal/i18n"
)

func TestDefaultLanguageIsEnglish(t *testing.T) {
	env := newTestEnv(t)
	body := get(t, env.handler, "/").Body.String()

	if !strings.Contains(body, `<html lang="en" dir="ltr">`) {
		t.Error("the default document language is not English")
	}
	if !strings.Contains(body, `<title>Overview · indexarr</title>`) {
		t.Error("the default page title is not in English")
	}
	for _, want := range []string{
		"Skip to content", "Overview", "Manage", "System",
		"Search releases", "Language", "English", "Italiano",
		`class="app-shell"`, `class="sidebar"`,
		`action="/releases"`, `name="q"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("English shell missing %q", want)
		}
	}
	for _, unwanted := range []string{"Panoramica", "Navigazione principale", "Cerca release"} {
		if strings.Contains(body, unwanted) {
			t.Errorf("English shell still contains Italian text %q", unwanted)
		}
	}
	for _, code := range []string{"en", "it", "es", "fr", "de"} {
		if !strings.Contains(body, `href="/language/`+code+`"`) {
			t.Errorf("language selector missing %q", code)
		}
	}
	if !strings.Contains(body, `href="/language/en" lang="en" hreflang="en" class="active" aria-current="true"`) {
		t.Error("the current language is not marked in the selector")
	}
}

func TestLanguageRouteSetsCookieAndRedirectsToReferer(t *testing.T) {
	env := newTestEnv(t)
	request := httptest.NewRequest(http.MethodGet, "/language/it", nil)
	request.Header.Set("Referer", "http://example.com/indexers?tab=native")
	recorder := httptest.NewRecorder()
	env.handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("GET /language/it = %d: %s", recorder.Code, recorder.Body.String())
	}
	if location := recorder.Header().Get("Location"); location != "/indexers?tab=native" {
		t.Errorf("Location = %q", location)
	}
	var languageCookieValue string
	for _, cookie := range recorder.Result().Cookies() {
		if cookie.Name != languageCookie {
			continue
		}
		languageCookieValue = cookie.Value
		if cookie.Path != "/" {
			t.Errorf("cookie path = %q", cookie.Path)
		}
		if cookie.MaxAge != languageCookieMaxAge {
			t.Errorf("cookie MaxAge = %d, want %d", cookie.MaxAge, languageCookieMaxAge)
		}
		if cookie.SameSite != http.SameSiteLaxMode {
			t.Errorf("cookie SameSite = %v", cookie.SameSite)
		}
	}
	if languageCookieValue != "it" {
		t.Fatalf("indexarr_lang cookie = %q", languageCookieValue)
	}

	// The next request without ?lang uses the saved cookie.
	next := httptest.NewRequest(http.MethodGet, "/", nil)
	next.AddCookie(&http.Cookie{Name: languageCookie, Value: languageCookieValue})
	page := httptest.NewRecorder()
	env.handler.ServeHTTP(page, next)
	body := page.Body.String()
	if !strings.Contains(body, `<html lang="it" dir="ltr">`) {
		t.Error("the saved cookie did not select Italian")
	}
	if !strings.Contains(body, "Panoramica") || !strings.Contains(body, "Cerca release") {
		t.Error("the Italian page was not rendered from the catalog")
	}
}

func TestLanguageQueryWinsWithoutCookie(t *testing.T) {
	env := newTestEnv(t)

	request := httptest.NewRequest(http.MethodGet, "/?lang=it", nil)
	request.Header.Set("Accept-Language", "fr-FR,fr;q=0.9")
	recorder := httptest.NewRecorder()
	env.handler.ServeHTTP(recorder, request)
	body := recorder.Body.String()
	if !strings.Contains(body, `<html lang="it" dir="ltr">`) ||
		!strings.Contains(body, "Panoramica") {
		t.Error("?lang=it did not select Italian")
	}

	french := get(t, env.handler, "/?lang=fr").Body.String()
	if !strings.Contains(french, `<html lang="fr" dir="ltr">`) ||
		!strings.Contains(french, "Système") {
		t.Error("?lang=fr did not select French")
	}

	unknown := get(t, env.handler, "/?lang=xx-YY").Body.String()
	if !strings.Contains(unknown, `<html lang="en" dir="ltr">`) ||
		!strings.Contains(unknown, "Overview") {
		t.Error("an unknown language did not fall back to English")
	}
}

func TestLanguageCookiePrecedence(t *testing.T) {
	env := newTestEnv(t)

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("Accept-Language", "fr-FR")
	request.AddCookie(&http.Cookie{Name: languageCookie, Value: "it"})
	recorder := httptest.NewRecorder()
	env.handler.ServeHTTP(recorder, request)
	if !strings.Contains(recorder.Body.String(), "Panoramica") {
		t.Error("the cookie did not win over Accept-Language")
	}

	request = httptest.NewRequest(http.MethodGet, "/?lang=en", nil)
	request.AddCookie(&http.Cookie{Name: languageCookie, Value: "it"})
	recorder = httptest.NewRecorder()
	env.handler.ServeHTTP(recorder, request)
	if !strings.Contains(recorder.Body.String(), "Overview") {
		t.Error("?lang=en did not win over the cookie")
	}
}

func TestItalianCatalogOnOwnedPages(t *testing.T) {
	env := newTestEnv(t)
	cases := []struct {
		path string
		want []string
	}{
		{"/indexers?lang=it", []string{"Indexer", "Nuovo indexer nativo"}},
		{"/definitions?lang=it", []string{"Definizioni", "Catalogo built-in", "Azione"}},
		{"/applications?lang=it", []string{"Applicazioni", "Livelli di sincronizzazione", "Sincronizza tutte"}},
		{"/import?lang=it", []string{"Importa", "Anteprima, nessuna modifica", "Genera piano da Prowlarr"}},
		{"/activity?lang=it", []string{"Attività", "Esecuzioni pianificate", "Log"}},
	}
	for _, tc := range cases {
		body := get(t, env.handler, tc.path).Body.String()
		if !strings.Contains(body, `<html lang="it" dir="ltr">`) {
			t.Errorf("%s is not marked as Italian", tc.path)
		}
		for _, want := range tc.want {
			if !strings.Contains(body, want) {
				t.Errorf("%s missing Italian %q", tc.path, want)
			}
		}
	}
}

func TestUnknownLanguageRouteRedirectsHome(t *testing.T) {
	env := newTestEnv(t)
	request := httptest.NewRequest(http.MethodGet, "/language/xx", nil)
	recorder := httptest.NewRecorder()
	env.handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusFound {
		t.Fatalf("GET /language/xx = %d, want 302", recorder.Code)
	}
	if location := recorder.Header().Get("Location"); location != "/" {
		t.Errorf("Location = %q, want /", location)
	}
	for _, cookie := range recorder.Result().Cookies() {
		if cookie.Name == languageCookie {
			t.Error("an unknown language must not be saved")
		}
	}
}

func TestSidebarActiveSection(t *testing.T) {
	env := newTestEnv(t)
	body := get(t, env.handler, "/indexers").Body.String()
	aside := body[strings.Index(body, `<aside class="sidebar"`):strings.Index(body, "</aside>")]
	if !strings.Contains(aside, `href="/indexers" class="active" aria-current="page"`) {
		t.Error("Indexers is not active on /indexers")
	}
	if strings.Count(aside, `href="/indexers"`) != 1 {
		t.Error("the sidebar lists Indexers more than once")
	}

	activity := get(t, env.handler, "/activity?tab=logs").Body.String()
	aside = activity[strings.Index(activity, `<aside class="sidebar"`):strings.Index(activity, "</aside>")]
	if !strings.Contains(aside, `href="/activity" class="active" aria-current="page"`) {
		t.Error("Activity is not active on /activity?tab=logs")
	}
}

func TestDefinitionsTableIDAndShortAction(t *testing.T) {
	env := newNativeEnv(t)
	body := get(t, env.handler, "/definitions").Body.String()
	for _, want := range []string{
		`class="definitions-table"`,
		`>Create indexer</a>`,
		`title="Create an indexer from this definition"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("definitions table missing %q", want)
		}
	}
	css := get(t, env.handler, "/static/app.css").Body.String()
	if !strings.Contains(css, ".definitions-table td:first-child code { white-space: nowrap; }") {
		t.Error("the definitions ID column is not kept on one line")
	}
}

var translatableAttribute = regexp.MustCompile(`(?i)\b(?:placeholder|aria-label|title|data-confirm|alt)="([^"]*)"`)

// TestReleaseAndSettingsTemplatesUseCatalog guards the i18n work on the
// releases, release and settings pages: every {{T}} key must exist and no
// user-visible text may stay outside the catalog.
//
// Limits of the scan: it checks text nodes and the attributes that render text
// (placeholder, aria-label, title, data-confirm, alt). It deliberately ignores
// value="" (form values stay untranslated), class, id, name, href, action and
// data-* hooks, and it cannot see strings assembled in handlers. Backend
// validation errors built from field names remain technical by design.
func TestReleaseAndSettingsTemplatesUseCatalog(t *testing.T) {
	files := []string{"releases", "releases_results", "release", "settings"}
	known := make(map[string]bool, len(i18n.Keys()))
	for _, key := range i18n.Keys() {
		known[key] = true
	}
	allowed := map[string]bool{"indexarr": true}

	for _, name := range files {
		path := "templates/" + name + ".html"
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		content := string(data)

		for _, match := range templateKeyAction.FindAllStringSubmatch(content, -1) {
			if !known[match[1]] {
				t.Errorf("%s uses missing catalog key %q", path, match[1])
			}
		}

		// Remove executable blocks, comments and actions; what remains is the
		// static markup that must not carry visible prose.
		static := regexp.MustCompile(`(?s)<script.*?</script>`).ReplaceAllString(content, "")
		static = regexp.MustCompile(`(?s)<style.*?</style>`).ReplaceAllString(static, "")
		static = regexp.MustCompile(`(?s)<!--.*?-->`).ReplaceAllString(static, "")
		static = templateTextAction.ReplaceAllString(static, " ")

		var literal []string
		for _, match := range translatableAttribute.FindAllStringSubmatch(static, -1) {
			for _, token := range templateToken.FindAllString(match[1], -1) {
				if !allowed[token] {
					literal = append(literal, token)
				}
			}
		}
		text := regexp.MustCompile(`<[^>]+>`).ReplaceAllString(static, "\n")
		for _, token := range templateToken.FindAllString(text, -1) {
			if !allowed[token] {
				literal = append(literal, token)
			}
		}
		if len(literal) > 0 {
			sort.Strings(literal)
			t.Errorf("%s has user-visible text outside the catalog: %v", path, literal)
		}
	}
}

// TestReleasesIndexerAndReleaseTypeAreDistinct covers V6: the source filter and
// column ("Indexer") must not collapse into the source-quality label.
func TestReleasesIndexerAndReleaseTypeAreDistinct(t *testing.T) {
	for _, lang := range i18n.Supported() {
		indexer := i18n.T(lang, "releases.indexer")
		releaseType := i18n.T(lang, "releases.releaseType")
		if indexer == "" || releaseType == "" || indexer == releaseType {
			t.Errorf("%s: indexer=%q releaseType=%q must be distinct and non-empty",
				lang, indexer, releaseType)
		}
	}
}

// TestReleaseAndSettingsPagesTranslate checks the owned pages in Italian and
// verifies that the initial settings HTML never carries the primary API key.
func TestReleaseAndSettingsPagesTranslate(t *testing.T) {
	env := newTestEnv(t)

	settingsBody := get(t, env.handler, "/settings?lang=it").Body.String()
	for _, want := range []string{
		`<html lang="it" dir="ltr">`,
		"Impostazioni", "Chiave API", "Client di download",
		"Salva impostazioni", "Backup automatici",
	} {
		if !strings.Contains(settingsBody, want) {
			t.Errorf("/settings?lang=it missing %q", want)
		}
	}
	if env.apiKey == "" {
		t.Fatal("test env has no API key")
	}
	if strings.Contains(settingsBody, env.apiKey) || strings.Contains(settingsBody, "apikey="+env.apiKey) {
		t.Fatal("the primary API key is present in the initial /settings HTML")
	}
	for _, want := range []string{"••••••••", "/settings/secrets/apikey", "/settings/secrets/torznab"} {
		if !strings.Contains(settingsBody, want) {
			t.Errorf("/settings?lang=it missing masked secret marker %q", want)
		}
	}

	releasesBody := get(t, env.handler, "/releases?lang=it").Body.String()
	for _, want := range []string{
		`<html lang="it" dir="ltr">`,
		"Risoluzione", "Tipo di release", "Indexer", "Cerca", "Salva questa ricerca come",
	} {
		if !strings.Contains(releasesBody, want) {
			t.Errorf("/releases?lang=it missing %q", want)
		}
	}
}
