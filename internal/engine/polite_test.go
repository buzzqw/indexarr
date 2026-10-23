package engine

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeRoundTripper is a deterministic base transport for the politeness tests.
type fakeRoundTripper struct {
	mu    sync.Mutex
	calls int
	last  *http.Request
	// respond receives the request and returns a response. A nil response
	// returns a plain 200.
	respond func(*http.Request) *http.Response
}

func (f *fakeRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	f.mu.Lock()
	f.calls++
	f.last = req
	respond := f.respond
	f.mu.Unlock()
	if respond != nil {
		if response := respond(req); response != nil {
			return response, nil
		}
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader("ok")),
		Request:    req,
	}, nil
}

func (f *fakeRoundTripper) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func politeRequest(t *testing.T, rawURL string) *http.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, rawURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	return req
}

func TestPolitenessFakeClockRateAndBurst(t *testing.T) {
	clock := newFakeClock()
	polite := NewPoliteness(1, 4, 5, time.Minute, clock)
	base := &fakeRoundTripper{}
	roundTripper := polite.RoundTripper(base)
	start := clock.Now()

	for i := 0; i < 10; i++ {
		if _, err := roundTripper.RoundTrip(politeRequest(t, "https://same.example/api")); err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
	}

	elapsed := clock.Now().Sub(start)
	if elapsed < 5900*time.Millisecond || elapsed > 6100*time.Millisecond {
		t.Errorf("ten requests advanced %s, want about 6s", elapsed)
	}
	if got := clock.sleepCount(); got != 6 {
		t.Errorf("limiter sleeps = %d, want 6", got)
	}
	if got := base.callCount(); got != 10 {
		t.Errorf("base calls = %d, want 10", got)
	}
}

func TestPolitenessHostsAreIndependent(t *testing.T) {
	clock := newFakeClock()
	polite := NewPoliteness(1, 4, 5, time.Minute, clock)
	base := &fakeRoundTripper{}
	roundTripper := polite.RoundTripper(base)
	start := clock.Now()

	for i := 0; i < 4; i++ {
		if _, err := roundTripper.RoundTrip(politeRequest(t, "https://a.example/api")); err != nil {
			t.Fatalf("host A request %d: %v", i, err)
		}
		if _, err := roundTripper.RoundTrip(politeRequest(t, "https://b.example/api")); err != nil {
			t.Fatalf("host B request %d: %v", i, err)
		}
	}
	if elapsed := clock.Now().Sub(start); elapsed != 0 {
		t.Fatalf("initial bursts advanced the clock by %s, want 0", elapsed)
	}

	if _, err := roundTripper.RoundTrip(politeRequest(t, "https://a.example/api")); err != nil {
		t.Fatalf("host A fifth request: %v", err)
	}
	if elapsed := clock.Now().Sub(start); elapsed != time.Second {
		t.Errorf("host A fifth request advanced %s, want 1s", elapsed)
	}
	if _, err := roundTripper.RoundTrip(politeRequest(t, "https://a.example/api")); err != nil {
		t.Fatalf("host A sixth request: %v", err)
	}
	if elapsed := clock.Now().Sub(start); elapsed != 2*time.Second {
		t.Errorf("host A sixth request advanced the total to %s, want 2s", elapsed)
	}

	// Host B still has its own burst budget even after host A waited twice.
	if _, err := roundTripper.RoundTrip(politeRequest(t, "https://b.example/api")); err != nil {
		t.Fatalf("host B fifth request: %v", err)
	}
	if elapsed := clock.Now().Sub(start); elapsed != 2*time.Second {
		t.Errorf("host B fifth request advanced the total to %s, want the clock to stay at 2s", elapsed)
	}
	if _, err := roundTripper.RoundTrip(politeRequest(t, "https://a.example/api")); err != nil {
		t.Fatalf("host A seventh request: %v", err)
	}
	if elapsed := clock.Now().Sub(start); elapsed != 3*time.Second {
		t.Errorf("host A seventh request advanced the total to %s, want 3s", elapsed)
	}
}

func TestPolitenessRetryAfterBlocksHost(t *testing.T) {
	clock := newFakeClock()
	polite := NewPoliteness(0, 1, 5, time.Minute, clock)
	base := &fakeRoundTripper{
		respond: func(req *http.Request) *http.Response {
			if strings.Contains(req.URL.Path, "first") {
				header := make(http.Header)
				header.Set("Retry-After", "30")
				return &http.Response{
					StatusCode: http.StatusTooManyRequests,
					Header:     header,
					Body:       io.NopCloser(strings.NewReader("slow down")),
					Request:    req,
				}
			}
			return nil
		},
	}
	roundTripper := polite.RoundTripper(base)

	response, err := roundTripper.RoundTrip(politeRequest(t, "https://limited.example/first"))
	if err != nil {
		t.Fatalf("first request: %v", err)
	}
	if response.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("first status = %d, want 429", response.StatusCode)
	}

	if _, err := roundTripper.RoundTrip(politeRequest(t, "https://limited.example/second")); !errors.Is(err, ErrBreakerOpen) {
		t.Fatalf("request during Retry-After = %v, want ErrBreakerOpen", err)
	}
	clock.Advance(29 * time.Second)
	if _, err := roundTripper.RoundTrip(politeRequest(t, "https://limited.example/third")); !errors.Is(err, ErrBreakerOpen) {
		t.Fatalf("request at 29s = %v, want ErrBreakerOpen", err)
	}
	if got := base.callCount(); got != 1 {
		t.Fatalf("base calls during Retry-After = %d, want 1", got)
	}

	clock.Advance(time.Second)
	if _, err := roundTripper.RoundTrip(politeRequest(t, "https://limited.example/fourth")); err != nil {
		t.Fatalf("request after Retry-After: %v", err)
	}
	if got := base.callCount(); got != 2 {
		t.Errorf("base calls after Retry-After = %d, want 2", got)
	}
}

func TestPolitenessSharedByNativeAndLegacy(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer server.Close()

	clock := newFakeClock()
	polite := NewPoliteness(1, 1, 5, time.Minute, clock)
	native := newLocalTransport(TransportConfig{Clock: clock, Politeness: polite})
	legacyBase := &fakeRoundTripper{}
	legacy := polite.RoundTripper(legacyBase)
	start := clock.Now()

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL, nil)
	if _, err := native.Do(context.Background(), "native", req); err != nil {
		t.Fatalf("native request: %v", err)
	}
	if clock.sleepCount() != 0 {
		t.Fatalf("native request slept on the burst: %d", clock.sleepCount())
	}

	legacyReq := politeRequest(t, server.URL+"/legacy")
	if _, err := legacy.RoundTrip(legacyReq); err != nil {
		t.Fatalf("legacy request: %v", err)
	}
	if elapsed := clock.Now().Sub(start); elapsed != time.Second {
		t.Errorf("legacy request advanced %s, want the shared 1s wait", elapsed)
	}
	if got := clock.sleepCount(); got != 1 {
		t.Errorf("legacy request did not wait for the shared token: sleeps = %d", got)
	}
	if got := legacyBase.callCount(); got != 1 {
		t.Errorf("legacy base calls = %d, want 1", got)
	}
}

func TestPolitenessReachesPrivateAddress(t *testing.T) {
	polite := NewPoliteness(0, 1, 5, time.Minute, newFakeClock())
	base := &fakeRoundTripper{}
	roundTripper := polite.RoundTripper(base)

	response, err := roundTripper.RoundTrip(politeRequest(t, "http://192.168.1.10:9117/api/v2.0/indexers/all/results/torznab/api?apikey=secret"))
	if err != nil {
		t.Fatalf("legacy path blocked a private address: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", response.StatusCode)
	}
	if got := base.last.URL.Hostname(); got != "192.168.1.10" {
		t.Errorf("base saw host %q, want 192.168.1.10", got)
	}
}

func TestPolitenessMetrics(t *testing.T) {
	clock := newFakeClock()
	polite := NewPoliteness(0, 1, 1, time.Minute, clock)
	base := &fakeRoundTripper{
		respond: func(req *http.Request) *http.Response {
			header := make(http.Header)
			status := http.StatusOK
			switch req.URL.Hostname() {
			case "limited.example":
				status = http.StatusTooManyRequests
				header.Set("Retry-After", "1")
			case "forbidden.example":
				status = http.StatusForbidden
			case "broken.example":
				status = http.StatusInternalServerError
			}
			return &http.Response{
				StatusCode: status,
				Header:     header,
				Body:       io.NopCloser(strings.NewReader("")),
				Request:    req,
			}
		},
	}
	roundTripper := polite.RoundTripper(base)

	for _, rawURL := range []string{
		"https://limited.example/one",
		"https://forbidden.example/one",
		"https://broken.example/one",
	} {
		if _, err := roundTripper.RoundTrip(politeRequest(t, rawURL)); err != nil {
			t.Fatalf("%s: %v", rawURL, err)
		}
	}
	// The threshold is 1: the 500 opened the breaker, the next request must be
	// refused and counted.
	if _, err := roundTripper.RoundTrip(politeRequest(t, "https://broken.example/two")); !errors.Is(err, ErrBreakerOpen) {
		t.Fatalf("request on open breaker = %v, want ErrBreakerOpen", err)
	}

	metrics := polite.HostMetrics()
	if len(metrics) != 3 {
		t.Fatalf("metrics = %#v, want three hosts", metrics)
	}
	byHost := map[string]HostMetric{}
	for _, metric := range metrics {
		byHost[metric.Host] = metric
	}
	if got := byHost["limited.example"]; got.RateLimited != 1 {
		t.Errorf("limited.example = %#v, want one 429", got)
	}
	if got := byHost["forbidden.example"]; got.Forbidden != 1 {
		t.Errorf("forbidden.example = %#v, want one 403", got)
	}
	if got := byHost["broken.example"]; got.BreakerOpen != 1 || got.State != BreakerOpen {
		t.Errorf("broken.example = %#v, want one breaker-open and state open", got)
	}
}
