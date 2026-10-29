package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/buzzqw/indexarr/internal/store"
)

func TestReleasesPageShowsLiveSearchAndBulkGrab(t *testing.T) {
	env := newTestEnv(t)
	insertSearchRelease(t, env.store, "Visible Release 1080p", "5040")
	body := get(t, env.handler, "/releases?lang=en").Body.String()
	for _, want := range []string{
		`action="/releases/live"`,
		`name="mode"`,
		`name="season"`,
		`name="episode"`,
		`name="year"`,
		`name="imdbid"`,
		`name="tmdbid"`,
		`name="tvdbid"`,
		`name="artist"`,
		`name="album"`,
		`name="author"`,
		`name="title"`,
		`name="indexers"`,
		`multiple`,
		`action="/releases/bulk"`,
		`name="op"`,
		`value="grab"`,
		`value="delete"`,
		`name="ids"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("releases page missing %q", want)
		}
	}
	if !strings.Contains(body, "page-help") {
		t.Error("releases page lost its explanatory paragraph")
	}
}

func TestLiveSearchRejectsMissingTerms(t *testing.T) {
	env := newTestEnv(t)
	token, cookie := csrfFrom(t, env.handler, "/releases")
	recorder := postForm(t, env.handler, "/releases/live",
		url.Values{"csrf": {token}, "mode": {"movie"}}, cookie)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "Type a query or an IMDb, TMDb, TVDb ID, artist, album, author or title.") {
		t.Errorf("validation message missing: %s", body)
	}
	if strings.Contains(body, "What each indexer did") {
		t.Error("an invalid live search ran the fan-out")
	}
}

func TestLiveSearchAsksForAtLeastOneIndexer(t *testing.T) {
	env := newTestEnv(t)
	token, cookie := csrfFrom(t, env.handler, "/releases")
	recorder := postForm(t, env.handler, "/releases/live",
		url.Values{"csrf": {token}, "mode": {"search"}, "q": {"Show"}}, cookie)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "Select at least one indexer to search.") {
		t.Errorf("indexer validation missing: %s", recorder.Body.String())
	}
}

// A fake qBittorrent is enough to prove that every selected release is sent
// and that the handler reports the two counts.
func TestBulkGrabSendsEverySelectedRelease(t *testing.T) {
	env := newTestEnv(t)
	var adds atomic.Int64
	client := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/add":
			adds.Add(1)
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
	insertSearchRelease(t, env.store, "Bulk One 1080p", "5040")
	insertSearchRelease(t, env.store, "Bulk Two 1080p", "5040")
	releases, _, err := env.store.SearchReleases(context.Background(), store.Filter{Limit: 10})
	if err != nil || len(releases) != 2 {
		t.Fatalf("releases = %d, %v", len(releases), err)
	}
	token, cookie := csrfFrom(t, env.handler, "/releases")
	recorder := postForm(t, env.handler, "/releases/bulk", url.Values{
		"csrf": {token},
		"op":   {"grab"},
		"ids":  {strconvI(releases[0].ID), strconvI(releases[1].ID)},
	}, cookie)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
	location, err := url.Parse(recorder.Header().Get("Location"))
	if err != nil {
		t.Fatalf("Location: %v", err)
	}
	if got := location.Query().Get("msg"); got != "2 sent, 0 failed" {
		t.Errorf("message = %q", got)
	}
	if got := adds.Load(); got != 2 {
		t.Errorf("download client receives = %d, want 2", got)
	}
}

func TestReleasesDeleteRouteStillWorks(t *testing.T) {
	env := newTestEnv(t)
	insertSearchRelease(t, env.store, "Delete Me 1080p", "5040")
	releases, _, err := env.store.SearchReleases(context.Background(), store.Filter{Limit: 10})
	if err != nil || len(releases) != 1 {
		t.Fatalf("releases = %d, %v", len(releases), err)
	}
	token, cookie := csrfFrom(t, env.handler, "/releases")
	recorder := postForm(t, env.handler, "/releases/delete", url.Values{
		"csrf": {token},
		"ids":  {strconvI(releases[0].ID)},
	}, cookie)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
	if _, total, err := env.store.SearchReleases(context.Background(), store.Filter{Limit: 10}); err != nil || total != 0 {
		t.Fatalf("release not deleted: total=%d err=%v", total, err)
	}
}
