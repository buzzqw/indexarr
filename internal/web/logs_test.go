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

	page := get(t, env.handler, "/activity?tab=logs")
	if page.Code != http.StatusOK {
		t.Fatalf("status = %d", page.Code)
	}
	body := page.Body.String()
	for _, want := range []string{"Activity", "info message", "error message", "Showing 2 of 2"} {
		if !strings.Contains(body, want) {
			t.Errorf("logs page missing %q", want)
		}
	}

	errorOnly := get(t, env.handler, "/activity?tab=logs&level=error").Body.String()
	if strings.Contains(errorOnly, "info message") || !strings.Contains(errorOnly, "error message") {
		t.Error("level filter is not applied")
	}
	searched := get(t, env.handler, "/activity?tab=logs&q=feed").Body.String()
	if !strings.Contains(searched, "info message") || strings.Contains(searched, "error message") {
		t.Error("text filter is not applied")
	}
	limited := get(t, env.handler, "/activity?tab=logs&limit=100").Body.String()
	if !strings.Contains(limited, "info message") {
		t.Error("limit filter broke the page")
	}
	auto := get(t, env.handler, "/activity?tab=logs&auto=1").Body.String()
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

	token, cookie := csrfFrom(t, env.handler, "/activity?tab=logs")
	cleared := postForm(t, env.handler, "/logs/clear", url.Values{"csrf": {token}}, cookie)
	if cleared.Code != http.StatusSeeOther {
		t.Fatalf("clear status = %d", cleared.Code)
	}
	if env.logs.Len() != 0 {
		t.Errorf("buffer len after clear = %d", env.logs.Len())
	}
	if body := get(t, env.handler, "/activity?tab=logs").Body.String(); !strings.Contains(body, "No log lines match") {
		t.Error("empty state missing")
	}
}

func TestLogsFilterByComponent(t *testing.T) {
	env := newTestEnv(t)
	now := time.Now()
	env.logs.Add(logbuffer.Entry{
		Time: now, Level: slog.LevelError, Message: "source run failed",
		Attrs: []logbuffer.Attr{
			{Key: "component", Value: "indexer"},
			{Key: "source", Value: "Tracker"},
			{Key: "error", Value: "HTTP 502"},
		},
	})
	env.logs.Add(logbuffer.Entry{
		Time: now, Level: slog.LevelWarn, Message: "item skipped",
		Attrs: []logbuffer.Attr{
			{Key: "component", Value: "rss"},
			{Key: "source", Value: "Feed"},
		},
	})
	env.logs.Add(logbuffer.Entry{
		Time: now, Level: slog.LevelError, Message: "database failed",
		Attrs: []logbuffer.Attr{
			{Key: "component", Value: "indexarr"},
		},
	})

	indexer := get(t, env.handler, "/activity?tab=logs&component=indexer")
	if indexer.Code != 200 {
		t.Fatalf("indexer logs = %d", indexer.Code)
	}
	body := indexer.Body.String()
	for _, want := range []string{"source run failed", "Tracker", "Problems (1)", "component"} {
		if !strings.Contains(body, want) {
			t.Errorf("indexer logs missing %q", want)
		}
	}
	if strings.Contains(body, "item skipped") || strings.Contains(body, "database failed") {
		t.Error("component filter leaked other entries")
	}

	rss := get(t, env.handler, "/activity?tab=logs&component=rss").Body.String()
	if !strings.Contains(rss, "item skipped") || strings.Contains(rss, "source run failed") {
		t.Error("rss component filter is wrong")
	}

	download := get(t, env.handler, "/logs/download?component=rss").Body.String()
	if !strings.Contains(download, "item skipped") || strings.Contains(download, "source run failed") {
		t.Errorf("component download filter = %q", download)
	}

	problems := get(t, env.handler, "/activity?tab=logs&level=warn").Body.String()
	if !strings.Contains(problems, "Problems (3)") {
		t.Errorf("problem count not shown: %s", problems)
	}
}
