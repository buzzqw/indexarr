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
	if torrent.DefinitionID != "1337x" || torrent.Status != TargetNative {
		t.Fatalf("1337x target = %+v", torrent)
	}
	if torrent.DefinitionState != "verificata" {
		t.Fatalf("1337x state = %q", torrent.DefinitionState)
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
	if usenet.Status != TargetPending || usenet.DefinitionID != "" {
		t.Fatalf("a source without a native route must stay pending: %+v", usenet)
	}
	if usenet.Reason != ReasonNoDefinition {
		t.Fatalf("pending reason = %q", usenet.Reason)
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
	if extto.DefinitionID != "extto" || extto.Status != TargetNative {
		t.Fatalf("ExtTo should use the verified hint: %+v", extto)
	}
	if extto.Config["filter"] != "c=TV" {
		t.Fatalf("ExtTo default setting missing: %+v", extto.Config)
	}
	if extto.Protocol != ProtocolTorrent || !extto.Enabled {
		t.Fatalf("ExtTo protocol/enabled = %q/%t", extto.Protocol, extto.Enabled)
	}

	galaxy := findTarget(t, targets, "Torrent Galaxy Mirror")
	if galaxy.DefinitionID != "torrentgalaxy" || galaxy.Status != TargetNative {
		t.Fatalf("host match should select torrentgalaxy: %+v", galaxy)
	}
	if galaxy.BaseURL != "https://torrentgalaxy.one/" {
		t.Fatalf("galaxy base URL = %q", galaxy.BaseURL)
	}

	corsaro := findTarget(t, targets, "Corsaro")
	if corsaro.DefinitionID != "corsaro" || corsaro.Status != TargetNative {
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
	if leech.DefinitionID != "torrentleech" || leech.Status != TargetNative {
		t.Fatalf("a draft definition is still a native route: %+v", leech)
	}
	if leech.DefinitionState != "bozza" {
		t.Fatalf("TorrentLeech state = %q", leech.DefinitionState)
	}
	if leech.Enabled || leech.Reason != ReasonMissingCredentials {
		t.Fatalf("TorrentLeech must wait for credentials: %+v", leech)
	}

	alpha := findTarget(t, targets, "Alpha")
	if alpha.DefinitionID != "torznab-generic" || alpha.Status != TargetNative {
		t.Fatalf("a Torznab implementation is a native generic source: %+v", alpha)
	}
	if alpha.Config["url"] != "https://alpha.example.invalid/" {
		t.Fatalf("Alpha endpoint = %q", alpha.Config["url"])
	}
	if alpha.Secrets["api_key"] != "alpha-key-SECRET" {
		t.Fatalf("Alpha API key was not preserved")
	}
	if strings.Contains(alpha.BaseURL, prowlarrAPIKey) {
		t.Fatalf("Prowlarr API key leaked into the endpoint: %q", alpha.BaseURL)
	}
	if alpha.Config["categories"] != "2000,5000" {
		t.Fatalf("Alpha categories = %q", alpha.Config["categories"])
	}

	disabled := findTarget(t, targets, "Disabled Site")
	if disabled.Status != TargetPending {
		t.Fatalf("Disabled Site = %+v", disabled)
	}

	requireSecretsAbsent(t, targetText(targets), prowlarrAPIKey, "alpha-key-SECRET",
		"corsaro-user-SECRET", "corsaro-pass-SECRET", "tl-rsskey-SECRET")
}

func TestFamilySiteBecomesNativeTarget(t *testing.T) {
	entries := builtinEntries(t)
	indexer := Indexer{
		Name:     "13City",
		Protocol: ProtocolTorrent,
		RemoteID: "13city",
		SiteURL:  "https://13city.org/",
		Username: "user-SECRET",
		Password: "pass-SECRET",
		Enabled:  true,
		Priority: 12,
		Tags:     []string{"tv"},
	}
	target := Target("jackett", "http://jackett:9117", "manager-key-SECRET", indexer, entries)
	if target.Status != TargetFamily || target.Family != catalog.FamilyNexusPHP {
		t.Fatalf("family target = %+v", target)
	}
	if target.DefinitionID != "jk-13city" || len(target.DefinitionBody) == 0 {
		t.Fatalf("family definition not synthesized: %+v", target)
	}
	if target.Config["url"] != "https://13city.org/" {
		t.Fatalf("family base URL = %q", target.Config["url"])
	}
	if target.Secrets["username"] != "user-SECRET" || target.Secrets["password"] != "pass-SECRET" {
		t.Fatalf("family credentials were not preserved: %+v", target.Secrets)
	}
	if _, ok := target.Secrets["passkey"]; ok {
		t.Fatalf("the account password must not be copied into the passkey setting")
	}
	if !target.Enabled || target.Reason != "" {
		t.Fatalf("family target should be enabled: %+v", target)
	}
}

func TestFamilySiteWithoutCredentialsIsDisabled(t *testing.T) {
	entries := builtinEntries(t)
	target := Target("jackett", "http://jackett:9117", "manager-key-SECRET", Indexer{
		Name:     "13City",
		Protocol: ProtocolTorrent,
		RemoteID: "13city",
		SiteURL:  "https://13city.org/",
		Enabled:  true,
	}, entries)
	if target.Status != TargetFamily {
		t.Fatalf("target = %+v", target)
	}
	if target.Enabled || target.Reason != ReasonMissingCredentials {
		t.Fatalf("a family site without credentials must be disabled: %+v", target)
	}
	if target.ReasonDetail == "" {
		t.Fatalf("the missing setting should be named")
	}
}

func TestTargetKeepsUpstreamMetadata(t *testing.T) {
	entries := builtinEntries(t)
	indexer := Indexer{
		Name:           "Some Usenet",
		Implementation: "Newznab",
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
	if target.DefinitionID != "newznab-generic" || target.Status != TargetNative {
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
	if target.Secrets["api_key"] != "api-SECRET" {
		t.Fatalf("credentials were not preserved as secrets")
	}
	if target.BaseURL != "https://some.example.invalid/" {
		t.Fatalf("native endpoint = %q", target.BaseURL)
	}
}

func TestTargetNeverBuildsManagerEndpoint(t *testing.T) {
	entries := builtinEntries(t)
	target := Target("prowlarr", "http://192.168.1.161:9696", "manager-key-SECRET", Indexer{
		Name: "Local", DefinitionHint: "unknown-local-site", RemoteID: "7",
		SiteURL: "https://tracker.example.invalid/", Protocol: ProtocolTorrent, Enabled: true,
	}, entries)
	if target.Status != TargetPending {
		t.Fatalf("target = %+v", target)
	}
	if target.BaseURL != "https://tracker.example.invalid/" || target.Config["allow_private"] != "" {
		t.Fatalf("a pending target must keep the site URL only: %+v", target)
	}
	if _, ok := target.Secrets["api_key"]; ok {
		t.Fatalf("a pending target must not carry the manager API key")
	}
}

func TestDraftHintIsNativeAndUnknownHostIsPending(t *testing.T) {
	entries := builtinEntries(t)
	draft := Target("jackett", "http://jackett:9117", "jackett-key", Indexer{
		Name: "TorrentLeech", Protocol: ProtocolTorrent,
		DefinitionHint: "torrentleech",
		BaseURL:        "https://rss.torrentleech.org/",
		SiteURL:        "https://rss.torrentleech.org/",
	}, entries)
	if draft.Status != TargetNative || draft.DefinitionID != "torrentleech" {
		t.Fatalf("draft target = %+v", draft)
	}

	unknown := Target("jackett", "http://jackett:9117", "jackett-key", Indexer{
		Name: "Unknown", Protocol: ProtocolTorrent, RemoteID: "9",
		DefinitionHint: "unknown-tracker",
		BaseURL:        "https://unknown.example.invalid/",
		SiteURL:        "https://unknown.example.invalid/",
	}, entries)
	if unknown.Status != TargetPending || unknown.Reason != ReasonNoDefinition {
		t.Fatalf("unknown target = %+v", unknown)
	}
}

func TestTargetKeyIsStable(t *testing.T) {
	a := NativeTarget{DefinitionID: "extto", BaseURL: "HTTPS://ExtTo.org/", Slug: "extto"}
	b := NativeTarget{DefinitionID: "extto", BaseURL: "https://extto.org", Slug: "extto"}
	if a.Key() != b.Key() {
		t.Fatalf("keys differ: %q vs %q", a.Key(), b.Key())
	}
	c := NativeTarget{DefinitionID: "", BaseURL: "https://extto.org", Slug: "extto"}
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
