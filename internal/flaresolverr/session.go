package flaresolverr

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// SessionError is returned by the session commands. Its message never
// contains the request URL, which may embed a token or a passkey.
type SessionError struct {
	Command string
	Message string
}

func (e *SessionError) Error() string {
	if strings.TrimSpace(e.Message) == "" {
		return "flaresolverr: " + e.Command + " failed"
	}
	return "flaresolverr: " + e.Command + ": " + e.Message
}

type sessionReply struct {
	Status   string   `json:"status"`
	Message  string   `json:"message"`
	Sessions []string `json:"sessions"`
	Solution struct {
		Status int    `json:"status"`
		Body   string `json:"response"`
	} `json:"solution"`
}

// CreateSession asks FlareSolverr for a named browser session. A session keeps
// cookies and the Cloudflare clearance between requests, so a login survives.
func CreateSession(ctx context.Context, client *http.Client, endpoint, name string, timeout time.Duration) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return &SessionError{Command: "sessions.create", Message: "empty session name"}
	}
	_, err := sessionCommand(ctx, client, endpoint, map[string]any{
		"cmd":     "sessions.create",
		"session": name,
	}, timeout)
	return err
}

// DestroySession closes a browser session. Closing an unknown session is not
// an error for the caller: the goal is that it no longer exists.
func DestroySession(ctx context.Context, client *http.Client, endpoint, name string, timeout time.Duration) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	_, err := sessionCommand(ctx, client, endpoint, map[string]any{
		"cmd":     "sessions.destroy",
		"session": name,
	}, timeout)
	return err
}

// ListSessions returns the names of the browser sessions FlareSolverr owns.
func ListSessions(ctx context.Context, client *http.Client, endpoint string, timeout time.Duration) ([]string, error) {
	reply, err := sessionCommand(ctx, client, endpoint, map[string]any{
		"cmd": "sessions.list",
	}, timeout)
	if err != nil {
		return nil, err
	}
	return append([]string(nil), reply.Sessions...), nil
}

// SessionGet fetches rawURL inside session. It returns the solved page body
// and its status code.
func SessionGet(ctx context.Context, client *http.Client, endpoint, session, rawURL string, headers http.Header, timeout time.Duration) ([]byte, int, error) {
	return sessionRequest(ctx, client, endpoint, session, "request.get", rawURL, headers, "", timeout)
}

// SessionPost submits an application/x-www-form-urlencoded body inside session.
func SessionPost(ctx context.Context, client *http.Client, endpoint, session, rawURL string, headers http.Header, form url.Values, timeout time.Duration) ([]byte, int, error) {
	payload := form.Encode()
	headers = cloneHeader(headers)
	if headers.Get("Content-Type") == "" {
		headers.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	return sessionRequest(ctx, client, endpoint, session, "request.post", rawURL, headers, payload, timeout)
}

func sessionRequest(ctx context.Context, client *http.Client, endpoint, session, command, rawURL string, headers http.Header, postData string, timeout time.Duration) ([]byte, int, error) {
	if strings.TrimSpace(rawURL) == "" {
		return nil, 0, &SessionError{Command: command, Message: "empty URL"}
	}
	if strings.TrimSpace(session) == "" {
		return nil, 0, &SessionError{Command: command, Message: "empty session"}
	}
	payload := map[string]any{
		"cmd":          command,
		"url":          rawURL,
		"session":      session,
		"disableMedia": true,
	}
	if forward := forwardableHeaders(headers); len(forward) > 0 {
		payload["headers"] = forward
	}
	if postData != "" {
		payload["postData"] = postData
	}
	reply, err := sessionCommand(ctx, client, endpoint, payload, timeout)
	if err != nil {
		return nil, 0, err
	}
	if reply.Solution.Body == "" {
		return nil, 0, &SessionError{Command: command, Message: "empty solution"}
	}
	status := reply.Solution.Status
	if status <= 0 {
		status = http.StatusOK
	}
	return []byte(reply.Solution.Body), status, nil
}

func sessionCommand(ctx context.Context, client *http.Client, endpoint string, payload map[string]any, timeout time.Duration) (*sessionReply, error) {
	command := Endpoint(endpoint)
	if command == "" {
		return nil, &SessionError{Command: fmt.Sprint(payload["cmd"]), Message: "empty endpoint"}
	}
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	payload["maxTimeout"] = int(timeout / time.Millisecond)
	if client == nil {
		client = &http.Client{Timeout: timeout + 15*time.Second}
	}

	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	requestCtx, cancel := context.WithTimeout(ctx, timeout+15*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, command, bytes.NewReader(encoded))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")

	response, err := client.Do(request)
	if err != nil {
		return nil, &SessionError{Command: fmt.Sprint(payload["cmd"]), Message: "unreachable"}
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return nil, &SessionError{Command: fmt.Sprint(payload["cmd"]), Message: "read failed"}
	}
	if len(body) > maxResponseBytes {
		return nil, &SessionError{Command: fmt.Sprint(payload["cmd"]), Message: "response too large"}
	}
	var parsed sessionReply
	if err := json.Unmarshal(body, &parsed); err != nil {
		if response.StatusCode >= 400 {
			return nil, &SessionError{Command: fmt.Sprint(payload["cmd"]), Message: fmt.Sprintf("HTTP %d", response.StatusCode)}
		}
		return nil, &SessionError{Command: fmt.Sprint(payload["cmd"]), Message: "invalid reply"}
	}
	// FlareSolverr answers 500 with a JSON message when the browser cannot
	// open the page (net::ERR_NAME_NOT_RESOLVED…): keep that message, the
	// connector uses it to move to a mirror.
	if response.StatusCode >= 400 && strings.EqualFold(parsed.Status, "ok") {
		return nil, &SessionError{Command: fmt.Sprint(payload["cmd"]), Message: fmt.Sprintf("HTTP %d", response.StatusCode)}
	}
	if response.StatusCode >= 400 && strings.TrimSpace(parsed.Message) == "" {
		parsed.Message = fmt.Sprintf("HTTP %d", response.StatusCode)
	}
	if !strings.EqualFold(parsed.Status, "ok") {
		message := strings.TrimSpace(parsed.Message)
		if message == "" {
			message = parsed.Status
		}
		// FlareSolverr may echo a URL in a failure message; keep only the
		// first line and never the query string.
		if index := strings.IndexAny(message, "\r\n"); index >= 0 {
			message = message[:index]
		}
		if index := strings.Index(message, "?"); index >= 0 {
			message = message[:index]
		}
		return nil, &SessionError{Command: fmt.Sprint(payload["cmd"]), Message: message}
	}
	return &parsed, nil
}

func cloneHeader(header http.Header) http.Header {
	if header == nil {
		return make(http.Header)
	}
	return header.Clone()
}
