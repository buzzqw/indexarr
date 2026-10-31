package web

import (
	"net"
	"net/http"
	"strconv"
	"strings"
)

// hostguard.go enforces the optional Trusted hosts allowlist. Links to
// downloads and to the public address are built from the request Host header,
// so a request with a forged Host could otherwise poison what the interface
// hands to a client. The list is empty by default: an installation that never
// sets it behaves exactly as before. Loopback is always accepted so a local
// health check or a container probe keeps working.

// splitTrustedHosts reads the comma/space separated allowlist.
func splitTrustedHosts(raw string) []string {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == '\r'
	})
	out := make([]string, 0, len(fields))
	for _, field := range fields {
		if field = strings.TrimSpace(field); field != "" {
			out = append(out, strings.ToLower(field))
		}
	}
	return out
}

// normalizeTrustedHosts validates and canonicalizes the allowlist typed in the
// settings page. An entry is a hostname, an IP address and optionally a port;
// a scheme, a path, a user or a query is refused instead of being stored as a
// value that can never match.
func normalizeTrustedHosts(raw string) (string, bool) {
	entries := splitTrustedHosts(raw)
	out := make([]string, 0, len(entries))
	seen := make(map[string]bool, len(entries))
	for _, entry := range entries {
		normalized, ok := normalizeHostEntry(entry)
		if !ok {
			return "", false
		}
		if !seen[normalized] {
			seen[normalized] = true
			out = append(out, normalized)
		}
	}
	return strings.Join(out, ", "), true
}

func normalizeHostEntry(entry string) (string, bool) {
	entry = strings.ToLower(strings.TrimSpace(entry))
	entry = strings.TrimSuffix(entry, ".")
	if entry == "" || strings.ContainsAny(entry, `/\?#@ `) || strings.Contains(entry, "://") {
		return "", false
	}
	host, port, hasPort := splitHostPortLoose(entry)
	if port != "" {
		value, err := strconv.Atoi(port)
		if err != nil || value < 1 || value > 65535 {
			return "", false
		}
	}
	if net.ParseIP(host) != nil {
		if hasPort {
			return net.JoinHostPort(host, port), true
		}
		return host, true
	}
	if !validHostname(host) {
		return "", false
	}
	if hasPort {
		return host + ":" + port, true
	}
	return host, true
}

func validHostname(host string) bool {
	if host == "" || len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') && r != '-' {
				return false
			}
		}
	}
	return true
}

// splitHostPortLoose separates the port when the value carries one. A bare
// IPv6 address has no port and is returned whole.
func splitHostPortLoose(raw string) (host, port string, hasPort bool) {
	raw = strings.TrimSpace(strings.ToLower(raw))
	if raw == "" {
		return "", "", false
	}
	if strings.HasPrefix(raw, "[") {
		if h, p, err := net.SplitHostPort(raw); err == nil {
			return strings.Trim(h, "[]"), p, true
		}
		return strings.Trim(raw, "[]"), "", false
	}
	if strings.Count(raw, ":") == 1 {
		if h, p, err := net.SplitHostPort(raw); err == nil {
			return h, p, true
		}
	}
	// No port (or a bare IPv6 literal, which has several colons).
	return strings.Trim(raw, "[]"), "", false
}

// hostAllowed reports whether host (the value of r.Host, with or without a
// port) matches one entry of the allowlist. An entry without a port matches
// any port of that hostname; an entry with a port matches only that one.
func hostAllowed(host string, allowed []string) bool {
	host, port, _ := splitHostPortLoose(host)
	if host == "" {
		return false
	}
	if isLoopbackHost(host) {
		return true
	}
	for _, entry := range allowed {
		entryHost, entryPort, _ := splitHostPortLoose(entry)
		if !strings.EqualFold(entryHost, host) {
			continue
		}
		if entryPort == "" || entryPort == port {
			return true
		}
	}
	return false
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

// trustedHosts is the middleware that applies the allowlist. Static assets and
// the service worker carry no link and stay reachable, so a wrong entry cannot
// leave the browser without the CSS needed to fix it; every other route,
// including health, Torznab and the admin API, is checked.
func (s *Server) trustedHosts(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/sw.js" || strings.HasPrefix(r.URL.Path, "/static/") {
			next.ServeHTTP(w, r)
			return
		}
		settings, err := s.store.LoadSettings(r.Context())
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		allowed := splitTrustedHosts(settings.TrustedHosts)
		if len(allowed) == 0 {
			next.ServeHTTP(w, r)
			return
		}
		if !hostAllowed(r.Host, allowed) {
			http.Error(w, "host not allowed", http.StatusBadRequest)
			return
		}
		// A reverse proxy may forward the original host separately. Every
		// value must be allowed too, otherwise a client could smuggle a
		// different host through the forwarded header.
		for _, forwarded := range strings.Split(r.Header.Get("X-Forwarded-Host"), ",") {
			if forwarded = strings.TrimSpace(forwarded); forwarded != "" && !hostAllowed(forwarded, allowed) {
				http.Error(w, "host not allowed", http.StatusBadRequest)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
