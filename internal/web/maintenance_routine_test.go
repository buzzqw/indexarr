package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/buzzqw/indexarr/internal/upkeep"
)

func TestRoutineMaintenancePanel(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	activity := upkeep.NewActivity()
	routine, err := upkeep.New(upkeep.Config{Store: env.store, Activity: activity})
	if err != nil {
		t.Fatal(err)
	}
	env.server.activity = activity
	env.server.routine = routine
	handler := env.server.Handler()

	page := get(t, handler, "/settings?section=maintenance&lang=en").Body.String()
	for _, want := range []string{
		`id="routine-maintenance"`, "Automatic routine maintenance", "Never run so far.",
		`name="maintenance_auto"`, `name="maintenance_idle_minutes"`, `value="routine"`,
		"Run now", "Maintenance never touches favorites",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("maintenance page missing %q", want)
		}
	}

	token, cookie := csrfFrom(t, handler, "/settings?section=maintenance")
	before := activity.LastAt()
	recorder := postForm(t, handler, "/maintenance", url.Values{
		"csrf": {token}, "action": {"routine"},
	}, cookie)
	if recorder.Code != http.StatusSeeOther || !routine.Status(ctx).Requested {
		t.Fatalf("run now = %d, requested %v", recorder.Code, routine.Status(ctx).Requested)
	}
	if !activity.LastAt().After(before) {
		t.Error("a change made in the UI must count as activity")
	}
	report := routine.Tick(ctx)
	if report == nil || report.Status != upkeep.StatusOK {
		t.Fatalf("manual pass = %+v", report)
	}
	page = get(t, handler, "/settings?section=maintenance&lang=en").Body.String()
	for _, want := range []string{"started by hand", "Integrity check", "Database intact (full check).",
		"Releases without links", "0 releases deleted", "Compaction (VACUUM)"} {
		if !strings.Contains(page, want) {
			t.Errorf("report missing %q", want)
		}
	}

	recorder = postForm(t, handler, "/settings", url.Values{
		"csrf": {token}, "section": {"maintenance"}, "maintenance_idle_minutes": {"30"},
	}, cookie)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("save = %d: %s", recorder.Code, recorder.Body.String())
	}
	settings, err := env.store.LoadSettings(ctx)
	if err != nil || settings.MaintenanceAuto || settings.MaintenanceIdleMinutes != 30 {
		t.Fatalf("saved = auto %v idle %d, %v", settings.MaintenanceAuto, settings.MaintenanceIdleMinutes, err)
	}
	recorder = postForm(t, handler, "/settings", url.Values{
		"csrf": {token}, "section": {"maintenance"}, "maintenance_auto": {"1"}, "maintenance_idle_minutes": {"2"},
	}, cookie)
	if recorder.Code == http.StatusSeeOther {
		t.Fatal("an idle window under 5 minutes must be refused")
	}
}
