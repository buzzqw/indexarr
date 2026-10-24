package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
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
