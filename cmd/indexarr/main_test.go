package main

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/buzzqw/indexarr/internal/logsafe"
)

func TestWebInterfaceLogMasksAPIKey(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	const key = "0123456789abcdef0123456789abcdef"
	logWebInterfaceReady(logger, "http://localhost:8484", key)
	line := buf.String()
	if strings.Contains(line, key) {
		t.Fatalf("log contains the full API key: %s", line)
	}
	if !strings.Contains(line, "0123…") {
		t.Fatalf("log lacks the masked prefix: %s", line)
	}
	if got := maskSecret("abc"); got != "…" {
		t.Fatalf("maskSecret(short) = %q", got)
	}
}

func TestOperationalLogsDoNotLeakSecrets(t *testing.T) {
	const secret = "vault-secret-0123456789abcdef"
	var buf bytes.Buffer
	logger := slog.New(logsafe.NewHandler(slog.NewTextHandler(&buf, nil)))
	logWebInterfaceReady(logger, "http://localhost:8484", secret)
	logger.Error("live search failed", "error", "GET https://site/api?passkey="+secret)
	logger.Warn("gextto import failed", "error", "https://rss.example/feed?rsskey="+secret)
	logger.Warn("jackett import failed", "error", "https://jackett/api?apikey="+secret)
	logger.Warn("prowlarr import failed", "error", "https://prowlarr/api?apikey="+secret)
	logger.Warn("grab failed", "error", "https://site/dl?u=user&e="+secret)
	logger.Warn("indexer test failed", "error", "login rejected: password="+secret)
	if strings.Contains(buf.String(), secret) {
		t.Fatalf("operational log leaked the secret:\n%s", buf.String())
	}
}

func TestIsLockedError(t *testing.T) {
	if !isLockedError(errors.New("database is locked (517)")) {
		t.Fatal("locked error not recognized")
	}
	if isLockedError(errors.New("no such table")) {
		t.Fatal("unrelated error treated as a lock")
	}
}
