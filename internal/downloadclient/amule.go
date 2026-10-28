// aMule support: an eD2k client is added through the aMule Web API
// (amuleapi). The HTTP surface is a small REST interface: a password-only
// login sets a session cookie, then POST /api/v1/downloads queues one or more
// ed2k:// links. indexarr never speaks the binary External Connections
// protocol; amuled keeps doing the networking.
package downloadclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"
	"time"
)

const KindAmule = "amule"

// amuleClient is one configured aMule Web endpoint. The cookie jar keeps the
// session between Send and Test calls; a 401 triggers one new login.
type amuleClient struct {
	cfg  Config
	http *http.Client
	mu   sync.Mutex
	auth bool
}

func newAmule(cfg Config) (*amuleClient, error) {
	base := strings.TrimRight(strings.TrimSpace(cfg.URL), "/")
	if base == "" {
		return nil, fmt.Errorf("aMule URL is required")
	}
	parsed, err := url.Parse(base)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, fmt.Errorf("aMule URL must be an http or https URL")
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	return &amuleClient{
		cfg: Config{Kind: KindAmule, URL: base, Password: cfg.Password},
		http: &http.Client{
			Timeout: 30 * time.Second,
			Jar:     jar,
		},
	}, nil
}

func (a *amuleClient) endpoint(path string) string {
	return a.cfg.URL + "/api/v1/" + strings.TrimLeft(path, "/")
}

func (a *amuleClient) post(ctx context.Context, path string, body any) ([]byte, int, error) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, 0, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, a.endpoint(path), bytes.NewReader(encoded))
	if err != nil {
		return nil, 0, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := a.http.Do(request)
	if err != nil {
		return nil, 0, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return nil, response.StatusCode, err
	}
	return data, response.StatusCode, nil
}

func (a *amuleClient) get(ctx context.Context, path string) ([]byte, int, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, a.endpoint(path), nil)
	if err != nil {
		return nil, 0, err
	}
	request.Header.Set("Accept", "application/json")
	response, err := a.http.Do(request)
	if err != nil {
		return nil, 0, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return nil, response.StatusCode, err
	}
	return data, response.StatusCode, nil
}

// login performs the password-only authentication. amuleapi counts every 401
// against the calling IP and locks it out after 30 in a minute, so a failed
// login is reported and never retried here.
func (a *amuleClient) login(ctx context.Context) error {
	password := strings.TrimSpace(a.cfg.Password)
	if password == "" {
		return fmt.Errorf("aMule password is required")
	}
	data, status, err := a.post(ctx, "auth/login", map[string]string{"password": password})
	if err != nil {
		return fmt.Errorf("aMule login: %w", err)
	}
	if status != http.StatusOK {
		return fmt.Errorf("aMule login: %s", apiMessage(data, status))
	}
	a.auth = true
	return nil
}

func (a *amuleClient) ensureLogin(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.auth {
		return nil
	}
	return a.login(ctx)
}

func (a *amuleClient) Send(ctx context.Context, item Item) error {
	link := strings.TrimSpace(item.Ed2k)
	if link == "" {
		return fmt.Errorf("aMule: no eD2k link to add")
	}
	if !strings.HasPrefix(strings.ToLower(link), "ed2k://") {
		return fmt.Errorf("aMule: not an eD2k file link")
	}
	if err := a.ensureLogin(ctx); err != nil {
		return err
	}
	data, status, err := a.post(ctx, "downloads", map[string][]string{"links": {link}})
	if err != nil {
		return fmt.Errorf("aMule: add link: %w", err)
	}
	if status == http.StatusUnauthorized {
		// The session expired: log in once more and repeat the add.
		a.mu.Lock()
		a.auth = false
		loginErr := a.login(ctx)
		a.mu.Unlock()
		if loginErr != nil {
			return loginErr
		}
		data, status, err = a.post(ctx, "downloads", map[string][]string{"links": {link}})
		if err != nil {
			return fmt.Errorf("aMule: add link: %w", err)
		}
	}
	// amuleapi answers 202 Accepted when the link is only queued for the core:
	// treating it as an error dropped a grab that aMule had actually accepted.
	if status != http.StatusOK && status != http.StatusMultiStatus && status != http.StatusCreated && status != http.StatusAccepted {
		return fmt.Errorf("aMule: %s", apiMessage(data, status))
	}
	if failed := firstFailedResult(data); failed != "" {
		return fmt.Errorf("aMule: %s", failed)
	}
	return nil
}

func (a *amuleClient) Test(ctx context.Context) (string, error) {
	if err := a.ensureLogin(ctx); err != nil {
		return "", err
	}
	data, status, err := a.get(ctx, "version")
	if err != nil {
		return "", fmt.Errorf("aMule: %w", err)
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("aMule: %s", apiMessage(data, status))
	}
	var version struct {
		Daemon string `json:"daemon_version"`
		API    string `json:"amuleapi_version"`
	}
	_ = json.Unmarshal(data, &version)
	if version.Daemon != "" {
		return "aMule " + version.Daemon, nil
	}
	if version.API != "" {
		return "aMule API " + version.API, nil
	}
	return "aMule OK", nil
}

// apiMessage extracts the {error:{message}} envelope amuleapi returns.
func apiMessage(body []byte, status int) string {
	var envelope struct {
		Error struct {
			Message string `json:"message"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err == nil {
		if message := strings.TrimSpace(envelope.Error.Message); message != "" {
			return message
		}
		if code := strings.TrimSpace(envelope.Error.Code); code != "" {
			return code
		}
	}
	if status > 0 {
		return fmt.Sprintf("HTTP %d", status)
	}
	return "unknown error"
}

// firstFailedResult reads the bulk `results` envelope of POST /downloads.
func firstFailedResult(body []byte) string {
	var envelope struct {
		Results []struct {
			OK    bool `json:"ok"`
			Error struct {
				Message string `json:"message"`
				Code    string `json:"code"`
			} `json:"error"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return ""
	}
	for _, result := range envelope.Results {
		if result.OK {
			continue
		}
		if message := strings.TrimSpace(result.Error.Message); message != "" {
			return message
		}
		if code := strings.TrimSpace(result.Error.Code); code != "" {
			return code
		}
		return "link rejected"
	}
	return ""
}
