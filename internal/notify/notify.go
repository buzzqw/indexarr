// Package notify sends indexarr notifications to a webhook and to Telegram.
// Both channels are optional.
//
// A Dispatcher adds the policy that a raw Send does not have: the per-event
// switches from Settings, the anti-spam window and the localized, short
// message with a link to the relevant page.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/buzzqw/indexarr/internal/i18n"
	"github.com/buzzqw/indexarr/internal/logtext"
	"github.com/buzzqw/indexarr/internal/store"
)

// Event kinds. They are stable identifiers used by the settings switches and
// by the anti-spam ledger.
const (
	KindNewReleases    = "new_releases"
	KindIndexerState   = "indexer_state"
	KindGrabFailed     = "grab_failed"
	KindCatalogUpdated = "catalog_updated"
	KindBackupFailed   = "backup_failed"
	// KindRunFailed is kept for older senders and tests; new code reports a
	// state change through KindIndexerState.
	KindRunFailed       = "run_failed"
	KindIndexerDisabled = "indexer_disabled"
	KindTest            = "test"
)

// Indexer conditions reported in a state-change notification.
const (
	StateOK       = "ok"
	StateError    = "error"
	StateBlocked  = "blocked"
	StateDegraded = "degraded"
	StateDisabled = "disabled"
)

// Event is one notification.
type Event struct {
	Kind   string    `json:"event"`
	Source string    `json:"source,omitempty"`
	Count  int       `json:"count,omitempty"`
	Titles []string  `json:"titles,omitempty"`
	Error  string    `json:"error,omitempty"`
	At     time.Time `json:"at"`
	// Path is the indexarr page of the event, relative to the public URL.
	Path string `json:"path,omitempty"`
	// Previous and Current describe an indexer state change.
	Previous string `json:"previous,omitempty"`
	Current  string `json:"current,omitempty"`
	// Title and App belong to a failed grab.
	Title string `json:"title,omitempty"`
	App   string `json:"app,omitempty"`
}

// Config holds the delivery channels.
type Config struct {
	WebhookURL     string
	TelegramToken  string
	TelegramChatID string
	// Language and BaseURL shape the Telegram message: the interface
	// language and, when known, the public address of indexarr.
	Language i18n.Language
	BaseURL  string
}

// Configured reports whether at least one channel is usable.
func Configured(cfg Config) bool {
	return strings.TrimSpace(cfg.WebhookURL) != "" ||
		(strings.TrimSpace(cfg.TelegramToken) != "" && strings.TrimSpace(cfg.TelegramChatID) != "")
}

// TelegramAPIBase is a variable so tests can point it at a fake server.
var TelegramAPIBase = "https://api.telegram.org"

// appContextKey carries the downstream application name of a grab.
type appContextKey struct{}

// WithApp annotates a grab with the application that asked for it (Sonarr,
// Radarr, Prowlarr, the web interface). The download resolver reads it for the
// failed-grab notification.
func WithApp(ctx context.Context, app string) context.Context {
	app = strings.TrimSpace(app)
	if app == "" {
		return ctx
	}
	return context.WithValue(ctx, appContextKey{}, app)
}

// AppFrom returns the application name attached to a grab, or "".
func AppFrom(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	value, _ := ctx.Value(appContextKey{}).(string)
	return strings.TrimSpace(value)
}

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
		if err := sendTelegram(ctx, token, chat, cfg.Language, cfg.BaseURL, event); err != nil {
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
		return scrub(err, endpoint)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "indexarr")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return scrub(err, endpoint)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		payload, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return scrub(fmt.Errorf("HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(payload))), endpoint)
	}
	return nil
}

func sendTelegram(ctx context.Context, token, chatID string, lang i18n.Language, baseURL string, event Event) error {
	if lang == "" {
		lang = i18n.EN
	}
	values := url.Values{}
	values.Set("chat_id", chatID)
	values.Set("text", MessageFor(event, lang, baseURL))
	values.Set("disable_web_page_preview", "true")
	endpoint := strings.TrimRight(TelegramAPIBase, "/") + "/bot" + token + "/sendMessage"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(values.Encode()))
	if err != nil {
		return scrub(err, token)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return scrub(err, token)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		payload, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return scrub(fmt.Errorf("HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(payload))), token)
	}
	return nil
}

// scrub removes a secret from an error text. Transport errors often include
// the full URL, which may carry the webhook token or the Telegram bot token.
func scrub(err error, secret string) error {
	if err == nil {
		return nil
	}
	message := err.Error()
	if secret = strings.TrimSpace(secret); secret != "" {
		message = strings.ReplaceAll(message, secret, "***")
	}
	// A token inside a URL path is also a long opaque word: mask the usual
	// query parameters even when the exact value is not known.
	message = redactQuery(message)
	return fmt.Errorf("%s", message)
}

var querySecret = []string{"apikey=", "api_key=", "token=", "passkey=", "rsskey=", "torrent_pass="}

func redactQuery(text string) string {
	lower := strings.ToLower(text)
	for _, key := range querySecret {
		for {
			index := strings.Index(lower, key)
			if index < 0 {
				break
			}
			start := index + len(key)
			end := start
			for end < len(text) && !strings.ContainsRune("& \t\"'", rune(text[end])) {
				end++
			}
			text = text[:start] + "***" + text[end:]
			lower = strings.ToLower(text)
		}
	}
	return text
}

// Message is the human-readable Telegram text in English. It is kept for
// callers and tests that do not know the interface language.
func Message(event Event) string {
	return MessageFor(event, i18n.EN, "")
}

// MessageFor builds the short, localized message of one event. baseURL is the
// public address of indexarr; when it is empty no link is appended. The first
// line says what happened in plain words; an error adds what went wrong and
// what to do, and keeps the technical text short at the end.
func MessageFor(event Event, lang i18n.Language, baseURL string) string {
	var b strings.Builder
	switch event.Kind {
	case KindNewReleases:
		if event.Source != "" {
			b.WriteString(i18n.T(lang, "notify.msg.newReleasesFrom", event.Source, event.Count))
		} else {
			b.WriteString(i18n.T(lang, "notify.msg.newReleases", event.Count))
		}
	case KindIndexerState:
		from := stateLabel(lang, event.Previous)
		switch strings.ToLower(strings.TrimSpace(event.Current)) {
		case StateOK, StateError, StateBlocked, StateDegraded, StateDisabled:
			b.WriteString(i18n.T(lang, "notify.msg.state."+strings.ToLower(strings.TrimSpace(event.Current)), event.Source, from))
		default:
			b.WriteString(i18n.T(lang, "notify.msg.state.other", event.Source, from, stateLabel(lang, event.Current)))
		}
	case KindGrabFailed:
		title := event.Title
		if title == "" {
			title = event.Source
		}
		if event.App != "" {
			b.WriteString(i18n.T(lang, "notify.msg.grabFailedApp", event.App, title))
		} else {
			b.WriteString(i18n.T(lang, "notify.msg.grabFailed", title))
		}
	case KindCatalogUpdated:
		b.WriteString(i18n.T(lang, "notify.msg.catalogUpdated", event.Count))
	case KindBackupFailed:
		b.WriteString(i18n.T(lang, "notify.msg.backupFailed"))
	case KindTest:
		b.WriteString(i18n.T(lang, "notify.msg.test"))
	case KindIndexerDisabled:
		b.WriteString(i18n.T(lang, "notify.msg.indexerDisabled", event.Source))
	case KindRunFailed:
		b.WriteString(i18n.T(lang, "notify.msg.runFailed", event.Source))
	default:
		b.WriteString("indexarr: " + event.Kind)
	}
	if event.Error != "" && event.Kind != KindTest {
		writeProblem(&b, lang, event.Error)
	}
	for _, title := range event.Titles {
		if strings.TrimSpace(title) == "" {
			continue
		}
		b.WriteString("\n• " + title)
	}
	if link := eventLink(baseURL, event.Path); link != "" {
		b.WriteString("\n" + i18n.T(lang, "notify.open", link))
	}
	return b.String()
}

// writeProblem explains a raw error: a known problem gets a sentence and a
// suggested action, and the original text follows, shortened, for reference.
func writeProblem(b *strings.Builder, lang i18n.Language, raw string) {
	if code := logtext.Problem(raw); code != "" {
		b.WriteString("\n" + i18n.T(lang, "notify.problem", i18n.T(lang, "logs.problem."+code)))
		b.WriteString("\n" + i18n.T(lang, "notify.advice", i18n.T(lang, "logs.advice."+code)))
	}
	detail := strings.TrimSpace(raw)
	if runes := []rune(detail); len(runes) > 200 {
		detail = string(runes[:200]) + "…"
	}
	b.WriteString("\n" + i18n.T(lang, "notify.detail", detail))
}

func stateLabel(lang i18n.Language, state string) string {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case StateOK:
		return i18n.T(lang, "notify.state.ok")
	case StateError:
		return i18n.T(lang, "notify.state.error")
	case StateBlocked:
		return i18n.T(lang, "notify.state.blocked")
	case StateDegraded:
		return i18n.T(lang, "notify.state.degraded")
	case StateDisabled:
		return i18n.T(lang, "notify.state.disabled")
	default:
		return i18n.T(lang, "notify.state.unknown")
	}
}

func eventLink(baseURL, path string) string {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	path = strings.TrimSpace(path)
	if baseURL == "" || path == "" {
		return ""
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return baseURL + path
}

// Dispatcher applies the settings policy around Send.
type Dispatcher struct {
	Store *store.Store
	Log   *slog.Logger
	// Now is replaced in tests to exercise the anti-spam window.
	Now func() time.Time
	// Deliver is replaced in tests; it defaults to Send.
	Deliver func(context.Context, Config, Event) error
}

// NewDispatcher builds a dispatcher; the store may be nil, in which case the
// anti-spam window and the state ledger are not persisted.
func NewDispatcher(st *store.Store, logger *slog.Logger) *Dispatcher {
	if logger == nil {
		logger = slog.Default()
	}
	return &Dispatcher{Store: st, Log: logger.With("component", "notify")}
}

func (d *Dispatcher) now() time.Time {
	if d != nil && d.Now != nil {
		return d.Now().UTC()
	}
	return time.Now().UTC()
}

func (d *Dispatcher) deliver(ctx context.Context, cfg Config, event Event) error {
	if d != nil && d.Deliver != nil {
		return d.Deliver(ctx, cfg, event)
	}
	return Send(ctx, cfg, event)
}

// Enabled reports whether the event kind is switched on in Settings.
func Enabled(settings store.Settings, kind string) bool {
	switch kind {
	case KindNewReleases:
		return settings.NotifyNewReleases
	case KindIndexerState, KindRunFailed, KindIndexerDisabled:
		return settings.NotifyFailures
	case KindGrabFailed:
		return settings.NotifyGrabFailed
	case KindCatalogUpdated:
		return settings.NotifyCatalogUpdated
	case KindBackupFailed:
		return settings.NotifyBackupFailed
	case KindTest:
		return true
	default:
		return false
	}
}

// ConfigFrom builds the delivery config from the (already decrypted)
// settings.
func ConfigFrom(settings store.Settings) Config {
	lang := i18n.Normalize(settings.UILanguage)
	return Config{
		WebhookURL:     settings.NotifyWebhookURL,
		TelegramToken:  settings.NotifyTelegramToken,
		TelegramChatID: settings.NotifyTelegramChatID,
		Language:       lang,
		BaseURL:        settings.PublicURL,
	}
}

// Dispatch delivers one event when its switch is on, the channels are
// configured and the anti-spam window has elapsed. It reports whether the
// event was delivered. New releases are grouped per pass by the caller, so
// they are not throttled a second time.
func (d *Dispatcher) Dispatch(ctx context.Context, settings store.Settings, event Event) bool {
	if !Enabled(settings, event.Kind) {
		return false
	}
	cfg := ConfigFrom(settings)
	if !Configured(cfg) {
		return false
	}
	if event.At.IsZero() {
		event.At = d.now()
	}
	dedupeSource := event.Source
	if event.Kind == KindIndexerState {
		// A change into a different state is a different event: error then
		// blocked then recovery must all be delivered. Repeating the same
		// transition (a flapping site) is throttled.
		dedupeSource += "|" + strings.ToLower(strings.TrimSpace(event.Current))
	}
	if d.Store != nil && event.Kind != KindNewReleases && event.Kind != KindTest {
		last, err := d.Store.NotificationSentAt(ctx, event.Kind, dedupeSource)
		if err == nil && !last.IsZero() && event.At.Sub(last) < settings.NotifyCooldown() {
			return false
		}
	}
	if err := d.deliver(ctx, cfg, event); err != nil {
		if d.Log != nil {
			// The error is already scrubbed by Send/scrub.
			d.Log.Warn("notification failed", "kind", event.Kind, "error", err)
		}
		return false
	}
	if d.Store != nil {
		if err := d.Store.RecordNotification(ctx, event.Kind, dedupeSource, event.At); err != nil && d.Log != nil {
			d.Log.Warn("cannot record the notification timestamp", "kind", event.Kind, "error", err)
		}
	}
	return true
}

// DispatchAsync sends one event in the background, with a bounded timeout, so
// a slow webhook never delays a scheduler pass or an HTTP response.
func (d *Dispatcher) DispatchAsync(ctx context.Context, settings store.Settings, event Event) {
	if d == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
		defer cancel()
		d.Dispatch(ctx, settings, event)
	}()
}

// SendTest delivers the test notification, bypassing the event switches, the
// anti-spam window and the catalog of a previous send. It returns the delivery
// error so the settings page can show it.
func (d *Dispatcher) SendTest(ctx context.Context, settings store.Settings) error {
	cfg := ConfigFrom(settings)
	if !Configured(cfg) {
		return errors.New("no notification channel is configured")
	}
	event := Event{Kind: KindTest, At: d.now(), Path: "/settings?section=notifications"}
	return d.deliver(ctx, cfg, event)
}
