// Command gensites builds sites.yaml, the lightweight catalog layer, from the
// committed manager metadata snapshots.
//
// It is run by "go generate ./internal/catalog" and never touches the network.
// The inputs are:
//
//   - metadata/jackett.json and metadata/prowlarr.json, committed snapshots
//     that are no longer refreshed from a live manager;
//   - metadata/families.yaml, curated software-family evidence;
//   - metadata/verification.yaml, live GET evidence refreshed by genverify;
//   - metadata/manual.yaml, the few priority rows that no manager knows;
//   - the native definitions, used to skip duplicate hosts.
//
// A row is "verificata" only when verification.yaml holds a successful GET and
// the row is public/semi-private and not a login or anti-bot page. A private
// site is always "da provvi" because no account was used. A failed GET that is
// a connection/DNS failure is "morta"; a 403 or 5xx stays "da provvi" because
// the site answered.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/buzzqw/indexarr/internal/catalog"
	"github.com/buzzqw/indexarr/internal/catalog/managerdata"
)

type snapshot struct {
	Schema  int                          `json:"schema"`
	Source  string                       `json:"source"`
	Fetched string                       `json:"fetched"`
	Sites   []managerdata.CatalogIndexer `json:"sites"`
}

type familyEntry struct {
	Family     string `yaml:"family"`
	Evidence   string `yaml:"evidence"`
	Confidence string `yaml:"confidence,omitempty"`
}

type familyManifest struct {
	Families map[string]familyEntry `yaml:"families"`
}

type manualManifest struct {
	Sites []catalog.Site `yaml:"sites"`
}

type blockedManifest struct {
	Schema  int             `yaml:"schema"`
	Updated string          `yaml:"updated,omitempty"`
	Sources []blockedSource `yaml:"sources"`
}

type blockedSource struct {
	ID       string `yaml:"id"`
	State    string `yaml:"state"`
	Reason   string `yaml:"reason"`
	Evidence string `yaml:"evidence"`
	Checked  string `yaml:"checked"`
}

type verificationManifest struct {
	Checked string                         `yaml:"checked"`
	Results map[string]catalogVerification `yaml:"results"`
}

type catalogVerification struct {
	URL      string   `yaml:"url"`
	HTTP     int      `yaml:"http,omitempty"`
	Final    string   `yaml:"final,omitempty"`
	Title    string   `yaml:"title,omitempty"`
	Bytes    int64    `yaml:"bytes,omitempty"`
	Families []string `yaml:"families,omitempty"`
	Checked  string   `yaml:"checked"`
	Error    string   `yaml:"error,omitempty"`
}

type output struct {
	Schema  int            `yaml:"schema"`
	Updated string         `yaml:"updated"`
	Source  string         `yaml:"source"`
	Sites   []catalog.Site `yaml:"sites"`
}

var italianPriority = map[string]bool{
	"ilcorsaroblu":    true,
	"itatorrents":     true,
	"arabafenice":     true,
	"futuretorrent":   true,
	"hdtorrentsit":    true,
	"shareisland-api": true,
	"girotorrent":     true,
	"trackerzero":     true,
	"ildragonegro":    true,
	"mircrew":         true,
	"extto":           true,
}

var skipLocalServices = map[string]bool{
	"magnetico": true,
	"bitmagnet": true,
}

func main() {
	dir := flag.String("metadata", "metadata", "metadata directory")
	out := flag.String("out", "sites.yaml", "output file")
	flag.Parse()

	jackett := loadSnapshot(filepath.Join(*dir, "jackett.json"))
	prowlarr := loadSnapshot(filepath.Join(*dir, "prowlarr.json"))
	families := loadFamilies(filepath.Join(*dir, "families.yaml"))
	verification := loadVerification(filepath.Join(*dir, "verification.yaml"))
	manual := loadManual(filepath.Join(*dir, "manual.yaml"))
	blocked := loadBlocked(filepath.Join(*dir, "blocked.yaml"))

	native, err := catalog.Builtins()
	if err != nil {
		fmt.Fprintf(os.Stderr, "gensites: native catalog: %v\n", err)
		os.Exit(1)
	}
	nativeHosts := map[string]string{}
	for _, entry := range native {
		if entry.Definition == nil {
			continue
		}
		for _, raw := range entry.Definition.Meta.URLs {
			if host := siteHost(raw); host != "" {
				nativeHosts[host] = entry.ID
			}
		}
	}

	usageByHost := map[string]int{}
	for _, site := range jackett.Sites {
		if site.Configured {
			if host := siteHost(site.URL); host != "" {
				usageByHost[host]++
			}
		}
	}
	for _, site := range prowlarr.Sites {
		if site.Configured {
			if host := siteHost(site.URL); host != "" {
				usageByHost[host]++
			}
		}
	}

	var sites []catalog.Site
	seenID := map[string]bool{}
	seenHost := map[string]bool{}
	add := func(site catalog.Site) {
		if site.ID == "" || seenID[site.ID] {
			return
		}
		seenID[site.ID] = true
		sites = append(sites, site)
	}

	for _, item := range jackett.Sites {
		if skipLocalServices[item.SourceID] {
			continue
		}
		host := siteHost(item.URL)
		if host != "" && nativeHosts[host] != "" {
			continue
		}
		family, knownFamily := families.Families[item.SourceID]
		key := "jackett:" + item.SourceID
		live, hasLive := verification.Results[key]
		// A live probe can prove the software family even when the curated
		// table has no entry. Only one detected family is accepted: two markers
		// on the same page are ambiguous, not a stronger claim.
		if !knownFamily && hasLive {
			if detected := singleFamily(live.Families); detected != "" {
				family = familyEntry{
					Family:     detected,
					Confidence: "possible",
					Evidence:   "live GET markers on " + live.URL,
				}
				knownFamily = true
			}
		}
		state, evidence := decideState(item.Type, knownFamily, live, hasLive)
		id := "jk-" + slugify(item.SourceID)
		site := catalog.Site{
			ID:          id,
			Name:        item.Name,
			Description: item.Description,
			URL:         item.URL,
			URLs:        item.URLs,
			Type:        normalizeType(item.Type),
			Language:    item.Language,
			Family:      familyID(family, knownFamily),
			Categories:  catalog.CategoryGroups(item.Categories),
			State:       state,
			Italian:     italianPriority[item.SourceID] || strings.HasPrefix(strings.ToLower(item.Language), "it"),
			Usage:       usageByHost[host],
			Required:    requiredFor(normalizeType(item.Type), familyID(family, knownFamily)),
			Source:      "jackett",
			SourceID:    item.SourceID,
			Checked:     live.Checked,
			Evidence:    evidence,
			Note:        familyEvidence(family, knownFamily),
		}
		if host != "" && seenHost[host] {
			continue
		}
		if host != "" {
			seenHost[host] = true
		}
		applyBlocked(&site, blocked)
		add(site)
	}

	for _, item := range prowlarr.Sites {
		host := siteHost(item.URL)
		if host != "" && (nativeHosts[host] != "" || seenHost[host]) {
			continue
		}
		family, knownFamily := families.Families[item.SourceID]
		key := "prowlarr:" + item.SourceID
		live, hasLive := verification.Results[key]
		if !knownFamily && hasLive {
			if detected := singleFamily(live.Families); detected != "" {
				family = familyEntry{
					Family:     detected,
					Confidence: "possible",
					Evidence:   "live GET markers on " + live.URL,
				}
				knownFamily = true
			}
		}
		state, evidence := decideState(item.Type, knownFamily, live, hasLive)
		site := catalog.Site{
			ID:          "pr-" + slugify(item.SourceID),
			Name:        item.Name,
			Description: item.Description,
			URL:         item.URL,
			URLs:        item.URLs,
			Type:        normalizeType(item.Type),
			Language:    item.Language,
			Family:      familyID(family, knownFamily),
			Categories:  catalog.CategoryGroups(item.Categories),
			State:       state,
			Italian:     strings.HasPrefix(strings.ToLower(item.Language), "it"),
			Usage:       usageByHost[host],
			Required:    requiredFor(normalizeType(item.Type), familyID(family, knownFamily)),
			Source:      "prowlarr",
			SourceID:    item.SourceID,
			Checked:     live.Checked,
			Evidence:    evidence,
			Note:        familyEvidence(family, knownFamily),
		}
		if host != "" {
			seenHost[host] = true
		}
		applyBlocked(&site, blocked)
		add(site)
	}

	for _, item := range manual.Sites {
		key := "manual:" + item.ID
		live, hasLive := verification.Results[key]
		state, evidence := decideState(item.Type, item.Family != "", live, hasLive)
		item.State = state
		item.Checked = live.Checked
		if item.Evidence == "" {
			item.Evidence = evidence
		}
		item.Required = requiredFor(item.Type, item.Family)
		item.Usage = usageByHost[siteHost(item.URL)]
		if item.Source == "" {
			item.Source = "manual"
		}
		applyBlocked(&item, blocked)
		add(item)
	}

	sort.SliceStable(sites, func(i, j int) bool { return sites[i].ID < sites[j].ID })
	manifest := output{
		Schema:  1,
		Updated: "2026-10-27",
		Source:  "jackett+prowlarr",
		Sites:   sites,
	}
	body, err := yaml.Marshal(manifest)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gensites: encode: %v\n", err)
		os.Exit(1)
	}
	header := "# Generated by go generate ./internal/catalog (gensites). DO NOT EDIT.\n#\n# Lightweight site rows built from manager metadata, never from a Cardigann\n# definition. States: verificata = live GET evidence; da-provvi = not proven\n# or private without an account; bloccata = cannot be implemented for a\n# non-code reason; morta = unreachable and not proposed.\n"
	if err := os.WriteFile(*out, append([]byte(header), body...), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "gensites: write %s: %v\n", *out, err)
		os.Exit(1)
	}
	stats := map[string]int{}
	familiesCount := map[string]int{}
	for _, site := range sites {
		stats[site.State]++
		if site.Family != "" {
			familiesCount[site.Family]++
		}
	}
	fmt.Printf("sites: %d (verificata %d, da-provvi %d, bloccata %d, morta %d)\n", len(sites), stats[catalog.SiteStateVerified], stats[catalog.SiteStateTry], stats[catalog.SiteStateBlocked], stats[catalog.SiteStateDead])
	for _, family := range []string{"unit3d", "gazelle", "nexusphp", "torrenttrader", "xbtit"} {
		fmt.Printf("  %-14s %d\n", family, familiesCount[family])
	}
}

func decideState(siteType string, familyKnown bool, live catalogVerification, hasLive bool) (string, string) {
	if !hasLive {
		return catalog.SiteStateTry, "no live probe"
	}
	evidence := evidenceText(live)
	if live.Error != "" {
		if deadProbeError(live.Error) {
			return catalog.SiteStateDead, evidence
		}
		// A timeout, an IPv6 route or a TLS hiccup says nothing about the site:
		// it stays "da provvi" and is retried on the next probe run.
		return catalog.SiteStateTry, evidence + "; probe inconclusive"
	}
	if live.HTTP >= 200 && live.HTTP < 400 {
		if siteType == "private" {
			return catalog.SiteStateTry, evidence + "; private, no account used"
		}
		if isLoginOrChallenge(live) {
			return catalog.SiteStateTry, evidence + "; login or anti-bot page"
		}
		if live.Bytes > 0 && live.Bytes < 800 {
			return catalog.SiteStateTry, evidence + "; page too small to trust"
		}
		return catalog.SiteStateVerified, evidence
	}
	return catalog.SiteStateTry, evidence
}

// deadProbeError is deliberately narrow: only a name that does not resolve or
// a refused connection proves the address is gone. Timeouts and IPv6 routes
// are transient and must not remove a source from the catalog.
func deadProbeError(text string) bool {
	text = strings.ToLower(text)
	for _, marker := range []string{"no such host", "connection refused"} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func evidenceText(live catalogVerification) string {
	target := live.Final
	if target == "" {
		target = live.URL
	}
	if live.Error != "" {
		return fmt.Sprintf("GET %s -> %s, checked %s", live.URL, live.Error, live.Checked)
	}
	title := strings.TrimSpace(live.Title)
	if title == "" {
		title = "(no title)"
	}
	return fmt.Sprintf("GET %s -> HTTP %d, %d bytes, title %q, checked %s", target, live.HTTP, live.Bytes, title, live.Checked)
}

func isLoginOrChallenge(live catalogVerification) bool {
	text := strings.ToLower(live.Title + " " + live.Final)
	for _, marker := range []string{
		"just a moment", "making sure you're not a bot", "attention required",
		"access denied", "forbidden", "login", "sign in", "signin", "connexion",
		"entrar", "登录", "登入", "вход", "bejelentkezés", "zaloguj", "inloggen",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

// singleFamily returns the detected family only when the page carries exactly
// one family marker. Ambiguous pages stay without a family: a wrong connector
// is worse than a source queued for a definition.
func singleFamily(families []string) string {
	seen := map[string]bool{}
	value := ""
	for _, family := range families {
		family = strings.ToLower(strings.TrimSpace(family))
		if family == "" || seen[family] {
			continue
		}
		seen[family] = true
		value = family
	}
	if len(seen) != 1 {
		return ""
	}
	return value
}

func familyID(entry familyEntry, known bool) string {
	if !known {
		return ""
	}
	return entry.Family
}

func familyEvidence(entry familyEntry, known bool) string {
	if !known {
		return ""
	}
	note := "family " + entry.Family
	if entry.Confidence != "" {
		note += " (" + entry.Confidence + ")"
	}
	if entry.Evidence != "" {
		note += ": " + entry.Evidence
	}
	return note
}

func requiredFor(siteType, family string) []string {
	switch family {
	case catalog.FamilyUnit3D, catalog.FamilyGazelle:
		return []string{"apikey"}
	case catalog.FamilyNexusPHP, catalog.FamilyTorrentTrader, catalog.FamilyXbtit:
		return []string{"username", "password"}
	case catalog.FamilyTorznab:
		return []string{"url"}
	}
	if siteType == "private" {
		return []string{"username", "password"}
	}
	// A lightweight row is never a one-click add: even a public site needs its
	// address confirmed and its family connector chosen. Keeping "url" as a
	// required field also stops the old quick-add path from trying to load a
	// native definition that does not exist for this row.
	return []string{"url"}
}

func normalizeType(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "public":
		return "public"
	case "semi-private":
		return "semi-private"
	default:
		return "private"
	}
}

func slugify(raw string) string {
	raw = strings.ToLower(strings.TrimSpace(raw))
	var out strings.Builder
	lastDash := false
	for _, r := range raw {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			out.WriteRune(r)
			lastDash = false
		default:
			if !lastDash && out.Len() > 0 {
				out.WriteByte('-')
				lastDash = true
			}
		}
	}
	return strings.Trim(out.String(), "-")
}

func siteHost(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Hostname() == "" {
		return ""
	}
	return strings.TrimPrefix(strings.TrimSuffix(strings.ToLower(parsed.Hostname()), "."), "www.")
}

func loadSnapshot(path string) snapshot {
	body, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gensites: %v\n", err)
		os.Exit(1)
	}
	var value snapshot
	if err := json.Unmarshal(body, &value); err != nil {
		fmt.Fprintf(os.Stderr, "gensites: %s: %v\n", path, err)
		os.Exit(1)
	}
	return value
}

func loadFamilies(path string) familyManifest {
	body, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gensites: %v\n", err)
		os.Exit(1)
	}
	var value familyManifest
	if err := yaml.Unmarshal(body, &value); err != nil {
		fmt.Fprintf(os.Stderr, "gensites: %s: %v\n", path, err)
		os.Exit(1)
	}
	return value
}

func loadVerification(path string) verificationManifest {
	body, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gensites: %v\n", err)
		os.Exit(1)
	}
	var value verificationManifest
	if err := yaml.Unmarshal(body, &value); err != nil {
		fmt.Fprintf(os.Stderr, "gensites: %s: %v\n", path, err)
		os.Exit(1)
	}
	return value
}

func loadManual(path string) manualManifest {
	body, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gensites: %v\n", err)
		os.Exit(1)
	}
	var value manualManifest
	if err := yaml.Unmarshal(body, &value); err != nil {
		fmt.Fprintf(os.Stderr, "gensites: %s: %v\n", path, err)
		os.Exit(1)
	}
	return value
}

// loadBlocked reads the curated list of sources that cannot be implemented for
// a non-code reason. The key is the manager source id, or the manual row id.
func loadBlocked(path string) map[string]blockedSource {
	body, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gensites: %v\n", err)
		os.Exit(1)
	}
	var value blockedManifest
	if err := yaml.Unmarshal(body, &value); err != nil {
		fmt.Fprintf(os.Stderr, "gensites: %s: %v\n", path, err)
		os.Exit(1)
	}
	if value.Schema != 1 {
		fmt.Fprintf(os.Stderr, "gensites: %s: schema must be 1\n", path)
		os.Exit(1)
	}
	out := make(map[string]blockedSource, len(value.Sources))
	for i, source := range value.Sources {
		key := strings.ToLower(strings.TrimSpace(source.ID))
		switch {
		case key == "":
			fmt.Fprintf(os.Stderr, "gensites: %s: sources[%d].id is required\n", path, i)
			os.Exit(1)
		case source.State != catalog.SiteStateBlocked && source.State != catalog.SiteStateDead:
			fmt.Fprintf(os.Stderr, "gensites: %s: sources[%d].state must be bloccata or morta\n", path, i)
			os.Exit(1)
		case strings.TrimSpace(source.Reason) == "":
			fmt.Fprintf(os.Stderr, "gensites: %s: sources[%d].reason is required\n", path, i)
			os.Exit(1)
		case strings.TrimSpace(source.Evidence) == "":
			fmt.Fprintf(os.Stderr, "gensites: %s: sources[%d].evidence is required\n", path, i)
			os.Exit(1)
		case strings.TrimSpace(source.Checked) == "":
			fmt.Fprintf(os.Stderr, "gensites: %s: sources[%d].checked is required\n", path, i)
			os.Exit(1)
		}
		if _, exists := out[key]; exists {
			fmt.Fprintf(os.Stderr, "gensites: %s: duplicate source %q\n", path, key)
			os.Exit(1)
		}
		out[key] = source
	}
	return out
}

// applyBlocked lets the curated table override the live probe. A blocked row
// stays in the catalog with the measured evidence but is removed from the
// queue and from the proposals.
func applyBlocked(site *catalog.Site, blocked map[string]blockedSource) {
	if site == nil || len(blocked) == 0 {
		return
	}
	for _, key := range []string{site.SourceID, site.ID} {
		entry, ok := blocked[strings.ToLower(strings.TrimSpace(key))]
		if !ok {
			continue
		}
		site.State = entry.State
		site.Evidence = entry.Evidence
		site.Note = entry.Reason
		site.Checked = entry.Checked
		return
	}
}
