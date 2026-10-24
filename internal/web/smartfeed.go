package web

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/buzzqw/indexarr/internal/feed"
	"github.com/buzzqw/indexarr/internal/indexersvc"
	"github.com/buzzqw/indexarr/internal/smartfeed"
	"github.com/buzzqw/indexarr/internal/store"
)

const smartfeedPlanTTL = 15 * time.Minute

// smartfeedPlanStore keeps the pasted Smartfeed URL in process memory between
// the recognition step and the confirmation form. The URL is a secret with a
// personal token: it must not be repeated in a query string, a hidden field or
// a log line. Only the random plan token travels through the HTML.
type smartfeedPlanStore struct {
	mu      sync.Mutex
	entries map[string]smartfeedPlan
	now     func() time.Time
}

type smartfeedPlan struct {
	rawURL  string
	created time.Time
}

func newSmartfeedPlanStore() *smartfeedPlanStore {
	return &smartfeedPlanStore{entries: make(map[string]smartfeedPlan), now: time.Now}
}

func (s *smartfeedPlanStore) put(rawURL string) (string, error) {
	token, err := newSmartfeedPlanToken()
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	s.entries[token] = smartfeedPlan{rawURL: rawURL, created: s.now()}
	return token, nil
}

func (s *smartfeedPlanStore) get(token string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.entries[token]
	if !ok {
		return "", false
	}
	if s.now().Sub(entry.created) > smartfeedPlanTTL {
		delete(s.entries, token)
		return "", false
	}
	return entry.rawURL, true
}

func (s *smartfeedPlanStore) delete(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.entries, token)
}

func (s *smartfeedPlanStore) pruneLocked() {
	now := s.now()
	for token, entry := range s.entries {
		if now.Sub(entry.created) > smartfeedPlanTTL {
			delete(s.entries, token)
		}
	}
}

func newSmartfeedPlanToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate Smartfeed plan token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

type smartfeedFormView struct {
	base
	Plan           string
	DisplayURL     string
	SuggestsT2     bool
	UseT2          bool
	NativeExists   bool
	NativeName     string
	ExistingLatest bool
	Problems       []string
	Error          string
	Username       string
	FlareSolverr   string
}

// smartfeedRecognized turns a pasted Smartfeed URL into a short-lived plan and
// redirects to a form that does not carry the token. It is called from the
// unified "Add source" resolver.
func (s *Server) smartfeedRecognized(w http.ResponseWriter, r *http.Request, rawURL string) {
	if s.smartfeedPlans == nil {
		redirect(w, r, "/indexers/new", "", s.translate(r, "indexer.error.serviceUnavailable"))
		return
	}
	token, err := s.smartfeedPlans.put(rawURL)
	if err != nil {
		redirect(w, r, "/indexers/new", "", err.Error())
		return
	}
	http.Redirect(w, r, "/indexers/smartfeed?plan="+token, http.StatusFound)
}

func (s *Server) smartfeedFormPage(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimSpace(r.URL.Query().Get("plan"))
	rawURL, ok := "", false
	if s.smartfeedPlans != nil && token != "" {
		rawURL, ok = s.smartfeedPlans.get(token)
	}
	if !ok {
		s.renderError(w, r, http.StatusBadRequest, s.translate(r, "idx.smartfeed.planExpired"))
		return
	}
	info, valid := smartfeed.Recognize(rawURL)
	if !valid {
		s.smartfeedPlans.delete(token)
		s.renderError(w, r, http.StatusBadRequest, s.translate(r, "idx.smartfeed.invalidURL"))
		return
	}
	data := s.smartfeedFormData(r, token, rawURL, info)
	s.render(w, r, "smartfeed_form", data, http.StatusOK)
}

func (s *Server) smartfeedFormData(r *http.Request, token, rawURL string, info smartfeed.Info) smartfeedFormView {
	data := smartfeedFormView{
		base:       s.newBaseT(r, "idx.smartfeed.title", "indexers"),
		Plan:       token,
		DisplayURL: feed.RedactURLForDisplay(rawURL),
		SuggestsT2: info.T == 1,
		UseT2:      info.T == 1,
	}
	if existing, found := s.findSmartfeedChannel(r.Context()); found {
		data.ExistingLatest = true
		data.NativeName = existing.Name
	}
	if native, found := s.findMirCrewNative(r.Context(), smartfeed.Site); found {
		data.NativeExists = true
		if data.NativeName == "" {
			data.NativeName = native.Name
		}
	}
	return data
}

func (s *Server) smartfeedFormAdd(w http.ResponseWriter, r *http.Request) {
	if s.svc == nil || s.smartfeedPlans == nil {
		redirect(w, r, "/indexers/new", "", s.translate(r, "indexer.error.serviceUnavailable"))
		return
	}
	token := strings.TrimSpace(r.FormValue("plan"))
	rawURL, ok := s.smartfeedPlans.get(token)
	if !ok {
		s.renderError(w, r, http.StatusBadRequest, s.translate(r, "idx.smartfeed.planExpired"))
		return
	}
	info, valid := smartfeed.Recognize(rawURL)
	if !valid {
		s.smartfeedPlans.delete(token)
		s.renderError(w, r, http.StatusBadRequest, s.translate(r, "idx.smartfeed.invalidURL"))
		return
	}
	if r.FormValue("use_t2") != "" {
		if normalized, changed := smartfeed.WithAnyPost(rawURL); changed {
			rawURL = normalized
			info, _ = smartfeed.Recognize(rawURL)
		}
	}

	data := s.smartfeedFormData(r, token, rawURL, info)
	if r.FormValue("mode") == "both" && !data.NativeExists {
		username := strings.TrimSpace(r.FormValue("username"))
		password := r.FormValue("password")
		if username == "" || password == "" {
			data.Problems = append(data.Problems, s.translate(r, "idx.smartfeed.credentialsRequired"))
			data.Error = s.translate(r, "idx.smartfeed.checkFields")
			data.Username = username
			data.FlareSolverr = strings.TrimSpace(r.FormValue("flaresolverr"))
			s.render(w, r, "smartfeed_form", data, http.StatusBadRequest)
			return
		}
		if err := s.createMirCrewNative(r, username, password, strings.TrimSpace(r.FormValue("flaresolverr"))); err != nil {
			data.Problems = append(data.Problems, s.indexerFriendlyError(r, err))
			data.Error = s.translate(r, "idx.smartfeed.checkFields")
			data.Username = username
			data.FlareSolverr = strings.TrimSpace(r.FormValue("flaresolverr"))
			s.render(w, r, "smartfeed_form", data, http.StatusBadRequest)
			return
		}
		data.NativeExists = true
	}

	latest, updated, err := s.saveSmartfeedChannel(r, rawURL)
	if err != nil {
		data.Problems = append(data.Problems, s.indexerFriendlyError(r, err))
		data.Error = s.translate(r, "idx.smartfeed.checkFields")
		s.render(w, r, "smartfeed_form", data, http.StatusBadRequest)
		return
	}
	s.smartfeedPlans.delete(token)
	messageKey := "idx.smartfeed.added"
	if updated {
		messageKey = "idx.smartfeed.updated"
	}
	if data.NativeExists {
		messageKey = "idx.smartfeed.addedPaired"
		if updated {
			messageKey = "idx.smartfeed.updatedPaired"
		}
	}
	redirect(w, r, "/indexers/"+fmt.Sprint(latest.ID), s.translate(r, messageKey, data.NativeName), "")
}

func (s *Server) saveSmartfeedChannel(r *http.Request, rawURL string) (store.Indexer, bool, error) {
	if err := s.ensureStoredDefinition(r, smartfeed.DefinitionID); err != nil {
		return store.Indexer{}, false, err
	}
	if existing, found := s.findSmartfeedChannel(r.Context()); found {
		enabled := true
		updated, err := s.svc.Update(r.Context(), existing.ID, indexersvc.Input{
			Enabled:  &enabled,
			Settings: map[string]string{"url": rawURL},
		})
		return updated, true, err
	}
	name, slug, err := s.uniqueIndexerIdentity(r.Context(), "MirCrew (Smartfeed)", smartfeed.DefinitionID)
	if err != nil {
		return store.Indexer{}, false, err
	}
	enabled := true
	priority := 25
	created, err := s.svc.Create(r.Context(), indexersvc.Input{
		DefinitionID: smartfeed.DefinitionID,
		Name:         name,
		Slug:         slug,
		Protocol:     "torrent",
		Priority:     &priority,
		Enabled:      &enabled,
		Settings: map[string]string{
			"url":       rawURL,
			"site":      smartfeed.Site,
			"channel":   "latest",
			"latest":    "true",
			"smartfeed": "true",
		},
	})
	return created, false, err
}

func (s *Server) createMirCrewNative(r *http.Request, username, password, flaresolverr string) error {
	if err := s.ensureStoredDefinition(r, smartfeed.NativeDefinitionID); err != nil {
		return err
	}
	name, slug, err := s.uniqueIndexerIdentity(r.Context(), "MirCrew", smartfeed.NativeDefinitionID)
	if err != nil {
		return err
	}
	enabled := true
	priority := 25
	_, err = s.svc.Create(r.Context(), indexersvc.Input{
		DefinitionID: smartfeed.NativeDefinitionID,
		Name:         name,
		Slug:         slug,
		Protocol:     "torrent",
		Priority:     &priority,
		Enabled:      &enabled,
		Settings: map[string]string{
			"username":     username,
			"password":     password,
			"flaresolverr": flaresolverr,
			"site":         smartfeed.Site,
			"channel":      "search",
			"latest":       "false",
		},
	})
	return err
}

func (s *Server) uniqueIndexerIdentity(ctx context.Context, base, definitionID string) (string, string, error) {
	indexers, err := s.store.ListIndexers(ctx)
	if err != nil {
		return "", "", err
	}
	names := make(map[string]bool, len(indexers))
	slugs := make(map[string]bool, len(indexers))
	for _, in := range indexers {
		names[strings.ToLower(in.Name)] = true
		slugs[strings.ToLower(in.Slug)] = true
	}
	name := uniqueIndexerName(base, names)
	slug := indexersvc.NormalizeSlug(name)
	if slug == "" {
		slug = definitionID
	}
	if !slugs[slug] {
		return name, slug, nil
	}
	for suffix := 2; ; suffix++ {
		candidate := fmt.Sprintf("%s-%d", slug, suffix)
		if !slugs[candidate] {
			return name, candidate, nil
		}
	}
}

func (s *Server) findSmartfeedChannel(ctx context.Context) (store.Indexer, bool) {
	indexers, err := s.store.ListIndexers(ctx)
	if err != nil {
		return store.Indexer{}, false
	}
	for _, in := range indexers {
		if strings.EqualFold(strings.TrimSpace(in.DefinitionID), smartfeed.DefinitionID) {
			return in, true
		}
		if truthy(publicConfigMap(in.ConfigJSON)["smartfeed"]) {
			return in, true
		}
	}
	return store.Indexer{}, false
}

func (s *Server) findMirCrewNative(ctx context.Context, site string) (store.Indexer, bool) {
	indexers, err := s.store.ListIndexers(ctx)
	if err != nil {
		return store.Indexer{}, false
	}
	var fallback store.Indexer
	fallbacks := 0
	for _, in := range indexers {
		if !strings.EqualFold(strings.TrimSpace(in.DefinitionID), smartfeed.NativeDefinitionID) {
			continue
		}
		config := publicConfigMap(in.ConfigJSON)
		if strings.EqualFold(strings.TrimSpace(config["site"]), strings.TrimSpace(site)) {
			return in, true
		}
		fallback = in
		fallbacks++
	}
	if fallbacks == 1 {
		return fallback, true
	}
	return store.Indexer{}, false
}

func (s *Server) smartfeedNativeChannel(ctx context.Context, site string) (bool, string) {
	if native, found := s.findMirCrewNative(ctx, site); found {
		return true, "/indexers/" + fmt.Sprint(native.ID)
	}
	return false, ""
}
