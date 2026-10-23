package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/buzzqw/indexarr/internal/feed"
	"github.com/buzzqw/indexarr/internal/store"
)

func parseSourceIDs(values []string) []int64 {
	seen := map[int64]bool{}
	var out []int64
	for _, raw := range values {
		id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if err != nil || id <= 0 || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

func (s *Server) sourcesBulk(w http.ResponseWriter, r *http.Request) {
	ids := parseSourceIDs(r.Form["ids"])
	if len(ids) == 0 {
		redirect(w, r, "/sources", "", "Select at least one source")
		return
	}
	action := strings.TrimSpace(r.FormValue("action"))
	var (
		affected int64
		err      error
		message  string
	)
	switch action {
	case "enable", "disable":
		enabled := action == "enable"
		affected, err = s.store.SetSourcesEnabled(r.Context(), ids, enabled)
		message = fmt.Sprintf("%d source(s) %s", affected, map[bool]string{true: "enabled", false: "disabled"}[enabled])
	case "interval":
		interval := intParam(r.FormValue("interval_seconds"), -1)
		if interval != 0 && (interval < 60 || interval > 30*24*3600) {
			redirect(w, r, "/sources", "", "Interval must be 0 (default) or between 60 and 2592000 seconds")
			return
		}
		affected, err = s.store.SetSourcesInterval(r.Context(), ids, interval)
		message = fmt.Sprintf("%d source(s) updated to interval %d", affected, interval)
	case "categories":
		categories, normalizeErr := normalizeCategories(r.FormValue("categories"))
		if normalizeErr != nil {
			redirect(w, r, "/sources", "", normalizeErr.Error())
			return
		}
		affected, err = s.store.SetSourcesCategories(r.Context(), ids, categories)
		message = fmt.Sprintf("%d source(s) updated: categories %q", affected, categories)
	default:
		redirect(w, r, "/sources", "", "Unknown bulk action")
		return
	}
	if err != nil {
		redirect(w, r, "/sources", "", err.Error())
		return
	}
	redirect(w, r, "/sources", message, "")
}

func (s *Server) sourcesBulkDelete(w http.ResponseWriter, r *http.Request) {
	ids := parseSourceIDs(r.Form["ids"])
	if len(ids) == 0 {
		redirect(w, r, "/sources", "", "Select at least one source")
		return
	}
	removed, err := s.store.DeleteSources(r.Context(), ids)
	if err != nil {
		redirect(w, r, "/sources", "", err.Error())
		return
	}
	redirect(w, r, "/sources", fmt.Sprintf("%d source(s) deleted; their releases stay in the cache", removed), "")
}

func (s *Server) sourceClone(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		redirect(w, r, "/sources", "", "invalid source id")
		return
	}
	original, err := s.store.GetSource(r.Context(), id)
	if err != nil {
		redirect(w, r, "/sources", "", "source not found")
		return
	}
	sources, err := s.store.ListSources(r.Context())
	if err != nil {
		redirect(w, r, "/sources", "", err.Error())
		return
	}
	names := make(map[string]bool, len(sources))
	for _, source := range sources {
		names[strings.ToLower(source.Name)] = true
	}
	clone := original
	clone.ID = 0
	clone.Name = uniqueSourceName(original.Name+" copy", names)
	clone.Enabled = false
	newID, err := s.store.CreateSource(r.Context(), clone)
	if err != nil {
		redirect(w, r, "/sources", "", friendlySourceError(err).Error())
		return
	}
	redirect(w, r, fmt.Sprintf("/sources/%d/edit", newID), "Source cloned (disabled)", "")
}

type sourceJSON struct {
	Name            string `json:"name"`
	Kind            string `json:"kind"`
	Manager         string `json:"manager"`
	URL             string `json:"url"`
	APIKey          string `json:"api_key"`
	Username        string `json:"username"`
	Password        string `json:"password"`
	Categories      string `json:"categories"`
	DefaultCategory string `json:"default_category"`
	Query           string `json:"query"`
	Enabled         bool   `json:"enabled"`
	IntervalSeconds int    `json:"interval_seconds"`
}

type sourcesJSON struct {
	Version int          `json:"version"`
	Sources []sourceJSON `json:"sources"`
}

func (s *Server) sourcesExport(w http.ResponseWriter, r *http.Request) {
	sources, err := s.store.ListSources(r.Context())
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	payload := sourcesJSON{Version: 1, Sources: make([]sourceJSON, 0, len(sources))}
	for _, source := range sources {
		payload.Sources = append(payload.Sources, sourceJSON{
			Name:            source.Name,
			Kind:            source.Kind,
			Manager:         source.Manager,
			URL:             source.URL,
			APIKey:          source.APIKey,
			Username:        source.Username,
			Password:        source.Password,
			Categories:      source.Categories,
			DefaultCategory: source.DefaultCategory,
			Query:           source.Query,
			Enabled:         source.Enabled,
			IntervalSeconds: source.IntervalSeconds,
		})
	}
	body, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	body = append(body, '\n')
	filename := "indexarr-sources-" + time.Now().Format("20060102-150405") + ".json"
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(body)
}

func (s *Server) sourcesImport(w http.ResponseWriter, r *http.Request) {
	raw := strings.TrimSpace(r.FormValue("json"))
	if raw == "" {
		redirect(w, r, "/sources", "", "Paste the sources JSON to import")
		return
	}
	var payload sourcesJSON
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		redirect(w, r, "/sources", "", "Invalid sources JSON: "+err.Error())
		return
	}
	if payload.Version != 0 && payload.Version != 1 {
		redirect(w, r, "/sources", "", fmt.Sprintf("Unsupported sources JSON version %d", payload.Version))
		return
	}
	if len(payload.Sources) == 0 {
		redirect(w, r, "/sources", "", "The JSON contains no sources")
		return
	}
	existing, err := s.store.ListSources(r.Context())
	if err != nil {
		redirect(w, r, "/sources", "", err.Error())
		return
	}
	names := make(map[string]bool, len(existing))
	urls := make(map[string]bool, len(existing))
	for _, source := range existing {
		names[strings.ToLower(source.Name)] = true
		urls[strings.TrimRight(source.URL, "/")] = true
	}

	imported, skipped, invalid := 0, 0, 0
	for index, entry := range payload.Sources {
		source, err := sourceFromJSON(entry)
		if err != nil {
			invalid++
			s.log.Warn("source import skipped", "component", "indexarr", "subsystem", "sources", "index", index, "name", entry.Name, "error", err)
			continue
		}
		if names[strings.ToLower(source.Name)] || urls[strings.TrimRight(source.URL, "/")] {
			skipped++
			continue
		}
		if _, err := s.store.CreateSource(r.Context(), source); err != nil {
			invalid++
			s.log.Warn("source import failed", "component", "indexarr", "subsystem", "sources", "index", index, "name", source.Name, "error", err)
			continue
		}
		names[strings.ToLower(source.Name)] = true
		urls[strings.TrimRight(source.URL, "/")] = true
		imported++
	}
	message := fmt.Sprintf("%d source(s) imported", imported)
	if skipped > 0 {
		message += fmt.Sprintf(", %d already present", skipped)
	}
	if invalid > 0 {
		message += fmt.Sprintf(", %d invalid (see Logs)", invalid)
	}
	redirect(w, r, "/sources", message, "")
}

func sourceFromJSON(entry sourceJSON) (store.Source, error) {
	source := store.Source{
		Name:            strings.TrimSpace(entry.Name),
		Kind:            strings.ToLower(strings.TrimSpace(entry.Kind)),
		Manager:         strings.ToLower(strings.TrimSpace(entry.Manager)),
		URL:             strings.TrimSpace(entry.URL),
		APIKey:          strings.TrimSpace(entry.APIKey),
		Username:        strings.TrimSpace(entry.Username),
		Password:        entry.Password,
		Categories:      strings.TrimSpace(entry.Categories),
		DefaultCategory: strings.TrimSpace(entry.DefaultCategory),
		Query:           strings.TrimSpace(entry.Query),
		Enabled:         entry.Enabled,
		IntervalSeconds: entry.IntervalSeconds,
	}
	if source.Name == "" || len(source.Name) > 100 {
		return source, fmt.Errorf("name is required (max 100 characters)")
	}
	if source.Kind == "" {
		source.Kind = "torznab"
	}
	if source.Kind != "torznab" && source.Kind != "rss" {
		return source, fmt.Errorf("kind must be torznab or rss")
	}
	if source.Manager == "" {
		source.Manager = feed.ManagerAuto
	}
	switch source.Manager {
	case feed.ManagerAuto, feed.ManagerJackett, feed.ManagerProwlarr, feed.ManagerMirCrew, feed.ManagerTorznab:
	default:
		return source, fmt.Errorf("invalid manager %q", source.Manager)
	}
	parsed, err := url.Parse(source.URL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return source, fmt.Errorf("URL must be an absolute http:// or https:// address")
	}
	if source.IntervalSeconds != 0 && (source.IntervalSeconds < 60 || source.IntervalSeconds > 30*24*3600) {
		return source, fmt.Errorf("interval must be 0 or between 60 and 2592000 seconds")
	}
	categories, err := normalizeCategories(source.Categories)
	if err != nil {
		return source, err
	}
	source.Categories = categories
	if source.DefaultCategory != "" {
		if _, err := strconv.Atoi(source.DefaultCategory); err != nil ||
			len(source.DefaultCategory) < 4 || len(source.DefaultCategory) > 6 {
			return source, fmt.Errorf("default_category must be a numeric Torznab id")
		}
	}
	if source.Kind == "rss" {
		source.Manager = feed.ManagerAuto
		source.Categories = ""
		source.Query = ""
	}
	return source, nil
}
