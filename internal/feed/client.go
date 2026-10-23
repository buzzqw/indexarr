package feed

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/buzzqw/indexarr/internal/flaresolverr"
)

const maxFeedBody = 32 << 20

// RequestOptions carries everything needed to talk to an indexer: the user
// agent, the Torznab API key (sent both as query parameter and as X-Api-Key)
// and optional HTTP Basic credentials for private endpoints and feeds.
type RequestOptions struct {
	UserAgent    string
	APIKey       string
	Username     string
	Password     string
	FlareSolverr string
	Headers      map[string]string
}

// getBody downloads a URL with a hard size cap and checks the status.
func getBody(ctx context.Context, client *http.Client, rawURL string, opts RequestOptions) ([]byte, error) {
	body, status, err := getBodyStatus(ctx, client, rawURL, opts)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, describeHTTPError(status, body)
	}
	return body, nil
}

// getBodyStatus is getBody without the status check, for callers that want to
// fall back after a specific status (the caps and /status probes).
func getBodyStatus(ctx context.Context, client *http.Client, rawURL string, opts RequestOptions) ([]byte, int, error) {
	if client == nil {
		client = http.DefaultClient
	}
	request, err := newRequest(ctx, rawURL, opts)
	if err != nil {
		return nil, 0, err
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, 0, fmt.Errorf("%s", redactSecrets(err.Error()))
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxFeedBody+1))
	if err != nil {
		return nil, response.StatusCode, err
	}
	if len(body) > maxFeedBody {
		return nil, response.StatusCode, fmt.Errorf("response larger than %d bytes", maxFeedBody)
	}
	if opts.FlareSolverr != "" && !isLocalTarget(rawURL) &&
		isChallengeResponse(response.StatusCode, response.Header, body) {
		solved, solvedStatus, solveErr := flaresolverr.Get(ctx, client, opts.FlareSolverr,
			rawURL, flaresolverr.DefaultTimeout)
		if solveErr != nil {
			return nil, 0, fmt.Errorf("Cloudflare challenge; FlareSolverr failed: %w", solveErr)
		}
		return solved, solvedStatus, nil
	}
	return body, response.StatusCode, nil
}

// isChallengeResponse recognises the Cloudflare interstitial, not every 403:
// a wrong API key must surface as an authentication error, not as a 60-second
// FlareSolverr solve.
func isChallengeResponse(status int, header http.Header, body []byte) bool {
	if strings.EqualFold(strings.TrimSpace(header.Get("cf-mitigated")), "challenge") {
		return true
	}
	sample := body
	if len(sample) > 64<<10 {
		sample = sample[:64<<10]
	}
	lower := strings.ToLower(string(sample))
	marker := false
	for _, candidate := range []string{
		"just a moment", "cf-chl", "__cf_chl", "attention required", "cloudflare",
	} {
		if strings.Contains(lower, candidate) {
			marker = true
			break
		}
	}
	if !marker {
		return false
	}
	contentType := strings.ToLower(header.Get("Content-Type"))
	if status == http.StatusForbidden || status == http.StatusServiceUnavailable {
		return strings.Contains(contentType, "html") || contentType == ""
	}
	if status >= 200 && status < 300 {
		return strings.Contains(contentType, "html")
	}
	return false
}

// isLocalTarget keeps FlareSolverr away from LAN services: a challenge cannot
// come from a private address, and routing a local error through the solver
// would only hide the real cause.
func isLocalTarget(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "localhost" || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".lan") {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()
}

func newRequest(ctx context.Context, rawURL string, opts RequestOptions) (*http.Request, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	if opts.UserAgent != "" {
		request.Header.Set("User-Agent", opts.UserAgent)
	}
	for key, value := range opts.Headers {
		request.Header.Set(key, value)
	}
	if opts.APIKey != "" && request.Header.Get("X-Api-Key") == "" {
		request.Header.Set("X-Api-Key", opts.APIKey)
	}
	if opts.Username != "" {
		request.SetBasicAuth(opts.Username, opts.Password)
	}
	return request, nil
}

// withQuery merges values into an endpoint that may already carry a query
// string (for example an API key pasted together with the URL).
func withQuery(endpoint string, values url.Values) string {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		separator := "?"
		if strings.Contains(endpoint, "?") {
			separator = "&"
		}
		return endpoint + separator + values.Encode()
	}
	query := parsed.Query()
	for key, list := range values {
		if len(list) == 0 {
			continue
		}
		query.Del(key)
		for _, value := range list {
			query.Add(key, value)
		}
	}
	parsed.RawQuery = query.Encode()
	return parsed.String()
}
