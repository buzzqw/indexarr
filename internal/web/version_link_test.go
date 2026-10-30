package web

import (
	"strings"
	"testing"
)

// The version in the sidebar (and in the mobile bar) must open the GitHub
// repository: it is the quickest way to reach the source from the UI.
func TestVersionLinksToTheGitHubRepository(t *testing.T) {
	env := newTestEnv(t)
	body := get(t, env.handler, "/").Body.String()
	const link = `href="https://github.com/buzzqw/indexarr"`
	if got := strings.Count(body, link); got != 2 {
		t.Fatalf("GitHub version link rendered %d times, want 2 (sidebar and mobile bar)", got)
	}
	if !strings.Contains(body, `target="_blank" rel="noopener noreferrer"`) {
		t.Error("version link does not open in a new tab with rel=noopener noreferrer")
	}
	if !strings.Contains(body, `class="version"`) {
		t.Error("version link lost the version class")
	}
}
