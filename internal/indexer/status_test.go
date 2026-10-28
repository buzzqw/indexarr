package indexer

import (
	"context"
	"errors"
	"fmt"
	"testing"

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
