package engine

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// Politeness is the per-host budget shared by every HTTP path of the process:
// the native transport and the legacy RSS/HTML/Torznab client. It combines a
// token bucket, a per-host circuit breaker and the Retry-After policy.
//
// The type deliberately has no SSRF guard: the legacy path must keep reaching
// Jackett and Prowlarr on a LAN address.
type Politeness struct {
	limiter  *hostLimiter
	breaker  *circuitBreaker
	clock    Clock
	cooldown time.Duration

	mu      sync.Mutex
	metrics map[string]*hostCounters
}

// HostMetric is a thread-safe snapshot of the counters for one host. It never
// contains a URL, a query string or credentials.
type HostMetric struct {
	Host        string
	RateLimited uint64 // HTTP 429 responses
	Forbidden   uint64 // HTTP 403 responses
	BreakerOpen uint64 // requests refused because the host breaker was open
	State       BreakerState
}

type hostCounters struct {
	rateLimited uint64
	forbidden   uint64
	breakerOpen uint64
}

// NewPoliteness builds the shared per-host budget. Zero or negative values
// select the process defaults: a rate of 0 means unlimited, burst 1, a breaker
// threshold of 5 and a 60s cooldown.
func NewPoliteness(rate float64, burst, threshold int, cooldown time.Duration, clock Clock) *Politeness {
	if clock == nil {
		clock = RealClock{}
	}
	if threshold < 1 {
		threshold = defaultFailureLimit
	}
	if cooldown <= 0 {
		cooldown = defaultCooldown
	}
	return &Politeness{
		limiter:  newHostLimiter(rate, burst, clock),
		breaker:  newCircuitBreaker(clock, threshold, cooldown),
		clock:    clock,
		cooldown: cooldown,
		metrics:  make(map[string]*hostCounters),
	}
}

// RoundTripper wraps base with the polite policy. Every request waits for a
// token for its host, checks the host breaker and records the outcome. A 429 or
// 503 with Retry-After blocks that host until the indicated instant.
func (p *Politeness) RoundTripper(base http.RoundTripper) http.RoundTripper {
	if p == nil {
		return base
	}
	if base == nil {
		base = http.DefaultTransport
	}
	return &politeRoundTripper{base: base, politeness: p}
}

type politeRoundTripper struct {
	base       http.RoundTripper
	politeness *Politeness
}

func (t *politeRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if req == nil || req.URL == nil {
		return t.base.RoundTrip(req)
	}
	host := hostKey(req.URL.Hostname())
	if err := t.politeness.before(req.Context(), host); err != nil {
		return nil, fmt.Errorf("engine: polite request to %s: %w", host, err)
	}
	response, err := t.base.RoundTrip(req)
	if err != nil {
		t.politeness.failure(host)
		return nil, err
	}
	if response == nil {
		t.politeness.success(host)
		return response, nil
	}
	t.politeness.observe(host, response.StatusCode, response.Header)
	return response, nil
}

// before waits for a host token and then checks the breaker. A request refused
// by an open breaker increments the per-host breaker-open counter.
func (p *Politeness) before(ctx context.Context, host string) error {
	if p == nil {
		return nil
	}
	host = hostKey(host)
	if err := p.limiter.wait(ctx, host); err != nil {
		return err
	}
	if err := p.breaker.allow(host); err != nil {
		p.noteBreakerOpen(host)
		return err
	}
	return nil
}

func (p *Politeness) success(host string) {
	if p == nil {
		return
	}
	p.breaker.success(hostKey(host))
}

func (p *Politeness) failure(host string) {
	if p == nil {
		return
	}
	p.breaker.failure(hostKey(host))
}

// release clears a half-open probe after a cancelled request without counting
// it as either a success or a failure.
func (p *Politeness) release(host string) {
	if p == nil {
		return
	}
	p.breaker.release(hostKey(host))
}

// observe records the response. Retry-After on 429/503 opens the host breaker;
// 5xx counts as a failure; 4xx is recorded but does not trip the breaker.
func (p *Politeness) observe(host string, status int, header http.Header) {
	if p == nil {
		return
	}
	host = hostKey(host)
	switch status {
	case http.StatusTooManyRequests:
		p.noteStatus(host, status)
		now := p.clock.Now()
		wait := retryAfter(header.Get("Retry-After"), now, p.cooldown)
		p.breaker.openUntil(host, now.Add(wait))
	case http.StatusServiceUnavailable:
		now := p.clock.Now()
		wait := retryAfter(header.Get("Retry-After"), now, p.cooldown)
		p.breaker.openUntil(host, now.Add(wait))
	case http.StatusForbidden:
		p.noteStatus(host, status)
		p.breaker.success(host)
	default:
		if status >= 500 {
			p.breaker.failure(host)
			return
		}
		p.breaker.success(host)
	}
}

// HostMetrics returns a copy of the per-host counters, sorted by host. It is
// safe to call while requests are in flight.
func (p *Politeness) HostMetrics() []HostMetric {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	out := make([]HostMetric, 0, len(p.metrics))
	for host, counters := range p.metrics {
		out = append(out, HostMetric{
			Host:        host,
			RateLimited: counters.rateLimited,
			Forbidden:   counters.forbidden,
			BreakerOpen: counters.breakerOpen,
		})
	}
	p.mu.Unlock()
	for i := range out {
		out[i].State = p.breaker.State(out[i].Host)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Host < out[j].Host })
	return out
}

func (p *Politeness) noteStatus(host string, status int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	entry := p.metricEntry(host)
	switch status {
	case http.StatusTooManyRequests:
		entry.rateLimited++
	case http.StatusForbidden:
		entry.forbidden++
	}
}

func (p *Politeness) noteBreakerOpen(host string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.metricEntry(host).breakerOpen++
}

// metricEntry must be called with p.mu held.
func (p *Politeness) metricEntry(host string) *hostCounters {
	entry := p.metrics[host]
	if entry == nil {
		entry = &hostCounters{}
		p.metrics[host] = entry
	}
	return entry
}

func hostKey(host string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
}
