package downloadclient

import (
	"context"
	"net/http"
	"net/http/cookiejar"
	"strings"
)

// gexttoClient talks to the qBittorrent-compatible Web API v2 served by
// gx-torrent (the standalone torrent client shipped with Gextto) and by
// Gextto's own torrent engine.
//
// Unlike qBittorrent, the daemon answers the login page with "Fails." when no
// Web UI password is configured, while the API is already open to the LAN. The
// client therefore logs in only when a password is present and sends a
// configured API key as X-Api-Key instead of a cookie session.
type gexttoClient struct {
	cfg Config
}

func newGextto(cfg Config) (*gexttoClient, error) {
	if _, err := parseBaseURL(cfg.URL, "Gextto / gx-torrent"); err != nil {
		return nil, err
	}
	return &gexttoClient{cfg: cfg}, nil
}

func (c *gexttoClient) base() string {
	return strings.TrimRight(strings.TrimSpace(c.cfg.URL), "/")
}

func (c *gexttoClient) httpClient() *http.Client {
	jar, _ := cookiejar.New(nil)
	client := newHTTPClient(jar)
	if key := strings.TrimSpace(c.cfg.APIKey); key != "" {
		client.Transport = &apiKeyTransport{base: http.DefaultTransport, key: key}
	}
	return client
}

func (c *gexttoClient) login(ctx context.Context, client *http.Client) error {
	// With an API key the daemon accepts every request without a session.
	if strings.TrimSpace(c.cfg.APIKey) != "" {
		return nil
	}
	// An empty password means the Web UI is not protected: a login would be
	// answered with "Fails." even though the API works.
	if strings.TrimSpace(c.cfg.Password) == "" {
		return nil
	}
	return qbLoginAs(ctx, client, c.base(), "Gextto / gx-torrent", c.cfg)
}

func (c *gexttoClient) Send(ctx context.Context, item Item) error {
	client := c.httpClient()
	if err := c.login(ctx, client); err != nil {
		return err
	}
	return qbAdd(ctx, client, c.base(), "Gextto / gx-torrent", c.cfg, item)
}

func (c *gexttoClient) Queue(ctx context.Context) ([]QueueItem, error) {
	client := c.httpClient()
	if err := c.login(ctx, client); err != nil {
		return nil, err
	}
	return qbQueue(ctx, client, c.base(), c.cfg)
}

func (c *gexttoClient) Test(ctx context.Context) (string, error) {
	client := c.httpClient()
	if err := c.login(ctx, client); err != nil {
		return "", err
	}
	version, err := qbVersion(ctx, client, c.base(), "Gextto / gx-torrent", c.cfg)
	if err != nil {
		return "", err
	}
	return "Gextto / gx-torrent " + version, nil
}

// apiKeyTransport adds the API key to every request. The header is what
// gx-torrent checks (X-Api-Key or Authorization: Bearer).
type apiKeyTransport struct {
	base http.RoundTripper
	key  string
}

func (t *apiKeyTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	clone := request.Clone(request.Context())
	clone.Header.Set("X-Api-Key", t.key)
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(clone)
}
