package feed

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Supported indexer managers.
const (
	ManagerAuto     = "auto"
	ManagerJackett  = "jackett"
	ManagerProwlarr = "prowlarr"
	ManagerMirCrew  = "mircrew"
	ManagerTorznab  = "torznab"
)

// ClientConfig describes one configured Torznab endpoint.
type ClientConfig struct {
	Name         string
	URL          string
	Manager      string
	APIKey       string
	Username     string
	Password     string
	FlareSolverr string
	Categories   []int
	UserAgent    string
	Timeout      time.Duration
	HTTPClient   *http.Client
}

// Client queries a Torznab indexer or a Prowlarr instance.
type Client struct {
	cfg      ClientConfig
	endpoint string
	manager  string
	http     *http.Client
}

// NewClient builds a client. The HTTP client is created with the configured
// timeout when none is supplied.
func NewClient(cfg ClientConfig) *Client {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 45 * time.Second
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: cfg.Timeout}
	}
	manager := effectiveManager(cfg.Manager, cfg.URL)
	return &Client{
		cfg:      cfg,
		manager:  manager,
		endpoint: Endpoint(cfg.URL, manager),
		http:     client,
	}
}

// Endpoint returns the URL the client queries.
func (c *Client) Endpoint() string { return c.endpoint }

// Manager returns the detected manager ("jackett", "prowlarr" or "torznab").
func (c *Client) Manager() string { return c.manager }

func (c *Client) options() RequestOptions {
	return RequestOptions{
		UserAgent:    c.cfg.UserAgent,
		APIKey:       c.cfg.APIKey,
		Username:     c.cfg.Username,
		Password:     c.cfg.Password,
		FlareSolverr: c.cfg.FlareSolverr,
	}
}

// Endpoint resolves the search endpoint from a user-supplied base URL. An
// explicit /api or /torznab path is kept as it is; a manager base becomes the
// manager's aggregate path; anything else is a direct Torznab service whose
// endpoint is <base>/api.
func Endpoint(base, manager string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		return ""
	}
	// Only the path decides whether the user already pasted an endpoint: a
	// host named "torznab.local" is still a base URL and needs /api.
	if parsed, err := url.Parse(base); err == nil {
		path := strings.ToLower(parsed.Path)
		if strings.Contains(path, "/api") || strings.Contains(path, "torznab") {
			return base
		}
	}
	switch effectiveManager(manager, base) {
	case ManagerProwlarr:
		return base + "/api/v1/search"
	case ManagerJackett:
		return base + "/api/v2.0/indexers/all/results/torznab/api"
	default:
		return base + "/api"
	}
}

func effectiveManager(manager, base string) string {
	switch strings.ToLower(strings.TrimSpace(manager)) {
	case ManagerProwlarr:
		return ManagerProwlarr
	case ManagerJackett:
		return ManagerJackett
	case ManagerMirCrew:
		// MirCrew is a direct Torznab service: the endpoint is <base>/api,
		// exactly like any other direct indexer.
		return ManagerMirCrew
	case ManagerTorznab:
		return ManagerTorznab
	}
	lower := strings.ToLower(strings.TrimSpace(base))
	switch {
	case strings.Contains(lower, "prowlarr"), strings.Contains(lower, ":9696"):
		return ManagerProwlarr
	case strings.Contains(lower, "jackett"), strings.Contains(lower, ":9117"):
		return ManagerJackett
	}
	return ManagerTorznab
}

// Fetch runs one search and returns the results.
func (c *Client) Fetch(ctx context.Context, query string, limit int) ([]Item, error) {
	if c.endpoint == "" {
		return nil, errors.New("empty indexer URL")
	}
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	if c.isProwlarrJSON() {
		return c.fetchProwlarr(ctx, query, limit)
	}
	return c.fetchTorznab(ctx, c.endpoint, query, limit)
}

func (c *Client) fetchTorznab(ctx context.Context, endpoint, query string, limit int) ([]Item, error) {
	values := url.Values{}
	values.Set("t", "search")
	values.Set("q", query)
	values.Set("extended", "1")
	values.Set("limit", strconv.Itoa(limit))
	if len(c.cfg.Categories) > 0 {
		values.Set("cat", joinInts(c.cfg.Categories))
	}
	if season, episode, ok := seasonEpisode(query); ok {
		values.Set("season", strconv.Itoa(season))
		values.Set("ep", strconv.Itoa(episode))
	}
	if c.cfg.APIKey != "" {
		values.Set("apikey", c.cfg.APIKey)
	}
	body, err := getBody(ctx, c.http, withQuery(endpoint, values), c.options())
	if err != nil {
		return nil, err
	}
	if torznabErr := TorznabError(body); torznabErr != "" {
		return nil, errors.New(torznabErr)
	}
	items, err := ParseRSS(body)
	if err != nil {
		if errors.Is(err, ErrHTMLPage) {
			return nil, fmt.Errorf("%s returned HTML instead of Torznab XML; check the endpoint URL, the API key and FlareSolverr", hostLabel(endpoint))
		}
		return nil, err
	}
	// Torznab results are torrents, but a link can still point to a details
	// page: only download-shaped URLs become a .torrent candidate.
	for i := range items {
		if items[i].TorrentURL == "" && LooksLikeTorrentURL(items[i].Link, "") {
			items[i].TorrentURL = items[i].Link
		}
	}
	return items, nil
}

// Test probes the endpoint and reports what it found. It uses t=caps, falling
// back to a one-item search for direct indexers that do not implement caps.
func (c *Client) Test(ctx context.Context) (string, error) {
	if c.endpoint == "" {
		return "", errors.New("empty indexer URL")
	}
	if c.isProwlarrJSON() {
		return c.testProwlarr(ctx)
	}
	values := url.Values{}
	values.Set("t", "caps")
	if c.cfg.APIKey != "" {
		values.Set("apikey", c.cfg.APIKey)
	}
	body, status, err := getBodyStatus(ctx, c.http, withQuery(c.endpoint, values), c.options())
	if err == nil && status >= 200 && status < 300 {
		if torznabErr := TorznabError(body); torznabErr != "" {
			return "", errors.New(torznabErr)
		}
		var caps struct {
			Server struct {
				Title   string `xml:"title,attr"`
				Version string `xml:"version,attr"`
			} `xml:"server"`
		}
		if xml.Unmarshal(body, &caps) == nil && strings.TrimSpace(caps.Server.Title) != "" {
			return c.serviceStatus(ctx, strings.TrimSpace("Torznab: "+caps.Server.Title+" "+caps.Server.Version))
		}
		return c.serviceStatus(ctx, "Torznab endpoint reachable")
	}
	if err != nil {
		return "", err
	}
	items, searchErr := c.fetchTorznab(ctx, c.endpoint, "", 1)
	if searchErr != nil {
		return "", fmt.Errorf("caps returned HTTP %d; search failed: %w", status, searchErr)
	}
	return c.serviceStatus(ctx, fmt.Sprintf("Torznab reachable, %d result(s) in a test search", len(items)))
}

// serviceStatus enriches a successful connectivity message with the health
// document some direct indexer services expose next to /api. MirCrew's
// mircrew-indexer is the reference: it keeps answering Torznab requests from a
// local index but reports a broken forum login on /status.
func (c *Client) serviceStatus(ctx context.Context, message string) (string, error) {
	parsed, err := url.Parse(c.endpoint)
	if err != nil || !strings.HasSuffix(parsed.Path, "/api") {
		return message, nil
	}
	statusURL := *parsed
	statusURL.Path = strings.TrimSuffix(parsed.Path, "/api") + "/status"
	statusURL.RawQuery = ""
	values := url.Values{}
	if c.cfg.APIKey != "" {
		values.Set("apikey", c.cfg.APIKey)
	}
	body, status, err := getBodyStatus(ctx, c.http, withQuery(statusURL.String(), values), c.options())
	if err != nil || status != http.StatusOK {
		return message, nil
	}
	var doc struct {
		OK         *bool  `json:"ok"`
		LoggedIn   *bool  `json:"logged_in"`
		LoginError string `json:"login_error"`
		LastError  string `json:"last_error"`
		LastRun    string `json:"last_run"`
	}
	if json.Unmarshal(body, &doc) != nil {
		return message, nil
	}
	if doc.OK != nil && !*doc.OK {
		reason := firstNonEmpty(doc.LastError, doc.LoginError, "unknown error")
		return "", fmt.Errorf("indexer service reports a problem: %s", reason)
	}
	if doc.LoginError != "" {
		return "", fmt.Errorf("indexer service forum login failed: %s", doc.LoginError)
	}
	if doc.LoggedIn == nil {
		return message, nil
	}
	if *doc.LoggedIn {
		return message + " · forum login ok", nil
	}
	if doc.LastRun != "" {
		return "", fmt.Errorf("indexer service forum login lost (last successful pass %s)", doc.LastRun)
	}
	return message + " · forum login in progress", nil
}

// TorznabError extracts the <error> element Jackett and Prowlarr use to explain
// a failed request.
func TorznabError(body []byte) string {
	decoder := xml.NewDecoder(strings.NewReader(string(body)))
	for {
		token, err := decoder.Token()
		if err != nil {
			return ""
		}
		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local != "error" {
			continue
		}
		var code, description string
		for _, attr := range start.Attr {
			switch strings.ToLower(attr.Name.Local) {
			case "code":
				code = attr.Value
			case "description":
				description = attr.Value
			}
		}
		description = strings.TrimSpace(description)
		if description == "" {
			continue
		}
		if code != "" {
			return redactSecrets("Torznab " + code + ": " + description)
		}
		return redactSecrets("Torznab: " + description)
	}
}

var seasonEpisodePattern = regexp.MustCompile(`(?i)s(\d{1,2})[ ._-]?e(\d{1,3})`)

func seasonEpisode(query string) (int, int, bool) {
	match := seasonEpisodePattern.FindStringSubmatch(query)
	if match == nil {
		return 0, 0, false
	}
	season, errSeason := strconv.Atoi(match[1])
	episode, errEpisode := strconv.Atoi(match[2])
	if errSeason != nil || errEpisode != nil || season <= 0 || episode <= 0 {
		return 0, 0, false
	}
	return season, episode, true
}

func joinInts(values []int) string {
	parts := make([]string, 0, len(values))
	for _, value := range values {
		if value > 0 {
			parts = append(parts, strconv.Itoa(value))
		}
	}
	return strings.Join(parts, ",")
}

func isHTTPURL(raw string) bool {
	lower := strings.ToLower(strings.TrimSpace(raw))
	return strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://")
}
