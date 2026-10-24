package catalog

import (
	_ "embed"
	"encoding/json"
	"net/url"
	"sort"
	"strings"
	"sync"

	"github.com/buzzqw/indexarr/internal/definition"
)

// The catalog index is a compact, build-time summary of the embedded
// definitions. The web UI and the site search read this file only: they never
// parse a definition just to show a row. The full YAML is parsed when an
// indexer is created or run, as before.
//
// Regenerate it after changing a definition or the manifest:
//
//	go generate ./internal/catalog
//
// The catalog test compares the file with BuildIndex(Builtins()) and fails
// when it is stale, so a forgotten regeneration cannot ship.

//go:generate go run ./gensites
//go:generate go run ./genindex

//go:embed index.json
var indexJSON []byte

// IndexEntry is one row of the generated site catalog. It contains everything
// the "Add indexer" page and the pasted-address resolver need, and nothing
// that requires parsing the definition YAML.
type IndexEntry struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Language    string   `json:"language,omitempty"`
	Type        string   `json:"type"`
	Protocol    string   `json:"protocol"`
	Categories  []string `json:"categories,omitempty"`
	Required    []string `json:"required,omitempty"`
	State       string   `json:"state"`
	Mirror      string   `json:"mirror,omitempty"`
	Hosts       []string `json:"hosts,omitempty"`

	// URL is the primary site address. Native entries use the first meta URL;
	// lightweight entries use the manager's link. It lets the UI show the
	// address without parsing a definition and lets the "Prova" action hit the
	// site directly.
	URL string `json:"url,omitempty"`
	// Family is the tracker software family (unit3d, gazelle, nexusphp,
	// torrenttrader, xbtit, torznab, custom). It is empty when the metadata
	// does not support a claim.
	Family string `json:"family,omitempty"`
	// Italian marks the coordinator's Italian priority list, independently of
	// the site language. It drives the quick "Italiani" filter.
	Italian bool `json:"italian,omitempty"`
	// Usage is the observed manager-configuration count. The UI can sort by it
	// without a second database read.
	Usage int `json:"usage,omitempty"`
	// Source is "native", "jackett" or "prowlarr".
	Source string `json:"source,omitempty"`
	// Evidence is the short live-test result for a lightweight entry.
	Evidence string `json:"evidence,omitempty"`
	// Reachable means a lightweight row answered an HTTP probe. It is not a
	// live search proof and must never paint the row green.
	Reachable bool `json:"reachable,omitempty"`
	// Proposable is false for dead rows, so a client can hide them without
	// memorising the state vocabulary.
	Proposable bool `json:"proposable"`
	// Latest is true when the definition publishes a list of new releases.
	// The guided add flow uses it to say what the site gives.
	Latest bool `json:"latest"`
}

// Index returns the embedded catalog index. The slice is a copy: callers may
// sort or filter it without changing the cached value.
func Index() ([]IndexEntry, error) {
	indexOnce.Do(func() {
		if len(indexJSON) == 0 {
			indexCache = []IndexEntry{}
			return
		}
		if err := json.Unmarshal(indexJSON, &indexCache); err != nil {
			indexErr = err
			indexCache = nil
			return
		}
		if indexCache == nil {
			indexCache = []IndexEntry{}
		}
	})
	if indexErr != nil {
		return nil, indexErr
	}
	out := make([]IndexEntry, len(indexCache))
	copy(out, indexCache)
	return out, nil
}

var (
	indexOnce  sync.Once
	indexCache []IndexEntry
	indexErr   error
)

// BuildIndex summarises parsed entries. It is exported so the generator and
// the alignment test use exactly the same rules. Entries are sorted by id and
// every list is sorted, which keeps the generated file stable across runs.
func BuildIndex(entries []Entry) []IndexEntry {
	out := make([]IndexEntry, 0, len(entries))
	for _, entry := range entries {
		def := entry.Definition
		if def == nil {
			continue
		}
		family := strings.TrimSpace(entry.Family)
		if family == "" && validFamilies[def.Connector] {
			family = def.Connector
		}
		item := IndexEntry{
			ID:          entry.ID,
			Name:        def.Meta.Name,
			Description: def.Meta.Description,
			Language:    strings.TrimSpace(def.Meta.Language),
			Type:        def.Meta.Type,
			Protocol:    def.Meta.Protocol,
			Categories:  indexCategories(def.Categories),
			Required:    indexRequired(def.Settings),
			State:       entry.State,
			Mirror:      strings.TrimSpace(def.Meta.Mirror),
			Hosts:       indexHosts(def.Meta.URLs),
			URL:         firstURL(def.Meta.URLs),
			Family:      family,
			Italian:     entry.Italian,
			Usage:       entry.Usage,
			Source:      "native",
			Proposable:  entry.State != "degradata",
			Latest:      def.Meta.HasLatest(),
		}
		out = append(out, item)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// indexCategoryOrder is the order used in the generated file and by the UI
// filters. It is intentionally independent of the definition order.
var indexCategoryOrder = []string{"movies", "tv", "anime", "music", "books", "games", "software", "xxx"}

// indexCategories maps the standard Torznab ids of a definition to the small
// set of groups the UI filters by. A top-level 4000 (PC) covers both games and
// software, so it is listed under both; child 4050 is unambiguously games.
func indexCategories(categories []definition.Category) []string {
	seen := map[string]bool{}
	for _, category := range categories {
		for _, group := range categoryGroups(category.Standard) {
			seen[group] = true
		}
	}
	if len(seen) == 0 {
		return nil
	}
	out := make([]string, 0, len(seen))
	for _, group := range indexCategoryOrder {
		if seen[group] {
			out = append(out, group)
		}
	}
	return out
}

func categoryGroups(id int) []string {
	switch {
	case id >= 2000 && id < 3000:
		return []string{"movies"}
	case id == 5070:
		return []string{"anime"}
	case id >= 5000 && id < 6000:
		return []string{"tv"}
	case id >= 3000 && id < 4000:
		return []string{"music"}
	case id >= 7000 && id < 8000:
		return []string{"books"}
	case id == 4050:
		return []string{"games"}
	case id == 4000:
		return []string{"games", "software"}
	case id >= 4000 && id < 5000:
		return []string{"software"}
	case id >= 1000 && id < 2000:
		return []string{"games"}
	case id >= 6000 && id < 7000:
		return []string{"xxx"}
	default:
		return nil
	}
}

func indexRequired(settings []definition.Setting) []string {
	out := make([]string, 0, len(settings))
	for _, setting := range settings {
		if setting.Required && strings.TrimSpace(setting.Name) != "" {
			out = append(out, setting.Name)
		}
	}
	sort.Strings(out)
	return out
}

func firstURL(urls []string) string {
	for _, raw := range urls {
		if parsed, err := url.Parse(strings.TrimSpace(raw)); err == nil && parsed.Scheme != "" && parsed.Host != "" {
			return strings.TrimSpace(raw)
		}
	}
	return ""
}

func indexHosts(urls []string) []string {
	seen := map[string]bool{}
	for _, raw := range urls {
		parsed, err := url.Parse(strings.TrimSpace(raw))
		if err != nil || parsed.Hostname() == "" {
			continue
		}
		host := strings.TrimPrefix(strings.ToLower(parsed.Hostname()), "www.")
		host = strings.TrimSuffix(host, ".")
		if host == "" || seen[host] {
			continue
		}
		seen[host] = true
	}
	if len(seen) == 0 {
		return nil
	}
	out := make([]string, 0, len(seen))
	for host := range seen {
		out = append(out, host)
	}
	sort.Strings(out)
	return out
}

// CategoryGroups maps the standard Torznab ids found in manager metadata to
// the small set of groups the UI filters by. It is exported for the catalog
// generator; BuildIndex uses the same rule for native definitions.
func CategoryGroups(ids []int) []string {
	seen := map[string]bool{}
	for _, id := range ids {
		for _, group := range categoryGroups(id) {
			seen[group] = true
		}
	}
	if len(seen) == 0 {
		return nil
	}
	out := make([]string, 0, len(seen))
	for _, group := range indexCategoryOrder {
		if seen[group] {
			out = append(out, group)
		}
	}
	return out
}
