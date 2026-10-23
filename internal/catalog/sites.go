package catalog

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/buzzqw/indexarr/internal/definition"
)

// The site catalog is a second, lighter layer beside the native definitions.
// A native definition describes exactly how to search a site and is validated
// and tested here. A Site is a single row generated from the metadata a
// manager already has (Jackett or Prowlarr): name, address, privacy, language,
// categories and, when it can be established from evidence, the tracker
// family. It lets the catalog list sites that have no native definition yet
// without importing a Cardigann definition.
//
// A Site is deliberately not a definition and is never parsed as one. When the
// user adds a site whose family connector exists, DefinitionForSite builds a
// small native definition from the row; the family connector then reads the
// address and credentials from the normal indexer settings.
const (
	SiteStateVerified = "verificata" // live-tested with GET and usable
	SiteStateTry      = "da-provvi"  // not proven yet, or a private site without an account
	SiteStateDead     = "morta"      // unreachable; never proposed

	FamilyUnit3D        = "unit3d"
	FamilyGazelle       = "gazelle"
	FamilyNexusPHP      = "nexusphp"
	FamilyTorrentTrader = "torrenttrader"
	FamilyXbtit         = "xbtit"
	FamilyTorznab       = "torznab"
	FamilyCustom        = "custom"
)

// Site is one lightweight catalog row generated from manager metadata.
type Site struct {
	ID          string   `yaml:"id" json:"id"`
	NativeID    string   `yaml:"native_id,omitempty" json:"nativeId,omitempty"`
	Name        string   `yaml:"name" json:"name"`
	Description string   `yaml:"description,omitempty" json:"description,omitempty"`
	URL         string   `yaml:"url" json:"url"`
	URLs        []string `yaml:"urls,omitempty" json:"urls,omitempty"`
	Type        string   `yaml:"type" json:"type"`
	Language    string   `yaml:"language,omitempty" json:"language,omitempty"`
	Family      string   `yaml:"family,omitempty" json:"family,omitempty"`
	Categories  []string `yaml:"categories,omitempty" json:"categories,omitempty"`
	State       string   `yaml:"state" json:"state"`
	Italian     bool     `yaml:"italian,omitempty" json:"italian,omitempty"`
	Usage       int      `yaml:"usage,omitempty" json:"usage,omitempty"`
	Required    []string `yaml:"required,omitempty" json:"required,omitempty"`
	Source      string   `yaml:"source,omitempty" json:"source,omitempty"`
	SourceID    string   `yaml:"source_id,omitempty" json:"sourceId,omitempty"`
	Checked     string   `yaml:"checked,omitempty" json:"checked,omitempty"`
	Evidence    string   `yaml:"evidence,omitempty" json:"evidence,omitempty"`
	Note        string   `yaml:"note,omitempty" json:"note,omitempty"`
}

type siteManifest struct {
	Schema  int    `yaml:"schema"`
	Updated string `yaml:"updated,omitempty"`
	Source  string `yaml:"source,omitempty"`
	Sites   []Site `yaml:"sites"`
}

var (
	validSiteStates = map[string]bool{
		SiteStateVerified: true,
		SiteStateTry:      true,
		SiteStateDead:     true,
	}
	validFamilies = map[string]bool{
		FamilyUnit3D:        true,
		FamilyGazelle:       true,
		FamilyNexusPHP:      true,
		FamilyTorrentTrader: true,
		FamilyXbtit:         true,
		FamilyTorznab:       true,
		FamilyCustom:        true,
	}
	validSiteTypes = map[string]bool{
		"public":       true,
		"semi-private": true,
		"private":      true,
	}
)

// Sites returns the lightweight site rows embedded in the binary, in manifest
// order. The rows are validated with the same strict YAML decoder as the native
// manifest: unknown keys, duplicate ids and impossible states are rejected.
func Sites() ([]Site, error) {
	body, err := fs.ReadFile(builtinFS, "sites.yaml")
	if err != nil {
		return nil, fmt.Errorf("catalog: read sites manifest: %w", err)
	}
	return parseSites(body)
}

func parseSites(body []byte) ([]Site, error) {
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, errors.New("catalog: empty sites manifest")
	}
	dec := yaml.NewDecoder(bytes.NewReader(body))
	dec.KnownFields(true)
	var manifest siteManifest
	if err := dec.Decode(&manifest); err != nil {
		return nil, fmt.Errorf("catalog: sites YAML: %w", err)
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); err == nil {
		return nil, fmt.Errorf("catalog: sites: multiple YAML documents are not supported (second document at line %d)", extra.Line)
	}
	if manifest.Schema != 1 {
		return nil, fmt.Errorf("catalog: sites schema must be 1 (got %d)", manifest.Schema)
	}
	if len(manifest.Sites) == 0 {
		return nil, errors.New("catalog: sites manifest has no sites")
	}
	seen := make(map[string]int, len(manifest.Sites))
	for i, site := range manifest.Sites {
		if !slugPattern.MatchString(site.ID) {
			return nil, fmt.Errorf("catalog: sites[%d].id: must be a slug matching %s", i, slugPattern.String())
		}
		if first, ok := seen[site.ID]; ok {
			return nil, fmt.Errorf("catalog: sites[%d].id: duplicate of sites[%d].id (%q)", i, first, site.ID)
		}
		seen[site.ID] = i
		if strings.TrimSpace(site.Name) == "" {
			return nil, fmt.Errorf("catalog: sites[%d].name: is required", i)
		}
		if !validSiteStates[site.State] {
			return nil, fmt.Errorf("catalog: sites[%d].state: must be one of verificata, da-provvi, morta", i)
		}
		if !validSiteTypes[site.Type] {
			return nil, fmt.Errorf("catalog: sites[%d].type: must be public, semi-private or private", i)
		}
		if site.Family != "" && !validFamilies[site.Family] {
			return nil, fmt.Errorf("catalog: sites[%d].family: unknown family %q", i, site.Family)
		}
		if site.URL == "" {
			return nil, fmt.Errorf("catalog: sites[%d].url: is required", i)
		}
		if parsed, err := url.Parse(site.URL); err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return nil, fmt.Errorf("catalog: sites[%d].url: must be an absolute URL", i)
		}
		if site.State == SiteStateVerified && strings.TrimSpace(site.Evidence) == "" {
			return nil, fmt.Errorf("catalog: sites[%d]: a verified site needs evidence", i)
		}
		if site.State == SiteStateDead && strings.TrimSpace(site.Evidence) == "" {
			return nil, fmt.Errorf("catalog: sites[%d]: a dead site needs the failure evidence", i)
		}
	}
	return manifest.Sites, nil
}

// BuildFullIndex summarises parsed native entries and lightweight sites into
// the generated index read by the UI. Native entries keep their state; sites
// carry their own. The result is sorted by id, which the catalog test asserts.
func BuildFullIndex(entries []Entry, sites []Site) []IndexEntry {
	out := BuildIndex(entries)
	seen := make(map[string]bool, len(out)+len(sites))
	for _, item := range out {
		seen[item.ID] = true
	}
	for _, site := range sites {
		if seen[site.ID] {
			continue
		}
		seen[site.ID] = true
		out = append(out, siteIndexEntry(site))
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func siteIndexEntry(site Site) IndexEntry {
	hosts := indexHosts(site.URLs)
	if len(hosts) == 0 {
		hosts = indexHosts([]string{site.URL})
	}
	categories := append([]string(nil), site.Categories...)
	sort.SliceStable(categories, func(i, j int) bool {
		return categoryRank(categories[i]) < categoryRank(categories[j])
	})
	required := append([]string(nil), site.Required...)
	sort.Strings(required)
	return IndexEntry{
		ID:          site.ID,
		Name:        site.Name,
		Description: site.Description,
		Language:    strings.TrimSpace(site.Language),
		Type:        site.Type,
		Protocol:    "torrent",
		Categories:  categories,
		Required:    required,
		State:       site.State,
		Hosts:       hosts,
		URL:         site.URL,
		Family:      site.Family,
		Italian:     site.Italian,
		Usage:       site.Usage,
		Source:      site.Source,
		Evidence:    site.Evidence,
		Proposable:  site.State != SiteStateDead,
	}
}

func categoryRank(group string) int {
	for index, candidate := range indexCategoryOrder {
		if candidate == group {
			return index
		}
	}
	return len(indexCategoryOrder)
}

// DefinitionForSite turns a lightweight row of a known family into the small
// native definition the family connector expects. It is the bridge that makes
// "name, URL, family, categories" enough to add a site. A custom or unknown
// family cannot be synthesised and returns an error.
func DefinitionForSite(site Site) ([]byte, error) {
	if !validFamilies[site.Family] || site.Family == FamilyCustom {
		return nil, fmt.Errorf("catalog: site %q has no family connector", site.ID)
	}
	if site.State == SiteStateDead {
		return nil, fmt.Errorf("catalog: site %q is dead", site.ID)
	}
	if site.URL == "" {
		return nil, fmt.Errorf("catalog: site %q has no URL", site.ID)
	}
	settings := familySettings(site.Family)
	categories := familyCategories(site.Categories)
	if len(categories) == 0 {
		categories = []definition.Category{{Site: "8000", Standard: 8000, Desc: "Other"}}
	}
	doc := definition.Definition{
		Schema:    definition.CurrentSchema,
		Connector: site.Family,
		Meta: definition.Meta{
			ID:          site.ID,
			Name:        site.Name,
			Language:    site.Language,
			Type:        site.Type,
			Protocol:    "torrent",
			Author:      "indexarr",
			Description: site.Description,
			URLs:        []string{site.URL},
			Updated:     "2026-10-23",
			Latest:      boolPtr(false),
		},
		Settings:   settings,
		Categories: categories,
		Capabilities: definition.Capabilities{
			Search: []string{"q"},
		},
	}
	body, err := yaml.Marshal(doc)
	if err != nil {
		return nil, err
	}
	if _, err := definition.Parse(body); err != nil {
		return nil, fmt.Errorf("catalog: synthesized definition for %q: %w", site.ID, err)
	}
	return body, nil
}

func familySettings(family string) []definition.Setting {
	urlSetting := definition.Setting{Name: "url", Type: "text", Label: "Base URL", Required: true}
	switch family {
	case FamilyUnit3D, FamilyGazelle, FamilyTorznab:
		return []definition.Setting{
			urlSetting,
			{Name: "apikey", Type: "password", Label: "API key", Required: true},
		}
	case FamilyNexusPHP:
		return []definition.Setting{
			urlSetting,
			{Name: "username", Type: "text", Label: "Username", Required: true},
			{Name: "password", Type: "password", Label: "Password", Required: true},
			{Name: "passkey", Type: "password", Label: "Passkey", Required: false},
		}
	case FamilyTorrentTrader, FamilyXbtit:
		return []definition.Setting{
			urlSetting,
			{Name: "username", Type: "text", Label: "Username", Required: true},
			{Name: "password", Type: "password", Label: "Password", Required: true},
		}
	default:
		return []definition.Setting{urlSetting}
	}
}

func familyCategories(groups []string) []definition.Category {
	standard := map[string]int{
		"movies": 2000, "tv": 5000, "anime": 5070, "music": 3000,
		"books": 7000, "games": 4050, "software": 4000, "xxx": 6000,
	}
	var out []definition.Category
	for _, group := range indexCategoryOrder {
		if !containsGroup(groups, group) {
			continue
		}
		id := standard[group]
		out = append(out, definition.Category{Site: fmt.Sprint(id), Standard: id, Desc: group})
	}
	return out
}

func containsGroup(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func boolPtr(value bool) *bool { return &value }

// SiteStats is the small report the UI and the release notes need: how many
// rows are verified, still to try and dead, split by source type.
type SiteStats struct {
	Total          int            `json:"total"`
	Verified       int            `json:"verified"`
	ToTry          int            `json:"toTry"`
	Dead           int            `json:"dead"`
	Italian        int            `json:"italian"`
	PublicVerified int            `json:"publicVerified"`
	Families       map[string]int `json:"families"`
}

// Stats counts sites and native entries without hiding a dead row.
func Stats(entries []Entry, sites []Site) SiteStats {
	stats := SiteStats{Total: len(entries) + len(sites), Families: map[string]int{}}
	for _, entry := range entries {
		switch entry.State {
		case "verificata", "stabile":
			stats.Verified++
			if entry.Definition != nil && entry.Definition.Meta.Type == "public" {
				stats.PublicVerified++
			}
		case "degradata":
			stats.Dead++
		default:
			stats.ToTry++
		}
	}
	for _, site := range sites {
		switch site.State {
		case SiteStateVerified:
			stats.Verified++
			if site.Type == "public" {
				stats.PublicVerified++
			}
		case SiteStateDead:
			stats.Dead++
		default:
			stats.ToTry++
		}
		if site.Italian {
			stats.Italian++
		}
		if site.Family != "" {
			stats.Families[site.Family]++
		}
	}
	return stats
}
