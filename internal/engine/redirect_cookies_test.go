package engine

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/buzzqw/indexarr/internal/definition"
)

// TestTransportRedirectAppliesTheCookiesItSets covers a login answered with a
// 302: the redirect target must receive the cookies the 302 set, not the ones
// of the first request, and a cookie the site expires must be dropped.
func TestTransportRedirectAppliesTheCookiesItSets(t *testing.T) {
	cookies := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login":
			http.SetCookie(w, &http.Cookie{Name: "sid", Value: "user", Path: "/"})
			http.SetCookie(w, &http.Cookie{Name: "guest", Value: "", Path: "/", MaxAge: -1})
			http.Redirect(w, r, "/step", http.StatusFound)
		case "/step":
			http.SetCookie(w, &http.Cookie{Name: "k", Value: "remember", Path: "/"})
			http.Redirect(w, r, "/index", http.StatusFound)
		default:
			cookies <- r.Header.Get("Cookie")
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	transport := newLocalTransport(TransportConfig{})
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, server.URL+"/login", nil)
	req.Header.Set("Cookie", "sid=anonymous; lang=it; guest=1")

	response, err := transport.Do(context.Background(), "idx", req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if got, want := <-cookies, "sid=user; lang=it; k=remember"; got != want {
		t.Errorf("Cookie on the redirect target = %q, want %q", got, want)
	}
	if len(response.Redirects) != 2 {
		t.Fatalf("Redirects = %d, want 2", len(response.Redirects))
	}
	if path := response.Redirects[0].URL.Path; path != "/login" {
		t.Errorf("first hop = %q, want /login", path)
	}
	if path := response.Redirects[1].URL.Path; path != "/step" {
		t.Errorf("second hop = %q, want /step", path)
	}
}

// TestFormLoginFollowsARedirectWithTheNewSession is the phpBB case measured
// on ilDraGoNeRo: the login POST answers with a 302 to index.php and sets the
// session cookie on the 302. The final page must be the logged-in one and the
// following search must carry the new session.
func TestFormLoginFollowsARedirectWithTheNewSession(t *testing.T) {
	loggedIn := func(r *http.Request) bool {
		cookie, err := r.Cookie("sid")
		return err == nil && cookie.Value == "user"
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/ucp.php", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			http.SetCookie(w, &http.Cookie{Name: "sid", Value: "user", Path: "/"})
			http.Redirect(w, r, "/index.php", http.StatusFound)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "sid", Value: "guest", Path: "/"})
		_, _ = w.Write([]byte(`<html><body><form method="post" action="/ucp.php">
<input type="hidden" name="form_token" value="synthetic-token"></form></body></html>`))
	})
	mux.HandleFunc("/index.php", func(w http.ResponseWriter, r *http.Request) {
		if loggedIn(r) {
			_, _ = w.Write([]byte(`<html><body><a href="./ucp.php?mode=logout">Esci</a></body></html>`))
			return
		}
		_, _ = w.Write([]byte(`<html><body><a href="./ucp.php?mode=login">Login</a></body></html>`))
	})
	mux.HandleFunc("/search.php", func(w http.ResponseWriter, r *http.Request) {
		if !loggedIn(r) {
			_, _ = w.Write([]byte(`<html><body><table class="results"><tbody></tbody></table></body></html>`))
			return
		}
		_, _ = w.Write([]byte(`<html><body><a href="./ucp.php?mode=logout">Esci</a><table class="results"><tbody>
<tr><td><a class="title" href="/viewtopic.php?t=1">Show S01 1080p</a></td></tr>
</tbody></table></body></html>`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	def, err := definition.Parse([]byte(`schema: 1
meta:
  id: redirect-login-test
  name: Redirect login test
  type: private
  protocol: torrent
  urls: ["` + server.URL + `/"]
capabilities:
  search: [q]
settings:
  - { name: username, type: text, label: Username }
  - { name: password, type: password, label: Password }
auth:
  strategy: form
  steps:
    - get: /ucp.php?mode=login
      extract:
        form_token: { selector: 'input[name="form_token"]', attr: value }
    - post: /ucp.php?mode=login
      form:
        username: "{{ settings.username }}"
        password: "{{ settings.password }}"
        form_token: "{{ form_token }}"
  success: { selector: 'a[href*="mode=logout"]' }
search:
  request:
    get: /search.php
    params: { keywords: "{{ query.keywords }}" }
  response:
    type: html
    rows: { selector: "table.results tbody tr" }
    fields:
      title: { selector: "a.title" }
      details: { selector: "a.title", attr: href, type: url }
    required: [title, details]
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	connector, err := NewDefinitionConnector(DefinitionConfig{
		Indexer:    IndexerRef{Slug: def.Meta.ID, Name: def.Meta.Name},
		Definition: def,
		Settings:   map[string]string{"username": "synthetic-user", "password": "synthetic-password"},
		Transport:  NewTransport(TransportConfig{AllowPrivate: true}),
	})
	if err != nil {
		t.Fatalf("NewDefinitionConnector: %v", err)
	}
	result, err := connector.Search(context.Background(), SearchRequest{Mode: ModeSearch, Query: "Show"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("items = %d, want 1 (the search ran as a guest)", len(result))
	}
}
