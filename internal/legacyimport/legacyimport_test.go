package legacyimport

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/buzzqw/indexarr/internal/store"
)

const (
	urlSecret      = "URL-SECRET-KEY"
	urlPassword    = "URL-SECRET-PASSWORD"
	sourceAPIKey   = "SOURCE-API-KEY-SECRET"
	sourcePassword = "SOURCE-PASSWORD-SECRET"
	feedSecret     = "FEED-PASSKEY-SECRET"
)

func newStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "indexarr.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func createSource(t *testing.T, st *store.Store, in store.Source) int64 {
	t.Helper()
	id, err := st.CreateSource(context.Background(), in)
	if err != nil {
		t.Fatalf("CreateSource(%q): %v", in.Name, err)
	}
	return id
}

func TestPreview(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	alpha := createSource(t, st, store.Source{
		Name:     "Alpha Torznab",
		Kind:     "torznab",
		Manager:  "auto",
		URL:      "https://user:" + urlPassword + "@tracker.example/api?apikey=" + urlSecret + "&cat=5000",
		APIKey:   sourceAPIKey,
		Username: "user",
		Password: sourcePassword,
		Enabled:  true,
	})
	beta := createSource(t, st, store.Source{
		Name:    "Beta RSS",
		Kind:    "rss",
		Manager: "auto",
		URL:     "https://feed.example/rss?passkey=" + feedSecret,
		APIKey:  sourceAPIKey,
		Enabled: true,
	})
	ftp := createSource(t, st, store.Source{
		Name:    "Gamma FTP",
		Kind:    "torznab",
		Manager: "auto",
		URL:     "ftp://tracker.example/api",
		Enabled: true,
	})
	invalid := createSource(t, st, store.Source{
		Name:    "Delta Invalid",
		Kind:    "torznab",
		Manager: "auto",
		URL:     "not-a-url",
		Enabled: true,
	})
	basic := createSource(t, st, store.Source{
		Name:     "Epsilon Basic",
		Kind:     "torznab",
		Manager:  "auto",
		URL:      "http://localhost:9117/api",
		Username: "user",
		Password: sourcePassword,
		Enabled:  true,
	})

	// The backfill creates the bridged indexers and their legacy definitions.
	if _, err := st.BackfillLegacySources(ctx); err != nil {
		t.Fatalf("BackfillLegacySources: %v", err)
	}
	indexers, err := st.ListIndexers(ctx)
	if err != nil {
		t.Fatalf("ListIndexers: %v", err)
	}
	bridged := make(map[int64]string, len(indexers))
	for _, in := range indexers {
		if in.LegacySourceID > 0 {
			bridged[in.LegacySourceID] = in.Slug
		}
	}

	// A source added after the backfill has no bridged indexer yet.
	unbridged := createSource(t, st, store.Source{
		Name:    "Zeta New Torznab",
		Kind:    "torznab",
		Manager: "auto",
		URL:     "https://new.example/api",
		Enabled: true,
	})

	before, err := st.ListSources(ctx)
	if err != nil {
		t.Fatalf("ListSources before: %v", err)
	}

	candidates, err := Preview(ctx, st)
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if len(candidates) != 6 {
		t.Fatalf("got %d candidates, want 6: %+v", len(candidates), candidates)
	}

	after, err := st.ListSources(ctx)
	if err != nil {
		t.Fatalf("ListSources after: %v", err)
	}
	if len(before) != len(after) {
		t.Fatalf("Preview changed the sources: %d -> %d", len(before), len(after))
	}
	for i := range before {
		if before[i].ID != after[i].ID || before[i].Enabled != after[i].Enabled ||
			before[i].URL != after[i].URL || before[i].APIKey != after[i].APIKey {
			t.Fatalf("Preview changed source %d", before[i].ID)
		}
	}

	byID := make(map[int64]Candidate, len(candidates))
	for _, candidate := range candidates {
		byID[candidate.SourceID] = candidate
	}

	alphaCandidate := byID[alpha]
	if !alphaCandidate.Native || alphaCandidate.Reason != "" {
		t.Errorf("alpha = %+v, want native", alphaCandidate)
	}
	if alphaCandidate.Kind != "torznab" || alphaCandidate.Name != "Alpha Torznab" {
		t.Errorf("alpha identity = %+v", alphaCandidate)
	}
	if !alphaCandidate.HasAPIKey || !alphaCandidate.HasBasicAuth {
		t.Errorf("alpha credential flags = %+v", alphaCandidate)
	}
	if alphaCandidate.Slug != bridged[alpha] || alphaCandidate.Slug == "" {
		t.Errorf("alpha slug = %q, want bridged %q", alphaCandidate.Slug, bridged[alpha])
	}
	if alphaCandidate.URL != "https://tracker.example/api?cat=5000" {
		t.Errorf("alpha URL = %q", alphaCandidate.URL)
	}

	betaCandidate := byID[beta]
	if !betaCandidate.Native || betaCandidate.Reason != "" {
		t.Errorf("rss candidate = %+v, want native", betaCandidate)
	}
	if betaCandidate.Slug != bridged[beta] || betaCandidate.Slug == "" {
		t.Errorf("rss slug = %q, want bridged %q", betaCandidate.Slug, bridged[beta])
	}
	if betaCandidate.URL != "https://feed.example/rss" {
		t.Errorf("rss URL = %q", betaCandidate.URL)
	}

	if byID[ftp].Native || !strings.Contains(byID[ftp].Reason, "http or https") {
		t.Errorf("ftp candidate = %+v", byID[ftp])
	}
	if byID[invalid].Native || byID[invalid].Reason == "" {
		t.Errorf("invalid candidate = %+v", byID[invalid])
	}
	if !byID[basic].Native || !byID[basic].HasBasicAuth || byID[basic].HasAPIKey {
		t.Errorf("basic candidate = %+v", byID[basic])
	}
	if !byID[unbridged].Native {
		t.Errorf("unbridged candidate = %+v", byID[unbridged])
	}
	if byID[unbridged].Slug != "" {
		t.Errorf("unbridged slug = %q, want empty", byID[unbridged].Slug)
	}

	native, skipped := Counts(candidates)
	if native != 4 || skipped != 2 {
		t.Errorf("counts = %d native, %d skipped; want 4, 2", native, skipped)
	}

	encoded, err := json.Marshal(candidates)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	for _, secret := range []string{
		urlSecret, urlPassword, sourceAPIKey, sourcePassword, feedSecret,
	} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("preview leaked %q: %s", secret, encoded)
		}
	}
}

func TestPreviewClassifiesRSSFeeds(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	const rssSecret = "RSS-PATH-SECRET-0123456789"

	plain := createSource(t, st, store.Source{
		Name: "Plain Feed", Kind: "rss",
		URL: "https://feed.example/rss?rsskey=" + rssSecret, Enabled: true,
	})
	knaben := createSource(t, st, store.Source{
		Name: "Knaben", Kind: "rss",
		URL: "https://rss.knaben.org/ita///1337x", Enabled: true,
	})
	extto := createSource(t, st, store.Source{
		Name: "ExtTo listing", Kind: "rss",
		URL: "https://ext.to/browse/", Enabled: true,
	})
	galaxy := createSource(t, st, store.Source{
		Name: "TorrentGalaxy listing", Kind: "rss",
		URL: "https://torrentgalaxy.one/get-posts/user:MIRCrewRS/", Enabled: true,
	})
	corsaro := createSource(t, st, store.Source{
		Name: "Corsaro listing", Kind: "rss",
		URL: "https://corsaro.example/torrent/1", Enabled: true,
	})
	invalid := createSource(t, st, store.Source{
		Name: "Broken Feed", Kind: "rss", URL: "not-a-url", Enabled: true,
	})

	candidates, err := Preview(ctx, st)
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	byID := make(map[int64]Candidate, len(candidates))
	for _, candidate := range candidates {
		byID[candidate.SourceID] = candidate
	}

	for _, id := range []int64{plain, knaben} {
		if !byID[id].Native || byID[id].Reason != "" {
			t.Errorf("source %d = %+v, want native feed", id, byID[id])
		}
	}
	for id, site := range map[int64]string{
		extto:   "ExtTo",
		galaxy:  "TorrentGalaxy",
		corsaro: "Corsaro",
	} {
		candidate := byID[id]
		if candidate.Native {
			t.Errorf("%s listing marked native: %+v", site, candidate)
		}
		if !strings.Contains(strings.ToLower(candidate.Reason), "html listing") ||
			!strings.Contains(candidate.Reason, site) {
			t.Errorf("%s reason = %q, want an HTML-listing explanation", site, candidate.Reason)
		}
	}
	if byID[invalid].Native || byID[invalid].Reason == "" {
		t.Errorf("invalid RSS candidate = %+v", byID[invalid])
	}
	if got := byID[plain].URL; strings.Contains(got, rssSecret) {
		t.Errorf("preview leaked the feed key: %q", got)
	}
}

func TestHTMLListingSite(t *testing.T) {
	cases := map[string]string{
		"https://ext.to/browse/":                              "ExtTo",
		"https://www.extto.org/":                              "ExtTo",
		"https://torrentgalaxy.one/get-posts/user:MIRCrewRS/": "TorrentGalaxy",
		"https://corsaro.example/torrent/1":                   "Corsaro",
		"https://rss.knaben.org/ita///1337x":                  "",
		"https://feed.example/rss":                            "",
		"not-a-url":                                           "",
	}
	for raw, want := range cases {
		if got := htmlListingSite(raw); got != want {
			t.Errorf("htmlListingSite(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestCounts(t *testing.T) {
	if native, skipped := Counts(nil); native != 0 || skipped != 0 {
		t.Fatalf("Counts(nil) = %d, %d", native, skipped)
	}
	native, skipped := Counts([]Candidate{
		{Native: true}, {Native: false}, {Native: true}, {Native: false},
	})
	if native != 2 || skipped != 2 {
		t.Fatalf("Counts = %d, %d; want 2, 2", native, skipped)
	}
}

func TestPreviewRequiresStore(t *testing.T) {
	if _, err := Preview(context.Background(), nil); err == nil {
		t.Fatal("Preview(nil) succeeded, want error")
	}
}

func TestSanitizedURL(t *testing.T) {
	cases := map[string]string{
		"https://tracker.example/api?apikey=secret&cat=5000":       "https://tracker.example/api?cat=5000",
		"https://user:pass@tracker.example/api":                    "https://tracker.example/api",
		"https://tracker.example/api?api_key=x&api-token=y&cat=1":  "https://tracker.example/api?cat=1",
		"https://tracker.example/api?passkey=x#fragment":           "https://tracker.example/api",
		"https://tracker.example/api?password=x&rsskey=y&secret=z": "https://tracker.example/api",
		"": "",
	}
	for input, want := range cases {
		if got := sanitizedURL(input); got != want {
			t.Errorf("sanitizedURL(%q) = %q, want %q", input, got, want)
		}
	}
}
