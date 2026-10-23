package web

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/buzzqw/indexarr/internal/store"
)

// TestReleaseUnknownValuesUseDash covers V7: missing metadata is rendered as an
// em dash, never as 0 B or "?".
func TestReleaseUnknownValuesUseDash(t *testing.T) {
	env := newTestEnv(t)
	release := store.Release{
		DedupKey:    "wp1b-unknown",
		Title:       "Unknown Metadata Release",
		SearchTitle: store.NormalizeSearch("Unknown Metadata Release"),
		Size:        0,
		Seeders:     -1,
		Leechers:    -1,
		Category:    "",
		Magnet:      "magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567",
		FirstSeenAt: time.Now(),
		LastSeenAt:  time.Now(),
	}
	if _, err := env.store.UpsertRelease(context.Background(), &release); err != nil {
		t.Fatalf("UpsertRelease: %v", err)
	}
	body := get(t, env.handler, "/releases?q=Unknown+Metadata").Body.String()
	if !strings.Contains(body, "Unknown Metadata Release") {
		t.Fatalf("release row missing:\n%s", body)
	}
	if strings.Contains(body, "0 B") {
		t.Errorf("unknown size rendered as 0 B:\n%s", body)
	}
	if strings.Contains(body, ">?<") {
		t.Errorf("unknown integer rendered as ?:\n%s", body)
	}
	if !strings.Contains(body, "—") {
		t.Errorf("unknown values do not use an em dash:\n%s", body)
	}
}
