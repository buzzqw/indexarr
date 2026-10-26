package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/buzzqw/indexarr/internal/definition"
	"github.com/buzzqw/indexarr/internal/feed"
	"github.com/buzzqw/indexarr/internal/indexersvc"
	"github.com/buzzqw/indexarr/internal/native"
	"github.com/buzzqw/indexarr/internal/store"
)

// sourcesRedirect keeps old /sources bookmarks and links working.
func (s *Server) sourcesRedirect(w http.ResponseWriter, r *http.Request) {
	target := "/indexers"
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	http.Redirect(w, r, target, http.StatusFound)
}

func (s *Server) indexersTestAll(w http.ResponseWriter, r *http.Request) {
	if s.sched == nil {
		redirect(w, r, "/indexers", "", s.translate(r, "indexer.error.serviceUnavailable"))
		return
	}
	count, err := s.sched.TestAll(r.Context())
	if err != nil {
		redirect(w, r, "/indexers", "", err.Error())
		return
	}
	redirect(w, r, "/indexers", s.translate(r, "idx.testAllQueued", count), "")
}

// indexersTestProblems probes only the sources currently flagged by the health
// model (problem, degraded or blocked). One click after fixing them checks the
// real state without re-testing the healthy half of the catalogue.
func (s *Server) indexersTestProblems(w http.ResponseWriter, r *http.Request) {
	if s.sched == nil || s.svc == nil {
		redirect(w, r, "/indexers?state=problem", "", s.translate(r, "indexer.error.serviceUnavailable"))
		return
	}
	indexers, err := s.svc.Indexers(r.Context())
	if err != nil {
		redirect(w, r, "/indexers?state=problem", "", err.Error())
		return
	}
	settings, settingsErr := s.store.LoadSettings(r.Context())
	if settingsErr != nil {
		settings = store.DefaultSettings()
	}
	globalInterval := settings.DefaultInterval()
	defs := s.indexerDefinitions(r.Context(), indexers)
	batch := s.loadHealthBatch(r.Context(), indexers)
	ids := make([]int64, 0, len(indexers))
	for _, in := range indexers {
		if !in.Enabled {
			continue
		}
		view := s.indexerRowView(r, in, defs, globalInterval, batch)
		switch view.Status.State {
		case "problem", "degraded", "blocked":
			ids = append(ids, in.ID)
		}
	}
	if len(ids) == 0 {
		redirect(w, r, "/indexers?state=problem", s.translate(r, "idx.testProblemsNone"), "")
		return
	}
	count, err := s.sched.TestMany(r.Context(), ids)
	if err != nil {
		if errors.Is(err, native.ErrTestAllRunning) {
			redirect(w, r, "/indexers?state=problem", "", s.translate(r, "idx.testAlreadyRunning"))
			return
		}
		redirect(w, r, "/indexers?state=problem", "", err.Error())
		return
	}
	redirect(w, r, "/indexers?state=problem", s.translate(r, "idx.testProblemsQueued", count), "")
}

func (s *Server) indexersRunAll(w http.ResponseWriter, r *http.Request) {
	if s.sched == nil {
		redirect(w, r, "/indexers", "", s.translate(r, "indexer.error.serviceUnavailable"))
		return
	}
	count, err := s.sched.RunAll(r.Context())
	if err != nil {
		redirect(w, r, "/indexers", "", err.Error())
		return
	}
	redirect(w, r, "/indexers", s.translate(r, "idx.runAllQueued", count), "")
}

// --------------------------------------------------------------- JSON export

type indexerJSON struct {
	Name            string            `json:"name"`
	Slug            string            `json:"slug"`
	DefinitionID    string            `json:"definition_id"`
	Protocol        string            `json:"protocol"`
	Enabled         bool              `json:"enabled"`
	Priority        int               `json:"priority"`
	IntervalSeconds int               `json:"interval_seconds,omitempty"`
	Tags            []string          `json:"tags,omitempty"`
	Settings        map[string]string `json:"settings,omitempty"`
}

type indexersJSON struct {
	Version  int           `json:"version"`
	Indexers []indexerJSON `json:"indexers"`
}

// indexersExport writes the public configuration only. Secrets live in the
// vault and are never included in the download.
func (s *Server) indexersExport(w http.ResponseWriter, r *http.Request) {
	indexers, err := s.store.ListIndexers(r.Context())
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	payload := indexersJSON{Version: 1, Indexers: make([]indexerJSON, 0, len(indexers))}
	for _, in := range indexers {
		settings := map[string]string{}
		decoder := json.NewDecoder(strings.NewReader(strings.TrimSpace(in.ConfigJSON)))
		var raw map[string]any
		if err := decoder.Decode(&raw); err == nil {
			for key, value := range raw {
				if indexersvc.IsSecretKey(key) {
					continue
				}
				text := configText(value)
				// A URL that carries a key in its query or path (an RSS feed
				// with an rsskey, for example) is a secret: never export it in
				// clear. The user re-enters it on import.
				if feed.RedactURL(text) != text {
					continue
				}
				settings[key] = text
			}
		}
		payload.Indexers = append(payload.Indexers, indexerJSON{
			Name:            in.Name,
			Slug:            in.Slug,
			DefinitionID:    in.DefinitionID,
			Protocol:        in.Protocol,
			Enabled:         in.Enabled,
			Priority:        in.Priority,
			IntervalSeconds: in.IntervalSeconds,
			Tags:            splitTagList(in.Tags),
			Settings:        settings,
		})
	}
	body, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	body = append(body, '\n')
	filename := "indexarr-indexers-" + time.Now().Format("20060102-150405") + ".json"
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(body)
}

func (s *Server) indexersImport(w http.ResponseWriter, r *http.Request) {
	if s.svc == nil {
		redirect(w, r, "/indexers", "", s.translate(r, "indexer.error.serviceUnavailable"))
		return
	}
	raw := strings.TrimSpace(r.FormValue("json"))
	if raw == "" {
		redirect(w, r, "/indexers", "", s.translate(r, "idx.importEmpty"))
		return
	}
	var payload indexersJSON
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		redirect(w, r, "/indexers", "", s.translate(r, "idx.importInvalidJSON", err.Error()))
		return
	}
	if payload.Version != 0 && payload.Version != 1 {
		redirect(w, r, "/indexers", "", s.translate(r, "idx.importVersion", payload.Version))
		return
	}
	if len(payload.Indexers) == 0 {
		redirect(w, r, "/indexers", "", s.translate(r, "idx.importNoIndexers"))
		return
	}
	existing, err := s.store.ListIndexers(r.Context())
	if err != nil {
		redirect(w, r, "/indexers", "", err.Error())
		return
	}
	slugs := make(map[string]bool, len(existing))
	names := make(map[string]bool, len(existing))
	for _, in := range existing {
		slugs[strings.ToLower(in.Slug)] = true
		names[strings.ToLower(in.Name)] = true
	}

	imported, skipped, invalid := 0, 0, 0
	for index, entry := range payload.Indexers {
		name := strings.TrimSpace(entry.Name)
		slug := indexersvc.NormalizeSlug(entry.Slug)
		if slug == "" {
			slug = indexersvc.NormalizeSlug(name)
		}
		if name == "" || slug == "" {
			invalid++
			continue
		}
		if slugs[slug] || names[strings.ToLower(name)] {
			skipped++
			continue
		}
		if err := s.ensureStoredDefinition(r, entry.DefinitionID); err != nil {
			invalid++
			s.log.Warn("indexer import skipped", "index", index, "name", name, "error", err)
			continue
		}
		priority := entry.Priority
		enabled := entry.Enabled
		interval := entry.IntervalSeconds
		if _, err := s.svc.Create(r.Context(), indexersvc.Input{
			DefinitionID:    entry.DefinitionID,
			Name:            name,
			Slug:            slug,
			Protocol:        entry.Protocol,
			Priority:        &priority,
			Tags:            entry.Tags,
			Enabled:         &enabled,
			IntervalSeconds: &interval,
			Settings:        entry.Settings,
		}); err != nil {
			invalid++
			s.log.Warn("indexer import failed", "index", index, "name", name, "error", err)
			continue
		}
		slugs[slug] = true
		names[strings.ToLower(name)] = true
		imported++
	}
	redirect(w, r, "/indexers", s.translate(r, "idx.importDone", imported, skipped, invalid), "")
}

// ---------------------------------------------------------------- discovery

type discoverPage struct {
	base
	SourceURL string
	Feeds     []feed.DiscoveredFeed
}

func (s *Server) indexersDiscover(w http.ResponseWriter, r *http.Request) {
	rawURL := strings.TrimSpace(r.URL.Query().Get("url"))
	if rawURL == "" {
		redirect(w, r, "/indexers", "", s.translate(r, "idx.discoverMissing"))
		return
	}
	settings, err := s.store.LoadSettings(r.Context())
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), settings.FetchTimeout()+20*time.Second)
	defer cancel()
	feeds, err := feed.DiscoverFeeds(ctx, rawURL, feed.RequestOptions{
		UserAgent:    settings.UserAgent,
		FlareSolverr: settings.FlareSolverrURL,
	}, settings.FetchTimeout())
	data := discoverPage{
		base:      s.newBase(r, "Feed discovery", "indexers"),
		SourceURL: rawURL,
		Feeds:     feeds,
	}
	if err != nil {
		data.Error = err.Error()
		s.log.Warn("feed discovery failed", "subsystem", "discovery", "url", rawURL, "error", err)
	} else {
		s.log.Info("feed discovery done", "url", rawURL, "feeds", len(feeds))
	}
	s.render(w, r, "discover", data, http.StatusOK)
}

// indexerQuickAdd creates a native RSS indexer from a feed URL discovered on a
// site. The legacy source model is gone; the feed is an ordinary indexer of the
// RSS family.
func (s *Server) indexerQuickAdd(w http.ResponseWriter, r *http.Request) {
	if s.svc == nil {
		redirect(w, r, "/indexers", "", s.translate(r, "indexer.error.serviceUnavailable"))
		return
	}
	rawURL := strings.TrimSpace(r.FormValue("url"))
	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		redirect(w, r, "/indexers", "", s.translate(r, "idx.urlInvalid"))
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		name = strings.TrimPrefix(parsed.Hostname(), "www.")
	}
	if name == "" {
		name = "RSS feed"
	}
	if err := s.ensureRSSDefinition(r); err != nil {
		redirect(w, r, "/indexers", "", err.Error())
		return
	}
	existing, _ := s.store.ListIndexers(r.Context())
	names := make(map[string]bool, len(existing))
	for _, in := range existing {
		names[strings.ToLower(in.Name)] = true
	}
	enabled := true
	priority := 25
	if _, err := s.svc.Create(r.Context(), indexersvc.Input{
		DefinitionID: "rss",
		Name:         uniqueIndexerName(name, names),
		Slug:         indexersvc.NormalizeSlug(name),
		Protocol:     "torrent",
		Priority:     &priority,
		Enabled:      &enabled,
		Settings:     map[string]string{"url": rawURL},
	}); err != nil {
		redirect(w, r, "/indexers", "", s.indexerFriendlyError(r, err))
		return
	}
	redirect(w, r, "/indexers", s.translate(r, "idx.feedAdded", name), "")
}

const rssDefinitionBody = `schema: 1
meta:
  id: rss
  name: RSS feed
  type: private
  protocol: torrent
  urls:
    - https://example.invalid/
connector: rss
settings:
  - { name: url, type: text, label: Feed URL, required: true }
  - { name: default-category, type: text, label: Default category }
  - { name: query, type: text, label: Client-side filter }
`

// ensureRSSDefinition installs a small generic RSS definition the first time a
// feed is added from the discovery page.
func (s *Server) ensureRSSDefinition(r *http.Request) error {
	if _, err := s.store.GetDefinition(r.Context(), "rss"); err == nil {
		return nil
	}
	return s.store.UpsertDefinition(r.Context(), store.Definition{
		ID:            "rss",
		SchemaVersion: 1,
		Family:        "rss",
		State:         "bozza",
		Origin:        "local",
		Author:        "ui",
		Body:          rssDefinitionBody,
		Hash:          definition.Fingerprint([]byte(rssDefinitionBody)),
		UpdatedAt:     time.Now().UTC(),
	})
}

func uniqueIndexerName(base string, taken map[string]bool) string {
	base = strings.TrimSpace(base)
	if base == "" {
		base = "indexer"
	}
	if !taken[strings.ToLower(base)] {
		return base
	}
	for suffix := 2; ; suffix++ {
		candidate := fmt.Sprintf("%s (%d)", base, suffix)
		if !taken[strings.ToLower(candidate)] {
			return candidate
		}
	}
}
