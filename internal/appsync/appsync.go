// Package appsync synchronizes indexarr indexers with the applications that
// consume Torznab/Newznab: Sonarr, Radarr, Lidarr, Readarr, Whisparr, Mylar3
// and LazyLibrarian.
//
// The Servarr family (Sonarr/Radarr/Lidarr/Readarr/Whisparr) is implemented
// against its v3 HTTP API, with a v1 compatibility fallback. Mylar3 and
// LazyLibrarian currently expose only a connectivity test; their Torznab
// settings API has not been verified, so indexer management returns
// ErrUnsupported instead of guessing endpoints.
package appsync

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// DefaultTimeout is used when Config.Timeout is zero or negative.
const DefaultTimeout = 30 * time.Second

// Kind identifies a supported application.
type Kind string

const (
	KindSonarr        Kind = "sonarr"
	KindRadarr        Kind = "radarr"
	KindLidarr        Kind = "lidarr"
	KindReadarr       Kind = "readarr"
	KindWhisparr      Kind = "whisparr"
	KindMylar3        Kind = "mylar3"
	KindLazyLibrarian Kind = "lazylibrarian"
)

// Valid reports whether k is one of the supported applications.
func (k Kind) Valid() bool {
	switch k {
	case KindSonarr, KindRadarr, KindLidarr, KindReadarr, KindWhisparr,
		KindMylar3, KindLazyLibrarian:
		return true
	default:
		return false
	}
}

// UsesServarrAPI reports whether k is driven through the Servarr v3 API.
func (k Kind) UsesServarrAPI() bool {
	switch k {
	case KindSonarr, KindRadarr, KindLidarr, KindReadarr, KindWhisparr:
		return true
	default:
		return false
	}
}

func (k Kind) displayName() string {
	switch k {
	case KindSonarr:
		return "Sonarr"
	case KindRadarr:
		return "Radarr"
	case KindLidarr:
		return "Lidarr"
	case KindReadarr:
		return "Readarr"
	case KindWhisparr:
		return "Whisparr"
	case KindMylar3:
		return "Mylar3"
	case KindLazyLibrarian:
		return "LazyLibrarian"
	default:
		return string(k)
	}
}

// Config describes one application connection. APIKey is handled like a
// password: it is never included in returned error messages.
type Config struct {
	Kind       Kind
	Name       string
	URL        string
	APIKey     string
	SyncLevel  string // disabled|add-remove|full
	Timeout    time.Duration
	HTTPClient *http.Client // optional, for tests
}

// IndexerSpec is the desired state of an indexer inside an application. The
// API key is the key indexarr generated for its own Torznab endpoint, not the
// application's API key.
type IndexerSpec struct {
	Name                    string
	Implementation          string // "Torznab" or "Newznab"
	ConfigContract          string // "TorznabSettings" or "NewznabSettings"
	BaseURL                 string // indexarr Torznab endpoint for this indexer
	APIKey                  string
	Categories              []int
	Priority                int
	Tags                    []int
	EnableRSS               bool
	EnableAutomaticSearch   bool
	EnableInteractiveSearch bool
	Protocol                string // torrent|usenet
	// Seed criteria copied to the *arr Torznab indexer. Zero keeps the app
	// default; the fields are always sent for torrent indexers so an explicit
	// zero clears a previous value.
	MinSeeders                int
	SeedRatio                 float64
	SeedTimeMinutes           int
	SeasonPackSeedTimeMinutes int
	AdditionalFields          map[string]any
}

// RemoteIndexer is an indexer as reported by an application.
type RemoteIndexer struct {
	ID             int
	Name           string
	Implementation string
	ConfigContract string
	Priority       int
	Tags           []int
	Fields         map[string]any // raw fields by name
	RawHash        string         // stable fingerprint of the settings that matter
}

// Client is the typed application-sync surface. Both Servarr and the
// simple adapters implement it.
type Client interface {
	Test(ctx context.Context) (string, error)
	ListIndexers(ctx context.Context) ([]RemoteIndexer, error)
	AddIndexer(ctx context.Context, spec IndexerSpec) (RemoteIndexer, error)
	UpdateIndexer(ctx context.Context, remoteID int, spec IndexerSpec) (RemoteIndexer, error)
	DeleteIndexer(ctx context.Context, remoteID int) error
}

// TagResolver is an optional capability of applications that store indexer
// tags as first-class objects (the Servarr family). ResolveTags returns one
// application tag ID per label, creating the missing tags. A client without
// tag support simply omits the method, and indexarr then sends no tags.
type TagResolver interface {
	ResolveTags(ctx context.Context, labels []string) ([]int, error)
}

// CategoriesForKind keeps only the standard Torznab categories that an
// application consumes: Sonarr searches TV, Radarr movies, Lidarr audio,
// Readarr books and Whisparr adult content. Sending the full category list of
// a site to every app (as the first implementation did) makes an *arr query
// categories it can never use. Unknown kinds keep the list unchanged.
func CategoriesForKind(kind Kind, in []int) []int {
	low, high := 0, 0
	switch kind {
	case KindSonarr:
		low, high = 5000, 5999
	case KindRadarr:
		low, high = 2000, 2999
	case KindLidarr:
		low, high = 3000, 3999
	case KindWhisparr:
		low, high = 6000, 6999
	case KindReadarr:
		low, high = 7000, 7999
	default:
		return sortedInts(in)
	}
	out := make([]int, 0, len(in))
	for _, category := range sortedInts(in) {
		if category >= low && category <= high {
			out = append(out, category)
		}
	}
	return out
}

// ErrUnsupported is returned by adapters that cannot manage indexers yet.
// Callers should use errors.Is.
var ErrUnsupported = errors.New("appsync: operation unsupported")

// New validates cfg and dispatches to the adapter for cfg.Kind.
func New(cfg Config) (Client, error) {
	cfg.Kind = Kind(strings.ToLower(strings.TrimSpace(string(cfg.Kind))))
	if !cfg.Kind.Valid() {
		return nil, fmt.Errorf("appsync: unknown application kind %q", cfg.Kind)
	}

	switch level := strings.ToLower(strings.TrimSpace(cfg.SyncLevel)); level {
	case "":
		cfg.SyncLevel = "disabled"
	case "disabled", "add-remove", "full":
		cfg.SyncLevel = level
	default:
		return nil, fmt.Errorf("appsync: invalid sync level %q", cfg.SyncLevel)
	}

	base, err := normalizeBaseURL(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("appsync: %s: %w", cfg.Kind, err)
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}
	cfg.HTTPClient = newHTTPClient(cfg.HTTPClient, cfg.Timeout)

	if cfg.Kind.UsesServarrAPI() {
		return newServarr(cfg, base), nil
	}
	return newSimple(cfg, base), nil
}

// normalizeBaseURL validates an application root URL and removes a trailing
// slash. Query strings and fragments are not meaningful for an app root and
// are dropped.
func normalizeBaseURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("URL is empty")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("URL must use http or https, got %q", parsed.Scheme)
	}
	if parsed.Host == "" {
		return "", errors.New("URL has no host")
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return strings.TrimRight(parsed.String(), "/"), nil
}

// fingerprint is the canonical representation used by SpecHash. Only fields
// indexarr manages are included: schema defaults and any application-side
// fields are deliberately excluded so a fresh install does not look
// perpetually different. AdditionalFields is a forward-compatibility seam and
// is not fingerprinted.
type fingerprint struct {
	Name                    string  `json:"name"`
	Implementation          string  `json:"implementation"`
	ConfigContract          string  `json:"configContract"`
	BaseURL                 string  `json:"baseUrl"`
	APIKey                  string  `json:"apiKey"`
	Categories              []int   `json:"categories"`
	Priority                int     `json:"priority"`
	Tags                    []int   `json:"tags"`
	EnableRSS               bool    `json:"enableRss"`
	EnableAutomaticSearch   bool    `json:"enableAutomaticSearch"`
	EnableInteractiveSearch bool    `json:"enableInteractiveSearch"`
	Protocol                string  `json:"protocol"`
	MinSeeders              int     `json:"minimumSeeders"`
	SeedRatio               float64 `json:"seedRatio"`
	SeedTimeMinutes         int     `json:"seedTime"`
	SeasonPackSeedTimeMin   int     `json:"seasonPackSeedTime"`
}

// SpecHash returns a stable sha256 fingerprint of the parts of spec that
// indexarr manages. Map ordering, category order and tag order do not affect
// the result.
func SpecHash(spec IndexerSpec) string {
	impl, contract, protocol := normalizeSpecIdentity(spec)
	canonical := fingerprint{
		Name:                    spec.Name,
		Implementation:          impl,
		ConfigContract:          contract,
		BaseURL:                 normalizeURLString(spec.BaseURL),
		APIKey:                  spec.APIKey,
		Categories:              sortedInts(spec.Categories),
		Priority:                normalizePriority(spec.Priority),
		Tags:                    sortedInts(spec.Tags),
		EnableRSS:               spec.EnableRSS,
		EnableAutomaticSearch:   spec.EnableAutomaticSearch,
		EnableInteractiveSearch: spec.EnableInteractiveSearch,
		Protocol:                protocol,
		MinSeeders:              spec.MinSeeders,
		SeedRatio:               spec.SeedRatio,
		SeedTimeMinutes:         spec.SeedTimeMinutes,
		SeasonPackSeedTimeMin:   spec.SeasonPackSeedTimeMinutes,
	}
	encoded, err := json.Marshal(canonical)
	if err != nil {
		// All fields are JSON-safe; this branch only exists so SpecHash never
		// panics on a future field type.
		return ""
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

// normalizeImplementation canonicalizes the two known implementations and
// derives the missing half of the implementation/configContract pair.
func normalizeImplementation(implementation, configContract string) (string, string) {
	implementation = strings.TrimSpace(implementation)
	configContract = strings.TrimSpace(configContract)

	switch {
	case strings.EqualFold(implementation, "torznab"):
		implementation = "Torznab"
	case strings.EqualFold(implementation, "newznab"):
		implementation = "Newznab"
	}

	if implementation == "" {
		switch {
		case strings.EqualFold(configContract, "TorznabSettings"):
			implementation = "Torznab"
		case strings.EqualFold(configContract, "NewznabSettings"):
			implementation = "Newznab"
		}
	}

	switch {
	case strings.EqualFold(configContract, "torznabsettings"):
		configContract = "TorznabSettings"
	case strings.EqualFold(configContract, "newznabsettings"):
		configContract = "NewznabSettings"
	}

	if configContract == "" {
		switch implementation {
		case "Torznab":
			configContract = "TorznabSettings"
		case "Newznab":
			configContract = "NewznabSettings"
		}
	}
	return implementation, configContract
}

// normalizeSpecIdentity derives the canonical implementation, config contract
// and protocol for a desired indexer. A completely empty identity defaults to
// Torznab, matching the common torrent case.
func normalizeSpecIdentity(spec IndexerSpec) (implementation, configContract, protocol string) {
	implementation, configContract = normalizeImplementation(spec.Implementation, spec.ConfigContract)
	protocol = strings.ToLower(strings.TrimSpace(spec.Protocol))

	if implementation == "" {
		switch protocol {
		case "usenet":
			implementation = "Newznab"
		default:
			implementation = "Torznab"
		}
	}
	if configContract == "" {
		switch implementation {
		case "Newznab":
			configContract = "NewznabSettings"
		default:
			configContract = "TorznabSettings"
		}
	}
	if protocol != "torrent" && protocol != "usenet" {
		protocol = "usenet"
		if strings.EqualFold(configContract, "TorznabSettings") ||
			strings.EqualFold(implementation, "Torznab") {
			protocol = "torrent"
		}
	}
	return implementation, configContract, protocol
}

// normalizeURLString is the URL form used by the fingerprint and by payloads.
func normalizeURLString(raw string) string {
	return strings.TrimRight(strings.TrimSpace(raw), "/")
}

func sortedInts(in []int) []int {
	out := append([]int(nil), in...)
	sort.Ints(out)
	return out
}

// normalizePriority maps the zero value to Servarr's default. IndexerSpec is
// often built from a store row or a UI form where priority is optional, and
// Servarr validates priorities in the 1-50 range; sending 0 would be rejected.
func normalizePriority(priority int) int {
	if priority <= 0 {
		return 25
	}
	return priority
}
