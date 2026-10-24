package family

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/buzzqw/indexarr/internal/engine"
)

// Connector names registered by Register.
const (
	ConnectorUnit3D        = "unit3d"
	ConnectorNewznab       = "newznab"
	ConnectorTorznab       = "torznab"
	ConnectorGazelle       = "gazelle"
	ConnectorNexusPHP      = "nexusphp"
	ConnectorTorrentTrader = "torrenttrader"
	ConnectorXbtit         = "xbtit"
)

// Register adds the native family connector factories to r. Settings
// understood by all factories are base_url (or url), categories, user_agent
// and allow_private. Gazelle additionally reads api_key (or apikey); NexusPHP
// reads username, password and passkey. RSS reads url (or base_url),
// default_category, query and the smartfeed flag set by the MirCrew add flow.
func Register(r *engine.Registry) error {
	if r == nil {
		return fmt.Errorf("family: nil registry")
	}
	if err := r.RegisterConnector(ConnectorUnit3D, unit3dFactory); err != nil {
		return fmt.Errorf("family: register %q: %w", ConnectorUnit3D, err)
	}
	if err := r.RegisterConnector(ConnectorNewznab, newznabFactory); err != nil {
		return fmt.Errorf("family: register %q: %w", ConnectorNewznab, err)
	}
	if err := r.RegisterConnector(ConnectorTorznab, torznabFactory); err != nil {
		return fmt.Errorf("family: register %q: %w", ConnectorTorznab, err)
	}
	if err := r.RegisterConnector(ConnectorGazelle, gazelleFactory); err != nil {
		return fmt.Errorf("family: register %q: %w", ConnectorGazelle, err)
	}
	if err := r.RegisterConnector(ConnectorNexusPHP, nexusphpFactory); err != nil {
		return fmt.Errorf("family: register %q: %w", ConnectorNexusPHP, err)
	}
	if err := r.RegisterConnector(ConnectorTorrentTrader, TorrentTraderFactory); err != nil {
		return fmt.Errorf("family: register %q: %w", ConnectorTorrentTrader, err)
	}
	if err := r.RegisterConnector(ConnectorXbtit, XbtitFactory); err != nil {
		return fmt.Errorf("family: register %q: %w", ConnectorXbtit, err)
	}
	if err := r.RegisterConnector(ConnectorRSS, rssFactory); err != nil {
		return fmt.Errorf("family: register %q: %w", ConnectorRSS, err)
	}
	return nil
}

func rssFactory(cfg engine.Config) (engine.Connector, error) {
	settings := cfg.Settings
	categories, err := parseCategoryList(settings["categories"])
	if err != nil {
		return nil, fmt.Errorf("rss: categories: %w", err)
	}
	return NewRSS(RSSConfig{
		Indexer:         cfg.Indexer,
		URL:             firstNonEmpty(settings["url"], settings["base_url"]),
		DefaultCategory: firstNonEmpty(settings["default_category"], settings["default-category"]),
		Query:           settings["query"],
		Transport:       transportFromSettings(cfg.Transport, settings),
		UserAgent:       firstNonEmpty(settings["user_agent"], settings["user-agent"]),
		Categories:      categories,
		Smartfeed:       boolSetting(settings["smartfeed"]),
	})
}

func gazelleFactory(cfg engine.Config) (engine.Connector, error) {
	settings := cfg.Settings
	categories, err := parseCategoryList(settings["categories"])
	if err != nil {
		return nil, fmt.Errorf("gazelle: categories: %w", err)
	}
	return NewGazelle(GazelleConfig{
		Indexer:      cfg.Indexer,
		BaseURL:      firstNonEmpty(settings["base_url"], settings["url"]),
		APIKey:       firstNonEmpty(settings["api_key"], settings["apikey"]),
		Transport:    transportFromSettings(cfg.Transport, settings),
		Categories:   categories,
		UserAgent:    settings["user_agent"],
		BrowsePath:   settings["browse_path"],
		DownloadPath: settings["download_path"],
	})
}

func nexusphpFactory(cfg engine.Config) (engine.Connector, error) {
	settings := cfg.Settings
	categories, err := parseCategoryList(settings["categories"])
	if err != nil {
		return nil, fmt.Errorf("nexusphp: categories: %w", err)
	}
	profile := DefaultNexusPHPProfile()
	setting := func(target *string, key string) {
		if value := strings.TrimSpace(settings[key]); value != "" {
			*target = value
		}
	}
	setting(&profile.LoginPath, "login_path")
	setting(&profile.LoginUserField, "login_user_field")
	setting(&profile.LoginPassField, "login_pass_field")
	setting(&profile.LoginSuccessSelector, "login_success_selector")
	setting(&profile.LoginErrorSelector, "login_error_selector")
	setting(&profile.SearchPath, "search_path")
	setting(&profile.RowsSelector, "rows_selector")
	setting(&profile.TitleSelector, "title_selector")
	setting(&profile.DetailsSelector, "details_selector")
	setting(&profile.DownloadSelector, "download_selector")
	setting(&profile.SizeSelector, "size_selector")
	setting(&profile.SeedersSelector, "seeders_selector")
	setting(&profile.LeechersSelector, "leechers_selector")
	setting(&profile.DateSelector, "date_selector")
	setting(&profile.CategorySelector, "category_selector")
	setting(&profile.PageParam, "page_param")

	return NewNexusPHP(NexusPHPConfig{
		Indexer:    cfg.Indexer,
		BaseURL:    firstNonEmpty(settings["base_url"], settings["url"]),
		Username:   settings["username"],
		Password:   settings["password"],
		Passkey:    settings["passkey"],
		Transport:  transportFromSettings(cfg.Transport, settings),
		Categories: categories,
		UserAgent:  settings["user_agent"],
		Profile:    profile,
	})
}

func unit3dFactory(cfg engine.Config) (engine.Connector, error) {
	settings := cfg.Settings
	categories, err := parseCategoryList(settings["categories"])
	if err != nil {
		return nil, fmt.Errorf("unit3d: categories: %w", err)
	}
	userAgent := settings["user_agent"]
	return NewUnit3D(Unit3DConfig{
		Indexer:    cfg.Indexer,
		BaseURL:    firstNonEmpty(settings["base_url"], settings["url"]),
		APIKey:     firstNonEmpty(settings["api_key"], settings["api_token"]),
		Transport:  transportFromSettings(cfg.Transport, settings),
		Categories: categories,
		UserAgent:  userAgent,
	})
}

func newznabFactory(cfg engine.Config) (engine.Connector, error) {
	settings := cfg.Settings
	categories, err := parseCategoryList(settings["categories"])
	if err != nil {
		return nil, fmt.Errorf("newznab: categories: %w", err)
	}
	userAgent := settings["user_agent"]
	return NewNewznab(NewznabConfig{
		Indexer:    cfg.Indexer,
		BaseURL:    firstNonEmpty(settings["base_url"], settings["url"]),
		APIKey:     firstNonEmpty(settings["api_key"], settings["api_token"]),
		Transport:  transportFromSettings(cfg.Transport, settings),
		Categories: categories,
		UserAgent:  userAgent,
	})
}

func torznabFactory(cfg engine.Config) (engine.Connector, error) {
	settings := cfg.Settings
	categories, err := parseCategoryList(settings["categories"])
	if err != nil {
		return nil, fmt.Errorf("torznab: categories: %w", err)
	}
	userAgent := settings["user_agent"]
	return NewTorznab(TorznabConfig{
		Indexer:    cfg.Indexer,
		BaseURL:    firstNonEmpty(settings["base_url"], settings["url"]),
		APIKey:     firstNonEmpty(settings["api_key"], settings["api_token"]),
		Transport:  transportFromSettings(cfg.Transport, settings),
		Categories: categories,
		UserAgent:  userAgent,
	})
}

// transportFromSettings uses the shared transport when the caller supplied
// one, so rate limits and the circuit breaker are common to every indexer.
// Without it each connector receives its own guarded transport rather than a
// bare http.Client.
func transportFromSettings(shared *engine.Transport, settings map[string]string) *engine.Transport {
	if shared != nil {
		return shared
	}
	return engine.NewTransport(engine.TransportConfig{
		UserAgent:    settings["user_agent"],
		AllowPrivate: boolSetting(settings["allow_private"]),
	})
}

func boolSetting(value string) bool {
	parsed, err := strconv.ParseBool(value)
	if err == nil {
		return parsed
	}
	switch value {
	case "yes", "on", "1":
		return true
	default:
		return false
	}
}
