package engine

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestHostLimiterBurstThenWait(t *testing.T) {
	clock := newFakeClock()
	limiter := newHostLimiter(2, 2, clock)
	start := clock.Now()

	for i := 0; i < 2; i++ {
		if err := limiter.wait(context.Background(), "Example.COM"); err != nil {
			t.Fatalf("burst wait %d: %v", i, err)
		}
	}
	if elapsed := clock.Now().Sub(start); elapsed != 0 {
		t.Errorf("burst consumed %s, want no wait", elapsed)
	}

	if err := limiter.wait(context.Background(), "example.com"); err != nil {
		t.Fatalf("third wait: %v", err)
	}
	elapsed := clock.Now().Sub(start)
	if elapsed < 450*time.Millisecond || elapsed > 550*time.Millisecond {
		t.Errorf("third wait advanced %s, want about 500ms", elapsed)
	}
	if clock.sleepCount() != 1 {
		t.Errorf("sleeps = %d, want 1", clock.sleepCount())
	}
}

func TestHostLimiterDisabled(t *testing.T) {
	clock := newFakeClock()
	limiter := newHostLimiter(0, 1, clock)
	for i := 0; i < 5; i++ {
		if err := limiter.wait(context.Background(), "example.com"); err != nil {
			t.Fatalf("wait %d: %v", i, err)
		}
	}
	if clock.sleepCount() != 0 {
		t.Error("disabled limiter slept")
	}
}

func TestHostLimiterPerHost(t *testing.T) {
	clock := newFakeClock()
	limiter := newHostLimiter(1, 1, clock)

	if err := limiter.wait(context.Background(), "a.example"); err != nil {
		t.Fatalf("first host: %v", err)
	}
	if err := limiter.wait(context.Background(), "b.example"); err != nil {
		t.Fatalf("second host: %v", err)
	}
	if clock.sleepCount() != 0 {
		t.Error("separate hosts shared a bucket")
	}
}

func TestHostLimiterRealClockCancel(t *testing.T) {
	limiter := newHostLimiter(0.001, 1, RealClock{})
	if err := limiter.wait(context.Background(), "example.com"); err != nil {
		t.Fatalf("consume token: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := limiter.wait(ctx, "example.com"); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("wait = %v, want context.DeadlineExceeded", err)
	}
}

func TestHostLimiterContextCancel(t *testing.T) {
	clock := newFakeClock()
	limiter := newHostLimiter(1, 1, clock)

	if err := limiter.wait(context.Background(), "example.com"); err != nil {
		t.Fatalf("consume token: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := limiter.wait(ctx, "example.com")
	if !errors.Is(err, context.Canceled) {
		t.Errorf("wait = %v, want context.Canceled", err)
	}
}
