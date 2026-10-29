package web

// The "Send to client" path must carry the source indexer's share limits: a
// private tracker asks for a ratio, and qBittorrent can enforce it from the
// moment the magnet is added. The Torznab/app-sync path already sends the same
// seed_ratio to the *arr applications.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/buzzqw/indexarr/internal/store"
)

func TestReleaseSendCarriesTheIndexerSeedLimits(t *testing.T) {
	env := newTestEnv(t)
	var ratio, seedingTime string
	client := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/add":
			_ = r.ParseMultipartForm(4 << 20)
			ratio = r.FormValue("ratioLimit")
			seedingTime = r.FormValue("seedingTimeLimit")
			_, _ = w.Write([]byte("Ok."))
		default:
			http.NotFound(w, r)
		}
	}))
	defer client.Close()

	settings, err := env.store.LoadSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	settings.DownloadClient = "qbittorrent"
	settings.DownloadClientURL = client.URL
	if err := env.store.SaveSettings(context.Background(), settings); err != nil {
		t.Fatal(err)
	}

	seedDefinition(t, env.store, "seed-limits", "", `schema: 1
meta:
  id: seed-limits
  name: seed-limits
  type: private
  protocol: torrent
  urls: [https://seed-limits.example/]
search:
  request:
    get: /search
  response:
    type: html
    rows: { selector: "tr" }
    fields:
      title: { selector: "a" }
    required: [title]
`)
	indexerID, err := env.store.CreateIndexer(context.Background(), store.Indexer{
		DefinitionID:    "seed-limits",
		Protocol:        "torrent",
		Slug:            "seed-limits",
		Name:            "Seed limits",
		Enabled:         true,
		Priority:        25,
		ConfigJSON:      "{}",
		SeedRatio:       1.5,
		SeedTimeMinutes: 90,
	})
	if err != nil {
		t.Fatal(err)
	}

	insertSearchRelease(t, env.store, "Send Limits 1080p", "5040")
	releases, _, err := env.store.SearchReleases(context.Background(), store.Filter{})
	if err != nil || len(releases) != 1 {
		t.Fatalf("releases = %d, %v", len(releases), err)
	}
	release := releases[0]
	if err := env.store.RecordReleaseSource(context.Background(), store.ReleaseSource{
		ReleaseID:   release.ID,
		IndexerID:   indexerID,
		FirstSeenAt: time.Now(),
		LastSeenAt:  time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	token, cookie := csrfFrom(t, env.handler, "/releases/"+strconvI(release.ID))
	recorder := postForm(t, env.handler, "/releases/"+strconvI(release.ID)+"/send",
		url.Values{"csrf": {token}}, cookie)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("send status = %d: %s", recorder.Code, recorder.Body.String())
	}
	if ratio != "1.5" || seedingTime != "90" {
		t.Errorf("ratioLimit = %q, seedingTimeLimit = %q", ratio, seedingTime)
	}
	if strings.TrimSpace(ratio) == "" {
		t.Error("the indexer ratio was not sent to qBittorrent")
	}
}
