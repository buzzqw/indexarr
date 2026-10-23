package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/buzzqw/indexarr/internal/feed"
	"github.com/buzzqw/indexarr/internal/store"
)

func (s *Server) sourcesQuickAdd(w http.ResponseWriter, r *http.Request) {
	rawURL := strings.TrimSpace(r.FormValue("url"))
	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		redirect(w, r, "/sources", "", "URL must be an absolute http:// or https:// address")
		return
	}
	kind := strings.ToLower(strings.TrimSpace(r.FormValue("kind")))
	if kind == "" || kind == "auto" {
		kind = feed.SuggestKind(rawURL)
	}
	if kind != "rss" && kind != "torznab" {
		redirect(w, r, "/sources", "", "Kind must be auto, rss or torznab")
		return
	}
	existing, err := s.store.ListSources(r.Context())
	if err != nil {
		redirect(w, r, "/sources", "", err.Error())
		return
	}
	names := make(map[string]bool, len(existing))
	for _, source := range existing {
		names[strings.ToLower(source.Name)] = true
		if strings.TrimRight(source.URL, "/") == strings.TrimRight(rawURL, "/") {
			redirect(w, r, "/sources", "", "This URL is already configured as "+source.Name)
			return
		}
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		name = strings.TrimPrefix(parsed.Hostname(), "www.")
		if name == "" {
			name = "source"
		}
	}
	name = uniqueSourceName(name, names)
	manager := feed.ManagerAuto
	source := store.Source{
		Name:    name,
		Kind:    kind,
		Manager: manager,
		URL:     rawURL,
		APIKey:  strings.TrimSpace(r.FormValue("api_key")),
		Enabled: true,
	}
	id, err := s.store.CreateSource(r.Context(), source)
	if err != nil {
		redirect(w, r, "/sources", "", friendlySourceError(err).Error())
		return
	}
	s.log.Info("source quickly added", "source_id", id, "name", name, "kind", kind)
	message := "Source added: " + name
	if s.engine != nil {
		if err := s.engine.Trigger(id); err == nil {
			message += " · first run queued"
		}
	}
	redirect(w, r, "/sources", message, "")
}

type discoverPage struct {
	base
	SourceURL string
	Feeds     []feed.DiscoveredFeed
}

func (s *Server) sourcesDiscover(w http.ResponseWriter, r *http.Request) {
	rawURL := strings.TrimSpace(r.URL.Query().Get("url"))
	if rawURL == "" {
		redirect(w, r, "/sources", "", "Enter a site URL to discover feeds")
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
		base:      s.newBase(r, "Feed discovery", "sources"),
		SourceURL: rawURL,
		Feeds:     feeds,
	}
	if err != nil {
		data.Error = err.Error()
		s.log.Warn("feed discovery failed", "component", "rss", "subsystem", "discovery", "url", rawURL, "error", err)
	} else {
		s.log.Info("feed discovery done", "url", rawURL, "feeds", len(feeds))
	}
	s.render(w, r, "discover", data, http.StatusOK)
}
