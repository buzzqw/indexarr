package simmock

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

const (
	trackerCSRF    = "synthetic-csrf-token"
	trackerCookie  = "demo-session"
	trackerCookieN = "session"
)

func (s *Server) handleTrackerLoginPage(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(trackerCookieN); err == nil && cookie.Value == trackerCookie {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!doctype html><html><body><p class="ok">already logged in</p><a href="/tracker/search">search</a></body></html>`))
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(`<!doctype html>
<html><head><title>Sim Tracker login</title></head><body>
<h1>Sim Tracker</h1>
<form method="post" action="/tracker/login">
  <input type="hidden" name="csrf_token" value="` + trackerCSRF + `">
  <label>Username <input type="text" name="username" value=""></label>
  <label>Password <input type="password" name="password" value=""></label>
  <button type="submit">Login</button>
</form>
</body></html>
`))
}

func (s *Server) handleTrackerLogin(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	if r.FormValue("csrf_token") != trackerCSRF {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`<!doctype html><html><body><div class="error">invalid csrf token</div></body></html>`))
		return
	}
	if strings.TrimSpace(r.FormValue("username")) == "" || strings.TrimSpace(r.FormValue("password")) == "" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`<!doctype html><html><body><div class="error">missing credentials</div></body></html>`))
		return
	}

	s.mu.Lock()
	s.state.TrackerLogins++
	s.mu.Unlock()

	http.SetCookie(w, &http.Cookie{
		Name:     trackerCookieN,
		Value:    trackerCookie,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(`<!doctype html><html><body><p class="ok">logged in</p><a href="/tracker/search">search</a></body></html>`))
}

func (s *Server) trackerAuthenticated(r *http.Request) bool {
	cookie, err := r.Cookie(trackerCookieN)
	return err == nil && cookie.Value == trackerCookie
}

func (s *Server) handleTrackerSearch(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if !s.trackerAuthenticated(r) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`<!doctype html><html><body><div class="error">login required</div></body></html>`))
		return
	}

	_, _ = w.Write([]byte(`<!doctype html>
<html><head><title>Sim Tracker search</title></head><body>
<h1>Results for "demo"</h1>
<table class="torrents">
  <thead><tr><th>Name</th><th>Size</th><th>Seeders</th><th>Leechers</th><th>Date</th><th>Category</th></tr></thead>
  <tbody>
    <tr>
      <td class="name"><a class="title" href="/tracker/details/1">The.Simpsons.S01E01.1080p.WEB-DL</a>
        <a class="download" href="/tracker/download/1.torrent">download</a></td>
      <td class="size">1.5 GB</td>
      <td class="seeders">42</td>
      <td class="leechers">5</td>
      <td class="date">2026-10-20 12:00</td>
      <td class="category"><a class="cat" href="/tracker/search?cat=5">TV</a></td>
    </tr>
    <tr>
      <td class="name"><a class="title" href="/tracker/details/2">Sim.Demo.Movie.2026.1080p.BluRay.x264</a>
        <a class="download" href="/tracker/download/2.torrent">download</a></td>
      <td class="size">8.0 GB</td>
      <td class="seeders">17</td>
      <td class="leechers">2</td>
      <td class="date">2026-10-19 12:00</td>
      <td class="category"><a class="cat" href="/tracker/search?cat=12">Movies</a></td>
    </tr>
  </tbody>
</table>
</body></html>
`))
}

func (s *Server) handleTrackerDownload(w http.ResponseWriter, r *http.Request) {
	if !s.trackerAuthenticated(r) {
		http.Error(w, "login required", http.StatusUnauthorized)
		return
	}
	file := r.PathValue("file")
	id := strings.TrimSuffix(file, ".torrent")
	n, err := strconv.Atoi(id)
	if err != nil || n < 1 || n > 2 {
		http.NotFound(w, r)
		return
	}

	s.mu.Lock()
	s.state.TrackerDownloads = append(s.state.TrackerDownloads, file)
	s.mu.Unlock()

	rel := s.releases[n-1]
	w.Header().Set("Content-Type", "application/x-bittorrent")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.torrent"`, rel.Title))
	w.Header().Set("Content-Length", strconv.Itoa(len(rel.Torrent)))
	_, _ = w.Write(rel.Torrent)
}
