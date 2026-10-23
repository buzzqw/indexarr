package engine

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// fakeClock is shared by breaker, limiter and transport tests. Sleep advances
// the clock immediately, so rate limiter waits are deterministic and instant.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	sleeps int
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, time.October, 23, 12, 0, 0, 0, time.UTC)}
}

func (f *fakeClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *fakeClock) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
}

func (f *fakeClock) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
	f.sleeps++
	return nil
}

func (f *fakeClock) sleepCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sleeps
}

func TestCircuitBreakerTransitions(t *testing.T) {
	clock := newFakeClock()
	breaker := newCircuitBreaker(clock, 2, 30*time.Second)

	if got := breaker.State("idx"); got != BreakerClosed {
		t.Errorf("unknown state = %q, want closed", got)
	}
	if err := breaker.allow("idx"); err != nil {
		t.Fatalf("closed allow: %v", err)
	}
	breaker.success("idx")

	breaker.failure("idx")
	if got := breaker.State("idx"); got != BreakerClosed {
		t.Errorf("after one failure state = %q, want closed", got)
	}
	breaker.failure("idx")
	if got := breaker.State("idx"); got != BreakerOpen {
		t.Fatalf("after threshold state = %q, want open", got)
	}
	if err := breaker.allow("idx"); !errors.Is(err, ErrBreakerOpen) {
		t.Errorf("open allow = %v, want ErrBreakerOpen", err)
	}

	clock.Advance(29 * time.Second)
	if got := breaker.State("idx"); got != BreakerOpen {
		t.Errorf("before cooldown state = %q, want open", got)
	}
	if err := breaker.allow("idx"); !errors.Is(err, ErrBreakerOpen) {
		t.Errorf("before cooldown allow = %v, want ErrBreakerOpen", err)
	}

	clock.Advance(time.Second)
	if got := breaker.State("idx"); got != BreakerHalfOpen {
		t.Fatalf("after cooldown state = %q, want half-open", got)
	}
	if err := breaker.allow("idx"); err != nil {
		t.Fatalf("half-open probe: %v", err)
	}
	if err := breaker.allow("idx"); !errors.Is(err, ErrBreakerOpen) {
		t.Errorf("second half-open probe = %v, want ErrBreakerOpen", err)
	}

	breaker.failure("idx")
	if got := breaker.State("idx"); got != BreakerOpen {
		t.Fatalf("failed probe state = %q, want open", got)
	}
	if err := breaker.allow("idx"); !errors.Is(err, ErrBreakerOpen) {
		t.Errorf("reopened allow = %v, want ErrBreakerOpen", err)
	}

	clock.Advance(30 * time.Second)
	if err := breaker.allow("idx"); err != nil {
		t.Fatalf("second probe: %v", err)
	}
	breaker.success("idx")
	if got := breaker.State("idx"); got != BreakerClosed {
		t.Errorf("after successful probe state = %q, want closed", got)
	}
	if err := breaker.allow("idx"); err != nil {
		t.Fatalf("closed allow after recovery: %v", err)
	}
}

func TestCircuitBreakerOpenUntil(t *testing.T) {
	clock := newFakeClock()
	breaker := newCircuitBreaker(clock, 5, time.Minute)

	breaker.openUntil("idx", clock.Now().Add(45*time.Second))
	if got := breaker.State("idx"); got != BreakerOpen {
		t.Fatalf("state = %q, want open", got)
	}
	if err := breaker.allow("idx"); !errors.Is(err, ErrBreakerOpen) {
		t.Fatalf("allow = %v, want ErrBreakerOpen", err)
	}

	clock.Advance(46 * time.Second)
	if got := breaker.State("idx"); got != BreakerHalfOpen {
		t.Fatalf("state after Retry-After = %q, want half-open", got)
	}
	if err := breaker.allow("idx"); err != nil {
		t.Fatalf("allow after Retry-After: %v", err)
	}
}

func TestCircuitBreakerReleaseClearsProbe(t *testing.T) {
	clock := newFakeClock()
	breaker := newCircuitBreaker(clock, 1, time.Minute)

	breaker.failure("idx")
	clock.Advance(time.Minute)
	if err := breaker.allow("idx"); err != nil {
		t.Fatalf("probe: %v", err)
	}
	if err := breaker.allow("idx"); !errors.Is(err, ErrBreakerOpen) {
		t.Fatalf("concurrent probe = %v, want ErrBreakerOpen", err)
	}
	breaker.release("idx")
	if err := breaker.allow("idx"); err != nil {
		t.Fatalf("probe after release: %v", err)
	}
}

func TestCircuitBreakerKeysAreIndependent(t *testing.T) {
	clock := newFakeClock()
	breaker := newCircuitBreaker(clock, 1, time.Minute)

	breaker.failure("a")
	if got := breaker.State("a"); got != BreakerOpen {
		t.Fatalf("key a state = %q, want open", got)
	}
	if got := breaker.State("b"); got != BreakerClosed {
		t.Errorf("key b state = %q, want closed", got)
	}
	if err := breaker.allow("b"); err != nil {
		t.Errorf("key b allow = %v, want nil", err)
	}
}
