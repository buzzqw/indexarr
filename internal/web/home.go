package web

import (
	"context"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/buzzqw/indexarr/internal/backup"
	"github.com/buzzqw/indexarr/internal/health"
	"github.com/buzzqw/indexarr/internal/store"
)

// HomeProblem is one actionable problem shown on the home page. Code and
// ActionKey are catalog keys, so the same status serves every language.
type HomeProblem struct {
	Name      string
	Code      string
	Count     int
	ActionKey string
	ActionURL string
	Kind      string
}

// HomeStatus is the cached view of computeHomeStatus. The home page and the
// sidebar badge both read it; the value is derived from every indexer's
// health, which is too much work to repeat on every page render.
func (s *Server) HomeStatus(ctx context.Context) HomeStatus {
	rev := s.store.CountsRevision()
	now := time.Now()
	s.statusMu.Lock()
	if s.statusOK && s.statusRev == rev && now.Sub(s.statusAt) < homeStatusTTL {
		out := s.status
		out.Problems = append([]HomeProblem(nil), s.status.Problems...)
		s.statusMu.Unlock()
		return out
	}
	s.statusMu.Unlock()

	status := s.computeHomeStatus(ctx)

	s.statusMu.Lock()
	if s.store.CountsRevision() == rev {
		s.status = status
		s.statusOK = true
		s.statusAt = now
		s.statusRev = rev
	}
	s.statusMu.Unlock()
	return status
}

// homeStatusTTL bounds how stale the sidebar badge can be. Indexer state and
// history writes bump the store revision, so a real change clears the cache
// immediately; the TTL only absorbs repeated reads that change nothing.
const homeStatusTTL = 10 * time.Second

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

// computeHomeStatus computes the state from current data only. Errors that
// happened and then resolved are deliberately not part of it: they stay in
// Attività.
func (s *Server) computeHomeStatus(ctx context.Context) HomeStatus {
	status := HomeStatus{Level: "ok"}
	indexers, err := s.store.ListIndexers(ctx)
	if err != nil {
		return HomeStatus{Level: "danger", Err: err.Error()}
	}
	for _, indexer := range indexers {
		if indexer.Enabled {
			status.ActiveIndexers++
		} else {
			status.DisabledIndexers++
		}
		if !indexer.Enabled {
			continue
		}
		view := s.loadIndexerHealth(ctx, indexer)
		if !view.Assessment.NeedsAttention() {
			continue
		}
		status.ProblemIndexers++
		problem := HomeProblem{
			Name:      indexer.Name,
			ActionKey: "home.action.indexer",
			ActionURL: "/indexers/" + itoa(indexer.ID),
			Kind:      "indexer",
		}
		problem.Code, problem.Count = homeProblemCode(view)
		status.Problems = append(status.Problems, problem)
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

// homeProblemCode maps the first health reason to the one-sentence catalog key
// shown in Home. The same reason drives the list and the detail page.
func homeProblemCode(view indexerHealthView) (string, int) {
	for _, reason := range view.Assessment.Reasons {
		switch reason.Code {
		case health.ReasonCircuit:
			return "home.problem.circuit", 0
		case health.ReasonLastError:
			return explainIndexerError(view.State.LastError, view.State.Circuit), 0
		case health.ReasonErrors:
			if raw := strings.TrimSpace(view.Assessment.LastError); raw != "" {
				code := explainIndexerError(raw, view.State.Circuit)
				if code == "home.problem.unknown" {
					return code, 0
				}
				return code, reason.Count
			}
			return "home.problem.errors", reason.Count
		case health.ReasonZeroResults:
			return "home.problem.zero", reason.Count
		}
	}
	return "home.problem.unknown", 0
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
	// A browser on the same machine sees 127.0.0.1, but Sonarr/Radarr in
	// Docker cannot reach that address. Propose the LAN address instead; the
	// help text explains when host.docker.internal is needed.
	hostname, port, err := net.SplitHostPort(host)
	if err == nil && (hostname == "localhost" || hostname == "127.0.0.1" || hostname == "::1") {
		if lan := lanIPv4(); lan != "" {
			host = net.JoinHostPort(lan, port)
		}
	}
	return scheme + "://" + host
}

func lanIPv4() string {
	interfaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			var ip net.IP
			switch value := addr.(type) {
			case *net.IPNet:
				ip = value.IP
			case *net.IPAddr:
				ip = value.IP
			}
			if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.To4() == nil {
				continue
			}
			if ip.IsPrivate() {
				return ip.String()
			}
		}
	}
	return ""
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
