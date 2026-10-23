package store

import (
	"context"
	"testing"
	"time"
)

func TestNetworkSettingsDefaults(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	settings, err := st.LoadSettings(ctx)
	if err != nil {
		t.Fatalf("LoadSettings: %v", err)
	}
	if settings.HostRate != 1 {
		t.Errorf("HostRate = %v, want 1", settings.HostRate)
	}
	if settings.HostBurst != 4 {
		t.Errorf("HostBurst = %d, want 4", settings.HostBurst)
	}
	if settings.BreakerThreshold != 5 {
		t.Errorf("BreakerThreshold = %d, want 5", settings.BreakerThreshold)
	}
	if settings.BreakerCooldownSeconds != 600 {
		t.Errorf("BreakerCooldownSeconds = %d, want 600", settings.BreakerCooldownSeconds)
	}
	if got := settings.BreakerCooldown(); got != 10*time.Minute {
		t.Errorf("BreakerCooldown() = %s, want 10m", got)
	}
}

func TestNetworkSettingsRoundTrip(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	settings, err := st.LoadSettings(ctx)
	if err != nil {
		t.Fatalf("LoadSettings: %v", err)
	}
	settings.HostRate = 2.5
	settings.HostBurst = 8
	settings.BreakerThreshold = 7
	settings.BreakerCooldownSeconds = 120
	if err := st.SaveSettings(ctx, settings); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}

	reloaded, err := st.LoadSettings(ctx)
	if err != nil {
		t.Fatalf("LoadSettings: %v", err)
	}
	if reloaded.HostRate != 2.5 || reloaded.HostBurst != 8 ||
		reloaded.BreakerThreshold != 7 || reloaded.BreakerCooldownSeconds != 120 {
		t.Errorf("network settings = %#v", reloaded)
	}
	if got := reloaded.BreakerCooldown(); got != 2*time.Minute {
		t.Errorf("BreakerCooldown() = %s, want 2m", got)
	}
}

func TestNetworkSettingsRejectOutOfRange(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	for key, value := range map[string]string{
		KeyHostRate:         "not-a-number",
		KeyHostBurst:        "0",
		KeyBreakerThreshold: "1000",
		KeyBreakerCooldown:  "-5",
	} {
		if err := st.SetSetting(ctx, key, value); err != nil {
			t.Fatalf("SetSetting(%s): %v", key, err)
		}
	}

	settings, err := st.LoadSettings(ctx)
	if err != nil {
		t.Fatalf("LoadSettings: %v", err)
	}
	if settings.HostRate != 1 || settings.HostBurst != 4 ||
		settings.BreakerThreshold != 5 || settings.BreakerCooldownSeconds != 600 {
		t.Errorf("invalid values did not fall back to defaults: %#v", settings)
	}
}
