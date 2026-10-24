package engine

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newLocalTransport(cfg TransportConfig) *Transport {
	cfg.AllowPrivate = true
	return NewTransport(cfg)
}

func TestTransportDoSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("User-Agent"); got != "test-agent" {
			http.Error(w, "missing user agent", http.StatusBadRequest)
			return
		}
		w.Header().Set("X-Test", "yes")
		_, _ = io.WriteString(w, "hello")
	}))
	defer server.Close()

	transport := newLocalTransport(TransportConfig{UserAgent: "test-agent"})
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := transport.Do(context.Background(), "idx", req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if response.Status != http.StatusOK {
		t.Errorf("status = %d, want 200", response.Status)
	}
	if string(response.Body) != "hello" {
		t.Errorf("body = %q, want hello", response.Body)
	}
	if response.Header.Get("X-Test") != "yes" {
		t.Error("response header not copied")
	}
	if response.URL == nil || response.URL.String() != server.URL {
		t.Errorf("response URL = %v, want %s", response.URL, server.URL)
	}
	if req.Header.Get("User-Agent") != "" {
		t.Error("Do mutated the caller's request")
	}
}

func TestTransportBodyTooLarge(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, strings.Repeat("x", 100))
	}))
	defer server.Close()

	transport := newLocalTransport(TransportConfig{MaxBodyBytes: 10})
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL, nil)
	response, err := transport.Do(context.Background(), "idx", req)
	if !errors.Is(err, ErrBodyTooLarge) {
		t.Fatalf("Do = %v, want ErrBodyTooLarge", err)
	}
	if response != nil {
		t.Error("oversized response returned")
	}
}

func TestTransportFiveHundredsOpenBreaker(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "broken", http.StatusInternalServerError)
	}))
	defer server.Close()

	transport := newLocalTransport(TransportConfig{FailureThreshold: 2, Cooldown: time.Minute})
	for i := 0; i < 2; i++ {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL, nil)
		response, err := transport.Do(context.Background(), "idx", req)
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		if response.Status != http.StatusInternalServerError {
			t.Fatalf("request %d status = %d, want 500", i, response.Status)
		}
	}

	if got := transport.BreakerState("idx"); got != BreakerOpen {
		t.Fatalf("breaker state = %q, want open", got)
	}
	if got := transport.BreakerState("other"); got != BreakerClosed {
		t.Errorf("unknown breaker state = %q, want closed", got)
	}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL, nil)
	if _, err := transport.Do(context.Background(), "idx", req); !errors.Is(err, ErrBreakerOpen) {
		t.Fatalf("third request = %v, want ErrBreakerOpen", err)
	}
}

func TestRetryAfter(t *testing.T) {
	now := time.Date(2026, time.October, 23, 12, 0, 0, 0, time.UTC)
	if got := retryAfter("120", now, time.Minute); got != 2*time.Minute {
		t.Errorf("seconds = %s, want 2m", got)
	}
	if got := retryAfter(now.Add(90*time.Second).Format(http.TimeFormat), now, time.Minute); got != 90*time.Second {
		t.Errorf("HTTP date = %s, want 90s", got)
	}
	if got := retryAfter(now.Add(-time.Minute).Format(http.TimeFormat), now, time.Minute); got != 0 {
		t.Errorf("past date = %s, want 0", got)
	}
	if got := retryAfter("nonsense", now, time.Minute); got != time.Minute {
		t.Errorf("invalid = %s, want fallback", got)
	}
	if got := retryAfter("", now, time.Minute); got != time.Minute {
		t.Errorf("empty = %s, want fallback", got)
	}
	if got := retryAfter("-1", now, time.Minute); got != time.Minute {
		t.Errorf("negative = %s, want fallback", got)
	}
}

func TestTransportRetryAfter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "120")
		http.Error(w, "slow down", http.StatusTooManyRequests)
	}))
	defer server.Close()

	transport := newLocalTransport(TransportConfig{})
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL, nil)
	if _, err := transport.Do(context.Background(), "idx", req); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("first request = %v, want ErrRateLimited", err)
	}
	if got := transport.breaker.State("idx"); got != BreakerOpen {
		t.Errorf("breaker state = %q, want open", got)
	}

	req, _ = http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL, nil)
	if _, err := transport.Do(context.Background(), "idx", req); !errors.Is(err, ErrBreakerOpen) {
		t.Fatalf("second request = %v, want ErrBreakerOpen", err)
	}
}

func TestTransportRateLimiterWaits(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer server.Close()

	clock := newFakeClock()
	transport := newLocalTransport(TransportConfig{
		RatePerSecond: 1,
		Burst:         1,
		Clock:         clock,
	})
	start := clock.Now()
	for i := 0; i < 2; i++ {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL, nil)
		if _, err := transport.Do(context.Background(), "idx", req); err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
	}
	if clock.sleepCount() != 1 {
		t.Errorf("limiter sleeps = %d, want 1", clock.sleepCount())
	}
	if elapsed := clock.Now().Sub(start); elapsed < time.Second {
		t.Errorf("fake clock advanced %s, want at least 1s", elapsed)
	}
}

func TestTransportRedirectStripsCredentialsOnHostChange(t *testing.T) {
	headers := make(chan http.Header, 1)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers <- r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/next", http.StatusFound)
	}))
	defer origin.Close()

	transport := newLocalTransport(TransportConfig{})
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, origin.URL, nil)
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Cookie", "session=secret")
	req.Header.Set("X-Api-Key", "secret")

	if _, err := transport.Do(context.Background(), "idx", req); err != nil {
		t.Fatalf("Do: %v", err)
	}
	got := <-headers
	for _, name := range []string{"Authorization", "Cookie", "X-Api-Key"} {
		if value := got.Get(name); value != "" {
			t.Errorf("%s leaked across hosts: %q", name, value)
		}
	}
}

func TestTransportRedirectDoesNotRestoreCredentials(t *testing.T) {
	headers := make(chan http.Header, 1)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/one" {
			http.Redirect(w, r, "/two", http.StatusFound)
			return
		}
		headers <- r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/one", http.StatusFound)
	}))
	defer origin.Close()

	transport := newLocalTransport(TransportConfig{})
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, origin.URL, nil)
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Cookie", "session=secret")
	req.Header.Set("X-Api-Key", "secret")

	if _, err := transport.Do(context.Background(), "idx", req); err != nil {
		t.Fatalf("Do: %v", err)
	}
	got := <-headers
	for _, name := range []string{"Authorization", "Cookie", "X-Api-Key"} {
		if value := got.Get(name); value != "" {
			t.Errorf("%s reintroduced on a later hop: %q", name, value)
		}
	}
}

func TestTransportRedirectKeepsCredentialsOnSameHost(t *testing.T) {
	headers := make(chan http.Header, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			http.Redirect(w, r, "/end", http.StatusFound)
			return
		}
		headers <- r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	transport := newLocalTransport(TransportConfig{})
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL+"/start", nil)
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Cookie", "session=secret")
	req.Header.Set("X-Api-Key", "secret")

	if _, err := transport.Do(context.Background(), "idx", req); err != nil {
		t.Fatalf("Do: %v", err)
	}
	got := <-headers
	for name, want := range map[string]string{
		"Authorization": "Bearer secret",
		"Cookie":        "session=secret",
		"X-Api-Key":     "secret",
	} {
		if value := got.Get(name); value != want {
			t.Errorf("%s = %q, want %q", name, value, want)
		}
	}
}

func TestTransportRedirectLimit(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Redirect(w, r, "/again", http.StatusFound)
	}))
	defer server.Close()

	transport := newLocalTransport(TransportConfig{})
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL, nil)
	_, err := transport.Do(context.Background(), "idx", req)
	if err == nil {
		t.Fatal("redirect loop succeeded")
	}
	if !strings.Contains(err.Error(), "redirects") {
		t.Errorf("error = %v, want redirect limit", err)
	}
	if got := requests.Load(); got != int64(maxRedirects+1) {
		t.Errorf("requests = %d, want %d", got, maxRedirects+1)
	}
}

func TestTransportMagnetRedirectNotFollowed(t *testing.T) {
	const magnet = "magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", magnet)
		w.WriteHeader(http.StatusFound)
	}))
	defer server.Close()

	transport := newLocalTransport(TransportConfig{})
	response, err := transport.Download(context.Background(), "idx", server.URL, 0)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if response.Status != http.StatusFound {
		t.Errorf("status = %d, want 302", response.Status)
	}
	if got := response.Header.Get("Location"); got != magnet {
		t.Errorf("Location = %q, want %q", got, magnet)
	}
}

func TestTransportDownloadSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "payload")
	}))
	defer server.Close()

	transport := newLocalTransport(TransportConfig{})
	response, err := transport.Download(context.Background(), "idx", server.URL, 0)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if string(response.Body) != "payload" {
		t.Errorf("body = %q, want payload", response.Body)
	}
}

func TestTransportDownloadReturnsErrorStatusAsResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "missing", http.StatusNotFound)
	}))
	defer server.Close()

	transport := newLocalTransport(TransportConfig{})
	response, err := transport.Download(context.Background(), "idx", server.URL, 0)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if response.Status != http.StatusNotFound {
		t.Errorf("status = %d, want 404", response.Status)
	}
}

func TestTransportBlocksPrivateWithoutOptIn(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	transport := NewTransport(TransportConfig{})
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL, nil)
	if _, err := transport.Do(context.Background(), "idx", req); !errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("Do = %v, want ErrBlockedAddress", err)
	}
}

func TestTransportUserAgentUsesProductVersion(t *testing.T) {
	old := productVersion
	t.Cleanup(func() { productVersion = old })
	SetProductVersion("2.0.0-rc1")
	var got string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("User-Agent")
	}))
	defer server.Close()
	transport := newLocalTransport(TransportConfig{})
	req, err := http.NewRequest(http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := transport.Do(context.Background(), "test", req); err != nil {
		t.Fatal(err)
	}
	if got != "indexarr/2.0.0-rc1" {
		t.Fatalf("User-Agent = %q", got)
	}
}

func TestTransportHasIPv4Fallback(t *testing.T) {
	tr := NewTransport(TransportConfig{})
	if tr.client4 == nil {
		t.Fatal("transport has no IPv4 fallback client")
	}
}
