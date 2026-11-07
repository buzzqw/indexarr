package upkeep

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/buzzqw/indexarr/internal/store"
)

func newRoutine(t *testing.T) (*Routine, *store.Store, *Activity) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "upkeep.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	activity := NewActivity()
	routine, err := New(Config{Store: st, Activity: activity})
	if err != nil {
		t.Fatal(err)
	}
	return routine, st, activity
}

// idleSince moves the clock of the routine forward, as if nobody had been
// served for the given time.
func idleSince(r *Routine, activity *Activity, quiet time.Duration) {
	at := activity.LastAt().Add(quiet)
	r.now = func() time.Time { return at }
}

func stepStatus(report *Report) map[string]string {
	out := map[string]string{}
	for _, step := range report.Steps {
		out[step.ID] = step.Status
	}
	return out
}

func TestRoutineWaitsForTheIdleWindow(t *testing.T) {
	ctx := context.Background()
	routine, _, activity := newRoutine(t)

	idleSince(routine, activity, 14*time.Minute)
	if report := routine.Tick(ctx); report != nil {
		t.Fatalf("ran after 14 idle minutes: %+v", report)
	}
	idleSince(routine, activity, 16*time.Minute)
	report := routine.Tick(ctx)
	if report == nil {
		t.Fatal("did not run after 16 idle minutes")
	}
	if report.Trigger != TriggerIdle || report.Status != StatusOK || !report.Weekly {
		t.Fatalf("first pass = trigger %s status %s weekly %v; want idle, ok, weekly",
			report.Trigger, report.Status, report.Weekly)
	}
	steps := stepStatus(report)
	for _, id := range []string{StepIntegrity, StepForeign, StepTitleIndex, StepDedupe,
		StepNoLinks, StepOrphans, StepFTS, StepAnalyze, StepReindex, StepVacuum, StepCheckpoint} {
		if steps[id] != StatusOK {
			t.Errorf("step %s = %q, want ok (%+v)", id, steps[id], report.Steps)
		}
	}

	// Done for today: another idle tick does nothing.
	if again := routine.Tick(ctx); again != nil {
		t.Fatalf("ran twice in one day: %+v", again)
	}

	// The next day is a daily pass: quick check, no reindex, VACUUM only
	// when there is space to reclaim.
	next := time.Now().Add(25 * time.Hour)
	routine.now = func() time.Time { return next }
	daily := routine.Tick(ctx)
	if daily == nil || daily.Weekly {
		t.Fatalf("second day = %+v; want a daily pass", daily)
	}
	steps = stepStatus(daily)
	if _, ok := steps[StepReindex]; ok {
		t.Error("a daily pass must not rebuild the indexes")
	}
	if steps[StepVacuum] != StatusSkipped {
		t.Errorf("daily VACUUM on a compact file = %q, want skipped", steps[StepVacuum])
	}
	if daily.Steps[0].Detail != "quick_check" {
		t.Errorf("daily integrity = %q, want quick_check", daily.Steps[0].Detail)
	}
}

func TestRoutineSurvivesARestart(t *testing.T) {
	ctx := context.Background()
	routine, st, activity := newRoutine(t)
	idleSince(routine, activity, time.Hour)
	if routine.Tick(ctx) == nil {
		t.Fatal("did not run")
	}
	restarted, err := New(Config{Store: st, Activity: activity})
	if err != nil {
		t.Fatal(err)
	}
	idleSince(restarted, activity, time.Hour)
	if report := restarted.Tick(ctx); report != nil {
		t.Fatal("a restart must not repeat today's pass")
	}
	status := restarted.Status(ctx)
	if status.Last == nil || status.Last.Status != StatusOK || status.DailyAt.IsZero() || status.NextDue.IsZero() {
		t.Fatalf("status after restart = %+v", status)
	}
}

func TestRoutinePostponesWhenAnAppComesBack(t *testing.T) {
	ctx := context.Background()
	routine, _, activity := newRoutine(t)
	// A request lands while the pass is starting: its time is after the
	// real start of the pass, so no step runs and the day stays due.
	activity.last.Store(time.Now().Add(time.Hour).UnixNano())
	idleSince(routine, activity, 20*time.Minute)
	report := routine.Tick(ctx)
	if report == nil || report.Status != StatusPostponed {
		t.Fatalf("report = %+v; want postponed", report)
	}
	for _, step := range report.Steps {
		if step.Status != StatusPostponed {
			t.Fatalf("step %s = %s, want postponed", step.ID, step.Status)
		}
	}
	state, _ := routine.loadState(ctx)
	if !state.DailyAt.IsZero() {
		t.Fatal("a postponed pass must leave the day due")
	}
}

func TestRoutineManualRunIgnoresActivityAndSchedule(t *testing.T) {
	ctx := context.Background()
	routine, _, activity := newRoutine(t)
	activity.Touch()
	if routine.Tick(ctx) != nil {
		t.Fatal("ran while busy without a request")
	}
	routine.Request()
	if !routine.Status(ctx).Requested {
		t.Fatal("request not visible in the status")
	}
	report := routine.Tick(ctx)
	if report == nil || report.Trigger != TriggerManual || report.Status != StatusOK {
		t.Fatalf("manual pass = %+v", report)
	}
	if routine.Status(ctx).Requested {
		t.Fatal("request still pending after the pass")
	}
}

func TestRoutineDisabledAndBusy(t *testing.T) {
	ctx := context.Background()
	routine, st, activity := newRoutine(t)
	settings, err := st.LoadSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	settings.MaintenanceAuto = false
	if err := st.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	idleSince(routine, activity, time.Hour)
	if routine.Tick(ctx) != nil {
		t.Fatal("ran while disabled")
	}
	busy := true
	routine.busy = func() bool { return busy }
	routine.Request()
	if routine.Tick(ctx) != nil {
		t.Fatal("ran during a backup")
	}
	busy = false
	if report := routine.Tick(ctx); report == nil || report.Trigger != TriggerManual {
		t.Fatalf("a manual request must still run when disabled: %+v", report)
	}
}

func TestRoutineOverdueNeedsOnlyAShortPause(t *testing.T) {
	ctx := context.Background()
	routine, _, activity := newRoutine(t)
	routine.createdAt = time.Now().Add(-49 * time.Hour)
	idleSince(routine, activity, 3*time.Minute)
	if routine.Tick(ctx) == nil {
		t.Fatal("an overdue pass must start after a short pause")
	}
}

func TestActivityCountsClientsAndChangesOnly(t *testing.T) {
	activity := NewActivity()
	activity.last.Store(0)
	ok := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})

	activity.WrapChanges(ok).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	if activity.last.Load() != 0 {
		t.Fatal("reading a UI page counted as activity")
	}
	activity.WrapChanges(ok).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/settings", nil))
	if activity.last.Load() == 0 {
		t.Fatal("a UI change did not count as activity")
	}
	activity.last.Store(0)
	activity.Wrap(ok).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api?t=caps", nil))
	if activity.last.Load() == 0 {
		t.Fatal("a Torznab request did not count as activity")
	}
	var nilActivity *Activity
	nilActivity.Touch()
	if nilActivity.Wrap(ok) == nil || nilActivity.IdleFor(time.Now()) != 0 {
		t.Fatal("a nil Activity must be inert")
	}
}
