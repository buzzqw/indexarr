package web

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/buzzqw/indexarr/internal/importers"
	"github.com/buzzqw/indexarr/internal/torznabapi"
)

// importTimeout bounds one import request. The Prowlarr importer talks to a
// remote instance; the Jackett one reads local files.
const importTimeout = 20 * time.Second

// importPlanTTL is how long a preview can be applied. The entry lives in
// process memory only: the browser gets an opaque token, never the plan or its
// secrets.
const importPlanTTL = 10 * time.Minute

type importPlanEntry struct {
	targets []importers.NativeTarget
	created time.Time
}

// importPlanStore keeps previews between the POST that builds the plan and the
// POST that applies it. A server restart or an expired token simply asks the
// user to preview again.
type importPlanStore struct {
	mu      sync.Mutex
	entries map[string]importPlanEntry
	now     func() time.Time
}

func newImportPlanStore() *importPlanStore {
	return &importPlanStore{
		entries: make(map[string]importPlanEntry),
		now:     time.Now,
	}
}

func (s *importPlanStore) put(targets []importers.NativeTarget) (string, error) {
	token, err := newImportToken()
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	s.entries[token] = importPlanEntry{
		targets: targets,
		created: s.now(),
	}
	return token, nil
}

func (s *importPlanStore) get(token string) (importPlanEntry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.entries[token]
	if !ok {
		return importPlanEntry{}, false
	}
	if s.now().Sub(entry.created) > importPlanTTL {
		delete(s.entries, token)
		return importPlanEntry{}, false
	}
	return entry, true
}

func (s *importPlanStore) delete(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.entries, token)
}

// pruneLocked drops expired entries so a long-running server does not grow
// without bound. The caller holds the mutex.
func (s *importPlanStore) pruneLocked() {
	now := s.now()
	for token, entry := range s.entries {
		if now.Sub(entry.created) > importPlanTTL {
			delete(s.entries, token)
		}
	}
}

func newImportToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate import token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

type importIndexerRow struct {
	Index          int
	Name           string
	Implementation string
	Protocol       string
	DefinitionHint string
	BaseURL        string
	Categories     string
	Tags           string
	Enabled        bool
	HasKey         bool
	HasCredentials bool
	Target         string
	Manager        string
}

type importApplicationRow struct {
	Kind      string
	Name      string
	URL       string
	SyncLevel string
	HasKey    bool
}

type importProxyRow struct {
	Kind        string
	URL         string
	HasUser     bool
	HasPassword bool
}

type importPlanView struct {
	Source       string
	Token        string
	Indexers     []importIndexerRow
	Applications []importApplicationRow
	Proxies      []importProxyRow
	Warnings     []string
}

type importSummaryRow struct {
	Name    string
	Target  string
	Manager string
	Action  string
}

type importSummaryView struct {
	Created int
	Updated int
	Skipped int
	Errors  int
	Rows    []importSummaryRow
	Error   string
}

type importPage struct {
	base
	ProwlarrURL      string
	JackettPath      string
	PublicURL        string
	PublicURLDefault bool
	Plan             *importPlanView
	Summary          *importSummaryView
}

func (s *Server) importPage(w http.ResponseWriter, r *http.Request) {
	data := s.newImportPage(r)
	data.ProwlarrURL = strings.TrimSpace(r.URL.Query().Get("base_url"))
	data.JackettPath = strings.TrimSpace(r.URL.Query().Get("path"))
	s.render(w, r, "import", data, http.StatusOK)
}

// newImportPage fills the fields shared by preview, apply and error pages. The
// public URL is the effective one used by torznabapi; when it is not configured
// the current request address is shown as a suggestion, not stored anywhere.
func (s *Server) newImportPage(r *http.Request) importPage {
	data := importPage{base: s.newBaseT(r, "import.title", "import")}
	settings, ok := settingsFromContext(r.Context())
	if !ok {
		settings, _ = s.store.LoadSettings(r.Context())
	}
	data.PublicURL = torznabapi.BaseURL(r, settings)
	data.PublicURLDefault = strings.TrimSpace(settings.PublicURL) == ""
	return data
}

func (s *Server) importProwlarr(w http.ResponseWriter, r *http.Request) {
	baseURL := strings.TrimSpace(r.FormValue("base_url"))
	apiKey := strings.TrimSpace(r.FormValue("api_key"))
	data := s.newImportPage(r)
	data.ProwlarrURL = baseURL
	if baseURL == "" || apiKey == "" {
		data.Error = s.translate(r, "import.error.prowlarrMissing")
		s.render(w, r, "import", data, http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), importTimeout)
	defer cancel()
	plan, err := importers.FromProwlarr(ctx, baseURL, apiKey, &http.Client{Timeout: importTimeout})
	if err != nil {
		s.log.Warn("prowlarr import failed", "component", "indexarr", "subsystem", "web", "error", err)
		data.Error = s.translate(r, "import.error.prowlarrFailed", redactImportText(err.Error(), apiKey))
		s.render(w, r, "import", data, http.StatusBadRequest)
		return
	}
	s.renderImportPlan(w, r, data, plan)
}

func (s *Server) importJackett(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimSpace(r.FormValue("path"))
	data := s.newImportPage(r)
	data.JackettPath = path
	if path == "" {
		data.Error = s.translate(r, "import.error.jackettMissing")
		s.render(w, r, "import", data, http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), importTimeout)
	defer cancel()
	plan, err := importers.FromJackettConfig(ctx, path)
	if err != nil {
		s.log.Warn("jackett import failed", "component", "indexarr", "subsystem", "web", "error", err)
		data.Error = s.translate(r, "import.error.jackettFailed", redactImportText(err.Error()))
		s.render(w, r, "import", data, http.StatusBadRequest)
		return
	}
	s.renderImportPlan(w, r, data, plan)
}

// renderImportPlan maps the plan to native targets, keeps them server-side and
// renders only the non-secret view. The token in the hidden field is the only
// handle the browser gets.
func (s *Server) renderImportPlan(w http.ResponseWriter, r *http.Request, data importPage, plan importers.Plan) {
	targets, err := importers.Targets(plan)
	if err != nil {
		s.log.Warn("import target mapping failed", "component", "indexarr", "subsystem", "web", "error", err)
		data.Error = s.translate(r, "import.error.targetsFailed", redactImportText(err.Error()))
		s.render(w, r, "import", data, http.StatusBadRequest)
		return
	}
	token, err := s.importPlans.put(targets)
	if err != nil {
		s.log.Error("store import plan", "component", "indexarr", "subsystem", "web", "error", err)
		data.Error = s.translate(r, "import.error.targetsFailed", err.Error())
		s.render(w, r, "import", data, http.StatusInternalServerError)
		return
	}
	data.Plan = importPlanFrom(plan, targets, token)
	s.render(w, r, "import", data, http.StatusOK)
}

// importApply applies only the checked sites of a preview. The token is
// one-shot: a refresh or a replayed form cannot import again silently.
func (s *Server) importApply(w http.ResponseWriter, r *http.Request) {
	data := s.newImportPage(r)
	token := strings.TrimSpace(r.FormValue("token"))
	entry, ok := s.importPlans.get(token)
	if !ok {
		data.Error = s.translate(r, "import.error.planExpired")
		s.render(w, r, "import", data, http.StatusGone)
		return
	}

	selected, err := selectImportTargets(entry.targets, r.Form["site"])
	if err != nil {
		data.Error = s.translate(r, "import.error.noSelection")
		s.render(w, r, "import", data, http.StatusBadRequest)
		return
	}

	result, applyErr := importers.ApplyTargets(r.Context(), s.store, s.vault, selected)
	s.importPlans.delete(token)
	data.Summary = importSummaryFrom(result)
	if applyErr != nil {
		s.log.Warn("apply import failed", "component", "indexarr", "subsystem", "web", "error", applyErr)
		data.Summary.Error = s.translate(r, "import.error.applyFailed",
			redactImportText(applyErr.Error()))
	}
	s.render(w, r, "import", data, http.StatusOK)
}

// selectImportTargets keeps only the indexes present in the form. Duplicate or
// out-of-range values are rejected instead of guessing.
func selectImportTargets(targets []importers.NativeTarget, values []string) ([]importers.NativeTarget, error) {
	if len(values) == 0 {
		return nil, fmt.Errorf("no site selected")
	}
	seen := make(map[int]bool, len(values))
	out := make([]importers.NativeTarget, 0, len(values))
	for _, raw := range values {
		index, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil || index < 0 || index >= len(targets) {
			return nil, fmt.Errorf("invalid site index %q", raw)
		}
		if seen[index] {
			continue
		}
		seen[index] = true
		out = append(out, targets[index])
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no site selected")
	}
	return out, nil
}

// importPlanFrom builds the page view. Secret fields (APIKey, Username,
// Password) are deliberately not copied: the template can only show whether
// a value exists, and the native target carries secrets server-side only.
func importPlanFrom(plan importers.Plan, targets []importers.NativeTarget, token string) *importPlanView {
	view := &importPlanView{
		Source:   plan.Source,
		Token:    token,
		Warnings: append([]string(nil), plan.Warnings...),
	}
	for i, indexer := range plan.Indexers {
		row := importIndexerRow{
			Index:          i,
			Name:           indexer.Name,
			Implementation: indexer.Implementation,
			Protocol:       string(indexer.Protocol),
			DefinitionHint: indexer.DefinitionHint,
			BaseURL:        indexer.BaseURL,
			Categories:     joinInts(indexer.Categories),
			Tags:           strings.Join(indexer.Tags, ", "),
			Enabled:        indexer.Enabled,
			HasKey:         indexer.APIKey != "",
			HasCredentials: indexer.Username != "" || indexer.Password != "",
		}
		if i < len(targets) {
			row.Target = targets[i].DefinitionID
			row.Manager = managerLabel(targets[i].ViaManager)
		}
		view.Indexers = append(view.Indexers, row)
	}
	for _, app := range plan.Applications {
		view.Applications = append(view.Applications, importApplicationRow{
			Kind:      app.Kind,
			Name:      app.Name,
			URL:       app.URL,
			SyncLevel: app.SyncLevel,
			HasKey:    app.APIKey != "",
		})
	}
	for _, proxy := range plan.Proxies {
		view.Proxies = append(view.Proxies, importProxyRow{
			Kind:        proxy.Kind,
			URL:         proxy.URL,
			HasUser:     proxy.Username != "",
			HasPassword: proxy.Password != "",
		})
	}
	return view
}

func importSummaryFrom(result importers.ApplyResult) *importSummaryView {
	view := &importSummaryView{
		Created: result.Created,
		Updated: result.Updated,
		Skipped: result.Skipped,
		Errors:  result.Errors,
	}
	for _, item := range result.Applied {
		action := "created"
		if item.Updated {
			action = "updated"
		}
		view.Rows = append(view.Rows, importSummaryRow{
			Name:    item.Name,
			Target:  item.DefinitionID,
			Manager: managerLabel(item.ViaManager),
			Action:  action,
		})
	}
	return view
}

func managerLabel(manager string) string {
	switch strings.ToLower(strings.TrimSpace(manager)) {
	case "jackett":
		return "Jackett"
	case "prowlarr":
		return "Prowlarr"
	default:
		return ""
	}
}

func joinInts(values []int) string {
	if len(values) == 0 {
		return ""
	}
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, strconv.Itoa(value))
	}
	return strings.Join(parts, ", ")
}

// redactImportText removes any secret the caller still knows about from an
// error or warning. The importers already redact their own messages; this is a
// second line of defence around the submitted values.
func redactImportText(text string, secrets ...string) string {
	for _, secret := range secrets {
		secret = strings.TrimSpace(secret)
		if secret == "" {
			continue
		}
		for _, form := range []string{secret, url.QueryEscape(secret), url.PathEscape(secret)} {
			if form != "" {
				text = strings.ReplaceAll(text, form, "[redacted]")
			}
		}
	}
	return text
}
