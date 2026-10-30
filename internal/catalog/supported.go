package catalog

// This file renders docs/SUPPORTED.md: the human-readable list of every source
// the catalog ships. It is generated, never written by hand, so the list
// cannot drift from the definitions. The catalog test compares the file with
// RenderSupportedDocs(Builtins()) and fails while it is stale:
//
//	go generate ./internal/catalog
//
// The rule is in AGENTS.md: adding a provider means regenerating this file in
// the same commit.

import (
	"fmt"
	"sort"
	"strings"

	"github.com/buzzqw/indexarr/internal/definition"
)

// genericConnectors maps a connector to the sentence that describes what it
// covers. A definition with one of these connectors is not a site: it is the
// endpoint or the family the user points at.
var genericConnectors = map[string]string{
	"unit3d":        "Any UNIT3D tracker: site address, then username and password or a session cookie",
	"gazelle":       "Any Gazelle tracker: site address and credentials",
	"nexusphp":      "Any NexusPHP tracker: site address and credentials",
	"torrenttrader": "Any TorrentTrader site: site address and credentials",
	"xbtit":         "Any Xbtit site: site address and credentials",
	"torznab":       "Any Torznab indexer: full endpoint and API key",
	"newznab":       "Any Newznab/Usenet indexer: full endpoint and API key",
	"rss":           "Any RSS/Atom feed with a .torrent, .nzb or magnet enclosure",
}

// accessLabels turns the required setting names into one readable cell.
var accessLabels = []struct {
	names []string
	label string
}{
	{[]string{"username", "password"}, "Account"},
	{[]string{"cookie"}, "Cookie"},
	{[]string{"apikey", "passkey", "api_key"}, "API key"},
	{[]string{"uploader"}, "Uploader"},
	{[]string{"url"}, "Endpoint"},
}

func accessLabel(def *definition.Definition) string {
	required := make(map[string]bool)
	for _, setting := range def.Settings {
		if setting.Required || strings.EqualFold(setting.Name, "cookie") {
			required[strings.ToLower(strings.TrimSpace(setting.Name))] = true
		}
	}
	for _, candidate := range accessLabels {
		for _, name := range candidate.names {
			if required[name] {
				return candidate.label
			}
		}
	}
	return "None"
}

func stateLabel(state string) string {
	switch state {
	case "verificata":
		return "verified"
	case "stabile":
		return "stable"
	case "degradata":
		return "degraded"
	case "bozza":
		return "draft"
	}
	return state
}

func shortLanguage(tag string) string {
	tag = strings.TrimSpace(tag)
	if tag == "" {
		return "—"
	}
	if i := strings.IndexByte(tag, '-'); i > 0 {
		return tag[:i]
	}
	return tag
}

// isGeneric reports whether the definition is a connector rather than one
// site: families, Torznab/Newznab endpoints, feeds and the shipped example.
// A site definition that uses a family connector (CorsaroBlu runs on Xbtit) is
// still one site: only the connector definitions themselves are generic.
func isGeneric(entry Entry) bool {
	id := entry.ID
	return id == "rss" || strings.HasPrefix(id, "example") || strings.HasSuffix(id, "-generic")
}

// knownFamilies are the family connectors a site can declare in the manifest.
var knownFamilies = map[string]bool{
	"unit3d":        true,
	"gazelle":       true,
	"nexusphp":      true,
	"torrenttrader": true,
	"xbtit":         true,
}

// notesFor collects the short facts worth a cell: an unofficial mirror and the
// tracker software family. The manifest note is a writing aid for maintainers
// and stays out of a user-facing list.
func notesFor(entry Entry) string {
	var notes []string
	if entry.Definition.Meta.Mirror != "" {
		notes = append(notes, "unofficial mirror")
	}
	if knownFamilies[entry.Family] {
		notes = append(notes, entry.Family+" family")
	}
	return strings.Join(notes, "; ")
}

func siteURL(def *definition.Definition) string {
	for _, raw := range def.Meta.URLs {
		if strings.TrimSpace(raw) != "" {
			return strings.TrimSpace(raw)
		}
	}
	return ""
}

// RenderSupportedDocs builds docs/SUPPORTED.md from the parsed catalog. The
// output is deterministic: entries are sorted by protocol and name.
func RenderSupportedDocs(entries []Entry) []byte {
	var b strings.Builder

	byProtocol := map[string][]Entry{}
	generic := make([]Entry, 0, 8)
	var drafts, degraded int
	for _, entry := range entries {
		if entry.Definition == nil {
			continue
		}
		// The shipped example definition documents the format; it is not a
		// source a user can add.
		if strings.HasPrefix(entry.ID, "example") {
			continue
		}
		if isGeneric(entry) {
			generic = append(generic, entry)
			continue
		}
		switch entry.State {
		case "bozza":
			drafts++
		case "degradata":
			degraded++
		}
		protocol := entry.Definition.Meta.Protocol
		byProtocol[protocol] = append(byProtocol[protocol], entry)
	}
	total := 0
	for _, list := range byProtocol {
		total += len(list)
		sort.SliceStable(list, func(i, j int) bool {
			return strings.ToLower(list[i].Definition.Meta.Name) < strings.ToLower(list[j].Definition.Meta.Name)
		})
	}
	sort.SliceStable(generic, func(i, j int) bool {
		return strings.ToLower(generic[i].Definition.Meta.Name) < strings.ToLower(generic[j].Definition.Meta.Name)
	})

	b.WriteString("# Supported sources\n\n")
	b.WriteString("<!-- Generated from the catalog by go generate ./internal/catalog. Do not edit by hand. -->\n\n")
	b.WriteString("Every source listed here is shipped with indexarr: no proxy, no external\n")
	b.WriteString("definition file to keep in sync. The catalog has no index of its own — it\n")
	b.WriteString("describes how to read the sites you configure.\n\n")
	fmt.Fprintf(&b, "**%d site definitions** plus %d generic connectors: %d verified, %d degraded and %d draft. A draft is usable but not production-ready.\n\n",
		total, len(generic), total-drafts-degraded, degraded, drafts)
	b.WriteString("When a definition is added, removed or changes state, this file is\n")
	b.WriteString("regenerated in the same commit (`make supported-list`); the catalog test fails\n")
	b.WriteString("while it is stale, so the list cannot describe a catalog that no longer exists.\n\n")

	b.WriteString("## Reading the tables\n\n")
	b.WriteString("| Column | Meaning |\n")
	b.WriteString("| --- | --- |\n")
	b.WriteString("| **State** | `verified` = written and checked against the real pages; `degraded` = works with a known limit; `draft` = usable but not production-ready |\n")
	b.WriteString("| **Access** | `None` = public, `Account` = username and password, `Cookie` = pasted session cookie, `API key` = key or passkey, `Uploader` = the site's uploader name, `Endpoint` = the address of a Torznab/Newznab endpoint or a feed |\n")
	b.WriteString("| **Language** | Primary language of the site |\n\n")

	protocolTitles := map[string]string{
		"torrent": "Torrent",
		"usenet":  "Usenet",
		"ed2k":    "eD2k",
	}
	order := []string{"torrent", "usenet", "ed2k"}
	for _, protocol := range order {
		list := byProtocol[protocol]
		if len(list) == 0 {
			continue
		}
		fmt.Fprintf(&b, "## %s (%d)\n\n", protocolTitles[protocol], len(list))
		b.WriteString("| Site | Access | Language | State | Notes |\n")
		b.WriteString("| --- | --- | --- | --- | --- |\n")
		for _, entry := range list {
			def := entry.Definition
			name := def.Meta.Name
			if url := siteURL(def); url != "" {
				name = fmt.Sprintf("[%s](%s)", name, url)
			}
			notes := notesFor(entry)
			if notes == "" {
				notes = "—"
			}
			fmt.Fprintf(&b, "| %s | %s | %s | %s | %s |\n",
				name, accessLabel(def), shortLanguage(def.Meta.Language), stateLabel(entry.State), notes)
		}
		b.WriteString("\n")
	}

	b.WriteString("## Family connectors and generic endpoints\n\n")
	b.WriteString("These are not single sites: one definition covers every site that speaks the\n")
	b.WriteString("same protocol or runs the same tracker software.\n\n")
	b.WriteString("| Connector | Covers |\n")
	b.WriteString("| --- | --- |\n")
	for _, entry := range generic {
		def := entry.Definition
		description := genericConnectors[def.Connector]
		if description == "" {
			description = strings.TrimSpace(def.Meta.Description)
		}
		fmt.Fprintf(&b, "| %s | %s |\n", def.Meta.Name, description)
	}
	b.WriteString("\n")
	b.WriteString("eD2k sources are configured with `protocol: ed2k`: indexarr stores the\n")
	b.WriteString("`ed2k://` links and sends them to aMule.\n\n")
	b.WriteString("Usenet has no per-site definition yet: any Newznab indexer works through\n")
	b.WriteString("the generic connector, and `.nzb` files are sent to SABnzbd or NZBGet.\n\n")
	b.WriteString("## Not in the catalog\n\n")
	b.WriteString("A site without a native definition stays out of the user list: there is no\n")
	b.WriteString("Cardigann execution and no proxy towards Jackett or Prowlarr. An endpoint that\n")
	b.WriteString("already speaks Torznab or Newznab is always usable through the generic\n")
	b.WriteString("connector, with no definition of its own.\n")

	return []byte(b.String())
}
