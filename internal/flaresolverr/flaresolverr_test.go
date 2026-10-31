package flaresolverr

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestEndpoint(t *testing.T) {
	cases := map[string]string{
		"":                              "",
		"http://127.0.0.1:8191":         "http://127.0.0.1:8191/v1",
		"http://127.0.0.1:8191/":        "http://127.0.0.1:8191/v1",
		"http://127.0.0.1:8191/v1":      "http://127.0.0.1:8191/v1",
		"http://127.0.0.1:8191/v1/":     "http://127.0.0.1:8191/v1",
		"  http://flaresolverr:8191/  ": "http://flaresolverr:8191/v1",
	}
	for input, want := range cases {
		if got := Endpoint(input); got != want {
			t.Errorf("Endpoint(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestGet(t *testing.T) {
	var command, target string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		var payload struct {
			Cmd        string `json:"cmd"`
			URL        string `json:"url"`
			MaxTimeout int    `json:"maxTimeout"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		command = payload.Cmd
		target = payload.URL
		if payload.MaxTimeout <= 0 {
			t.Errorf("maxTimeout = %d", payload.MaxTimeout)
		}
		_, _ = w.Write([]byte(`{"status":"ok","message":"","solution":{"status":200,"response":"<rss>solved</rss>"}}`))
	}))
	defer server.Close()

	body, status, err := Get(context.Background(), server.Client(), server.URL, "https://indexer.example/api?t=caps", 5*time.Second)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if command != "request.get" || target != "https://indexer.example/api?t=caps" {
		t.Errorf("command/target = %q/%q", command, target)
	}
	if string(body) != "<rss>solved</rss>" || status != 200 {
		t.Errorf("body/status = %q/%d", body, status)
	}
}

func TestFetchForwardsHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Headers map[string]string `json:"headers"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload.Headers["Cookie"] != "session=synthetic" {
			t.Errorf("cookie = %q", payload.Headers["Cookie"])
		}
		if payload.Headers["Authorization"] != "Bearer synthetic" {
			t.Errorf("authorization = %q", payload.Headers["Authorization"])
		}
		if _, present := payload.Headers["Content-Length"]; present {
			t.Error("hop-by-hop content-length forwarded")
		}
		_, _ = w.Write([]byte(`{"status":"ok","solution":{"status":200,"response":"page"}}`))
	}))
	defer server.Close()

	headers := http.Header{
		"Cookie":         []string{"session=synthetic"},
		"Authorization":  []string{"Bearer synthetic"},
		"Content-Length": []string{"12"},
		"Accept":         []string{"text/html"},
	}
	body, status, err := Fetch(context.Background(), server.Client(), server.URL,
		"https://indexer.example/", Options{Headers: headers, Timeout: time.Second})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if string(body) != "page" || status != http.StatusOK {
		t.Errorf("body/status = %q/%d", body, status)
	}
}

func TestFetchRespectsGlobalConcurrency(t *testing.T) {
	old := DefaultMaxConcurrency
	SetMaxConcurrency(1)
	defer SetMaxConcurrency(old)

	var inFlight, peak int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		current := atomic.AddInt32(&inFlight, 1)
		for {
			previous := atomic.LoadInt32(&peak)
			if current <= previous || atomic.CompareAndSwapInt32(&peak, previous, current) {
				break
			}
		}
		time.Sleep(30 * time.Millisecond)
		atomic.AddInt32(&inFlight, -1)
		_, _ = w.Write([]byte(`{"status":"ok","solution":{"status":200,"response":"page"}}`))
	}))
	defer server.Close()

	var wg sync.WaitGroup
	errs := make(chan error, 6)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := Fetch(context.Background(), server.Client(), server.URL,
				"https://indexer.example/", Options{Timeout: 2 * time.Second})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("Fetch: %v", err)
		}
	}
	if got := atomic.LoadInt32(&peak); got > 1 {
		t.Fatalf("peak concurrent solves = %d, want at most 1", got)
	}
}

func TestFetchWaitingForSlotHonoursCancellation(t *testing.T) {
	old := DefaultMaxConcurrency
	SetMaxConcurrency(1)
	defer SetMaxConcurrency(old)

	busy := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(busy)
		<-release
		_, _ = w.Write([]byte(`{"status":"ok","solution":{"status":200,"response":"page"}}`))
	}))
	defer server.Close()

	go func() {
		_, _, _ = Fetch(context.Background(), server.Client(), server.URL,
			"https://indexer.example/one", Options{Timeout: 5 * time.Second})
	}()
	<-busy
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := Fetch(ctx, server.Client(), server.URL,
		"https://indexer.example/two", Options{Timeout: 5 * time.Second}); err == nil {
		t.Fatal("Fetch with a canceled context succeeded while the only slot was busy")
	}
	close(release)
}

func TestGetErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"error","message":"Challenge not detected"}`))
	}))
	defer server.Close()
	_, _, err := Get(context.Background(), server.Client(), server.URL, "https://x.example/", time.Second)
	if err == nil || !strings.Contains(err.Error(), "Challenge not detected") {
		t.Fatalf("error = %v", err)
	}
	if _, _, err := Get(context.Background(), server.Client(), "", "https://x.example/", time.Second); err == nil {
		t.Fatal("empty endpoint accepted")
	}
}
