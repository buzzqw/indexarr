package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	defaultTimeout      = 45 * time.Second
	defaultMaxBodyBytes = 16 << 20
	defaultFailureLimit = 5
	defaultCooldown     = 60 * time.Second
	defaultUserAgent    = "indexarr/2.0"
	maxRedirects        = 5
)

// Sentinel errors returned by the transport. They are wrapped with context,
// so callers should use errors.Is.
var (
	ErrBreakerOpen  = errors.New("engine: circuit breaker open")
	ErrRateLimited  = errors.New("engine: rate limited by indexer")
	ErrBodyTooLarge = errors.New("engine: response body too large")
)

// Clock supplies the current time. Tests inject a fake clock to exercise
// breaker and rate limiter timings without sleeping.
type Clock interface {
	Now() time.Time
}

// RealClock is the production Clock.
type RealClock struct{}

// Now implements Clock.
func (RealClock) Now() time.Time { return time.Now() }

// TransportConfig configures the shared HTTP transport. Zero values select
// the defaults documented on NewTransport.
type TransportConfig struct {
	Timeout          time.Duration
	UserAgent        string
	MaxBodyBytes     int64
	AllowPrivate     bool    // opt-in for local indexers only
	RatePerSecond    float64 // per host; 0 = unlimited
	Burst            int
	FailureThreshold int
	Cooldown         time.Duration
	Clock            Clock
}

// Response is a fully read HTTP response. Body is always closed by the
// transport before returning.
type Response struct {
	Status int
	Header http.Header
	Body   []byte
	URL    *url.URL
}

// Transport is the one HTTP transport shared by all connectors. It applies
// the SSRF guard, per-host rate limiting, size limits and a per-indexer
// circuit breaker.
type Transport struct {
	cfg     TransportConfig
	clock   Clock
	client  *http.Client
	guard   *Guard
	breaker *circuitBreaker
	limiter *hostLimiter
}

// NewTransport builds a transport from cfg. Defaults for zero fields are:
// 45s timeout, User-Agent "indexarr/2.0", 16 MiB body limit, 5 consecutive
// failures before opening the breaker, 60s cooldown and a burst of 1 when
// rate limiting is enabled.
func NewTransport(cfg TransportConfig) *Transport {
	cfg = normalizeTransportConfig(cfg)

	guard := &Guard{AllowPrivate: cfg.AllowPrivate}
	dialer := &net.Dialer{Timeout: cfg.Timeout, KeepAlive: 30 * time.Second}
	httpTransport := &http.Transport{
		// Per-indexer proxies arrive with the proxy module; no global proxy is
		// used so the SSRF guard always sees the real target.
		Proxy:                 nil,
		DialContext:           guard.DialContext(dialer),
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
	}

	transport := &Transport{
		cfg:     cfg,
		clock:   cfg.Clock,
		guard:   guard,
		breaker: newCircuitBreaker(cfg.Clock, cfg.FailureThreshold, cfg.Cooldown),
		limiter: newHostLimiter(cfg.RatePerSecond, cfg.Burst, cfg.Clock),
	}
	transport.client = &http.Client{
		Transport:     httpTransport,
		Timeout:       cfg.Timeout,
		CheckRedirect: transport.checkRedirect,
	}
	return transport
}

// Do executes req. The caller's request is cloned, so the User-Agent and
// redirect handling never mutate it. HTTP error statuses (including 5xx) are
// returned as a Response for the caller to judge; only transport errors,
// blocked addresses, oversized bodies and rate limiting are errors. A 429 or
// 503 opens the breaker until Retry-After and returns ErrRateLimited.
func (t *Transport) Do(ctx context.Context, breakerKey string, req *http.Request) (*Response, error) {
	if t == nil {
		return nil, errors.New("engine: nil transport")
	}
	if req == nil {
		return nil, errors.New("engine: nil request")
	}
	return t.do(ctx, breakerKey, req, t.cfg.MaxBodyBytes, false)
}

// Download fetches rawURL as a GET with an optional size limit (0 means the
// configured default).
//
// Redirect convention: a redirect to a magnet: URI is never followed. The
// transport returns the 3xx Response so the caller can read Location and build
// a Payload.Magnet. Download also returns the Response for 3xx, 4xx and 5xx
// statuses instead of turning them into errors; 429/503 still open the breaker
// using Retry-After, and 5xx/transport failures count towards it.
func (t *Transport) Download(ctx context.Context, breakerKey, rawURL string, maxBytes int64) (*Response, error) {
	if t == nil {
		return nil, errors.New("engine: nil transport")
	}
	if maxBytes <= 0 {
		maxBytes = t.cfg.MaxBodyBytes
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("engine: parse download URL: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("engine: build download request: %w", err)
	}
	return t.do(ctx, breakerKey, req, maxBytes, true)
}

// BreakerState reports the circuit breaker state for an indexer key. Unknown
// keys report BreakerClosed. It is safe to call concurrently with requests.
func (t *Transport) BreakerState(key string) BreakerState {
	if t == nil || t.breaker == nil {
		return BreakerClosed
	}
	return t.breaker.State(key)
}

func (t *Transport) do(ctx context.Context, breakerKey string, req *http.Request, maxBody int64, statusAsResponse bool) (*Response, error) {
	if ctx == nil {
		return nil, errors.New("engine: nil context")
	}
	if req == nil || req.URL == nil {
		return nil, errors.New("engine: request without URL")
	}
	if err := t.guard.CheckURL(req.URL); err != nil {
		return nil, err
	}

	host := strings.ToLower(req.URL.Hostname())
	if err := t.limiter.wait(ctx, host); err != nil {
		return nil, fmt.Errorf("engine: rate limit wait for %s: %w", host, err)
	}

	key := breakerKey
	if key == "" {
		key = "host:" + host
	}
	if err := t.breaker.allow(key); err != nil {
		return nil, fmt.Errorf("engine: breaker %q: %w", key, err)
	}

	outgoing := req.Clone(ctx)
	if outgoing.Header == nil {
		outgoing.Header = make(http.Header)
	}
	if outgoing.Header.Get("User-Agent") == "" {
		outgoing.Header.Set("User-Agent", t.cfg.UserAgent)
	}

	response, err := t.client.Do(outgoing)
	if err != nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		if ctx.Err() != nil {
			t.breaker.release(key)
		} else {
			t.breaker.failure(key)
		}
		return nil, fmt.Errorf("engine: request %s: %w", host, err)
	}
	defer response.Body.Close()

	body, err := readLimited(response.Body, maxBody)
	if err != nil {
		if ctx.Err() != nil {
			t.breaker.release(key)
		} else {
			t.breaker.failure(key)
		}
		return nil, fmt.Errorf("engine: read %s: %w", host, err)
	}

	result := &Response{
		Status: response.StatusCode,
		Header: response.Header.Clone(),
		Body:   body,
		URL:    response.Request.URL,
	}

	if response.StatusCode == http.StatusTooManyRequests ||
		response.StatusCode == http.StatusServiceUnavailable {
		now := t.clock.Now()
		wait := retryAfter(response.Header.Get("Retry-After"), now, t.cfg.Cooldown)
		t.breaker.openUntil(key, now.Add(wait))
		if statusAsResponse {
			return result, nil
		}
		return nil, fmt.Errorf("engine: %s returned %d, retry after %s: %w",
			host, response.StatusCode, wait.Round(time.Millisecond), ErrRateLimited)
	}

	if response.StatusCode >= 500 {
		t.breaker.failure(key)
	} else {
		t.breaker.success(key)
	}
	return result, nil
}

// checkRedirect revalidates every redirect target, enforces the hop limit and
// removes credentials when the host changes. A magnet: target is returned as
// the last response rather than followed.
func (t *Transport) checkRedirect(req *http.Request, via []*http.Request) error {
	if req == nil || req.URL == nil {
		return errors.New("engine: redirect without URL")
	}
	if strings.EqualFold(req.URL.Scheme, "magnet") {
		return http.ErrUseLastResponse
	}
	if len(via) > maxRedirects {
		return fmt.Errorf("engine: stopped after %d redirects", maxRedirects)
	}
	if err := t.guard.CheckURL(req.URL); err != nil {
		return err
	}
	if !sameHostChain(req, via) {
		req.Header.Del("Authorization")
		req.Header.Del("Cookie")
		req.Header.Del("X-Api-Key")
	}
	return nil
}

// sameHostChain reports whether every hop, including the target, stayed on the
// initial host. Once the chain leaves that host, credentials are not restored
// on later hops that happen to point back to it or within the new host.
func sameHostChain(req *http.Request, via []*http.Request) bool {
	if len(via) == 0 {
		return true
	}
	initial := via[0].URL.Host
	if !strings.EqualFold(req.URL.Host, initial) {
		return false
	}
	for _, previous := range via {
		if !strings.EqualFold(previous.URL.Host, initial) {
			return false
		}
	}
	return true
}

func normalizeTransportConfig(cfg TransportConfig) TransportConfig {
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultTimeout
	}
	if cfg.UserAgent == "" {
		cfg.UserAgent = defaultUserAgent
	}
	if cfg.MaxBodyBytes <= 0 {
		cfg.MaxBodyBytes = defaultMaxBodyBytes
	}
	if cfg.FailureThreshold <= 0 {
		cfg.FailureThreshold = defaultFailureLimit
	}
	if cfg.Cooldown <= 0 {
		cfg.Cooldown = defaultCooldown
	}
	if cfg.RatePerSecond < 0 {
		cfg.RatePerSecond = 0
	}
	if cfg.RatePerSecond > 0 && cfg.Burst <= 0 {
		cfg.Burst = 1
	}
	if cfg.Clock == nil {
		cfg.Clock = RealClock{}
	}
	return cfg
}

func readLimited(body io.Reader, max int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(body, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("engine: body exceeds %d bytes: %w", max, ErrBodyTooLarge)
	}
	return data, nil
}

// retryAfter parses Retry-After as delta-seconds or an HTTP date. An absent or
// invalid value falls back to the configured cooldown.
func retryAfter(value string, now time.Time, fallback time.Duration) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		if seconds < 0 {
			return fallback
		}
		return time.Duration(seconds) * time.Second
	}
	if when, err := http.ParseTime(value); err == nil {
		wait := when.Sub(now)
		if wait < 0 {
			return 0
		}
		return wait
	}
	return fallback
}
