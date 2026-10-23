package web

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/buzzqw/indexarr/internal/backup"
	"github.com/buzzqw/indexarr/internal/store"
)

// HomeProblem is one actionable problem shown on the home page. Code and
// ActionKey are catalog keys, so the same status serves every language.
type HomeProblem struct {
	Name      string
	Code      string
	ActionKey string
	ActionURL string
	Kind      string
}

// HomeStatus is the single source of truth for "does indexarr work?". The home
// page and the sidebar badge both read this value.
type HomeStatus struct {
	Level            string // "ok", "warn" or "danger"
	Problems         []HomeProblem
	ActiveIndexers   int
	DisabledIndexers int
	ProblemIndexers  int
	Apps             int
	ProblemApps      int
	LastReleaseAt    time.Time
	// Err is a technical message for the red "cannot work" state. It is
	// translated into a generic sentence by the template.
	Err string
}

// Healthy reports whether the status has no current problem.
func (s HomeStatus) Healthy() bool {
	return s.Level == "ok"
}

// HasProblems reports whether the status has at least one actionable problem.
func (s HomeStatus) HasProblems() bool {
	return len(s.Problems) > 0
}

// HomeStatus computes the state from current data only. Errors that happened
// and then resolved are deliberately not part of it: they stay in Attività.
func (s *Server) HomeStatus(ctx context.Context) HomeStatus {
	status := HomeStatus{Level: "ok"}
	indexers, err := s.store.ListIndexers(ctx)
	if err != nil {
		return HomeStatus{Level: "danger", Err: err.Error()}
	}
	now := time.Now()
	for _, indexer := range indexers {
		if indexer.Enabled {
			status.ActiveIndexers++
		} else {
			status.DisabledIndexers++
		}
		state, err := s.store.GetIndexerState(ctx, indexer.ID)
		if err != nil {
			continue
		}
		lastError := strings.TrimSpace(state.LastError)
		if lastError == "" && state.Circuit != "open" && state.DisabledUntil.Before(now) {
			continue
		}
		if !indexer.Enabled {
			continue
		}
		status.ProblemIndexers++
		status.Problems = append(status.Problems, HomeProblem{
			Name:      indexer.Name,
			Code:      explainIndexerError(lastError, state.Circuit),
			ActionKey: "home.action.indexer",
			ActionURL: "/indexers/" + itoa(indexer.ID),
			Kind:      "indexer",
		})
	}
	applications, err := s.store.ListApplications(ctx)
	if err != nil {
		return HomeStatus{Level: "danger", Err: err.Error()}
	}
	status.Apps = len(applications)
	for _, app := range applications {
		if strings.TrimSpace(app.LastError) == "" {
			continue
		}
		status.ProblemApps++
		status.Problems = append(status.Problems, HomeProblem{
			Name:      app.Name,
			Code:      "home.problem.app",
			ActionKey: "home.action.app",
			ActionURL: "/applications",
			Kind:      "app",
		})
	}
	status.LastReleaseAt, _ = s.store.LastReleaseSeen(ctx)
	if len(status.Problems) > 0 {
		status.Level = "warn"
	}
	return status
}

// explainIndexerError maps a raw error into a catalog key. Unknown errors keep
// the generic sentence; the full text stays in the indexer detail and logs.
func explainIndexerError(message, circuit string) string {
	text := strings.ToLower(message)
	switch {
	case circuit == "open":
		return "home.problem.circuit"
	case strings.Contains(text, "429"):
		return "home.problem.rateLimit"
	case strings.Contains(text, "401") || strings.Contains(text, "403") ||
		strings.Contains(text, "credential") || strings.Contains(text, "unauthor"):
		return "home.problem.denied"
	case strings.Contains(text, "cloudflare") || strings.Contains(text, "challenge"):
		return "home.problem.cloudflare"
	case strings.Contains(text, "no such host") || strings.Contains(text, "dns") ||
		strings.Contains(text, "connection refused") || strings.Contains(text, "connect:"):
		return "home.problem.dns"
	case strings.Contains(text, "timeout") || strings.Contains(text, "deadline"):
		return "home.problem.timeout"
	case strings.Contains(text, "http"):
		return "home.problem.http"
	default:
		return "home.problem.unknown"
	}
}

// homeNextStep is the single suggestion of the home page.
type homeNextStep struct {
	Key       string
	ActionKey string
	ActionURL string
}

// nextStep returns at most one action, in the documented order: first indexer,
// public address, app, download client, backup. Nothing to do → nil.
func (s *Server) nextStep(ctx context.Context, status HomeStatus, settings store.Settings, clients int) *homeNextStep {
	switch {
	case status.ActiveIndexers == 0:
		return &homeNextStep{Key: "home.next.indexer", ActionKey: "home.action.addIndexer", ActionURL: "/indexers/new"}
	case strings.TrimSpace(settings.PublicURL) == "":
		return &homeNextStep{Key: "home.next.address", ActionKey: "home.action.useAddress", ActionURL: "#connect"}
	case status.Apps == 0:
		return &homeNextStep{Key: "home.next.app", ActionKey: "home.action.connectApp", ActionURL: "/applications/new"}
	case clients == 0 && settings.DownloadClient == "none":
		return &homeNextStep{Key: "home.next.client", ActionKey: "home.action.addClient", ActionURL: "/download-clients/new"}
	case !s.backupEver():
		return &homeNextStep{Key: "home.next.backup", ActionKey: "home.action.backup", ActionURL: "/settings?section=backup"}
	default:
		return nil
	}
}

// suggestedPublicURL is the address the browser is using right now. It is only
// a proposal until the operator confirms it with "Usa questo indirizzo".
func suggestedPublicURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if forwarded := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); forwarded == "http" || forwarded == "https" {
		scheme = forwarded
	}
	host := strings.TrimSpace(r.Host)
	if host == "" {
		return ""
	}
	return scheme + "://" + host
}

// backupEver reports whether at least one backup file exists.
func (s *Server) backupEver() bool {
	backups, err := backup.List(s.store.Path())
	return err == nil && len(backups) > 0
}

func itoa(value int64) string {
	if value == 0 {
		return "0"
	}
	var digits [20]byte
	i := len(digits)
	for value > 0 {
		i--
		digits[i] = byte('0' + value%10)
		value /= 10
	}
	return string(digits[i:])
}
