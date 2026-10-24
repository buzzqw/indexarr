package indexer

import (
	"context"
	"log/slog"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/buzzqw/indexarr/internal/engine"
	"github.com/buzzqw/indexarr/internal/feed"
	"github.com/buzzqw/indexarr/internal/smartfeed"
	"github.com/buzzqw/indexarr/internal/store"
)

type fakeSmartfeed struct {
	item engine.Item
}

func (f *fakeSmartfeed) Capabilities() engine.Capabilities {
	return engine.Capabilities{Protocol: engine.ProtocolTorrent, Categories: []int{5040}}
}
func (f *fakeSmartfeed) Search(context.Context, engine.SearchRequest) ([]engine.Item, error) {
	return nil, nil
}
func (f *fakeSmartfeed) Latest(context.Context, engine.LatestRequest) ([]engine.Item, error) {
	return []engine.Item{f.item}, nil
}
func (f *fakeSmartfeed) Download(context.Context, engine.Item) (engine.Payload, error) {
	return engine.Payload{}, smartfeed.PendingGrabError()
}
func (f *fakeSmartfeed) Test(context.Context) (engine.TestResult, error) {
	return engine.TestResult{OK: true}, nil
}

type fakeNative struct {
	downloads atomic.Int32
}

func (f *fakeNative) Capabilities() engine.Capabilities {
	return engine.Capabilities{Protocol: engine.ProtocolTorrent}
}
func (f *fakeNative) Search(context.Context, engine.SearchRequest) ([]engine.Item, error) {
	return nil, nil
}
func (f *fakeNative) Latest(context.Context, engine.LatestRequest) ([]engine.Item, error) {
	return nil, nil
}
func (f *fakeNative) Download(context.Context, engine.Item) (engine.Payload, error) {
	f.downloads.Add(1)
	return engine.Payload{Magnet: feed.MagnetFromHash(hashA, "topic")}, nil
}
func (f *fakeNative) Test(context.Context) (engine.TestResult, error) {
	return engine.TestResult{OK: true}, nil
}

func TestSmartfeedPollStoresPendingTopicWithoutDownloadingOrThanking(t *testing.T) {
	st := newTestStore(t)
	v := newTestVault(t, 1)
	native := &fakeNative{}
	smart := &fakeSmartfeed{item: engine.Item{
		Protocol:   engine.ProtocolTorrent,
		Title:      "Reacher - Stagione 3 (2026) 1080p",
		DetailsURL: "https://mircrew-releases.org/viewtopic.php?f=51&t=165159",
		Categories: []int{5040},
		Seeders:    -1,
		Leechers:   -1,
		Published:  time.Now().Add(-time.Hour),
		Attributes: map[string]string{smartfeed.AttributePending: "true"},
	}}
	registry := engine.NewRegistry()
	if err := registry.RegisterConnector("fake-smartfeed", func(engine.Config) (engine.Connector, error) {
		return smart, nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := registry.RegisterConnector("fake-native", func(engine.Config) (engine.Connector, error) {
		return native, nil
	}); err != nil {
		t.Fatal(err)
	}
	manager, err := New(Config{
		Store: st, Vault: v, Registry: registry, Transport: newTestTransport(),
		Logger: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
	})
	if err != nil {
		t.Fatal(err)
	}
	seedDefinition(t, st, smarfeedDefinitionID, "", `schema: 1
connector: fake-smartfeed
meta:
  id: mircrew-smartfeed
  name: Smartfeed
  type: private
  protocol: torrent
  urls: [https://mircrew-releases.org/]
`)
	seedDefinition(t, st, smartfeed.NativeDefinitionID, "", `schema: 1
connector: fake-native
meta:
  id: mircrew
  name: MirCrew
  type: private
  protocol: torrent
  urls: [https://mircrew-releases.org/]
`)
	latestID := createIndexer(t, st, smarfeedDefinitionID, "mircrew-smartfeed", "MirCrew (Smartfeed)", "torrent",
		`{"site":"mircrew-releases.org","channel":"latest","latest":"true","smartfeed":"true"}`)
	nativeID := createIndexer(t, st, smartfeed.NativeDefinitionID, "mircrew", "MirCrew", "torrent",
		`{"site":"mircrew-releases.org","channel":"search","latest":"false"}`)

	if _, err := manager.Run(context.Background(), latestID); err != nil {
		t.Fatalf("poll: %v", err)
	}
	if got := native.downloads.Load(); got != 0 {
		t.Fatalf("the poll downloaded/opened a topic %d time(s)", got)
	}
	releases, total, err := st.SearchReleases(context.Background(), store.Filter{IndexerID: latestID, Limit: 10})
	if err != nil || total != 1 {
		t.Fatalf("releases = %d, %v", total, err)
	}
	release := releases[0]
	if release.Magnet != "" || len(release.Torrent) != 0 {
		t.Fatalf("pending topic has a payload: %+v", release)
	}
	sources, err := st.ListReleaseSources(context.Background(), release.ID)
	if err != nil || len(sources) != 1 || sources[0].DownloadRef == "" {
		t.Fatalf("sources = %+v, %v", sources, err)
	}
	ref, err := OpenDownloadRef(v, sources[0].DownloadRef)
	if err != nil {
		t.Fatalf("OpenDownloadRef: %v", err)
	}
	if ref.IndexerID != nativeID {
		t.Errorf("reference indexer = %d, want the native %d", ref.IndexerID, nativeID)
	}
	payload, err := manager.Download(context.Background(), ref.IndexerID, ref.Item)
	if err != nil || payload.Magnet == "" {
		t.Fatalf("grab through the native indexer = %+v, %v", payload, err)
	}
	if got := native.downloads.Load(); got != 1 {
		t.Errorf("native downloads = %d, want 1 at grab", got)
	}
}

const smarfeedDefinitionID = "mircrew-smartfeed"
