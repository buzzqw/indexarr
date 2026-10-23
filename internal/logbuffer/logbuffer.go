// Package logbuffer keeps the most recent log records in memory so the web
// interface can show them without shell access. It wraps a normal slog handler
// and redacts credentials before storing an entry.
package logbuffer

import (
	"context"
	"io"
	"regexp"
	"strings"
	"sync"
	"time"

	"log/slog"
)

const DefaultMaxEntries = 2000

// Attr is one rendered log attribute.
type Attr struct {
	Key   string
	Value string
}

// Entry is one captured log record.
type Entry struct {
	Time    time.Time
	Level   slog.Level
	Message string
	Attrs   []Attr
}

// LevelName returns DEBUG, INFO, WARN or ERROR.
func (e Entry) LevelName() string { return e.Level.String() }

// LevelClass returns the CSS class used by the log page.
func (e Entry) LevelClass() string {
	switch {
	case e.Level >= slog.LevelError:
		return "error"
	case e.Level >= slog.LevelWarn:
		return "warn"
	case e.Level >= slog.LevelInfo:
		return "info"
	default:
		return "debug"
	}
}

// Text renders the entry as one log line.
func (e Entry) Text() string {
	var b strings.Builder
	b.WriteString(e.Time.Format("2006-01-02 15:04:05.000"))
	b.WriteByte(' ')
	b.WriteString(e.Level.String())
	b.WriteByte(' ')
	b.WriteString(e.Message)
	for _, attr := range e.Attrs {
		b.WriteByte(' ')
		b.WriteString(attr.Key)
		b.WriteByte('=')
		b.WriteString(attr.Value)
	}
	return b.String()
}

// Buffer is a fixed-size ring of log entries, safe for concurrent use.
type Buffer struct {
	mu      sync.RWMutex
	entries []Entry
	next    int
	full    bool
	max     int
}

// New creates a buffer holding at most max entries. A non-positive max selects
// DefaultMaxEntries.
func New(max int) *Buffer {
	if max <= 0 {
		max = DefaultMaxEntries
	}
	return &Buffer{max: max}
}

// Add appends an entry, overwriting the oldest when the ring is full.
func (b *Buffer) Add(entry Entry) {
	if entry.Time.IsZero() {
		entry.Time = time.Now()
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.entries) < b.max {
		b.entries = append(b.entries, entry)
		if len(b.entries) == b.max {
			b.full = true
			b.next = 0
		}
		return
	}
	b.entries[b.next] = entry
	b.next = (b.next + 1) % b.max
}

// Snapshot returns matching entries, newest first. A non-positive limit means
// "all matching entries".
func (b *Buffer) Snapshot(limit int, minLevel slog.Level, query string) []Entry {
	b.mu.RLock()
	defer b.mu.RUnlock()
	ordered := b.orderedLocked()
	out := make([]Entry, 0, len(ordered))
	for i := len(ordered) - 1; i >= 0; i-- {
		entry := ordered[i]
		if entry.Level < minLevel || !matches(entry, query) {
			continue
		}
		if limit > 0 && len(out) >= limit {
			break
		}
		out = append(out, entry)
	}
	return out
}

// Dump returns matching entries in chronological order as text, for download.
func (b *Buffer) Dump(minLevel slog.Level, query string) string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	var out strings.Builder
	for _, entry := range b.orderedLocked() {
		if entry.Level < minLevel || !matches(entry, query) {
			continue
		}
		out.WriteString(entry.Text())
		out.WriteByte('\n')
	}
	return out.String()
}

// Clear empties the buffer.
func (b *Buffer) Clear() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.entries = nil
	b.next = 0
	b.full = false
}

// Len returns the number of captured entries.
func (b *Buffer) Len() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.entries)
}

// Cap returns the maximum number of entries.
func (b *Buffer) Cap() int { return b.max }

func (b *Buffer) orderedLocked() []Entry {
	if !b.full {
		return append([]Entry(nil), b.entries...)
	}
	out := make([]Entry, 0, len(b.entries))
	out = append(out, b.entries[b.next:]...)
	out = append(out, b.entries[:b.next]...)
	return out
}

func matches(entry Entry, query string) bool {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return true
	}
	if strings.Contains(strings.ToLower(entry.Message), query) {
		return true
	}
	for _, attr := range entry.Attrs {
		if strings.Contains(strings.ToLower(attr.Key), query) ||
			strings.Contains(strings.ToLower(attr.Value), query) {
			return true
		}
	}
	return false
}

type handler struct {
	base   slog.Handler
	buffer *Buffer
	attrs  []Attr
	groups []string
}

// NewHandler wraps next so every record at the handler's level is also stored
// in buffer. The underlying handler still writes to its destination (stdout).
func NewHandler(next slog.Handler, buffer *Buffer) slog.Handler {
	if next == nil {
		next = slog.NewTextHandler(io.Discard, nil)
	}
	if buffer == nil {
		buffer = New(0)
	}
	return &handler{base: next, buffer: buffer}
}

func (h *handler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.base.Enabled(ctx, level)
}

func (h *handler) Handle(ctx context.Context, record slog.Record) error {
	attrs := append([]Attr(nil), h.attrs...)
	record.Attrs(func(attr slog.Attr) bool {
		attrs = appendAttr(attrs, h.groups, attr)
		return true
	})
	h.buffer.Add(Entry{
		Time:    record.Time,
		Level:   record.Level,
		Message: redact(record.Message),
		Attrs:   attrs,
	})
	return h.base.Handle(ctx, record)
}

func (h *handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	merged := append([]Attr(nil), h.attrs...)
	merged = appendAttrs(merged, h.groups, attrs)
	return &handler{
		base:   h.base.WithAttrs(attrs),
		buffer: h.buffer,
		attrs:  merged,
		groups: h.groups,
	}
}

func (h *handler) WithGroup(name string) slog.Handler {
	groups := append(append([]string(nil), h.groups...), name)
	return &handler{
		base:   h.base.WithGroup(name),
		buffer: h.buffer,
		attrs:  h.attrs,
		groups: groups,
	}
}

func appendAttrs(out []Attr, groups []string, attrs []slog.Attr) []Attr {
	for _, attr := range attrs {
		out = appendAttr(out, groups, attr)
	}
	return out
}

func appendAttr(out []Attr, groups []string, attr slog.Attr) []Attr {
	if attr.Key == "" {
		return out
	}
	value := attr.Value.Resolve().String()
	if sensitiveKey(attr.Key) {
		value = "***"
	} else {
		value = redact(value)
	}
	key := attr.Key
	if len(groups) > 0 {
		key = strings.Join(groups, ".") + "." + key
	}
	return append(out, Attr{Key: key, Value: value})
}

var (
	secretPattern = regexp.MustCompile(`(?i)(apikey|api_key|passkey|password|secret|token)=([^&\s"']+)`)
	basicPattern  = regexp.MustCompile(`(?i)(://)[^/@\s]+:[^/@\s]+@`)
)

func redact(text string) string {
	text = secretPattern.ReplaceAllString(text, "$1=***")
	return basicPattern.ReplaceAllString(text, "$1***@")
}

func sensitiveKey(key string) bool {
	key = strings.ToLower(key)
	for _, marker := range []string{"apikey", "api_key", "passkey", "password", "secret", "token"} {
		if strings.Contains(key, marker) {
			return true
		}
	}
	return false
}
