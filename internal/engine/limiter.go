package engine

import (
	"context"
	"math"
	"strings"
	"sync"
	"time"
)

// hostLimiter is a token bucket per host. It is deliberately in memory: a
// restart resets the buckets, which is acceptable for one process serving one
// set of indexers.
type hostLimiter struct {
	mu    sync.Mutex
	rate  float64
	burst int
	clock Clock
	hosts map[string]*tokenBucket
}

type tokenBucket struct {
	tokens float64
	last   time.Time
}

func newHostLimiter(rate float64, burst int, clock Clock) *hostLimiter {
	if clock == nil {
		clock = RealClock{}
	}
	if rate < 0 {
		rate = 0
	}
	if burst < 1 {
		burst = 1
	}
	return &hostLimiter{
		rate:  rate,
		burst: burst,
		clock: clock,
		hosts: make(map[string]*tokenBucket),
	}
}

// wait blocks until a token is available for host or ctx is cancelled. A zero
// rate disables limiting.
func (l *hostLimiter) wait(ctx context.Context, host string) error {
	if l == nil || l.rate <= 0 {
		return nil
	}
	host = strings.ToLower(host)

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		now := l.clock.Now()

		l.mu.Lock()
		bucket := l.hosts[host]
		if bucket == nil {
			bucket = &tokenBucket{tokens: float64(l.burst), last: now}
			l.hosts[host] = bucket
		}
		if elapsed := now.Sub(bucket.last).Seconds(); elapsed > 0 {
			bucket.tokens = math.Min(float64(l.burst), bucket.tokens+elapsed*l.rate)
			bucket.last = now
		}
		if bucket.tokens >= 1 {
			bucket.tokens--
			l.mu.Unlock()
			return nil
		}
		wait := time.Duration((1 - bucket.tokens) / l.rate * float64(time.Second))
		l.mu.Unlock()

		if wait <= 0 {
			wait = time.Nanosecond
		}
		if err := sleepClock(ctx, l.clock, wait); err != nil {
			return err
		}
	}
}

// clockSleeper is an optional Clock extension used by tests: a fake clock can
// advance itself instead of making the test wait in real time.
type clockSleeper interface {
	Sleep(context.Context, time.Duration) error
}

func sleepClock(ctx context.Context, clock Clock, d time.Duration) error {
	if sleeper, ok := clock.(clockSleeper); ok {
		return sleeper.Sleep(ctx, d)
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
