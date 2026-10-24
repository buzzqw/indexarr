package web

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/buzzqw/indexarr/internal/indexer"
	"github.com/buzzqw/indexarr/internal/indexersvc"
	"github.com/buzzqw/indexarr/internal/store"
)

// storeWarningTTL bounds how long a measured fourth-source warning stays
// valid. The pending form values, including secrets, live only in memory and
// are never rendered back into the page.
const storeWarningTTL = 20 * time.Minute

type pendingStoreEntry struct {
	input        indexersvc.Input
	definitionID string
	editID       int64
	created      time.Time
}

type pendingStoreSet struct {
	mu      sync.Mutex
	entries map[string]pendingStoreEntry
	now     func() time.Time
}

func newPendingStoreSet() *pendingStoreSet {
	return &pendingStoreSet{
		entries: make(map[string]pendingStoreEntry),
		now:     time.Now,
	}
}

func (p *pendingStoreSet) put(entry pendingStoreEntry) (string, error) {
	token, err := newImportToken()
	if err != nil {
		return "", err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pruneLocked()
	entry.created = p.now()
	p.entries[token] = entry
	return token, nil
}

func (p *pendingStoreSet) get(token string) (pendingStoreEntry, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	entry, ok := p.entries[token]
	if !ok {
		return pendingStoreEntry{}, false
	}
	if p.now().Sub(entry.created) > storeWarningTTL {
		delete(p.entries, token)
		return pendingStoreEntry{}, false
	}
	return entry, true
}

func (p *pendingStoreSet) take(token string) (pendingStoreEntry, bool) {
	entry, ok := p.get(token)
	if ok {
		p.mu.Lock()
		delete(p.entries, token)
		p.mu.Unlock()
	}
	return entry, ok
}

func (p *pendingStoreSet) drop(token string) {
	p.mu.Lock()
	delete(p.entries, token)
	p.mu.Unlock()
}

func (p *pendingStoreSet) pruneLocked() {
	for token, entry := range p.entries {
		if p.now().Sub(entry.created) > storeWarningTTL {
			delete(p.entries, token)
		}
	}
}

type storeWarningView struct {
	base
	Token       string
	Name        string
	PerDay      int64
	MonthBytes  int64
	DBSize      int64
	Items       int
	Dated       int
	WindowLabel string
	StoredCount int
	Limit       int
	ProbeError  string
	BackURL     string
	ConfirmURL  string
	CancelURL   string
}

// storeWarningNeeded reports whether activating this form would exceed the
// soft limit of stored search-only sources. The candidate itself is not
// counted when it is already stored.
func (s *Server) storeWarningNeeded(ctx context.Context, in indexersvc.Input, editID int64) (bool, int, int, error) {
	if !truthy(in.Settings["store_latest"]) {
		return false, 0, 0, nil
	}
	settings, err := s.store.LoadSettings(ctx)
	if err != nil {
		return false, 0, 0, err
	}
	if settings.StoredSourceLimit <= 0 {
		return false, 0, settings.StoredSourceLimit, nil
	}
	count, err := s.store.StoredSourceCount(ctx)
	if err != nil {
		return false, 0, settings.StoredSourceLimit, err
	}
	if editID != 0 {
		current, err := s.store.GetIndexer(ctx, editID)
		if err == nil && truthy(publicConfigMap(current.ConfigJSON)["store_latest"]) {
			// Already stored: changing the other fields is not a new source.
			return false, count, settings.StoredSourceLimit, nil
		}
	}
	return count >= settings.StoredSourceLimit, count, settings.StoredSourceLimit, nil
}

// showStoreWarning measures one latest-list read and renders the confirmation.
// Nothing is stored: the temporary indexer exists disabled only for the probe
// and is removed before the page is rendered.
func (s *Server) showStoreWarning(w http.ResponseWriter, r *http.Request, defID string, editID int64, in indexersvc.Input) {
	view := storeWarningView{
		base:        s.newBaseT(r, "storeWarning.title", "indexers"),
		Name:        in.Name,
		BackURL:     "/indexers/new?definition=" + defID,
		ConfirmURL:  "/indexers/store/confirm",
		CancelURL:   "/indexers/store/cancel",
		StoredCount: 0,
		Limit:       store.DefaultSettings().StoredSourceLimit,
	}
	if editID != 0 {
		view.BackURL = "/indexers/" + strconv.FormatInt(editID, 10)
		if in.Name == "" {
			if current, err := s.store.GetIndexer(r.Context(), editID); err == nil {
				view.Name = current.Name
			}
		}
	}
	if strings.TrimSpace(view.Name) == "" {
		view.Name = defID
	}
	if _, count, limit, err := s.storeWarningNeeded(r.Context(), in, editID); err == nil {
		view.StoredCount, view.Limit = count, limit
	}
	token, err := s.pendingStores.put(pendingStoreEntry{
		input:        cloneIndexerInput(in),
		definitionID: defID,
		editID:       editID,
	})
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	view.Token = token
	if s.native == nil || s.svc == nil {
		view.ProbeError = "the native engine is not available"
		s.render(w, r, "store_warning", view, http.StatusOK)
		return
	}
	probe, probeErr := s.probeStoreCandidate(r, defID, in)
	if probeErr != nil {
		view.ProbeError = probeErr.Error()
	} else {
		view.PerDay = probe.PerDay
		view.MonthBytes = probe.BytesPerMonth
		view.DBSize = probe.DBSize
		view.Items = probe.Items
		view.Dated = probe.Dated
		view.WindowLabel = storeProbeWindow(probe.Window)
	}
	s.render(w, r, "store_warning", view, http.StatusOK)
}

// probeStoreCandidate creates a disabled temporary indexer with the submitted
// configuration, reads its latest list and deletes it. The temporary row never
// reaches the scheduler and carries no store flag.
func (s *Server) probeStoreCandidate(r *http.Request, defID string, in indexersvc.Input) (indexer.LatestProbe, error) {
	ctx := r.Context()
	probeInput := cloneIndexerInput(in)
	probeInput.Name = "store probe"
	probeInput.Slug = "store-probe-" + strings.ReplaceAll(defID, "/", "-")
	probeInput.DefinitionID = defID
	probeInput.Settings["store_latest"] = "false"
	enabled := false
	probeInput.Enabled = &enabled
	created, err := s.svc.Create(ctx, probeInput)
	if err != nil {
		return indexer.LatestProbe{}, fmt.Errorf("%s", s.indexerFriendlyError(r, err))
	}
	defer func() { _ = s.svc.Delete(context.WithoutCancel(ctx), created.ID) }()
	return s.native.ProbeLatest(ctx, created.ID, 100)
}

func (s *Server) indexerStoreConfirm(w http.ResponseWriter, r *http.Request) {
	if s.svc == nil {
		s.renderError(w, r, http.StatusServiceUnavailable, s.translate(r, "indexer.error.serviceUnavailable"))
		return
	}
	token := strings.TrimSpace(r.FormValue("token"))
	entry, ok := s.pendingStores.take(token)
	if !ok {
		s.renderError(w, r, http.StatusBadRequest, s.translate(r, "indexer.error.invalidID"))
		return
	}
	var id int64
	var err error
	if entry.editID != 0 {
		_, err = s.svc.Update(r.Context(), entry.editID, entry.input)
		id = entry.editID
	} else {
		var created store.Indexer
		created, err = s.svc.Create(r.Context(), entry.input)
		id = created.ID
	}
	if err != nil {
		redirect(w, r, entryBackURL(entry), "", s.indexerFriendlyError(r, err))
		return
	}
	redirect(w, r, "/indexers/"+strconv.FormatInt(id, 10), s.translate(r, "indexer.saved"), "")
}

func (s *Server) indexerStoreCancel(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimSpace(r.FormValue("token"))
	entry, ok := s.pendingStores.take(token)
	if !ok {
		redirect(w, r, "/indexers", "", "")
		return
	}
	redirect(w, r, entryBackURL(entry), "", "")
}

func entryBackURL(entry pendingStoreEntry) string {
	if entry.editID != 0 {
		return "/indexers/" + strconv.FormatInt(entry.editID, 10)
	}
	return "/indexers/new?definition=" + entry.definitionID
}

func cloneIndexerInput(in indexersvc.Input) indexersvc.Input {
	out := in
	if in.Priority != nil {
		value := *in.Priority
		out.Priority = &value
	}
	if in.Enabled != nil {
		value := *in.Enabled
		out.Enabled = &value
	}
	if in.IntervalSeconds != nil {
		value := *in.IntervalSeconds
		out.IntervalSeconds = &value
	}
	out.Tags = append([]string(nil), in.Tags...)
	if in.Settings != nil {
		out.Settings = make(map[string]string, len(in.Settings))
		for key, value := range in.Settings {
			out.Settings[key] = value
		}
	}
	return out
}

func storeProbeWindow(window time.Duration) string {
	if window <= 0 {
		return "1 day"
	}
	hours := int(window.Hours() + 0.5)
	if hours < 24 {
		if hours < 1 {
			hours = 1
		}
		return fmt.Sprintf("%d h", hours)
	}
	days := int(window.Hours()/24 + 0.5)
	if days < 1 {
		days = 1
	}
	return fmt.Sprintf("%d days", days)
}
