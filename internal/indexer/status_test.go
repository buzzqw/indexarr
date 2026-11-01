package indexer

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/buzzqw/indexarr/internal/engine"
)

// TestStatusForBreakerOpenIsPaused locks the rule that a request refused by
// the per-source breaker is a pause, not a failure: the source was never
// contacted, so it must not extend the retry streak or the auto-disable count.
func TestStatusForBreakerOpenIsPaused(t *testing.T) {
	// The wrapped text is the real one: a definition connector turns the
	// transport error into "request failed: <sentinel>".
	refused := fmt.Errorf("indexer tg: fetch: %w",
		fmt.Errorf("request failed: %w", engine.ErrBreakerOpen))
	if got := statusFor(context.Background(), refused); got != StatusPaused {
		t.Errorf("statusFor(breaker) = %q, want %q", got, StatusPaused)
	}
	if got := statusFor(context.Background(), errors.New("boom")); got != "error" {
		t.Errorf("statusFor(error) = %q, want error", got)
	}
	if got := statusFor(context.Background(), nil); got != "ok" {
		t.Errorf("statusFor(nil) = %q, want ok", got)
	}
}

// TestStatusForTimeoutIsAFailure keeps every slow source in the health model.
// The connector reports a request that ran out of time as
// context.DeadlineExceeded; when that was read as "canceled" the failure streak
// never grew and the run row disappeared from the counters.
func TestStatusForTimeoutIsAFailure(t *testing.T) {
	timeout := fmt.Errorf("indexer extto: fetch: request timed out: %w", context.DeadlineExceeded)
	if got := statusFor(context.Background(), timeout); got != "error" {
		t.Errorf("statusFor(timeout) = %q, want error", got)
	}
	// A deadline of the caller is still indexarr stopping the run.
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	<-ctx.Done()
	if got := statusFor(ctx, context.DeadlineExceeded); got != StatusCanceled {
		t.Errorf("statusFor(caller deadline) = %q, want %q", got, StatusCanceled)
	}
}
