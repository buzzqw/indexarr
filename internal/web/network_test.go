package web

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

type metricRoundTripper struct {
	status int
}

func (m metricRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: m.status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader("")),
		Request:    req,
	}, nil
}

func TestActivityShowsNetworkCounters(t *testing.T) {
	env := newTestEnv(t)

	limited := env.polite.RoundTripper(metricRoundTripper{status: http.StatusTooManyRequests})
	request, _ := http.NewRequest(http.MethodGet, "http://counters.example/feed", nil)
	if _, err := limited.RoundTrip(request); err != nil {
		t.Fatalf("record 429: %v", err)
	}

	body := get(t, env.handler, "/activity?tab=network&lang=en").Body.String()
	for _, want := range []string{
		"Network counters",
		"counters.example",
		"Per-host counters since the last restart",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("activity page missing %q", want)
		}
	}

	metrics := get(t, env.handler, "/metrics").Body.String()
	if !strings.Contains(metrics, `indexarr_host_rate_limited_total{host="counters.example"} 1`) {
		t.Errorf("metrics missing the per-host 429 counter:\n%s", metrics)
	}
}

func TestSettingsNetworkSectionRoundTrip(t *testing.T) {
	env := newTestEnv(t)

	body := get(t, env.handler, "/settings?section=network&lang=en").Body.String()
	for _, want := range []string{
		"Network",
		`name="host_rate"`,
		`name="host_burst"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("settings page missing %q", want)
		}
	}
	advanced := get(t, env.handler, "/settings?section=advanced&lang=en").Body.String()
	for _, want := range []string{`name="breaker_threshold"`, `name="breaker_cooldown"`} {
		if !strings.Contains(advanced, want) {
			t.Errorf("advanced settings page missing %q", want)
		}
	}

	token, cookie := csrfFrom(t, env.handler, "/settings?section=network&lang=en")
	recorder := postForm(t, env.handler, "/settings", url.Values{
		"csrf":       {token},
		"section":    {"network"},
		"host_rate":  {"2.5"},
		"host_burst": {"8"},
	}, cookie)
	if recorder.Code != http.StatusSeeOther && recorder.Code != http.StatusFound {
		t.Fatalf("POST /settings = %d: %s", recorder.Code, recorder.Body.String())
	}

	advancedToken, advancedCookie := csrfFrom(t, env.handler, "/settings?section=advanced&lang=en")
	if rec := postForm(t, env.handler, "/settings", url.Values{
		"csrf": {advancedToken}, "section": {"advanced"},
		"breaker_threshold": {"7"}, "breaker_cooldown": {"120"},
	}, advancedCookie); rec.Code != http.StatusSeeOther {
		t.Fatalf("POST advanced settings = %d: %s", rec.Code, rec.Body.String())
	}

	settings, err := env.store.LoadSettings(context.Background())
	if err != nil {
		t.Fatalf("LoadSettings: %v", err)
	}
	if settings.HostRate != 2.5 || settings.HostBurst != 8 ||
		settings.BreakerThreshold != 7 || settings.BreakerCooldownSeconds != 120 {
		t.Errorf("saved network settings = %#v", settings)
	}
}
