package web

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/buzzqw/indexarr/internal/indexersvc"
	"github.com/buzzqw/indexarr/internal/store"
)

func (s *Server) indexerUnifiedTest(w http.ResponseWriter, r *http.Request) {
	s.indexerAction(w, r, false)
}

func (s *Server) indexerUnifiedRun(w http.ResponseWriter, r *http.Request) {
	s.indexerAction(w, r, true)
}

func (s *Server) indexerAction(w http.ResponseWriter, r *http.Request, run bool) {
	id, err := pathID(r)
	if err != nil {
		redirect(w, r, "/indexers", "", s.translate(r, "indexer.error.invalidID"))
		return
	}
	if _, err := s.store.GetIndexer(r.Context(), id); err != nil {
		redirect(w, r, "/indexers", "", s.translate(r, "indexer.error.notFound"))
		return
	}
	data, buildErr := s.buildIndexerDetail(r, id)
	if buildErr != nil {
		redirect(w, r, "/indexers", "", s.indexerErrorMessage(r, buildErr))
		return
	}
	if s.native == nil {
		if run {
			data.Error = s.translate(r, "indexer.error.nativeEngineRun")
		} else {
			data.Error = s.translate(r, "indexer.error.nativeEngineTest")
		}
		s.render(w, r, "indexer", data, http.StatusOK)
		return
	}
	if run {
		stats, runErr := s.native.Run(r.Context(), id)
		if stats != nil {
			data.Run = stats
			if len(stats.Items) > 20 {
				data.RunItems = stats.Items[:20]
			} else {
				data.RunItems = stats.Items
			}
		}
		if runErr != nil {
			data.Error = s.translate(r, "indexer.runFailed", runErr.Error())
		}
	} else {
		result, testErr := s.native.Test(r.Context(), id)
		data.Test = &result
		if testErr != nil {
			data.Error = s.translate(r, "indexer.testFailedDetail", testErr.Error())
		}
	}
	if refreshed, err := s.buildIndexerDetail(r, id); err == nil {
		refreshed.Test = data.Test
		refreshed.Run = data.Run
		refreshed.RunItems = data.RunItems
		refreshed.Error = data.Error
		data = refreshed
	}
	s.render(w, r, "indexer", data, http.StatusOK)
}

func (s *Server) indexerUnifiedDelete(w http.ResponseWriter, r *http.Request) {
	if s.svc == nil {
		s.renderError(w, r, http.StatusServiceUnavailable, s.translate(r, "indexer.error.serviceUnavailable"))
		return
	}
	returnPath := sanitizeIndexerReturn(r.FormValue("return"))
	if returnPath == "" {
		returnPath = "/indexers"
	}
	id, err := pathID(r)
	if err != nil {
		redirect(w, r, returnPath, "", s.translate(r, "indexer.error.invalidID"))
		return
	}
	if err := s.svc.Delete(r.Context(), id); err != nil {
		redirect(w, r, returnPath, "", s.indexerFriendlyError(r, err))
		return
	}
	redirect(w, r, returnPath, s.translate(r, "indexer.deleted"), "")
}

func (s *Server) indexerToggle(w http.ResponseWriter, r *http.Request) {
	if s.svc == nil {
		redirect(w, r, "/indexers", "", s.translate(r, "indexer.error.serviceUnavailable"))
		return
	}
	returnPath := sanitizeIndexerReturn(r.FormValue("return"))
	if returnPath == "" {
		returnPath = "/indexers"
	}
	id, err := pathID(r)
	if err != nil {
		redirect(w, r, returnPath, "", s.translate(r, "indexer.error.invalidID"))
		return
	}
	current, err := s.store.GetIndexer(r.Context(), id)
	if err != nil {
		redirect(w, r, returnPath, "", s.indexerErrorMessage(r, err))
		return
	}
	enabled := !current.Enabled
	if _, err := s.svc.Update(r.Context(), id, indexersvc.Input{Enabled: &enabled}); err != nil {
		redirect(w, r, returnPath, "", s.indexerFriendlyError(r, err))
		return
	}
	if enabled {
		redirect(w, r, returnPath, s.translate(r, "idx.enabledMessage", current.Name), "")
		return
	}
	redirect(w, r, returnPath, s.translate(r, "idx.disabledMessage", current.Name), "")
}

// indexerReduceFrequency implements the "Riduci la frequenza" action of a rate
// limited source. It doubles the interval that is currently in force (the
// source's own value, or the global default when it has none), keeps it within
// the same 30-day ceiling the form accepts, and never touches the other
// sources. Live app searches are not delayed: the interval governs the
// automatic updates only.
func (s *Server) indexerReduceFrequency(w http.ResponseWriter, r *http.Request) {
	const (
		minInterval = 30 * 60
		maxInterval = 30 * 24 * 60 * 60
	)
	if s.svc == nil {
		redirect(w, r, "/indexers", "", s.translate(r, "indexer.error.serviceUnavailable"))
		return
	}
	id, err := pathID(r)
	if err != nil {
		redirect(w, r, "/indexers", "", s.translate(r, "indexer.error.invalidID"))
		return
	}
	current, err := s.store.GetIndexer(r.Context(), id)
	if err != nil {
		redirect(w, r, "/indexers", "", s.indexerErrorMessage(r, err))
		return
	}
	target := frequencyReturnPath(r.FormValue("return"), id)
	settings, settingsErr := s.store.LoadSettings(r.Context())
	if settingsErr != nil {
		settings = store.DefaultSettings()
	}
	base := current.IntervalSeconds
	if base <= 0 {
		base = settings.DefaultIntervalSeconds
	}
	if base <= 0 {
		base = 3600
	}
	if base >= maxInterval {
		redirect(w, r, target, "", s.translate(r, "idx.frequencyMax", current.Name))
		return
	}
	next := base * 2
	if next < minInterval {
		next = minInterval
	}
	if next > maxInterval {
		next = maxInterval
	}
	if _, err := s.svc.Update(r.Context(), id, indexersvc.Input{IntervalSeconds: &next}); err != nil {
		redirect(w, r, target, "", s.indexerFriendlyError(r, err))
		return
	}
	redirect(w, r, target,
		s.translate(r, "idx.frequencyReduced", current.Name, intervalEveryLabel(requestLanguage(r), next)), "")
}

// indexerResumeCircuit implements the "Riprendi ora" action of a source whose
// circuit breaker is open. The breaker lives in memory and has a bounded
// cooldown; the operator can clear it at once, without editing, deleting and
// recreating the source. The automatic schedule and the query budget are left
// as they are: this only allows the next request to be attempted.
func (s *Server) indexerResumeCircuit(w http.ResponseWriter, r *http.Request) {
	if s.native == nil {
		redirect(w, r, "/indexers", "", s.translate(r, "indexer.error.serviceUnavailable"))
		return
	}
	id, err := pathID(r)
	if err != nil {
		redirect(w, r, "/indexers", "", s.translate(r, "indexer.error.invalidID"))
		return
	}
	target := frequencyReturnPath(r.FormValue("return"), id)
	indexer, err := s.native.ResetCircuit(r.Context(), id)
	if err != nil {
		redirect(w, r, target, "", s.indexerErrorMessage(r, err))
		return
	}
	s.log.Info("circuit breaker cleared by hand",
		"component", "indexarr", "subsystem", "web",
		"indexer", indexer.Slug, "indexer_id", indexer.ID)
	redirect(w, r, target, s.translate(r, "idx.resume.done", indexer.Name), "")
}

func (s *Server) indexerDuplicate(w http.ResponseWriter, r *http.Request) {
	if s.svc == nil {
		redirect(w, r, "/indexers", "", s.translate(r, "indexer.error.serviceUnavailable"))
		return
	}
	id, err := pathID(r)
	if err != nil {
		redirect(w, r, "/indexers", "", s.translate(r, "indexer.error.invalidID"))
		return
	}
	current, err := s.store.GetIndexer(r.Context(), id)
	if err != nil {
		redirect(w, r, "/indexers", "", s.indexerErrorMessage(r, err))
		return
	}
	settings := publicConfigMap(current.ConfigJSON)
	secrets, err := s.store.ListSecrets(r.Context(), id)
	if err != nil {
		redirect(w, r, "/indexers", "", err.Error())
		return
	}
	for _, secret := range secrets {
		if s.vault == nil {
			redirect(w, r, "/indexers", "", s.translate(r, "indexer.error.vaultRequired"))
			return
		}
		value, err := s.vault.Open(secret.Nonce, secret.Ciphertext)
		if err != nil {
			redirect(w, r, "/indexers", "", err.Error())
			return
		}
		settings[secret.Key] = value
	}
	indexers, _ := s.store.ListIndexers(r.Context())
	names := make(map[string]bool, len(indexers))
	for _, in := range indexers {
		names[strings.ToLower(in.Name)] = true
	}
	name := uniqueIndexerName(current.Name, names)
	enabled := current.Enabled
	priority := current.Priority
	created, err := s.svc.Create(r.Context(), indexersvc.Input{
		DefinitionID: current.DefinitionID,
		Name:         name,
		Protocol:     current.Protocol,
		Priority:     &priority,
		Tags:         splitTagList(current.Tags),
		Enabled:      &enabled,
		Settings:     settings,
	})
	if err != nil {
		redirect(w, r, "/indexers", "", s.indexerFriendlyError(r, err))
		return
	}
	redirect(w, r, "/indexers/"+strconv.FormatInt(created.ID, 10), s.translate(r, "idx.duplicated", name), "")
}

func (s *Server) indexersBulk(w http.ResponseWriter, r *http.Request) {
	if s.svc == nil {
		redirect(w, r, "/indexers", "", s.translate(r, "indexer.error.serviceUnavailable"))
		return
	}
	returnPath := sanitizeIndexerReturn(r.FormValue("return"))
	if returnPath == "" {
		returnPath = "/indexers"
	}
	ids := make([]int64, 0)
	for _, raw := range r.Form["ids"] {
		if id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64); err == nil && id > 0 {
			ids = append(ids, id)
		}
	}
	action := strings.ToLower(strings.TrimSpace(r.FormValue("action")))
	if len(ids) == 0 {
		redirect(w, r, returnPath, "", s.translate(r, "idx.bulkNone"))
		return
	}
	count := 0
	for _, id := range ids {
		switch action {
		case "delete":
			if err := s.svc.Delete(r.Context(), id); err == nil {
				count++
			}
		case "disable", "enable":
			enabled := action == "enable"
			if _, err := s.svc.Update(r.Context(), id, indexersvc.Input{Enabled: &enabled}); err == nil {
				count++
			}
		}
	}
	redirect(w, r, returnPath, s.translate(r, "idx.bulkDone", count), "")
}
