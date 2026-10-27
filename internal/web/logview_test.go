package web

import (
	"log/slog"
	"strings"
	"testing"

	"github.com/buzzqw/indexarr/internal/i18n"
	"github.com/buzzqw/indexarr/internal/logbuffer"
	"github.com/buzzqw/indexarr/internal/logtext"
)

func logEntry(level slog.Level, message string, kv ...string) logbuffer.Entry {
	entry := logbuffer.Entry{Level: level, Message: message}
	for i := 0; i+1 < len(kv); i += 2 {
		entry.Attrs = append(entry.Attrs, logbuffer.Attr{Key: kv[i], Value: kv[i+1]})
	}
	return entry
}

func TestLogViewNativeRunReadsNaturally(t *testing.T) {
	entry := logEntry(slog.LevelInfo, "native: run complete", "component", "native",
		"indexers", "2", "ran", "2", "skipped", "0", "failed", "0",
		"seen", "1083", "new", "30", "updated", "6", "sources", "MirCrew, Knaben", "failed_sources", "")
	view := newLogView(i18n.IT, entry, nil)
	want := "Aggiornamento automatico di MirCrew, Knaben completato: 30 release nuove aggiunte al catalogo, 6 aggiornate, 1083 risultati letti."
	if view.Sentence != want {
		t.Errorf("sentence = %q\nwant       %q", view.Sentence, want)
	}
	if view.Area != "Aggiornamento fonti" || view.Problem != "" {
		t.Errorf("area = %q, problem = %q", view.Area, view.Problem)
	}
	for _, detail := range view.Details {
		if detail.Key == "component" {
			t.Error("component is repeated in the details")
		}
	}

	quiet := logEntry(slog.LevelInfo, "native: run complete", "indexers", "1", "ran", "1",
		"failed", "0", "seen", "100", "new", "0", "updated", "100", "sources", "MirCrew")
	if got := newLogView(i18n.EN, quiet, nil).Sentence; !strings.Contains(got, "nothing new") {
		t.Errorf("quiet run = %q", got)
	}
	old := logEntry(slog.LevelInfo, "native: run complete", "indexers", "1", "ran", "1",
		"failed", "0", "seen", "100", "new", "0", "updated", "100")
	if got := newLogView(i18n.EN, old, nil).Sentence; strings.Contains(got, "{") || !strings.Contains(got, "1 sources updated") {
		t.Errorf("entry without sources = %q", got)
	}
}

func TestLogViewExplainsProblems(t *testing.T) {
	entry := logEntry(slog.LevelWarn, "native: indexer run failed", "indexer", "tgx",
		"indexer_id", "4", "duration_ms", "20000", "problem", logtext.Blocked)
	view := newLogView(i18n.EN, entry, map[string]string{"tgx": "TorrentGalaxy"})
	if !strings.HasPrefix(view.Sentence, "The update of TorrentGalaxy failed after 20.0 s") {
		t.Errorf("sentence = %q", view.Sentence)
	}
	if !strings.Contains(view.Problem, "Cloudflare") || !strings.Contains(view.Advice, "FlareSolverr") {
		t.Errorf("problem = %q, advice = %q", view.Problem, view.Advice)
	}

	withError := logEntry(slog.LevelWarn, "health check failed", "indexer", "x",
		"error", "Get \"https://x\": context deadline exceeded")
	view = newLogView(i18n.IT, withError, nil)
	if view.Problem != i18n.T(i18n.IT, "logs.problem.timeout") || view.ErrorText == "" {
		t.Errorf("timeout problem = %q, error = %q", view.Problem, view.ErrorText)
	}
	unknown := logEntry(slog.LevelError, "adminapi: get indexer", "id", "3", "error", "weird")
	view = newLogView(i18n.EN, unknown, nil)
	if !strings.Contains(view.Sentence, "operation: get indexer") || view.Problem == "" {
		t.Errorf("prefix family = %q / %q", view.Sentence, view.Problem)
	}
}

func TestLogViewFallbackAndDurations(t *testing.T) {
	view := newLogView(i18n.EN, logEntry(slog.LevelInfo, "something new"), nil)
	if view.Sentence != "Something new" || view.Area != "Other" {
		t.Errorf("fallback = %q / %q", view.Sentence, view.Area)
	}
	started := logEntry(slog.LevelInfo, "native: scheduler started", "interval", "1h0m0s", "tick", "1m0s", "workers", "2")
	if got := newLogView(i18n.EN, started, nil).Sentence; !strings.Contains(got, "every 1 h;") || !strings.Contains(got, "every 1 min,") {
		t.Errorf("durations = %q", got)
	}
}

func TestLogCatalogComplete(t *testing.T) {
	for _, lang := range i18n.Supported() {
		check := func(key string) {
			if got := i18n.T(lang, key); got == key {
				t.Errorf("%s: missing %q", lang, key)
			}
		}
		for _, spec := range logMessages {
			if spec.variant != nil {
				for _, v := range []string{"none", "count", "failedAll", "failed", "quiet", "ok"} {
					check("logs.msg." + spec.id + "." + v)
				}
				continue
			}
			check("logs.msg." + spec.id)
		}
		for _, family := range logPrefixes {
			check("logs.msg." + family.id)
		}
		for _, area := range logAreas {
			check("logs.area." + area)
		}
		for _, code := range logtext.Codes() {
			check("logs.problem." + code)
			check("logs.problemShort." + code)
			check("logs.advice." + code)
		}
	}
}
