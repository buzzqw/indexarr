package downloadclient

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
)

// delugeClient talks to the Deluge Web UI JSON-RPC endpoint.
type delugeClient struct {
	cfg      Config
	endpoint string
	http     *http.Client
}

func newDeluge(cfg Config) (*delugeClient, error) {
	endpoint, err := endpointWithSuffix(cfg.URL, "/json", "Deluge")
	if err != nil {
		return nil, err
	}
	jar, _ := cookiejar.New(nil)
	return &delugeClient{cfg: cfg, endpoint: endpoint, http: newHTTPClient(jar)}, nil
}

func (c *delugeClient) Send(ctx context.Context, item Item) error {
	if len(item.Torrent) == 0 && strings.TrimSpace(item.Magnet) == "" && strings.TrimSpace(item.URL) == "" {
		return fmt.Errorf("release has neither a .torrent nor a magnet")
	}
	if err := c.login(ctx); err != nil {
		return err
	}
	status, err := c.add(ctx, item)
	if err == nil {
		return nil
	}
	if status != http.StatusUnauthorized && status != http.StatusForbidden {
		return err
	}
	// The saved session expired between login and add: log in once more and
	// retry exactly once.
	if loginErr := c.relogin(ctx); loginErr != nil {
		return loginErr
	}
	_, err = c.add(ctx, item)
	return err
}

func (c *delugeClient) Test(ctx context.Context) (string, error) {
	if err := c.login(ctx); err != nil {
		return "", err
	}
	raw, _, err := c.call(ctx, "core.get_session_state", []any{})
	if err != nil {
		return "", err
	}
	var hashes []string
	if err := json.Unmarshal(raw, &hashes); err != nil {
		// Some daemons return a map keyed by info hash.
		var byHash map[string]any
		if mapErr := json.Unmarshal(raw, &byHash); mapErr != nil {
			return "", fmt.Errorf("Deluge get_session_state: %w", err)
		}
		return fmt.Sprintf("Deluge OK (%d torrents)", len(byHash)), nil
	}
	return fmt.Sprintf("Deluge OK (%d torrents)", len(hashes)), nil
}

func (c *delugeClient) login(ctx context.Context) error {
	return c.loginWith(ctx, false)
}

func (c *delugeClient) relogin(ctx context.Context) error {
	return c.loginWith(ctx, true)
}

func (c *delugeClient) loginWith(ctx context.Context, force bool) error {
	if !force && c.hasSessionCookie() {
		if raw, _, err := c.call(ctx, "auth.check_session", []any{}); err == nil {
			var valid bool
			if json.Unmarshal(raw, &valid) == nil && valid {
				return nil
			}
		}
	}
	raw, _, err := c.call(ctx, "auth.login", []any{c.cfg.Username, c.cfg.Password})
	if err != nil {
		return fmt.Errorf("Deluge login: %w", err)
	}
	var ok bool
	if err := json.Unmarshal(raw, &ok); err != nil {
		return fmt.Errorf("Deluge login response: %w", err)
	}
	if !ok {
		return fmt.Errorf("Deluge login failed")
	}
	return nil
}

func (c *delugeClient) hasSessionCookie() bool {
	if c.http.Jar == nil {
		return false
	}
	parsed, err := url.Parse(c.endpoint)
	if err != nil {
		return false
	}
	for _, cookie := range c.http.Jar.Cookies(parsed) {
		if cookie.Name == "_session_id" && cookie.Value != "" {
			return true
		}
	}
	return false
}

func (c *delugeClient) add(ctx context.Context, item Item) (int, error) {
	options := map[string]any{"add_paused": c.cfg.Paused}
	if c.cfg.SavePath != "" {
		options["download_location"] = c.cfg.SavePath
	}
	switch {
	case len(item.Torrent) > 0:
		_, status, err := c.call(ctx, "core.add_torrent_file", []any{
			torrentFilename(item.Title),
			base64.StdEncoding.EncodeToString(item.Torrent),
			options,
		})
		return status, err
	case strings.TrimSpace(item.Magnet) != "":
		_, status, err := c.call(ctx, "core.add_torrent_magnet", []any{item.Magnet, options})
		return status, err
	default:
		_, status, err := c.call(ctx, "core.add_torrent_url", []any{item.URL, options})
		return status, err
	}
}

type delugeRequest struct {
	Method string `json:"method"`
	Params []any  `json:"params"`
	ID     int    `json:"id"`
}

type delugeError struct {
	Message string `json:"message"`
	Code    int    `json:"code"`
}

type delugeResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *delugeError    `json:"error"`
	ID     int             `json:"id"`
}

func (c *delugeClient) call(ctx context.Context, method string, params []any) (json.RawMessage, int, error) {
	payload, err := json.Marshal(delugeRequest{Method: method, Params: params, ID: 1})
	if err != nil {
		return nil, 0, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, 0, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return nil, 0, requestError("Deluge "+method, err, c.endpoint, c.cfg.Password)
	}
	body := readBody(response.Body, 1<<20)
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, response.StatusCode, httpFailure("Deluge "+method, response, body, c.cfg.Password)
	}
	var envelope delugeResponse
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, response.StatusCode, fmt.Errorf("Deluge response: %w", err)
	}
	if envelope.Error != nil && envelope.Error.Message != "" {
		return nil, response.StatusCode, fmt.Errorf("Deluge %s: %s",
			method, redactText(envelope.Error.Message, maxErrorBody, c.cfg.Password))
	}
	return envelope.Result, response.StatusCode, nil
}
