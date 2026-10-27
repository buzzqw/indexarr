package engine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/buzzqw/indexarr/internal/flaresolverr"
)

const (
	defaultSessionMaxAge  = 12 * time.Hour
	defaultSessionTimeout = 60 * time.Second
	// defaultSessionIdle closes a browser that nobody is using. Without it an
	// indexer queried once keeps a whole Chromium (hundreds of MB, a busy
	// renderer) alive until the process stops.
	defaultSessionIdle = 30 * time.Minute
)

// SessionRegistry tracks the FlareSolverr sessions owned by the connectors of
// one process. Orphan cleanup consults it so a connector never destroys a
// session that another connector is using, even when both share the same
// ownership prefix.
type SessionRegistry struct {
	mu    sync.Mutex
	names map[string]struct{}
}

// NewSessionRegistry returns an empty registry.
func NewSessionRegistry() *SessionRegistry {
	return &SessionRegistry{names: make(map[string]struct{})}
}

func (r *SessionRegistry) add(name string) {
	if r == nil || name == "" {
		return
	}
	r.mu.Lock()
	r.names[name] = struct{}{}
	r.mu.Unlock()
}

func (r *SessionRegistry) remove(name string) {
	if r == nil || name == "" {
		return
	}
	r.mu.Lock()
	delete(r.names, name)
	r.mu.Unlock()
}

func (r *SessionRegistry) has(name string) bool {
	if r == nil || name == "" {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.names[name]
	return ok
}

// flareSessionState owns one persistent FlareSolverr browser session. Every
// request of the connector (search, login, detail and download) is sent
// through it, so the Cloudflare clearance and the forum login stay valid.
type flareSessionState struct {
	endpoint string
	prefix   string
	timeout  time.Duration
	maxAge   time.Duration
	idle     time.Duration
	client   *http.Client
	registry *SessionRegistry

	// mu serializes requests: one FlareSolverr session can serve only one
	// command at a time.
	mu       sync.Mutex
	name     string
	created  time.Time
	lastUsed time.Time
	cleaned  bool
}

func (c *DefinitionConnector) configureSession(cfg DefinitionConfig) error {
	session := c.def.Session
	if session == nil {
		return nil
	}
	mode := strings.ToLower(strings.TrimSpace(session.Mode))
	if mode != "" && mode != "flaresolverr" {
		return fmt.Errorf("definition %q: unsupported session mode %q", c.def.Meta.ID, session.Mode)
	}

	endpoint := strings.TrimSpace(session.Endpoint)
	if strings.Contains(endpoint, "{{") {
		rendered, err := c.authTemplateValues().render(endpoint)
		if err != nil {
			return fmt.Errorf("definition %q: session endpoint: %w", c.def.Meta.ID, err)
		}
		endpoint = strings.TrimSpace(rendered)
	}
	if endpoint == "" {
		endpoint = strings.TrimSpace(cfg.SessionEndpoint)
	}
	if endpoint == "" {
		return fmt.Errorf("definition %q: session endpoint is not configured", c.def.Meta.ID)
	}

	prefix := strings.TrimSpace(session.NamePrefix)
	if prefix == "" {
		prefix = "indexarr-" + c.def.Meta.ID
	}
	maxAge := session.MaxAge.Duration()
	if maxAge <= 0 {
		maxAge = defaultSessionMaxAge
	}
	timeout := session.Timeout.Duration()
	if timeout <= 0 {
		timeout = cfg.SessionTimeout
	}
	if timeout <= 0 {
		timeout = defaultSessionTimeout
	}
	idle := session.IdleTimeout.Duration()
	if idle <= 0 {
		idle = defaultSessionIdle
	}

	c.flare = &flareSessionState{
		endpoint: endpoint,
		prefix:   prefix,
		timeout:  timeout,
		maxAge:   maxAge,
		idle:     idle,
		client:   &http.Client{Timeout: timeout + 15*time.Second},
		registry: cfg.Sessions,
	}
	return nil
}

func (c *DefinitionConnector) doSession(ctx context.Context, request *http.Request) (*Response, error) {
	if request == nil || request.URL == nil {
		return nil, errors.New("engine: request without URL")
	}
	state := c.flare
	state.mu.Lock()
	defer state.mu.Unlock()

	// Read the body once so a session recovery can repeat the request. The
	// body carries a login form or a search form, never a secret that would
	// be logged.
	var body []byte
	if request.Body != nil {
		read, err := io.ReadAll(request.Body)
		_ = request.Body.Close()
		if err != nil {
			return nil, errors.New("engine: read request body")
		}
		body = read
	}

	now := c.now()
	if err := state.ensure(ctx, now); err != nil {
		return nil, c.sessionError(err)
	}

	response, err := state.roundTrip(ctx, request, body)
	if err == nil {
		state.touch(c.now())
		return response, nil
	}
	if ctx.Err() != nil {
		return nil, c.requestError(ctx.Err())
	}

	// A dead tab or a browser crash is not fatal: close the session, create a
	// fresh browser and repeat the request once, as the Python service does.
	state.reset(ctx)
	if err := state.ensure(ctx, now); err != nil {
		return nil, c.sessionError(err)
	}
	response, err = state.roundTrip(ctx, request, body)
	if err != nil {
		return nil, c.sessionError(err)
	}
	state.touch(c.now())
	return response, nil
}

func (s *flareSessionState) ensure(ctx context.Context, now time.Time) error {
	if s.name != "" && (s.maxAge <= 0 || now.Sub(s.created) < s.maxAge) {
		return nil
	}
	if s.name != "" {
		s.destroy(ctx)
	}
	if !s.cleaned {
		s.cleanupOrphans(ctx)
		s.cleaned = true
	}
	name, err := sessionName(s.prefix)
	if err != nil {
		return err
	}
	if err := flaresolverr.CreateSession(ctx, s.client, s.endpoint, name, s.timeout); err != nil {
		return err
	}
	s.name = name
	s.created = now
	s.lastUsed = now
	s.registry.add(name)
	return nil
}

// cleanupOrphans destroys sessions left behind by a previous process. Only
// sessions carrying this definition's ownership prefix are touched, and only
// when no live connector of this process owns them.
func (s *flareSessionState) cleanupOrphans(ctx context.Context) {
	names, err := flaresolverr.ListSessions(ctx, s.client, s.endpoint, s.timeout)
	if err != nil {
		return
	}
	for _, name := range names {
		if !strings.HasPrefix(name, s.prefix+"-") {
			continue
		}
		if s.registry.has(name) {
			continue
		}
		_ = flaresolverr.DestroySession(ctx, s.client, s.endpoint, name, s.timeout)
	}
}

// touch records that the browser was just used, so the idle sweep starts from
// the last real request instead of from the creation time.
func (s *flareSessionState) touch(now time.Time) {
	if s.name == "" {
		return
	}
	s.lastUsed = now
}

// reset closes the current session so the next ensure() creates a new browser.
// The FlareSolverr session is not reused after a failure: a crashed tab keeps
// failing until the browser is thrown away.
func (s *flareSessionState) reset(ctx context.Context) {
	_ = s.destroy(ctx)
}

func (s *flareSessionState) destroy(ctx context.Context) error {
	name := s.name
	s.name = ""
	s.created = time.Time{}
	s.lastUsed = time.Time{}
	if name == "" {
		return nil
	}
	s.registry.remove(name)
	return flaresolverr.DestroySession(ctx, s.client, s.endpoint, name, s.timeout)
}

// CloseSession destroys the browser session owned by the connector. The
// connector stays usable: the next request creates a new browser and logs in
// again. It waits for the request in flight, if any, so the browser is never
// closed under it.
func (c *DefinitionConnector) CloseSession(ctx context.Context) error {
	if c == nil || c.flare == nil {
		return nil
	}
	state := c.flare
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.destroy(ctx)
}

// SweepSession closes the browser when it has been idle longer than the
// definition's idle timeout. A session serving a request is never touched:
// TryLock fails and the session is not idle by definition.
func (c *DefinitionConnector) SweepSession(ctx context.Context) bool {
	if c == nil || c.flare == nil {
		return false
	}
	state := c.flare
	if !state.mu.TryLock() {
		return false
	}
	defer state.mu.Unlock()
	if state.name == "" || state.lastUsed.IsZero() || state.idle <= 0 {
		return false
	}
	if c.now().Sub(state.lastUsed) < state.idle {
		return false
	}
	return state.destroy(ctx) == nil
}

func (s *flareSessionState) roundTrip(ctx context.Context, request *http.Request, body []byte) (*Response, error) {
	headers := request.Header.Clone()
	var (
		page   []byte
		status int
		err    error
	)
	switch request.Method {
	case http.MethodGet, http.MethodHead, "":
		page, status, err = flaresolverr.SessionGet(ctx, s.client, s.endpoint, s.name,
			request.URL.String(), headers, s.timeout)
	case http.MethodPost:
		form, parseErr := url.ParseQuery(string(body))
		if parseErr != nil {
			return nil, errors.New("request.post needs a form body")
		}
		page, status, err = flaresolverr.SessionPost(ctx, s.client, s.endpoint, s.name,
			request.URL.String(), headers, form, s.timeout)
	default:
		return nil, fmt.Errorf("unsupported method %s in a persistent session", request.Method)
	}
	if err != nil {
		return nil, err
	}
	return &Response{
		Status: status,
		Header: make(http.Header),
		Body:   page,
		URL:    request.URL,
	}, nil
}

func (c *DefinitionConnector) sessionError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return c.requestError(err)
	}
	// flareSessionState errors never contain a URL: the message names the
	// FlareSolverr command only.
	return fmt.Errorf("definition %q: persistent session: %v", c.def.Meta.ID, err)
}

func sessionName(prefix string) (string, error) {
	buf := make([]byte, 6)
	if _, err := rand.Read(buf); err != nil {
		return "", errors.New("generate session name")
	}
	return prefix + "-" + hex.EncodeToString(buf), nil
}
