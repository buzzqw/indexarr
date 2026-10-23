package family

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/buzzqw/indexarr/internal/engine"
)

// Xbtit/BtiTracker is another widely forked PHP tracker family. Stock Xbtit
// routes everything through index.php (page=login, page=torrents,
// page=torrent-details, page=download), while BtiTracker-derived forks keep
// takelogin.php and download.php. Both login paths and both download link
// shapes are supported out of the box.
//
// The selectors below are synthetic defaults ("bozza") for the stock
// sortabletable layout; forks are expected to override XbtitProfile.

const (
	xbtitBreakerFallback = "xbtit"
	xbtitDefaultDownload = "index.php?page=download&id=%s"
)

// XbtitProfile overrides the site-specific parts of the connector. Selectors
// accept a comma-separated CSS list and may use :nth-child column fallbacks.
type XbtitProfile struct {
	LoginPath        string
	SearchPath       string
	RowsSelector     string
	TitleSelector    string
	DetailsSelector  string
	DownloadSelector string
	SizeSelector     string
	SeedersSelector  string
	LeechersSelector string
	DateSelector     string
	CategorySelector string
	PageParam        string
}

// DefaultXbtitProfile returns the stock Xbtit layout. PageParam is "page" for
// the profile contract; NewXbtit switches to "pages" when SearchPath already
// binds page to the "torrents" route, which is how Xbtit actually paginates.
func DefaultXbtitProfile() XbtitProfile {
	return XbtitProfile{
		LoginPath:        "index.php?page=login",
		SearchPath:       "index.php?page=torrents&search=...&category=...&pages=...",
		RowsSelector:     "table#sortabletable tbody tr",
		TitleSelector:    `a[href*="torrent-details"]`,
		DetailsSelector:  `a[href*="torrent-details"]`,
		DownloadSelector: `a[href*="page=download"], a[href*="download.php"]`,
		SizeSelector:     "td.size, td:nth-child(3)",
		SeedersSelector:  "td.seeders, td:nth-child(4)",
		LeechersSelector: "td.leechers, td:nth-child(5)",
		DateSelector:     "td.date, td.added, td:nth-child(6)",
		CategorySelector: `a[href*="category="], td:nth-child(2)`,
		PageParam:        "page",
	}
}

func (p XbtitProfile) withDefaults() XbtitProfile {
	defaults := DefaultXbtitProfile()
	if strings.TrimSpace(p.LoginPath) == "" {
		p.LoginPath = defaults.LoginPath
	}
	if strings.TrimSpace(p.SearchPath) == "" {
		p.SearchPath = defaults.SearchPath
	}
	if strings.TrimSpace(p.RowsSelector) == "" {
		p.RowsSelector = defaults.RowsSelector
	}
	if strings.TrimSpace(p.TitleSelector) == "" {
		p.TitleSelector = defaults.TitleSelector
	}
	if strings.TrimSpace(p.DetailsSelector) == "" {
		p.DetailsSelector = defaults.DetailsSelector
	}
	if strings.TrimSpace(p.DownloadSelector) == "" {
		p.DownloadSelector = defaults.DownloadSelector
	}
	if strings.TrimSpace(p.SizeSelector) == "" {
		p.SizeSelector = defaults.SizeSelector
	}
	if strings.TrimSpace(p.SeedersSelector) == "" {
		p.SeedersSelector = defaults.SeedersSelector
	}
	if strings.TrimSpace(p.LeechersSelector) == "" {
		p.LeechersSelector = defaults.LeechersSelector
	}
	if strings.TrimSpace(p.DateSelector) == "" {
		p.DateSelector = defaults.DateSelector
	}
	if strings.TrimSpace(p.CategorySelector) == "" {
		p.CategorySelector = defaults.CategorySelector
	}
	if strings.TrimSpace(p.PageParam) == "" {
		p.PageParam = defaults.PageParam
	}
	return p
}

// XbtitConfig configures one Xbtit/BtiTracker tracker.
type XbtitConfig struct {
	Indexer    engine.IndexerRef
	BaseURL    string
	Username   string
	Password   string
	Passkey    string
	Transport  *engine.Transport
	Categories []int // site category ids used when a search names none
	UserAgent  string
	Profile    XbtitProfile
}

// Xbtit is the native connector shared by Xbtit and BtiTracker trackers.
type Xbtit struct {
	core *htmlTracker
}

var _ engine.Connector = (*Xbtit)(nil)

// NewXbtit validates cfg and builds a connector.
func NewXbtit(cfg XbtitConfig) (*Xbtit, error) {
	base, err := parseBaseURL(cfg.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("xbtit: %w", err)
	}
	if err := validateCredentialPair(cfg.Username, cfg.Password); err != nil {
		return nil, fmt.Errorf("xbtit: %w", err)
	}
	profile := cfg.Profile.withDefaults()
	if err := validateProfileSelectors(profile.RowsSelector, profile.TitleSelector,
		profile.DetailsSelector, profile.DownloadSelector, profile.SizeSelector,
		profile.SeedersSelector, profile.LeechersSelector, profile.DateSelector,
		profile.CategorySelector); err != nil {
		return nil, fmt.Errorf("xbtit: %w", err)
	}

	pageParam := profile.PageParam
	if strings.EqualFold(pageParam, "page") && routeBindsPage(profile.SearchPath) {
		// index.php?page=torrents is the route; Xbtit paginates with pages=.
		pageParam = "pages"
	}

	loginPaths := []string{profile.LoginPath}
	if !strings.EqualFold(profile.LoginPath, "takelogin.php") {
		loginPaths = append(loginPaths, "takelogin.php")
	}

	cfg.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	cfg.Username = strings.TrimSpace(cfg.Username)
	cfg.Password = strings.TrimSpace(cfg.Password)
	cfg.Passkey = strings.TrimSpace(cfg.Passkey)
	cfg.Indexer.Protocol = engine.ProtocolTorrent
	cfg.Categories = append([]int(nil), cfg.Categories...)
	if cfg.Transport == nil {
		cfg.Transport = engine.NewTransport(engine.TransportConfig{UserAgent: cfg.UserAgent})
	}

	breaker := strings.TrimSpace(cfg.Indexer.Slug)
	if breaker == "" {
		breaker = xbtitBreakerFallback
	}

	return &Xbtit{core: &htmlTracker{
		label:         "xbtit",
		base:          base,
		transport:     cfg.Transport,
		breaker:       breaker,
		userAgent:     cfg.UserAgent,
		username:      cfg.Username,
		password:      cfg.Password,
		passkey:       cfg.Passkey,
		categories:    cfg.Categories,
		loginPaths:    loginPaths,
		searchPath:    profile.SearchPath,
		pageParam:     pageParam,
		categoryParam: "category",
		rows:          profile.RowsSelector,
		rowFallbacks:  []string{"table.lista tr", "table#sortabletable tr"},
		title:         profile.TitleSelector,
		details:       profile.DetailsSelector,
		downloadLink:  profile.DownloadSelector,
		size:          profile.SizeSelector,
		seeders:       profile.SeedersSelector,
		leechers:      profile.LeechersSelector,
		date:          profile.DateSelector,
		category:      profile.CategorySelector,
		downloadByID:  xbtitDefaultDownload,
		session:       newTrackerSession(),
	}}, nil
}

// Capabilities implements engine.Connector.
func (x *Xbtit) Capabilities() engine.Capabilities {
	return x.core.capabilities()
}

// Search implements engine.Connector.
func (x *Xbtit) Search(ctx context.Context, req engine.SearchRequest) ([]engine.Item, error) {
	return x.core.search(ctx, req)
}

// Latest implements engine.Connector.
func (x *Xbtit) Latest(ctx context.Context, req engine.LatestRequest) ([]engine.Item, error) {
	return x.core.latest(ctx, req)
}

// Download implements engine.Connector.
func (x *Xbtit) Download(ctx context.Context, item engine.Item) (engine.Payload, error) {
	return x.core.download(ctx, item)
}

// Test implements engine.Connector.
func (x *Xbtit) Test(ctx context.Context) (engine.TestResult, error) {
	return x.core.test(ctx)
}

// XbtitFactory builds an Xbtit connector from generic settings. It reads
// base_url (or url), username, password, passkey, api_key (passkey fallback),
// categories and user_agent.
func XbtitFactory(cfg engine.Config) (engine.Connector, error) {
	settings := cfg.Settings
	categories, err := parseCategoryList(settings["categories"])
	if err != nil {
		return nil, fmt.Errorf("xbtit: categories: %w", err)
	}
	return NewXbtit(XbtitConfig{
		Indexer:    cfg.Indexer,
		BaseURL:    firstNonEmpty(settings["base_url"], settings["url"]),
		Username:   settings["username"],
		Password:   settings["password"],
		Passkey:    firstNonEmpty(settings["passkey"], settings["api_key"]),
		Transport:  transportFromSettings(cfg.Transport, settings),
		Categories: categories,
		UserAgent:  settings["user_agent"],
	})
}

// routeBindsPage reports whether SearchPath uses page as a non-numeric route
// parameter, in which case the connector must paginate with pages.
func routeBindsPage(searchPath string) bool {
	parsed, err := url.Parse(strings.TrimSpace(searchPath))
	if err != nil {
		return false
	}
	values := parsed.Query()["page"]
	if len(values) == 0 {
		return false
	}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || value == "..." {
			return false
		}
		for _, r := range value {
			if r < '0' || r > '9' {
				return true
			}
		}
	}
	return false
}
