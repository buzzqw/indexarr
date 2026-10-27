// Package downloadclient sends a cached release to a download client.
//
// qBittorrent is driven through its Web API v2, Transmission through its JSON
// RPC endpoint, Deluge through its JSON-RPC API, rTorrent through XML-RPC,
// SABnzbd through its HTTP API and NZBGet through its JSON-RPC API. All are
// optional: when the feature is disabled the release page hides the button.
package downloadclient

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"

	"github.com/buzzqw/indexarr/internal/store"
)

const (
	KindNone         = "none"
	KindQBittorrent  = "qbittorrent"
	KindTransmission = "transmission"
	KindDeluge       = "deluge"
	KindRTorrent     = "rtorrent"
	KindSABnzbd      = "sabnzbd"
	KindNZBGet       = "nzbget"
)

// Config is the saved download-client configuration. SABnzbd uses APIKey as
// its API key; when empty, Password is used for backwards compatibility.
type Config struct {
	Kind     string
	URL      string
	Username string
	Password string
	APIKey   string
	Category string
	SavePath string
	Paused   bool
}

// Item is one download to hand to a client. Torrent clients use Magnet,
// Torrent or URL; Usenet clients use NZB or URL. Title is used to derive a
// filename and is never used as a credential.
type Item struct {
	Title   string
	Magnet  string
	Ed2k    string
	Torrent []byte
	NZB     []byte
	URL     string
}

// Client is a configured download client.
type Client interface {
	Send(ctx context.Context, item Item) error
	Test(ctx context.Context) (string, error)
}

// Tester is the connectivity-check half of Client. It is exposed separately so
// callers that only need to add downloads can accept a narrower interface.
type Tester interface {
	Test(ctx context.Context) (string, error)
}

// New builds a client for the configured kind. The returned Client owns its
// own HTTP client and cookie jar; it is safe for concurrent use.
func New(cfg Config) (Client, error) {
	switch normalizeKind(cfg.Kind) {
	case KindQBittorrent:
		return &clientFuncs{
			cfg:  cfg,
			send: addQBittorrentItem,
			test: testQBittorrent,
		}, nil
	case KindTransmission:
		return &clientFuncs{
			cfg:  cfg,
			send: addTransmissionItem,
			test: testTransmission,
		}, nil
	case KindDeluge:
		return newDeluge(cfg)
	case KindRTorrent:
		return newRTorrent(cfg)
	case KindSABnzbd:
		return newSABnzbd(cfg)
	case KindNZBGet:
		return newNZBGet(cfg)
	case KindAmule:
		return newAmule(cfg)
	case "", KindNone:
		return nil, fmt.Errorf("no download client configured")
	default:
		return nil, fmt.Errorf("unknown download client %q", cfg.Kind)
	}
}

// Build is an alias for New, kept for callers that name the factory Build.
func Build(cfg Config) (Client, error) {
	return New(cfg)
}

// Send hands item to the configured client.
func Send(ctx context.Context, cfg Config, item Item) error {
	client, err := New(cfg)
	if err != nil {
		return err
	}
	return client.Send(ctx, item)
}

// Test checks connectivity and returns a short human-readable description of
// the remote client, such as "Deluge OK (3 torrents)".
func Test(ctx context.Context, cfg Config) (string, error) {
	client, err := New(cfg)
	if err != nil {
		return "", err
	}
	return client.Test(ctx)
}

// Add sends the release to the configured client. A stored .torrent is
// preferred; otherwise the magnet is used. For Usenet releases the generic
// payload is sent as an .nzb.
func Add(ctx context.Context, cfg Config, release store.Release) error {
	item := Item{
		Title:   release.Title,
		Magnet:  release.Magnet,
		Ed2k:    release.Ed2k,
		Torrent: release.Torrent,
	}
	if len(release.Payload) > 0 {
		if isUsenetPayload(release.Protocol, release.PayloadType) {
			item.NZB = release.Payload
		} else if len(item.Torrent) == 0 {
			item.Torrent = release.Payload
		}
	}
	return Send(ctx, cfg, item)
}

func isUsenetPayload(protocol, payloadType string) bool {
	if strings.EqualFold(strings.TrimSpace(protocol), "usenet") {
		return true
	}
	payloadType = strings.ToLower(strings.TrimSpace(payloadType))
	return strings.Contains(payloadType, "nzb") || strings.Contains(payloadType, "usenet")
}

func normalizeKind(kind string) string {
	return strings.ToLower(strings.TrimSpace(kind))
}

// clientFuncs adapts the existing function-style clients to Client.
type clientFuncs struct {
	cfg  Config
	send func(context.Context, Config, Item) error
	test func(context.Context, Config) (string, error)
}

var (
	_ Client = (*clientFuncs)(nil)
	_ Client = (*delugeClient)(nil)
	_ Client = (*rtorrentClient)(nil)
	_ Client = (*sabnzbdClient)(nil)
	_ Client = (*nzbgetClient)(nil)
	_ Client = (*amuleClient)(nil)

	_ Tester = (*clientFuncs)(nil)
	_ Tester = (*delugeClient)(nil)
	_ Tester = (*rtorrentClient)(nil)
	_ Tester = (*sabnzbdClient)(nil)
	_ Tester = (*nzbgetClient)(nil)
	_ Tester = (*amuleClient)(nil)
)

func (c *clientFuncs) Send(ctx context.Context, item Item) error {
	return c.send(ctx, c.cfg, item)
}

func (c *clientFuncs) Test(ctx context.Context) (string, error) {
	return c.test(ctx, c.cfg)
}

func addQBittorrentItem(ctx context.Context, cfg Config, item Item) error {
	return addQBittorrent(ctx, cfg, store.Release{
		Title:   item.Title,
		Magnet:  item.Magnet,
		Torrent: item.Torrent,
	})
}

func addTransmissionItem(ctx context.Context, cfg Config, item Item) error {
	return addTransmission(ctx, cfg, store.Release{
		Title:   item.Title,
		Magnet:  item.Magnet,
		Torrent: item.Torrent,
	})
}

func qbLogin(ctx context.Context, client *http.Client, base string, cfg Config) error {
	login := url.Values{}
	login.Set("username", cfg.Username)
	login.Set("password", cfg.Password)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		base+"/api/v2/auth/login", strings.NewReader(login.Encode()))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Referer", base)
	response, err := client.Do(request)
	if err != nil {
		return requestError("qBittorrent login", err, base, cfg.Password)
	}
	body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
	response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.EqualFold(strings.TrimSpace(string(body)), "Ok.") {
		return fmt.Errorf("qBittorrent login failed (HTTP %d): %s",
			response.StatusCode, redactText(string(body), 300, cfg.Password))
	}
	return nil
}

func testQBittorrent(ctx context.Context, cfg Config) (string, error) {
	base := strings.TrimRight(strings.TrimSpace(cfg.URL), "/")
	if base == "" {
		return "", fmt.Errorf("qBittorrent URL is empty")
	}
	jar, _ := cookiejar.New(nil)
	client := newHTTPClient(jar)
	if err := qbLogin(ctx, client, base, cfg); err != nil {
		return "", err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/v2/app/version", nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("Referer", base)
	response, err := client.Do(request)
	if err != nil {
		return "", requestError("qBittorrent version", err, base, cfg.Password)
	}
	body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("qBittorrent version failed (HTTP %d): %s",
			response.StatusCode, redactText(string(body), 300, cfg.Password))
	}
	version := strings.TrimSpace(string(body))
	if version == "" {
		version = "unknown"
	}
	return "qBittorrent " + version, nil
}

func addQBittorrent(ctx context.Context, cfg Config, release store.Release) error {
	base := strings.TrimRight(strings.TrimSpace(cfg.URL), "/")
	if base == "" {
		return fmt.Errorf("qBittorrent URL is empty")
	}
	if len(release.Torrent) == 0 && strings.TrimSpace(release.Magnet) == "" {
		return fmt.Errorf("release has neither a .torrent nor a magnet")
	}
	jar, _ := cookiejar.New(nil)
	client := newHTTPClient(jar)
	if err := qbLogin(ctx, client, base, cfg); err != nil {
		return err
	}

	var payload bytes.Buffer
	writer := multipart.NewWriter(&payload)
	if len(release.Torrent) > 0 {
		part, err := writer.CreateFormFile("torrents", torrentFilename(release.Title))
		if err != nil {
			return err
		}
		if _, err := part.Write(release.Torrent); err != nil {
			return err
		}
	} else {
		_ = writer.WriteField("urls", release.Magnet)
	}
	if cfg.Category != "" {
		_ = writer.WriteField("category", cfg.Category)
	}
	if cfg.SavePath != "" {
		_ = writer.WriteField("savepath", cfg.SavePath)
	}
	_ = writer.WriteField("paused", boolString(cfg.Paused))
	if err := writer.Close(); err != nil {
		return err
	}
	add, err := http.NewRequestWithContext(ctx, http.MethodPost,
		base+"/api/v2/torrents/add", &payload)
	if err != nil {
		return err
	}
	add.Header.Set("Content-Type", writer.FormDataContentType())
	add.Header.Set("Referer", base)
	added, err := client.Do(add)
	if err != nil {
		return requestError("qBittorrent add", err, base, cfg.Password)
	}
	addedBody, _ := io.ReadAll(io.LimitReader(added.Body, 4096))
	added.Body.Close()
	if added.StatusCode != http.StatusOK || !strings.EqualFold(strings.TrimSpace(string(addedBody)), "Ok.") {
		return fmt.Errorf("qBittorrent add failed (HTTP %d): %s",
			added.StatusCode, redactText(string(addedBody), 300, cfg.Password))
	}
	return nil
}

func addTransmission(ctx context.Context, cfg Config, release store.Release) error {
	endpoint, err := transmissionEndpoint(cfg.URL)
	if err != nil {
		return err
	}
	if len(release.Torrent) == 0 && strings.TrimSpace(release.Magnet) == "" {
		return fmt.Errorf("release has neither a .torrent nor a magnet")
	}
	arguments := map[string]any{"paused": cfg.Paused}
	if len(release.Torrent) > 0 {
		arguments["metainfo"] = base64.StdEncoding.EncodeToString(release.Torrent)
	} else {
		arguments["filename"] = release.Magnet
	}
	if cfg.SavePath != "" {
		arguments["download-dir"] = cfg.SavePath
	}
	if _, err := transmissionCall(ctx, cfg, endpoint, "torrent-add", arguments); err != nil {
		return err
	}
	return nil
}

func testTransmission(ctx context.Context, cfg Config) (string, error) {
	endpoint, err := transmissionEndpoint(cfg.URL)
	if err != nil {
		return "", err
	}
	raw, err := transmissionCall(ctx, cfg, endpoint, "session-get", map[string]any{})
	if err != nil {
		return "", err
	}
	var session struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(raw, &session); err != nil {
		return "", fmt.Errorf("Transmission response: %w", err)
	}
	if session.Version == "" {
		session.Version = "unknown"
	}
	return "Transmission " + session.Version, nil
}

func transmissionEndpoint(raw string) (string, error) {
	endpoint := strings.TrimRight(strings.TrimSpace(raw), "/")
	if endpoint == "" {
		return "", fmt.Errorf("Transmission URL is empty")
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return "", fmt.Errorf("Transmission URL must be an absolute http:// or https:// address")
	}
	if parsed.Path == "" || parsed.Path == "/" {
		parsed.Path = "/transmission/rpc"
	}
	return parsed.String(), nil
}

func transmissionCall(ctx context.Context, cfg Config, endpoint, method string, arguments map[string]any) (json.RawMessage, error) {
	body, err := json.Marshal(map[string]any{
		"method":    method,
		"arguments": arguments,
	})
	if err != nil {
		return nil, err
	}
	client := newHTTPClient(nil)
	session := ""
	for attempt := 0; attempt < 2; attempt++ {
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		request.Header.Set("Content-Type", "application/json")
		if session != "" {
			request.Header.Set("X-Transmission-Session-Id", session)
		}
		if cfg.Username != "" {
			request.SetBasicAuth(cfg.Username, cfg.Password)
		}
		response, err := client.Do(request)
		if err != nil {
			return nil, requestError("Transmission", err, endpoint, cfg.Password)
		}
		if response.StatusCode == http.StatusConflict {
			session = response.Header.Get("X-Transmission-Session-Id")
			response.Body.Close()
			if session == "" {
				return nil, fmt.Errorf("Transmission did not return a session id")
			}
			continue
		}
		payload, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("Transmission HTTP %d: %s",
				response.StatusCode, redactText(string(payload), 300, cfg.Password))
		}
		var rpc struct {
			Result    string          `json:"result"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(payload, &rpc); err != nil {
			return nil, fmt.Errorf("Transmission response: %w", err)
		}
		if rpc.Result != "success" {
			return nil, fmt.Errorf("Transmission: %s", redactText(rpc.Result, 300, cfg.Password))
		}
		return rpc.Arguments, nil
	}
	return nil, fmt.Errorf("Transmission: session negotiation failed")
}

func torrentFilename(title string) string {
	return safeFilename(title, "release") + ".torrent"
}

func nzbFilename(title string) string {
	name := safeFilename(title, "release")
	if strings.HasSuffix(strings.ToLower(name), ".nzb") {
		return name
	}
	return name + ".nzb"
}

func safeFilename(title, fallback string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		title = fallback
	}
	return strings.Map(func(r rune) rune {
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|':
			return '_'
		}
		return r
	}, title)
}

func boolString(value bool) string {
	if value {
		return "true"
	}
	return "false"
}
