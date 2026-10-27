package web

import (
	"strings"
	"testing"

	"github.com/buzzqw/indexarr/internal/catalog"
)

// TestCatalogQueueShowsBlockedReason pins the two additions of the maintenance
// view: the head says how the queue is composed, and a blocked row carries the
// measured reason instead of leaving the maintainer to open blocked.yaml.
func TestCatalogQueueShowsBlockedReason(t *testing.T) {
	env := newNativeEnv(t)
	env.server.catalogIndex = func() ([]catalog.IndexEntry, error) {
		return []catalog.IndexEntry{
			{
				ID: "echo-blocked", Name: "EchoBlocked", Source: "jackett",
				Type: "public", Protocol: "torrent", State: catalog.SiteStateBlocked,
				Hosts: []string{"echo.example"}, Required: []string{"url"},
				Note: "The .torrent is behind a captcha the solver does not clear.",
			},
			{
				ID: "foxtrot-write", Name: "FoxtrotWrite", Source: "jackett",
				Type: "private", Protocol: "torrent", State: catalog.SiteStateTry,
				Hosts: []string{"foxtrot.example"}, Required: []string{"username", "password"},
			},
		}, nil
	}

	body := get(t, env.handler, "/indexers/new?all=1&queue=1").Body.String()
	if !strings.Contains(body, "The .torrent is behind a captcha the solver does not clear.") {
		t.Errorf("queue view does not show the blocked reason")
	}
	if !strings.Contains(body, "Queue: 1 to write, 0 drafts, 1 blocked, 0 dead.") {
		t.Errorf("queue view does not show the queue composition")
	}

	// The user-facing catalogue hides the queue and its notes.
	body = get(t, env.handler, "/indexers/new?all=1").Body.String()
	if strings.Contains(body, "captcha") || strings.Contains(body, "Queue:") {
		t.Errorf("the user catalogue leaks the maintenance details")
	}
}
