package feed

import (
	"strings"
	"testing"
)

func TestRedactSecrets(t *testing.T) {
	input := `Get "http://host:9696/api/v1/search?query=x&apikey=0123456789abcdef&passkey=deadbeef": dial tcp: timeout`
	got := redactSecrets(input)
	for _, secret := range []string{"0123456789abcdef", "deadbeef"} {
		if strings.Contains(got, secret) {
			t.Errorf("secret %q leaked in %q", secret, got)
		}
	}
	if !strings.Contains(got, "apikey=***") || !strings.Contains(got, "passkey=***") {
		t.Errorf("secrets not marked: %q", got)
	}
	// A message without credentials is unchanged.
	plain := "HTTP 500: internal error"
	if got := redactSecrets(plain); got != plain {
		t.Errorf("plain message changed: %q", got)
	}
}

func TestRedactSecretsMasksRSSKeyPath(t *testing.T) {
	const rsskey = "0123456789abcdef0123456789abcdef"
	input := `Get "https://tracker.example/rss/` + rsskey + `/latest?rsskey=` + rsskey + `": dial tcp: timeout`
	got := redactSecrets(input)
	if strings.Contains(got, rsskey) {
		t.Fatalf("rsskey leaked in %q", got)
	}
	if !strings.Contains(got, "/rss/***/latest") || !strings.Contains(got, "rsskey=***") {
		t.Errorf("rsskey not marked: %q", got)
	}
	// A normal path and a short id are left alone.
	normal := `Get "http://host/api/v1/search/42": EOF`
	if got := redactSecrets(normal); got != normal {
		t.Errorf("normal URL changed: %q", got)
	}
}

func TestRedactURL(t *testing.T) {
	const rsskey = "abcdefabcdefabcdefabcdefabcdefab"
	got := RedactURL("https://user:pass@tracker.example/rss/" + rsskey + "/feed?rsskey=" + rsskey)
	if strings.Contains(got, rsskey) || strings.Contains(got, "user") || strings.Contains(got, "pass@") {
		t.Fatalf("URL not redacted: %q", got)
	}
	if !strings.Contains(got, "https://tracker.example/rss/***/feed") {
		t.Errorf("unexpected redacted URL: %q", got)
	}
}
