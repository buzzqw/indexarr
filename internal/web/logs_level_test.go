package web

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestRuntimeLogLevel(t *testing.T) {
	env := newTestEnv(t)
	token, cookie := csrfFrom(t, env.handler, "/logs")
	page := get(t, env.handler, "/logs").Body.String()
	if !strings.Contains(page, "runtime_level") || !strings.Contains(page, "Runtime verbosity") {
		t.Fatal("runtime log level controls are missing")
	}

	recorder := postForm(t, env.handler, "/logs/level", url.Values{
		"csrf":          {token},
		"runtime_level": {"debug"},
	}, cookie)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("set level status = %d: %s", recorder.Code, recorder.Body.String())
	}
	if env.level.Level() != slog.LevelDebug {
		t.Errorf("level = %v", env.level.Level())
	}
	settings, _ := env.store.LoadSettings(context.Background())
	if settings.LogLevel != "debug" {
		t.Errorf("stored log level = %q", settings.LogLevel)
	}

	env.server.log.Debug("runtime debug probe")
	body := get(t, env.handler, "/logs?level=debug").Body.String()
	if !strings.Contains(body, "runtime debug probe") {
		t.Error("debug message not captured after changing level")
	}

	invalid := postForm(t, env.handler, "/logs/level", url.Values{
		"csrf":          {token},
		"runtime_level": {"verbose"},
	}, cookie)
	if invalid.Code != http.StatusSeeOther {
		t.Fatalf("invalid level status = %d", invalid.Code)
	}
	if location := invalid.Header().Get("Location"); !strings.Contains(location, "err=") {
		t.Errorf("invalid level accepted: %q", location)
	}
}
