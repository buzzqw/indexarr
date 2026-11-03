package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/buzzqw/indexarr/internal/catalog"
	"github.com/buzzqw/indexarr/internal/indexersvc"
	"github.com/buzzqw/indexarr/internal/store"
)

func TestGuideProvidesUsesDefinitionMetadata(t *testing.T) {
	env := newTestEnv(t)
	request := httptest.NewRequest(http.MethodGet, "/indexers/new?lang=it", nil)
	cases := []struct {
		name  string
		entry catalog.IndexEntry
		want  string
	}{
		{
			name:  "storable latest list",
			entry: catalog.IndexEntry{LatestList: true, Storable: true},
			want:  "lista novità: sì, memorizzabile",
		},
		{
			name:  "latest list that already has a feed",
			entry: catalog.IndexEntry{LatestList: true, Scheduled: true},
			want:  "lista novità: sì",
		},
		{
			name:  "site with periodic updates",
			entry: catalog.IndexEntry{Scheduled: true},
			want:  "Novità dal sito",
		},
		{
			name:  "search only",
			entry: catalog.IndexEntry{},
			want:  "nessuna lista novità",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := env.server.guideProvides(request, tc.entry); !strings.Contains(got, tc.want) {
				t.Errorf("guideProvides = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestStoreWarningNeedsThreeStoredSources(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	if err := env.store.UpsertDefinition(ctx, store.Definition{
		ID: "test", SchemaVersion: 1, Family: "torznab", State: "verificata",
		Origin: "local", Author: "test", Body: "schema: 1", Hash: strings.Repeat("a", 64),
	}); err != nil {
		t.Fatal(err)
	}
	for _, slug := range []string{"one", "two", "three"} {
		if _, err := env.store.CreateIndexer(ctx, store.Indexer{
			DefinitionID: "test", Slug: slug, Name: slug, Enabled: true,
			ConfigJSON: `{"store_latest":"true"}`,
		}); err != nil {
			t.Fatal(err)
		}
	}
	in := indexersvc.Input{Settings: map[string]string{"store_latest": "true"}}
	needed, count, limit, err := env.server.storeWarningNeeded(ctx, in, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !needed || count != 3 || limit != 3 {
		t.Errorf("needed=%v count=%d limit=%d, want true/3/3", needed, count, limit)
	}
	// The third source does not warn.
	in.Settings["store_latest"] = "false"
	if needed, _, _, _ := env.server.storeWarningNeeded(ctx, in, 0); needed {
		t.Error("a site without the store flag warned")
	}
}

func TestDashboardShowsStoredCounter(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	if err := env.store.UpsertDefinition(ctx, store.Definition{
		ID: "test", SchemaVersion: 1, Family: "torznab", State: "verificata",
		Origin: "local", Author: "test", Body: "schema: 1", Hash: strings.Repeat("b", 64),
	}); err != nil {
		t.Fatal(err)
	}
	indexerID, err := env.store.CreateIndexer(ctx, store.Indexer{
		DefinitionID: "test", Slug: "stored", Name: "Stored", Enabled: true,
		ConfigJSON: `{"store_latest":"true"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	release := store.Release{
		DedupKey: "stored-1", Title: "Stored Show S01E01", SearchTitle: store.NormalizeSearch("Stored Show S01E01"),
		Size: 3 << 20, Category: "5040", PublishedAt: time.Now(), FirstSeenAt: time.Now(), LastSeenAt: time.Now(),
		Magnet: "magnet:?xt=urn:btih:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
	if _, err := env.store.UpsertRelease(ctx, &release); err != nil {
		t.Fatal(err)
	}
	if err := env.store.RecordReleaseSource(ctx, store.ReleaseSource{
		ReleaseID: release.ID, IndexerID: indexerID, FirstSeenAt: time.Now(), LastSeenAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	body := get(t, env.handler, "/").Body.String()
	if !strings.Contains(body, "Stored: 1 releases") {
		t.Errorf("home page has no stored counter:\n%s", body)
	}
	if !strings.Contains(body, "MB") {
		t.Errorf("home counter has no measured size")
	}
}

// The warning page posts to these two routes: they went missing once and the
// buttons answered 404.
func TestStoreWarningButtonsReachTheirHandlers(t *testing.T) {
	env := newTestEnv(t)
	token, err := env.server.pendingStores.put(pendingStoreEntry{definitionID: "example"})
	if err != nil {
		t.Fatal(err)
	}
	csrf, cookie := csrfFrom(t, env.handler, "/indexers")
	cancel := postForm(t, env.handler, "/indexers/store/cancel", url.Values{"csrf": {csrf}, "token": {token}}, cookie)
	if cancel.Code != http.StatusSeeOther {
		t.Fatalf("POST /indexers/store/cancel = %d, want 303", cancel.Code)
	}
	if location := cancel.Header().Get("Location"); !strings.HasPrefix(location, "/indexers/new?definition=example") {
		t.Errorf("cancel went to %q, want the form of the definition", location)
	}

	// The token is spent: the handler refuses it (400, or 503 without the
	// indexer service of a full daemon), but the route answers.
	confirm := postForm(t, env.handler, "/indexers/store/confirm", url.Values{"csrf": {csrf}, "token": {token}}, cookie)
	if confirm.Code == http.StatusNotFound || confirm.Code == http.StatusMethodNotAllowed {
		t.Fatalf("POST /indexers/store/confirm = %d: the route is missing", confirm.Code)
	}
}
