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

	throttleMu sync.Mutex
	throttles  map[string]*hostThrottle
}

// Adaptive pacing after a rate limit. The base budget (rate and burst) is the
// operator's; these constants are what the process learns on its own when a
// site says 429. Scheduled reads (a periodic run) are spaced out, while a live
// app search keeps its normal window: the site's answer to a burst is already
// handled by the breaker.
const (
	// hostSpacingMin is the first pause added between two scheduled requests
	// to a host that answered 429.
	hostSpacingMin = 2 * time.Second
	// hostSpacingMax caps that pause, so a long crawl becomes slow instead of
	// a burst and still finishes.
	hostSpacingMax = 30 * time.Second
	// hostSpacingRecoverAfter is the number of good answers that halves the
	// pause: a site that stops complaining is not punished forever.
	hostSpacingRecoverAfter = 20
	// hostCooldownCap bounds the growing cooldown of a host that keeps saying
	// 429. Ten minutes is the configured base; a site that repeats the answer
	// is asking for hours, not for another probe every ten minutes.
	hostCooldownCap = 6 * time.Hour
)

// hostThrottle is what the process learnt about one host after a 429.
type hostThrottle struct {
	// spacing is the extra pause before the next scheduled request.
	spacing time.Duration
	// last is when the current scheduled slot was taken.
	last time.Time
	// strikes counts the consecutive throttled answers; it grows the cooldown.
	strikes int
	// good counts the answers since the last throttle; enough of them halve
	// the spacing again.
	good int
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
		limiter:   newHostLimiter(rate, burst, clock),
		breaker:   newCircuitBreaker(clock, threshold, cooldown),
		clock:     clock,
		cooldown:  cooldown,
		metrics:   make(map[string]*hostCounters),
		throttles: make(map[string]*hostThrottle),
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

// beforeScheduled waits the learned spacing of a host before a scheduled read.
// It is called after the breaker admitted the request, so a refused probe never
// holds a slot. Live app searches do not pass through here: their deadline is
// short and the breaker already protects the site from a burst.
func (p *Politeness) beforeScheduled(ctx context.Context, host string) error {
	if p == nil {
		return nil
	}
	host = hostKey(host)
	p.throttleMu.Lock()
	entry := p.throttle(host)
	now := p.clock.Now()
	var wait time.Duration
	if entry.spacing > 0 && !entry.last.IsZero() {
		if next := entry.last.Add(entry.spacing); next.After(now) {
			wait = next.Sub(now)
		}
	}
	entry.last = now.Add(wait)
	p.throttleMu.Unlock()
	if wait <= 0 {
		return nil
	}
	return sleepClock(ctx, p.clock, wait)
}

// cooldownFor turns the base cooldown of one throttled answer into the real
// one. A host that keeps saying 429 gets twice as long each time, up to
// hostCooldownCap; a Retry-After longer than that is still honoured by the
// caller.
func (p *Politeness) cooldownFor(host string, base time.Duration) time.Duration {
	if p == nil {
		return base
	}
	host = hostKey(host)
	p.throttleMu.Lock()
	defer p.throttleMu.Unlock()
	entry := p.throttle(host)
	entry.strikes++
	entry.good = 0
	switch {
	case entry.spacing <= 0:
		entry.spacing = hostSpacingMin
	case entry.spacing < hostSpacingMax:
		entry.spacing *= 2
		if entry.spacing > hostSpacingMax {
			entry.spacing = hostSpacingMax
		}
	}
	wait := base
	for i := 1; i < entry.strikes && wait < hostCooldownCap; i++ {
		wait *= 2
	}
	if wait > hostCooldownCap {
		wait = hostCooldownCap
	}
	if wait < base {
		wait = base
	}
	return wait
}

// recoverHost records a good answer. After enough of them the extra spacing is
// halved, so a host that was merely busy returns to the normal budget.
func (p *Politeness) recoverHost(host string) {
	if p == nil {
		return
	}
	host = hostKey(host)
	p.throttleMu.Lock()
	defer p.throttleMu.Unlock()
	entry := p.throttle(host)
	if entry.spacing <= 0 {
		return
	}
	entry.good++
	if entry.good < hostSpacingRecoverAfter {
		return
	}
	entry.good = 0
	entry.spacing /= 2
	if entry.spacing < hostSpacingMin {
		entry.spacing = 0
		entry.strikes = 0
	}
}

// throttle returns the entry of one host. It must be called with throttleMu
// held.
func (p *Politeness) throttle(host string) *hostThrottle {
	entry := p.throttles[host]
	if entry == nil {
		entry = &hostThrottle{}
		p.throttles[host] = entry
	}
	return entry
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
		wait := p.cooldownFor(host, retryAfter(header.Get("Retry-After"), now, p.cooldown))
		p.breaker.openUntil(host, now.Add(wait))
	case http.StatusServiceUnavailable:
		now := p.clock.Now()
		wait := p.cooldownFor(host, retryAfter(header.Get("Retry-After"), now, p.cooldown))
		p.breaker.openUntil(host, now.Add(wait))
	case http.StatusForbidden:
		p.noteStatus(host, status)
		p.recoverHost(host)
		p.breaker.success(host)
	default:
		if status >= 500 {
			p.breaker.failure(host)
			return
		}
		p.recoverHost(host)
		p.breaker.success(host)
	}
}

// ResetHost forgets the breaker state and the counters of one host. It is the
// manual counterpart of the automatic cooldown: the operator says the site is
// back and the next request must be attempted, not refused.
func (p *Politeness) ResetHost(host string) {
	if p == nil {
		return
	}
	host = hostKey(host)
	if host == "" {
		return
	}
	p.breaker.reset(host)
	p.mu.Lock()
	delete(p.metrics, host)
	p.mu.Unlock()
	p.throttleMu.Lock()
	delete(p.throttles, host)
	p.throttleMu.Unlock()
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
