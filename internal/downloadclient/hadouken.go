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

// hadoukenClient speaks JSON-RPC to Hadouken 5.1+ at /api. The WebUI methods
// take a torrent as a base64 file or as a URL, plus an options object.
type hadoukenClient struct {
	cfg      Config
	endpoint string
	http     *http.Client
}

func newHadouken(cfg Config) (*hadoukenClient, error) {
	endpoint, err := endpointWithSuffix(cfg.URL, "/api", "Hadouken")
	if err != nil {
		return nil, err
	}
	return &hadoukenClient{cfg: cfg, endpoint: endpoint, http: newHTTPClient(nil)}, nil
}

func (c *hadoukenClient) Send(ctx context.Context, item Item) error {
	if err := checkItemSize(item); err != nil {
		return err
	}
	options := map[string]string{}
	if strings.TrimSpace(c.cfg.Category) != "" {
		options["label"] = c.cfg.Category
	}
	switch {
	case len(item.Torrent) > 0:
		_, err := c.call(ctx, "webui.addTorrent", []any{
			"file", base64.StdEncoding.EncodeToString(item.Torrent), options,
		})
		return err
	case strings.TrimSpace(item.Magnet) != "":
		return c.addURL(ctx, item.Magnet, options)
	case strings.TrimSpace(item.URL) != "":
		return c.addURL(ctx, item.URL, options)
	default:
		return fmt.Errorf("Hadouken needs a .torrent file, a magnet link or a URL")
	}
}

func (c *hadoukenClient) addURL(ctx context.Context, uri string, options map[string]string) error {
	_, err := c.call(ctx, "webui.addTorrent", []any{"url", uri, options})
	return err
}

func (c *hadoukenClient) Test(ctx context.Context) (string, error) {
	raw, err := c.call(ctx, "core.getSystemInfo", []any{})
	if err != nil {
		return "", err
	}
	var info struct {
		Versions map[string]string `json:"versions"`
	}
	if err := json.Unmarshal(raw, &info); err != nil {
		return "", fmt.Errorf("Hadouken system info: %w", err)
	}
	version := info.Versions["hadouken"]
	if version == "" {
		return "Hadouken OK", nil
	}
	return "Hadouken " + version, nil
}

type hadoukenRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      string `json:"id"`
	Method  string `json:"method"`
	Params  []any  `json:"params"`
}

type hadoukenResponse struct {
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error"`
	ID     string          `json:"id"`
}

func (c *hadoukenClient) call(ctx context.Context, method string, params []any) (json.RawMessage, error) {
	payload, err := json.Marshal(hadoukenRequest{
		JSONRPC: "2.0",
		ID:      "indexarr",
		Method:  method,
		Params:  params,
	})
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	if c.cfg.Username != "" || c.cfg.Password != "" {
		request.SetBasicAuth(c.cfg.Username, c.cfg.Password)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return nil, requestError("Hadouken "+method, err, c.endpoint, c.cfg.Password)
	}
	body := readBody(response.Body, 1<<20)
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, httpFailure("Hadouken "+method, response, body, c.cfg.Password)
	}
	var envelope hadoukenResponse
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("Hadouken response: %w", err)
	}
	if message := hadoukenErrorMessage(envelope.Error); message != "" {
		return nil, fmt.Errorf("Hadouken %s: %s",
			method, redactText(message, maxErrorBody, c.cfg.Password))
	}
	return envelope.Result, nil
}

func hadoukenErrorMessage(raw json.RawMessage) string {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" || trimmed == "false" {
		return ""
	}
	var object struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(raw, &object); err == nil && object.Message != "" {
		return object.Message
	}
	var message string
	if err := json.Unmarshal(raw, &message); err == nil {
		return message
	}
	return trimmed
}
