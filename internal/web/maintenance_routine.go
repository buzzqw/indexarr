package web

import (
	"net/http"
	"time"

	"github.com/buzzqw/indexarr/internal/store"
	"github.com/buzzqw/indexarr/internal/upkeep"
)

// routineView is the "Routine maintenance" panel of Settings > Maintenance.
type routineView struct {
	Available   bool
	Enabled     bool
	IdleMinutes int
	Running     bool
	Requested   bool
	LastActive  time.Time
	NextDue     time.Time
	WeeklyAt    time.Time
	HasLast     bool
	LastAt      time.Time
	LastStatus  string
	LastLabel   string
	LastTrigger string
	LastWeekly  bool
	LastTook    time.Duration
	DBBefore    int64
	DBAfter     int64
	Steps       []routineStepView
}

type routineStepView struct {
	Label       string
	Status      string
	StatusLabel string
	Text        string
	Took        time.Duration
}

func (s *Server) routineData(r *http.Request) routineView {
	if s.routine == nil {
		return routineView{}
	}
	status := s.routine.Status(r.Context())
	view := routineView{
		Available:   true,
		Enabled:     status.Enabled,
		IdleMinutes: status.IdleMinutes,
		Running:     status.Running,
		Requested:   status.Requested,
		LastActive:  status.LastActive,
		NextDue:     status.NextDue,
		WeeklyAt:    status.WeeklyAt,
	}
	last := status.Last
	if last == nil {
		return view
	}
	view.HasLast = true
	view.LastAt = last.StartedAt
	view.LastStatus = last.Status
	view.LastLabel = s.translate(r, routineStatusKey(last.Status))
	view.LastTrigger = s.translate(r, routineTriggerKey(last.Trigger))
	view.LastWeekly = last.Weekly
	view.LastTook = last.Duration().Round(100 * time.Millisecond)
	view.DBBefore = last.DBBefore
	view.DBAfter = last.DBAfter
	for _, step := range last.Steps {
		view.Steps = append(view.Steps, routineStepView{
			Label:       s.translate(r, routineStepKey(step.ID)),
			Status:      step.Status,
			StatusLabel: s.translate(r, routineStatusKey(step.Status)),
			Text:        s.routineStepText(r, step),
			Took:        (time.Duration(step.DurationMS) * time.Millisecond).Round(10 * time.Millisecond),
		})
	}
	return view
}

// routineStepText explains one step result in words.
func (s *Server) routineStepText(r *http.Request, step upkeep.StepResult) string {
	switch step.Status {
	case upkeep.StatusError:
		return s.translate(r, "maintenance.result.error", step.Detail)
	case upkeep.StatusPostponed:
		return s.translate(r, "maintenance.result.postponed")
	case upkeep.StatusSkipped:
		if step.ID == upkeep.StepVacuum && step.Detail == "not_needed" {
			return s.translate(r, "maintenance.result.vacuumNotNeeded")
		}
		return s.translate(r, "maintenance.result.skipped")
	}
	switch step.ID {
	case upkeep.StepIntegrity:
		if step.Detail == "integrity_check" {
			return s.translate(r, "maintenance.result.integrityFull")
		}
		return s.translate(r, "maintenance.result.integrityQuick")
	case upkeep.StepForeign:
		if step.Count > 0 {
			return s.translate(r, "maintenance.result.foreignBad", step.Count)
		}
		return s.translate(r, "maintenance.result.foreignOK")
	case upkeep.StepTitleIndex:
		if step.Detail == "rebuilt" {
			return s.translate(r, "maintenance.result.titleRebuilt")
		}
		return s.translate(r, "maintenance.result.titleOK")
	case upkeep.StepDedupe:
		return s.translate(r, "maintenance.result.dedupe", step.Count)
	case upkeep.StepNoLinks:
		return s.translate(r, "maintenance.result.noLinks", step.Count)
	case upkeep.StepOrphans:
		return s.translate(r, "maintenance.result.orphans", step.Count)
	case upkeep.StepFTS:
		return s.translate(r, "maintenance.result.fts")
	case upkeep.StepAnalyze:
		return s.translate(r, "maintenance.result.analyze")
	case upkeep.StepReindex:
		return s.translate(r, "maintenance.result.reindex")
	case upkeep.StepVacuum:
		if step.Detail == "no_space" {
			return s.translate(r, "maintenance.result.vacuumNoSpace")
		}
		return s.translate(r, "maintenance.result.vacuum", humanBytes(step.Count))
	case upkeep.StepCheckpoint:
		return s.translate(r, "maintenance.result.checkpoint")
	}
	return ""
}

// applyMaintenanceSettings reads the routine maintenance switch and its idle
// window.
func (s *Server) applyMaintenanceSettings(r *http.Request, in *store.Settings, current store.Settings) error {
	in.MaintenanceAuto = r.FormValue("maintenance_auto") != ""
	return s.readIntSettings(r, []intSetting{
		{"maintenance_idle_minutes", &in.MaintenanceIdleMinutes, current.MaintenanceIdleMinutes, 5, 240},
	})
}

func routineStatusKey(status string) string {
	switch status {
	case upkeep.StatusWarn:
		return "maintenance.routine.status.warn"
	case upkeep.StatusError:
		return "maintenance.routine.status.error"
	case upkeep.StatusSkipped:
		return "maintenance.routine.status.skipped"
	case upkeep.StatusPostponed:
		return "maintenance.routine.status.postponed"
	default:
		return "maintenance.routine.status.ok"
	}
}

func routineTriggerKey(trigger string) string {
	if trigger == upkeep.TriggerManual {
		return "maintenance.routine.trigger.manual"
	}
	return "maintenance.routine.trigger.idle"
}

func routineStepKey(id string) string {
	switch id {
	case upkeep.StepIntegrity:
		return "maintenance.step.integrity"
	case upkeep.StepForeign:
		return "maintenance.step.foreign_keys"
	case upkeep.StepTitleIndex:
		return "maintenance.step.title_index"
	case upkeep.StepDedupe:
		return "maintenance.step.dedupe"
	case upkeep.StepNoLinks:
		return "maintenance.step.no_links"
	case upkeep.StepOrphans:
		return "maintenance.step.orphans"
	case upkeep.StepFTS:
		return "maintenance.step.fts_optimize"
	case upkeep.StepAnalyze:
		return "maintenance.step.analyze"
	case upkeep.StepReindex:
		return "maintenance.step.reindex"
	case upkeep.StepVacuum:
		return "maintenance.step.vacuum"
	default:
		return "maintenance.step.checkpoint"
	}
}
