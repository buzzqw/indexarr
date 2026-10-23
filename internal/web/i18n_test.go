package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
		{"/indexers?lang=it", []string{"Indexer", "Nativi", "Ponte legacy", "Nuovo indexer nativo"}},
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
	body := get(t, env.handler, "/indexers?tab=legacy").Body.String()
	aside := body[strings.Index(body, `<aside class="sidebar"`):strings.Index(body, "</aside>")]
	if !strings.Contains(aside, `href="/indexers" class="active" aria-current="page"`) {
		t.Error("Indexers is not active on /indexers?tab=legacy")
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
