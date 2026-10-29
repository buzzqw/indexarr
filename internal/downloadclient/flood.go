package downloadclient

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// floodClient speaks the Flood REST API under /api. It authenticates with a
// JSON POST to /api/auth/authenticate and keeps the session cookie in its jar.
type floodClient struct {
	cfg      Config
	endpoint string
	http     *http.Client
}

func newFlood(cfg Config) (*floodClient, error) {
	endpoint, err := endpointWithSuffix(cfg.URL, "/api", "Flood")
	if err != nil {
		return nil, err
	}
	return &floodClient{cfg: cfg, endpoint: endpoint, http: newHTTPClient(nil)}, nil
}

func (c *floodClient) Send(ctx context.Context, item Item) error {
	if err := checkItemSize(item); err != nil {
		return err
	}
	if err := c.login(ctx); err != nil {
		return err
	}
	body := map[string]any{}
	tags := []string{}
	if strings.TrimSpace(c.cfg.Category) != "" {
		tags = append(tags, c.cfg.Category)
	}
	body["tags"] = tags
	if strings.TrimSpace(c.cfg.SavePath) != "" {
		body["destination"] = c.cfg.SavePath
	}
	if !c.cfg.Paused {
		body["start"] = true
	}
	switch {
	case len(item.Torrent) > 0:
		body["files"] = []string{base64.StdEncoding.EncodeToString(item.Torrent)}
		return c.post(ctx, "/torrents/add-files", body)
	case strings.TrimSpace(item.Magnet) != "":
		body["urls"] = []string{item.Magnet}
		return c.post(ctx, "/torrents/add-urls", body)
	case strings.TrimSpace(item.URL) != "":
		body["urls"] = []string{item.URL}
		return c.post(ctx, "/torrents/add-urls", body)
	default:
		return fmt.Errorf("Flood needs a .torrent file, a magnet link or a URL")
	}
}

func (c *floodClient) Test(ctx context.Context) (string, error) {
	if err := c.login(ctx); err != nil {
		return "", err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.endpoint+"/auth/verify", nil)
	if err != nil {
		return "", err
	}
	response, err := c.http.Do(request)
	if err != nil {
		return "", requestError("Flood verify", err, c.endpoint, c.cfg.Password)
	}
	body := readBody(response.Body, 1<<20)
	response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return "", fmt.Errorf("Flood authentication failed")
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return "", httpFailure("Flood verify", response, body, c.cfg.Password)
	}
	return "Flood OK", nil
}

func (c *floodClient) login(ctx context.Context) error {
	payload, err := json.Marshal(map[string]string{
		"username": c.cfg.Username,
		"password": c.cfg.Password,
	})
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.endpoint+"/auth/authenticate", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return requestError("Flood login", err, c.endpoint, c.cfg.Password)
	}
	body := readBody(response.Body, 1<<20)
	response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return fmt.Errorf("Flood authentication failed")
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return httpFailure("Flood login", response, body, c.cfg.Password)
	}
	return nil
}

func (c *floodClient) post(ctx context.Context, path string, body any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.endpoint+path, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return requestError("Flood "+path, err, c.endpoint, c.cfg.Password)
	}
	responseBody := readBody(response.Body, 1<<20)
	response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return fmt.Errorf("Flood authentication failed")
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return httpFailure("Flood add", response, responseBody, c.cfg.Password)
	}
	return nil
}
