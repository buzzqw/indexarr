package appsync

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// simpleClient covers Mylar3 and LazyLibrarian. Both expose a version command
// that is enough for a connectivity test. Neither has a documented, verified
// API for managing Torznab indexers, so the indexer methods deliberately fail
// with ErrUnsupported rather than inventing endpoints.
type simpleClient struct {
	kind   Kind
	root   string
	apiKey string
	http   *http.Client
}

func newSimple(cfg Config, root string) *simpleClient {
	return &simpleClient{
		kind:   cfg.Kind,
		root:   root,
		apiKey: cfg.APIKey,
		http:   cfg.HTTPClient,
	}
}

// Test calls GET /api?cmd=getVersion&apikey=... and tolerates the different
// JSON envelopes the two applications use.
func (c *simpleClient) Test(ctx context.Context) (string, error) {
	op := fmt.Sprintf("appsync: %s test", c.kind)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.versionURL(), nil)
	if err != nil {
		return "", fmt.Errorf("%s: %w", op, err)
	}
	req.Header.Set("Accept", "application/json")

	status, body, err := execute(ctx, c.http, req, c.apiKey)
	if err != nil {
		return "", fmt.Errorf("%s: %s", op, err)
	}
	if !isSuccess(status) {
		return "", apiError(op, status, body, c.apiKey)
	}

	version := versionFromBody(body)
	if version == "" {
		return "", fmt.Errorf("%s: no version in response", op)
	}
	return c.kind.displayName() + " " + version, nil
}

// ListIndexers is not implemented for Mylar3/LazyLibrarian yet.
func (c *simpleClient) ListIndexers(ctx context.Context) ([]RemoteIndexer, error) {
	return nil, c.unsupported("list indexers")
}

// AddIndexer is not implemented for Mylar3/LazyLibrarian yet.
func (c *simpleClient) AddIndexer(ctx context.Context, spec IndexerSpec) (RemoteIndexer, error) {
	return RemoteIndexer{}, c.unsupported("add indexer")
}

// UpdateIndexer is not implemented for Mylar3/LazyLibrarian yet.
func (c *simpleClient) UpdateIndexer(ctx context.Context, remoteID int, spec IndexerSpec) (RemoteIndexer, error) {
	return RemoteIndexer{}, c.unsupported("update indexer")
}

// DeleteIndexer is not implemented for Mylar3/LazyLibrarian yet.
func (c *simpleClient) DeleteIndexer(ctx context.Context, remoteID int) error {
	return c.unsupported("delete indexer")
}

func (c *simpleClient) unsupported(operation string) error {
	return fmt.Errorf("%w: %s %s: the Torznab settings API has not been verified",
		ErrUnsupported, c.kind, operation)
}

// versionURL builds /api?cmd=getVersion&apikey=... without duplicating /api
// when the configured URL already includes it. The key stays in the query
// string, as required by both applications, and is stripped by the redirect
// policy if the request leaves the host.
func (c *simpleClient) versionURL() string {
	parsed, err := url.Parse(c.root)
	if err != nil {
		// New validated the URL; this is only a defensive fallback.
		return strings.TrimRight(c.root, "/") + "/api?cmd=getVersion"
	}
	path := strings.TrimRight(parsed.Path, "/")
	if !strings.HasSuffix(strings.ToLower(path), "/api") {
		path += "/api"
	}
	if path == "" {
		path = "/api"
	}
	parsed.Path = path

	query := parsed.Query()
	query.Set("cmd", "getVersion")
	if c.apiKey != "" {
		query.Set("apikey", c.apiKey)
	}
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

// versionFromBody extracts a version from the common response shapes:
//
//	{"version":"1.2.3"}
//	{"data":{"version":"1.2.3"}}
//	{"result":"1.2.3"}
//
// A short plain-text body is accepted as well, for builds that do not wrap
// the response in JSON.
func versionFromBody(body []byte) string {
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return ""
	}

	var payload any
	if err := json.Unmarshal(body, &payload); err != nil {
		if len(trimmed) > 200 || strings.HasPrefix(trimmed, "<") {
			return ""
		}
		return trimmed
	}
	return findVersion(payload, 0)
}

func findVersion(value any, depth int) string {
	if depth > 4 {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case json.Number:
		return typed.String()
	case map[string]any:
		for _, key := range []string{
			"version", "current_version", "currentVersion", "appVersion", "build",
		} {
			if version := findVersion(typed[key], depth+1); version != "" {
				return version
			}
		}
		for _, key := range []string{"data", "result", "response"} {
			if version := findVersion(typed[key], depth+1); version != "" {
				return version
			}
		}
	}
	return ""
}
