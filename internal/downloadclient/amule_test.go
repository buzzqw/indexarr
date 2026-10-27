package downloadclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestAmuleSendsEd2kLink(t *testing.T) {
	var logins, adds atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/login":
			logins.Add(1)
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["password"] != "secret" {
				t.Errorf("password = %q", body["password"])
			}
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "ok", Path: "/"})
			_, _ = w.Write([]byte(`{"role":"admin"}`))
		case "/api/v1/downloads":
			if cookie, err := r.Cookie("session"); err != nil || cookie.Value != "ok" {
				t.Errorf("downloads without session cookie: %v", err)
			}
			adds.Add(1)
			var body struct {
				Links []string `json:"links"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if len(body.Links) != 1 || !strings.HasPrefix(body.Links[0], "ed2k://|file|") {
				t.Errorf("links = %v", body.Links)
			}
			_, _ = w.Write([]byte(`{"results":[{"ok":true}]}`))
		case "/api/v1/version":
			_, _ = w.Write([]byte(`{"amuleapi_version":"3.1.0","daemon_version":"3.1.0"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := New(Config{Kind: KindAmule, URL: server.URL, Password: "secret"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	link := "ed2k://|file|Rare%20Movie.mkv|700|0123456789abcdef0123456789abcdef|/"
	if err := client.Send(context.Background(), Item{Title: "Rare Movie", Ed2k: link}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	description, err := client.Test(context.Background())
	if err != nil || description != "aMule 3.1.0" {
		t.Fatalf("Test = %q, %v", description, err)
	}
	if logins.Load() != 1 {
		t.Errorf("logins = %d, want 1", logins.Load())
	}
	if adds.Load() != 1 {
		t.Errorf("adds = %d, want 1", adds.Load())
	}
}

func TestAmuleReportsRejectedLink(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/login":
			_, _ = w.Write([]byte(`{"role":"admin"}`))
		case "/api/v1/downloads":
			w.WriteHeader(http.StatusMultiStatus)
			_, _ = w.Write([]byte(`{"results":[{"ok":false,"error":{"code":"invalid_link","message":"the link is not valid"}}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client, err := New(Config{Kind: KindAmule, URL: server.URL, Password: "secret"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	err = client.Send(context.Background(), Item{Ed2k: "ed2k://|file|x.mkv|10|0123456789abcdef0123456789abcdef|/"})
	if err == nil || !strings.Contains(err.Error(), "the link is not valid") {
		t.Fatalf("Send error = %v", err)
	}
}

func TestAmuleRequiresPassword(t *testing.T) {
	client, err := New(Config{Kind: KindAmule, URL: "http://127.0.0.1:4711"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := client.Send(context.Background(), Item{Ed2k: "ed2k://|file|x.mkv|10|0123456789abcdef0123456789abcdef|/"}); err == nil {
		t.Fatal("expected a missing password error")
	}
}
