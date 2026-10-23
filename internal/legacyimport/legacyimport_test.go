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
	if betaCandidate.Native {
		t.Errorf("rss candidate marked native: %+v", betaCandidate)
	}
	if !strings.Contains(strings.ToLower(betaCandidate.Reason), "rss") {
		t.Errorf("rss reason = %q", betaCandidate.Reason)
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
	if native != 3 || skipped != 3 {
		t.Errorf("counts = %d native, %d skipped; want 3, 3", native, skipped)
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
