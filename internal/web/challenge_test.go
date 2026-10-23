package web

import (
	"context"
	"strings"
	"testing"

	"github.com/buzzqw/indexarr/internal/store"
)

func TestIndexerFormShowsChallengeOverride(t *testing.T) {
	env := newNativeEnv(t)
	body := get(t, env.handler, "/indexers/new?definition=corsaro").Body.String()
	for _, want := range []string{
		`name="challenge"`, `name="flaresolverr_url"`, `name="flaresolverr_timeout"`,
		"Cloudflare check", "Use the global setting", "Always allow FlareSolverr",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("indexer form missing %q", want)
		}
	}
}

func TestSettingsNetworkShowsFlareSolverrTimeout(t *testing.T) {
	env := newNativeEnv(t)
	body := get(t, env.handler, "/settings?section=network").Body.String()
	for _, want := range []string{`name="flaresolverr_timeout_seconds"`, "Solve timeout (seconds)"} {
		if !strings.Contains(body, want) {
			t.Errorf("network settings missing %q", want)
		}
	}
}

func TestIndexerListShowsBlockedWithRemedy(t *testing.T) {
	env := newNativeEnv(t)
	seedDefinition(t, env.store, "blocked-html", "", phase2Definition(
		"blocked-html", "", "public", []string{"https://blocked.example/"}, "", ""))
	id := seedPhase2Indexer(t, env.store, "blocked-html", "blocked-site", true,
		`{"url":"https://blocked.example/"}`)
	if err := env.store.UpsertIndexerState(context.Background(), store.IndexerState{
		IndexerID: id,
		Circuit:   "closed",
		LastError: "request blocked by a cloudflare challenge: engine: blocked.example returned HTTP 403 (cloudflare challenge); configure FlareSolverr",
	}); err != nil {
		t.Fatal(err)
	}

	body := get(t, env.handler, "/indexers").Body.String()
	for _, want := range []string{
		`class="status-dot status-blocked"`,
		"The site requires a Cloudflare check.",
		"Configure FlareSolverr",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("indexer list missing %q", want)
		}
	}
	if strings.Contains(body, "The last update failed.") {
		t.Error("the blocked indexer still reports the generic problem")
	}
}
