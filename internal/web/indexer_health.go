package web

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/buzzqw/indexarr/internal/definition"
	"github.com/buzzqw/indexarr/internal/feed"
	"github.com/buzzqw/indexarr/internal/health"
	"github.com/buzzqw/indexarr/internal/i18n"
	"github.com/buzzqw/indexarr/internal/logbuffer"
	"github.com/buzzqw/indexarr/internal/store"
)

// healthVerdict is the plain answer to "does this source work?" that the
// Overview tab shows above the details: a label, a colour and one sentence.
type healthVerdict struct {
	Label string
	Class string
	Text  string
}

// indexerLogRow is one line of the per-source log. It comes from the same
// indexer_history table as Activity, so the Overview tab and the full run log
// can never disagree.
type indexerLogRow struct {
	ID      int64
	When    time.Time
	Kind    string
	Status  string
	Message string
	Latency string
	Href    string
}

// indexerHealthView is the one place the interface reads an indexer's state.
// Home, the sidebar badge, the list and the detail page all come through here,
// so they cannot disagree about whether a site is healthy.
type indexerHealthView struct {
	Indexer    store.Indexer
	State      store.IndexerState
	HasState   bool
	LastRun    *store.IndexerHistory
	Assessment health.Assessment
	// FeedOnly means the definition has no remote query endpoint. Its search
	// zeroes are normal and must not drive the health badge.
	FeedOnly bool
	// Latest reports whether the definition publishes periodic latest runs.
	Latest bool
}

// healthBatch carries the state and the health-relevant history of every
// indexer in two reads. The indexers page used to run two queries per source
// (state and history), which at 46 sources cost about 25 ms of SQLite work per
// render. The batch keeps the per-indexer shape of the model while removing
// the round trips.
type healthBatch struct {
	states  map[int64]store.IndexerState
	history map[int64][]store.IndexerHistory
}

// loadHealthBatch reads the state and history of the given indexers. It
// returns nil when either read fails, so the caller falls back to the
// per-indexer queries and the page keeps working.
func (s *Server) loadHealthBatch(ctx context.Context, indexers []store.Indexer) *healthBatch {
	ids := make([]int64, 0, len(indexers))
	for _, in := range indexers {
		if in.ID > 0 {
			ids = append(ids, in.ID)
		}
	}
	states, err := s.store.IndexerStates(ctx)
	if err != nil {
		return nil
	}
	history, err := s.store.ListIndexerHealthHistory(ctx, ids, 50)
	if err != nil {
		return nil
	}
	return &healthBatch{states: states, history: history}
}

// loadIndexerHealth builds the health view of one source. def is the parsed
// definition when the caller already has it; batch is the value shared by a
// list page (nil means "read this indexer on its own").
func (s *Server) loadIndexerHealth(ctx context.Context, in store.Indexer, def *definition.Definition, batch *healthBatch) indexerHealthView {
	view := indexerHealthView{Indexer: in}
	var history []store.IndexerHistory
	if batch != nil {
		if state, ok := batch.states[in.ID]; ok {
			view.State = state
			view.HasState = true
		}
		history = batch.history[in.ID]
	} else {
		if state, err := s.store.GetIndexerState(ctx, in.ID); err == nil {
			view.State = state
			view.HasState = true
		}
		if rows, err := s.store.ListIndexerHealthHistory(ctx, []int64{in.ID}, 50); err == nil {
			history = rows[in.ID]
		} else if rows, err := s.store.ListIndexerHistory(ctx, in.ID, 50); err == nil {
			// The slim projection is an optimisation, not a new requirement:
			// keep the full rows if it ever fails.
			history = rows
		}
	}
	for i := range history {
		if strings.EqualFold(history[i].Kind, "run") {
			view.LastRun = &history[i]
			break
		}
	}
	view.Latest = true
	if def == nil {
		def, _ = s.parsedDefinition(ctx, in.DefinitionID)
	}
	if def != nil {
		view.FeedOnly = strings.EqualFold(strings.TrimSpace(def.Connector), "rss")
		view.Latest = def.Meta.HasLatest()
	}
	if value, ok := indexerConfigLatest(in.ConfigJSON); ok {
		view.Latest = value
	}
	view.Assessment = health.Assess(health.Input{
		Enabled:   in.Enabled,
		Circuit:   view.State.Circuit,
		LastError: view.State.LastError,
		History:   history,
		FeedOnly:  view.FeedOnly,
	}, health.DefaultThresholds())
	return view
}

func indexerConfigLatest(configJSON string) (bool, bool) {
	if strings.TrimSpace(configJSON) == "" {
		return false, false
	}
	var values map[string]string
	if err := json.Unmarshal([]byte(configJSON), &values); err != nil {
		return false, false
	}
	raw, ok := values["latest"]
	if !ok {
		return false, false
	}
	value, err := strconv.ParseBool(strings.TrimSpace(raw))
	if err != nil {
		return false, false
	}
	return value, true
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
	if (view.LastRun != nil && view.LastRun.Status == "blocked") ||
		(view.HasState && isChallengeText(view.State.LastError)) {
		return statusDot{State: "blocked", Label: s.translate(r, "idx.statusBlocked")}
	}
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
	if (view.LastRun != nil && view.LastRun.Status == "blocked") ||
		(view.HasState && isChallengeText(view.State.LastError)) {
		if problem := s.problemFor(r, view.Indexer, view.State, view.LastRun); problem != nil {
			return []uiProblem{*problem}
		}
	}
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

// verdict answers "does it work well or badly?" in one sentence. The detail
// comes from the same problems the header shows, so the two never diverge.
func (s *Server) verdict(r *http.Request, view indexerHealthView, status statusDot, problems []uiProblem) healthVerdict {
	detail := ""
	if len(problems) > 0 {
		detail = strings.TrimSpace(problems[0].Text)
	}
	if detail == "" {
		detail = s.translate(r, "idx.problem.generic")
	}
	switch status.State {
	case "blocked":
		return healthVerdict{Label: status.Label, Class: "danger", Text: s.translate(r, "idx.health.blocked", detail)}
	case "problem":
		return healthVerdict{Label: status.Label, Class: "danger", Text: s.translate(r, "idx.health.problem", detail)}
	case "degraded":
		return healthVerdict{Label: status.Label, Class: "warn", Text: s.translate(r, "idx.health.degraded", detail)}
	case "disabled":
		return healthVerdict{Label: status.Label, Text: s.translate(r, "idx.health.disabled")}
	case "testing":
		return healthVerdict{Label: status.Label, Text: s.translate(r, "idx.health.testing")}
	default:
		text := s.translate(r, "idx.health.ok")
		if view.LastRun != nil {
			lang := requestLanguage(r)
			text = s.translate(r, "idx.health.okRun",
				kindLabelLang(lang, view.LastRun.Kind), humanAgoLang(lang, view.LastRun.StartedAt))
		}
		return healthVerdict{Label: status.Label, Class: "ok", Text: text}
	}
}

func kindLabelLang(lang i18n.Language, kind string) string {
	key := "idx.data.kind." + strings.ToLower(strings.TrimSpace(kind))
	if text := i18n.T(lang, key); text != key {
		return text
	}
	return kind
}

// indexerSiteAddress normalizes the configured address into a clickable
// http(s) URL. Credentials, the query string and secret-like path segments are
// removed, because the address is visible on every page of the detail view.
func indexerSiteAddress(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return ""
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
	default:
		return ""
	}
	return feed.RedactURL(parsed.String())
}

// indexerLogRows keeps the newest operations of one source. Only "run" rows
// have a detail page, so only those get a link.
func indexerLogRows(history []store.IndexerHistory, limit int) []indexerLogRow {
	if limit <= 0 {
		limit = 8
	}
	out := make([]indexerLogRow, 0, limit)
	for i := range history {
		if len(out) >= limit {
			break
		}
		item := history[i]
		row := indexerLogRow{
			ID:      item.ID,
			When:    item.StartedAt,
			Kind:    item.Kind,
			Status:  item.Status,
			Message: strings.TrimSpace(item.Error),
			Latency: formatLatency(item.LatencyMS),
		}
		if row.Message == "" {
			row.Message = strings.TrimSpace(item.Query)
		}
		if strings.EqualFold(strings.TrimSpace(item.Kind), "run") {
			row.Href = "/runs/" + strconv.FormatInt(item.ID, 10)
		}
		out = append(out, row)
	}
	return out
}

// indexerAppLog returns the newest process log lines that belong to one
// source. Matching uses the attributes the engine writes (indexer, source,
// indexer_id) and falls back to the slug in the message.
func (s *Server) indexerAppLog(in store.Indexer, limit int) []logbuffer.Entry {
	if s.logs == nil {
		return nil
	}
	if limit <= 0 {
		limit = 10
	}
	entries := s.logs.SnapshotFilter(logbuffer.Filter{MinLevel: slog.LevelDebug})
	out := make([]logbuffer.Entry, 0, limit)
	for _, entry := range entries {
		if !indexerLogEntryMatches(entry, in) {
			continue
		}
		out = append(out, entry)
		if len(out) >= limit {
			break
		}
	}
	return out
}

func indexerLogEntryMatches(entry logbuffer.Entry, in store.Indexer) bool {
	slug := strings.ToLower(strings.TrimSpace(in.Slug))
	name := strings.ToLower(strings.TrimSpace(in.Name))
	id := strconv.FormatInt(in.ID, 10)
	for _, attr := range entry.Attrs {
		key := strings.ToLower(strings.TrimSpace(attr.Key))
		value := strings.ToLower(strings.TrimSpace(attr.Value))
		switch key {
		case "indexer", "source", "indexer_slug":
			if (slug != "" && value == slug) || (name != "" && value == name) {
				return true
			}
		case "indexer_id", "source_id":
			if value == id {
				return true
			}
		}
	}
	return slug != "" && strings.Contains(strings.ToLower(entry.Message), slug)
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
			ActionPost:  "/indexers/" + strconv.FormatInt(in.ID, 10) + "/frequency",
			ActionTitle: s.translate(r, "idx.problem.rateTip"),
			CSRF:        csrfToken(r.Context()),
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
	case strings.Contains(raw, "session endpoint") || strings.Contains(raw, "flaresolverr"):
		return uiProblem{
			Text:        s.translate(r, "idx.problem.sessionEndpoint"),
			ActionLabel: s.translate(r, "idx.problem.sessionEndpointAction"),
			ActionURL:   "/settings?section=network",
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
