package main

import (
	"bytes"
	"errors"
	"log/slog"
	"net"
	"strings"
	"testing"

	"github.com/buzzqw/indexarr/internal/logsafe"
)

func TestPreflightListenFailsBeforeStartupWhenPortIsTaken(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen busy: %v", err)
	}
	defer busy.Close()
	if listener, err := preflightListen(busy.Addr().String()); err == nil {
		listener.Close()
		t.Fatal("preflightListen accepted an address already in use")
	} else if !strings.Contains(err.Error(), busy.Addr().String()) {
		t.Fatalf("error does not name the address: %v", err)
	}

	free, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen free: %v", err)
	}
	address := free.Addr().String()
	free.Close()
	listener, err := preflightListen(address)
	if err != nil {
		t.Fatalf("preflightListen(%s): %v", address, err)
	}
	listener.Close()
}

func TestWebInterfaceLogMasksAPIKey(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	const key = "0123456789abcdef0123456789abcdef"
	logWebInterfaceReady(logger, "http://localhost:8788", key)
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
	logWebInterfaceReady(logger, "http://localhost:8788", secret)
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
