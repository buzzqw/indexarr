package ingest

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/buzzqw/indexarr/internal/backup"
	"github.com/buzzqw/indexarr/internal/store"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func torrentBytes() []byte {
	info := "d6:lengthi12345e4:name8:test.iso12:piece lengthi16384e6:pieces20:aaaaaaaaaaaaaaaaaaaa" + "e"
	return []byte("d8:announce14:http://tracker4:info" + info + "e")
}

func TestRunSourceTorznab(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(t.TempDir() + "/ingest.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	torrent := torrentBytes()
	torrentHash := sha1.Sum(torrent[strings.Index(string(torrent), "4:info")+6 : len(torrent)-1])
	hashHex := hex.EncodeToString(torrentHash[:])

	var torrentDownloads int
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/torrent/2":
			torrentDownloads++
			w.Header().Set("Content-Type", "application/x-bittorrent")
			_, _ = w.Write(torrent)
		case r.URL.Path == "/api":
			w.Header().Set("Content-Type", "application/xml")
			_, _ = io.WriteString(w, fmt.Sprintf(`<rss version="2.0" xmlns:torznab="http://torznab.com/schemas/2015/feed"><channel>
				<item><title>Show.S01E01.1080p</title><guid>g1</guid>
					<link>magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567</link>
					<torznab:attr name="size" value="1000"/>
					<torznab:attr name="seeders" value="5"/>
					<torznab:attr name="category" value="5040"/></item>
				<item><title>Movie.2024.1080p</title><guid>g2</guid>
					<link>%s/torrent/2</link>
					<enclosure url="%s/torrent/2" type="application/x-bittorrent" length="500"/></item>
				<item><title>Useless.Web.Page</title><link>https://tracker.test/details/3</link></item>
			</channel></rss>`, server.URL, server.URL))
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	}))
	defer server.Close()

	sourceID, err := st.CreateSource(ctx, store.Source{
		Name: "Test", Kind: "torznab", Manager: "torznab", URL: server.URL,
		APIKey: "secret", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	engine := New(st, testLogger())
	result, err := engine.RunSource(ctx, sourceID)
	if err != nil {
		t.Fatalf("RunSource: %v", err)
	}
	if result.Seen != 3 || result.New != 2 || result.Updated != 0 || result.Torrents != 1 {
		t.Fatalf("result = %#v", result)
	}
	if torrentDownloads != 1 {
		t.Errorf("torrent downloads = %d", torrentDownloads)
	}
	releases, total, err := st.SearchReleases(ctx, store.Filter{Limit: 10})
	if err != nil || total != 2 {
		t.Fatalf("stored releases = %d, %v", total, err)
	}
	var withTorrent, withMagnet bool
	for _, rel := range releases {
		if len(rel.Torrent) > 0 {
			withTorrent = true
			if rel.InfoHash != hashHex {
				t.Errorf("infohash = %q, want %q", rel.InfoHash, hashHex)
			}
			if rel.Magnet == "" {
				t.Error("magnet missing on a downloaded release")
			}
		}
		if strings.Contains(rel.Title, "Show") && strings.HasPrefix(rel.Magnet, "magnet:") {
			withMagnet = true
		}
	}
	if !withTorrent || !withMagnet {
		t.Errorf("withTorrent=%v withMagnet=%v", withTorrent, withMagnet)
	}

	// The second run must update the two rows and reuse the stored .torrent
	// instead of downloading it again.
	result, err = engine.RunSource(ctx, sourceID)
	if err != nil {
		t.Fatalf("second RunSource: %v", err)
	}
	if result.New != 0 || result.Updated != 2 || result.Torrents != 0 {
		t.Fatalf("second result = %#v", result)
	}
	if torrentDownloads != 1 {
		t.Errorf("torrent downloaded again: %d", torrentDownloads)
	}

	runs, _ := st.ListRuns(ctx, sourceID, false, 10)
	if len(runs) != 2 {
		t.Errorf("runs = %d", len(runs))
	}
	source, _ := st.GetSource(ctx, sourceID)
	if source.LastOKAt.IsZero() || source.TotalItems != 2 {
		t.Errorf("source after runs = %#v", source)
	}
}

// serverURLPlaceholder documented the lazy URL issue that the var server
// declaration above solves; it is kept out of the tests intentionally.
func TestRunSourceRSS(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(t.TempDir() + "/rss.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `<rss version="2.0"><channel><title>Feed</title>
			<item><title>Feed.Release.1080p</title><guid>f1</guid>
			<link>magnet:?xt=urn:btih:abcdefabcdefabcdefabcdefabcdefabcdefabcd</link>
			<enclosure url="magnet:?xt=urn:btih:abcdefabcdefabcdefabcdefabcdefabcdefabcd" type="application/x-bittorrent" length="0"/>
			<torznab:attr xmlns:torznab="http://torznab.com/schemas/2015/feed" name="size" value="777"/></item>
		</channel></rss>`)
	}))
	defer server.Close()

	sourceID, err := st.CreateSource(ctx, store.Source{
		Name: "Feed", Kind: "rss", URL: server.URL, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	engine := New(st, testLogger())
	result, err := engine.RunSource(ctx, sourceID)
	if err != nil {
		t.Fatalf("RunSource: %v", err)
	}
	if result.Seen != 1 || result.New != 1 || result.Torrents != 0 {
		t.Fatalf("result = %#v", result)
	}
	releases, total, _ := st.SearchReleases(ctx, store.Filter{})
	if total != 1 || releases[0].Size != 777 {
		t.Fatalf("releases = %#v", releases)
	}
	if !strings.HasPrefix(releases[0].Magnet, "magnet:") {
		t.Errorf("magnet = %q", releases[0].Magnet)
	}
}

func TestRunSourceSkipsBlockedReleases(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(t.TempDir() + "/blocked.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `<rss version="2.0"><channel>
			<item><title>Blocked.Release.1080p</title><guid>blocked-1</guid>
				<link>magnet:?xt=urn:btih:1111111111111111111111111111111111111111</link></item>
			<item><title>Allowed.Release.1080p</title><guid>allowed-1</guid>
				<link>magnet:?xt=urn:btih:2222222222222222222222222222222222222222</link></item>
		</channel></rss>`)
	}))
	defer server.Close()

	sourceID, err := st.CreateSource(ctx, store.Source{
		Name: "RSS", Kind: "rss", URL: server.URL, Enabled: true, DefaultCategory: "5040",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddBlocklistRule(ctx, store.BlockKindTitle, "blocked", "test"); err != nil {
		t.Fatal(err)
	}
	engine := New(st, testLogger())
	result, err := engine.RunSource(ctx, sourceID)
	if err != nil {
		t.Fatalf("RunSource: %v", err)
	}
	if result.Seen != 2 || result.New != 1 || result.Blocked != 1 {
		t.Fatalf("result = %#v", result)
	}
	releases, total, err := st.SearchReleases(ctx, store.Filter{})
	if err != nil || total != 1 {
		t.Fatalf("releases = %d, %v", total, err)
	}
	if releases[0].Title != "Allowed.Release.1080p" {
		t.Errorf("stored release = %q", releases[0].Title)
	}
	if releases[0].Category != "5040" {
		t.Errorf("default category not applied: %q", releases[0].Category)
	}
	runs, _ := st.ListRuns(ctx, sourceID, false, 1)
	if len(runs) != 1 || runs[0].ItemsBlocked != 1 {
		t.Errorf("run = %#v", runs)
	}
}

func TestCancelRun(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(t.TempDir() + "/cancel.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()
	sourceID, err := st.CreateSource(ctx, store.Source{Name: "Slow", Kind: "rss", URL: server.URL, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	engine := New(st, testLogger())
	done := make(chan error, 1)
	go func() {
		_, err := engine.RunSource(ctx, sourceID)
		done <- err
	}()
	deadline := time.Now().Add(2 * time.Second)
	for !engine.IsRunning(sourceID) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !engine.IsRunning(sourceID) {
		t.Fatal("run did not start")
	}
	if !engine.Cancel(sourceID) {
		t.Fatal("Cancel returned false")
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("canceled run returned nil")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("run did not stop after cancel")
	}
}

func TestAutoDisableAfterFailures(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(t.TempDir() + "/autodisable.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer server.Close()
	sourceID, err := st.CreateSource(ctx, store.Source{Name: "Broken", Kind: "rss", URL: server.URL, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	settings, err := st.LoadSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	settings.AutoDisableAfter = 1
	settings.NotifyNewReleases = false
	settings.NotifyFailures = false
	if err := st.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	engine := New(st, testLogger())
	if _, err := engine.RunSource(ctx, sourceID); err == nil {
		t.Fatal("expected the run to fail")
	}
	source, err := st.GetSource(ctx, sourceID)
	if err != nil {
		t.Fatal(err)
	}
	if source.Enabled {
		t.Error("source was not auto-disabled")
	}
	if source.ConsecutiveErrors != 1 {
		t.Errorf("consecutive errors = %d", source.ConsecutiveErrors)
	}
}

func TestNotifyNewReleases(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(t.TempDir() + "/notify.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	feed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `<rss version="2.0"><channel><item>
			<title>Notified.Release.1080p</title><guid>n1</guid>
			<link>magnet:?xt=urn:btih:3333333333333333333333333333333333333333</link>
		</item></channel></rss>`)
	}))
	defer feed.Close()
	events := make(chan map[string]any, 1)
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var event map[string]any
		if err := json.NewDecoder(r.Body).Decode(&event); err != nil {
			t.Error(err)
		}
		events <- event
		w.WriteHeader(http.StatusNoContent)
	}))
	defer hook.Close()

	settings, err := st.LoadSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	settings.NotifyWebhookURL = hook.URL
	settings.NotifyNewReleases = true
	settings.NotifyFailures = false
	if err := st.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	sourceID, err := st.CreateSource(ctx, store.Source{Name: "Notify feed", Kind: "rss", URL: feed.URL, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	engine := New(st, testLogger())
	if _, err := engine.RunSource(ctx, sourceID); err != nil {
		t.Fatalf("RunSource: %v", err)
	}
	select {
	case event := <-events:
		if event["event"] != "new_releases" || event["source"] != "Notify feed" {
			t.Errorf("event = %#v", event)
		}
		if count, _ := event["count"].(float64); count != 1 {
			t.Errorf("count = %v", event["count"])
		}
	case <-time.After(3 * time.Second):
		t.Fatal("webhook was not called")
	}
}

func TestSearchLiveStoresResults(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(t.TempDir() + "/live.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:torznab="http://torznab.com/schemas/2015/feed"><channel>
<item><title>Live.Show.S01E02.1080p</title><guid>live-1</guid>
<link>magnet:?xt=urn:btih:4444444444444444444444444444444444444444</link>
<torznab:attr name="size" value="12345"/></item>
</channel></rss>`)
	}))
	defer server.Close()
	if _, err := st.CreateSource(ctx, store.Source{
		Name: "Live", Kind: "torznab", Manager: "torznab",
		URL: server.URL + "/api", APIKey: "k", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	engine := New(st, testLogger())
	stored, err := engine.SearchLive(ctx, store.Filter{Query: "Live Show", Season: 1, Episode: 2})
	if err != nil {
		t.Fatalf("SearchLive: %v", err)
	}
	if stored == 0 {
		t.Fatal("SearchLive stored nothing")
	}
	releases, total, err := st.SearchReleases(ctx, store.Filter{Query: "Live Show"})
	if err != nil || total != 1 || len(releases) != 1 {
		t.Fatalf("cached releases = %d, %v", total, err)
	}
	if releases[0].Title != "Live.Show.S01E02.1080p" {
		t.Errorf("title = %q", releases[0].Title)
	}
}

func TestAutomaticBackup(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(t.TempDir() + "/auto-backup.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	settings, err := st.LoadSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	settings.BackupEnabled = true
	settings.BackupIntervalHours = 24
	settings.BackupKeep = 2
	if err := st.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	engine := New(st, testLogger())
	engine.maybeBackup(ctx)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if !backup.LatestAutomatic(st.Path()).IsZero() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("automatic backup was not created")
}

func TestTriggerAndAlreadyRunning(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/trig.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	engine := New(st, testLogger())

	// The engine has no started worker in this test: the job stays queued.
	sourceID, err := st.CreateSource(context.Background(), store.Source{
		Name: "S", Kind: "torznab", URL: "http://example.test/api", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Trigger(sourceID); err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	queued, err := engine.TriggerAll(context.Background())
	if err != nil {
		t.Fatalf("TriggerAll: %v", err)
	}
	if queued != 1 {
		t.Errorf("queued = %d", queued)
	}

	// A source that times out on a reachable-looking but dead address must
	// still finish and record the failure rather than hang the caller.
	engine2 := New(st, testLogger())
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := engine2.RunSource(ctx, sourceID); err == nil {
		t.Fatal("expected a run error against an unreachable indexer")
	}
	runs, _ := st.ListRuns(context.Background(), sourceID, true, 10)
	if len(runs) != 1 {
		t.Errorf("failed runs = %d", len(runs))
	}
}
