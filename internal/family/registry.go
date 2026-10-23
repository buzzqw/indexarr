package family

import (
	"fmt"
	"strconv"

	"github.com/buzzqw/indexarr/internal/engine"
)

// Connector names registered by Register.
const (
	ConnectorUnit3D  = "unit3d"
	ConnectorNewznab = "newznab"
)

// Register adds the UNIT3D and generic Newznab connector factories to r.
// Settings understood by both factories are base_url (or url), api_key (or
// api_token), categories, user_agent and allow_private.
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
	return nil
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
