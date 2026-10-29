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
	"unicode"

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

// grabBatchWindow groups the grabs of one application into a single message.
// A client that sends a season in one go hands over every episode in a few
// seconds; without the window that became one notification per episode.
const grabBatchWindow = 8 * time.Second

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
	if title := singleLine(event.Title, 160); title != "" {
		return title
	}
	return singleLine(event.Source, 160)
}

// TitleFor is the short subject every channel can use as its own title: the
// Apprise and Gotify title, the email subject and the ntfy title. It uses the
// same event words as MessageFor, so a notification never says two things.
// The leading marker is the same one the message opens with, so a title seen
// on its own (an email subject, a notification header) still says how serious
// the event is.
func TitleFor(event Event, lang i18n.Language) string {
	if lang == "" {
		lang = i18n.EN
	}
	source := singleLine(event.Source, 160)
	app := singleLine(event.App, 80)
	var title string
	switch event.Kind {
	case KindNewReleases:
		if source != "" {
			title = i18n.T(lang, "notify.newReleasesFrom", event.Count, source)
		} else {
			title = i18n.T(lang, "notify.newReleases", event.Count)
		}
	case KindIndexerState:
		title = i18n.T(lang, "notify.indexerState", source,
			stateLabel(lang, event.Previous), stateLabel(lang, event.Current))
	case KindGrabFailed:
		if app != "" {
			title = i18n.T(lang, "notify.grabFailedApp", app, grabTitle(event))
		} else {
			title = i18n.T(lang, "notify.grabFailed", grabTitle(event))
		}
	case KindGrabSuccess:
		switch {
		case event.Count > 1 && app != "":
			title = i18n.T(lang, "notify.grabSuccessAppMany", event.Count, app)
		case event.Count > 1:
			title = i18n.T(lang, "notify.grabSuccessMany", event.Count)
		case app != "":
			title = i18n.T(lang, "notify.grabSuccessApp", app, grabTitle(event))
		default:
			title = i18n.T(lang, "notify.grabSuccess", grabTitle(event))
		}
	case KindCatalogUpdated:
		title = i18n.T(lang, "notify.catalogUpdated", event.Count)
	case KindBackupFailed:
		title = i18n.T(lang, "notify.backupFailed")
	case KindTest:
		title = i18n.T(lang, "notify.test")
	case KindIndexerDisabled:
		title = i18n.T(lang, "notify.indexerDisabled", source)
	case KindRunFailed:
		title = i18n.T(lang, "notify.runFailed", source)
	default:
		title = singleLine("indexarr: "+event.Kind, 160)
	}
	if icon := titleIcon(event); icon != "" {
		return icon + " " + title
	}
	return title
}

// MessageFor builds the short, localized message of one event. baseURL is the
// public address of indexarr; when it is empty no link is appended. The first
// line says what happened in plain words; an error adds what went wrong and
// what to do, and keeps the technical text short at the end.
func MessageFor(event Event, lang i18n.Language, baseURL string) string {
	source := singleLine(event.Source, 160)
	app := singleLine(event.App, 80)
	var b strings.Builder
	switch event.Kind {
	case KindNewReleases:
		if source != "" {
			b.WriteString(i18n.T(lang, "notify.msg.newReleasesFrom", source, event.Count))
		} else {
			b.WriteString(i18n.T(lang, "notify.msg.newReleases", event.Count))
		}
	case KindIndexerState:
		from := stateLabel(lang, event.Previous)
		switch strings.ToLower(strings.TrimSpace(event.Current)) {
		case StateOK, StateError, StateBlocked, StateDegraded, StateDisabled:
			b.WriteString(i18n.T(lang, "notify.msg.state."+strings.ToLower(strings.TrimSpace(event.Current)), source, from))
		default:
			b.WriteString(i18n.T(lang, "notify.msg.state.other", source, from, stateLabel(lang, event.Current)))
		}
	case KindGrabFailed:
		title := grabTitle(event)
		if app != "" {
			b.WriteString(i18n.T(lang, "notify.msg.grabFailedApp", app, title))
		} else {
			b.WriteString(i18n.T(lang, "notify.msg.grabFailed", title))
		}
	case KindGrabSuccess:
		title := grabTitle(event)
		switch {
		case event.Count > 1 && app != "":
			b.WriteString(i18n.T(lang, "notify.msg.grabSuccessAppMany", app, event.Count))
		case event.Count > 1:
			b.WriteString(i18n.T(lang, "notify.msg.grabSuccessMany", event.Count))
		case app != "":
			b.WriteString(i18n.T(lang, "notify.msg.grabSuccessApp", app, title))
		default:
			b.WriteString(i18n.T(lang, "notify.msg.grabSuccess", title))
		}
	case KindCatalogUpdated:
		b.WriteString(i18n.T(lang, "notify.msg.catalogUpdated", event.Count))
	case KindBackupFailed:
		b.WriteString(i18n.T(lang, "notify.msg.backupFailed"))
	case KindTest:
		b.WriteString(i18n.T(lang, "notify.msg.test"))
	case KindIndexerDisabled:
		b.WriteString(i18n.T(lang, "notify.msg.indexerDisabled", source))
	case KindRunFailed:
		b.WriteString(i18n.T(lang, "notify.msg.runFailed", source))
	default:
		b.WriteString(singleLine("indexarr: "+event.Kind, 160))
	}
	if event.Error != "" && event.Kind != KindTest {
		writeProblem(&b, lang, event.Error)
	} else if event.Kind == KindIndexerState {
		// Without a technical cause the message still has to say what to do:
		// a state notification is only useful if it suggests a next step.
		if advice := stateAdvice(lang, event.Current); advice != "" {
			b.WriteString("\n" + i18n.T(lang, "notify.advice", advice))
		}
	}
	for _, title := range event.Titles {
		title = singleLine(title, 160)
		if title == "" {
			continue
		}
		b.WriteString("\n• " + title)
	}
	if link := eventLink(baseURL, event.Path); link != "" {
		b.WriteString("\n" + i18n.T(lang, "notify.open", link))
	}
	return b.String()
}

// stateAdvice is the next step for a state change that carries no error text.
// A healthy source needs no action, and a disabled one already says how to
// turn it back on in its own sentence.
func stateAdvice(lang i18n.Language, state string) string {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case StateError:
		return i18n.T(lang, "notify.advice.state.error")
	case StateBlocked:
		return i18n.T(lang, "notify.advice.state.blocked")
	case StateDegraded:
		return i18n.T(lang, "notify.advice.state.degraded")
	case StateOK, StateDisabled:
		return ""
	default:
		return i18n.T(lang, "notify.advice.state.other")
	}
}

// writeProblem explains a raw error: a known problem gets a sentence and a
// suggested action, and the original text follows, shortened, for reference.
func writeProblem(b *strings.Builder, lang i18n.Language, raw string) {
	code := logtext.Problem(raw)
	if code == "" {
		code = logtext.Other
	}
	b.WriteString("\n" + i18n.T(lang, "notify.problem", i18n.T(lang, "logs.problem."+code)))
	b.WriteString("\n" + i18n.T(lang, "notify.advice", i18n.T(lang, "logs.advice."+code)))
	detail := singleLine(raw, 200)
	b.WriteString("\n" + i18n.T(lang, "notify.detail", detail))
}

// singleLine keeps a user-supplied title on one line and within a readable
// length. Release names can carry tabs, newlines and control characters; left
// as they are, they would break the bullet list and the header of an email or
// a push notification.
func singleLine(value string, limit int) string {
	text := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, value)
	text = strings.Join(strings.Fields(text), " ")
	runes := []rune(text)
	if limit > 0 && len(runes) > limit {
		text = string(runes[:limit]) + "…"
	}
	return text
}

// titleIcon is the marker shown at the start of the title, chosen to match
// the first line of the message. It makes an email subject or a push header
// readable before opening it.
func titleIcon(event Event) string {
	switch event.Kind {
	case KindNewReleases:
		return "📥"
	case KindIndexerState:
		switch strings.ToLower(strings.TrimSpace(event.Current)) {
		case StateOK:
			return "✅"
		case StateError:
			return "⚠️"
		case StateBlocked:
			return "⛔"
		case StateDegraded:
			return "🟡"
		case StateDisabled:
			return "⏸"
		default:
			return "ℹ️"
		}
	case KindGrabFailed, KindBackupFailed:
		return "❌"
	case KindRunFailed:
		return "⚠️"
	case KindGrabSuccess, KindTest:
		return "✅"
	case KindCatalogUpdated:
		return "📚"
	case KindIndexerDisabled:
		return "⏸"
	default:
		return ""
	}
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

// grabBatch collects the successful grabs of one application for a short
// window, so a season handed over in one go arrives as one notification.
type grabBatch struct {
	settings store.Settings
	events   []Event
}

// Dispatcher applies the settings policy around Send.
type Dispatcher struct {
	Store *store.Store
	Log   *slog.Logger
	// Now is replaced in tests to exercise the anti-spam window.
	Now func() time.Time
	// Deliver is replaced in tests; it defaults to Send.
	Deliver func(context.Context, Config, Event) error
	// BatchWindow overrides the grab grouping window; zero uses
	// grabBatchWindow.
	BatchWindow time.Duration

	grabMu      sync.Mutex
	grabBatches map[string]*grabBatch
}

// NewDispatcher builds a dispatcher; the store may be nil, in which case the
// anti-spam window and the state ledger are not persisted.
func NewDispatcher(st *store.Store, logger *slog.Logger) *Dispatcher {
	if logger == nil {
		logger = slog.Default()
	}
	return &Dispatcher{
		Store:       st,
		Log:         logger.With("component", "notify"),
		grabBatches: map[string]*grabBatch{},
	}
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
		// same indexer are different events, so the title is the key. A
		// merged grab burst is one event for the whole application.
		if event.Kind == KindGrabSuccess && event.Count > 1 {
			dedupeSource = "batch|" + strings.ToLower(strings.TrimSpace(event.App)) + "|" + strings.TrimSpace(event.Title)
		} else if title := strings.TrimSpace(event.Title); title != "" {
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
			d.Log.Warn("notification failed", "kind", event.Kind, "source", singleLine(event.Source, 160), "error", err)
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
	if event.Kind == KindGrabSuccess {
		d.enqueueGrab(settings, event)
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
		defer cancel()
		d.Dispatch(ctx, settings, event)
	}()
}

// enqueueGrab adds one successful grab to the batch of its application. The
// first event starts the timer; the batch is delivered when the timer fires.
func (d *Dispatcher) enqueueGrab(settings store.Settings, event Event) {
	key := strings.ToLower(strings.TrimSpace(event.App))
	if key == "" {
		key = "unknown"
	}
	d.grabMu.Lock()
	if d.grabBatches == nil {
		d.grabBatches = map[string]*grabBatch{}
	}
	batch := d.grabBatches[key]
	if batch == nil {
		batch = &grabBatch{}
		d.grabBatches[key] = batch
		time.AfterFunc(d.batchWindow(), func() { d.flushGrab(key) })
	}
	batch.settings = settings
	batch.events = append(batch.events, event)
	d.grabMu.Unlock()
}

func (d *Dispatcher) batchWindow() time.Duration {
	if d != nil && d.BatchWindow > 0 {
		return d.BatchWindow
	}
	return grabBatchWindow
}

// flushGrab delivers the batch collected for one application. A batch with a
// single event is delivered unchanged; two or more become one message whose
// bullet list names every release.
func (d *Dispatcher) flushGrab(key string) {
	d.grabMu.Lock()
	batch := d.grabBatches[key]
	delete(d.grabBatches, key)
	d.grabMu.Unlock()
	if batch == nil || len(batch.events) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	d.Dispatch(ctx, batch.settings, mergeGrabEvents(batch.events))
}

// mergeGrabEvents turns a burst into one grab-success event. Count is the
// number of releases; the titles are deduplicated and kept in arrival order.
func mergeGrabEvents(events []Event) Event {
	if len(events) == 0 {
		return Event{Kind: KindGrabSuccess}
	}
	if len(events) == 1 {
		return events[0]
	}
	merged := events[0]
	merged.Kind = KindGrabSuccess
	merged.Count = len(events)
	merged.Titles = nil
	merged.Size = 0
	merged.Categories = nil
	seenTitles := map[string]bool{}
	seenCategories := map[string]bool{}
	for _, event := range events {
		merged.Size += event.Size
		for _, category := range event.Categories {
			if category = strings.TrimSpace(category); category == "" || seenCategories[category] {
				continue
			}
			seenCategories[category] = true
			merged.Categories = append(merged.Categories, category)
		}
		title := grabTitle(event)
		if title == "" || seenTitles[title] {
			continue
		}
		seenTitles[title] = true
		merged.Titles = append(merged.Titles, title)
	}
	return merged
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
