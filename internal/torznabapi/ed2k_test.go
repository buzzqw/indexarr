package torznabapi

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/buzzqw/indexarr/internal/store"
)

func insertEd2kRelease(t *testing.T, st *store.Store, title, link, hash string) int64 {
	t.Helper()
	release := store.Release{
		DedupKey:    "ed2k:" + hash,
		Title:       title,
		SearchTitle: store.NormalizeSearch(title),
		Size:        1234,
		Category:    "2000",
		Protocol:    "ed2k",
		Ed2k:        link,
		Ed2kHash:    hash,
		PublishedAt: time.Now().Add(-time.Hour),
		GUID:        "ed2k:" + hash,
		FirstSeenAt: time.Now().Add(-time.Hour),
		LastSeenAt:  time.Now(),
	}
	if _, err := st.UpsertRelease(context.Background(), &release); err != nil {
		t.Fatalf("UpsertRelease: %v", err)
	}
	return release.ID
}

func TestEd2kItemAndGrab(t *testing.T) {
	api, st := newAPI(t)
	const link = "ed2k://|file|Rare%20Movie.mkv|1234|0123456789abcdef0123456789abcdef|/"
	id := insertEd2kRelease(t, st, "Rare Movie", link, "0123456789abcdef0123456789abcdef")

	recorder := do(t, api, "/api?t=search&apikey=secret&q=Rare")
	if recorder.Code != http.StatusOK {
		t.Fatalf("search status = %d: %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	for _, want := range []string{
		`<link>` + link + `</link>`,
		`<guid isPermaLink="false">0123456789abcdef0123456789abcdef</guid>`,
		`<torznab:attr name="ed2k" value="` + link + `"/>`,
		`<torznab:attr name="ed2khash" value="0123456789abcdef0123456789abcdef"/>`,
		`type="application/x-ed2k"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q\n%s", want, body)
		}
	}
	// The swarm of an eD2k file is unknown: 0/0 would read as a dead swarm.
	for _, unwanted := range []string{"magnet:", "application/x-bittorrent", "application/x-nzb",
		`name="seeders"`, `name="leechers"`, `name="peers"`} {
		if strings.Contains(body, unwanted) {
			t.Errorf("eD2k item contains %q\n%s", unwanted, body)
		}
	}

	grab := do(t, api, "/dl/"+itoa(id)+".torrent?apikey=secret")
	if grab.Code != http.StatusFound {
		t.Fatalf("grab status = %d: %s", grab.Code, grab.Body.String())
	}
	if location := grab.Header().Get("Location"); location != link {
		t.Errorf("Location = %q, want %q", location, link)
	}
}

func TestLiveWaitOverride(t *testing.T) {
	cases := map[string]time.Duration{
		"":   0,
		"0":  0,
		"-3": 0,
		"5":  5 * time.Second,
		"45": 45 * time.Second,
		"99": 60 * time.Second,
	}
	for raw, want := range cases {
		if got := liveWaitOverride(raw); got != want {
			t.Errorf("liveWaitOverride(%q) = %v, want %v", raw, got, want)
		}
	}
}
