package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
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
	settings := store.Settings{UILanguage: "zh", NotifyLanguage: "it"}
	if got := ConfigFrom(settings).Language; got != i18n.IT {
		t.Errorf("chosen language = %q, want it", got)
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
