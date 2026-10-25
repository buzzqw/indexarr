package web

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/buzzqw/indexarr/internal/store"
)

// proxyOption is one row of the indexer form's proxy selector. The URL shown
// never contains credentials: they live sealed in the vault.
type proxyOption struct {
	ID    int64
	Label string
}

func (s *Server) proxyOptions(r *http.Request) ([]proxyOption, error) {
	if s.store == nil {
		return nil, nil
	}
	proxies, err := s.store.ListProxies(r.Context())
	if err != nil {
		return nil, err
	}
	out := make([]proxyOption, 0, len(proxies))
	for _, proxy := range proxies {
		out = append(out, proxyOption{ID: proxy.ID, Label: proxyLabel(proxy)})
	}
	return out, nil
}

func proxyLabel(proxy store.Proxy) string {
	label := strings.TrimSpace(proxy.Kind)
	if label == "" {
		label = "proxy"
	}
	parsed, err := url.Parse(strings.TrimSpace(proxy.URL))
	if err == nil && parsed.Host != "" {
		label += " · " + parsed.Host
	} else if raw := strings.TrimSpace(proxy.URL); raw != "" {
		label += " · " + raw
	}
	return label
}

// proxyKind validates the scheme and returns the stored kind. SOCKS4 is not
// supported by the Go transport used by the engine; HTTP, HTTPS, SOCKS5 and
// SOCKS5H are.
func proxyKind(raw string) (string, bool) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Hostname() == "" {
		return "", false
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https", "socks5", "socks5h":
		return strings.ToLower(parsed.Scheme), true
	default:
		return "", false
	}
}

// splitProxyCredentials removes userinfo from a proxy URL and returns the
// sealed value the engine expects ("user:password").
func splitProxyCredentials(raw string) (cleanURL, credentials string, err error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Hostname() == "" {
		return "", "", errors.New("invalid proxy URL")
	}
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
	return parsed.String(), credentials, nil
}
