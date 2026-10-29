package downloadclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// downloadStationClient talks to Synology Download Station through the
// documented auth.cgi login and DownloadStation/task.cgi create calls.
type downloadStationClient struct {
	cfg     Config
	authURL string
	taskURL string
	http    *http.Client
}

func newDownloadStation(cfg Config) (*downloadStationClient, error) {
	parsed, err := parseBaseURL(cfg.URL, "Synology Download Station")
	if err != nil {
		return nil, err
	}
	path := strings.TrimRight(parsed.Path, "/")
	if strings.HasSuffix(strings.ToLower(path), "/webapi") {
		path = path[:len(path)-len("/webapi")]
	}
	parsed.Path = path
	parsed.RawQuery = ""
	parsed.Fragment = ""
	base := strings.TrimRight(parsed.String(), "/")
	return &downloadStationClient{
		cfg:     cfg,
		authURL: base + "/webapi/auth.cgi",
		taskURL: base + "/webapi/DownloadStation/task.cgi",
		http:    newHTTPClient(nil),
	}, nil
}

func (c *downloadStationClient) Send(ctx context.Context, item Item) error {
	if err := checkItemSize(item); err != nil {
		return err
	}
	sid, err := c.login(ctx)
	if err != nil {
		return err
	}
	switch {
	case len(item.Torrent) > 0:
		return c.addFile(ctx, sid, safeTransferFilename(item.Title, "torrent"), item.Torrent)
	case len(item.NZB) > 0:
		return c.addFile(ctx, sid, safeTransferFilename(item.Title, "nzb"), item.NZB)
	case strings.TrimSpace(item.Magnet) != "":
		return c.addURI(ctx, sid, item.Magnet)
	case strings.TrimSpace(item.URL) != "":
		return c.addURI(ctx, sid, item.URL)
	default:
		return fmt.Errorf("Download Station needs a .torrent, an .nzb, a magnet link or a URL")
	}
}

func (c *downloadStationClient) addURI(ctx context.Context, sid, uri string) error {
	form := url.Values{}
	form.Set("api", "SYNO.DownloadStation.Task")
	form.Set("version", "3")
	form.Set("method", "create")
	form.Set("_sid", sid)
	form.Set("uri", uri)
	if strings.TrimSpace(c.cfg.SavePath) != "" {
		form.Set("destination", c.cfg.SavePath)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.taskURL, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return c.process(ctx, request, "Download Station create", sid)
}

func (c *downloadStationClient) addFile(ctx context.Context, sid, filename string, data []byte) error {
	var payload bytes.Buffer
	writer := multipart.NewWriter(&payload)
	for name, value := range map[string]string{
		"api":     "SYNO.DownloadStation.Task",
		"version": "2",
		"method":  "create",
		"_sid":    sid,
	} {
		_ = writer.WriteField(name, value)
	}
	if strings.TrimSpace(c.cfg.SavePath) != "" {
		_ = writer.WriteField("destination", c.cfg.SavePath)
	}
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		return err
	}
	if _, err := part.Write(data); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.taskURL, &payload)
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", writer.FormDataContentType())
	return c.process(ctx, request, "Download Station create", sid)
}

func (c *downloadStationClient) Test(ctx context.Context) (string, error) {
	sid, err := c.login(ctx)
	if err != nil {
		return "", err
	}
	form := url.Values{}
	form.Set("api", "SYNO.DownloadStation.Task")
	form.Set("version", "1")
	form.Set("method", "list")
	form.Set("_sid", sid)
	form.Set("additional", "detail,transfer")
	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.taskURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	body, err := c.do(ctx, request, "Download Station list", sid)
	if err != nil {
		return "", err
	}
	var response dsResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return "", fmt.Errorf("Download Station response: %w", err)
	}
	if !response.Success {
		return "", dsError("Download Station list", response.Error)
	}
	var data struct {
		Total int `json:"total"`
	}
	if err := json.Unmarshal(response.Data, &data); err != nil {
		return "Synology Download Station OK", nil
	}
	return "Synology Download Station OK (" + strconv.Itoa(data.Total) + " tasks)", nil
}

type dsAPIError struct {
	Code int `json:"code"`
}

type dsResponse struct {
	Success bool            `json:"success"`
	Data    json.RawMessage `json:"data"`
	Error   *dsAPIError     `json:"error"`
}

func (c *downloadStationClient) login(ctx context.Context) (string, error) {
	var last error
	for _, version := range []string{"6", "2"} {
		sid, err := c.loginVersion(ctx, version)
		if err == nil {
			return sid, nil
		}
		last = err
	}
	return "", last
}

func (c *downloadStationClient) loginVersion(ctx context.Context, version string) (string, error) {
	form := url.Values{}
	form.Set("api", "SYNO.API.Auth")
	form.Set("version", version)
	form.Set("method", "login")
	form.Set("account", c.cfg.Username)
	form.Set("passwd", c.cfg.Password)
	form.Set("session", "DownloadStation")
	form.Set("format", "sid")
	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.authURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	body, err := c.do(ctx, request, "Download Station login", "")
	if err != nil {
		return "", err
	}
	var response dsResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return "", fmt.Errorf("Download Station login response: %w", err)
	}
	if !response.Success {
		return "", dsError("Download Station login", response.Error)
	}
	var data struct {
		SID string `json:"sid"`
	}
	if err := json.Unmarshal(response.Data, &data); err != nil {
		return "", fmt.Errorf("Download Station login response: %w", err)
	}
	if data.SID == "" {
		return "", fmt.Errorf("Download Station login returned no session id")
	}
	return data.SID, nil
}

func (c *downloadStationClient) process(ctx context.Context, request *http.Request, operation, sid string) error {
	body, err := c.do(ctx, request, operation, sid)
	if err != nil {
		return err
	}
	var response dsResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return fmt.Errorf("Download Station response: %w", err)
	}
	if !response.Success {
		return dsError(operation, response.Error)
	}
	return nil
}

func (c *downloadStationClient) do(ctx context.Context, request *http.Request, operation, sid string) ([]byte, error) {
	response, err := c.http.Do(request)
	if err != nil {
		return nil, requestError(operation, err, c.taskURL, c.cfg.Password, sid)
	}
	body := readBody(response.Body, 1<<20)
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, httpFailure(operation, response, body, c.cfg.Password, sid)
	}
	return body, nil
}

func dsError(operation string, apiError *dsAPIError) error {
	if apiError == nil {
		return fmt.Errorf("%s failed", operation)
	}
	return fmt.Errorf("%s failed: Synology error %d", operation, apiError.Code)
}
