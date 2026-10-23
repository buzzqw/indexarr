package engine

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const challengePage = `<html><head><title>Just a moment...</title></head>
<body><div id="cf-challenge-running">Checking your browser before accessing.</div></body></html>`

type flareCall struct {
	Cmd     string            `json:"cmd"`
	URL     string            `json:"url"`
	Timeout int               `json:"maxTimeout"`
	Headers map[string]string `json:"headers"`
}

func TestChallengePassthrough(t *testing.T) {
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "hello")
	}))
	defer site.Close()

	var calls int32
	solver := ChallengeSolverFunc(func(context.Context, *http.Request) (*Response, error) {
		atomic.AddInt32(&calls, 1)
		return &Response{Status: http.StatusOK, Body: []byte("wrong")}, nil
	})
	transport := newLocalTransport(TransportConfig{Solver: solver})
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, site.URL, nil)
	response, err := transport.Do(context.Background(), "idx", req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if string(response.Body) != "hello" {
		t.Errorf("body = %q", response.Body)
	}
	if atomic.LoadInt32(&calls) != 0 {
		t.Error("the solver was called for a normal page")
	}
}

func TestChallengePlainForbiddenPassthrough(t *testing.T) {
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, "forbidden: no credentials")
	}))
	defer site.Close()

	var calls int32
	transport := newLocalTransport(TransportConfig{Solver: ChallengeSolverFunc(func(context.Context, *http.Request) (*Response, error) {
		atomic.AddInt32(&calls, 1)
		return &Response{Status: http.StatusOK, Body: []byte("solved")}, nil
	})})
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, site.URL, nil)
	response, err := transport.Do(context.Background(), "idx", req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if response.Status != http.StatusForbidden {
		t.Errorf("status = %d, want 403", response.Status)
	}
	if atomic.LoadInt32(&calls) != 0 {
		t.Error("the solver was called without a challenge marker")
	}
}

func TestChallengeSolvedByFlareSolverr(t *testing.T) {
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("cf-mitigated", "challenge")
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, challengePage)
	}))
	defer site.Close()

	var call flareCall
	solverServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1" {
			t.Errorf("solver request = %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&call); err != nil {
			t.Fatal(err)
		}
		_, _ = io.WriteString(w, `{"status":"ok","message":"","solution":{"status":200,"response":"<rss>solved</rss>"}}`)
	}))
	defer solverServer.Close()

	solver := NewFlareSolverrSolver(solverServer.URL, 5*time.Second)
	transport := newLocalTransport(TransportConfig{Solver: solver})
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, site.URL+"/api?t=search", nil)
	req.Header.Set("Cookie", "session=synthetic")
	req.Header.Set("Authorization", "Bearer synthetic-token")
	response, err := transport.Do(context.Background(), "idx", req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if response.Status != http.StatusOK || string(response.Body) != "<rss>solved</rss>" {
		t.Errorf("response = %d %q", response.Status, response.Body)
	}
	if call.Cmd != "request.get" || call.URL != site.URL+"/api?t=search" {
		t.Errorf("solver command/url = %q/%q", call.Cmd, call.URL)
	}
	if call.Headers["Cookie"] != "session=synthetic" {
		t.Errorf("cookie not forwarded: %#v", call.Headers)
	}
	if call.Headers["Authorization"] != "Bearer synthetic-token" {
		t.Errorf("authorization not forwarded: %#v", call.Headers)
	}
	if transport.BreakerState("idx") != BreakerClosed {
		t.Errorf("breaker = %s after a solved challenge", transport.BreakerState("idx"))
	}
}

func TestChallengeWithoutSolver(t *testing.T) {
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, challengePage)
	}))
	defer site.Close()

	transport := newLocalTransport(TransportConfig{})
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, site.URL, nil)
	_, err := transport.Do(context.Background(), "idx", req)
	if !errors.Is(err, ErrChallenge) {
		t.Fatalf("error = %v, want ErrChallenge", err)
	}
	if errors.Is(err, ErrChallengeSolver) {
		t.Error("ErrChallengeSolver reported without a configured solver")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "flaresolverr") {
		t.Errorf("the error does not name the remedy: %v", err)
	}
}

func TestChallengeSolverServerError(t *testing.T) {
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, challengePage)
	}))
	defer site.Close()

	solverServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "flaresolverr exploded", http.StatusInternalServerError)
	}))
	defer solverServer.Close()

	solver := NewFlareSolverrSolver(solverServer.URL, time.Second)
	transport := newLocalTransport(TransportConfig{Solver: solver, FailureThreshold: 1, Cooldown: time.Minute})
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, site.URL, nil)
	_, err := transport.Do(context.Background(), "idx", req)
	if !errors.Is(err, ErrChallenge) || !errors.Is(err, ErrChallengeSolver) {
		t.Fatalf("error = %v, want ErrChallenge and ErrChallengeSolver", err)
	}
	if transport.BreakerState("idx") != BreakerOpen {
		t.Errorf("breaker = %s, want open after a failed solve", transport.BreakerState("idx"))
	}
}

func TestChallengeSolverTimeout(t *testing.T) {
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, challengePage)
	}))
	defer site.Close()

	release := make(chan struct{})
	solverServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer solverServer.Close()
	defer close(release)

	solver := NewFlareSolverrSolverWithClient(solverServer.URL, time.Second, &http.Client{Timeout: 100 * time.Millisecond})
	transport := newLocalTransport(TransportConfig{Solver: solver})
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, site.URL, nil)
	started := time.Now()
	_, err := transport.Do(context.Background(), "idx", req)
	if !errors.Is(err, ErrChallenge) || !errors.Is(err, ErrChallengeSolver) {
		t.Fatalf("error = %v, want ErrChallenge and ErrChallengeSolver", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Errorf("the timeout took %s", elapsed)
	}
}

func TestChallengeDetected(t *testing.T) {
	cases := []struct {
		name   string
		status int
		header http.Header
		body   string
		want   bool
	}{
		{"cloudflare 403", 403, nil, challengePage, true},
		{"cloudflare 503", 503, nil, "Attention Required! | Cloudflare", true},
		{"header", 403, http.Header{"Cf-Mitigated": []string{"challenge"}}, "anything", true},
		{"plain 403", 403, nil, "Forbidden", false},
		{"plain 503", 503, nil, "Service unavailable", false},
		{"200 mention", 200, nil, "Cloudflare", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ChallengeDetected(&Response{Status: tc.status, Header: tc.header, Body: []byte(tc.body)}); got != tc.want {
				t.Errorf("ChallengeDetected = %v, want %v", got, tc.want)
			}
		})
	}
}

// ChallengeSolverFunc is a test double for ChallengeSolver.
type ChallengeSolverFunc func(context.Context, *http.Request) (*Response, error)

func (f ChallengeSolverFunc) Solve(ctx context.Context, req *http.Request) (*Response, error) {
	return f(ctx, req)
}
