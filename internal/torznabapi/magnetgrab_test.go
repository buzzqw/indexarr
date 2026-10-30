package torznabapi

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/buzzqw/indexarr/internal/indexer"
	"github.com/buzzqw/indexarr/internal/store"
)

// A magnet-only release used to answer 404 "use the magnet link" on /dl. The
// download link now redirects to the magnet and the grab is recorded with its
// delivery kind.
func TestDownloadLinkRedirectsMagnetOnlyReleaseAndRecordsGrab(t *testing.T) {
	api, st := newAPI(t)
	hash := "cccccccccccccccccccccccccccccccccccccccc"
	id := insertRelease(t, st, "Magnet Only Show 1080p", "5040", hash, "", nil)
	indexerID := linkReleaseIndexer(t, st, "magnet", id)

	var gotVia string
	records := 0
	api.SetGrabGuard(nil, func(ctx context.Context, gotIndexer, gotRelease int64, _ string) error {
		records++
		if gotIndexer != indexerID || gotRelease != id {
			t.Errorf("record = %d/%d, want %d/%d", gotIndexer, gotRelease, indexerID, id)
		}
		gotVia = grabViaFrom(ctx)
		return nil
	})

	recorder := do(t, api, "/dl/"+itoa(id)+".torrent?apikey=secret")
	if recorder.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302: %s", recorder.Code, recorder.Body.String())
	}
	if location := recorder.Header().Get("Location"); !strings.HasPrefix(location, "magnet:?xt=urn:btih:"+hash) {
		t.Errorf("Location = %q", location)
	}
	if records != 1 || gotVia != indexer.GrabViaMagnet {
		t.Errorf("records = %d, via = %q", records, gotVia)
	}
}

// The magnet indexarr resolves for a grab is stored on the release, so the
// Download tab can offer "copy magnet" later without resolving the source
// reference again (which may thank a forum post).
func TestDownloadLinkStoresTheResolvedMagnet(t *testing.T) {
	api, st := newAPI(t)
	now := time.Now()
	release := store.Release{
		DedupKey: "resolved-magnet", Title: "Resolved Magnet Show",
		SearchTitle: store.NormalizeSearch("Resolved Magnet Show"),
		Size:        1234, Category: "5040", Protocol: "torrent",
		PublishedAt: now.Add(-time.Hour), FirstSeenAt: now.Add(-time.Hour), LastSeenAt: now,
	}
	if _, err := st.UpsertRelease(context.Background(), &release); err != nil {
		t.Fatalf("UpsertRelease: %v", err)
	}
	linkReleaseIndexer(t, st, "resolved", release.ID)
	const magnet = "magnet:?xt=urn:btih:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	api.SetDownloadResolver(func(context.Context, int64) ([]byte, string, string, error) {
		return []byte(magnet), "application/x-magnet", "", nil
	})

	recorder := do(t, api, "/dl/"+itoa(release.ID)+".torrent?apikey=secret")
	if recorder.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302: %s", recorder.Code, recorder.Body.String())
	}
	stored, err := st.GetRelease(context.Background(), release.ID)
	if err != nil {
		t.Fatalf("GetRelease: %v", err)
	}
	if stored.Magnet != magnet {
		t.Errorf("stored magnet = %q, want the resolved link", stored.Magnet)
	}
}

func TestProxyMagnetsMovesTheMagnetBehindTheDownloadLink(t *testing.T) {
	api, st := newAPI(t)
	hash := "dddddddddddddddddddddddddddddddddddddddd"
	id := insertRelease(t, st, "Proxy Magnet Show 1080p", "5040", hash, "", nil)

	body := do(t, api, "/api?t=search&q=proxy+magnet&apikey=secret").Body.String()
	if !strings.Contains(body, `name="magneturl"`) {
		t.Fatalf("default feed lost the magneturl attribute: %s", body)
	}

	settings, err := st.LoadSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	settings.TorznabProxyMagnets = true
	if err := st.SaveSettings(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	body = do(t, api, "/api?t=search&q=proxy+magnet&apikey=secret").Body.String()
	if strings.Contains(body, `name="magneturl"`) || strings.Contains(body, "magnet:?") {
		t.Errorf("proxied feed still carries the magnet: %s", body)
	}
	if !strings.Contains(body, "/dl/"+itoa(id)+".torrent?apikey=") {
		t.Errorf("proxied feed has no download link: %s", body)
	}
	if !strings.Contains(body, `name="infohash" value="`+hash+`"`) {
		t.Errorf("proxied feed lost the infohash: %s", body)
	}
}

// grabViaFrom reads the delivery kind the server put in the record context.
func grabViaFrom(ctx context.Context) string {
	if via, ok := indexer.GrabViaFromContext(ctx); ok {
		return via
	}
	return indexer.GrabViaTorrent
}
