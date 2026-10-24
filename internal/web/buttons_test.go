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
	if !strings.Contains(location, "section=network") {
		t.Errorf("location = %q, want the network section", location)
	}
	// A successful test saves the endpoint it just verified, so the operator
	// never loses the value typed in the field.
	settings, err := env.store.LoadSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if settings.FlareSolverrURL != fs.URL {
		t.Errorf("the test did not save the URL: %q", settings.FlareSolverrURL)
	}
}

func TestFlareSolverrTestButtonEmptyURL(t *testing.T) {
	env := newTestEnv(t)
	token, cookie := csrfFrom(t, env.handler, "/settings")
	recorder := postForm(t, env.handler, "/settings/flaresolverr/test", url.Values{
		"csrf":             {token},
		"flaresolverr_url": {""},
	}, cookie)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "FlareSolverr URL is empty") || !strings.Contains(body, `class="field-error"`) {
		t.Errorf("empty URL did not render a field error: %s", body)
	}
	if !strings.Contains(body, `id="flaresolverr_url"`) {
		t.Error("the network form is not visible after the error")
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
