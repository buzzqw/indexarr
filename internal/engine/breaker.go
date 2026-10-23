package engine

import (
	"sync"
	"time"
)

// BreakerState is the observable state of one circuit breaker entry.
type BreakerState string

const (
	BreakerClosed   BreakerState = "closed"
	BreakerOpen     BreakerState = "open"
	BreakerHalfOpen BreakerState = "half-open"
)

// circuitBreaker trips after consecutive failures and lets a single probe
// through once the cooldown has elapsed. It is keyed by indexer slug so one
// failing indexer cannot block the others, even when they share a host.
type circuitBreaker struct {
	mu        sync.Mutex
	clock     Clock
	threshold int
	cooldown  time.Duration
	entries   map[string]*breakerEntry
}

type breakerEntry struct {
	state       BreakerState
	failures    int
	openedUntil time.Time
	probing     bool
}

func newCircuitBreaker(clock Clock, threshold int, cooldown time.Duration) *circuitBreaker {
	if clock == nil {
		clock = RealClock{}
	}
	if threshold < 1 {
		threshold = 1
	}
	if cooldown <= 0 {
		cooldown = time.Minute
	}
	return &circuitBreaker{
		clock:     clock,
		threshold: threshold,
		cooldown:  cooldown,
		entries:   make(map[string]*breakerEntry),
	}
}

// allow reports whether a request may proceed. In half-open state only one
// probe is admitted; concurrent callers get ErrBreakerOpen.
func (b *circuitBreaker) allow(key string) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	entry := b.entry(key)
	now := b.clock.Now()
	if entry.state == BreakerOpen {
		if now.Before(entry.openedUntil) {
			return ErrBreakerOpen
		}
		entry.state = BreakerHalfOpen
		entry.probing = false
	}
	if entry.state == BreakerHalfOpen {
		if entry.probing {
			return ErrBreakerOpen
		}
		entry.probing = true
	}
	return nil
}

// success closes the breaker and clears the failure count.
func (b *circuitBreaker) success(key string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	entry := b.entry(key)
	entry.state = BreakerClosed
	entry.failures = 0
	entry.probing = false
	entry.openedUntil = time.Time{}
}

// failure records one failed request and opens the breaker at the threshold.
// A failed half-open probe always reopens the breaker.
func (b *circuitBreaker) failure(key string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	entry := b.entry(key)
	entry.probing = false
	if entry.state == BreakerHalfOpen {
		entry.state = BreakerOpen
		entry.failures = 0
		entry.openedUntil = b.clock.Now().Add(b.cooldown)
		return
	}
	entry.failures++
	if entry.failures >= b.threshold {
		entry.state = BreakerOpen
		entry.openedUntil = b.clock.Now().Add(b.cooldown)
	}
}

// openUntil keeps the breaker open until an explicit time, used for
// Retry-After. A time already in the past allows an immediate probe.
func (b *circuitBreaker) openUntil(key string, until time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()

	entry := b.entry(key)
	now := b.clock.Now()
	if entry.state == BreakerOpen && entry.openedUntil.After(now) && entry.openedUntil.After(until) {
		return // never shorten an existing Retry-After window
	}
	if until.After(now) {
		entry.state = BreakerOpen
		entry.openedUntil = until
		entry.probing = false
		return
	}
	entry.state = BreakerHalfOpen
	entry.openedUntil = time.Time{}
	entry.probing = false
}

// release clears an in-flight half-open probe without counting a result, for
// callers whose request was cancelled rather than answered by the indexer.
func (b *circuitBreaker) release(key string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	entry, ok := b.entries[key]
	if ok && entry.state == BreakerHalfOpen {
		entry.probing = false
	}
}

// State returns the breaker state for key, or BreakerClosed for an unknown
// key. An open breaker whose cooldown has elapsed is reported as half-open.
func (b *circuitBreaker) State(key string) BreakerState {
	b.mu.Lock()
	defer b.mu.Unlock()

	entry, ok := b.entries[key]
	if !ok {
		return BreakerClosed
	}
	if entry.state == BreakerOpen && !b.clock.Now().Before(entry.openedUntil) {
		entry.state = BreakerHalfOpen
		entry.probing = false
	}
	return entry.state
}

func (b *circuitBreaker) entry(key string) *breakerEntry {
	entry := b.entries[key]
	if entry == nil {
		entry = &breakerEntry{state: BreakerClosed}
		b.entries[key] = entry
	}
	return entry
}
