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

// aria2Client talks to the aria2 JSON-RPC endpoint. The secret token travels in
// every RPC call as "token:<secret>", as aria2 requires; it is never put in the
// URL.
type aria2Client struct {
	cfg      Config
	endpoint string
	http     *http.Client
}

func newAria2(cfg Config) (*aria2Client, error) {
	endpoint, err := endpointWithSuffix(cfg.URL, "/jsonrpc", "Aria2")
	if err != nil {
		return nil, err
	}
	return &aria2Client{cfg: cfg, endpoint: endpoint, http: newHTTPClient(nil)}, nil
}

func (c *aria2Client) secret() string {
	return firstNonEmpty(c.cfg.APIKey, c.cfg.Password)
}

func (c *aria2Client) Send(ctx context.Context, item Item) error {
	if err := checkItemSize(item); err != nil {
		return err
	}
	options := map[string]string{}
	if strings.TrimSpace(c.cfg.SavePath) != "" {
		options["dir"] = c.cfg.SavePath
	}
	if c.cfg.Paused {
		options["pause"] = "true"
	}
	token := c.tokenParams()
	switch {
	case len(item.Torrent) > 0:
		params := append(token, base64.StdEncoding.EncodeToString(item.Torrent), []string{}, options)
		if _, err := c.call(ctx, "aria2.addTorrent", params); err != nil {
			return err
		}
		return nil
	case strings.TrimSpace(item.Magnet) != "":
		return c.addURI(ctx, item.Magnet, options, token)
	case strings.TrimSpace(item.URL) != "":
		return c.addURI(ctx, item.URL, options, token)
	default:
		return fmt.Errorf("Aria2 needs a .torrent file, a magnet link or a URL")
	}
}

func (c *aria2Client) addURI(ctx context.Context, uri string, options map[string]string, token []any) error {
	params := append(token, []string{uri}, options)
	_, err := c.call(ctx, "aria2.addUri", params)
	return err
}

func (c *aria2Client) tokenParams() []any {
	secret := c.secret()
	if secret == "" {
		return []any{}
	}
	return []any{"token:" + secret}
}

func (c *aria2Client) Test(ctx context.Context) (string, error) {
	raw, err := c.call(ctx, "aria2.getVersion", c.tokenParams())
	if err != nil {
		return "", err
	}
	var version struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(raw, &version); err != nil {
		return "", fmt.Errorf("Aria2 version response: %w", err)
	}
	if version.Version == "" {
		version.Version = "unknown"
	}
	return "Aria2 " + version.Version, nil
}

type aria2Request struct {
	JSONRPC string `json:"jsonrpc"`
	ID      string `json:"id"`
	Method  string `json:"method"`
	Params  []any  `json:"params"`
}

type aria2Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      string          `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (c *aria2Client) call(ctx context.Context, method string, params []any) (json.RawMessage, error) {
	payload, err := json.Marshal(aria2Request{
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
	response, err := c.http.Do(request)
	if err != nil {
		return nil, requestError("Aria2 "+method, err, c.endpoint, c.cfg.APIKey, c.cfg.Password)
	}
	body := readBody(response.Body, 1<<20)
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, httpFailure("Aria2 "+method, response, body, c.cfg.APIKey, c.cfg.Password)
	}
	var envelope aria2Response
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("Aria2 response: %w", err)
	}
	if envelope.Error != nil {
		return nil, fmt.Errorf("Aria2 %s: code %d: %s", method, envelope.Error.Code,
			redactText(envelope.Error.Message, maxErrorBody, c.cfg.APIKey, c.cfg.Password))
	}
	return envelope.Result, nil
}
