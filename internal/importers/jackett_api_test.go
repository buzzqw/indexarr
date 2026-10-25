package importers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

const jackettTestKey = "jackett-api-key-SECRET"

func TestFromJackettAPIReadsLiveListAndDisablesFailures(t *testing.T) {
	var mu sync.Mutex
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		mu.Lock()
		paths = append(paths, r.URL.String())
		mu.Unlock()
		switch {
		case r.URL.Path == "/api/v2.0/indexers/all/results/torznab/api":
			if got := r.URL.Query().Get("t"); got != "indexers" {
				t.Errorf("t = %q", got)
			}
			if got := r.URL.Query().Get("configured"); got != "true" {
				t.Errorf("configured = %q", got)
			}
			if got := r.URL.Query().Get("apikey"); got != jackettTestKey {
				t.Errorf("apikey = %q", got)
			}
			w.Header().Set("Content-Type", "application/xml")
			fmt.Fprint(w, `<indexers>
  <indexer id="good" configured="true">
    <title>Good Site</title>
    <description>Working indexer</description>
    <link>https://good.example/</link>
    <language>en-US</language>
    <type>public</type>
    <caps><categories><category id="2000" name="Movies"><subcat id="2040" name="Movies/HD"/></category></categories></caps>
  </indexer>
  <indexer id="bad" configured="true">
    <title>Bad Site</title>
    <link>https://bad.example/</link>
    <type>public</type>
  </indexer>
  <indexer id="off" configured="false">
    <title>Off Site</title>
    <link>https://off.example/</link>
  </indexer>
</indexers>`)
		case strings.HasPrefix(r.URL.Path, "/api/v2.0/indexers/good/"):
			if got := r.URL.Query().Get("t"); got != "caps" {
				t.Errorf("good probe t = %q", got)
			}
			fmt.Fprint(w, `<caps><server title="Jackett"/></caps>`)
		case strings.HasPrefix(r.URL.Path, "/api/v2.0/indexers/bad/"):
			http.Error(w, "Indexer is not configured", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	plan, err := FromJackettAPI(context.Background(), server.URL, jackettTestKey, server.Client())
	if err != nil {
		t.Fatalf("FromJackettAPI: %v", err)
	}
	if plan.Source != "jackett" || plan.ManagerURL != server.URL {
		t.Fatalf("plan = %+v", plan)
	}
	if len(plan.Indexers) != 2 {
		t.Fatalf("indexers = %d, want 2 configured: %+v", len(plan.Indexers), plan.Indexers)
	}
	byID := map[string]Indexer{}
	for _, indexer := range plan.Indexers {
		byID[indexer.RemoteID] = indexer
	}
	if !byID["good"].Enabled {
		t.Errorf("good indexer is disabled")
	}
	if byID["bad"].Enabled {
		t.Errorf("bad indexer stayed enabled")
	}
	if !strings.Contains(byID["bad"].DisabledReason, "500") {
		t.Errorf("bad reason = %q", byID["bad"].DisabledReason)
	}
	if got := byID["good"].BaseURL; got != server.URL+"/api/v2.0/indexers/good/results/torznab/" {
		t.Errorf("good endpoint = %q", got)
	}
	if got := byID["good"].Categories; len(got) != 2 || got[0] != 2000 || got[1] != 2040 {
		t.Errorf("good categories = %v", got)
	}
	if byID["good"].SiteURL != "https://good.example/" {
		t.Errorf("good site URL = %q", byID["good"].SiteURL)
	}
	joined := strings.Join(plan.Warnings, "\n")
	if !strings.Contains(joined, "Bad Site") || !strings.Contains(joined, "HTTP 500") {
		t.Errorf("warnings = %v", plan.Warnings)
	}
	if strings.Contains(joined, jackettTestKey) {
		t.Fatalf("warning leaked the API key: %v", plan.Warnings)
	}

	// Only the bulk list and caps probes; no writes, no search.
	mu.Lock()
	defer mu.Unlock()
	for _, path := range paths {
		if strings.Contains(path, "t=search") || strings.Contains(path, "t=get") {
			t.Errorf("unexpected request %s", path)
		}
	}
}

func TestFromJackettAPIOptionsIncludeInactiveCatalog(t *testing.T) {
	const key = "jackett-catalog-key-SECRET"
	var mu sync.Mutex
	var probes []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		switch {
		case r.URL.Path == "/api/v2.0/indexers/all/results/torznab/api":
			if got := r.URL.Query().Get("apikey"); got != key {
				t.Errorf("apikey = %q", got)
			}
			w.Header().Set("Content-Type", "application/xml")
			if r.URL.Query().Get("configured") == "true" {
				fmt.Fprint(w, `<indexers>
  <indexer id="alpha" configured="true">
    <title>Alpha</title>
    <link>https://alpha.example/</link>
    <type>public</type>
  </indexer>
</indexers>`)
				return
			}
			fmt.Fprint(w, `<indexers>
  <indexer id="alpha" configured="true">
    <title>Alpha</title>
    <link>https://alpha.example/</link>
    <type>public</type>
  </indexer>
  <indexer id="beta" configured="false">
    <title>Beta</title>
    <link>https://beta.example/</link>
    <type>private</type>
  </indexer>
</indexers>`)
		case strings.HasPrefix(r.URL.Path, "/api/v2.0/indexers/alpha/"):
			if got := r.URL.Query().Get("t"); got != "caps" {
				t.Errorf("alpha probe t = %q", got)
			}
			mu.Lock()
			probes = append(probes, "alpha")
			mu.Unlock()
			fmt.Fprint(w, `<caps><server title="Jackett"/></caps>`)
		case strings.HasPrefix(r.URL.Path, "/api/v2.0/indexers/beta/"):
			mu.Lock()
			probes = append(probes, "beta")
			mu.Unlock()
			fmt.Fprint(w, `<caps><server title="Jackett"/></caps>`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	plan, err := FromJackettAPIWithOptions(context.Background(), server.URL, key, server.Client(),
		JackettOptions{IncludeInactive: true})
	if err != nil {
		t.Fatalf("FromJackettAPIWithOptions: %v", err)
	}
	if len(plan.Indexers) != 2 {
		t.Fatalf("indexers = %d, want 2: %+v", len(plan.Indexers), plan.Indexers)
	}
	byID := map[string]Indexer{}
	for _, indexer := range plan.Indexers {
		byID[indexer.RemoteID] = indexer
	}
	if !byID["alpha"].Enabled || byID["alpha"].CatalogOnly {
		t.Errorf("alpha = %+v", byID["alpha"])
	}
	if byID["beta"].Enabled || !byID["beta"].CatalogOnly {
		t.Errorf("beta = %+v", byID["beta"])
	}
	if byID["beta"].DisabledReason != "not configured in Jackett" {
		t.Errorf("beta reason = %q", byID["beta"].DisabledReason)
	}
	if got := strings.Join(plan.Warnings, "\n"); !strings.Contains(got, "1 catalog indexers") {
		t.Errorf("warnings = %v", plan.Warnings)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, id := range probes {
		if id == "beta" {
			t.Fatal("the inactive catalog indexer was probed")
		}
	}
	if len(probes) != 1 {
		t.Fatalf("probes = %v, want only alpha", probes)
	}
}

func TestFromJackettAPIRejectsBadURLWithoutLeakingKey(t *testing.T) {
	_, err := FromJackettAPI(context.Background(), "file:///tmp/jackett", jackettTestKey, nil)
	if err == nil {
		t.Fatal("FromJackettAPI accepted a non-http URL")
	}
	if strings.Contains(err.Error(), jackettTestKey) {
		t.Fatalf("error leaked the key: %v", err)
	}
}

func TestDeduplicatePrefersNativeThenFamilyThenPending(t *testing.T) {
	pending := NativeTarget{
		Name: "Site", Slug: "site", Status: TargetPending,
		SiteKey: "site.example", BaseURL: "https://site.example/",
	}
	family := NativeTarget{
		Name: "Site", Slug: "site", DefinitionID: "jk-site", Status: TargetFamily,
		Family: "nexusphp", SiteKey: "site.example", BaseURL: "https://site.example/",
	}
	native := NativeTarget{
		Name: "Site", Slug: "site", DefinitionID: "site", Status: TargetNative,
		SiteKey: "site.example", BaseURL: "https://site.example/",
	}
	other := NativeTarget{
		Name: "Other", Slug: "other", Status: TargetPending,
		SiteKey: "other.example", BaseURL: "https://other.example/",
	}

	kept, dropped := Deduplicate([]NativeTarget{pending, other, family, native})
	if len(kept) != 2 {
		t.Fatalf("kept = %d, want 2: %+v", len(kept), kept)
	}
	if kept[0].DefinitionID != "site" || kept[0].Status != TargetNative {
		t.Fatalf("site winner = %+v", kept[0])
	}
	if kept[1].Name != "Other" {
		t.Fatalf("other site = %+v", kept[1])
	}
	if len(dropped) != 2 {
		t.Fatalf("dropped = %+v", dropped)
	}
	for _, item := range dropped {
		if item.CoveredBy.Name != "Site" {
			t.Errorf("covered by %q", item.CoveredBy.Name)
		}
	}
}

func TestDeduplicateIsIdempotent(t *testing.T) {
	targets := []NativeTarget{
		{Name: "A", Status: TargetPending, SiteKey: "a"},
		{Name: "A", Status: TargetFamily, DefinitionID: "jk-a", SiteKey: "a"},
		{Name: "A", Status: TargetNative, DefinitionID: "a", SiteKey: "a"},
		{Name: "B", Status: TargetNative, DefinitionID: "b", SiteKey: "b"},
	}
	first, dropped := Deduplicate(targets)
	second, droppedAgain := Deduplicate(first)
	if len(first) != 2 || len(second) != 2 {
		t.Fatalf("first=%d second=%d", len(first), len(second))
	}
	if len(dropped) != 2 || len(droppedAgain) != 0 {
		t.Fatalf("dropped=%d again=%d", len(dropped), len(droppedAgain))
	}
	if first[0].Status != TargetNative || second[0].Status != TargetNative {
		t.Fatalf("winner changed: %+v -> %+v", first[0], second[0])
	}
}

func TestTorrentLeechRSSTargetKeepsTheFeedInSecrets(t *testing.T) {
	const feed = "https://rss.example/SECRET-RSS-KEY"
	target := TorrentLeechRSSTarget(feed)
	if target.DefinitionID != "torrentleech-rss" || target.Slug != "torrentleech-rss" {
		t.Fatalf("target = %+v", target)
	}
	if target.Secrets["url"] != feed {
		t.Fatalf("secret = %q", target.Secrets["url"])
	}
	if strings.Contains(fmt.Sprint(target.Config), "SECRET") {
		t.Fatalf("feed URL leaked into config: %v", target.Config)
	}
}

func TestPairChannelsKeepsFeedAndSearchForOneSite(t *testing.T) {
	feed := NativeTarget{
		Name: "Site (RSS)", Slug: "site-rss", DefinitionID: "site-rss",
		Status: TargetNative, SiteKey: "site.example", SiteGroup: "site.example",
		Channel: "latest", Config: map[string]string{"latest": "true"},
	}
	search := NativeTarget{
		Name: "Site", Slug: "site", DefinitionID: "site",
		Status: TargetNative, SiteKey: "site.example", SiteGroup: "site.example",
	}
	targets := PairChannels([]NativeTarget{feed, search})
	kept, dropped := Deduplicate(targets)
	if len(kept) != 2 || len(dropped) != 0 {
		t.Fatalf("kept=%d dropped=%d targets=%+v", len(kept), len(dropped), kept)
	}
	byChannel := map[string]NativeTarget{}
	for _, target := range kept {
		byChannel[target.Channel] = target
	}
	if byChannel["latest"].Config["latest"] != "true" {
		t.Errorf("feed latest = %q", byChannel["latest"].Config["latest"])
	}
	if byChannel["search"].Config["latest"] != "false" {
		t.Errorf("search latest = %q", byChannel["search"].Config["latest"])
	}
	if byChannel["search"].Config["site"] != "site.example" {
		t.Errorf("search site = %q", byChannel["search"].Config["site"])
	}
}
