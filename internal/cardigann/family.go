package cardigann

import (
	"strings"

	"github.com/buzzqw/indexarr/internal/definition"
	"gopkg.in/yaml.v3"
)

// Family connectors can reproduce an API definition without translating its
// HTML/JSON response templates. Detecting one early is both safer (the
// connector implements the protocol) and more useful than an approximated
// native search block.
func (c *converter) detectFamily() (connector, reason string) {
	if c.raw.Search == nil {
		return "", ""
	}

	paths := c.searchPathValues()
	for _, path := range paths {
		lower := strings.ToLower(path)
		if strings.Contains(lower, "api/torrents") && !strings.Contains(lower, "api/v1/torrents") {
			return "unit3d", "UNIT3D torrent API path " + path
		}
	}
	if c.hasFieldSelector("download_link") {
		return "unit3d", "UNIT3D download_link response field"
	}

	lowerPaths := make([]string, 0, len(paths))
	for _, path := range paths {
		lowerPaths = append(lowerPaths, strings.ToLower(path))
	}
	for _, path := range lowerPaths {
		if strings.Contains(path, "torznab") || strings.Contains(path, "newznab") {
			return c.usenetOrTorrentConnector(), "Torznab/Newznab API path " + path
		}
	}
	for _, node := range c.searchInputNodes() {
		hasT := false
		hasKey := false
		for _, key := range sortedKeys(node) {
			switch strings.ToLower(strings.TrimSpace(key)) {
			case "t":
				hasT = true
			case "apikey", "api_key", "passkey", "cat":
				hasKey = true
			}
		}
		if hasT && hasKey && c.responseIsXML() {
			return c.usenetOrTorrentConnector(), "Torznab/Newznab style parameters (t, apikey/cat) with an XML response"
		}
	}
	return "", ""
}

func (c *converter) usenetOrTorrentConnector() string {
	if c.metaProtocol() == "usenet" {
		return "newznab"
	}
	return "torznab"
}

// familyAPIURL builds the endpoint default for Torznab/Newznab connectors,
// which expect the API URL itself rather than the site root.
func familyAPIURL(base, path string) string {
	path = strings.TrimSpace(path)
	if path == "" || strings.Contains(path, "://") || strings.Contains(path, "{{") {
		return base
	}
	return strings.TrimRight(base, "/") + "/" + strings.TrimLeft(path, "/")
}

func (c *converter) firstSearchPath() string {
	paths := c.searchPathValues()
	if len(paths) > 0 {
		return paths[0]
	}
	return ""
}

func (c *converter) searchPathValues() []string {
	search := c.raw.Search
	if search == nil {
		return nil
	}
	var paths []string
	if strings.TrimSpace(search.Path) != "" {
		paths = append(paths, strings.TrimSpace(search.Path))
	}
	for _, path := range search.Paths {
		if strings.TrimSpace(path.Path) != "" {
			paths = append(paths, strings.TrimSpace(path.Path))
		}
	}
	if c.raw.Login != nil && strings.TrimSpace(c.raw.Login.Path) != "" {
		paths = append(paths, strings.TrimSpace(c.raw.Login.Path))
	}
	return paths
}

func (c *converter) hasFieldSelector(selector string) bool {
	search := c.raw.Search
	if search == nil {
		return false
	}
	fields := []rawFields{search.Fields}
	for _, path := range search.Paths {
		fields = append(fields, path.Fields)
	}
	for _, set := range fields {
		for _, field := range set {
			for _, value := range field.Selector {
				if strings.EqualFold(strings.TrimSpace(value), selector) {
					return true
				}
			}
		}
	}
	return false
}

func (c *converter) searchInputNodes() []map[string]string {
	search := c.raw.Search
	if search == nil {
		return nil
	}
	nodes := []map[string]string{search.Inputs}
	for _, path := range search.Paths {
		nodes = append(nodes, path.Inputs)
	}
	return nodes
}

func (c *converter) responseIsXML() bool {
	search := c.raw.Search
	if search == nil {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(search.Type), "xml") {
		return true
	}
	if search.Response != nil && strings.EqualFold(strings.TrimSpace(search.Response.Type), "xml") {
		return true
	}
	for _, path := range search.Paths {
		if path.Response != nil && strings.EqualFold(strings.TrimSpace(path.Response.Type), "xml") {
			return true
		}
		if len(path.Rows.Selector) > 0 && strings.EqualFold(strings.TrimSpace(path.Rows.Selector[0]), "item") {
			return true
		}
	}
	if len(search.Rows.Selector) > 0 && strings.EqualFold(strings.TrimSpace(search.Rows.Selector[0]), "item") {
		return true
	}
	return false
}

// applyFamilySettings makes sure a family definition has the settings its
// connector reads. The URL fallback is filled by the indexer from meta.urls;
// the API key is always a password field and never carries a default.
func (c *converter) applyFamilySettings(connector string, meta definition.Meta) {
	if _, ok := c.lookupSetting("url"); !ok {
		setting := definition.Setting{
			Name:  "url",
			Type:  "text",
			Label: "Base URL",
		}
		if len(meta.URLs) > 0 {
			setting.Default = meta.URLs[0]
			if connector == "torznab" || connector == "newznab" {
				setting.Default = familyAPIURL(meta.URLs[0], c.firstSearchPath())
			}
		}
		c.addSetting("url", setting, false)
	}

	keyName := "apikey"
	if connector == "newznab" || connector == "torznab" {
		if _, ok := c.lookupSetting("apikey"); !ok {
			if _, passkey := c.lookupSetting("passkey"); passkey {
				keyName = "passkey"
				c.addWarning("definition uses passkey; the %s connector reads it as an API key", connector)
			}
		}
	}
	if _, ok := c.lookupSetting(keyName); !ok {
		c.addSetting(keyName, definition.Setting{
			Type:  "password",
			Label: "API key",
		}, true)
	}
	if native, ok := c.lookupSetting(keyName); ok {
		c.markRequired(native)
		for index := range c.settings {
			if c.settings[index].Name == native {
				c.settings[index].Type = "password"
				c.settings[index].Default = ""
			}
		}
	}

	// Remove settings that only exist to describe the Cardigann API in a
	// browser form and are not inputs the family connector reads.
	filtered := make([]definition.Setting, 0, len(c.settings))
	for _, setting := range c.settings {
		label := strings.ToLower(strings.TrimSpace(setting.Label))
		if strings.HasPrefix(strings.ToLower(setting.Name), "info-") ||
			strings.HasPrefix(label, "about ") {
			continue
		}
		filtered = append(filtered, setting)
	}
	c.settings = filtered
}

// familyHeaders returns the Authorization header value when the definition
// uses a Bearer API key. It is used only to explain the mapping in warnings.
func (c *converter) familyHeaders() map[string]string {
	search := c.raw.Search
	if search == nil {
		return nil
	}
	out := make(map[string]string)
	nodes := []yaml.Node{search.Headers}
	for _, path := range search.Paths {
		nodes = append(nodes, path.Headers)
	}
	for _, node := range nodes {
		if !nodeSet(node) {
			continue
		}
		if node.Kind == yaml.AliasNode && node.Alias != nil {
			node = *node.Alias
		}
		if node.Kind != yaml.MappingNode {
			continue
		}
		for index := 0; index+1 < len(node.Content); index += 2 {
			name := strings.TrimSpace(scalarText(node.Content[index]))
			value := headerValueString(node.Content[index+1])
			if name != "" && value != "" {
				out[name] = value
			}
		}
	}
	return out
}

func headerValueString(node *yaml.Node) string {
	value, _ := headerValue(node)
	return value
}
