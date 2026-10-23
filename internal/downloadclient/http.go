package downloadclient

import (
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// defaultTimeout bounds every request to a download client. Callers may pass
// an earlier context deadline; this is only a safety net for a stuck server.
const defaultTimeout = 30 * time.Second

// maxErrorBody is the maximum amount of a response body reflected in errors.
const maxErrorBody = 300

var (
	credentialPattern  = regexp.MustCompile(`(?i)(apikey|api_key|password|passwd|pass|token|auth|_session_id)=([^&\s"']+)`)
	urlUserinfoPattern = regexp.MustCompile(`(?i)\b(https?://)[^/@\s"']+@`)
)

func newHTTPClient(jar http.CookieJar) *http.Client {
	if jar == nil {
		jar, _ = cookiejar.New(nil)
	}
	return &http.Client{Jar: jar, Timeout: defaultTimeout}
}

// redactText trims a server response for an error and removes secrets. It is
// used for every body reflected to a caller or log.
func redactText(text string, limit int, secrets ...string) string {
	text = strings.TrimSpace(text)
	text = urlUserinfoPattern.ReplaceAllString(text, "$1")
	text = credentialPattern.ReplaceAllString(text, "$1=***")
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		text = strings.ReplaceAll(text, secret, "***")
	}
	if limit > 0 && len(text) > limit {
		text = text[:limit] + "..."
	}
	return text
}

// safeURL hides userinfo and the query string, which on these APIs can carry
// an API key or a session id.
func safeURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "<invalid URL>"
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

// requestError keeps transport details while stripping the request URL (which
// may contain credentials) from the message.
func requestError(op string, err error, rawURL string, secrets ...string) error {
	message := err.Error()
	if rawURL != "" {
		message = strings.ReplaceAll(message, rawURL, safeURL(rawURL))
	}
	return fmt.Errorf("%s: %s", op, redactText(message, maxErrorBody, secrets...))
}

func httpFailure(op string, response *http.Response, body []byte, secrets ...string) error {
	return fmt.Errorf("%s failed (HTTP %d): %s",
		op, response.StatusCode, redactText(string(body), maxErrorBody, secrets...))
}

func readBody(body io.Reader, limit int64) []byte {
	payload, _ := io.ReadAll(io.LimitReader(body, limit))
	return payload
}

// parseBaseURL validates an absolute http(s) URL.
func parseBaseURL(raw, client string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("%s URL is empty", client)
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, fmt.Errorf("%s URL must be an absolute http:// or https:// address", client)
	}
	parsed.Fragment = ""
	return parsed, nil
}

// endpointWithSuffix appends a default API suffix unless the configured path
// already ends with it. A base path (for example /sabnzbd) is preserved.
func endpointWithSuffix(raw, suffix, client string) (string, error) {
	parsed, err := parseBaseURL(raw, client)
	if err != nil {
		return "", err
	}
	path := strings.TrimRight(parsed.Path, "/")
	if !strings.HasSuffix(strings.ToLower(path), strings.ToLower(suffix)) {
		path += suffix
	}
	parsed.Path = path
	parsed.RawQuery = ""
	return parsed.String(), nil
}
