package downloadclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"mime/multipart"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
)

// utorrentClient drives the uTorrent/BitTorrent Web UI. The API needs a token
// from token.html first; the same basic credentials authenticate that page and
// every gui/ call.
type utorrentClient struct {
	cfg  Config
	gui  string
	http *http.Client
	mu   sync.Mutex
	tok  string
}

var utorrentTokenPattern = regexp.MustCompile(`(?is)id=["']token["'][^>]*>([^<]+)<`)

func newUTorrent(cfg Config) (*utorrentClient, error) {
	parsed, err := parseBaseURL(cfg.URL, "uTorrent")
	if err != nil {
		return nil, err
	}
	path := strings.TrimRight(parsed.Path, "/")
	if !strings.HasSuffix(strings.ToLower(path), "/gui") {
		path += "/gui"
	}
	parsed.Path = path
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return &utorrentClient{cfg: cfg, gui: parsed.String(), http: newHTTPClient(nil)}, nil
}

func (c *utorrentClient) Send(ctx context.Context, item Item) error {
	if err := checkItemSize(item); err != nil {
		return err
	}
	switch {
	case len(item.Torrent) > 0:
		return c.addFile(ctx, item)
	case strings.TrimSpace(item.Magnet) != "":
		return c.addURL(ctx, item.Magnet)
	case strings.TrimSpace(item.URL) != "":
		return c.addURL(ctx, item.URL)
	default:
		return fmt.Errorf("uTorrent needs a .torrent file, a magnet link or a URL")
	}
}

func (c *utorrentClient) addURL(ctx context.Context, uri string) error {
	for attempt := 0; attempt < 2; attempt++ {
		token, err := c.token(ctx)
		if err != nil {
			return err
		}
		values := url.Values{}
		values.Set("token", token)
		values.Set("action", "add-url")
		values.Set("s", uri)
		request, err := http.NewRequestWithContext(ctx, http.MethodGet,
			c.gui+"/?"+values.Encode(), nil)
		if err != nil {
			return err
		}
		response, err := c.do(request)
		if err != nil {
			return err
		}
		if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusBadRequest {
			response.Body.Close()
			c.invalidate()
			continue
		}
		body := readBody(response.Body, 1<<20)
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return httpFailure("uTorrent add-url", response, body, c.cfg.Password, token)
		}
		return nil
	}
	return fmt.Errorf("uTorrent rejected the token twice")
}

func (c *utorrentClient) addFile(ctx context.Context, item Item) error {
	var payload bytes.Buffer
	writer := multipart.NewWriter(&payload)
	part, err := writer.CreateFormFile("torrent_file", safeTransferFilename(item.Title, "torrent"))
	if err != nil {
		return err
	}
	if _, err := part.Write(item.Torrent); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	contentType := writer.FormDataContentType()
	for attempt := 0; attempt < 2; attempt++ {
		token, err := c.token(ctx)
		if err != nil {
			return err
		}
		values := url.Values{}
		values.Set("token", token)
		values.Set("action", "add-file")
		values.Set("path", "")
		request, err := http.NewRequestWithContext(ctx, http.MethodPost,
			c.gui+"/?"+values.Encode(), bytes.NewReader(payload.Bytes()))
		if err != nil {
			return err
		}
		request.Header.Set("Content-Type", contentType)
		response, err := c.do(request)
		if err != nil {
			return err
		}
		if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusBadRequest {
			response.Body.Close()
			c.invalidate()
			continue
		}
		body := readBody(response.Body, 1<<20)
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return httpFailure("uTorrent add-file", response, body, c.cfg.Password, token)
		}
		return nil
	}
	return fmt.Errorf("uTorrent rejected the token twice")
}

func (c *utorrentClient) Test(ctx context.Context) (string, error) {
	token, err := c.token(ctx)
	if err != nil {
		return "", err
	}
	values := url.Values{}
	values.Set("token", token)
	values.Set("action", "getsettings")
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.gui+"/?"+values.Encode(), nil)
	if err != nil {
		return "", err
	}
	response, err := c.do(request)
	if err != nil {
		return "", err
	}
	body := readBody(response.Body, 1<<20)
	response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusBadRequest {
		c.invalidate()
		return "", fmt.Errorf("uTorrent rejected the token")
	}
	if response.StatusCode != http.StatusOK {
		return "", httpFailure("uTorrent getsettings", response, body, c.cfg.Password, token)
	}
	var settings struct {
		Build json.Number `json:"build"`
	}
	if err := json.Unmarshal(body, &settings); err != nil {
		return "", fmt.Errorf("uTorrent response: %w", err)
	}
	if settings.Build.String() == "" {
		return "uTorrent OK", nil
	}
	return "uTorrent build " + settings.Build.String(), nil
}

func (c *utorrentClient) token(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.tok != "" {
		return c.tok, nil
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.gui+"/token.html", nil)
	if err != nil {
		return "", err
	}
	response, err := c.do(request)
	if err != nil {
		return "", err
	}
	body := readBody(response.Body, 1<<20)
	response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized {
		return "", fmt.Errorf("uTorrent authentication failed")
	}
	if response.StatusCode != http.StatusOK {
		return "", httpFailure("uTorrent token", response, body, c.cfg.Password)
	}
	match := utorrentTokenPattern.FindSubmatch(body)
	if match == nil {
		return "", fmt.Errorf("uTorrent token page did not contain a token")
	}
	c.tok = html.UnescapeString(strings.TrimSpace(string(match[1])))
	if c.tok == "" {
		return "", fmt.Errorf("uTorrent token page returned an empty token")
	}
	return c.tok, nil
}

func (c *utorrentClient) invalidate() {
	c.mu.Lock()
	c.tok = ""
	c.mu.Unlock()
}

func (c *utorrentClient) do(request *http.Request) (*http.Response, error) {
	if c.cfg.Username != "" || c.cfg.Password != "" {
		request.SetBasicAuth(c.cfg.Username, c.cfg.Password)
	}
	request.Header.Set("Cache-Control", "no-cache")
	request.Header.Set("Accept", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return nil, requestError("uTorrent", err, c.gui, c.cfg.Password)
	}
	return response, nil
}
