package engine

import (
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// Session is the in-memory state of one definition connector: cookies captured
// from the site plus named values extracted during login (CSRF tokens, nonces,
// session ids). It is safe for concurrent use.
type Session struct {
	mu       sync.RWMutex
	cookies  map[string]map[string]*http.Cookie // domain -> "name|path" -> cookie
	values   map[string]string
	loggedIn bool
	loginAt  time.Time
}

func newSession() *Session {
	return &Session{
		cookies: make(map[string]map[string]*http.Cookie),
		values:  make(map[string]string),
	}
}

// Cookies returns the cookies that apply to u as an HTTP Cookie header value.
func (s *Session) Cookies(u *url.URL) string {
	if s == nil || u == nil {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	now := time.Now()
	host := strings.ToLower(u.Hostname())
	path := u.EscapedPath()
	if path == "" {
		path = "/"
	}

	selected := make(map[string]*http.Cookie)
	// Process less specific domains and paths first so that the most specific
	// cookie wins on a name collision, as a browser jar would.
	domains := make([]string, 0, len(s.cookies))
	for domain := range s.cookies {
		if cookieDomainMatches(host, domain) {
			domains = append(domains, domain)
		}
	}
	sort.Slice(domains, func(i, j int) bool {
		return len(strings.TrimPrefix(domains[i], ".")) < len(strings.TrimPrefix(domains[j], "."))
	})
	for _, domain := range domains {
		jar := s.cookies[domain]
		keys := make([]string, 0, len(jar))
		for key := range jar {
			keys = append(keys, key)
		}
		sort.Slice(keys, func(i, j int) bool {
			return len(jar[keys[i]].Path) < len(jar[keys[j]].Path)
		})
		for _, key := range keys {
			cookie := jar[key]
			if !cookie.Expires.IsZero() && cookie.Expires.Before(now) {
				continue
			}
			if cookie.Path != "" && !strings.HasPrefix(path, cookie.Path) {
				continue
			}
			selected[cookie.Name] = cookie
		}
	}
	if len(selected) == 0 {
		return ""
	}

	names := make([]string, 0, len(selected))
	for name := range selected {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, (&http.Cookie{Name: name, Value: selected[name].Value}).String())
	}
	return strings.Join(parts, "; ")
}

// Capture stores every Set-Cookie header from an HTTP response for u.
func (s *Session) Capture(u *url.URL, header http.Header) {
	if s == nil || u == nil || header == nil {
		return
	}
	response := &http.Response{Header: header}
	cookies := response.Cookies()
	if len(cookies) == 0 {
		return
	}

	host := strings.ToLower(u.Hostname())
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, cookie := range cookies {
		if cookie == nil || cookie.Name == "" {
			continue
		}
		domain := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(cookie.Domain), "."))
		if domain == "" {
			domain = host
		}
		if !strings.HasPrefix(domain, ".") {
			domain = "." + domain
		}
		jar := s.cookies[domain]
		if jar == nil {
			jar = make(map[string]*http.Cookie)
			s.cookies[domain] = jar
		}
		key := cookie.Name + "|" + cookie.Path
		if cookie.MaxAge < 0 {
			delete(jar, key)
			continue
		}
		cp := *cookie
		jar[key] = &cp
	}
}

func cookieDomainMatches(host, domain string) bool {
	domain = strings.TrimPrefix(domain, ".")
	return host == domain || strings.HasSuffix(host, "."+domain)
}

// SetValue stores a value extracted during login.
func (s *Session) SetValue(name, value string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.values[name] = value
	s.mu.Unlock()
}

// Value returns a value extracted during login.
func (s *Session) Value(name string) (string, bool) {
	if s == nil {
		return "", false
	}
	s.mu.RLock()
	value, ok := s.values[name]
	s.mu.RUnlock()
	return value, ok
}

// Values returns a copy of all extracted values.
func (s *Session) Values() map[string]string {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]string, len(s.values))
	for key, value := range s.values {
		out[key] = value
	}
	return out
}

func (s *Session) markLoggedIn(now time.Time) {
	s.mu.Lock()
	s.loggedIn = true
	s.loginAt = now
	s.mu.Unlock()
}

// loginStamp is the time of the current login, zero when logged out.
func (s *Session) loginStamp() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.loginAt
}

func (s *Session) invalidate() {
	s.mu.Lock()
	s.loggedIn = false
	s.loginAt = time.Time{}
	s.mu.Unlock()
}

func (s *Session) loginValid(now time.Time, refresh time.Duration) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.loggedIn {
		return false
	}
	if refresh <= 0 {
		return true
	}
	return now.Sub(s.loginAt) < refresh
}
