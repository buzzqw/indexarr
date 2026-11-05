package web

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

func TestReleaseSendToDownloadClient(t *testing.T) {
	env := newTestEnv(t)
	added := false
	client := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/add":
			added = true
			_ = r.ParseMultipartForm(4 << 20)
			if got := r.FormValue("urls"); !strings.HasPrefix(got, "magnet:") {
				t.Errorf("urls = %q", got)
			}
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
	settings.DownloadClientCategory = "indexarr"
	if err := env.store.SaveSettings(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	insertSearchRelease(t, env.store, "Send Test 1080p", "5040")
	releases, _, err := env.store.SearchReleases(context.Background(), store.Filter{})
	if err != nil || len(releases) != 1 {
		t.Fatalf("releases = %d, %v", len(releases), err)
	}
	release := releases[0]
	token, cookie := csrfFrom(t, env.handler, "/releases/"+strconvI(release.ID))
	page := get(t, env.handler, "/releases/"+strconvI(release.ID)).Body.String()
	if !strings.Contains(page, "Send to qBittorrent") {
		t.Fatal("send button missing")
	}
	recorder := postForm(t, env.handler, "/releases/"+strconvI(release.ID)+"/send",
		url.Values{"csrf": {token}}, cookie)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("send status = %d: %s", recorder.Code, recorder.Body.String())
	}
	if !added {
		t.Error("qBittorrent add was not called")
	}
}

// seedEd2kRelease stores an eD2k release and links it to the indexer that
// supplied it, so IndexerForRelease can attribute the grab.
func seedEd2kRelease(t *testing.T, st *store.Store, indexerID int64, title, link string) store.Release {
	t.Helper()
	now := time.Now()
	release := store.Release{
		DedupKey:    "ed2k:" + title,
		Title:       title,
		SearchTitle: store.NormalizeSearch(title),
		Size:        700,
		Protocol:    "ed2k",
		Ed2k:        link,
		Ed2kHash:    "0123456789abcdef0123456789abcdef",
		FirstSeenAt: now,
		LastSeenAt:  now,
	}
	if _, err := st.UpsertRelease(context.Background(), &release); err != nil {
		t.Fatalf("UpsertRelease: %v", err)
	}
	if err := st.RecordReleaseSource(context.Background(), store.ReleaseSource{
		ReleaseID: release.ID, IndexerID: indexerID, FirstSeenAt: now, LastSeenAt: now,
	}); err != nil {
		t.Fatalf("RecordReleaseSource: %v", err)
	}
	return release
}

func grabStatuses(t *testing.T, st *store.Store, indexerID int64) []string {
	t.Helper()
	rows, err := st.ListIndexerHistory(context.Background(), indexerID, 20)
	if err != nil {
		t.Fatalf("ListIndexerHistory: %v", err)
	}
	var out []string
	for _, row := range rows {
		if row.Kind == "grab" {
			out = append(out, row.Status)
		}
	}
	return out
}

// An eD2k download from the release list passes through indexarr, so the source
// gets a grab row and the release is marked downloaded.
func TestEd2kListDownloadIsRecorded(t *testing.T) {
	env := newNativeEnv(t)
	ctx := context.Background()
	seedDefinition(t, env.store, "ed2k-src", "", `schema: 1
meta:
  id: ed2k-src
  name: ed2k-src
  type: public
  protocol: ed2k
  urls: [https://ed2k-src.example/]
search:
  request:
    get: /
  response:
    type: html
`)
	indexerID := seedPhase2Indexer(t, env.store, "ed2k-src", "ed2k-src", true, `{}`)
	const link = "ed2k://|file|Rare%20Movie.mkv|1234|0123456789abcdef0123456789abcdef|/"
	release := seedEd2kRelease(t, env.store, indexerID, "Rare Movie", link)

	recorder := get(t, env.handler, "/releases/"+strconvI(release.ID)+"/download")
	if recorder.Code != http.StatusFound {
		t.Fatalf("download status = %d: %s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Location"); got != link {
		t.Errorf("Location = %q, want %q", got, link)
	}
	if statuses := grabStatuses(t, env.store, indexerID); len(statuses) != 1 || statuses[0] != "ed2k" {
		t.Errorf("grab rows = %v, want one ed2k", statuses)
	}
	stored, err := env.store.GetRelease(ctx, release.ID)
	if err != nil {
		t.Fatalf("GetRelease: %v", err)
	}
	if stored.DownloadedAt.IsZero() {
		t.Error("the release was not marked downloaded")
	}
}

// Sending an eD2k release to aMule is a delivery indexarr made: it must leave
// the same grab row.
func TestEd2kSendToAMuleIsRecorded(t *testing.T) {
	env := newNativeEnv(t)
	ctx := context.Background()
	amule := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/login", "/api/v1/downloads":
			_, _ = w.Write([]byte(`{"results":[{"ok":true}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer amule.Close()

	token, cookie := csrfFrom(t, env.handler, "/download-clients/new")
	created := postForm(t, env.handler, "/download-clients", url.Values{
		"csrf":     {token},
		"name":     {"aMule"},
		"kind":     {"amule"},
		"protocol": {"ed2k"},
		"url":      {amule.URL},
		"password": {"synthetic-amule-password"},
		"enabled":  {"on"},
	}, cookie)
	if created.Code != http.StatusSeeOther {
		t.Fatalf("create client = %d: %s", created.Code, created.Body.String())
	}

	seedDefinition(t, env.store, "ed2k-src", "", `schema: 1
meta:
  id: ed2k-src
  name: ed2k-src
  type: public
  protocol: ed2k
  urls: [https://ed2k-src.example/]
search:
  request:
    get: /
  response:
    type: html
`)
	indexerID := seedPhase2Indexer(t, env.store, "ed2k-src", "ed2k-src", true, `{}`)
	const link = "ed2k://|file|Rare%20Movie.mkv|1234|0123456789abcdef0123456789abcdef|/"
	release := seedEd2kRelease(t, env.store, indexerID, "Rare Movie", link)

	token, cookie = csrfFrom(t, env.handler, "/releases/"+strconvI(release.ID))
	sent := postForm(t, env.handler, "/releases/"+strconvI(release.ID)+"/send",
		url.Values{"csrf": {token}}, cookie)
	if sent.Code != http.StatusSeeOther {
		t.Fatalf("send status = %d: %s", sent.Code, sent.Body.String())
	}
	if statuses := grabStatuses(t, env.store, indexerID); len(statuses) != 1 || statuses[0] != "ed2k" {
		t.Errorf("grab rows = %v, want one ed2k", statuses)
	}
	stored, err := env.store.GetRelease(ctx, release.ID)
	if err != nil {
		t.Fatalf("GetRelease: %v", err)
	}
	if stored.DownloadedAt.IsZero() {
		t.Error("the release was not marked downloaded")
	}
}

// A row button sends one release and returns to the same filtered results
// page instead of the detail page.
func TestReleaseSendReturnsToTheResultsPage(t *testing.T) {
	env := newTestEnv(t)
	client := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login", "/api/v2/torrents/add":
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
	insertSearchRelease(t, env.store, "Return Test 1080p", "5040")
	releases, _, err := env.store.SearchReleases(context.Background(), store.Filter{})
	if err != nil || len(releases) != 1 {
		t.Fatalf("releases = %d, %v", len(releases), err)
	}
	token, cookie := csrfFrom(t, env.handler, "/releases")
	recorder := postForm(t, env.handler, "/releases/"+strconvI(releases[0].ID)+"/send",
		url.Values{"csrf": {token}, "return": {"/releases?q=Return"}}, cookie)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("send status = %d: %s", recorder.Code, recorder.Body.String())
	}
	location := recorder.Header().Get("Location")
	if !strings.HasPrefix(location, "/releases?q=Return&msg=") {
		t.Errorf("Location = %q, want the results page", location)
	}
}

// A source can pin one download client: the release from that indexer goes
// there even when another client is the protocol default.
func TestReleaseSendUsesThePerIndexerClient(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	var aAdded, bAdded bool
	serverA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/add":
			aAdded = true
			_, _ = w.Write([]byte("Ok."))
		}
	}))
	defer serverA.Close()
	serverB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/add":
			bAdded = true
			_, _ = w.Write([]byte("Ok."))
		}
	}))
	defer serverB.Close()

	clientA, err := env.store.CreateDownloadClient(ctx, store.DownloadClient{
		Name: "Default", Kind: "qbittorrent", Protocol: "torrent",
		URL: serverA.URL, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	clientB, err := env.store.CreateDownloadClient(ctx, store.DownloadClient{
		Name: "Pinned", Kind: "qbittorrent", Protocol: "torrent",
		URL: serverB.URL, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if clientA == clientB {
		t.Fatal("the two clients share an id")
	}
	seedHomeDefinition(t, env, "pinned-def")
	indexerID, err := env.store.CreateIndexer(ctx, store.Indexer{
		DefinitionID: "pinned-def", Protocol: "torrent", Slug: "pinned",
		Name: "Pinned source", Enabled: true, Priority: 25,
		DownloadClientID: clientB, ConfigJSON: "{}",
	})
	if err != nil {
		t.Fatal(err)
	}
	insertSearchRelease(t, env.store, "Pinned Release 1080p", "5040")
	releases, _, err := env.store.SearchReleases(ctx, store.Filter{})
	if err != nil || len(releases) != 1 {
		t.Fatalf("releases = %d, %v", len(releases), err)
	}
	now := time.Now()
	if err := env.store.RecordReleaseSource(ctx, store.ReleaseSource{
		ReleaseID: releases[0].ID, IndexerID: indexerID, FirstSeenAt: now, LastSeenAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	token, cookie := csrfFrom(t, env.handler, "/releases")
	recorder := postForm(t, env.handler, "/releases/"+strconvI(releases[0].ID)+"/send",
		url.Values{"csrf": {token}}, cookie)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("send status = %d: %s", recorder.Code, recorder.Body.String())
	}
	if aAdded {
		t.Error("the default client received a release pinned to another client")
	}
	if !bAdded {
		t.Error("the pinned client did not receive the release")
	}
}
