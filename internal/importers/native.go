package importers

import (
	"fmt"
	"net"
	"net/url"
	"sort"
	"strings"

	"github.com/buzzqw/indexarr/internal/catalog"
	"github.com/buzzqw/indexarr/internal/definition"
)

// nativeDefinition is the catalog id used for the Torznab bridge: an indexer
// whose site has no verified native definition points at the per-indexer
// Torznab endpoint of Jackett or Prowlarr instead.
const nativeDefinition = "torznab-generic"

// NativeTarget decides how an imported indexer is recreated: from a verified
// catalog definition, or as a torznab indexer pointing at the source manager.
// Secrets are carried in the target because the caller seals them in the
// vault; they must never be logged or included in errors.
type NativeTarget struct {
	Name         string
	Slug         string
	DefinitionID string // verified catalog id, or "torznab-generic"
	ViaManager   string // "jackett" | "prowlarr" | "" (direct)
	SiteKey      string // stable site identity used to drop duplicates
	// SiteGroup and Channel pair the search bridge with a native latest feed
	// of the same site. A site can therefore have two channels (search and
	// latest) without being treated as a duplicate.
	SiteGroup      string
	Channel        string            // "search", "latest" or empty
	BaseURL        string            // per-indexer Torznab endpoint when ViaManager != ""
	Config         map[string]string // non-secret settings for config_json
	Secrets        map[string]string // sealed by the caller, never logged
	Enabled        bool
	DisabledReason string // why an upstream indexer is imported disabled
	Priority       int
	Tags           []string
	Categories     []int
	Protocol       Protocol
	RemoteID       string
}

// Key returns the idempotency key of a target: definition, base URL and slug.
// A second import of the same site produces the same key.
func (t NativeTarget) Key() string {
	slug := strings.TrimSpace(t.Slug)
	if slug == "" {
		slug = slugify(t.Name)
	}
	key := strings.ToLower(strings.TrimSpace(t.DefinitionID)) + "|" +
		normalizeTargetURL(t.BaseURL) + "|" + slug
	if channel := strings.ToLower(strings.TrimSpace(t.Channel)); channel != "" {
		key += "|" + channel
	}
	return key
}

// Targets maps every indexer of the plan to a native target. The built-in
// catalog is loaded once; a catalog error is returned instead of falling back
// to bridges, because that would silently lose the verified definitions.
func Targets(plan Plan) ([]NativeTarget, error) {
	entries, err := catalog.Builtins()
	if err != nil {
		return nil, fmt.Errorf("importers: load catalog: %w", err)
	}
	return BuildTargets(plan, entries), nil
}

// BuildTargets maps a plan with an explicit catalog. It is the deterministic
// entry point used by tests and by callers that already loaded the catalog.
// Disabled upstream indexers stay disabled: the caller must import them.
func BuildTargets(plan Plan, entries []catalog.Entry) []NativeTarget {
	out := make([]NativeTarget, 0, len(plan.Indexers))
	for _, indexer := range plan.Indexers {
		out = append(out, Target(plan.Source, plan.ManagerURL, plan.ManagerAPIKey, indexer, entries))
	}
	return out
}

// Target maps one imported indexer to a native target.
//
// The verified catalog wins when the definition hint names it or when the
// tracker host matches one of its meta.urls. Everything else becomes a native
// torznab indexer pointed at the per-indexer endpoint of the manager:
//
//	Jackett    <manager>/api/v2.0/indexers/<id>/results/torznab/
//	Prowlarr   <manager>/<id>/api
//
// The API key stays out of the URL: it is returned in Secrets and sealed by
// the caller, and the connectors add it to every request.
func Target(source, managerURL, managerAPIKey string, indexer Indexer, entries []catalog.Entry) NativeTarget {
	if entry, ok := matchDefinition(indexer, entries); ok {
		return catalogTarget(entry, indexer)
	}
	return bridgeTarget(source, managerURL, managerAPIKey, indexer)
}

// managerIsPrivate reports whether the manager URL points at a loopback,
// private or unspecified address. Only those bridge indexers opt into the
// private-address policy; public managers stay behind the default SSRF guard.
func managerIsPrivate(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Hostname() == "" {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified()
}

// verifiedState reports whether a catalog entry may be used to create a
// native indexer. A bozza is a draft and falls back to the Torznab bridge.
func verifiedState(state string) bool {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "verificata", "stabile":
		return true
	default:
		return false
	}
}

func matchDefinition(indexer Indexer, entries []catalog.Entry) (catalog.Entry, bool) {
	hint := strings.ToLower(strings.TrimSpace(indexer.DefinitionHint))
	if hint != "" {
		for _, entry := range entries {
			if verifiedState(entry.State) && strings.EqualFold(entry.ID, hint) {
				return entry, true
			}
		}
	}

	hosts := indexerHosts(indexer)
	if len(hosts) == 0 {
		return catalog.Entry{}, false
	}
	for _, entry := range entries {
		if !verifiedState(entry.State) || entry.Definition == nil {
			continue
		}
		for _, raw := range entry.Definition.Meta.URLs {
			if host := urlHost(raw); host != "" && hosts[host] {
				return entry, true
			}
		}
	}
	return catalog.Entry{}, false
}

// indexerHosts collects the tracker host names the manager exposes. BaseURL is
// included because Prowlarr stores the site URL there; SiteURL and the
// site_link field cover Jackett, whose BaseURL is the local manager endpoint.
func indexerHosts(indexer Indexer) map[string]bool {
	hosts := make(map[string]bool, 3)
	for _, raw := range []string{indexer.SiteURL, indexer.BaseURL, indexer.Fields["site_link"]} {
		if host := urlHost(raw); host != "" {
			hosts[host] = true
		}
	}
	return hosts
}

// urlHost returns a comparison-ready host name: lower case, no port, no
// leading "www." and no trailing dot.
func urlHost(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" {
		return ""
	}
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	return strings.TrimPrefix(host, "www.")
}

// catalogTarget recreates the site from a verified definition. The definition's
// defaults fill the declared settings; credentials are moved to Secrets so the
// caller can seal them.
func catalogTarget(entry catalog.Entry, indexer Indexer) NativeTarget {
	config := make(map[string]string)
	secrets := make(map[string]string)
	def := entry.Definition

	if def != nil {
		for _, setting := range def.Settings {
			value := strings.TrimSpace(setting.Default)
			if isURLSetting(setting.Name) && value == "" {
				value = firstNonEmpty(indexer.SiteURL, firstMetaURL(def))
			}
			if isSecretSetting(setting.Name) {
				if secret, ok := credentialFor(setting.Name, indexer); ok && secret != "" {
					secrets[secretSettingKey(def, setting.Name)] = secret
				}
				continue
			}
			if isURLSetting(setting.Name) && value == "" {
				value = firstMetaURL(def)
			}
			if value != "" {
				config[setting.Name] = value
			}
		}
	}

	// Family connectors (rss, torznab, …) read the selected categories from a
	// setting; definition-native connectors map categories inside the YAML.
	if def != nil && strings.TrimSpace(def.Connector) != "" {
		if categories := joinInts(indexer.Categories); categories != "" {
			config["categories"] = categories
		}
	}

	protocol := indexer.Protocol
	if protocol == "" && def != nil {
		protocol = normalizeProtocol(def.Meta.Protocol, "")
	}

	return NativeTarget{
		Name:           indexer.Name,
		Slug:           slugify(indexer.Name),
		DefinitionID:   entry.ID,
		SiteKey:        siteKeyForIndexer(indexer, entry.ID),
		SiteGroup:      siteKeyForIndexer(indexer, entry.ID),
		ViaManager:     "",
		BaseURL:        firstNonEmpty(indexer.SiteURL, firstMetaURL(def)),
		Config:         config,
		Secrets:        secrets,
		Enabled:        indexer.Enabled,
		DisabledReason: targetDisabledReason(indexer, ""),
		Priority:       indexer.Priority,
		Tags:           append([]string(nil), indexer.Tags...),
		Categories:     append([]int(nil), indexer.Categories...),
		Protocol:       protocol,
		RemoteID:       indexer.RemoteID,
	}
}

// bridgeTarget points a native torznab indexer at the per-indexer endpoint of
// the manager. The API key is a secret, not part of the stored URL.
func bridgeTarget(source, managerURL, managerAPIKey string, indexer Indexer) NativeTarget {
	manager := strings.ToLower(strings.TrimSpace(source))
	endpoint := managerEndpoint(manager, managerURL, indexer)

	config := map[string]string{"url": endpoint}
	// Jackett and Prowlarr commonly run on the LAN. The SSRF guard blocks
	// private addresses by default, so a bridge to a local manager must opt in
	// explicitly. The setting is not a secret: it only widens the guard for
	// this one indexer.
	if managerIsPrivate(managerURL) {
		config["allow_private"] = "true"
	}
	if categories := joinInts(indexer.Categories); categories != "" {
		config["categories"] = categories
	}

	secrets := make(map[string]string)
	// The bridge authenticates to the manager, not to the tracker: the
	// manager's key is the credential of the per-indexer endpoint. Older
	// callers that do not carry it fall back to the indexer value (Jackett
	// stores the same global key in both places).
	key := firstNonEmpty(managerAPIKey, indexer.APIKey)
	if key != "" {
		secrets["api_key"] = key
	}
	if indexer.Username != "" {
		secrets["username"] = indexer.Username
	}
	if indexer.Password != "" {
		secrets["password"] = indexer.Password
	}

	return NativeTarget{
		Name:           indexer.Name,
		Slug:           slugify(indexer.Name),
		DefinitionID:   nativeDefinition,
		SiteKey:        siteKeyForIndexer(indexer, ""),
		SiteGroup:      siteKeyForIndexer(indexer, ""),
		ViaManager:     manager,
		BaseURL:        endpoint,
		Config:         config,
		Secrets:        secrets,
		Enabled:        indexer.Enabled,
		DisabledReason: targetDisabledReason(indexer, manager),
		Priority:       indexer.Priority,
		Tags:           append([]string(nil), indexer.Tags...),
		Categories:     append([]int(nil), indexer.Categories...),
		Protocol:       indexer.Protocol,
		RemoteID:       indexer.RemoteID,
	}
}

func disabledUpstreamReason(enabled bool, manager string) string {
	if enabled {
		return ""
	}
	if manager != "" {
		return "disattivato in " + manager
	}
	return "disattivato nella sorgente"
}

func targetDisabledReason(indexer Indexer, manager string) string {
	if reason := strings.TrimSpace(indexer.DisabledReason); reason != "" {
		return reason
	}
	return disabledUpstreamReason(indexer.Enabled, manager)
}

// managerEndpoint builds the documented per-indexer Torznab URL. When the plan
// does not carry the manager base URL (older callers), a Jackett endpoint
// already derived by the importer is kept as-is.
func managerEndpoint(manager, managerURL string, indexer Indexer) string {
	base := strings.TrimRight(strings.TrimSpace(managerURL), "/")
	id := strings.TrimSpace(indexer.RemoteID)
	switch manager {
	case "jackett":
		if base != "" && id != "" {
			return base + "/api/v2.0/indexers/" + url.PathEscape(id) + "/results/torznab/"
		}
		// Older callers may already carry the derived Jackett endpoint.
		return strings.TrimSpace(indexer.BaseURL)
	case "prowlarr":
		if base != "" && id != "" {
			return base + "/" + url.PathEscape(id) + "/api"
		}
		// indexer.BaseURL is the tracker site, not Prowlarr's endpoint: an
		// empty value makes the caller report the missing manager URL.
		return ""
	default:
		return strings.TrimSpace(indexer.BaseURL)
	}
}

// siteKeyForIndexer is the identity used to recognise the same site arriving
// from Jackett, Prowlarr and the native catalog. The tracker host is preferred
// because manager endpoints (127.0.0.1:9117, /14/api) are not site identities;
// the definition id and, as a last resort, the slug keep entries without a
// usable URL distinct.
func siteKeyForIndexer(indexer Indexer, definitionID string) string {
	for _, raw := range []string{indexer.SiteURL, indexer.Fields["site_link"]} {
		if host := urlHost(raw); host != "" {
			return host
		}
	}
	if definitionID != "" {
		return "definition:" + strings.ToLower(strings.TrimSpace(definitionID))
	}
	return "slug:" + slugify(indexer.Name)
}

// DroppedTarget records a duplicate that was not imported, with the target
// that covers it, so the summary can say "non importato: già coperto da …".
type DroppedTarget struct {
	Target    NativeTarget
	CoveredBy NativeTarget
}

// targetRank implements the coordinator's preference: a verified native
// definition first, then the Prowlarr bridge, then the Jackett bridge.
func targetRank(target NativeTarget) int {
	if strings.ToLower(strings.TrimSpace(target.DefinitionID)) != nativeDefinition {
		return 0
	}
	switch strings.ToLower(strings.TrimSpace(target.ViaManager)) {
	case "prowlarr":
		return 1
	case "jackett":
		return 2
	default:
		return 3
	}
}

// PairChannels implements the general rule for a site that has both a native
// latest feed and a search bridge: the feed handles latest, the bridge handles
// searches, and the two are one site with two channels rather than duplicates.
func PairChannels(targets []NativeTarget) []NativeTarget {
	groups := make(map[string][]int)
	for i := range targets {
		group := strings.TrimSpace(targets[i].SiteGroup)
		if group == "" {
			group = strings.TrimSpace(targets[i].SiteKey)
			targets[i].SiteGroup = group
		}
		if group != "" {
			groups[group] = append(groups[group], i)
		}
	}
	for group, indexes := range groups {
		hasFeed := false
		for _, i := range indexes {
			if strings.EqualFold(strings.TrimSpace(targets[i].Channel), "latest") {
				hasFeed = true
				break
			}
		}
		for _, i := range indexes {
			target := &targets[i]
			if target.Config == nil {
				target.Config = make(map[string]string)
			}
			target.Config["site"] = group
			if !hasFeed {
				continue
			}
			if strings.EqualFold(strings.TrimSpace(target.Channel), "latest") {
				target.Channel = "latest"
				target.Config["channel"] = "latest"
				target.Config["latest"] = "true"
				continue
			}
			target.Channel = "search"
			target.Config["channel"] = "search"
			target.Config["latest"] = "false"
		}
	}
	return targets
}

// Deduplicate keeps one target per site. The first target wins only on equal
// rank; otherwise the higher-preference source replaces it and the loser is
// returned in dropped. Ordering is stable, so an idempotent re-import sees the
// same list and updates the same rows.
func Deduplicate(targets []NativeTarget) ([]NativeTarget, []DroppedTarget) {
	kept := make([]NativeTarget, 0, len(targets))
	var dropped []DroppedTarget
	position := make(map[string]int, len(targets))
	for _, target := range targets {
		key := strings.TrimSpace(target.SiteKey)
		if channel := strings.ToLower(strings.TrimSpace(target.Channel)); channel != "" {
			key += "|" + channel
		}
		if key == "" {
			kept = append(kept, target)
			continue
		}
		index, seen := position[key]
		if !seen {
			position[key] = len(kept)
			kept = append(kept, target)
			continue
		}
		current := kept[index]
		if targetRank(target) < targetRank(current) {
			kept[index] = target
			dropped = append(dropped, DroppedTarget{Target: current, CoveredBy: target})
			continue
		}
		dropped = append(dropped, DroppedTarget{Target: target, CoveredBy: current})
	}
	return kept, dropped
}

// TorrentLeechRSSTarget builds the native personal-feed target. The feed URL
// contains the user's rsskey: it travels in Secrets, is sealed by the caller
// and is never written to a config_json or a log.
func TorrentLeechRSSTarget(feedURL string) NativeTarget {
	return NativeTarget{
		Name:         "TorrentLeech (RSS)",
		Slug:         "torrentleech-rss",
		DefinitionID: "torrentleech-rss",
		SiteKey:      "torrentleech-rss",
		SiteGroup:    "torrentleech.org",
		Channel:      "latest",
		ViaManager:   "",
		Config:       map[string]string{"default-category": "8000", "site": "torrentleech.org", "channel": "latest", "latest": "true"},
		Secrets:      map[string]string{"url": strings.TrimSpace(feedURL)},
		Enabled:      true,
		Priority:     25,
		Protocol:     ProtocolTorrent,
	}
}

func isURLSetting(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "url", "base_url", "base-url", "endpoint":
		return true
	default:
		return false
	}
}

// isSecretSetting mirrors indexersvc.IsSecretKey without importing the
// service (and its runtime dependencies) into the importer.
func isSecretSetting(name string) bool {
	normalized := strings.ToLower(strings.TrimSpace(name))
	for _, fragment := range []string{
		"api_key", "apikey", "api-key", "token", "password", "passwd",
		"passkey", "secret", "cookie", "username", "user", "login",
	} {
		if strings.Contains(normalized, fragment) {
			return true
		}
	}
	return false
}

// secretSettingKey is the vault key a declaration's secret is stored under.
// Family connectors read the canonical api_key name while the v1 format can
// only declare "apikey"; native definition connectors use the declared name.
func secretSettingKey(def *definition.Definition, name string) string {
	if def != nil && strings.TrimSpace(def.Connector) != "" {
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "apikey", "api-key":
			return "api_key"
		}
	}
	return name
}

// credentialFor picks the imported value that belongs to a secret setting.
func credentialFor(name string, indexer Indexer) (string, bool) {
	normalized := strings.NewReplacer("_", "", "-", "", " ", "").
		Replace(strings.ToLower(strings.TrimSpace(name)))
	switch {
	case strings.Contains(normalized, "user"), strings.Contains(normalized, "login"):
		return indexer.Username, indexer.Username != ""
	case strings.Contains(normalized, "pass"):
		return indexer.Password, indexer.Password != ""
	case strings.Contains(normalized, "api"), strings.Contains(normalized, "token"),
		strings.Contains(normalized, "key"), strings.Contains(normalized, "secret"):
		return indexer.APIKey, indexer.APIKey != ""
	default:
		return "", false
	}
}

func firstMetaURL(def *definition.Definition) string {
	if def == nil {
		return ""
	}
	for _, raw := range def.Meta.URLs {
		if value := strings.TrimSpace(raw); value != "" {
			return value
		}
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func joinInts(values []int) string {
	if len(values) == 0 {
		return ""
	}
	sorted := append([]int(nil), values...)
	sort.Ints(sorted)
	parts := make([]string, 0, len(sorted))
	for _, value := range sorted {
		parts = append(parts, fmt.Sprint(value))
	}
	return strings.Join(parts, ",")
}

// normalizeTargetURL makes an endpoint comparable without changing its path:
// scheme and host are lower-cased and a trailing slash is dropped.
func normalizeTargetURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return strings.TrimRight(raw, "/")
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	return strings.TrimRight(parsed.String(), "/")
}
