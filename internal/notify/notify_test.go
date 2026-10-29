package notify

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/buzzqw/indexarr/internal/i18n"
	"github.com/buzzqw/indexarr/internal/store"
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

	event := Event{Kind: KindNewReleases, Source: "MirCrew", Count: 2, Titles: []string{"A", "B"}}
	if err := Send(context.Background(), Config{WebhookURL: server.URL}, event); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if received.Kind != KindNewReleases || received.Source != "MirCrew" || received.Count != 2 || len(received.Titles) != 2 {
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

	event := Event{Kind: KindRunFailed, Source: "Feed", Error: "HTTP 403", At: time.Now(), Path: "/indexers"}
	cfg := Config{
		TelegramToken: "token", TelegramChatID: "42",
		Language: i18n.IT, BaseURL: "https://idx.example",
	}
	if err := Send(context.Background(), cfg, event); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if path != "/bottoken/sendMessage" || chat != "42" {
		t.Errorf("path/chat = %q/%q", path, chat)
	}
	if !strings.Contains(text, "Feed") || !strings.Contains(text, "HTTP 403") {
		t.Errorf("text = %q", text)
	}
	if !strings.Contains(text, "L'aggiornamento di Feed non è riuscito") || !strings.Contains(text, "Cosa non va:") {
		t.Errorf("the Telegram message is not in the interface language: %q", text)
	}
	if !strings.Contains(text, "https://idx.example/indexers") {
		t.Errorf("the Telegram message has no public link: %q", text)
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
	for name, cfg := range map[string]Config{
		"apprise":  {AppriseURL: "http://apprise"},
		"discord":  {DiscordURL: "http://discord"},
		"slack":    {SlackURL: "http://slack"},
		"gotify":   {GotifyURL: "http://gotify", GotifyToken: "t"},
		"ntfy":     {NtfyURL: "http://ntfy", NtfyTopic: "topic"},
		"pushover": {PushoverToken: "t", PushoverUser: "u"},
		"email":    {SMTPHost: "smtp:587", SMTPFrom: "a@b.c", SMTPTo: "d@e.f"},
		"script":   {ScriptPath: "/bin/true"},
	} {
		if !Configured(cfg) {
			t.Errorf("%s config not detected", name)
		}
	}
	if Configured(Config{GotifyURL: "http://gotify"}) || Configured(Config{NtfyTopic: "topic"}) ||
		Configured(Config{PushoverToken: "t"}) || Configured(Config{SMTPHost: "smtp:587"}) {
		t.Error("an incomplete channel reported as configured")
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusBadGateway)
	}))
	defer server.Close()
	if err := Send(context.Background(), Config{WebhookURL: server.URL}, Event{Kind: "x"}); err == nil {
		t.Error("HTTP error not reported")
	}
}

// TestConfigFromPrefersTheChosenNotificationLanguage guards the two-level
// language: an explicit choice wins, otherwise the messages follow the
// interface language the operator last chose.
func TestConfigFromPrefersTheChosenNotificationLanguage(t *testing.T) {
	settings := store.Settings{
		UILanguage: "zh", NotifyLanguage: "it",
		NotifyDiscordURL: "https://discord.example/hook", NotifyNtfyTopic: "indexarr",
		NotifySMTPHost: "smtp.example.com:587", NotifyScriptPath: "/bin/true",
	}
	cfg := ConfigFrom(settings)
	if cfg.Language != i18n.IT {
		t.Errorf("chosen language = %q, want it", cfg.Language)
	}
	if cfg.DiscordURL != settings.NotifyDiscordURL || cfg.NtfyTopic != settings.NotifyNtfyTopic ||
		cfg.SMTPHost != settings.NotifySMTPHost || cfg.ScriptPath != settings.NotifyScriptPath {
		t.Errorf("channel config = %#v", cfg)
	}
	settings.NotifyLanguage = ""
	if got := ConfigFrom(settings).Language; got != i18n.ZH {
		t.Errorf("fallback language = %q, want zh", got)
	}
	message := MessageFor(Event{Kind: KindIndexerState, Source: "ExtTo", Previous: StateError, Current: StateOK},
		ConfigFrom(store.Settings{UILanguage: "zh", NotifyLanguage: "it"}).Language, "")
	if !strings.Contains(message, "funziona di nuovo") {
		t.Errorf("message is not in the chosen language: %q", message)
	}
}

// TestEveryEventHasAMessageAndALink is the per-event table: the five settings
// events plus the test one must each render a localized, short message.
func TestEveryEventHasAMessageAndALink(t *testing.T) {
	cases := []struct {
		event Event
		want  []string
	}{
		{Event{Kind: KindNewReleases, Count: 3, Source: "Site"}, []string{"3", "Site"}},
		{Event{Kind: KindIndexerState, Source: "Site", Previous: StateOK, Current: StateDegraded}, []string{"Site", "only in part", "healthy"}},
		{Event{Kind: KindIndexerState, Source: "Site", Previous: StateError, Current: StateOK}, []string{"Site", "works again", "error"}},
		{Event{Kind: KindGrabFailed, Title: "Dune 2021", App: "radarr", Error: "link quarantined"}, []string{"Dune 2021", "radarr", "quarantined"}},
		{Event{Kind: KindGrabSuccess, Title: "Dune 2021", App: "sonarr"}, []string{"Dune 2021", "sonarr", "grabbed"}},
		{Event{Kind: KindCatalogUpdated, Count: 2}, []string{"2", "Catalog"}},
		{Event{Kind: KindBackupFailed, Error: "disk full"}, []string{"backup", "disk full"}},
		{Event{Kind: KindTest}, []string{"test", "works"}},
	}
	for _, item := range cases {
		item.event.Path = "/releases"
		message := MessageFor(item.event, i18n.EN, "https://idx.example")
		for _, want := range item.want {
			if !strings.Contains(strings.ToLower(message), strings.ToLower(want)) {
				t.Errorf("%s message %q misses %q", item.event.Kind, message, want)
			}
		}
		if !strings.Contains(message, "https://idx.example/releases") {
			t.Errorf("%s message has no public link: %q", item.event.Kind, message)
		}
	}
	// Another language is a real translation, not a copy of the English line.
	if got := MessageFor(Event{Kind: KindBackupFailed}, i18n.IT, ""); got == MessageFor(Event{Kind: KindBackupFailed}, i18n.EN, "") {
		t.Errorf("Italian message is the English one: %q", got)
	}
}

func testDispatcher(t *testing.T) (*Dispatcher, *store.Store) {
	t.Helper()
	st, err := store.Open(t.TempDir() + "/notify.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return NewDispatcher(st, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))), st
}

func settingsFor(webhook string) store.Settings {
	settings := store.DefaultSettings()
	settings.NotifyWebhookURL = webhook
	settings.NotifyCooldownMinutes = 30
	settings.NotifyNewReleases = true
	settings.NotifyFailures = true
	settings.NotifyGrabFailed = true
	settings.NotifyGrabSuccess = true
	settings.NotifyCatalogUpdated = true
	settings.NotifyBackupFailed = true
	return settings
}

func TestDispatchRespectsEverySwitch(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	dispatcher, _ := testDispatcher(t)

	kinds := map[string]func(*store.Settings){
		KindNewReleases:    func(s *store.Settings) { s.NotifyNewReleases = false },
		KindIndexerState:   func(s *store.Settings) { s.NotifyFailures = false },
		KindGrabFailed:     func(s *store.Settings) { s.NotifyGrabFailed = false },
		KindGrabSuccess:    func(s *store.Settings) { s.NotifyGrabSuccess = false },
		KindCatalogUpdated: func(s *store.Settings) { s.NotifyCatalogUpdated = false },
		KindBackupFailed:   func(s *store.Settings) { s.NotifyBackupFailed = false },
	}
	for kind, disable := range kinds {
		settings := settingsFor(server.URL)
		disable(&settings)
		before := calls.Load()
		if dispatcher.Dispatch(context.Background(), settings, Event{Kind: kind, Source: "switch-" + kind}) {
			t.Errorf("%s was dispatched while switched off", kind)
		}
		if calls.Load() != before {
			t.Errorf("%s reached the webhook while switched off", kind)
		}
		enabled := settingsFor(server.URL)
		if !dispatcher.Dispatch(context.Background(), enabled, Event{Kind: kind, Source: "on-" + kind}) {
			t.Errorf("%s was not dispatched while switched on", kind)
		}
	}
}

func TestDispatchAntiSpamWindow(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	dispatcher, _ := testDispatcher(t)
	now := time.Date(2026, 10, 25, 10, 0, 0, 0, time.UTC)
	dispatcher.Now = func() time.Time { return now }
	settings := settingsFor(server.URL)

	event := Event{Kind: KindIndexerState, Source: "Site", Previous: StateOK, Current: StateError}
	if !dispatcher.Dispatch(context.Background(), settings, event) {
		t.Fatal("first event was not dispatched")
	}
	now = now.Add(10 * time.Minute)
	if dispatcher.Dispatch(context.Background(), settings, event) {
		t.Error("the same event within the window was dispatched again")
	}
	if calls.Load() != 1 {
		t.Errorf("webhook calls = %d, want 1", calls.Load())
	}
	// A different source is a different event.
	other := event
	other.Source = "Other"
	if !dispatcher.Dispatch(context.Background(), settings, other) {
		t.Error("another source was suppressed")
	}
	now = now.Add(21 * time.Minute)
	if !dispatcher.Dispatch(context.Background(), settings, event) {
		t.Error("event not dispatched after the window elapsed")
	}
	if calls.Load() != 3 {
		t.Errorf("webhook calls = %d, want 3", calls.Load())
	}
	// The window is configurable.
	settings.NotifyCooldownMinutes = 120
	now = now.Add(31 * time.Minute)
	if dispatcher.Dispatch(context.Background(), settings, event) {
		t.Error("the configured 120 minute window was not applied")
	}
}

func TestGrabSuccessAntiSpamUsesTheReleaseTitle(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	dispatcher, _ := testDispatcher(t)
	settings := settingsFor(server.URL)
	first := Event{Kind: KindGrabSuccess, Source: "Tracker", Title: "First"}
	if !dispatcher.Dispatch(context.Background(), settings, first) {
		t.Fatal("the first grab was not dispatched")
	}
	second := Event{Kind: KindGrabSuccess, Source: "Tracker", Title: "Second"}
	if !dispatcher.Dispatch(context.Background(), settings, second) {
		t.Error("a different release from the same indexer was suppressed")
	}
	if dispatcher.Dispatch(context.Background(), settings, first) {
		t.Error("the same release was dispatched twice inside the window")
	}
	if calls.Load() != 2 {
		t.Errorf("webhook calls = %d, want 2", calls.Load())
	}
}

func TestSendTestBypassesSwitchesAndAntiSpam(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var event Event
		_ = json.NewDecoder(r.Body).Decode(&event)
		if event.Kind != KindTest {
			t.Errorf("test delivery kind = %q", event.Kind)
		}
		calls.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	dispatcher, _ := testDispatcher(t)
	settings := settingsFor(server.URL)
	settings.NotifyNewReleases = false
	settings.NotifyFailures = false
	for i := 0; i < 2; i++ {
		if err := dispatcher.SendTest(context.Background(), settings); err != nil {
			t.Fatalf("SendTest: %v", err)
		}
	}
	if calls.Load() != 2 {
		t.Errorf("test deliveries = %d, want 2", calls.Load())
	}
	if err := dispatcher.SendTest(context.Background(), store.DefaultSettings()); err == nil {
		t.Error("SendTest without a channel returned no error")
	}
}

func TestDeliveryErrorDoesNotLeakTheSecret(t *testing.T) {
	const secret = "TOPSECRETWEBHOOKTOKEN"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "rejected", http.StatusInternalServerError)
	}))
	defer server.Close()
	var logs bytes.Buffer
	st, err := store.Open(t.TempDir() + "/notify.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	dispatcher := NewDispatcher(st, slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	settings := settingsFor(server.URL + "/hook/" + secret)
	if dispatcher.Dispatch(context.Background(), settings, Event{Kind: KindIndexerState, Source: "Site"}) {
		t.Fatal("a failing webhook reported a delivery")
	}
	if strings.Contains(logs.String(), secret) {
		t.Errorf("the log contains the webhook token: %s", logs.String())
	}
	if strings.Contains(dispatcherError(t, dispatcher, settings), secret) {
		t.Error("the delivery error contains the webhook token")
	}
}

func TestAppriseChannel(t *testing.T) {
	var path string
	var payload map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("apprise request = %s %s", r.Method, r.Header.Get("Content-Type"))
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	event := Event{Kind: KindGrabFailed, Title: "Dune", App: "radarr", Error: "HTTP 500"}
	cfg := Config{AppriseURL: server.URL, AppriseKey: "family", Language: i18n.EN, BaseURL: "https://idx.example"}
	if err := Send(context.Background(), cfg, event); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if path != "/notify/family" {
		t.Errorf("apprise path = %q", path)
	}
	if payload["type"] != "failure" || !strings.Contains(payload["title"], "Download failed") ||
		!strings.Contains(payload["body"], "Dune") {
		t.Errorf("apprise payload = %#v", payload)
	}
}

func TestDiscordChannel(t *testing.T) {
	var content string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Content  string `json:"content"`
			Username string `json:"username"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		content = payload.Content
		if payload.Username != "indexarr" {
			t.Errorf("discord username = %q", payload.Username)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	cfg := Config{DiscordURL: server.URL + "/hook/SECRET", Language: i18n.EN}
	event := Event{Kind: KindNewReleases, Count: 2, Titles: []string{"A", "B"}}
	if err := Send(context.Background(), cfg, event); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if !strings.Contains(content, "2 new releases") || !strings.Contains(content, "• A") {
		t.Errorf("discord content = %q", content)
	}
}

func TestSlackChannel(t *testing.T) {
	var text string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Text string `json:"text"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		text = payload.Text
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	cfg := Config{SlackURL: server.URL + "/hook/SECRET", Language: i18n.EN}
	if err := Send(context.Background(), cfg, Event{Kind: KindBackupFailed}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if !strings.Contains(strings.ToLower(text), "backup") {
		t.Errorf("slack text = %q", text)
	}
}

func TestGotifyChannel(t *testing.T) {
	const token = "GOTIFYSECRET"
	var gotToken, path, title, message string
	var priority int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotToken = r.Header.Get("X-Gotify-Key")
		path = r.URL.Path
		var payload struct {
			Title    string `json:"title"`
			Message  string `json:"message"`
			Priority int    `json:"priority"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		title, message, priority = payload.Title, payload.Message, payload.Priority
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	cfg := Config{GotifyURL: server.URL, GotifyToken: token, Language: i18n.EN}
	if err := Send(context.Background(), cfg, Event{Kind: KindBackupFailed, Error: "disk full"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if gotToken != token || path != "/message" {
		t.Errorf("gotify token/path = %q/%q", gotToken, path)
	}
	if !strings.Contains(title, "backup") || !strings.Contains(message, "disk full") || priority != 8 {
		t.Errorf("gotify payload = %q %q %d", title, message, priority)
	}
}

func TestNtfyChannel(t *testing.T) {
	const token = "NTFYSECRET"
	var path, auth, title, body string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		auth = r.Header.Get("Authorization")
		title = r.Header.Get("Title")
		raw, _ := io.ReadAll(r.Body)
		body = string(raw)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	cfg := Config{NtfyURL: server.URL, NtfyTopic: "indexarr", NtfyToken: token, Language: i18n.EN}
	event := Event{Kind: KindGrabSuccess, Title: "Dune", App: "sonarr"}
	if err := Send(context.Background(), cfg, event); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if path != "/indexarr" || auth != "Bearer "+token {
		t.Errorf("ntfy path/auth = %q/%q", path, auth)
	}
	if !strings.Contains(title, "Download started") || !strings.Contains(body, "Dune") {
		t.Errorf("ntfy title/body = %q/%q", title, body)
	}
}

func TestPushoverChannel(t *testing.T) {
	old := PushoverAPIBase
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("token") != "APP" || r.Form.Get("user") != "USER" {
			t.Errorf("pushover form = %v", r.Form)
		}
		if r.Form.Get("priority") != "1" || !strings.Contains(r.Form.Get("message"), "Dune") {
			t.Errorf("pushover message = %v", r.Form)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	PushoverAPIBase = server.URL
	defer func() { PushoverAPIBase = old }()

	cfg := Config{PushoverToken: "APP", PushoverUser: "USER", Language: i18n.EN}
	if err := Send(context.Background(), cfg, Event{Kind: KindGrabFailed, Title: "Dune"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
}

// fakeSMTP accepts one connection and returns the DATA body through a channel.
func fakeSMTP(t *testing.T) (string, <-chan string) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	messages := make(chan string, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer conn.Close()
		reader := bufio.NewReader(conn)
		writer := bufio.NewWriter(conn)
		write := func(line string) {
			_, _ = writer.WriteString(line)
			_ = writer.Flush()
		}
		write("220 localhost ESMTP\r\n")
		var data strings.Builder
		inData := false
		for {
			line, readErr := reader.ReadString('\n')
			if readErr != nil {
				return
			}
			if inData {
				if line == ".\r\n" {
					inData = false
					messages <- data.String()
					write("250 OK\r\n")
					continue
				}
				data.WriteString(line)
				continue
			}
			upper := strings.ToUpper(strings.TrimSpace(line))
			switch {
			case strings.HasPrefix(upper, "EHLO"):
				write("250-localhost\r\n250 HELP\r\n")
			case strings.HasPrefix(upper, "HELO"):
				write("250 localhost\r\n")
			case strings.HasPrefix(upper, "MAIL FROM"), strings.HasPrefix(upper, "RCPT TO"):
				write("250 OK\r\n")
			case upper == "DATA":
				write("354 End data with <CR><LF>.<CR><LF>\r\n")
				inData = true
			case upper == "QUIT":
				write("221 Bye\r\n")
				return
			default:
				write("250 OK\r\n")
			}
		}
	}()
	return listener.Addr().String(), messages
}

func TestEmailChannel(t *testing.T) {
	addr, messages := fakeSMTP(t)
	cfg := Config{
		SMTPHost: addr, SMTPFrom: "indexarr@example.com",
		SMTPTo:   "ops@example.com, second@example.com",
		Language: i18n.EN, Timeout: 3 * time.Second,
	}
	if err := Send(context.Background(), cfg, Event{Kind: KindTest}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	select {
	case body := <-messages:
		for _, want := range []string{"Subject:", "To: ops@example.com, second@example.com",
			"Content-Type: text/plain", "Test notification from indexarr"} {
			if !strings.Contains(body, want) {
				t.Errorf("email body misses %q:\n%s", want, body)
			}
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no email reached the fake SMTP server")
	}
}

func TestCustomScriptChannel(t *testing.T) {
	dir := t.TempDir()
	output := filepath.Join(dir, "event.txt")
	script := filepath.Join(dir, "notify.sh")
	body := `#!/bin/sh
printf '%s\n' "$INDEXARR_EVENTTYPE" "$INDEXARR_INSTANCE" "$INDEXARR_APPLICATIONURL" \
  "$INDEXARR_RELEASETITLE" "$INDEXARR_RELEASESOURCE" "$INDEXARR_RELEASESIZE" \
  "$INDEXARR_RELEASECATEGORIES" "$INDEXARR_HEALTHLEVEL" "$PROWLARR_EVENTTYPE" \
  "$prowlarr_eventtype" > "$1"
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		ScriptPath: script, ScriptArgs: `"` + output + `"`,
		Language: i18n.EN, BaseURL: "https://idx.example", Timeout: 3 * time.Second,
	}
	event := Event{
		Kind: KindGrabSuccess, Title: "Dune", Source: "Tracker", Size: 1234,
		Categories: []string{"2000", "2040"},
	}
	if err := Send(context.Background(), cfg, event); err != nil {
		t.Fatalf("Send: %v", err)
	}
	raw, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	want := []string{"GrabSuccess", "indexarr", "https://idx.example", "Dune", "Tracker", "1234", "2000,2040", "info", "GrabSuccess", "GrabSuccess"}
	if len(lines) != len(want) {
		t.Fatalf("script output = %q", lines)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Errorf("script line %d = %q, want %q", i, lines[i], want[i])
		}
	}
}

func TestCustomScriptHonoursTimeout(t *testing.T) {
	cfg := Config{ScriptPath: "sh", ScriptArgs: `-c "sleep 5"`, Timeout: 60 * time.Millisecond}
	started := time.Now()
	err := Send(context.Background(), cfg, Event{Kind: KindTest})
	if err == nil {
		t.Fatal("a script that ignores the timeout returned no error")
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Errorf("the script timeout took %v", elapsed)
	}
}

func TestHTTPChannelTimeoutAndSecretScrub(t *testing.T) {
	const secret = "TIMEOUTSECRET"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(150 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	cfg := Config{DiscordURL: server.URL + "/" + secret, Timeout: 30 * time.Millisecond}
	started := time.Now()
	err := Send(context.Background(), cfg, Event{Kind: KindTest})
	if err == nil {
		t.Fatal("a slow channel returned no error")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Errorf("the channel timeout took %v", elapsed)
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("the timeout error contains the webhook secret: %v", err)
	}
}

func TestChannelErrorScrubsTheToken(t *testing.T) {
	const token = "GOTIFYTOPSECRET"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "rejected token "+token, http.StatusUnauthorized)
	}))
	defer server.Close()
	cfg := Config{GotifyURL: server.URL, GotifyToken: token}
	err := Send(context.Background(), cfg, Event{Kind: KindTest})
	if err == nil {
		t.Fatal("a rejected Gotify call returned no error")
	}
	if strings.Contains(err.Error(), token) {
		t.Errorf("the error contains the Gotify token: %v", err)
	}
}

func TestTitleFor(t *testing.T) {
	if got := TitleFor(Event{Kind: KindGrabSuccess, Title: "Dune", App: "sonarr"}, i18n.EN); !strings.Contains(got, "Dune") ||
		!strings.Contains(got, "sonarr") {
		t.Errorf("grab success title = %q", got)
	}
	if got := TitleFor(Event{Kind: KindTest}, i18n.EN); got == "" || got == "notify.test" {
		t.Errorf("test title = %q", got)
	}
}

// dispatcherError invokes Send directly to inspect the error text.
func dispatcherError(t *testing.T, d *Dispatcher, settings store.Settings) string {
	t.Helper()
	err := Send(context.Background(), ConfigFrom(settings), Event{Kind: KindTest})
	if err == nil {
		t.Fatal("expected a delivery error")
	}
	return err.Error()
}

func TestAppContext(t *testing.T) {
	ctx := WithApp(context.Background(), "sonarr")
	if got := AppFrom(ctx); got != "sonarr" {
		t.Errorf("AppFrom = %q", got)
	}
	if got := AppFrom(context.Background()); got != "" {
		t.Errorf("AppFrom without a value = %q", got)
	}
}
