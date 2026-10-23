package appsync

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxResponseBytes = 8 << 20
	maxErrorRunes    = 300
	maxRedirects     = 10
)

// newHTTPClient builds the per-application HTTP client. When the caller
// supplies an http.Client (tests, custom transports) its transport, jar and
// redirect handler are preserved. Every redirect is checked: credentials are
// stripped as soon as the destination host differs from the original request.
func newHTTPClient(src *http.Client, timeout time.Duration) *http.Client {
	client := &http.Client{Timeout: timeout}
	var originalCheck func(*http.Request, []*http.Request) error
	if src != nil {
		client.Transport = src.Transport
		client.Jar = src.Jar
		originalCheck = src.CheckRedirect
	}

	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) > 0 && !sameHost(via[0].URL, req.URL) {
			stripCredentials(req)
		}
		if originalCheck != nil {
			return originalCheck(req, via)
		}
		if len(via) >= maxRedirects {
			return errors.New("appsync: stopped after 10 redirects")
		}
		return nil
	}
	return client
}

func sameHost(a, b *url.URL) bool {
	if a == nil || b == nil {
		return false
	}
	return strings.EqualFold(a.Host, b.Host)
}

// stripCredentials removes the application API key from a redirect before the
// request leaves for another host. Servarr uses the X-Api-Key header; Mylar3
// and LazyLibrarian put apikey/api_key in the query string.
func stripCredentials(req *http.Request) {
	req.Header.Del("X-Api-Key")
	req.Header.Del("Authorization")

	query := req.URL.Query()
	changed := false
	for _, key := range []string{"apikey", "api_key"} {
		if query.Has(key) {
			query.Del(key)
			changed = true
		}
	}
	if changed {
		req.URL.RawQuery = query.Encode()
	}
}

// execute performs one request and reads a bounded response body.
func execute(ctx context.Context, client *http.Client, req *http.Request, secret string) (int, []byte, error) {
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("%s %s: %s", req.Method, req.URL.Path, redact(err.Error(), secret))
	}
	defer resp.Body.Close()

	body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if readErr != nil {
		return resp.StatusCode, nil,
			fmt.Errorf("%s %s: read response: %s", req.Method, req.URL.Path, redact(readErr.Error(), secret))
	}
	return resp.StatusCode, body, nil
}

// apiError formats a non-2xx response. The API key is redacted before the
// body is truncated, and the detail is capped at 300 characters.
func apiError(op string, status int, body []byte, secret string) error {
	detail := strings.TrimSpace(redact(string(body), secret))
	detail = truncateRunes(detail, maxErrorRunes)
	if detail == "" {
		return fmt.Errorf("%s: HTTP %d", op, status)
	}
	return fmt.Errorf("%s: HTTP %d: %s", op, status, detail)
}

func redact(text, secret string) string {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return text
	}
	replacements := []string{
		secret,
		url.QueryEscape(secret),
		url.PathEscape(secret),
		url.QueryEscape(url.QueryEscape(secret)),
	}
	for _, replacement := range replacements {
		if replacement != "" {
			text = strings.ReplaceAll(text, replacement, "[REDACTED]")
		}
	}
	return text
}

func truncateRunes(text string, limit int) string {
	if limit <= 0 || utf8.RuneCountInString(text) <= limit {
		return text
	}
	runes := []rune(text)
	return string(runes[:limit])
}

func isSuccess(status int) bool {
	return status >= 200 && status < 300
}
