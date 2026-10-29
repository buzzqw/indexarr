package web

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/buzzqw/indexarr/internal/store"
)

type proxyRow struct {
	ID       int64
	Kind     string
	URL      string
	HasAuth  bool
	InUse    int
	EditURL  string
	DeleteOK bool
}

type proxyPage struct {
	base
	Rows     []proxyRow
	Edit     *proxyRow
	Kind     string
	URL      string
	Username string
	Kinds    []string
	HasAuth  bool
	Error    string
	Message  string
}

func (s *Server) proxiesPage(w http.ResponseWriter, r *http.Request) {
	data, err := s.buildProxyPage(r)
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	s.render(w, r, "proxies", data, http.StatusOK)
}

func (s *Server) buildProxyPage(r *http.Request) (proxyPage, error) {
	data := proxyPage{base: s.newBaseT(r, "proxies.title", "settings"),
		Kinds: []string{"http", "https", "socks4", "socks4a", "socks5", "socks5h"}}
	proxies, err := s.store.ListProxies(r.Context())
	if err != nil {
		return data, err
	}
	usage, err := s.proxyUsage(r)
	if err != nil {
		return data, err
	}
	for _, proxy := range proxies {
		row := proxyRow{
			ID: proxy.ID, Kind: proxy.Kind, URL: proxy.URL,
			HasAuth: len(proxy.SecretsEnc) > 0, InUse: usage[proxy.ID],
			EditURL: "/proxies?edit=" + strconv.FormatInt(proxy.ID, 10), DeleteOK: true,
		}
		data.Rows = append(data.Rows, row)
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("edit")); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err == nil && id > 0 {
			proxy, err := s.store.GetProxy(r.Context(), id)
			if err == nil {
				data.Edit = &proxyRow{ID: proxy.ID, Kind: proxy.Kind, URL: proxy.URL,
					HasAuth: len(proxy.SecretsEnc) > 0, InUse: usage[proxy.ID]}
				data.Kind = proxy.Kind
				data.URL = proxy.URL
				data.HasAuth = len(proxy.SecretsEnc) > 0
			}
		}
	}
	data.Message = strings.TrimSpace(r.URL.Query().Get("message"))
	return data, nil
}

func (s *Server) proxyUsage(r *http.Request) (map[int64]int, error) {
	out := map[int64]int{}
	indexers, err := s.store.ListIndexers(r.Context())
	if err != nil {
		return nil, err
	}
	for _, indexer := range indexers {
		if indexer.ProxyID != nil {
			out[*indexer.ProxyID]++
		}
	}
	return out, nil
}

type proxyForm struct {
	kind     string
	url      string
	username string
	password string
	clear    bool
}

func proxyFormFromRequest(r *http.Request) proxyForm {
	return proxyForm{
		kind:     strings.ToLower(strings.TrimSpace(r.FormValue("kind"))),
		url:      strings.TrimSpace(r.FormValue("url")),
		username: strings.TrimSpace(r.FormValue("username")),
		password: r.FormValue("password"),
		clear:    r.FormValue("clear_auth") != "",
	}
}

func (s *Server) proxyCreate(w http.ResponseWriter, r *http.Request) {
	form := proxyFormFromRequest(r)
	kind, cleanURL, secrets, err := s.proxyValues(r, form, false)
	if err != nil {
		s.renderProxyError(w, r, err)
		return
	}
	if _, err := s.store.CreateProxy(r.Context(), store.Proxy{Kind: kind, URL: cleanURL, SecretsEnc: secrets}); err != nil {
		s.renderProxyError(w, r, err)
		return
	}
	http.Redirect(w, r, "/proxies?message="+url.QueryEscape(s.translate(r, "proxies.created")), http.StatusSeeOther)
}

func (s *Server) proxyUpdate(w http.ResponseWriter, r *http.Request) {
	id, ok := proxyID(r)
	if !ok {
		s.renderProxyError(w, r, errors.New(s.translate(r, "proxies.notFound")))
		return
	}
	current, err := s.store.GetProxy(r.Context(), id)
	if err != nil {
		s.renderProxyError(w, r, errors.New(s.translate(r, "proxies.notFound")))
		return
	}
	form := proxyFormFromRequest(r)
	kind, cleanURL, secrets, err := s.proxyValues(r, form, true)
	if err != nil {
		s.renderProxyError(w, r, err)
		return
	}
	if secrets == nil {
		secrets = current.SecretsEnc
	}
	if form.clear {
		secrets = nil
	}
	if err := s.store.UpdateProxy(r.Context(), store.Proxy{
		ID: id, Kind: kind, URL: cleanURL, SecretsEnc: secrets,
	}); err != nil {
		s.renderProxyError(w, r, err)
		return
	}
	http.Redirect(w, r, "/proxies?message="+url.QueryEscape(s.translate(r, "proxies.updated")), http.StatusSeeOther)
}

func (s *Server) proxyDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := proxyID(r)
	if !ok {
		s.renderProxyError(w, r, errors.New(s.translate(r, "proxies.notFound")))
		return
	}
	if err := s.store.DeleteProxy(r.Context(), id); err != nil {
		s.renderProxyError(w, r, err)
		return
	}
	http.Redirect(w, r, "/proxies?message="+url.QueryEscape(s.translate(r, "proxies.deleted")), http.StatusSeeOther)
}

func proxyID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(strings.TrimSpace(r.PathValue("id")), 10, 64)
	return id, err == nil && id > 0
}

// proxyValues validates the form and returns the stored kind, the URL without
// credentials and the sealed credential blob. A nil blob means "keep the
// current secret" on update; on create it means "no credentials".
func (s *Server) proxyValues(r *http.Request, form proxyForm, update bool) (string, string, []byte, error) {
	kind, ok := proxyKind(form.url)
	if !ok {
		return "", "", nil, errors.New(s.translate(r, "proxies.error.invalidURL"))
	}
	if form.kind != "" && form.kind != kind {
		// The scheme drives the kind; a mismatched select is a client mistake,
		// not a reason to store an unusable proxy.
		form.kind = kind
	}
	cleanURL, credentials, err := splitProxyCredentials(form.url)
	if err != nil {
		return "", "", nil, errors.New(s.translate(r, "proxies.error.invalidURL"))
	}
	if form.username != "" {
		if form.password != "" {
			credentials = form.username + ":" + form.password
		} else {
			credentials = form.username
		}
	}
	var blob []byte
	if credentials != "" {
		if s.vault == nil {
			return "", "", nil, errors.New(s.translate(r, "proxies.error.vault"))
		}
		sealed, err := s.vault.SealString(credentials)
		if err != nil {
			return "", "", nil, errors.New(s.translate(r, "proxies.error.vault"))
		}
		blob = []byte(sealed)
	} else if update {
		blob = nil
	}
	return kind, cleanURL, blob, nil
}

func (s *Server) renderProxyError(w http.ResponseWriter, r *http.Request, err error) {
	data, buildErr := s.buildProxyPage(r)
	if buildErr != nil {
		s.renderError(w, r, http.StatusInternalServerError, buildErr.Error())
		return
	}
	data.Error = err.Error()
	s.render(w, r, "proxies", data, http.StatusBadRequest)
}
