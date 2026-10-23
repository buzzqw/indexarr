package perf_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/buzzqw/indexarr/internal/perf"
	"github.com/buzzqw/indexarr/internal/store"
	"github.com/buzzqw/indexarr/internal/torznabapi"
)

// TestTorznabSearchDoesNotLeak runs 1000 Torznab searches against a populated
// store and checks that goroutines and file descriptors settle back to their
// starting values. Bounds are deliberately generous: the point is to catch an
// unbounded leak, not to pin the runtime to an exact count.
func TestTorznabSearchDoesNotLeak(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("file descriptor accounting uses /proc")
	}
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "leak.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := perf.Seed(ctx, st, perf.Options{Releases: 100, APIKey: "leak-key"}); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	server := torznabapi.New(st, slog.New(slog.NewTextHandler(io.Discard, nil)), "leak")

	targets := []string{
		"/api?t=search&apikey=leak-key&limit=10&q=1080p",
		"/api?t=search&apikey=leak-key&limit=10",
		"/api?t=tvsearch&apikey=leak-key&limit=10&q=benchmark&season=1&ep=1",
	}
	requests := make([]*http.Request, len(targets))
	for i, target := range targets {
		requests[i] = httptest.NewRequest(http.MethodGet, target, nil)
	}
	do := func(i int) {
		recorder := httptest.NewRecorder()
		server.ServeHTTP(recorder, requests[i%len(requests)])
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
		}
		if !strings.Contains(recorder.Body.String(), "<rss") {
			t.Fatalf("response is not an RSS document: %s", recorder.Body.String())
		}
	}

	for i := 0; i < 25; i++ {
		do(i)
	}
	runtime.GC()
	time.Sleep(50 * time.Millisecond)
	beforeGoroutines := runtime.NumGoroutine()
	beforeFDs := openFDs(t)

	for i := 0; i < 1000; i++ {
		do(i)
	}
	runtime.GC()
	time.Sleep(50 * time.Millisecond)

	afterGoroutines := runtime.NumGoroutine()
	afterFDs := openFDs(t)
	t.Logf("goroutines %d -> %d, file descriptors %d -> %d",
		beforeGoroutines, afterGoroutines, beforeFDs, afterFDs)
	if afterGoroutines > beforeGoroutines+8 {
		t.Errorf("goroutines grew from %d to %d", beforeGoroutines, afterGoroutines)
	}
	if afterFDs > beforeFDs+8 {
		t.Errorf("file descriptors grew from %d to %d", beforeFDs, afterFDs)
	}
}

func openFDs(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatalf("read /proc/self/fd: %v", err)
	}
	return len(entries)
}
