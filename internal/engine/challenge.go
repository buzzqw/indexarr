package engine

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/buzzqw/indexarr/internal/flaresolverr"
)

var (
	// ErrChallenge marks a response that is a Cloudflare browser check rather
	// than the page the indexer asked for. It is distinct from a generic
	// transport error so the caller can report the indexer as blocked.
	ErrChallenge = errors.New("engine: cloudflare challenge")
	// ErrChallengeSolver marks a challenge that might have been solved but
	// FlareSolverr was unreachable, timed out or returned an error.
	ErrChallengeSolver = errors.New("engine: challenge solver unavailable")
)

// challengeBodyLimit is how much of a response is searched for the markers.
// A Cloudflare interstitial is small; a huge page that happens to start with
// 403 is not scanned in full.
const challengeBodyLimit = 64 << 10

// challengeMarkers are the fragments present in the Cloudflare pages that ask
// the browser to run JavaScript or solve a check. A plain 403 or 503 without
// one of them is a normal refusal, not a solvable challenge.
var challengeMarkers = []string{
	"just a moment",
	"attention required! | cloudflare",
	"checking your browser",
	"enable javascript and cookies to continue",
	"cf-browser-verification",
	"_cf_chl_opt",
	"cf_challenge",
	"/cdn-cgi/challenge-platform/",
}

// ChallengeSolver turns a request that hit a Cloudflare challenge into the
// page a browser would see. A nil solver disables the retry.
type ChallengeSolver interface {
	Solve(ctx context.Context, req *http.Request) (*Response, error)
}

// ChallengeDetected reports whether resp is a Cloudflare browser check. Only
// 403 and 503 are considered (Cloudflare's challenge statuses); a 200 page
// that merely mentions Cloudflare is not a challenge.
func ChallengeDetected(resp *Response) bool {
	if resp == nil {
		return false
	}
	if resp.Status != http.StatusForbidden && resp.Status != http.StatusServiceUnavailable {
		return false
	}
	if resp.Header != nil {
		mitigated := strings.ToLower(strings.TrimSpace(resp.Header.Get("cf-mitigated")))
		if strings.Contains(mitigated, "challenge") {
			return true
		}
	}
	body := resp.Body
	if len(body) > challengeBodyLimit {
		body = body[:challengeBodyLimit]
	}
	text := strings.ToLower(string(body))
	for _, marker := range challengeMarkers {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

// challengeError keeps the transport sentinels while carrying the host and the
// status that need to reach the operator. It never includes the request URL.
type challengeError struct {
	host   string
	status int
	solver bool
	cause  error
}

func (e *challengeError) Error() string {
	if e.solver {
		if e.cause != nil {
			return fmt.Sprintf("engine: %s returned HTTP %d (cloudflare challenge); challenge solver failed: %v",
				e.host, e.status, e.cause)
		}
		return fmt.Sprintf("engine: %s returned HTTP %d (cloudflare challenge); challenge solver did not solve it",
			e.host, e.status)
	}
	return fmt.Sprintf("engine: %s returned HTTP %d (cloudflare challenge); configure FlareSolverr",
		e.host, e.status)
}

// Unwrap exposes both sentinels. errors.Is(err, ErrChallenge) is true for
// every challenge; ErrChallengeSolver is added when a solver was configured
// but could not produce the page.
func (e *challengeError) Unwrap() []error {
	errs := []error{ErrChallenge}
	if e.solver {
		errs = append(errs, ErrChallengeSolver)
	}
	return errs
}

// FlareSolverrSolver is the production ChallengeSolver. It forwards the
// request headers (cookies, token, basic auth) to FlareSolverr, which fetches
// the page with a real browser. A LAN address is still the indexer's choice:
// the solver only sees the public page FlareSolverr requests.
type FlareSolverrSolver struct {
	endpoint string
	timeout  time.Duration
	client   *http.Client
}

// NewFlareSolverrSolver builds a solver for endpoint. An empty endpoint
// returns nil, which the transport reads as "challenge retry disabled".
func NewFlareSolverrSolver(endpoint string, timeout time.Duration) *FlareSolverrSolver {
	if strings.TrimSpace(endpoint) == "" {
		return nil
	}
	if timeout <= 0 {
		timeout = flaresolverr.DefaultTimeout
	}
	return &FlareSolverrSolver{
		endpoint: endpoint,
		timeout:  timeout,
		client:   &http.Client{Timeout: timeout + 15*time.Second},
	}
}

// NewFlareSolverrSolverWithClient is NewFlareSolverrSolver with an explicit
// HTTP client. Tests use it to inject a short timeout without waiting for the
// production grace period.
func NewFlareSolverrSolverWithClient(endpoint string, timeout time.Duration, client *http.Client) *FlareSolverrSolver {
	solver := NewFlareSolverrSolver(endpoint, timeout)
	if solver == nil {
		return nil
	}
	if client != nil {
		solver.client = client
	}
	return solver
}

// Solve implements ChallengeSolver. FlareSolverr's request.get command only
// fetches a page, so a challenge on a POST search cannot be replayed: the
// caller receives a clear solver error instead of a silent fallback.
func (s *FlareSolverrSolver) Solve(ctx context.Context, req *http.Request) (*Response, error) {
	if s == nil {
		return nil, fmt.Errorf("%w: no solver configured", ErrChallengeSolver)
	}
	if req == nil || req.URL == nil {
		return nil, fmt.Errorf("%w: request without URL", ErrChallengeSolver)
	}
	if req.Method != "" && req.Method != http.MethodGet && req.Method != http.MethodHead {
		return nil, fmt.Errorf("%w: only GET pages can be solved", ErrChallengeSolver)
	}
	body, status, err := flaresolverr.Fetch(ctx, s.client, s.endpoint, req.URL.String(), flaresolverr.Options{
		Headers: req.Header,
		Timeout: s.timeout,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrChallengeSolver, err)
	}
	return &Response{
		Status: status,
		Header: http.Header{},
		Body:   body,
		URL:    req.URL,
	}, nil
}
