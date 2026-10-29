package adminapi

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/buzzqw/indexarr/internal/store"
)

type proxyRequest struct {
	Kind      string `json:"kind"`
	URL       string `json:"url"`
	Username  string `json:"username"`
	Password  string `json:"password"`
	ClearAuth bool   `json:"clear_auth"`
}

type proxyResponse struct {
	ID      int64  `json:"id"`
	Kind    string `json:"kind"`
	URL     string `json:"url"`
	HasAuth bool   `json:"has_auth"`
}

func (s *Server) handleProxyList(w http.ResponseWriter, r *http.Request) {
	proxies, err := s.store.ListProxies(r.Context())
	if err != nil {
		s.logger.Error("adminapi: list proxies", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	out := make([]proxyResponse, 0, len(proxies))
	for _, proxy := range proxies {
		out = append(out, proxyView(proxy))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleProxyGet(w http.ResponseWriter, r *http.Request) {
	id, ok := proxyID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid proxy id")
		return
	}
	proxy, err := s.store.GetProxy(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "proxy not found")
		return
	}
	writeJSON(w, http.StatusOK, proxyView(proxy))
}

func (s *Server) handleProxyCreate(w http.ResponseWriter, r *http.Request) {
	var req proxyRequest
	if !decodeRequest(w, r, &req) {
		return
	}
	kind, cleanURL, blob, err := s.proxyValues(req, false, nil)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	id, err := s.store.CreateProxy(r.Context(), store.Proxy{Kind: kind, URL: cleanURL, SecretsEnc: blob})
	if err != nil {
		s.logger.Error("adminapi: create proxy", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	proxy, err := s.store.GetProxy(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusCreated, proxyView(proxy))
}

func (s *Server) handleProxyUpdate(w http.ResponseWriter, r *http.Request) {
	id, ok := proxyID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid proxy id")
		return
	}
	current, err := s.store.GetProxy(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "proxy not found")
		return
	}
	var req proxyRequest
	if !decodeRequest(w, r, &req) {
		return
	}
	kind, cleanURL, blob, err := s.proxyValues(req, true, &current)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.store.UpdateProxy(r.Context(), store.Proxy{
		ID: id, Kind: kind, URL: cleanURL, SecretsEnc: blob,
	}); err != nil {
		s.logger.Error("adminapi: update proxy", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	proxy, _ := s.store.GetProxy(r.Context(), id)
	writeJSON(w, http.StatusOK, proxyView(proxy))
}

func (s *Server) handleProxyDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := proxyID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid proxy id")
		return
	}
	if err := s.store.DeleteProxy(r.Context(), id); err != nil {
		s.logger.Error("adminapi: delete proxy", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func proxyView(proxy store.Proxy) proxyResponse {
	return proxyResponse{ID: proxy.ID, Kind: proxy.Kind, URL: proxy.URL, HasAuth: len(proxy.SecretsEnc) > 0}
}

func proxyID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(strings.TrimSpace(r.PathValue("id")), 10, 64)
	return id, err == nil && id > 0
}

// proxyValues validates a proxy request and returns the stored kind, the URL
// without credentials and the sealed credential blob. A nil blob keeps the
// current secret on update; clear_auth removes it.
func (s *Server) proxyValues(req proxyRequest, update bool, current *store.Proxy) (string, string, []byte, error) {
	raw := strings.TrimSpace(req.URL)
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" {
		return "", "", nil, errors.New("invalid proxy URL")
	}
	kind := strings.ToLower(parsed.Scheme)
	switch kind {
	case "http", "https", "socks4", "socks4a", "socks5", "socks5h":
	default:
		return "", "", nil, errors.New("proxy scheme must be http, https, socks4, socks4a, socks5 or socks5h")
	}
	credentials := ""
	if parsed.User != nil {
		user := parsed.User.Username()
		pass, _ := parsed.User.Password()
		if pass != "" {
			credentials = user + ":" + pass
		} else {
			credentials = user
		}
		parsed.User = nil
	}
	parsed.Fragment = ""
	if strings.TrimSpace(req.Username) != "" {
		if strings.TrimSpace(req.Password) != "" {
			credentials = strings.TrimSpace(req.Username) + ":" + req.Password
		} else {
			credentials = strings.TrimSpace(req.Username)
		}
	}
	var blob []byte
	if credentials != "" {
		if s.vault == nil {
			return "", "", nil, errors.New("vault not configured")
		}
		sealed, err := s.vault.SealString(credentials)
		if err != nil {
			return "", "", nil, errors.New("cannot seal proxy credentials")
		}
		blob = []byte(sealed)
	} else if update && current != nil && !req.ClearAuth {
		blob = current.SecretsEnc
	}
	if req.ClearAuth {
		blob = nil
	}
	return kind, parsed.String(), blob, nil
}
