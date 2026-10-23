package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

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
