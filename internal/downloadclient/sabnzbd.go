package downloadclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
)

// sabnzbdClient talks to the SABnzbd HTTP API.
type sabnzbdClient struct {
	cfg      Config
	endpoint string
	http     *http.Client
}

func newSABnzbd(cfg Config) (*sabnzbdClient, error) {
	endpoint, err := endpointWithSuffix(cfg.URL, "/api", "SABnzbd")
	if err != nil {
		return nil, err
	}
	return &sabnzbdClient{cfg: cfg, endpoint: endpoint, http: newHTTPClient(nil)}, nil
}

func (c *sabnzbdClient) apiKey() string {
	if c.cfg.APIKey != "" {
		return c.cfg.APIKey
	}
	if c.cfg.Password != "" {
		return c.cfg.Password
	}
	return c.cfg.Username
}

func (c *sabnzbdClient) Send(ctx context.Context, item Item) error {
	if len(item.NZB) > 0 {
		return c.addFile(ctx, item)
	}
	if strings.TrimSpace(item.URL) != "" {
		return c.addURL(ctx, item)
	}
	return fmt.Errorf("SABnzbd needs an .nzb or an NZB URL")
}

func (c *sabnzbdClient) Test(ctx context.Context) (string, error) {
	query := url.Values{}
	query.Set("mode", "version")
	query.Set("t", "version")
	query.Set("output", "json")
	query.Set("apikey", c.apiKey())
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.endpoint+"?"+query.Encode(), nil)
	if err != nil {
		return "", err
	}
	body, err := c.do(request)
	if err != nil {
		return "", err
	}
	var response sabResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return "", fmt.Errorf("SABnzbd response: %w", err)
	}
	if response.Version == "" {
		return "", fmt.Errorf("SABnzbd version response is empty")
	}
	return "SABnzbd " + response.Version, nil
}

func (c *sabnzbdClient) addURL(ctx context.Context, item Item) error {
	form := c.baseForm("addurl")
	form.Set("name", item.URL)
	if title := strings.TrimSpace(item.Title); title != "" {
		form.Set("nzbname", title)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	body, err := c.do(request)
	if err != nil {
		return err
	}
	return c.checkAdd(body)
}

func (c *sabnzbdClient) addFile(ctx context.Context, item Item) error {
	var payload bytes.Buffer
	writer := multipart.NewWriter(&payload)
	form := c.baseForm("addfile")
	if title := strings.TrimSpace(item.Title); title != "" {
		form.Set("nzbname", title)
	}
	for name, value := range form {
		for _, entry := range value {
			_ = writer.WriteField(name, entry)
		}
	}
	part, err := writer.CreateFormFile("name", nzbFilename(item.Title))
	if err != nil {
		return err
	}
	if _, err := part.Write(item.NZB); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, &payload)
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", writer.FormDataContentType())
	body, err := c.do(request)
	if err != nil {
		return err
	}
	return c.checkAdd(body)
}

func (c *sabnzbdClient) baseForm(action string) url.Values {
	form := url.Values{}
	form.Set("mode", action)
	form.Set("t", action)
	form.Set("output", "json")
	form.Set("apikey", c.apiKey())
	if c.cfg.Category != "" {
		form.Set("cat", c.cfg.Category)
	}
	if strings.TrimSpace(c.cfg.SavePath) != "" {
		form.Set("dir", c.cfg.SavePath)
	}
	if c.cfg.Paused {
		// SABnzbd has no paused flag; priority -1 holds the job paused.
		form.Set("priority", "-1")
	}
	return form
}

func (c *sabnzbdClient) do(request *http.Request) ([]byte, error) {
	response, err := c.http.Do(request)
	if err != nil {
		return nil, requestError("SABnzbd", err, request.URL.String(), c.apiKey())
	}
	body := readBody(response.Body, 1<<20)
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, httpFailure("SABnzbd", response, body, c.apiKey())
	}
	return body, nil
}

type sabResponse struct {
	Status  *bool    `json:"status"`
	Error   string   `json:"error"`
	NZOIDs  []string `json:"nzo_ids"`
	Version string   `json:"version"`
}

func (c *sabnzbdClient) checkAdd(body []byte) error {
	var response sabResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return fmt.Errorf("SABnzbd response: %w", err)
	}
	if response.Status != nil && !*response.Status {
		message := response.Error
		if message == "" {
			message = "add rejected"
		}
		return fmt.Errorf("SABnzbd: %s", redactText(message, maxErrorBody, c.apiKey()))
	}
	if response.Error != "" && (response.Status == nil || !*response.Status) {
		return fmt.Errorf("SABnzbd: %s", redactText(response.Error, maxErrorBody, c.apiKey()))
	}
	if response.Status == nil && len(response.NZOIDs) == 0 {
		return fmt.Errorf("SABnzbd: add rejected")
	}
	return nil
}
