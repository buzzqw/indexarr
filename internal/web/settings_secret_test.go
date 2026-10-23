package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSettingsNeverRenderAPIKey(t *testing.T) {
	env := newTestEnv(t)
	body := get(t, env.handler, "/settings").Body.String()
	if env.apiKey == "" {
		t.Fatal("test env has no API key")
	}
	if strings.Contains(body, env.apiKey) {
		t.Fatal("primary API key is present in the initial /settings HTML")
	}
	if strings.Contains(body, "apikey="+env.apiKey) {
		t.Fatal("primary API key is present in the example Torznab URL")
	}
	for _, want := range []string{"••••••••", "/settings/secrets/apikey", "/settings/secrets/torznab"} {
		if !strings.Contains(body, want) {
			t.Errorf("/settings missing %q", want)
		}
	}

	for _, name := range []string{"apikey", "torznab"} {
		request := httptest.NewRequest(http.MethodGet, "/settings/secrets/"+name, nil)
		request.Header.Set("X-Requested-With", "fetch")
		recorder := httptest.NewRecorder()
		env.handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("/settings/secrets/%s = %d: %s", name, recorder.Code, recorder.Body.String())
		}
		if !strings.Contains(recorder.Body.String(), env.apiKey) {
			t.Errorf("/settings/secrets/%s did not return the key", name)
		}
	}

	request := httptest.NewRequest(http.MethodGet, "/settings/secrets/apikey", nil)
	recorder := httptest.NewRecorder()
	env.handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("secret without fetch header = %d, want 404", recorder.Code)
	}
}
