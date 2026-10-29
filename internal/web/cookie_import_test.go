package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/buzzqw/indexarr/internal/cookiepair"
)

const cookieDefinition = `schema: 1
meta:
  id: cookie-src
  name: Cookie source
  type: private
  protocol: torrent
  urls: [https://iptorrents.example/]
settings:
  - {name: cookie, type: password, label: Session cookie, required: true}
capabilities:
  search: [q]
search:
  request:
    get: /t
  response:
    type: html
    rows: {selector: tr}
    fields:
      title: {selector: a}
    required: [title]
`

func TestBrowserCookieImportPairsOnce(t *testing.T) {
	cookiepair.Reset()
	env := newNativeEnv(t)
	seedDefinition(t, env.store, "cookie-src", "", cookieDefinition)
	id := seedPhase2Indexer(t, env.store, "cookie-src", "cookie-src", true,
		`{"url":"https://iptorrents.example/"}`)

	token, _, err := cookiepair.Issue(id)
	if err != nil {
		t.Fatal(err)
	}
	body := `{"cookie":"uid=synthetic; pass=synthetic","user_agent":"SyntheticBrowser/1.0"}`
	request := httptest.NewRequest(http.MethodPost, "/pair", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Indexarr-Pairing", token)
	recorder := httptest.NewRecorder()
	env.handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("pair = %d: %s", recorder.Code, recorder.Body.String())
	}

	secret, err := env.store.GetSecret(context.Background(), id, "cookie")
	if err != nil {
		t.Fatalf("GetSecret: %v", err)
	}
	plain, err := env.vault.Open(secret.Nonce, secret.Ciphertext)
	if err != nil {
		t.Fatalf("vault.Open: %v", err)
	}
	if plain != "uid=synthetic; pass=synthetic" {
		t.Errorf("stored cookie = %q", plain)
	}
	indexer, err := env.store.GetIndexer(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if got := publicConfigMap(indexer.ConfigJSON)["user_agent"]; got != "SyntheticBrowser/1.0" {
		t.Errorf("user_agent = %q", got)
	}

	// The token is consumed by the first request.
	request = httptest.NewRequest(http.MethodPost, "/pair", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Indexarr-Pairing", token)
	recorder = httptest.NewRecorder()
	env.handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Errorf("reused token = %d, want 401", recorder.Code)
	}

	request = httptest.NewRequest(http.MethodPost, "/pair", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Indexarr-Pairing", "not-a-token")
	recorder = httptest.NewRecorder()
	env.handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Errorf("unknown token = %d, want 401", recorder.Code)
	}
}

func TestBrowserCookiePageAndCommand(t *testing.T) {
	cookiepair.Reset()
	env := newNativeEnv(t)
	seedDefinition(t, env.store, "cookie-src", "", cookieDefinition)
	id := seedPhase2Indexer(t, env.store, "cookie-src", "cookie-src", true,
		`{"url":"https://iptorrents.example/"}`)

	detail := get(t, env.handler, "/indexers/"+strconvI(id)).Body.String()
	if !strings.Contains(detail, "Import cookie") {
		t.Error("the detail page has no cookie import button")
	}
	page := get(t, env.handler, "/indexers/"+strconvI(id)+"/cookie")
	if page.Code != http.StatusOK {
		t.Fatalf("cookie page = %d: %s", page.Code, page.Body.String())
	}
	if !strings.Contains(page.Body.String(), "cookie-command") {
		t.Error("the cookie page has no command")
	}

	csrf, cookie := csrfFrom(t, env.handler, "/indexers/"+strconvI(id)+"/cookie")
	command := postForm(t, env.handler, "/indexers/"+strconvI(id)+"/cookie",
		url.Values{"csrf": {csrf}}, cookie)
	if command.Code != http.StatusOK {
		t.Fatalf("command page = %d: %s", command.Code, command.Body.String())
	}
	body := command.Body.String()
	for _, want := range []string{"curl -fsSL", "import-browser-cookie.py", "--indexarr", "--token", "--domain", "iptorrents.example"} {
		if !strings.Contains(body, want) {
			t.Errorf("command page missing %q", want)
		}
	}

	script := get(t, env.handler, "/static/import-browser-cookie.py")
	if script.Code != http.StatusOK {
		t.Fatalf("helper download = %d", script.Code)
	}
	if !strings.Contains(script.Body.String(), "X-Indexarr-Pairing") {
		t.Error("the helper is not the cookie importer")
	}
}

func TestCookieIndexerCanBeCreatedWithoutCookieAndTheFormOffersImport(t *testing.T) {
	cookiepair.Reset()
	env := newNativeEnv(t)
	seedDefinition(t, env.store, "cookie-src", "", cookieDefinition)

	form := get(t, env.handler, "/indexers/new?definition=cookie-src").Body.String()
	if !strings.Contains(form, "Create and import cookie") {
		t.Error("the add form has no create-and-import button")
	}
	csrf, cookie := csrfFrom(t, env.handler, "/indexers/new?definition=cookie-src")
	recorder := postForm(t, env.handler, "/indexers", url.Values{
		"csrf":          {csrf},
		"definition_id": {"cookie-src"},
		"name":          {"Cookie import"},
		"slug":          {"cookie-import"},
		"enabled":       {"on"},
		"import_cookie": {"1"},
	}, cookie)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("create status = %d: %s", recorder.Code, recorder.Body.String())
	}
	location := recorder.Header().Get("Location")
	if !strings.Contains(location, "/cookie") {
		t.Fatalf("redirect = %q, want the cookie page", location)
	}
	page := get(t, env.handler, location)
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "cookie-command") {
		t.Errorf("cookie page after create = %d: %s", page.Code, page.Body.String())
	}
}
