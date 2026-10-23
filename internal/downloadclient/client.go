// Package downloadclient sends a cached release to qBittorrent or Transmission.
// qBittorrent is driven through its Web API v2; Transmission through its JSON
// RPC endpoint. Both are optional: when the feature is disabled the release
// page hides the button.
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
	"time"

	"github.com/buzzqw/indexarr/internal/store"
)

const (
	KindNone         = "none"
	KindQBittorrent  = "qbittorrent"
	KindTransmission = "transmission"
)

// Config is the saved download-client configuration.
type Config struct {
	Kind     string
	URL      string
	Username string
	Password string
	Category string
	SavePath string
	Paused   bool
}

// Add sends the release to the configured client. A stored .torrent is
// preferred; otherwise the magnet is used.
func Add(ctx context.Context, cfg Config, release store.Release) error {
	switch strings.ToLower(strings.TrimSpace(cfg.Kind)) {
	case KindQBittorrent:
		return addQBittorrent(ctx, cfg, release)
	case KindTransmission:
		return addTransmission(ctx, cfg, release)
	case "", KindNone:
		return fmt.Errorf("no download client configured")
	default:
		return fmt.Errorf("unknown download client %q", cfg.Kind)
	}
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
	client := &http.Client{Jar: jar, Timeout: 30 * time.Second}

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
		return fmt.Errorf("qBittorrent login: %w", err)
	}
	body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
	response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.EqualFold(strings.TrimSpace(string(body)), "Ok.") {
		return fmt.Errorf("qBittorrent login failed (HTTP %d): %s", response.StatusCode, strings.TrimSpace(string(body)))
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
		return fmt.Errorf("qBittorrent add: %w", err)
	}
	addedBody, _ := io.ReadAll(io.LimitReader(added.Body, 4096))
	added.Body.Close()
	if added.StatusCode != http.StatusOK || !strings.EqualFold(strings.TrimSpace(string(addedBody)), "Ok.") {
		return fmt.Errorf("qBittorrent add failed (HTTP %d): %s", added.StatusCode, strings.TrimSpace(string(addedBody)))
	}
	return nil
}

func addTransmission(ctx context.Context, cfg Config, release store.Release) error {
	endpoint := strings.TrimRight(strings.TrimSpace(cfg.URL), "/")
	if endpoint == "" {
		return fmt.Errorf("Transmission URL is empty")
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return fmt.Errorf("Transmission URL must be an absolute http:// or https:// address")
	}
	if parsed.Path == "" || parsed.Path == "/" {
		parsed.Path = "/transmission/rpc"
	}
	endpoint = parsed.String()
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
	body, err := json.Marshal(map[string]any{
		"method":    "torrent-add",
		"arguments": arguments,
	})
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 30 * time.Second}
	session := ""
	for attempt := 0; attempt < 2; attempt++ {
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return err
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
			return fmt.Errorf("Transmission: %w", err)
		}
		if response.StatusCode == http.StatusConflict {
			session = response.Header.Get("X-Transmission-Session-Id")
			response.Body.Close()
			if session == "" {
				return fmt.Errorf("Transmission did not return a session id")
			}
			continue
		}
		payload, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return fmt.Errorf("Transmission HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(payload)))
		}
		var rpc struct {
			Result string `json:"result"`
		}
		if err := json.Unmarshal(payload, &rpc); err != nil {
			return fmt.Errorf("Transmission response: %w", err)
		}
		if rpc.Result != "success" {
			return fmt.Errorf("Transmission: %s", rpc.Result)
		}
		return nil
	}
	return fmt.Errorf("Transmission: session negotiation failed")
}

func torrentFilename(title string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		title = "release"
	}
	return strings.Map(func(r rune) rune {
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|':
			return '_'
		}
		return r
	}, title) + ".torrent"
}

func boolString(value bool) string {
	if value {
		return "true"
	}
	return "false"
}
