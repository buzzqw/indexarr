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
	"time"
)

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

// Get asks FlareSolverr to fetch rawURL and returns the solved body and the
// status of the solved page. endpoint may be a base URL or a /v1 URL.
func Get(ctx context.Context, client *http.Client, endpoint, rawURL string, timeout time.Duration) ([]byte, int, error) {
	command := Endpoint(endpoint)
	if command == "" {
		return nil, 0, fmt.Errorf("flaresolverr: empty endpoint")
	}
	if strings.TrimSpace(rawURL) == "" {
		return nil, 0, fmt.Errorf("flaresolverr: empty URL")
	}
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	if client == nil {
		client = http.DefaultClient
	}
	payload, err := json.Marshal(map[string]any{
		"cmd":        "request.get",
		"url":        rawURL,
		"maxTimeout": int(timeout / time.Millisecond),
	})
	if err != nil {
		return nil, 0, err
	}
	requestCtx, cancel := context.WithTimeout(ctx, timeout+15*time.Second)
	defer cancel()
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

func shortBody(body []byte) string {
	text := strings.TrimSpace(string(body))
	if len(text) > 200 {
		text = text[:200] + "…"
	}
	return text
}
