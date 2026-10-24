package downloadproxy

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/buzzqw/indexarr/internal/definition"
	"github.com/buzzqw/indexarr/internal/engine"
	"github.com/buzzqw/indexarr/internal/indexer"
	"github.com/buzzqw/indexarr/internal/store"
	"github.com/buzzqw/indexarr/internal/vault"
)

const testAPIKey = "super-secret-newznab-key"

func testVault(t *testing.T) *vault.Vault {
	t.Helper()
	key := make([]byte, 32)
	key[0] = 7
	v, err := vault.New(key)
	if err != nil {
		t.Fatalf("vault.New: %v", err)
	}
	return v
}

func testStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(t.TempDir() + "/proxy.db")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func testManager(t *testing.T, st *store.Store, v *vault.Vault) *indexer.Manager {
	t.Helper()
	m, err := indexer.New(indexer.Config{
		Store:     st,
		Vault:     v,
		Transport: engine.NewTransport(engine.TransportConfig{AllowPrivate: true}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("indexer.New: %v", err)
	}
	return m
}

func seedNewznab(t *testing.T, st *store.Store, v *vault.Vault, baseURL string) int64 {
	t.Helper()
	ctx := context.Background()
	body := `schema: 1
meta: {id: nz-test, name: NZ Test, type: private, protocol: usenet, urls: ["` + baseURL + `"]}
connector: newznab
`
	if _, err := definition.Parse([]byte(body)); err != nil {
		t.Fatalf("definition.Parse: %v", err)
	}
	if err := st.UpsertDefinition(ctx, store.Definition{
		ID:            "nz-test",
		SchemaVersion: definition.CurrentSchema,
		Family:        "newznab",
		State:         "verificata",
		Origin:        "local",
		Author:        "test",
		Body:          body,
		Hash:          definition.Fingerprint([]byte(body)),
		UpdatedAt:     time.Now(),
	}); err != nil {
		t.Fatalf("UpsertDefinition: %v", err)
	}
	id, err := st.CreateIndexer(ctx, store.Indexer{
		DefinitionID: "nz-test",
		Protocol:     "usenet",
		Slug:         "nz-test",
		Name:         "NZ Test",
		Enabled:      true,
		Priority:     25,
		ConfigJSON:   `{"url":"` + baseURL + `","categories":[5030]}`,
	})
	if err != nil {
		t.Fatalf("CreateIndexer: %v", err)
	}
	nonce, ciphertext, err := v.Seal(testAPIKey)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if err := st.PutSecret(ctx, store.Secret{
		IndexerID: id, Key: "api_key", Nonce: nonce, Ciphertext: ciphertext,
	}); err != nil {
		t.Fatalf("PutSecret: %v", err)
	}
	return id
}

func insertRelease(t *testing.T, st *store.Store, dedupKey, title string) store.Release {
	t.Helper()
	release := store.Release{
		DedupKey:    dedupKey,
		Title:       title,
		SearchTitle: store.NormalizeSearch(title),
		Size:        99,
		Category:    "5030",
		Protocol:    "usenet",
		PublishedAt: time.Now().Add(-time.Hour),
		FirstSeenAt: time.Now().Add(-time.Hour),
		LastSeenAt:  time.Now(),
	}
	if _, err := st.UpsertRelease(context.Background(), &release); err != nil {
		t.Fatalf("UpsertRelease: %v", err)
	}
	return release
}

func TestResolveDownloadsPersistsAndServesLocally(t *testing.T) {
	ctx := context.Background()
	nzb := []byte(`<?xml version="1.0"?><nzb><file subject="movie"/></nzb>`)
	var gets int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("t") != "get" {
			http.NotFound(w, r)
			return
		}
		atomic.AddInt32(&gets, 1)
		if r.URL.Query().Get("apikey") != testAPIKey {
			http.Error(w, "bad key", http.StatusForbidden)
			return
		}
		if r.URL.Query().Get("id") != "release-1" {
			http.Error(w, "bad id", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/x-nzb")
		_, _ = w.Write(nzb)
	}))
	defer server.Close()

	st := testStore(t)
	v := testVault(t)
	manager := testManager(t, st, v)
	indexerID := seedNewznab(t, st, v, server.URL)
	resolver, err := New(Config{Store: st, Vault: v, Manager: manager})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	release := insertRelease(t, st, "usenet-ref-1", "Usenet Movie 1080p")
	ref, err := indexer.SealDownloadRef(v, indexerID, engine.Item{
		Protocol:    engine.ProtocolUsenet,
		Title:       release.Title,
		GUID:        "release-1",
		DownloadURL: server.URL + "/get.nzb?apikey=" + testAPIKey,
		Size:        release.Size,
	})
	if err != nil {
		t.Fatalf("SealDownloadRef: %v", err)
	}
	if strings.Contains(ref, server.URL) || strings.Contains(ref, testAPIKey) {
		t.Fatalf("sealed reference leaks its contents")
	}
	if err := st.RecordReleaseSource(ctx, store.ReleaseSource{
		ReleaseID: release.ID, IndexerID: indexerID, DownloadRef: ref,
	}); err != nil {
		t.Fatalf("RecordReleaseSource: %v", err)
	}

	payload, err := resolver.Resolve(ctx, release.ID)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if string(payload.Body) != string(nzb) {
		t.Errorf("body = %q", payload.Body)
	}
	if payload.ContentType != "application/x-nzb" {
		t.Errorf("content type = %q", payload.ContentType)
	}
	if !strings.HasSuffix(payload.Filename, ".nzb") {
		t.Errorf("filename = %q", payload.Filename)
	}
	if gets != 1 {
		t.Errorf("server gets = %d, want 1", gets)
	}

	stored, err := st.GetRelease(ctx, release.ID)
	if err != nil {
		t.Fatalf("GetRelease: %v", err)
	}
	if string(stored.Payload) != string(nzb) {
		t.Errorf("persisted payload = %q", stored.Payload)
	}
	if stored.PayloadType != "application/x-nzb" || stored.Protocol != "usenet" {
		t.Errorf("persisted metadata = protocol %q type %q", stored.Protocol, stored.PayloadType)
	}

	// The second request is served from the database, not the tracker.
	again, err := resolver.Resolve(ctx, release.ID)
	if err != nil {
		t.Fatalf("second Resolve: %v", err)
	}
	if string(again.Body) != string(nzb) {
		t.Errorf("second body = %q", again.Body)
	}
	if gets != 1 {
		t.Errorf("server gets after cached resolve = %d, want 1", gets)
	}
}

func TestResolveErrorsAreGeneric(t *testing.T) {
	ctx := context.Background()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "tracker exploded at "+r.URL.String(), http.StatusInternalServerError)
	}))
	defer server.Close()

	st := testStore(t)
	v := testVault(t)
	manager := testManager(t, st, v)
	indexerID := seedNewznab(t, st, v, server.URL)
	resolver, err := New(Config{Store: st, Vault: v, Manager: manager})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	release := insertRelease(t, st, "usenet-ref-fail", "Broken Release")
	leakURL := server.URL + "/leak.nzb?apikey=" + testAPIKey
	ref, err := indexer.SealDownloadRef(v, indexerID, engine.Item{
		Protocol:    engine.ProtocolUsenet,
		Title:       release.Title,
		DownloadURL: leakURL,
	})
	if err != nil {
		t.Fatalf("SealDownloadRef: %v", err)
	}
	if err := st.RecordReleaseSource(ctx, store.ReleaseSource{
		ReleaseID: release.ID, IndexerID: indexerID, DownloadRef: ref,
	}); err != nil {
		t.Fatalf("RecordReleaseSource: %v", err)
	}

	_, err = resolver.Resolve(ctx, release.ID)
	if !errors.Is(err, ErrTemporary) && !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Resolve error = %v, want a temporary/unavailable class", err)
	}
	message := err.Error()
	for _, secret := range []string{server.URL, "leak.nzb", testAPIKey, "apikey"} {
		if strings.Contains(message, secret) {
			t.Errorf("error leaks %q: %s", secret, message)
		}
	}
}

func TestNewAndResolveErrors(t *testing.T) {
	if _, err := New(Config{}); !errors.Is(err, ErrStoreRequired) {
		t.Errorf("New without store = %v", err)
	}
	st := testStore(t)
	if _, err := New(Config{Store: st}); !errors.Is(err, ErrManagerRequired) {
		t.Errorf("New without manager = %v", err)
	}
	v := testVault(t)
	resolver, err := New(Config{Store: st, Vault: v, Manager: testManager(t, st, v)})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := resolver.Resolve(context.Background(), 9999); !errors.Is(err, ErrNotFound) {
		t.Errorf("Resolve missing = %v, want ErrNotFound", err)
	}
}

func seedTorrentTorznab(t *testing.T, st *store.Store, v *vault.Vault, baseURL string) int64 {
	t.Helper()
	ctx := context.Background()
	body := `schema: 1
meta: {id: tz-test, name: TZ Test, type: private, protocol: torrent, urls: ["` + baseURL + `"]}
connector: torznab
`
	if _, err := definition.Parse([]byte(body)); err != nil {
		t.Fatalf("definition.Parse: %v", err)
	}
	if err := st.UpsertDefinition(ctx, store.Definition{
		ID: "tz-test", SchemaVersion: definition.CurrentSchema, Family: "torznab",
		State: "verificata", Origin: "local", Author: "test", Body: body,
		Hash: definition.Fingerprint([]byte(body)), UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("UpsertDefinition: %v", err)
	}
	id, err := st.CreateIndexer(ctx, store.Indexer{
		DefinitionID: "tz-test", Protocol: "torrent", Slug: "tz-test",
		Name: "TZ Test", Enabled: true, Priority: 25,
		ConfigJSON: `{"url":"` + baseURL + `","categories":[5040]}`,
	})
	if err != nil {
		t.Fatalf("CreateIndexer: %v", err)
	}
	return id
}

func insertTorrentRelease(t *testing.T, st *store.Store, key, title string) store.Release {
	t.Helper()
	release := store.Release{
		DedupKey: key, Title: title, SearchTitle: store.NormalizeSearch(title),
		Size: 12345, Category: "5040", Protocol: "torrent",
		PublishedAt: time.Now().Add(-time.Hour), FirstSeenAt: time.Now().Add(-time.Hour),
		LastSeenAt: time.Now(),
	}
	if _, err := st.UpsertRelease(context.Background(), &release); err != nil {
		t.Fatalf("UpsertRelease: %v", err)
	}
	return release
}

func TestResolveDownloadLinkShapes(t *testing.T) {
	magnet := "magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567&dn=Show"
	cases := []struct {
		name       string
		status     int
		location   string
		body       string
		fixture    string
		wantKind   error
		wantMagnet string
	}{
		{"redirect magnet", http.StatusFound, magnet, "", "", nil, magnet},
		{"body magnet", http.StatusOK, "", "", "body_magnet.txt", nil, magnet},
		{"torrent file", http.StatusOK, "", "", "valid.torrent", nil, ""},
		{"html login", http.StatusOK, "", "", "login.html", ErrLogin, ""},
		{"html challenge", http.StatusOK, "", "", "challenge.html", ErrBlocked, ""},
		{"404 no magnet", http.StatusNotFound, "", "", "no_magnet.txt", ErrTemporary, ""},
		{"502 login", http.StatusBadGateway, "", "", "login_502.txt", ErrTemporary, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.location != "" {
					w.Header().Set("Location", tc.location)
					w.WriteHeader(tc.status)
					return
				}
				body := []byte(tc.body)
				if tc.fixture != "" {
					body, _ = os.ReadFile(filepath.Join("testdata", tc.fixture))
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write(body)
			}))
			defer server.Close()
			st := testStore(t)
			v := testVault(t)
			manager := testManager(t, st, v)
			indexerID := seedTorrentTorznab(t, st, v, server.URL)
			resolver, err := New(Config{Store: st, Vault: v, Manager: manager})
			if err != nil {
				t.Fatal(err)
			}
			release := insertTorrentRelease(t, st, "tz-"+tc.name, "Torrent Release")
			ref, err := indexer.SealDownloadRef(v, indexerID, engine.Item{
				Title: release.Title, GUID: "rel-1", DownloadURL: server.URL + "/download",
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := st.RecordReleaseSource(context.Background(), store.ReleaseSource{
				ReleaseID: release.ID, IndexerID: indexerID, DownloadRef: ref,
			}); err != nil {
				t.Fatal(err)
			}
			payload, err := resolver.Resolve(context.Background(), release.ID)
			if tc.wantKind != nil {
				if !errors.Is(err, tc.wantKind) {
					t.Fatalf("Resolve error = %v, want %v", err, tc.wantKind)
				}
				return
			}
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if tc.wantMagnet != "" {
				if payload.Magnet != tc.wantMagnet {
					t.Fatalf("magnet = %q", payload.Magnet)
				}
				return
			}
			wantBody, _ := os.ReadFile(filepath.Join("testdata", "valid.torrent"))
			if string(payload.Body) != string(wantBody) {
				t.Fatalf("body = %q", payload.Body)
			}
			if !strings.HasSuffix(payload.Filename, ".torrent") {
				t.Errorf("filename = %q", payload.Filename)
			}
		})
	}
}

func TestRepeated404QuarantinesSource(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer server.Close()
	st := testStore(t)
	v := testVault(t)
	manager := testManager(t, st, v)
	indexerID := seedTorrentTorznab(t, st, v, server.URL)
	resolver, _ := New(Config{Store: st, Vault: v, Manager: manager})
	release := insertTorrentRelease(t, st, "quarantine", "Quarantine Release")
	ref, _ := indexer.SealDownloadRef(v, indexerID, engine.Item{Title: release.Title, GUID: "rel-1", DownloadURL: server.URL + "/download"})
	_ = st.RecordReleaseSource(context.Background(), store.ReleaseSource{ReleaseID: release.ID, IndexerID: indexerID, DownloadRef: ref})
	for i := 0; i < 2; i++ {
		if _, err := resolver.Resolve(context.Background(), release.ID); err == nil {
			t.Fatal("404 did not fail")
		}
	}
	sources, _ := st.ReleaseSourcesFor(context.Background(), release.ID)
	if len(sources) != 1 || sources[0].DownloadFailures != 2 || !sources[0].Quarantined {
		t.Fatalf("source after repeated 404 = %+v", sources)
	}
	if _, err := resolver.Resolve(context.Background(), release.ID); !errors.Is(err, ErrQuarantined) {
		t.Fatalf("quarantined resolve = %v", err)
	}
}
