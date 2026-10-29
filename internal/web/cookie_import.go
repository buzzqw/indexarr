package web

// Browser-cookie import: indexarr cannot read another site's cookies from a
// web page (they are HttpOnly and belong to a different origin). This file
// implements the one-command path: the indexer page shows a short-lived token
// and a command; the user runs it on the machine that has the browser, a small
// Python helper reads the local Firefox profile and posts the Cookie header
// here. The token is one-time, in memory and scoped to one indexer; the API
// key and the UI password never leave indexarr.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/buzzqw/indexarr/internal/cookiepair"
	"github.com/buzzqw/indexarr/internal/definition"
	"github.com/buzzqw/indexarr/internal/indexersvc"
	"github.com/buzzqw/indexarr/internal/store"
)

const (
	browserCookieMaxBody = 64 << 10
	browserCookieMaxSize = 16 << 10
	browserCookieMaxUA   = 512
)

type cookieImportView struct {
	base
	Indexer    store.Indexer
	Domain     string
	ScriptURL  string
	Command    string
	HasCommand bool
	Error      string
}

func (s *Server) indexerCookiePage(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.renderError(w, r, http.StatusBadRequest, s.translate(r, "indexer.error.invalidID"))
		return
	}
	in, def, err := s.cookieImportIndexer(r, id)
	if err != nil {
		s.renderError(w, r, statusForIndexerError(err), s.indexerErrorMessage(r, err))
		return
	}
	s.render(w, r, "cookie_import", s.buildCookieImportView(r, in, def), http.StatusOK)
}

func (s *Server) indexerCookieCommand(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.renderError(w, r, http.StatusBadRequest, s.translate(r, "indexer.error.invalidID"))
		return
	}
	in, def, err := s.cookieImportIndexer(r, id)
	if err != nil {
		s.renderError(w, r, statusForIndexerError(err), s.indexerErrorMessage(r, err))
		return
	}
	view := s.buildCookieImportView(r, in, def)
	if view.Error != "" {
		s.render(w, r, "cookie_import", view, http.StatusBadRequest)
		return
	}
	token, _, err := cookiepair.Issue(id)
	if err != nil {
		s.log.Error("browser cookie: issue token", "component", "indexarr", "subsystem", "web", "indexer_id", id, "error", err)
		view.Error = s.translate(r, "idx.cookie.tokenFailed")
		s.render(w, r, "cookie_import", view, http.StatusInternalServerError)
		return
	}
	base := s.cookieImportBaseURL(r)
	view.Command = fmt.Sprintf(
		"curl -fsSL %s -o /tmp/import-browser-cookie.py && python3 /tmp/import-browser-cookie.py --indexarr %s --token %s --domain %s",
		shellQuote(base+"/static/import-browser-cookie.py"),
		shellQuote(base),
		shellQuote(token),
		shellQuote(view.Domain),
	)
	view.HasCommand = true
	s.render(w, r, "cookie_import", view, http.StatusOK)
}

// cookieImportIndexer loads the indexer and refuses the feature for a
// definition that has no `cookie` setting: the import would store a secret
// nothing reads.
func (s *Server) cookieImportIndexer(r *http.Request, id int64) (store.Indexer, *definition.Definition, error) {
	in, err := s.store.GetIndexer(r.Context(), id)
	if err != nil {
		return store.Indexer{}, nil, err
	}
	def, _ := s.parsedDefinition(r.Context(), in.DefinitionID)
	return in, def, nil
}

func (s *Server) buildCookieImportView(r *http.Request, in store.Indexer, def *definition.Definition) cookieImportView {
	view := cookieImportView{
		base:    s.newBase(r, in.Name, "indexers"),
		Indexer: in,
	}
	if !definitionHasCookie(def) {
		view.Error = s.translate(r, "idx.cookie.notSupported")
		return view
	}
	view.Domain = cookieImportDomain(in, def)
	if view.Domain == "" {
		view.Error = s.translate(r, "idx.cookie.noDomain")
		return view
	}
	view.ScriptURL = s.cookieImportBaseURL(r) + "/static/import-browser-cookie.py"
	return view
}

func definitionHasCookie(def *definition.Definition) bool {
	if def == nil {
		return false
	}
	for _, setting := range def.Settings {
		if strings.EqualFold(strings.TrimSpace(setting.Name), "cookie") {
			return true
		}
	}
	return false
}

// cookieImportDomain is the cookie domain the helper searches for: the host
// of the indexer's address without a leading www., so `www.example.org` still
// finds the `example.org` cookies the browser stores.
func cookieImportDomain(in store.Indexer, def *definition.Definition) string {
	config := publicConfigMap(in.ConfigJSON)
	address := strings.TrimSpace(config["url"])
	if address == "" {
		address = strings.TrimSpace(config["site"])
	}
	if address == "" {
		address = strings.TrimSpace(config["base_url"])
	}
	if address == "" && def != nil && len(def.Meta.URLs) > 0 {
		address = strings.TrimSpace(def.Meta.URLs[0])
	}
	if address == "" {
		return ""
	}
	parsed, err := url.Parse(address)
	if err != nil {
		return ""
	}
	host := strings.ToLower(strings.TrimSpace(parsed.Hostname()))
	host = strings.TrimPrefix(host, "www.")
	return host
}

// cookieImportBaseURL is the address the browser machine must call. The
// public_url setting wins (reverse proxy); otherwise the request's own Host
// and scheme are used, and the UI is normally opened on the LAN address.
func (s *Server) cookieImportBaseURL(r *http.Request) string {
	if settings, err := s.store.LoadSettings(r.Context()); err == nil && strings.TrimSpace(settings.PublicURL) != "" {
		return strings.TrimRight(strings.TrimSpace(settings.PublicURL), "/")
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// browserCookieImport is the unauthenticated (token-only) endpoint the helper
// calls. The token is one-time and scoped to one indexer, so no API key or UI
// password travels with the command.
func (s *Server) browserCookieImport(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimSpace(r.Header.Get("X-Indexarr-Pairing"))
	if token == "" {
		pairJSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": "missing pairing token"})
		return
	}
	indexerID, ok := cookiepair.Consume(token)
	if !ok {
		pairJSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": "the pairing token is invalid or expired"})
		return
	}
	if s.svc == nil {
		pairJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "the indexer service is unavailable"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, browserCookieMaxBody)
	var input struct {
		Cookie    string `json:"cookie"`
		UserAgent string `json:"user_agent"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		pairJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid request body"})
		return
	}
	cookie := strings.TrimSpace(input.Cookie)
	cookie = strings.TrimSpace(strings.TrimPrefix(cookie, "Cookie:"))
	cookie = strings.TrimSpace(strings.TrimPrefix(cookie, "cookie:"))
	cookie = strings.ReplaceAll(cookie, "\r", " ")
	cookie = strings.ReplaceAll(cookie, "\n", " ")
	userAgent := strings.TrimSpace(input.UserAgent)
	if cookie == "" || len(cookie) > browserCookieMaxSize {
		pairJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "the cookie is empty or too long"})
		return
	}
	if userAgent == "" || len(userAgent) > browserCookieMaxUA {
		pairJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "the User-Agent is missing or too long"})
		return
	}
	if _, err := s.svc.Update(r.Context(), indexerID, indexersvc.Input{
		Settings: map[string]string{"cookie": cookie, "user_agent": userAgent},
	}); err != nil {
		s.log.Error("browser cookie: store the cookie", "component", "indexarr", "subsystem", "web", "indexer_id", indexerID, "error", err)
		pairJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "cannot store the cookie"})
		return
	}
	s.log.Info("browser cookie imported", "component", "indexarr", "subsystem", "web", "indexer_id", indexerID)
	pairJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func pairJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
