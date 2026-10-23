package cardigann

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/buzzqw/indexarr/internal/definition"
	"gopkg.in/yaml.v3"
)

// Torznab/Newznab standard categories. Cardigann category mappings usually
// name a category ("TV/HD") instead of giving its numeric id; this table turns
// the common names into ids. Unknown names are reported, never guessed.
var standardCategories = map[string]int{
	"console":     1000,
	"console/nds": 1010, "console/nintendo ds": 1010,
	"console/psp":      1020,
	"console/wii":      1030,
	"console/xbox":     1040,
	"console/xbox 360": 1050,
	"console/wiiware":  1060,
	"console/xbox one": 1070,
	"console/ps4":      1080, "console/playstation 4": 1080,
	"console/ps3": 1090, "console/playstation 3": 1090,
	"console/other": 1100,
	"console/3ds":   1110, "console/nintendo 3ds": 1110,
	"console/ps vita": 1120, "console/vita": 1120,
	"console/wiiu": 1130, "console/wii u": 1130,

	"movies": 2000, "movie": 2000, "films": 2000,
	"movies/foreign": 2010,
	"movies/other":   2020,
	"movies/sd":      2030,
	"movies/hd":      2040,
	"movies/uhd":     2045, "movies/4k": 2045,
	"movies/bluray": 2050, "movies/blu-ray": 2050,
	"movies/3d":     2060,
	"movies/dvd":    2070,
	"movies/web-dl": 2080, "movies/webdl": 2080,
	"movies/web": 2080,

	"audio": 3000, "music": 3000,
	"audio/mp3": 3010, "music/mp3": 3010,
	"audio/flac": 3020, "music/flac": 3020,
	"audio/audiobook": 3030, "audio/audiobooks": 3030, "music/audiobook": 3030,
	"audio/lossless": 3040, "music/lossless": 3040,
	"audio/other": 3050, "music/other": 3050,
	"audio/foreign": 3060, "music/foreign": 3060,

	"pc":              4000,
	"pc/0day":         4010,
	"pc/iso":          4020,
	"pc/mac":          4030,
	"pc/mobile-other": 4040,
	"pc/games":        4050, "games": 4050,
	"pc/mobile-ios":     4060,
	"pc/mobile-android": 4070,

	"tv":        5000,
	"tv/web-dl": 5010, "tv/webdl": 5010,
	"tv/foreign": 5020,
	"tv/sd":      5030,
	"tv/hd":      5040,
	"tv/uhd":     5045, "tv/4k": 5045,
	"tv/other": 5050,
	"tv/sport": 5060, "tv/sports": 5060,
	"tv/anime":       5070,
	"tv/documentary": 5080, "tv/documentaries": 5080,

	"xxx": 6000, "adult": 6000,
	"xxx/dvd":      6010,
	"xxx/wmv":      6020,
	"xxx/xvid":     6030,
	"xxx/x264":     6040,
	"xxx/uhd":      6050,
	"xxx/pack":     6060,
	"xxx/imageset": 6070,
	"xxx/other":    6080,

	"books": 7000, "book": 7000, "ebooks": 7000,
	"books/ebook":     7010,
	"books/comics":    7020,
	"books/technical": 7030,
	"books/other":     7040,
	"books/foreign":   7050,

	"other":        8000,
	"other/misc":   8010,
	"other/hashed": 8020,
}

// categoryStandardID resolves a Cardigann category name to a Torznab id.
func categoryStandardID(name string) (int, bool) {
	normalized := strings.ToLower(strings.TrimSpace(name))
	normalized = strings.NewReplacer("_", " ", "  ", " ").Replace(normalized)
	if id, ok := standardCategories[normalized]; ok {
		return id, true
	}
	// "TV HD" and "TV-HD" are common spellings of "TV/HD".
	slashed := strings.ReplaceAll(normalized, " - ", " ")
	slashed = strings.ReplaceAll(slashed, " ", "/")
	if id, ok := standardCategories[slashed]; ok {
		return id, true
	}
	return 0, false
}

type categoryKey struct {
	site     string
	standard int
}

func (c *converter) convertCategories() []definition.Category {
	var categories []definition.Category
	seen := make(map[categoryKey]bool)

	add := func(site string, standard int, desc string, source string) {
		site = strings.TrimSpace(site)
		desc = strings.TrimSpace(desc)
		if standard < 1000 || standard > 8999 {
			c.addWarning("category %q has non-standard id %d; dropped", source, standard)
			return
		}
		if site == "" {
			site = strconv.Itoa(standard)
		}
		if desc == "" {
			desc = source
		}
		key := categoryKey{site: site, standard: standard}
		if seen[key] {
			return
		}
		seen[key] = true
		categories = append(categories, definition.Category{
			Site:     site,
			Standard: standard,
			Desc:     desc,
		})
	}

	for _, mapping := range c.raw.CategoryMappings {
		c.addCategoryMapping(mapping, add)
	}
	for _, mapping := range c.raw.Caps.CategoryMappings {
		c.addCategoryMapping(mapping, add)
	}
	for _, mapping := range c.raw.Caps.CategoryMappingsAlt {
		c.addCategoryMapping(mapping, add)
	}

	if nodeSet(c.raw.Categories) {
		c.addCategoriesNode(&c.raw.Categories, add)
	}
	if nodeSet(c.raw.Caps.Category) {
		c.addCategoriesNode(&c.raw.Caps.Category, add)
	}
	if nodeSet(c.raw.Caps.Categories) {
		c.addCategoriesNode(&c.raw.Caps.Categories, add)
	}

	if len(categories) == 0 {
		c.addWarning("no category mapping found; native categories are empty")
		return nil
	}

	sort.SliceStable(categories, func(i, j int) bool {
		if categories[i].Site != categories[j].Site {
			return categories[i].Site < categories[j].Site
		}
		return categories[i].Standard < categories[j].Standard
	})
	return categories
}

func (c *converter) addCategoryMapping(mapping rawCategoryMapping, add func(string, int, string, string)) {
	site := strings.TrimSpace(string(mapping.ID))
	if site == "" {
		site = strings.TrimSpace(string(mapping.CatID))
	}
	if site == "" {
		site = strings.TrimSpace(string(mapping.Site))
	}

	standardText := strings.TrimSpace(string(mapping.Standard))
	if standardText == "" && len(mapping.Cat) > 0 {
		standardText = mapping.Cat[0]
	}
	if standardText == "" && len(mapping.Category) > 0 {
		standardText = mapping.Category[0]
	}
	if standardText == "" {
		c.addWarning("category mapping for site %q has no category; dropped", site)
		return
	}
	standard, ok := c.resolveStandard(standardText)
	if !ok {
		return
	}
	if site == "" {
		c.addUnsupported(fmt.Sprintf("category mapping %q without a site id", standardText))
		c.addWarning("category mapping %q has no site id; dropped", standardText)
		return
	}
	desc := strings.TrimSpace(mapping.Desc)
	if desc == "" {
		desc = standardText
	}
	add(site, standard, desc, standardText)
}

func (c *converter) addCategoriesNode(node *yaml.Node, add func(string, int, string, string)) {
	if node == nil {
		return
	}
	if node.Kind == yaml.AliasNode && node.Alias != nil {
		node = node.Alias
	}
	switch node.Kind {
	case yaml.MappingNode:
		keys := make([]string, 0, len(node.Content)/2)
		for i := 0; i+1 < len(node.Content); i += 2 {
			keys = append(keys, scalarText(node.Content[i]))
		}
		sort.Strings(keys)
		for _, key := range keys {
			for i := 0; i+1 < len(node.Content); i += 2 {
				if scalarText(node.Content[i]) != key {
					continue
				}
				c.addCategoryValue(key, node.Content[i+1], key, add)
				break
			}
		}
	case yaml.SequenceNode:
		for _, item := range node.Content {
			if item.Kind == yaml.MappingNode {
				c.addCategoryMapItem(item, add)
				continue
			}
			c.addCategoryValue("", item, "", add)
		}
	case yaml.ScalarNode:
		c.addCategoryValue("", node, "", add)
	}
}

func (c *converter) addCategoryMapItem(item *yaml.Node, add func(string, int, string, string)) {
	values := make(map[string]string, len(item.Content)/2)
	for i := 0; i+1 < len(item.Content); i += 2 {
		values[strings.ToLower(scalarText(item.Content[i]))] = scalarText(item.Content[i+1])
	}
	site := firstNonEmpty(values["site"], values["cat-id"], values["catid"], values["id"])
	standardText := firstNonEmpty(values["standard"], values["cat"], values["category"])
	desc := firstNonEmpty(values["desc"], values["description"], values["name"])
	if standardText == "" {
		c.addWarning("category entry for site %q has no standard category; dropped", site)
		return
	}
	standard, ok := c.resolveStandard(standardText)
	if !ok {
		return
	}
	if site == "" {
		site = desc
	}
	add(site, standard, desc, standardText)
}

func (c *converter) addCategoryValue(site string, value *yaml.Node, desc string, add func(string, int, string, string)) {
	if value == nil {
		return
	}
	if value.Kind == yaml.AliasNode && value.Alias != nil {
		value = value.Alias
	}
	switch value.Kind {
	case yaml.ScalarNode:
		text := scalarText(value)
		standard, ok := c.resolveStandard(text)
		if !ok {
			return
		}
		if site == "" {
			site = text
		}
		if desc == "" {
			desc = text
		}
		add(site, standard, desc, text)
	case yaml.SequenceNode:
		for _, item := range value.Content {
			c.addCategoryValue(site, item, desc, add)
		}
	case yaml.MappingNode:
		c.addCategoryMapItem(value, add)
	}
}

func (c *converter) resolveStandard(raw string) (int, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	if id, err := strconv.Atoi(raw); err == nil {
		if id < 1000 || id > 8999 {
			c.addWarning("category id %d is outside 1000-8999; dropped", id)
			return 0, false
		}
		return id, true
	}
	if id, ok := categoryStandardID(raw); ok {
		return id, true
	}
	c.addUnsupported(fmt.Sprintf("category %q cannot be mapped to a Torznab category", raw))
	c.addWarning("category %q is not a known Torznab category; dropped", raw)
	return 0, false
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
