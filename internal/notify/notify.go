// Package notify sends indexarr notifications to a webhook, Telegram, Apprise,
// Discord, Slack, Gotify, ntfy, Pushover, email or a custom script. Every
// channel is optional.
//
// A Dispatcher adds the policy that a raw Send does not have: the per-event
// switches from Settings, the anti-spam window and the localized, short
// message with a link to the relevant page.
package notify

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
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
	KindGrabSuccess    = "grab_success"
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
	// Title, App, Size and Categories describe a grab.
	Title      string   `json:"title,omitempty"`
	App        string   `json:"app,omitempty"`
	Size       int64    `json:"size,omitempty"`
	Categories []string `json:"categories,omitempty"`
}

// Config holds the delivery channels.
type Config struct {
	WebhookURL     string
	TelegramToken  string
	TelegramChatID string
	// Apprise posts title/body/type to an Apprise API server. The key is
	// optional and selects one Apprise configuration.
	AppriseURL string
	AppriseKey string
	// Discord and Slack are incoming webhook URLs.
	DiscordURL string
	SlackURL   string
	// Gotify: server URL and application token.
	GotifyURL   string
	GotifyToken string
	// ntfy: server URL, topic and an optional access token.
	NtfyURL   string
	NtfyTopic string
	NtfyToken string
	// Pushover: application token and user/group key.
	PushoverToken string
	PushoverUser  string
	// Email over SMTP. SMTPHost is host:port.
	SMTPHost     string
	SMTPStartTLS bool
	SMTPUsername string
	SMTPPassword string
	SMTPFrom     string
	SMTPTo       string
	// ScriptPath is executed with ScriptArgs; the event travels in the
	// INDEXARR_* environment variables.
	ScriptPath string
	ScriptArgs string
	// Language and BaseURL shape every message: the interface language and,
	// when known, the public address of indexarr.
	Language i18n.Language
	BaseURL  string
	// InstanceName names this indexarr in the custom-script environment.
	InstanceName string
	// Timeout bounds every channel call. Zero uses DefaultTimeout.
	Timeout time.Duration
}

// DefaultTimeout bounds one delivery attempt, including the custom script.
const DefaultTimeout = 15 * time.Second

// Configured reports whether at least one channel is usable.
func Configured(cfg Config) bool {
	return strings.TrimSpace(cfg.WebhookURL) != "" ||
		(strings.TrimSpace(cfg.TelegramToken) != "" && strings.TrimSpace(cfg.TelegramChatID) != "") ||
		strings.TrimSpace(cfg.AppriseURL) != "" ||
		strings.TrimSpace(cfg.DiscordURL) != "" ||
		strings.TrimSpace(cfg.SlackURL) != "" ||
		(strings.TrimSpace(cfg.GotifyURL) != "" && strings.TrimSpace(cfg.GotifyToken) != "") ||
		(strings.TrimSpace(cfg.NtfyURL) != "" && strings.TrimSpace(cfg.NtfyTopic) != "") ||
		(strings.TrimSpace(cfg.PushoverToken) != "" && strings.TrimSpace(cfg.PushoverUser) != "") ||
		(strings.TrimSpace(cfg.SMTPHost) != "" && strings.TrimSpace(cfg.SMTPFrom) != "" && strings.TrimSpace(cfg.SMTPTo) != "") ||
		strings.TrimSpace(cfg.ScriptPath) != ""
}

// TelegramAPIBase is a variable so tests can point it at a fake server.
var TelegramAPIBase = "https://api.telegram.org"

// PushoverAPIBase is a variable so tests can point it at a fake server.
var PushoverAPIBase = "https://api.pushover.net/1/messages.json"

// timeout returns the per-channel bound.
func (cfg Config) timeout() time.Duration {
	if cfg.Timeout > 0 {
		return cfg.Timeout
	}
	return DefaultTimeout
}

// secrets lists every stored secret so an error or a log line can never show
// one. The values are also used to scrub the URL path of a webhook channel.
func (cfg Config) secrets() []string {
	return []string{
		cfg.WebhookURL,
		cfg.TelegramToken,
		cfg.AppriseKey,
		cfg.DiscordURL,
		cfg.SlackURL,
		cfg.GotifyToken,
		cfg.NtfyToken,
		cfg.PushoverToken,
		cfg.PushoverUser,
		cfg.SMTPPassword,
	}
}

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
// The channels run concurrently, each with its own timeout, so one slow
// service never prevents the others from receiving the message.
func Send(ctx context.Context, cfg Config, event Event) error {
	if event.At.IsZero() {
		event.At = time.Now().UTC()
	}
	lang := cfg.Language
	if lang == "" {
		lang = i18n.EN
	}
	message := MessageFor(event, lang, cfg.BaseURL)
	title := TitleFor(event, lang)
	channels := outbound(cfg, event, message, title)
	results := make([]error, len(channels))
	var wait sync.WaitGroup
	for index := range channels {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			limit := cfg.timeout()
			callCtx, cancel := context.WithTimeout(ctx, limit)
			defer cancel()
			results[index] = channels[index].run(callCtx)
		}(index)
	}
	wait.Wait()
	var errs []error
	for index, err := range results {
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", channels[index].name, err))
		}
	}
	return errors.Join(errs...)
}

// scrubSecrets removes every known secret from an error text. Transport errors
// often include the full URL, which may carry a token in its path or query.
func scrubSecrets(err error, secrets ...string) error {
	if err == nil {
		return nil
	}
	message := err.Error()
	for _, secret := range secrets {
		if secret = strings.TrimSpace(secret); secret != "" {
			message = strings.ReplaceAll(message, secret, "***")
		}
	}
	// A token inside a URL path is also a long opaque word: mask the usual
	// query parameters even when the exact value is not known.
	message = redactQuery(message)
	return fmt.Errorf("%s", message)
}

var querySecret = []string{"apikey=", "api_key=", "token=", "access_token=", "auth=", "passkey=", "rsskey=", "torrent_pass="}

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

// Message is the human-readable text in English. It is kept for callers and
// tests that do not know the interface language.
func Message(event Event) string {
	return MessageFor(event, i18n.EN, "")
}

func grabTitle(event Event) string {
	if title := strings.TrimSpace(event.Title); title != "" {
		return title
	}
	return strings.TrimSpace(event.Source)
}

// TitleFor is the short subject every channel can use as its own title: the
// Apprise and Gotify title, the email subject and the ntfy title. It uses the
// same event words as MessageFor, so a notification never says two things.
func TitleFor(event Event, lang i18n.Language) string {
	if lang == "" {
		lang = i18n.EN
	}
	switch event.Kind {
	case KindNewReleases:
		if event.Source != "" {
			return i18n.T(lang, "notify.newReleasesFrom", event.Count, event.Source)
		}
		return i18n.T(lang, "notify.newReleases", event.Count)
	case KindIndexerState:
		return i18n.T(lang, "notify.indexerState", event.Source,
			stateLabel(lang, event.Previous), stateLabel(lang, event.Current))
	case KindGrabFailed:
		if event.App != "" {
			return i18n.T(lang, "notify.grabFailedApp", event.App, grabTitle(event))
		}
		return i18n.T(lang, "notify.grabFailed", grabTitle(event))
	case KindGrabSuccess:
		if event.App != "" {
			return i18n.T(lang, "notify.grabSuccessApp", event.App, grabTitle(event))
		}
		return i18n.T(lang, "notify.grabSuccess", grabTitle(event))
	case KindCatalogUpdated:
		return i18n.T(lang, "notify.catalogUpdated", event.Count)
	case KindBackupFailed:
		return i18n.T(lang, "notify.backupFailed")
	case KindTest:
		return i18n.T(lang, "notify.test")
	case KindIndexerDisabled:
		return i18n.T(lang, "notify.indexerDisabled", event.Source)
	case KindRunFailed:
		return i18n.T(lang, "notify.runFailed", event.Source)
	default:
		return "indexarr: " + event.Kind
	}
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
		title := grabTitle(event)
		if event.App != "" {
			b.WriteString(i18n.T(lang, "notify.msg.grabFailedApp", event.App, title))
		} else {
			b.WriteString(i18n.T(lang, "notify.msg.grabFailed", title))
		}
	case KindGrabSuccess:
		title := grabTitle(event)
		if event.App != "" {
			b.WriteString(i18n.T(lang, "notify.msg.grabSuccessApp", event.App, title))
		} else {
			b.WriteString(i18n.T(lang, "notify.msg.grabSuccess", title))
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
	case KindGrabSuccess:
		return settings.NotifyGrabSuccess
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
	// An explicit notification language wins; otherwise the messages follow
	// the interface language the operator last chose.
	lang := i18n.Normalize(settings.NotifyLanguage)
	if strings.TrimSpace(settings.NotifyLanguage) == "" {
		lang = i18n.Normalize(settings.UILanguage)
	}
	return Config{
		WebhookURL:     settings.NotifyWebhookURL,
		TelegramToken:  settings.NotifyTelegramToken,
		TelegramChatID: settings.NotifyTelegramChatID,
		AppriseURL:     settings.NotifyAppriseURL,
		AppriseKey:     settings.NotifyAppriseKey,
		DiscordURL:     settings.NotifyDiscordURL,
		SlackURL:       settings.NotifySlackURL,
		GotifyURL:      settings.NotifyGotifyURL,
		GotifyToken:    settings.NotifyGotifyToken,
		NtfyURL:        settings.NotifyNtfyURL,
		NtfyTopic:      settings.NotifyNtfyTopic,
		NtfyToken:      settings.NotifyNtfyToken,
		PushoverToken:  settings.NotifyPushoverToken,
		PushoverUser:   settings.NotifyPushoverUser,
		SMTPHost:       settings.NotifySMTPHost,
		SMTPStartTLS:   settings.NotifySMTPStartTLS,
		SMTPUsername:   settings.NotifySMTPUsername,
		SMTPPassword:   settings.NotifySMTPPassword,
		SMTPFrom:       settings.NotifySMTPFrom,
		SMTPTo:         settings.NotifySMTPTo,
		ScriptPath:     settings.NotifyScriptPath,
		ScriptArgs:     settings.NotifyScriptArgs,
		Language:       lang,
		BaseURL:        settings.PublicURL,
		InstanceName:   "indexarr",
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
	switch event.Kind {
	case KindIndexerState:
		// A change into a different state is a different event: error then
		// blocked then recovery must all be delivered. Repeating the same
		// transition (a flapping site) is throttled.
		dedupeSource += "|" + strings.ToLower(strings.TrimSpace(event.Current))
	case KindGrabSuccess, KindGrabFailed:
		// The source of a grab is the indexer; different releases from the
		// same indexer are different events, so the title is the key.
		if title := strings.TrimSpace(event.Title); title != "" {
			dedupeSource = title
		}
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
