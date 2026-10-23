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
)

// flareSessionState owns one persistent FlareSolverr browser session. Every
// request of the connector (search, login, detail and download) is sent
// through it, so the Cloudflare clearance and the forum login stay valid.
type flareSessionState struct {
	endpoint string
	prefix   string
	timeout  time.Duration
	maxAge   time.Duration
	client   *http.Client

	// mu serializes requests: one FlareSolverr session can serve only one
	// command at a time.
	mu      sync.Mutex
	name    string
	created time.Time
	cleaned bool
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

	c.flare = &flareSessionState{
		endpoint: endpoint,
		prefix:   prefix,
		timeout:  timeout,
		maxAge:   maxAge,
		client:   &http.Client{Timeout: timeout + 15*time.Second},
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
	return nil
}

// cleanupOrphans destroys sessions left behind by a previous process. Only
// sessions carrying this definition's ownership prefix are touched.
func (s *flareSessionState) cleanupOrphans(ctx context.Context) {
	names, err := flaresolverr.ListSessions(ctx, s.client, s.endpoint, s.timeout)
	if err != nil {
		return
	}
	for _, name := range names {
		if !strings.HasPrefix(name, s.prefix+"-") {
			continue
		}
		_ = flaresolverr.DestroySession(ctx, s.client, s.endpoint, name, s.timeout)
	}
}

// reset closes the current session so the next ensure() creates a new browser.
// The FlareSolverr session is not reused after a failure: a crashed tab keeps
// failing until the browser is thrown away.
func (s *flareSessionState) reset(ctx context.Context) {
	s.destroy(ctx)
}

func (s *flareSessionState) destroy(ctx context.Context) {
	name := s.name
	s.name = ""
	s.created = time.Time{}
	if name == "" {
		return
	}
	_ = flaresolverr.DestroySession(ctx, s.client, s.endpoint, name, s.timeout)
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
