package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestNormalizeTrustedHosts(t *testing.T) {
	for _, test := range []struct {
		raw  string
		want string
		ok   bool
	}{
		{"", "", true},
		{"indexarr.example.com", "indexarr.example.com", true},
		{" Indexarr.Example.COM. ", "indexarr.example.com", true},
		{"indexarr.example.com, 192.168.1.10:8788", "indexarr.example.com, 192.168.1.10:8788", true},
		{"indexarr.example.com,indexarr.example.com", "indexarr.example.com", true},
		{"[::1]:8788", "[::1]:8788", true},
		{"::1", "::1", true},
		{"https://indexarr.example.com", "", false},
		{"indexarr.example.com/path", "", false},
		{"user@indexarr.example.com", "", false},
		{"bad host", "", false},
		{"indexarr.example.com:0", "", false},
		{"indexarr.example.com:70000", "", false},
	} {
		got, ok := normalizeTrustedHosts(test.raw)
		if ok != test.ok || (ok && got != test.want) {
			t.Errorf("normalizeTrustedHosts(%q) = %q, %v; want %q, %v", test.raw, got, ok, test.want, test.ok)
		}
	}
}

func TestHostAllowed(t *testing.T) {
	allowed := splitTrustedHosts("indexarr.example.com, media.example.com:8788, 192.168.1.10")
	for _, test := range []struct {
		host string
		want bool
	}{
		{"indexarr.example.com", true},
		{"indexarr.example.com:8788", true},
		{"INDEXARR.EXAMPLE.COM:80", true},
		{"media.example.com:8788", true},
		{"media.example.com:8787", false},
		{"192.168.1.10:8788", true},
		{"192.168.1.11:8788", false},
		{"evil.example.com", false},
		{"localhost:8788", true},
		{"127.0.0.1:8788", true},
		{"[::1]:8788", true},
	} {
		if got := hostAllowed(test.host, allowed); got != test.want {
			t.Errorf("hostAllowed(%q) = %v, want %v", test.host, got, test.want)
		}
	}
}

func TestTrustedHostsMiddleware(t *testing.T) {
	env := newTestEnv(t)
	settings, err := env.store.LoadSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	settings.TrustedHosts = "indexarr.example.com"
	if err := env.store.SaveSettings(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	env.server.invalidateTrustedHosts()

	serve := func(path, host, forwarded string) int {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.Host = host
		if forwarded != "" {
			request.Header.Set("X-Forwarded-Host", forwarded)
		}
		recorder := httptest.NewRecorder()
		env.handler.ServeHTTP(recorder, request)
		return recorder.Code
	}

	if code := serve("/healthz", "evil.example.com", ""); code != http.StatusBadRequest {
		t.Fatalf("foreign host = %d, want 400", code)
	}
	if code := serve("/healthz", "indexarr.example.com:8788", ""); code == http.StatusBadRequest {
		t.Fatalf("allowed host was refused: %d", code)
	}
	if code := serve("/healthz", "localhost:8788", ""); code == http.StatusBadRequest {
		t.Fatalf("loopback was refused: %d", code)
	}
	if code := serve("/healthz", "indexarr.example.com", "evil.example.com"); code != http.StatusBadRequest {
		t.Fatalf("foreign forwarded host = %d, want 400", code)
	}
	if code := serve("/static/app.css", "evil.example.com", ""); code == http.StatusBadRequest {
		t.Fatalf("static asset was refused: %d", code)
	}

	settings.TrustedHosts = ""
	if err := env.store.SaveSettings(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	env.server.invalidateTrustedHosts()
	if code := serve("/healthz", "evil.example.com", ""); code == http.StatusBadRequest {
		t.Fatalf("an empty allowlist still refused a host: %d", code)
	}
}

func TestTrustedHostsSettingsAreSaved(t *testing.T) {
	env := newTestEnv(t)
	token, cookie := csrfFrom(t, env.handler, "/settings?section=security")
	recorder := postForm(t, env.handler, "/settings", url.Values{
		"csrf":                {token},
		"section":             {"security"},
		"trusted_hosts":       {"IndexArr.Example.com, 192.168.1.10:8788"},
		"ui_user":             {""},
		"ui_password":         {""},
		"ui_password_confirm": {""},
	}, cookie)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
	settings, err := env.store.LoadSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if settings.TrustedHosts != "indexarr.example.com, 192.168.1.10:8788" {
		t.Errorf("trusted hosts = %q", settings.TrustedHosts)
	}

	// The test client uses example.com as Host; allow it so the second POST
	// reaches the validation instead of being stopped by the guard.
	settings.TrustedHosts = "example.com"
	if err := env.store.SaveSettings(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	env.server.invalidateTrustedHosts()
	recorder = postForm(t, env.handler, "/settings", url.Values{
		"csrf":                {token},
		"section":             {"security"},
		"trusted_hosts":       {"https://indexarr.example.com/path"},
		"ui_user":             {""},
		"ui_password":         {""},
		"ui_password_confirm": {""},
	}, cookie)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("invalid allowlist status = %d: %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "Each entry must be a hostname") {
		t.Error("the invalid allowlist did not show the English error")
	}
}
