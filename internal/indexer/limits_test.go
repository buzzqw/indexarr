package indexer

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/buzzqw/indexarr/internal/engine"
	"github.com/buzzqw/indexarr/internal/store"
)

func setLimits(t *testing.T, st *store.Store, indexerID int64, queryLimit, grabLimit int) {
	t.Helper()
	indexer, err := st.GetIndexer(context.Background(), indexerID)
	if err != nil {
		t.Fatalf("GetIndexer: %v", err)
	}
	indexer.QueryLimit = queryLimit
	indexer.GrabLimit = grabLimit
	if err := st.UpdateIndexer(context.Background(), indexer); err != nil {
		t.Fatalf("UpdateIndexer: %v", err)
	}
}

func TestSearchAndLatestRespectQueryLimit(t *testing.T) {
	t.Run("search", func(t *testing.T) {
		st := newTestStore(t)
		v := newTestVault(t, 1)
		server := newSearchServer(t)
		manager := newTestManager(t, st, v)
		indexerID := seedNative(t, st, v, server.URL, true)
		setLimits(t, st, indexerID, 1, 0)
		ctx := context.Background()

		req := engine.SearchRequest{Mode: engine.ModeSearch, Query: "ubuntu", Categories: []int{5040}, Limit: 10}
		if _, err := manager.Search(ctx, indexerID, req); err != nil {
			t.Fatalf("first Search: %v", err)
		}
		_, err := manager.Search(ctx, indexerID, req)
		if !errors.Is(err, ErrQueryLimit) {
			t.Fatalf("second Search = %v, want ErrQueryLimit", err)
		}
		count := historyCount(t, st, indexerID, queryHistoryKinds)
		if count != 1 {
			t.Fatalf("query history rows = %d, want 1 (rejected attempt must not be recorded)", count)
		}
	})

	t.Run("latest", func(t *testing.T) {
		st := newTestStore(t)
		v := newTestVault(t, 1)
		server := newSearchServer(t)
		manager := newTestManager(t, st, v)
		indexerID := seedNative(t, st, v, server.URL, true)
		setLimits(t, st, indexerID, 1, 0)
		ctx := context.Background()

		req := engine.LatestRequest{Categories: []int{5040}, Limit: 10}
		if _, err := manager.Latest(ctx, indexerID, req); err != nil {
			t.Fatalf("first Latest: %v", err)
		}
		_, err := manager.Latest(ctx, indexerID, req)
		if !errors.Is(err, ErrQueryLimit) {
			t.Fatalf("second Latest = %v, want ErrQueryLimit", err)
		}
		count := historyCount(t, st, indexerID, queryHistoryKinds)
		if count != 1 {
			t.Fatalf("query history rows = %d, want 1", count)
		}
	})
}

func TestRunRespectsQueryLimitAndRecordsError(t *testing.T) {
	st := newTestStore(t)
	v := newTestVault(t, 1)
	server := newSearchServer(t)
	manager := newTestManager(t, st, v)
	indexerID := seedNative(t, st, v, server.URL, true)
	setLimits(t, st, indexerID, 1, 0)
	ctx := context.Background()

	if _, err := manager.Run(ctx, indexerID); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	_, err := manager.Run(ctx, indexerID)
	if !errors.Is(err, ErrQueryLimit) {
		t.Fatalf("second Run = %v, want ErrQueryLimit", err)
	}
	if strings.Contains(err.Error(), secretValue) {
		t.Fatalf("limit error leaked a secret: %v", err)
	}

	history, historyErr := st.ListIndexerHistory(ctx, indexerID, 10)
	if historyErr != nil {
		t.Fatalf("ListIndexerHistory: %v", historyErr)
	}
	if len(history) == 0 || history[0].Kind != "run" || history[0].Status != "error" {
		t.Fatalf("rejected run history = %+v", history)
	}
	if !strings.Contains(history[0].Error, "daily query limit") {
		t.Fatalf("history error = %q, want the limit message", history[0].Error)
	}
	if strings.Contains(history[0].Error, secretValue) {
		t.Fatalf("history error leaked a secret: %q", history[0].Error)
	}
}

func TestGrabLimit(t *testing.T) {
	st := newTestStore(t)
	v := newTestVault(t, 1)
	server := newSearchServer(t)
	manager := newTestManager(t, st, v)
	indexerID := seedNative(t, st, v, server.URL, true)
	setLimits(t, st, indexerID, 0, 1)
	ctx := context.Background()

	if err := manager.CheckGrab(ctx, indexerID); err != nil {
		t.Fatalf("CheckGrab before any grab: %v", err)
	}
	if err := manager.RecordGrab(ctx, indexerID, 42, "Ubuntu 24.04"); err != nil {
		t.Fatalf("RecordGrab: %v", err)
	}
	if err := manager.CheckGrab(ctx, indexerID); !errors.Is(err, ErrGrabLimit) {
		t.Fatalf("CheckGrab after one grab = %v, want ErrGrabLimit", err)
	}

	history, err := st.ListIndexerHistory(ctx, indexerID, 10)
	if err != nil {
		t.Fatalf("ListIndexerHistory: %v", err)
	}
	if len(history) != 1 || history[0].Kind != "grab" || history[0].Status != "ok" ||
		history[0].Query != "Ubuntu 24.04" {
		t.Fatalf("grab history = %+v", history)
	}

	// A grab older than the 24-hour window does not count against the limit.
	oldID := createIndexer(t, st, "native-test", "old-grab", "Old Grab", "torrent",
		`{"url":"`+server.URL+`"}`)
	setLimits(t, st, oldID, 0, 1)
	if _, err := st.RecordIndexerHistory(ctx, store.IndexerHistory{
		IndexerID: oldID, Kind: "grab", Status: "ok",
		StartedAt: time.Now().Add(-25 * time.Hour),
	}); err != nil {
		t.Fatalf("RecordIndexerHistory old grab: %v", err)
	}
	if err := manager.CheckGrab(ctx, oldID); err != nil {
		t.Fatalf("old grab counted against the limit: %v", err)
	}
}

func historyCount(t *testing.T, st *store.Store, indexerID int64, kinds []string) int {
	t.Helper()
	count, err := st.CountIndexerHistory(context.Background(), indexerID, kinds, time.Now().Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("CountIndexerHistory: %v", err)
	}
	return count
}

func TestIndexerProxyRoutesRequests(t *testing.T) {
	st := newTestStore(t)
	v := newTestVault(t, 1)
	server := newSearchServer(t)

	var mu sync.Mutex
	proxyHits := 0
	proxyAuth := ""
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		proxyHits++
		proxyAuth = r.Header.Get("Proxy-Authorization")
		mu.Unlock()
		if !r.URL.IsAbs() {
			http.Error(w, "not a forward-proxy request", http.StatusBadRequest)
			return
		}
		outgoing := r.Clone(r.Context())
		outgoing.RequestURI = ""
		outgoing.Header.Del("Proxy-Connection")
		outgoing.Header.Del("Proxy-Authorization")
		response, err := http.DefaultTransport.RoundTrip(outgoing)
		if err != nil {
			http.Error(w, "proxy upstream error", http.StatusBadGateway)
			return
		}
		defer response.Body.Close()
		for name, values := range response.Header {
			for _, value := range values {
				w.Header().Add(name, value)
			}
		}
		w.WriteHeader(response.StatusCode)
		_, _ = io.Copy(w, response.Body)
	}))
	t.Cleanup(proxy.Close)

	sealed, err := v.SealString("proxyuser:proxypass")
	if err != nil {
		t.Fatalf("SealString: %v", err)
	}
	proxyID, err := st.CreateProxy(context.Background(), store.Proxy{
		Kind:       "http",
		URL:        proxy.URL,
		SecretsEnc: []byte(sealed),
	})
	if err != nil {
		t.Fatalf("CreateProxy: %v", err)
	}

	manager := newTestManager(t, st, v)
	indexerID := seedNative(t, st, v, server.URL, true)
	indexer, err := st.GetIndexer(context.Background(), indexerID)
	if err != nil {
		t.Fatalf("GetIndexer: %v", err)
	}
	indexer.ProxyID = &proxyID
	if err := st.UpdateIndexer(context.Background(), indexer); err != nil {
		t.Fatalf("UpdateIndexer: %v", err)
	}

	items, err := manager.Search(context.Background(), indexerID, engine.SearchRequest{
		Mode: engine.ModeSearch, Query: "ubuntu", Categories: []int{5040}, Limit: 10,
	})
	if err != nil {
		t.Fatalf("Search through proxy: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("items = %d, want 2", len(items))
	}
	mu.Lock()
	defer mu.Unlock()
	if proxyHits == 0 {
		t.Fatal("the indexer request did not go through the proxy")
	}
	wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("proxyuser:proxypass"))
	if proxyAuth != wantAuth {
		t.Fatalf("Proxy-Authorization = %q, want %q", proxyAuth, wantAuth)
	}
}

func TestProxyWithSecretsRequiresVault(t *testing.T) {
	st := newTestStore(t)
	v := newTestVault(t, 1)
	server := newSearchServer(t)
	indexerID := seedNative(t, st, v, server.URL, false)

	proxyID, err := st.CreateProxy(context.Background(), store.Proxy{
		Kind: "http", URL: "http://proxy.example:8080", SecretsEnc: []byte("sealed"),
	})
	if err != nil {
		t.Fatalf("CreateProxy: %v", err)
	}
	indexer, err := st.GetIndexer(context.Background(), indexerID)
	if err != nil {
		t.Fatalf("GetIndexer: %v", err)
	}
	indexer.ProxyID = &proxyID
	if err := st.UpdateIndexer(context.Background(), indexer); err != nil {
		t.Fatalf("UpdateIndexer: %v", err)
	}

	manager := newTestManager(t, st, nil)
	_, _, err = manager.Connector(context.Background(), indexerID)
	if !errors.Is(err, ErrVaultRequired) {
		t.Fatalf("Connector = %v, want ErrVaultRequired", err)
	}
}
