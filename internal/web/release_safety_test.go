package web

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/buzzqw/indexarr/internal/indexersvc"
	"github.com/buzzqw/indexarr/internal/store"
)

func TestReleaseDetailMagnetUsesSafeURL(t *testing.T) {
	env := newTestEnv(t)
	release := store.Release{
		DedupKey:    "block-a-magnet",
		Title:       "Block A Magnet",
		SearchTitle: store.NormalizeSearch("Block A Magnet"),
		Category:    "5040",
		PublishedAt: time.Now().Add(-time.Hour),
		FirstSeenAt: time.Now(),
		LastSeenAt:  time.Now(),
		Magnet:      "magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567&dn=BlockA",
	}
	if _, err := env.store.UpsertRelease(context.Background(), &release); err != nil {
		t.Fatal(err)
	}
	body := get(t, env.handler, "/releases/"+strconv.FormatInt(release.ID, 10)).Body.String()
	if strings.Contains(body, "#ZgotmplZ") {
		t.Fatal("release detail rendered #ZgotmplZ for the magnet link")
	}
	if !strings.Contains(body, `href="magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567&amp;dn=BlockA"`) {
		t.Errorf("release detail does not keep the magnet href: %s", body)
	}
}

func seedRSSSecretDefinition(t *testing.T, env *nativeEnv, id string) {
	t.Helper()
	body := `schema: 1
connector: rss
meta:
  id: ` + id + `
  name: ` + id + `
  type: public
  protocol: torrent
  urls: ["https://rss.example/"]
settings:
  - {name: url, type: text, required: true}
`
	if err := env.store.UpsertDefinition(context.Background(), store.Definition{
		ID:            id,
		SchemaVersion: 1,
		State:         "bozza",
		Origin:        "local",
		Author:        "test",
		Body:          body,
		Hash:          "hash-" + id,
	}); err != nil {
		t.Fatalf("UpsertDefinition: %v", err)
	}
}

func TestSensitiveFeedURLNeverAppearsInHTMLOrExport(t *testing.T) {
	cases := map[string]struct {
		url      string
		redacted string
	}{
		"path key":  {"https://rss.example/SYNTHETICRSSKEY/feed.xml", "https://rss.example/••••/feed.xml"},
		"query key": {"https://rss.example/feed.xml?passkey=SYNTHETICPASSKEY", "https://rss.example/feed.xml?passkey=••••"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			env := newNativeEnv(t)
			seedRSSSecretDefinition(t, env, "rss-secret-web")
			created, err := env.svc.Create(context.Background(), indexersvc.Input{
				DefinitionID: "rss-secret-web",
				Name:         "Synthetic feed",
				Settings:     map[string]string{"url": tc.url},
			})
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			if strings.Contains(created.ConfigJSON, "SYNTHETIC") || strings.Contains(created.ConfigJSON, tc.url) {
				t.Fatalf("sensitive URL is in config_json: %s", created.ConfigJSON)
			}

			path := "/indexers/" + strconv.FormatInt(created.ID, 10) + "/edit"
			body := get(t, env.handler, path).Body.String()
			if strings.Contains(body, tc.url) || strings.Contains(body, "SYNTHETICRSSKEY") || strings.Contains(body, "SYNTHETICPASSKEY") {
				t.Fatalf("edit page leaked the sensitive URL: %s", body)
			}
			if !strings.Contains(body, tc.redacted) {
				t.Errorf("edit page does not show the redacted URL %q", tc.redacted)
			}

			export := get(t, env.handler, "/indexers/export").Body.String()
			if strings.Contains(export, tc.url) || strings.Contains(export, "SYNTHETICRSSKEY") || strings.Contains(export, "SYNTHETICPASSKEY") {
				t.Fatalf("export leaked the sensitive URL: %s", export)
			}

			var config string
			if err := env.store.DB().QueryRow(
				`SELECT config_json FROM indexers WHERE id = ?`, created.ID).Scan(&config); err != nil {
				t.Fatalf("read config_json: %v", err)
			}
			if strings.Contains(config, tc.url) || strings.Contains(config, "SYNTHETIC") {
				t.Fatalf("database config_json contains the sensitive URL: %s", config)
			}
		})
	}
}

func TestIndexerFormPrefillsSuggestedInterval(t *testing.T) {
	env := newNativeEnv(t)
	body := `schema: 1
connector: rss
meta:
  id: interval-web
  name: Interval Web
  type: public
  protocol: torrent
  urls: ["https://feed.example/"]
  interval: 15m
settings:
  - {name: url, type: text, required: true}
`
	if err := env.store.UpsertDefinition(context.Background(), store.Definition{
		ID: "interval-web", SchemaVersion: 1, State: "bozza", Origin: "local",
		Author: "test", Body: body, Hash: "hash-interval-web",
	}); err != nil {
		t.Fatalf("UpsertDefinition: %v", err)
	}
	page := get(t, env.handler, "/indexers/new?definition=interval-web").Body.String()
	if !strings.Contains(page, `name="interval_seconds"`) || !strings.Contains(page, `value="900"`) {
		t.Errorf("the add form does not prefill meta.interval: %s", page)
	}
}

func TestReleaseDetailShowsDownloadQuarantine(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	if err := env.store.UpsertDefinition(ctx, store.Definition{
		ID: "quarantine-def", SchemaVersion: 1, Family: "torznab",
		State: "verificata", Origin: "local", Author: "test",
		Body: "schema: 1", Hash: "hash",
	}); err != nil {
		t.Fatal(err)
	}
	indexer, err := env.store.CreateIndexer(ctx, store.Indexer{
		DefinitionID: "quarantine-def", Slug: "quarantine", Name: "Quarantine",
		Protocol: "torrent", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	release := store.Release{
		DedupKey: "quarantine-release", Title: "Quarantine Release",
		SearchTitle: store.NormalizeSearch("Quarantine Release"), Category: "5040",
		PublishedAt: time.Now(), FirstSeenAt: time.Now(), LastSeenAt: time.Now(),
	}
	if _, err := env.store.UpsertRelease(ctx, &release); err != nil {
		t.Fatal(err)
	}
	if err := env.store.RecordReleaseSource(ctx, store.ReleaseSource{
		ReleaseID: release.ID, IndexerID: indexer,
	}); err != nil {
		t.Fatal(err)
	}
	if err := env.store.RecordSourceDownloadNotice(ctx, release.ID, indexer, "site requires a login"); err != nil {
		t.Fatal(err)
	}
	body := get(t, env.handler, "/releases/"+strconv.FormatInt(release.ID, 10)).Body.String()
	if !strings.Contains(body, "site requires a login") {
		t.Fatalf("release detail does not show the download error")
	}
}
