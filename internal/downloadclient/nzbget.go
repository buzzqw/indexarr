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

// nzbgetClient talks to the NZBGet JSON-RPC endpoint.
type nzbgetClient struct {
	cfg      Config
	endpoint string
	http     *http.Client
}

func newNZBGet(cfg Config) (*nzbgetClient, error) {
	endpoint, err := endpointWithSuffix(cfg.URL, "/jsonrpc", "NZBGet")
	if err != nil {
		return nil, err
	}
	return &nzbgetClient{cfg: cfg, endpoint: endpoint, http: newHTTPClient(nil)}, nil
}

func (c *nzbgetClient) Send(ctx context.Context, item Item) error {
	if len(item.NZB) > 0 {
		return c.append(ctx, item)
	}
	if strings.TrimSpace(item.URL) != "" {
		return c.appendURL(ctx, item)
	}
	return fmt.Errorf("NZBGet needs an .nzb or an NZB URL")
}

func (c *nzbgetClient) append(ctx context.Context, item Item) error {
	raw, err := c.call(ctx, "append", c.appendParams(nzbFilename(item.Title), item.NZB))
	if err != nil {
		return err
	}
	return nzbAppendResult(raw)
}

func (c *nzbgetClient) appendURL(ctx context.Context, item Item) error {
	params := c.appendParams(nzbFilename(item.Title), nil)
	params[1] = item.URL
	raw, err := c.call(ctx, "appendurl", params)
	if err != nil {
		return err
	}
	return nzbAppendResult(raw)
}

func (c *nzbgetClient) appendParams(name string, content []byte) []any {
	payload := ""
	if len(content) > 0 {
		payload = base64.StdEncoding.EncodeToString(content)
	}
	return []any{
		name,
		payload,
		c.cfg.Category,
		0, // priority: normal
		c.cfg.Paused,
		"",      // dupe key
		0,       // dupe score
		"SCORE", // dupe mode
	}
}

func (c *nzbgetClient) Test(ctx context.Context) (string, error) {
	raw, err := c.call(ctx, "version", nil)
	if err != nil {
		return "", err
	}
	var version string
	if err := json.Unmarshal(raw, &version); err != nil || version == "" {
		version = strings.Trim(strings.TrimSpace(string(raw)), `"`)
	}
	if version == "" || version == "null" {
		version = "unknown"
	}
	return "NZBGet " + version, nil
}

type nzbgetRequest struct {
	Method string `json:"method"`
	Params []any  `json:"params"`
	ID     int    `json:"id"`
}

type nzbgetError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type nzbgetResponse struct {
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error"`
	ID     int             `json:"id"`
}

func (c *nzbgetClient) call(ctx context.Context, method string, params []any) (json.RawMessage, error) {
	if params == nil {
		params = []any{}
	}
	payload, err := json.Marshal(nzbgetRequest{Method: method, Params: params, ID: 1})
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	if c.cfg.Username != "" {
		request.SetBasicAuth(c.cfg.Username, c.cfg.Password)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return nil, requestError("NZBGet "+method, err, c.endpoint, c.cfg.Password)
	}
	body := readBody(response.Body, 1<<20)
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, httpFailure("NZBGet "+method, response, body, c.cfg.Password)
	}
	var envelope nzbgetResponse
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("NZBGet response: %w", err)
	}
	if message := nzbErrorMessage(envelope.Error); message != "" {
		return nil, fmt.Errorf("NZBGet %s: %s",
			method, redactText(message, maxErrorBody, c.cfg.Password))
	}
	return envelope.Result, nil
}

func nzbErrorMessage(raw json.RawMessage) string {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" || trimmed == "false" {
		return ""
	}
	var rpcError nzbgetError
	if err := json.Unmarshal(raw, &rpcError); err == nil && rpcError.Message != "" {
		return rpcError.Message
	}
	var message string
	if err := json.Unmarshal(raw, &message); err == nil {
		return message
	}
	return trimmed
}

func nzbAppendResult(raw json.RawMessage) error {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" || trimmed == "false" {
		return fmt.Errorf("NZBGet add rejected")
	}
	var id int64
	if err := json.Unmarshal(raw, &id); err == nil && id <= 0 {
		return fmt.Errorf("NZBGet add rejected")
	}
	return nil
}
