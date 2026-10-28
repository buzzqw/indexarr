package store

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/buzzqw/indexarr/internal/vault"
)

func testVault(t *testing.T) *vault.Vault {
	t.Helper()
	v, err := vault.New(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// TestNotificationSecretsMigrateToTheVault covers the move required by the
// notifications block: the plaintext rows of an older installation disappear
// and the values are readable only with the master key.
func TestNotificationSecretsMigrateToTheVault(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	if err := st.SetSetting(ctx, KeyNotifyWebhook, "https://hooks.example/indexarr/TOPSECRET"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting(ctx, KeyNotifyTelegramToken, "123456:TELEGRAMTOPSECRET"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting(ctx, KeyNotifyTelegramChat, "-100200"); err != nil {
		t.Fatal(err)
	}

	st.AttachVault(testVault(t))
	moved, err := st.MigrateSettingsSecrets(ctx)
	if err != nil {
		t.Fatalf("MigrateSettingsSecrets: %v", err)
	}
	if moved != 2 {
		t.Fatalf("moved = %d, want 2", moved)
	}
	// A second run is a no-op.
	if moved, err := st.MigrateSettingsSecrets(ctx); err != nil || moved != 0 {
		t.Fatalf("second migration = %d, %v", moved, err)
	}

	for _, key := range []string{KeyNotifyWebhook, KeyNotifyTelegramToken} {
		var value string
		err := st.DB().QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&value)
		if err == nil {
			t.Errorf("plaintext setting %s still exists: %q", key, value)
		}
	}
	settings, err := st.LoadSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if settings.NotifyWebhookURL != "https://hooks.example/indexarr/TOPSECRET" {
		t.Errorf("webhook after migration = %q", settings.NotifyWebhookURL)
	}
	if settings.NotifyTelegramToken != "123456:TELEGRAMTOPSECRET" {
		t.Errorf("token after migration = %q", settings.NotifyTelegramToken)
	}
	if settings.NotifyTelegramChatID != "-100200" {
		t.Errorf("chat id after migration = %q", settings.NotifyTelegramChatID)
	}

	// The sealed row does not contain the plaintext.
	var ciphertext []byte
	if err := st.DB().QueryRowContext(ctx,
		`SELECT ciphertext FROM app_secrets WHERE key = ?`, AppSecretNotifyTelegramToken).
		Scan(&ciphertext); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ciphertext, []byte("TELEGRAMTOPSECRET")) {
		t.Error("the sealed token still contains its plaintext")
	}
}

// TestSaveSettingsSealsTheNotificationChannels checks the round trip through
// SaveSettings, including the explicit removal.
func TestSaveSettingsSealsTheNotificationChannels(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	st.AttachVault(testVault(t))
	settings, err := st.LoadSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	settings.NotifyWebhookURL = "https://hooks.example/indexarr/hook-token"
	settings.NotifyTelegramToken = "999:SECRETTOKEN"
	settings.NotifyTelegramChatID = "42"
	settings.NotifyGrabFailed = false
	settings.NotifyCatalogUpdated = false
	settings.NotifyBackupFailed = false
	settings.NotifyCooldownMinutes = 90
	settings.UILanguage = "it"
	if err := st.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	loaded, err := st.LoadSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.NotifyWebhookURL != settings.NotifyWebhookURL ||
		loaded.NotifyTelegramToken != settings.NotifyTelegramToken {
		t.Errorf("secrets round trip = %#v", loaded)
	}
	if loaded.NotifyGrabFailed || loaded.NotifyCatalogUpdated || loaded.NotifyBackupFailed {
		t.Errorf("event switches = %#v", loaded)
	}
	if loaded.NotifyCooldown() != 90*time.Minute {
		t.Errorf("cooldown = %v", loaded.NotifyCooldown())
	}
	if loaded.UILanguage != "it" {
		t.Errorf("UI language = %q", loaded.UILanguage)
	}
	// The message language is a separate choice: saved as it is, and an
	// unknown code is dropped so the messages follow the interface again.
	settings.NotifyLanguage = "it"
	if err := st.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	if loaded, err = st.LoadSettings(ctx); err != nil {
		t.Fatal(err)
	}
	if loaded.NotifyLanguage != "it" {
		t.Errorf("notification language = %q", loaded.NotifyLanguage)
	}
	settings.NotifyLanguage = "klingon"
	if err := st.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	if loaded, err = st.LoadSettings(ctx); err != nil {
		t.Fatal(err)
	}
	if loaded.NotifyLanguage != "" {
		t.Errorf("unknown notification language kept as %q", loaded.NotifyLanguage)
	}
	var plaintext int
	if err := st.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM settings WHERE key IN (?, ?)`,
		KeyNotifyWebhook, KeyNotifyTelegramToken).Scan(&plaintext); err != nil {
		t.Fatal(err)
	}
	if plaintext != 0 {
		t.Errorf("plaintext notification settings = %d", plaintext)
	}

	loaded.NotifyWebhookURL = ""
	loaded.NotifyTelegramToken = ""
	if err := st.SaveSettings(ctx, loaded); err != nil {
		t.Fatal(err)
	}
	loaded, _ = st.LoadSettings(ctx)
	if loaded.NotifyWebhookURL != "" || loaded.NotifyTelegramToken != "" {
		t.Errorf("removed secrets still load: %#v", loaded)
	}
}

func TestNotificationLedgerAndIndexerState(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	at := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	if err := st.RecordNotification(ctx, "indexer_state", "Site", at); err != nil {
		t.Fatal(err)
	}
	got, err := st.NotificationSentAt(ctx, "indexer_state", "Site")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(at) {
		t.Errorf("notification timestamp = %v, want %v", got, at)
	}
	if missing, err := st.NotificationSentAt(ctx, "indexer_state", "Other"); err != nil || !missing.IsZero() {
		t.Errorf("unknown event = %v, %v", missing, err)
	}

	mustCreateDefinition(t, st, "notify-state")
	id := mustCreateIndexer(t, st, "notify-state", "notify-state", "Notify State")
	if state, err := st.IndexerNotifyState(ctx, id); err != nil || state != "" {
		t.Errorf("initial state = %q, %v", state, err)
	}
	if err := st.SetIndexerNotifyState(ctx, id, "error"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetIndexerNotifyState(ctx, id, "ok"); err != nil {
		t.Fatal(err)
	}
	if state, _ := st.IndexerNotifyState(ctx, id); state != "ok" {
		t.Errorf("updated state = %q", state)
	}
}
