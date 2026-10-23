package importers

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/buzzqw/indexarr/internal/catalog"
)

func builtinEntries(t *testing.T) []catalog.Entry {
	t.Helper()
	entries, err := catalog.Builtins()
	if err != nil {
		t.Fatalf("catalog.Builtins: %v", err)
	}
	return entries
}

func findTarget(t *testing.T, targets []NativeTarget, name string) NativeTarget {
	t.Helper()
	for _, target := range targets {
		if target.Name == name {
			return target
		}
	}
	t.Fatalf("target %q not found in %+v", name, targets)
	return NativeTarget{}
}

func TestNativeTargetsFromJackettFixture(t *testing.T) {
	plan, err := FromJackettConfig(context.Background(), filepath.Join("testdata", "jackett"))
	if err != nil {
		t.Fatalf("FromJackettConfig: %v", err)
	}
	if plan.ManagerURL != "http://127.0.0.1:9117" {
		t.Fatalf("ManagerURL = %q", plan.ManagerURL)
	}

	targets, err := Targets(plan)
	if err != nil {
		t.Fatalf("Targets: %v", err)
	}
	if len(targets) != 2 {
		t.Fatalf("got %d targets, want 2: %+v", len(targets), targets)
	}

	torrent := findTarget(t, targets, "1337x")
	// 1337x is now a verified native definition, so the verified catalog wins
	// over the manager bridge for the same hint.
	if torrent.DefinitionID != "1337x" || torrent.ViaManager != "" {
		t.Fatalf("1337x target = %+v", torrent)
	}
	if torrent.BaseURL != "https://1337x.example.invalid/" {
		t.Fatalf("1337x endpoint = %q", torrent.BaseURL)
	}
	if _, ok := torrent.Secrets["api_key"]; ok {
		t.Fatalf("the native definition must not carry the Jackett API key")
	}
	if !torrent.Enabled || torrent.Priority != 25 {
		t.Fatalf("1337x enabled/priority = %t/%d", torrent.Enabled, torrent.Priority)
	}
	if len(torrent.Tags) != 1 || torrent.Tags[0] != "movies" {
		t.Fatalf("1337x tags = %v", torrent.Tags)
	}
	if torrent.RemoteID != "1337x" || torrent.Slug != "1337x" {
		t.Fatalf("1337x remote id/slug = %q/%q", torrent.RemoteID, torrent.Slug)
	}
	if torrent.Protocol != ProtocolTorrent {
		t.Fatalf("1337x protocol = %q", torrent.Protocol)
	}

	usenet := findTarget(t, targets, "Test Usenet")
	if usenet.DefinitionID != nativeDefinition || usenet.ViaManager != "jackett" {
		t.Fatalf("usenet target = %+v", usenet)
	}
	if usenet.Enabled {
		t.Fatalf("disabled upstream indexer was re-enabled")
	}
	if usenet.Protocol != ProtocolUsenet {
		t.Fatalf("usenet protocol = %q", usenet.Protocol)
	}
	requireSecretsAbsent(t, targetText(targets), jackettAPIKey)
}

func TestNativeTargetsFromProwlarrFixture(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("testdata", "prowlarr", "indexers.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/indexer":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(body)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	plan, err := FromProwlarr(context.Background(), server.URL, prowlarrAPIKey, server.Client())
	if err != nil {
		t.Fatalf("FromProwlarr: %v", err)
	}
	targets, err := Targets(plan)
	if err != nil {
		t.Fatalf("Targets: %v", err)
	}
	if len(targets) != len(plan.Indexers) {
		t.Fatalf("got %d targets for %d indexers", len(targets), len(plan.Indexers))
	}

	extto := findTarget(t, targets, "ExtTo")
	if extto.DefinitionID != "extto" || extto.ViaManager != "" {
		t.Fatalf("ExtTo should use the verified hint: %+v", extto)
	}
	if extto.Config["filter"] != "c=TV" {
		t.Fatalf("ExtTo default setting missing: %+v", extto.Config)
	}
	if extto.Protocol != ProtocolTorrent || !extto.Enabled {
		t.Fatalf("ExtTo protocol/enabled = %q/%t", extto.Protocol, extto.Enabled)
	}

	galaxy := findTarget(t, targets, "Torrent Galaxy Mirror")
	if galaxy.DefinitionID != "torrentgalaxy" || galaxy.ViaManager != "" {
		t.Fatalf("host match should select torrentgalaxy: %+v", galaxy)
	}
	if galaxy.BaseURL != "https://torrentgalaxy.one/" {
		t.Fatalf("galaxy base URL = %q", galaxy.BaseURL)
	}

	corsaro := findTarget(t, targets, "Corsaro")
	if corsaro.DefinitionID != "corsaro" {
		t.Fatalf("Corsaro should use the verified hint: %+v", corsaro)
	}

	knaben := findTarget(t, targets, "Knaben")
	if knaben.DefinitionID != "knaben" {
		t.Fatalf("Knaben should use the verified rss definition: %+v", knaben)
	}
	if knaben.Config["url"] != "https://rss.knaben.org/ita" {
		t.Fatalf("Knaben feed URL = %q", knaben.Config["url"])
	}

	leech := findTarget(t, targets, "TorrentLeech")
	if leech.DefinitionID != nativeDefinition || leech.ViaManager != "prowlarr" {
		t.Fatalf("a bozza definition must fall back to the bridge: %+v", leech)
	}
	if leech.BaseURL != server.URL+"/15/api" {
		t.Fatalf("TorrentLeech endpoint = %q", leech.BaseURL)
	}

	alpha := findTarget(t, targets, "Alpha")
	if alpha.BaseURL != server.URL+"/16/api" || alpha.Secrets["api_key"] != prowlarrAPIKey {
		t.Fatalf("Alpha target = %+v", alpha)
	}
	if strings.Contains(alpha.BaseURL, prowlarrAPIKey) {
		t.Fatalf("Prowlarr API key leaked into the endpoint: %q", alpha.BaseURL)
	}
	if alpha.Config["categories"] != "2000,5000" {
		t.Fatalf("Alpha categories = %q", alpha.Config["categories"])
	}

	disabled := findTarget(t, targets, "Disabled Site")
	if disabled.Enabled {
		t.Fatalf("disabled Prowlarr indexer was re-enabled")
	}
	if disabled.DefinitionID != nativeDefinition {
		t.Fatalf("Disabled Site definition = %q", disabled.DefinitionID)
	}

	requireSecretsAbsent(t, targetText(targets), prowlarrAPIKey, "alpha-key-SECRET",
		"corsaro-user-SECRET", "corsaro-pass-SECRET", "tl-rsskey-SECRET")
}

func TestTargetKeepsUpstreamMetadata(t *testing.T) {
	entries := builtinEntries(t)
	indexer := Indexer{
		Name:           "Some Tracker",
		Protocol:       ProtocolUsenet,
		DefinitionHint: "some-tracker",
		RemoteID:       "42",
		BaseURL:        "https://some.example.invalid/",
		SiteURL:        "https://some.example.invalid/",
		APIKey:         "api-SECRET",
		Username:       "user-SECRET",
		Password:       "pass-SECRET",
		Categories:     []int{5000, 2000},
		Tags:           []string{"tv", "hd"},
		Priority:       7,
		Enabled:        false,
	}
	target := Target("prowlarr", "http://prowlarr:9696", "manager-key-SECRET", indexer, entries)
	if target.DefinitionID != nativeDefinition || target.ViaManager != "prowlarr" {
		t.Fatalf("target = %+v", target)
	}
	if target.Enabled || target.Priority != 7 || target.Protocol != ProtocolUsenet {
		t.Fatalf("metadata not preserved: %+v", target)
	}
	if strings.Join(target.Tags, ",") != "tv,hd" {
		t.Fatalf("tags = %v", target.Tags)
	}
	if target.Config["categories"] != "2000,5000" {
		t.Fatalf("categories = %q", target.Config["categories"])
	}
	if target.Secrets["api_key"] != "manager-key-SECRET" ||
		target.Secrets["username"] != "user-SECRET" ||
		target.Secrets["password"] != "pass-SECRET" {
		t.Fatalf("credentials were not preserved as secrets")
	}
	if target.BaseURL != "http://prowlarr:9696/42/api" {
		t.Fatalf("Prowlarr endpoint = %q", target.BaseURL)
	}
}

func TestBridgeToLocalManagerAllowsPrivateAddress(t *testing.T) {
	entries := builtinEntries(t)
	target := Target("prowlarr", "http://192.168.1.161:9696", "manager-key-SECRET", Indexer{
		Name: "Local", DefinitionHint: "unknown-local-site", RemoteID: "7",
		SiteURL: "http://192.168.1.161:9696/7", Protocol: ProtocolTorrent, Enabled: true,
	}, entries)
	if target.Config["allow_private"] != "true" {
		t.Fatalf("bridge to a LAN manager did not opt into private addresses: %+v", target.Config)
	}
	public := Target("prowlarr", "https://prowlarr.example.invalid", "manager-key-SECRET", Indexer{
		Name: "Public", DefinitionHint: "unknown-public-site", RemoteID: "8",
		SiteURL: "https://tracker.example.invalid/", Protocol: ProtocolTorrent, Enabled: true,
	}, entries)
	if _, ok := public.Config["allow_private"]; ok {
		t.Fatalf("public manager should not opt into private addresses: %+v", public.Config)
	}
}

func TestTargetFallsBackWithoutVerifiedDefinition(t *testing.T) {
	entries := builtinEntries(t)
	cases := []struct {
		name    string
		indexer Indexer
	}{
		{
			name: "draft hint",
			indexer: Indexer{
				Name: "TorrentLeech", Protocol: ProtocolTorrent,
				DefinitionHint: "torrentleech",
				BaseURL:        "https://rss.torrentleech.org/",
				SiteURL:        "https://rss.torrentleech.org/",
			},
		},
		{
			name: "unknown host",
			indexer: Indexer{
				Name: "Unknown", Protocol: ProtocolTorrent,
				DefinitionHint: "unknown-tracker",
				BaseURL:        "https://unknown.example.invalid/",
				SiteURL:        "https://unknown.example.invalid/",
			},
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			test.indexer.RemoteID = "9"
			target := Target("jackett", "http://jackett:9117", "jackett-key", test.indexer, entries)
			if target.DefinitionID != nativeDefinition || target.ViaManager != "jackett" {
				t.Fatalf("target = %+v", target)
			}
			want := "http://jackett:9117/api/v2.0/indexers/9/results/torznab/"
			if target.BaseURL != want {
				t.Fatalf("endpoint = %q, want %q", target.BaseURL, want)
			}
		})
	}
}

func TestTargetKeyIsStable(t *testing.T) {
	a := NativeTarget{DefinitionID: "extto", BaseURL: "HTTPS://ExtTo.org/", Slug: "extto"}
	b := NativeTarget{DefinitionID: "extto", BaseURL: "https://extto.org", Slug: "extto"}
	if a.Key() != b.Key() {
		t.Fatalf("keys differ: %q vs %q", a.Key(), b.Key())
	}
	c := NativeTarget{DefinitionID: nativeDefinition, BaseURL: "https://extto.org", Slug: "extto"}
	if a.Key() == c.Key() {
		t.Fatalf("definition must be part of the idempotency key")
	}
}

func TestFromProwlarrUsesOnlyGet(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("testdata", "prowlarr", "indexers.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	var mu sync.Mutex
	var methods []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		methods = append(methods, r.Method)
		mu.Unlock()
		if r.Method != http.MethodGet {
			t.Errorf("Prowlarr received %s %s, only GET is allowed", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		switch r.URL.Path {
		case "/api/v1/indexer":
			_, _ = io.WriteString(w, string(body))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	if _, err := FromProwlarr(context.Background(), server.URL, prowlarrAPIKey, server.Client()); err != nil {
		t.Fatalf("FromProwlarr: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(methods) == 0 {
		t.Fatal("no request was made")
	}
	for _, method := range methods {
		if method != http.MethodGet {
			t.Fatalf("recorded method %q, want GET", method)
		}
	}
}

// targetText renders everything a target may print, excluding the secret
// values: it makes sure the readable fields never embed credentials.
func targetText(targets []NativeTarget) string {
	var b strings.Builder
	for _, target := range targets {
		b.WriteString(target.Name)
		b.WriteByte(' ')
		b.WriteString(target.DefinitionID)
		b.WriteByte(' ')
		b.WriteString(target.BaseURL)
		b.WriteByte(' ')
		for key, value := range target.Config {
			b.WriteString(key)
			b.WriteByte('=')
			b.WriteString(value)
			b.WriteByte(' ')
		}
		b.WriteString("\n")
	}
	return b.String()
}
