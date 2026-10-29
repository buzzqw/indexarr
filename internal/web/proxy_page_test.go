package web

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/buzzqw/indexarr/internal/store"
)

func TestProxyCreateKeepsCredentialsOutOfThePage(t *testing.T) {
	env := newTestEnv(t)
	if recorder := get(t, env.handler, "/proxies"); recorder.Code != http.StatusOK {
		t.Fatalf("GET /proxies = %d: %s", recorder.Code, recorder.Body.String())
	}
	token, cookie := csrfFrom(t, env.handler, "/proxies")
	recorder := postForm(t, env.handler, "/proxies", url.Values{
		"csrf":     {token},
		"kind":     {"http"},
		"url":      {"http://user:pass-SECRET@127.0.0.1:8080"},
		"username": {""},
		"password": {""},
	}, cookie)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("POST /proxies = %d: %s", recorder.Code, recorder.Body.String())
	}

	proxies, err := env.store.ListProxies(t.Context())
	if err != nil || len(proxies) != 1 {
		t.Fatalf("proxies = %+v, %v", proxies, err)
	}
	if proxies[0].URL != "http://127.0.0.1:8080" {
		t.Fatalf("stored URL = %q", proxies[0].URL)
	}
	if len(proxies[0].SecretsEnc) == 0 {
		t.Fatal("credentials were not sealed")
	}

	body := get(t, env.handler, "/proxies").Body.String()
	if strings.Contains(body, "pass-SECRET") || strings.Contains(body, "user:pass-SECRET") {
		t.Fatalf("the proxy page leaked credentials: %s", body)
	}
	if !strings.Contains(body, "127.0.0.1:8080") {
		t.Fatal("the proxy URL is missing from the page")
	}
}

func TestProxyRejectsUnsupportedScheme(t *testing.T) {
	env := newTestEnv(t)
	token, cookie := csrfFrom(t, env.handler, "/proxies")
	recorder := postForm(t, env.handler, "/proxies", url.Values{
		"csrf": {token},
		"kind": {"socks4x"},
		"url":  {"socks4x://127.0.0.1:1080"},
	}, cookie)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("POST /proxies = %d, want 400", recorder.Code)
	}
	proxies, _ := env.store.ListProxies(t.Context())
	if len(proxies) != 0 {
		t.Fatalf("an unsupported proxy was stored: %+v", proxies)
	}
}

func TestProxyAcceptsSOCKS4Schemes(t *testing.T) {
	for _, scheme := range []string{"socks4", "socks4a"} {
		t.Run(scheme, func(t *testing.T) {
			env := newTestEnv(t)
			token, cookie := csrfFrom(t, env.handler, "/proxies")
			recorder := postForm(t, env.handler, "/proxies", url.Values{
				"csrf": {token},
				"kind": {scheme},
				"url":  {scheme + "://127.0.0.1:1080"},
			}, cookie)
			if recorder.Code != http.StatusSeeOther {
				t.Fatalf("POST /proxies = %d: %s", recorder.Code, recorder.Body.String())
			}
			proxies, err := env.store.ListProxies(t.Context())
			if err != nil || len(proxies) != 1 {
				t.Fatalf("proxies = %+v, %v", proxies, err)
			}
			if proxies[0].Kind != scheme {
				t.Errorf("stored kind = %q, want %q", proxies[0].Kind, scheme)
			}
			if proxies[0].URL != scheme+"://127.0.0.1:1080" {
				t.Errorf("stored URL = %q", proxies[0].URL)
			}
			body := get(t, env.handler, "/proxies").Body.String()
			if !strings.Contains(body, `value="`+scheme+`"`) {
				t.Errorf("the proxy selector has no %s option", scheme)
			}
		})
	}
}

func TestProxyDelete(t *testing.T) {
	env := newTestEnv(t)
	token, cookie := csrfFrom(t, env.handler, "/proxies")
	created := postForm(t, env.handler, "/proxies", url.Values{
		"csrf": {token}, "kind": {"http"}, "url": {"http://127.0.0.1:8080"},
	}, cookie)
	if created.Code != http.StatusSeeOther {
		t.Fatalf("create = %d: %s", created.Code, created.Body.String())
	}
	proxies, _ := env.store.ListProxies(t.Context())
	if len(proxies) != 1 {
		t.Fatalf("proxies = %+v", proxies)
	}
	deleted := postForm(t, env.handler, "/proxies/"+strconv.FormatInt(proxies[0].ID, 10)+"/delete",
		url.Values{"csrf": {token}}, cookie)
	if deleted.Code != http.StatusSeeOther {
		t.Fatalf("delete = %d: %s", deleted.Code, deleted.Body.String())
	}
	after, _ := env.store.ListProxies(t.Context())
	if len(after) != 0 {
		t.Fatalf("proxy was not deleted: %+v", after)
	}
}

func TestIndexerFormShowsProxyAndCriteria(t *testing.T) {
	env := newNativeEnv(t)
	if _, err := env.store.CreateProxy(t.Context(), store.Proxy{
		Kind: "http", URL: "http://127.0.0.1:8080",
	}); err != nil {
		t.Fatalf("CreateProxy: %v", err)
	}
	body := get(t, env.handler, "/indexers/new?definition=extto").Body.String()
	for _, want := range []string{
		`name="proxy_id"`, `name="min_seeders"`, `name="seed_ratio"`,
		`name="seed_time_minutes"`, `name="season_pack_seed_time_minutes"`,
		`name="query_limit"`, `name="grab_limit"`, "127.0.0.1:8080",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("indexer form missing %q", want)
		}
	}
}
