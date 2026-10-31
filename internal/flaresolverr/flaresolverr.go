// Package flaresolverr is a minimal client for a FlareSolverr service. It
// posts the request.get command to /v1 and returns the solved page.
package flaresolverr

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// DefaultMaxConcurrency bounds the browser solves one indexarr process asks a
// FlareSolverr instance for at the same time. A browser session is heavy and
// FlareSolverr becomes unresponsive when many challenges are submitted at once;
// the queue is inside indexarr so a cycle over many protected sources cannot
// starve the service.
const DefaultMaxConcurrency = 2

var (
	slotsMu sync.RWMutex
	slots   = make(chan struct{}, DefaultMaxConcurrency)
)

// SetMaxConcurrency changes the process-wide solve limit. It is exported for
// tests and for a future configuration key; values below one restore the
// default.
func SetMaxConcurrency(n int) {
	if n < 1 {
		n = DefaultMaxConcurrency
	}
	slotsMu.Lock()
	slots = make(chan struct{}, n)
	slotsMu.Unlock()
}

func acquireSlot(ctx context.Context) error {
	slotsMu.RLock()
	queue := slots
	slotsMu.RUnlock()
	select {
	case queue <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func releaseSlot() {
	slotsMu.RLock()
	queue := slots
	slotsMu.RUnlock()
	select {
	case <-queue:
	default:
	}
}

// DefaultTimeout bounds one solve. FlareSolverr's own default maxTimeout is
// 60 seconds.
const DefaultTimeout = 60 * time.Second

const maxResponseBytes = 32 << 20

// Endpoint normalises a configured base URL to the /v1 command endpoint.
func Endpoint(raw string) string {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" {
		return ""
	}
	if strings.HasSuffix(strings.ToLower(raw), "/v1") {
		return raw
	}
	return raw + "/v1"
}

type reply struct {
	Status   string `json:"status"`
	Message  string `json:"message"`
	Solution struct {
		Status   int    `json:"status"`
		Response string `json:"response"`
	} `json:"solution"`
}

// Options carries the per-call settings of a solve. Headers are forwarded to
// FlareSolverr so a request that needs Basic auth, a token or a session cookie
// keeps working when it is retried through the solver. The caller is expected
// to hand over only the headers the site needs; hop-by-hop headers are dropped.
type Options struct {
	Headers http.Header
	Timeout time.Duration
}

// Get asks FlareSolverr to fetch rawURL and returns the solved body and the
// status of the solved page. endpoint may be a base URL or a /v1 URL.
func Get(ctx context.Context, client *http.Client, endpoint, rawURL string, timeout time.Duration) ([]byte, int, error) {
	return Fetch(ctx, client, endpoint, rawURL, Options{Timeout: timeout})
}

// Fetch is Get with forwarded headers. It returns the body FlareSolverr read
// from rawURL and the status code of that page.
func Fetch(ctx context.Context, client *http.Client, endpoint, rawURL string, options Options) ([]byte, int, error) {
	command := Endpoint(endpoint)
	if command == "" {
		return nil, 0, fmt.Errorf("flaresolverr: empty endpoint")
	}
	if strings.TrimSpace(rawURL) == "" {
		return nil, 0, fmt.Errorf("flaresolverr: empty URL")
	}
	timeout := options.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	if client == nil {
		client = http.DefaultClient
	}
	requestBody := map[string]any{
		"cmd":        "request.get",
		"url":        rawURL,
		"maxTimeout": int(timeout / time.Millisecond),
	}
	if headers := forwardableHeaders(options.Headers); len(headers) > 0 {
		requestBody["headers"] = headers
	}
	payload, err := json.Marshal(requestBody)
	if err != nil {
		return nil, 0, err
	}
	requestCtx, cancel := context.WithTimeout(ctx, timeout+15*time.Second)
	defer cancel()
	if err := acquireSlot(requestCtx); err != nil {
		return nil, 0, fmt.Errorf("flaresolverr: waiting for a browser slot: %w", err)
	}
	defer releaseSlot()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, command, bytes.NewReader(payload))
	if err != nil {
		return nil, 0, err
	}
	request.Header.Set("Content-Type", "application/json")
	httpResponse, err := client.Do(request)
	if err != nil {
		return nil, 0, err
	}
	defer httpResponse.Body.Close()
	body, err := io.ReadAll(io.LimitReader(httpResponse.Body, maxResponseBytes+1))
	if err != nil {
		return nil, 0, err
	}
	if len(body) > maxResponseBytes {
		return nil, 0, fmt.Errorf("flaresolverr: response larger than %d bytes", maxResponseBytes)
	}
	if httpResponse.StatusCode >= 400 {
		return nil, 0, fmt.Errorf("flaresolverr: HTTP %d: %s", httpResponse.StatusCode, shortBody(body))
	}
	var parsed reply
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, 0, fmt.Errorf("flaresolverr: invalid reply: %w", err)
	}
	if !strings.EqualFold(parsed.Status, "ok") {
		message := strings.TrimSpace(parsed.Message)
		if message == "" {
			message = parsed.Status
		}
		return nil, 0, fmt.Errorf("flaresolverr: %s", message)
	}
	if parsed.Solution.Response == "" {
		return nil, 0, fmt.Errorf("flaresolverr: empty solution")
	}
	status := parsed.Solution.Status
	if status <= 0 {
		status = http.StatusOK
	}
	return []byte(parsed.Solution.Response), status, nil
}

// forwardableHeaders copies the headers that describe the page request.
// Content-Length and the other hop-by-hop fields are owned by the client that
// talks to FlareSolverr, not by the request being solved.
func forwardableHeaders(header http.Header) map[string]string {
	if len(header) == 0 {
		return nil
	}
	out := make(map[string]string, len(header))
	for name, values := range header {
		switch strings.ToLower(name) {
		case "host", "content-length", "content-type", "connection", "transfer-encoding",
			"upgrade", "te", "trailer", "proxy-authorization", "proxy-authenticate":
			continue
		}
		if len(values) == 0 {
			continue
		}
		out[name] = strings.Join(values, ", ")
	}
	return out
}

func shortBody(body []byte) string {
	text := strings.TrimSpace(string(body))
	if len(text) > 200 {
		text = text[:200] + "…"
	}
	return text
}
