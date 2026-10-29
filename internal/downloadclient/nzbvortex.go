package downloadclient

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"sync"
)

// nzbVortexClient talks to the NZBVortex API. Authentication is a nonce plus a
// SHA-256 hash of nonce, cnonce and the API key; the returned session id is
// sent with every add.
type nzbVortexClient struct {
	cfg      Config
	endpoint string
	http     *http.Client
	mu       sync.Mutex
	sid      string
}

func newNZBVortex(cfg Config) (*nzbVortexClient, error) {
	endpoint, err := endpointWithSuffix(cfg.URL, "/api", "NZBVortex")
	if err != nil {
		return nil, err
	}
	return &nzbVortexClient{cfg: cfg, endpoint: endpoint, http: newHTTPClient(nil)}, nil
}

func (c *nzbVortexClient) apiKey() string {
	return firstNonEmpty(c.cfg.APIKey, c.cfg.Password)
}

func (c *nzbVortexClient) Send(ctx context.Context, item Item) error {
	if err := checkItemSize(item); err != nil {
		return err
	}
	if len(item.NZB) == 0 {
		return fmt.Errorf("NZBVortex needs an .nzb file")
	}
	for attempt := 0; attempt < 2; attempt++ {
		sid, err := c.session(ctx)
		if err != nil {
			return err
		}
		result, err := c.add(ctx, sid, item)
		if err != nil {
			return err
		}
		switch strings.ToLower(strings.ReplaceAll(result, "_", "")) {
		case "", "ok":
			return nil
		case "notloggedin":
			c.invalidate()
			continue
		default:
			return fmt.Errorf("NZBVortex add failed: %s",
				redactText(result, maxErrorBody, c.apiKey(), c.cfg.Password))
		}
	}
	return fmt.Errorf("NZBVortex rejected the session twice")
}

func (c *nzbVortexClient) add(ctx context.Context, sid string, item Item) (string, error) {
	var payload bytes.Buffer
	writer := multipart.NewWriter(&payload)
	part, err := writer.CreateFormFile("name", safeTransferFilename(item.Title, "nzb"))
	if err != nil {
		return "", err
	}
	if _, err := part.Write(item.NZB); err != nil {
		return "", err
	}
	if err := writer.Close(); err != nil {
		return "", err
	}

	query := url.Values{}
	query.Set("priority", "0")
	query.Set("sessionid", sid)
	if strings.TrimSpace(c.cfg.Category) != "" {
		query.Set("groupname", c.cfg.Category)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.endpoint+"/nzb/add?"+query.Encode(), &payload)
	if err != nil {
		return "", err
	}
	request.Header.Set("Content-Type", writer.FormDataContentType())
	body, err := c.do(ctx, request, "NZBVortex add", sid)
	if err != nil {
		return "", err
	}
	var response struct {
		Result string `json:"result"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return "", fmt.Errorf("NZBVortex response: %w", err)
	}
	return response.Result, nil
}

func (c *nzbVortexClient) Test(ctx context.Context) (string, error) {
	if _, err := c.session(ctx); err != nil {
		return "", err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.endpoint+"/app/appversion", nil)
	if err != nil {
		return "", err
	}
	body, err := c.do(ctx, request, "NZBVortex version", "")
	if err != nil {
		return "", err
	}
	var response struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return "", fmt.Errorf("NZBVortex version response: %w", err)
	}
	if response.Version == "" {
		return "NZBVortex OK", nil
	}
	return "NZBVortex " + response.Version, nil
}

func (c *nzbVortexClient) session(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sid != "" {
		return c.sid, nil
	}
	return c.login(ctx)
}

func (c *nzbVortexClient) login(ctx context.Context) (string, error) {
	key := c.apiKey()
	if key == "" {
		return "", fmt.Errorf("NZBVortex needs an API key")
	}
	nonce, err := c.getNonce(ctx)
	if err != nil {
		return "", err
	}
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	cnonce := hex.EncodeToString(random)
	sum := sha256.Sum256([]byte(nonce + ":" + cnonce + ":" + key))
	query := url.Values{}
	query.Set("nonce", nonce)
	query.Set("cnonce", cnonce)
	query.Set("hash", base64.StdEncoding.EncodeToString(sum[:]))
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.endpoint+"/auth/login?"+query.Encode(), nil)
	if err != nil {
		return "", err
	}
	body, err := c.do(ctx, request, "NZBVortex login", "")
	if err != nil {
		return "", err
	}
	var response struct {
		Result      string `json:"result"`
		LoginResult string `json:"loginResult"`
		SessionID   string `json:"sessionId"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return "", fmt.Errorf("NZBVortex login response: %w", err)
	}
	if strings.EqualFold(strings.ReplaceAll(response.LoginResult, "_", ""), "failed") {
		return "", fmt.Errorf("NZBVortex authentication failed")
	}
	if response.SessionID == "" {
		return "", fmt.Errorf("NZBVortex login returned no session id")
	}
	c.sid = response.SessionID
	return c.sid, nil
}

func (c *nzbVortexClient) getNonce(ctx context.Context) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.endpoint+"/auth/nonce", nil)
	if err != nil {
		return "", err
	}
	body, err := c.do(ctx, request, "NZBVortex nonce", "")
	if err != nil {
		return "", err
	}
	var response struct {
		AuthNonce string `json:"authNonce"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return "", fmt.Errorf("NZBVortex nonce response: %w", err)
	}
	if response.AuthNonce == "" {
		return "", fmt.Errorf("NZBVortex returned an empty nonce")
	}
	return response.AuthNonce, nil
}

func (c *nzbVortexClient) invalidate() {
	c.mu.Lock()
	c.sid = ""
	c.mu.Unlock()
}

func (c *nzbVortexClient) do(ctx context.Context, request *http.Request, operation, sid string) ([]byte, error) {
	response, err := c.http.Do(request)
	if err != nil {
		return nil, requestError(operation, err, request.URL.String(), c.apiKey(), c.cfg.Password)
	}
	body := readBody(response.Body, 1<<20)
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, httpFailure(operation, response, body, c.apiKey(), c.cfg.Password, sid)
	}
	return body, nil
}
