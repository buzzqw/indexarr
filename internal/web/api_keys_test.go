package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestAdditionalAPIKeys(t *testing.T) {
	env := newTestEnv(t)
	token, cookie := csrfFrom(t, env.handler, "/settings")

	recorder := postForm(t, env.handler, "/settings/apikeys", url.Values{
		"csrf": {token},
		"name": {"Sonarr"},
	}, cookie)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("add key status = %d: %s", recorder.Code, recorder.Body.String())
	}
	keys, err := env.store.ListAPIKeys(context.Background())
	if err != nil || len(keys) != 1 || keys[0].Name != "Sonarr" {
		t.Fatalf("keys = %#v, %v", keys, err)
	}
	page := get(t, env.handler, "/settings").Body.String()
	if !strings.Contains(page, "Sonarr") || !strings.Contains(page, "••••••••") {
		t.Error("additional key name/mask not listed")
	}
	if strings.Contains(page, keys[0].Key) {
		t.Fatal("additional API key is present in the initial settings HTML")
	}
	secretRequest := httptest.NewRequest(http.MethodGet,
		"/settings/secrets/key/"+strconvI(keys[0].ID), nil)
	secretRequest.Header.Set("X-Requested-With", "fetch")
	secretRecorder := httptest.NewRecorder()
	env.handler.ServeHTTP(secretRecorder, secretRequest)
	if secretRecorder.Code != http.StatusOK || !strings.Contains(secretRecorder.Body.String(), keys[0].Key) {
		t.Fatalf("secret endpoint = %d: %s", secretRecorder.Code, secretRecorder.Body.String())
	}

	deleted := postForm(t, env.handler, "/settings/apikeys/"+strconvI(keys[0].ID)+"/delete",
		url.Values{"csrf": {token}}, cookie)
	if deleted.Code != http.StatusSeeOther {
		t.Fatalf("delete key status = %d", deleted.Code)
	}
	keys, _ = env.store.ListAPIKeys(context.Background())
	if len(keys) != 0 {
		t.Errorf("keys after revoke = %#v", keys)
	}
}
