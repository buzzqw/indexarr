package feed

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// mappedClient sends requests for a fake host to the test server, so the
// client sees a non-local target (and therefore may use FlareSolverr).
func mappedClient(host string, target *httptest.Server) *http.Client {
	dialer := &net.Dialer{}
	return &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			name, _, err := net.SplitHostPort(addr)
			if err == nil && strings.EqualFold(name, host) {
				addr = target.Listener.Addr().String()
			}
			return dialer.DialContext(ctx, network, addr)
		},
	}}
}

func flareSolverrServer(t *testing.T, calls *atomic.Int64, solution string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/v1" {
			t.Errorf("flaresolverr request = %s %s", r.Method, r.URL.Path)
		}
		var payload struct {
			Cmd string `json:"cmd"`
			URL string `json:"url"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode payload: %v", err)
		}
		if payload.Cmd != "request.get" || !strings.HasPrefix(payload.URL, "http") {
			t.Errorf("payload = %#v", payload)
		}
		reply, err := json.Marshal(map[string]any{
			"status": "ok",
			"solution": map[string]any{
				"status":   200,
				"response": solution,
			},
		})
		if err != nil {
			t.Error(err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(reply)
	}))
}

func challengeServer(t *testing.T, calls *atomic.Int64) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("cf-mitigated", "challenge")
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `<html><head><title>Just a moment...</title></head><body>cloudflare</body></html>`)
	}))
}

func TestRSSThroughFlareSolverr(t *testing.T) {
	var fsCalls, directCalls atomic.Int64
	fs := flareSolverrServer(t, &fsCalls, `<rss version="2.0"><channel><title>solved</title>
		<item><title>RSS through solver</title>
		<link>magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567</link></item>
		</channel></rss>`)
	defer fs.Close()
	indexer := challengeServer(t, &directCalls)
	defer indexer.Close()

	items, err := FetchRSS(context.Background(), mappedClient("feed.example", indexer),
		"http://feed.example/feed.xml", RequestOptions{FlareSolverr: fs.URL})
	if err != nil {
		t.Fatalf("FetchRSS: %v", err)
	}
	if len(items) != 1 || items[0].Title != "RSS through solver" {
		t.Fatalf("items = %#v", items)
	}
	if fsCalls.Load() != 1 || directCalls.Load() != 1 {
		t.Errorf("flare calls = %d, direct calls = %d", fsCalls.Load(), directCalls.Load())
	}
}

func TestTorznabThroughFlareSolverr(t *testing.T) {
	var fsCalls, directCalls atomic.Int64
	fs := flareSolverrServer(t, &fsCalls, torznabResponse)
	defer fs.Close()
	indexer := challengeServer(t, &directCalls)
	defer indexer.Close()

	client := NewClient(ClientConfig{
		URL:          "http://indexer.example/api",
		Manager:      ManagerTorznab,
		APIKey:       "secret",
		FlareSolverr: fs.URL,
		HTTPClient:   mappedClient("indexer.example", indexer),
	})
	items, err := client.Fetch(context.Background(), "Show", 10)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("items = %d, want 3", len(items))
	}
	if fsCalls.Load() != 1 || directCalls.Load() != 1 {
		t.Errorf("flare calls = %d, direct calls = %d", fsCalls.Load(), directCalls.Load())
	}
}

func TestFlareSolverrSkippedForLocalTarget(t *testing.T) {
	var fsCalls, directCalls atomic.Int64
	fs := flareSolverrServer(t, &fsCalls, torznabResponse)
	defer fs.Close()
	indexer := challengeServer(t, &directCalls)
	defer indexer.Close()

	client := NewClient(ClientConfig{
		URL:          indexer.URL,
		Manager:      ManagerTorznab,
		FlareSolverr: fs.URL,
	})
	if _, err := client.Fetch(context.Background(), "Show", 10); err == nil {
		t.Fatal("a local 403 must not be presented as a solved page")
	}
	if fsCalls.Load() != 0 {
		t.Errorf("FlareSolverr called %d times for a local target", fsCalls.Load())
	}
}

func TestChallengeDetection(t *testing.T) {
	htmlChallenge := http.Header{"Content-Type": {"text/html; charset=utf-8"}}
	html := []byte(`<html><title>Just a moment...</title><body>cf-chl</body></html>`)
	if !isChallengeResponse(http.StatusForbidden, htmlChallenge, html) {
		t.Error("403 HTML challenge not detected")
	}
	if !isChallengeResponse(http.StatusOK, htmlChallenge, html) {
		t.Error("200 HTML challenge not detected")
	}
	cfHeader := http.Header{"Cf-Mitigated": {"challenge"}}
	if !isChallengeResponse(http.StatusForbidden, cfHeader, []byte("")) {
		t.Error("cf-mitigated not detected")
	}
	xmlError := http.Header{"Content-Type": {"application/xml"}}
	if isChallengeResponse(http.StatusForbidden, xmlError, []byte(`<error description="cloudflare"/>>`)) {
		t.Error("an XML API error was mistaken for a challenge")
	}
}

func TestLocalTarget(t *testing.T) {
	for _, raw := range []string{
		"http://127.0.0.1:9117/api",
		"http://localhost:9117/api",
		"http://[::1]:9117/api",
		"http://192.168.1.10:9117/api",
		"http://prowlarr.local:9117/api",
		"http://10.0.0.5/api",
	} {
		if !isLocalTarget(raw) {
			t.Errorf("%s should be local", raw)
		}
	}
	for _, raw := range []string{
		"https://indexer.example/api",
		"http://8.8.8.8/api",
	} {
		if isLocalTarget(raw) {
			t.Errorf("%s should not be local", raw)
		}
	}
}
