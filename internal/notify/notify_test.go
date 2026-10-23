package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestWebhook(t *testing.T) {
	var received Event
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("request = %s %s", r.Method, r.Header.Get("Content-Type"))
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Error(err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	event := Event{Kind: "new_releases", Source: "MirCrew", Count: 2, Titles: []string{"A", "B"}}
	if err := Send(context.Background(), Config{WebhookURL: server.URL}, event); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if received.Kind != "new_releases" || received.Source != "MirCrew" || received.Count != 2 || len(received.Titles) != 2 {
		t.Errorf("event = %#v", received)
	}
}

func TestTelegram(t *testing.T) {
	var chat, text, path string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_ = r.ParseForm()
		chat = r.Form.Get("chat_id")
		text = r.Form.Get("text")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()
	old := TelegramAPIBase
	TelegramAPIBase = server.URL
	defer func() { TelegramAPIBase = old }()

	event := Event{Kind: "run_failed", Source: "Feed", Error: "HTTP 403", At: time.Now()}
	if err := Send(context.Background(), Config{TelegramToken: "token", TelegramChatID: "42"}, event); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if path != "/bottoken/sendMessage" || chat != "42" {
		t.Errorf("path/chat = %q/%q", path, chat)
	}
	if !strings.Contains(text, "Feed") || !strings.Contains(text, "HTTP 403") {
		t.Errorf("text = %q", text)
	}
}

func TestConfiguredAndErrors(t *testing.T) {
	if Configured(Config{}) {
		t.Error("empty config reported as configured")
	}
	if !Configured(Config{WebhookURL: "http://x"}) {
		t.Error("webhook config not detected")
	}
	if !Configured(Config{TelegramToken: "t", TelegramChatID: "c"}) {
		t.Error("telegram config not detected")
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusBadGateway)
	}))
	defer server.Close()
	if err := Send(context.Background(), Config{WebhookURL: server.URL}, Event{Kind: "x"}); err == nil {
		t.Error("HTTP error not reported")
	}
}
