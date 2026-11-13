package logsafe

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

func TestRedactAndHandlerMaskCredentials(t *testing.T) {
	const secret = "super-secret-value-1234"
	for _, input := range []string{
		"https://host/api?apikey=" + secret,
		"https://host/api?passkey=" + secret + "&x=1",
		"https://host/api?rsskey=" + secret,
		"https://host/api?torrent_pass=" + secret,
		"https://host/api?u=user&e=" + secret,
		"https://user:" + secret + "@host/path",
	} {
		if out := Redact(input); strings.Contains(out, secret) {
			t.Fatalf("Redact(%q) leaked the secret: %q", input, out)
		}
	}
	var buf bytes.Buffer
	logger := slog.New(NewHandler(slog.NewTextHandler(&buf, nil)))
	logger.LogAttrs(context.Background(), slog.LevelError, "live search failed",
		slog.String("error", "GET https://host/api?apikey="+secret))
	if strings.Contains(buf.String(), secret) {
		t.Fatalf("handler leaked the secret: %s", buf.String())
	}
	if !strings.Contains(buf.String(), "supe…") {
		t.Fatalf("handler did not mask the value: %s", buf.String())
	}
}

func TestRedactHeadersWebhooksAndJSON(t *testing.T) {
	const secret = "abcdef1234567890"
	for name, input := range map[string]string{
		"authorization": "Authorization: Bearer " + secret,
		"bearer":        "sent with bearer " + secret,
		"x-api-key":     "X-Api-Key: " + secret,
		"discord":       "POST https://discord.com/api/webhooks/123456/" + secret,
		"telegram":      "https://api.telegram.org/bot123456:" + secret + "/sendMessage",
		"json":          `{"password":"` + secret + `","user":"me"}`,
	} {
		if out := Redact(input); strings.Contains(out, secret) {
			t.Errorf("%s leaked the secret: %q", name, out)
		}
	}
}

func TestHandlerRedactsErrorAttributes(t *testing.T) {
	const secret = "abcdef1234567890"
	var buf bytes.Buffer
	logger := slog.New(NewHandler(slog.NewTextHandler(&buf, nil)))
	logger.Error("login failed", "error",
		errors.New("POST https://host/api?apikey="+secret+": 401"))
	if strings.Contains(buf.String(), secret) {
		t.Fatalf("KindAny error leaked the secret: %s", buf.String())
	}
	if !strings.Contains(buf.String(), "abcd…") {
		t.Fatalf("KindAny error did not mask the value: %s", buf.String())
	}
}
