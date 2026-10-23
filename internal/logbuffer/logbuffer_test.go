package logbuffer

import (
	"io"
	"log/slog"
	"strings"
	"testing"
)

func testLogger(buffer *Buffer) *slog.Logger {
	return slog.New(NewHandler(slog.NewTextHandler(io.Discard, nil), buffer))
}

func TestCapturesFiltersAndRedacts(t *testing.T) {
	buffer := New(10)
	logger := testLogger(buffer)
	logger.Info("generated", "apikey", "supersecret")
	logger.Warn("fetch", "url", "http://host/api?apikey=supersecret&x=1")
	logger.With("source", "MirCrew").Error("boom", "target", "http://user:pass@host/path")
	logger.Debug("debug is below the default level")

	if got := buffer.Len(); got != 3 {
		t.Fatalf("len = %d, want 3 (debug is filtered by the text handler level)", got)
	}
	entries := buffer.Snapshot(10, slog.LevelDebug, "")
	if len(entries) != 3 {
		t.Fatalf("snapshot = %d", len(entries))
	}
	if entries[0].Message != "boom" || entries[2].Message != "generated" {
		t.Errorf("order = %q ... %q", entries[0].Message, entries[2].Message)
	}
	if entries[0].Attrs[0].Key != "source" || entries[0].Attrs[0].Value != "MirCrew" {
		t.Errorf("WithAttrs lost: %#v", entries[0].Attrs)
	}
	for _, entry := range entries {
		text := entry.Text()
		if strings.Contains(text, "supersecret") || strings.Contains(text, "user:pass") {
			t.Errorf("secret leaked: %q", text)
		}
	}
	if entries[1].Attrs[0].Value != "http://host/api?apikey=***&x=1" {
		t.Errorf("URL value not redacted: %#v", entries[1].Attrs)
	}
	if entries[2].Attrs[0].Value != "***" {
		t.Errorf("sensitive key not redacted: %#v", entries[2].Attrs)
	}
	if entries[0].LevelClass() != "error" || entries[1].LevelClass() != "warn" || entries[2].LevelClass() != "info" {
		t.Errorf("level classes = %q/%q/%q",
			entries[0].LevelClass(), entries[1].LevelClass(), entries[2].LevelClass())
	}

	if filtered := buffer.Snapshot(10, slog.LevelError, ""); len(filtered) != 1 || filtered[0].Level != slog.LevelError {
		t.Errorf("level filter = %#v", filtered)
	}
	if filtered := buffer.Snapshot(10, slog.LevelDebug, "mircrew"); len(filtered) != 1 {
		t.Errorf("query filter = %#v", filtered)
	}
	if limited := buffer.Snapshot(2, slog.LevelDebug, ""); len(limited) != 2 {
		t.Errorf("limit = %d", len(limited))
	}
}

func TestRingAndDumpOrder(t *testing.T) {
	buffer := New(2)
	logger := testLogger(buffer)
	logger.Info("one")
	logger.Info("two")
	logger.Info("three")

	entries := buffer.Snapshot(10, slog.LevelDebug, "")
	if len(entries) != 2 || entries[0].Message != "three" || entries[1].Message != "two" {
		t.Fatalf("ring = %#v", entries)
	}
	dump := buffer.Dump(slog.LevelDebug, "")
	lines := strings.Split(strings.TrimSpace(dump), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], "two") || !strings.Contains(lines[1], "three") {
		t.Errorf("dump order = %q", lines)
	}
	buffer.Clear()
	if buffer.Len() != 0 {
		t.Errorf("len after clear = %d", buffer.Len())
	}
	if dump := buffer.Dump(slog.LevelDebug, ""); dump != "" {
		t.Errorf("dump after clear = %q", dump)
	}
}

func TestGroupPrefix(t *testing.T) {
	buffer := New(10)
	logger := testLogger(buffer)
	logger.WithGroup("request").Info("hello", "url", "http://x")
	entries := buffer.Snapshot(1, slog.LevelDebug, "")
	if len(entries) != 1 || entries[0].Attrs[0].Key != "request.url" {
		t.Fatalf("group prefix = %#v", entries)
	}
}

func TestComponentFilter(t *testing.T) {
	buffer := New(10)
	logger := testLogger(buffer)
	logger.Warn("indexer down", "component", "indexer", "source", "Tracker")
	logger.Warn("rss down", "component", "rss", "source", "Feed")
	logger.Error("internal", "component", "indexarr")

	indexers := buffer.SnapshotFilter(Filter{MinLevel: slog.LevelWarn, Component: "indexer"})
	if len(indexers) != 1 || indexers[0].Source() != "Tracker" || indexers[0].Component() != "indexer" {
		t.Fatalf("indexer filter = %#v", indexers)
	}
	if got := buffer.SnapshotFilter(Filter{MinLevel: slog.LevelWarn, Component: "rss"}); len(got) != 1 {
		t.Errorf("rss filter = %#v", got)
	}
	if got := buffer.SnapshotFilter(Filter{MinLevel: slog.LevelWarn, Component: "indexarr"}); len(got) != 1 {
		t.Errorf("indexarr filter = %#v", got)
	}
	dump := buffer.DumpFilter(Filter{MinLevel: slog.LevelWarn, Component: "rss"})
	if !strings.Contains(dump, "rss down") || strings.Contains(dump, "indexer down") {
		t.Errorf("component dump = %q", dump)
	}
}

func TestClampsHugeFields(t *testing.T) {
	buffer := New(10)
	logger := testLogger(buffer)
	huge := strings.Repeat("x", maxFieldLen*3)
	logger.Error(huge, "error", huge)

	entry := buffer.Snapshot(1, slog.LevelError, "")[0]
	if len(entry.Message) > maxFieldLen+4 {
		t.Errorf("message length = %d", len(entry.Message))
	}
	if len(entry.Attrs[0].Value) > maxFieldLen+4 {
		t.Errorf("attr length = %d", len(entry.Attrs[0].Value))
	}
	if !strings.HasSuffix(entry.Message, "…") || !strings.HasSuffix(entry.Attrs[0].Value, "…") {
		t.Error("clamped values are not marked")
	}
}
