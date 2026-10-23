package web

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/buzzqw/indexarr/internal/health"
	"github.com/buzzqw/indexarr/internal/store"
)

// indexerHealthView is the one place the interface reads an indexer's state.
// Home, the sidebar badge, the list and the detail page all come through here,
// so they cannot disagree about whether a site is healthy.
type indexerHealthView struct {
	Indexer    store.Indexer
	State      store.IndexerState
	HasState   bool
	History    []store.IndexerHistory
	LastRun    *store.IndexerHistory
	Assessment health.Assessment
}

func (s *Server) loadIndexerHealth(ctx context.Context, in store.Indexer) indexerHealthView {
	view := indexerHealthView{Indexer: in}
	if state, err := s.store.GetIndexerState(ctx, in.ID); err == nil {
		view.State = state
		view.HasState = true
	}
	if history, err := s.store.ListIndexerHistory(ctx, in.ID, 50); err == nil {
		view.History = history
		for i := range history {
			if strings.EqualFold(history[i].Kind, "run") {
				view.LastRun = &history[i]
				break
			}
		}
	}
	view.Assessment = health.Assess(health.Input{
		Enabled:   in.Enabled,
		Circuit:   view.State.Circuit,
		LastError: view.State.LastError,
		History:   view.History,
	}, health.DefaultThresholds())
	return view
}

// dotState maps the model to the status-dot scale of UI-GUIDE §5. A degraded
// site is still a problem the operator has to see, but it keeps its own label
// and colour.
func dotState(view indexerHealthView) string {
	switch view.Assessment.State {
	case health.StateDisabled:
		return "disabled"
	case health.StateProblem:
		return "problem"
	case health.StateDegraded:
		return "degraded"
	case health.StateTesting:
		return "testing"
	default:
		return "ok"
	}
}

func (s *Server) statusDotFor(r *http.Request, view indexerHealthView) statusDot {
	state := dotState(view)
	label := map[string]string{
		"ok":       "idx.statusOk",
		"problem":  "idx.statusProblem",
		"degraded": "idx.statusDegraded",
		"testing":  "idx.statusTesting",
		"disabled": "idx.statusDisabled",
	}[state]
	return statusDot{State: state, Label: s.translate(r, label)}
}

// healthProblems turns model reasons into cause + remedy rows. Every reason
// carries the action that can fix it: open the site, edit the query or reduce
// the request rate.
func (s *Server) healthProblems(r *http.Request, view indexerHealthView) []uiProblem {
	detail := "/indexers/" + strconv.FormatInt(view.Indexer.ID, 10)
	edit := detail + "/edit"
	out := make([]uiProblem, 0, len(view.Assessment.Reasons))
	seen := map[string]bool{}
	for _, reason := range view.Assessment.Reasons {
		if seen[reason.Code] {
			continue
		}
		seen[reason.Code] = true
		switch reason.Code {
		case health.ReasonCircuit:
			out = append(out, uiProblem{
				Text:        s.translate(r, "idx.problem.circuit"),
				ActionLabel: s.translate(r, "idx.problem.genericAction"),
				ActionURL:   detail,
			})
		case health.ReasonLastError:
			raw := strings.TrimSpace(view.State.LastError)
			if raw == "" && view.LastRun != nil {
				raw = strings.TrimSpace(view.LastRun.Error)
			}
			out = append(out, s.problemForMessage(r, view.Indexer, raw))
		case health.ReasonErrors:
			if strings.TrimSpace(view.Assessment.LastError) != "" {
				out = append(out, s.problemForMessage(r, view.Indexer, view.Assessment.LastError))
				break
			}
			out = append(out, uiProblem{
				Text:        s.translate(r, "idx.problem.errors", reason.Count),
				ActionLabel: s.translate(r, "idx.problem.genericAction"),
				ActionURL:   detail,
			})
		case health.ReasonZeroResults:
			out = append(out, uiProblem{
				Text:        s.translate(r, "idx.problem.zero", reason.Count),
				ActionLabel: s.translate(r, "idx.problem.zeroAction"),
				ActionURL:   edit,
			})
		case health.ReasonCompleteness:
			out = append(out, uiProblem{
				Text:        s.missingFieldPhrase(r, reason.Field),
				ActionLabel: s.translate(r, "common.edit"),
				ActionURL:   edit,
			})
		}
	}
	return out
}

func (s *Server) healthProblem(r *http.Request, view indexerHealthView) *uiProblem {
	problems := s.healthProblems(r, view)
	if len(problems) == 0 {
		return nil
	}
	return &problems[0]
}

// problemForMessage classifies a raw connector error. It stays separate from
// the health model so the model never has to know UI text or error strings.
func (s *Server) problemForMessage(r *http.Request, in store.Indexer, message string) uiProblem {
	raw := strings.ToLower(strings.TrimSpace(message))
	detail := "/indexers/" + strconv.FormatInt(in.ID, 10)
	switch {
	case strings.Contains(raw, "429") || strings.Contains(raw, "rate limit") || strings.Contains(raw, "too many"):
		return uiProblem{
			Text:        s.translate(r, "idx.problem.rate"),
			ActionLabel: s.translate(r, "idx.problem.rateAction"),
			ActionURL:   "/settings",
		}
	case strings.Contains(raw, "403") || strings.Contains(raw, "forbidden") || strings.Contains(raw, "cloudflare"):
		return uiProblem{
			Text:        s.translate(r, "idx.problem.forbidden"),
			ActionLabel: s.translate(r, "idx.problem.forbiddenAction"),
			ActionURL:   "/settings",
		}
	case strings.Contains(raw, "401") || strings.Contains(raw, "unauthorized") ||
		strings.Contains(raw, "login") || strings.Contains(raw, "credential") || strings.Contains(raw, "password"):
		return uiProblem{
			Text:        s.translate(r, "idx.problem.auth"),
			ActionLabel: s.translate(r, "idx.problem.authAction"),
			ActionURL:   detail + "/edit",
		}
	case strings.Contains(raw, "timeout") || strings.Contains(raw, "deadline") ||
		strings.Contains(raw, "no such host") || strings.Contains(raw, "connection refused") ||
		strings.Contains(raw, "unreachable") || strings.Contains(raw, "dial"):
		return uiProblem{
			Text:        s.translate(r, "idx.problem.unreachable"),
			ActionLabel: s.translate(r, "idx.problem.unreachableAction"),
			ActionURL:   detail,
		}
	default:
		return uiProblem{
			Text:        s.translate(r, "idx.problem.generic"),
			ActionLabel: s.translate(r, "idx.problem.genericAction"),
			ActionURL:   detail,
		}
	}
}
