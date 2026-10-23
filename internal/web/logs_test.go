package web

import (
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/buzzqw/indexarr/internal/logbuffer"
)

func TestLogsPageAndFilters(t *testing.T) {
	env := newTestEnv(t)
	env.logs.Add(logbuffer.Entry{
		Time: time.Now().Add(-time.Minute), Level: slog.LevelInfo,
		Message: "info message", Attrs: []logbuffer.Attr{{Key: "source", Value: "feed"}},
	})
	env.logs.Add(logbuffer.Entry{
		Time: time.Now(), Level: slog.LevelError,
		Message: "error message", Attrs: []logbuffer.Attr{{Key: "apikey", Value: "***"}},
	})

	page := get(t, env.handler, "/logs")
	if page.Code != http.StatusOK {
		t.Fatalf("status = %d", page.Code)
	}
	body := page.Body.String()
	for _, want := range []string{"Logs", "info message", "error message", "Showing 2 of 2"} {
		if !strings.Contains(body, want) {
			t.Errorf("logs page missing %q", want)
		}
	}

	errorOnly := get(t, env.handler, "/logs?level=error").Body.String()
	if strings.Contains(errorOnly, "info message") || !strings.Contains(errorOnly, "error message") {
		t.Error("level filter is not applied")
	}
	searched := get(t, env.handler, "/logs?q=feed").Body.String()
	if !strings.Contains(searched, "info message") || strings.Contains(searched, "error message") {
		t.Error("text filter is not applied")
	}
	limited := get(t, env.handler, "/logs?limit=100").Body.String()
	if !strings.Contains(limited, "info message") {
		t.Error("limit filter broke the page")
	}
	auto := get(t, env.handler, "/logs?auto=1").Body.String()
	if !strings.Contains(auto, `http-equiv="refresh"`) {
		t.Error("auto refresh meta tag missing")
	}
}

func TestLogsDownloadAndClear(t *testing.T) {
	env := newTestEnv(t)
	env.logs.Add(logbuffer.Entry{Time: time.Now(), Level: slog.LevelInfo, Message: "download me"})
	env.logs.Add(logbuffer.Entry{Time: time.Now(), Level: slog.LevelDebug, Message: "skip me"})

	download := get(t, env.handler, "/logs/download?level=info")
	if download.Code != http.StatusOK {
		t.Fatalf("download status = %d", download.Code)
	}
	if !strings.Contains(download.Body.String(), "download me") || strings.Contains(download.Body.String(), "skip me") {
		t.Errorf("download body = %q", download.Body.String())
	}
	if disposition := download.Header().Get("Content-Disposition"); !strings.Contains(disposition, "attachment") {
		t.Errorf("Content-Disposition = %q", disposition)
	}

	token, cookie := csrfFrom(t, env.handler, "/logs")
	cleared := postForm(t, env.handler, "/logs/clear", url.Values{"csrf": {token}}, cookie)
	if cleared.Code != http.StatusSeeOther {
		t.Fatalf("clear status = %d", cleared.Code)
	}
	if env.logs.Len() != 0 {
		t.Errorf("buffer len after clear = %d", env.logs.Len())
	}
	if body := get(t, env.handler, "/logs").Body.String(); !strings.Contains(body, "No log entries match") {
		t.Error("empty state missing")
	}
}
