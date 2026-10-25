package importers

import (
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/buzzqw/indexarr/internal/catalog"
	"github.com/buzzqw/indexarr/internal/definition"
)

// How an imported manager source becomes an indexarr indexer. There is no
// third mode: indexarr either owns the source natively (a catalog definition
// or a family connector) or it does not create an indexer at all. A source
// without a native route stays in the catalog as "to write" and is never
// turned into a Torznab proxy towards Jackett or Prowlarr.
const (
	TargetNative  = "native"
	TargetFamily  = "family"
	TargetPending = "pending"

	// Reason codes, translated by the UI. They are deliberately short and
	// stable: a new code needs a translation, an old one must keep working.
	ReasonNoDefinition        = "no_definition"
	ReasonMissingCredentials  = "missing_credentials"
	ReasonDisabledUpstream    = "disabled_upstream"
	ReasonNotConfigured       = "not_configured"
	ReasonCatalogCompleteOnly = "catalog_only"
)

// NativeTarget decides how an imported indexer is recreated: from a verified
// (or draft) catalog definition, from a family connector plus the site row, or
// not at all. Secrets are carried in the target because the caller seals them
// in the vault; they must never be logged or included in errors.
type NativeTarget struct {
	Name         string
	Slug         string
	DefinitionID string // catalog/family definition id; empty when pending
	// Status is TargetNative, TargetFamily or TargetPending.
	Status string
	// Family is the connector family (unit3d, nexusphp, …) for family targets.
	Family string
	// DefinitionState is the catalog state of the definition: verificata,
	// stabile, bozza or degradata. It lets the UI show a draft as a draft.
	DefinitionState string
	// DefinitionBody is the synthesized definition for a family site. It is
	// written to the store by ApplyTargets before the indexer row.
	DefinitionBody []byte
	// Reason is a stable reason code, not a sentence.
	Reason string
	// ReasonDetail names the missing setting, without its value.
	ReasonDetail string
	ViaManager   string // legacy value for existing rows; new targets leave it empty
	SiteKey      string // stable site identity used to drop duplicates
	// SiteGroup and Channel pair a native latest feed with a native search
	// source. A site can therefore have two channels without being treated as
	// a duplicate.
	SiteGroup      string
	Channel        string            // "search", "latest" or empty
	BaseURL        string            // site URL, never a manager endpoint
	Config         map[string]string // non-secret settings for config_json
	Secrets        map[string]string // sealed by the caller, never logged
	Enabled        bool
	DisabledReason string // why the indexer is created disabled
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
	definition := strings.ToLower(strings.TrimSpace(t.DefinitionID))
	if definition == "" {
		definition = TargetPending
	}
	key := definition + "|" + normalizeTargetURL(t.BaseURL) + "|" + slug
	if channel := strings.ToLower(strings.TrimSpace(t.Channel)); channel != "" {
		key += "|" + channel
	}
	return key
}

// Targets maps every indexer of the plan to a native target. The built-in
// catalog and the lightweight site rows are loaded once; an error is returned
// instead of guessing, because a wrong catalog would silently turn native
// sources into pending ones.
func Targets(plan Plan) ([]NativeTarget, error) {
	entries, err := catalog.Builtins()
	if err != nil {
		return nil, fmt.Errorf("importers: load catalog: %w", err)
	}
	sites, err := catalog.Sites()
	if err != nil {
		return nil, fmt.Errorf("importers: load sites: %w", err)
	}
	return buildTargets(plan, entries, sites), nil
}

// BuildTargets maps a plan with an explicit catalog. It is the deterministic
// entry point used by tests and by callers that already loaded the catalog.
// Disabled upstream indexers stay disabled: the caller must import them.
func BuildTargets(plan Plan, entries []catalog.Entry) []NativeTarget {
	sites, _ := catalog.Sites()
	return buildTargets(plan, entries, sites)
}

func buildTargets(plan Plan, entries []catalog.Entry, sites []catalog.Site) []NativeTarget {
	out := make([]NativeTarget, 0, len(plan.Indexers))
	for _, indexer := range plan.Indexers {
		out = append(out, target(plan.Source, indexer, entries, sites))
	}
	return out
}

// Target maps one imported indexer to a native target. It is kept exported for
// callers that need a single row; it loads the two static catalogs itself.
func Target(source, managerURL, managerAPIKey string, indexer Indexer, entries []catalog.Entry) NativeTarget {
	// managerURL and managerAPIKey are accepted for compatibility with the
	// CLI call sites. Native adoption never builds a manager endpoint, so they
	// are intentionally unused.
	_ = managerURL
	_ = managerAPIKey
	sites, _ := catalog.Sites()
	return target(source, indexer, entries, sites)
}

func target(source string, indexer Indexer, entries []catalog.Entry, sites []catalog.Site) NativeTarget {
	// 1. A catalog definition with the same id or host wins: it describes the
	//    site exactly, not just its software family.
	if entry, ok := matchDefinition(indexer, entries); ok {
		return catalogEntryTarget(entry, indexer)
	}
	// 2. A manager indexer that is itself a Torznab/Newznab feed is already a
	//    native generic source: indexarr talks to it directly.
	if entry, ok := genericImplementationEntry(indexer, entries); ok {
		return catalogEntryTarget(entry, indexer)
	}
	// 3. A lightweight site row with a known software family is adopted with
	//    the family connector. No Cardigann definition is read or copied.
	if site, ok := matchFamilySite(source, indexer, sites); ok {
		if target, err := familyTarget(site, indexer); err == nil {
			return target
		}
	}
	// 4. Nothing native exists yet: keep the source visible as "to write" and
	//    create no indexer. A proxy to the manager is not a native source.
	return pendingTarget(source, indexer)
}

// nativeState reports whether a catalog entry may be used to create a native
// indexer. Drafts are usable (and marked as drafts), because the alternative
// would be a proxy to the manager, which is exactly what native adoption
// removes.
func nativeState(state string) bool {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "bozza", "verificata", "stabile", "degradata":
		return true
	default:
		return false
	}
}

func matchDefinition(indexer Indexer, entries []catalog.Entry) (catalog.Entry, bool) {
	hint := strings.ToLower(strings.TrimSpace(indexer.DefinitionHint))
	if hint != "" {
		for _, entry := range entries {
			if nativeState(entry.State) && strings.EqualFold(entry.ID, hint) {
				return entry, true
			}
		}
	}

	hosts := indexerHosts(indexer)
	if len(hosts) == 0 {
		return catalog.Entry{}, false
	}
	for _, entry := range entries {
		if !nativeState(entry.State) || entry.Definition == nil || placeholderDefinition(entry.ID) {
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

// placeholderDefinition reports the catalog entries that exist as writing
// examples or generic connectors. Their meta URL is a documentation address,
// so a host match against them would be a false positive.
func placeholderDefinition(id string) bool {
	id = strings.ToLower(strings.TrimSpace(id))
	return id == "example-html" || strings.HasSuffix(id, "-generic")
}

// genericImplementationEntry picks the native connector for a manager indexer
// whose implementation is already a standard feed protocol.
func genericImplementationEntry(indexer Indexer, entries []catalog.Entry) (catalog.Entry, bool) {
	id := ""
	switch strings.ToLower(strings.TrimSpace(indexer.Implementation)) {
	case "torznab":
		id = "torznab-generic"
	case "newznab":
		id = "newznab-generic"
	default:
		return catalog.Entry{}, false
	}
	for _, entry := range entries {
		if entry.ID == id && entry.Definition != nil {
			return entry, true
		}
	}
	return catalog.Entry{}, false
}

// matchFamilySite finds the lightweight row of the same manager source. The
// manager id is the exact identity; the host is the fallback for a row that
// was generated from the other manager. Only rows with a usable family
// connector qualify.
func matchFamilySite(source string, indexer Indexer, sites []catalog.Site) (catalog.Site, bool) {
	source = strings.ToLower(strings.TrimSpace(source))
	remoteID := strings.TrimSpace(indexer.RemoteID)
	for _, site := range sites {
		if !usableFamilySite(site) {
			continue
		}
		if source != "" && strings.EqualFold(site.Source, source) &&
			remoteID != "" && strings.EqualFold(site.SourceID, remoteID) {
			return site, true
		}
	}
	hosts := indexerHosts(indexer)
	if len(hosts) == 0 {
		return catalog.Site{}, false
	}
	for _, site := range sites {
		if !usableFamilySite(site) {
			continue
		}
		for _, host := range siteHosts(site) {
			if hosts[host] {
				return site, true
			}
		}
	}
	return catalog.Site{}, false
}

func usableFamilySite(site catalog.Site) bool {
	family := strings.ToLower(strings.TrimSpace(site.Family))
	return family != "" && family != catalog.FamilyCustom && site.State != catalog.SiteStateDead
}

func siteHosts(site catalog.Site) []string {
	urls := make([]string, 0, len(site.URLs)+1)
	urls = append(urls, site.URL)
	urls = append(urls, site.URLs...)
	out := make([]string, 0, len(urls))
	seen := map[string]bool{}
	for _, raw := range urls {
		host := urlHost(raw)
		if host == "" || seen[host] {
			continue
		}
		seen[host] = true
		out = append(out, host)
	}
	return out
}

// indexerHosts collects the tracker host names the manager exposes. BaseURL is
// included because Prowlarr stores the site URL there; SiteURL and the
// site_link field cover Jackett.
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

func catalogEntryTarget(entry catalog.Entry, indexer Indexer) NativeTarget {
	state := strings.TrimSpace(entry.State)
	if state == "" {
		state = "bozza"
	}
	return definitionTarget(
		entry.ID, TargetNative, state, entry.Family, entry.Definition,
		nil, indexer, indexerSiteURL(indexer, entry.Definition),
	)
}

func familyTarget(site catalog.Site, indexer Indexer) (NativeTarget, error) {
	body, err := catalog.DefinitionForSite(site)
	if err != nil {
		return NativeTarget{}, err
	}
	def, err := definition.Parse(body)
	if err != nil {
		return NativeTarget{}, err
	}
	return definitionTarget(
		site.ID, TargetFamily, "bozza", site.Family, def, body, indexer, site.URL,
	), nil
}

// definitionTarget fills the settings and secrets of a native target from the
// definition. A required credential that the manager does not expose (Jackett
// never returns tracker passwords) disables the row with an explicit reason:
// the coordinator completes it in indexarr instead of silently running an
// indexer that cannot log in.
func definitionTarget(id, status, state, family string, def *definition.Definition, body []byte, indexer Indexer, siteURL string) NativeTarget {
	config := make(map[string]string)
	secrets := make(map[string]string)
	baseURL := firstNonEmpty(siteURL, indexer.SiteURL, indexer.BaseURL, firstMetaURL(def))

	if def != nil {
		for _, setting := range def.Settings {
			name := strings.TrimSpace(setting.Name)
			if name == "" {
				continue
			}
			if isSecretSetting(name) {
				if secret, ok := credentialFor(name, indexer); ok && secret != "" {
					secrets[secretSettingKey(def, name)] = secret
				}
				continue
			}
			value := strings.TrimSpace(setting.Default)
			if isURLSetting(name) {
				value = baseURL
			}
			if value != "" {
				config[name] = value
			}
		}
	}

	// Family connectors read the selected categories from a setting;
	// definition-native connectors map categories inside the YAML.
	if def != nil && strings.TrimSpace(def.Connector) != "" {
		if categories := joinInts(indexer.Categories); categories != "" {
			config["categories"] = categories
		}
	}

	missing := missingRequiredSecret(def, secrets)
	enabled := indexer.Enabled
	reason := ""
	detail := ""
	if missing != "" {
		enabled = false
		reason = ReasonMissingCredentials
		detail = missing
	} else if !indexer.Enabled {
		reason = ReasonDisabledUpstream
	}
	if indexer.CatalogOnly {
		enabled = false
		reason = ReasonNotConfigured
	}

	protocol := indexer.Protocol
	if protocol == "" && def != nil {
		protocol = normalizeProtocol(def.Meta.Protocol, "")
	}

	return NativeTarget{
		Name:            indexer.Name,
		Slug:            slugify(indexer.Name),
		DefinitionID:    id,
		Status:          status,
		Family:          family,
		DefinitionState: state,
		DefinitionBody:  body,
		Reason:          reason,
		ReasonDetail:    detail,
		SiteKey:         siteKeyForIndexer(indexer, id),
		SiteGroup:       siteKeyForIndexer(indexer, id),
		BaseURL:         baseURL,
		Config:          config,
		Secrets:         secrets,
		Enabled:         enabled,
		DisabledReason:  disabledReason(reason, detail, indexer),
		Priority:        indexer.Priority,
		Tags:            append([]string(nil), indexer.Tags...),
		Categories:      append([]int(nil), indexer.Categories...),
		Protocol:        protocol,
		RemoteID:        indexer.RemoteID,
	}
}

// missingRequiredSecret returns the first required secret setting with no
// value. Only secret names are inspected: a missing URL or category is a
// definition bug, not a credential the coordinator can type in.
func missingRequiredSecret(def *definition.Definition, secrets map[string]string) string {
	if def == nil {
		return ""
	}
	for _, setting := range def.Settings {
		if !setting.Required || !isSecretSetting(setting.Name) {
			continue
		}
		key := secretSettingKey(def, setting.Name)
		if strings.TrimSpace(secrets[key]) == "" {
			return setting.Name
		}
	}
	return ""
}

func disabledReason(reason, detail string, indexer Indexer) string {
	switch reason {
	case ReasonMissingCredentials:
		if detail != "" {
			return "missing " + detail
		}
		return "missing credentials"
	case ReasonNotConfigured:
		return "not configured in the manager"
	case ReasonDisabledUpstream:
		if value := strings.TrimSpace(indexer.DisabledReason); value != "" {
			return value
		}
		return "disabled in the manager"
	default:
		return strings.TrimSpace(indexer.DisabledReason)
	}
}

func pendingTarget(source string, indexer Indexer) NativeTarget {
	baseURL := firstNonEmpty(indexer.SiteURL, indexer.Fields["site_link"])
	if !strings.EqualFold(strings.TrimSpace(source), "jackett") {
		// Prowlarr stores the tracker URL in BaseURL; Jackett stores its own
		// per-indexer endpoint there, which must never appear as a site URL.
		baseURL = firstNonEmpty(baseURL, indexer.BaseURL)
	}
	return NativeTarget{
		Name:       indexer.Name,
		Slug:       slugify(indexer.Name),
		Status:     TargetPending,
		Reason:     ReasonNoDefinition,
		SiteKey:    siteKeyForIndexer(indexer, ""),
		SiteGroup:  siteKeyForIndexer(indexer, ""),
		BaseURL:    indexerSiteURL(indexer, nil),
		Enabled:    false,
		Priority:   indexer.Priority,
		Tags:       append([]string(nil), indexer.Tags...),
		Categories: append([]int(nil), indexer.Categories...),
		Protocol:   indexer.Protocol,
		RemoteID:   indexer.RemoteID,
		ViaManager: "",
	}
}

func indexerSiteURL(indexer Indexer, def *definition.Definition) string {
	return firstNonEmpty(indexer.SiteURL, indexer.Fields["site_link"], firstMetaURL(def), indexer.BaseURL)
}

// siteKeyForIndexer is the identity used to recognise the same site arriving
// from Jackett, Prowlarr and the native catalog. The tracker host is preferred
// because manager endpoints are not site identities; the definition id and,
// as a last resort, the slug keep entries without a usable URL distinct.
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

// targetStatus normalises a target built by an older caller that did not set
// Status yet.
func targetStatus(target NativeTarget) string {
	if status := strings.ToLower(strings.TrimSpace(target.Status)); status != "" {
		return status
	}
	if strings.TrimSpace(target.DefinitionID) == "" {
		return TargetPending
	}
	if strings.TrimSpace(target.ViaManager) != "" {
		return TargetFamily
	}
	return TargetNative
}

// targetRank implements the coordinator's preference: a catalog definition
// first, then a family connector, then the "to write" row. A pending target
// never hides a working native source for the same site.
func targetRank(target NativeTarget) int {
	switch targetStatus(target) {
	case TargetNative:
		return 0
	case TargetFamily:
		return 1
	default:
		return 2
	}
}

// PairChannels implements the general rule for a site that has both a native
// latest feed and a native search source: the feed handles latest, the search
// source handles searches, and the two are one site with two channels rather
// than duplicates.
func PairChannels(targets []NativeTarget) []NativeTarget {
	groups := make(map[string][]int)
	for i := range targets {
		if targetStatus(targets[i]) == TargetPending {
			continue
		}
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
		Name:            "TorrentLeech (RSS)",
		Slug:            "torrentleech-rss",
		DefinitionID:    "torrentleech-rss",
		Status:          TargetNative,
		DefinitionState: "bozza",
		SiteKey:         "torrentleech-rss",
		SiteGroup:       "torrentleech.org",
		Channel:         "latest",
		ViaManager:      "",
		Config:          map[string]string{"default-category": "8000", "site": "torrentleech.org", "channel": "latest", "latest": "true"},
		Secrets:         map[string]string{"url": strings.TrimSpace(feedURL)},
		Enabled:         true,
		Priority:        25,
		Protocol:        ProtocolTorrent,
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
	// A tracker passkey and a personal rsskey are not the account password.
	// Mapping them to Password would put the wrong secret in the wrong place
	// and silently break downloads. They stay empty until the user enters them.
	case strings.Contains(normalized, "passkey"), strings.Contains(normalized, "rsskey"):
		return "", false
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
		// Custom manager categories (Prowlarr uses ids >= 100000) are not
		// Newznab categories and become a wall of meaningless chips. Drop
		// them at the import boundary; the standard parent remains.
		if value >= 100000 {
			continue
		}
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
