package flaresolverr

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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
