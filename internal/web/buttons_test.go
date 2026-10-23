package web

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestFlareSolverrTestButton(t *testing.T) {
	env := newTestEnv(t)
	fs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1" {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, `{"status":"ok","solution":{"status":200,"response":"<html>solved</html>"}}`)
	}))
	defer fs.Close()

	token, cookie := csrfFrom(t, env.handler, "/settings")
	recorder := postForm(t, env.handler, "/settings/flaresolverr/test", url.Values{
		"csrf":             {token},
		"flaresolverr_url": {fs.URL},
	}, cookie)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
	location := recorder.Header().Get("Location")
	if !strings.Contains(location, "FlareSolverr+OK") {
		t.Errorf("location = %q", location)
	}
	// Testing must not save the endpoint: the user still has to press Save.
	settings, err := env.store.LoadSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if settings.FlareSolverrURL != "" {
		t.Errorf("the test saved the URL: %q", settings.FlareSolverrURL)
	}
}

func TestFlareSolverrTestButtonEmptyURL(t *testing.T) {
	env := newTestEnv(t)
	token, cookie := csrfFrom(t, env.handler, "/settings")
	recorder := postForm(t, env.handler, "/settings/flaresolverr/test", url.Values{
		"csrf":             {token},
		"flaresolverr_url": {""},
	}, cookie)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("status = %d", recorder.Code)
	}
	if location := recorder.Header().Get("Location"); !strings.Contains(location, "empty") {
		t.Errorf("location = %q", location)
	}
}

func TestTestAllIndexersWithoutScheduler(t *testing.T) {
	env := newTestEnv(t)
	token, cookie := csrfFrom(t, env.handler, "/indexers")
	recorder := postForm(t, env.handler, "/indexers/test-all", url.Values{"csrf": {token}}, cookie)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("status = %d", recorder.Code)
	}
}
