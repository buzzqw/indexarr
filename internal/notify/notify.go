// Package notify sends new-release and failure notifications to a webhook and
// to Telegram. Both channels are optional.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Event is one notification.
type Event struct {
	Kind   string    `json:"event"`
	Source string    `json:"source,omitempty"`
	Count  int       `json:"count,omitempty"`
	Titles []string  `json:"titles,omitempty"`
	Error  string    `json:"error,omitempty"`
	At     time.Time `json:"at"`
}

// Config holds the delivery channels.
type Config struct {
	WebhookURL     string
	TelegramToken  string
	TelegramChatID string
}

// Configured reports whether at least one channel is usable.
func Configured(cfg Config) bool {
	return strings.TrimSpace(cfg.WebhookURL) != "" ||
		(strings.TrimSpace(cfg.TelegramToken) != "" && strings.TrimSpace(cfg.TelegramChatID) != "")
}

// TelegramAPIBase is a variable so tests can point it at a fake server.
var TelegramAPIBase = "https://api.telegram.org"

// Send delivers the event to every configured channel and joins the errors.
func Send(ctx context.Context, cfg Config, event Event) error {
	if event.At.IsZero() {
		event.At = time.Now().UTC()
	}
	var errs []error
	if webhook := strings.TrimSpace(cfg.WebhookURL); webhook != "" {
		if err := sendWebhook(ctx, webhook, event); err != nil {
			errs = append(errs, fmt.Errorf("webhook: %w", err))
		}
	}
	if token, chat := strings.TrimSpace(cfg.TelegramToken), strings.TrimSpace(cfg.TelegramChatID); token != "" && chat != "" {
		if err := sendTelegram(ctx, token, chat, event); err != nil {
			errs = append(errs, fmt.Errorf("telegram: %w", err))
		}
	}
	return errors.Join(errs...)
}

func sendWebhook(ctx context.Context, endpoint string, event Event) error {
	body, err := json.Marshal(event)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "indexarr")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		payload, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return fmt.Errorf("HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(payload)))
	}
	return nil
}

func sendTelegram(ctx context.Context, token, chatID string, event Event) error {
	values := url.Values{}
	values.Set("chat_id", chatID)
	values.Set("text", Message(event))
	values.Set("disable_web_page_preview", "true")
	endpoint := strings.TrimRight(TelegramAPIBase, "/") + "/bot" + token + "/sendMessage"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(values.Encode()))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return fmt.Errorf("%s", strings.ReplaceAll(err.Error(), token, "***"))
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		payload, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		text := strings.ReplaceAll(strings.TrimSpace(string(payload)), token, "***")
		return fmt.Errorf("HTTP %d: %s", response.StatusCode, text)
	}
	return nil
}

// Message is the human-readable Telegram text.
func Message(event Event) string {
	var b strings.Builder
	switch event.Kind {
	case "new_releases":
		fmt.Fprintf(&b, "indexarr: %d new release(s) from %s", event.Count, event.Source)
	case "source_disabled":
		fmt.Fprintf(&b, "indexarr: source %s disabled after repeated failures", event.Source)
		if event.Error != "" {
			b.WriteString("\n" + event.Error)
		}
	case "run_failed":
		fmt.Fprintf(&b, "indexarr: run failed for %s", event.Source)
		if event.Error != "" {
			b.WriteString("\n" + event.Error)
		}
	case "indexer_degraded":
		fmt.Fprintf(&b, "indexarr: indexer %s is degraded", event.Source)
		if event.Error != "" {
			b.WriteString("\n" + event.Error)
		}
	default:
		b.WriteString("indexarr: " + event.Kind)
	}
	for _, title := range event.Titles {
		b.WriteString("\n- " + title)
	}
	return b.String()
}
