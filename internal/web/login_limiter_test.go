package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestLoginLimiterBackoffAndRecovery(t *testing.T) {
	now := time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)
	limiter := loginLimiter{now: func() time.Time { return now }}
	const address = "192.0.2.10"

	for i := 1; i < loginFailureThreshold; i++ {
		if pause := limiter.fail(address); pause != 0 {
			t.Fatalf("failure %d paused for %v, want 0", i, pause)
		}
	}
	if pause := limiter.fail(address); pause != loginFirstPause {
		t.Fatalf("threshold pause = %v, want %v", pause, loginFirstPause)
	}
	if wait := limiter.retryAfter(address); wait != loginFirstPause {
		t.Fatalf("retry after = %v, want %v", wait, loginFirstPause)
	}

	now = now.Add(loginFirstPause)
	if wait := limiter.retryAfter(address); wait != 0 {
		t.Fatalf("retry after the pause = %v, want 0", wait)
	}
	if pause := limiter.fail(address); pause != 2*loginFirstPause {
		t.Fatalf("second pause = %v, want %v", pause, 2*loginFirstPause)
	}

	limiter.success(address)
	if wait := limiter.retryAfter(address); wait != 0 {
		t.Fatalf("retry after a success = %v, want 0", wait)
	}
	if pause := limiter.fail(address); pause != 0 {
		t.Fatalf("first failure after a success paused for %v", pause)
	}
}

func TestUIAuthenticationPausesFailedAttempts(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	settings, err := env.store.LoadSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := hashPassword("secret123")
	if err != nil {
		t.Fatal(err)
	}
	settings.UIUser = "admin"
	settings.UIPasswordHash = hash
	if err := env.store.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}

	attempt := func(password string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodGet, "/", nil)
		request.RemoteAddr = "192.0.2.10:40000"
		request.SetBasicAuth("admin", password)
		recorder := httptest.NewRecorder()
		env.handler.ServeHTTP(recorder, request)
		return recorder
	}

	for i := 1; i < loginFailureThreshold; i++ {
		if recorder := attempt("wrong"); recorder.Code != http.StatusUnauthorized {
			t.Fatalf("wrong attempt %d = %d, want 401", i, recorder.Code)
		}
	}
	if recorder := attempt("wrong"); recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("threshold attempt = %d, want 429: %s", recorder.Code, recorder.Body.String())
	}
	blocked := attempt("secret123")
	if blocked.Code != http.StatusTooManyRequests {
		t.Fatalf("correct password during the pause = %d, want 429", blocked.Code)
	}
	if blocked.Header().Get("Retry-After") == "" {
		t.Fatal("429 response has no Retry-After header")
	}

	// The browser sending no credentials is a prompt, not a failure: it is
	// answered as usual (401) and does not extend the pause.
	failures := func() int {
		env.server.loginLimit.mu.Lock()
		defer env.server.loginLimit.mu.Unlock()
		return env.server.loginLimit.entries["192.0.2.10"].failures
	}
	before := failures()
	plain := httptest.NewRequest(http.MethodGet, "/", nil)
	plain.RemoteAddr = "192.0.2.10:40000"
	recorder := httptest.NewRecorder()
	env.handler.ServeHTTP(recorder, plain)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated request during the pause = %d, want 401", recorder.Code)
	}
	if after := failures(); after != before {
		t.Fatalf("unauthenticated request changed failures from %d to %d", before, after)
	}

	env.server.loginLimit.now = func() time.Time { return time.Now().Add(time.Hour) }
	if recorder := attempt("secret123"); recorder.Code != http.StatusOK {
		t.Fatalf("correct password after the pause = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
}
