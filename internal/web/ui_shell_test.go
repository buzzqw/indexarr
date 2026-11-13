package web

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/buzzqw/indexarr/internal/store"
)

func TestShellHasThemeAndShortcuts(t *testing.T) {
	env := newTestEnv(t)
	body := get(t, env.handler, "/").Body.String()
	for _, want := range []string{
		`data-theme-select`,
		`data-shortcuts-open`,
		`data-shortcuts-dialog`,
		`aria-keyshortcuts="/"`,
		`id="shortcuts-title"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("shell missing %q", want)
		}
	}
}

func TestReleasesCarryBulkMagnetData(t *testing.T) {
	env := newTestEnv(t)
	const magnet = "magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567"
	release := store.Release{
		DedupKey:    "bulk-magnet",
		Title:       "Bulk Magnet Release",
		SearchTitle: store.NormalizeSearch("Bulk Magnet Release"),
		Magnet:      magnet,
		FirstSeenAt: time.Now(),
		LastSeenAt:  time.Now(),
	}
	if _, err := env.store.UpsertRelease(context.Background(), &release); err != nil {
		t.Fatalf("UpsertRelease: %v", err)
	}
	body := get(t, env.handler, "/releases?q=Bulk+Magnet").Body.String()
	for _, want := range []string{
		`data-bulk-copy-magnets`,
		`data-bulk-check`,
		`data-magnet="` + magnet + `"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("releases page missing %q", want)
		}
	}
}
