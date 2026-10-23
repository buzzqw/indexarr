package cardigann

import (
	"strings"

	"github.com/buzzqw/indexarr/internal/definition"
	"gopkg.in/yaml.v3"
)

// The raw types below are a lenient, typed view of the Cardigann fields this
// importer understands. Unknown keys are ignored by yaml.v3; every custom
// unmarshaller accepts the scalar/list/mapping variations that occur in real
// Jackett and Prowlarr definitions. A malformed *document* is an error; an
// odd field inside it is recorded as a warning or unsupported feature later.

type rawDefinition struct {
	ID          string     `yaml:"id"`
	Name        string     `yaml:"name"`
	Description string     `yaml:"description"`
	Language    string     `yaml:"language"`
	Type        string     `yaml:"type"`
	Protocol    string     `yaml:"protocol"`
	Usenet      bool       `yaml:"usenet"`
	Link        string     `yaml:"link"`
	URL         string     `yaml:"url"`
	URLs        stringList `yaml:"urls"`
	Links       stringList `yaml:"links"`
	LegacyLinks stringList `yaml:"legacyLinks"`

	Caps              rawCaps              `yaml:"caps"`
	Categories        yaml.Node            `yaml:"categories"`
	CategoryMappings  []rawCategoryMapping `yaml:"categorymappings"`
	Settings          rawSettings          `yaml:"settings"`
	Login             *rawLogin            `yaml:"login"`
	Search            *rawSearch           `yaml:"search"`
	Download          *rawDownload         `yaml:"download"`
	Response          *rawResponse         `yaml:"response"`
	KeywordsFilters   yaml.Node            `yaml:"keywordsfilters"`
	TitleFilters      yaml.Node            `yaml:"titlefilters"`
	RequestDelay      yaml.Node            `yaml:"requestDelay"`
	RequestDelaySnake yaml.Node            `yaml:"request_delay"`
	Delay             yaml.Node            `yaml:"delay"`
	Go                yaml.Node            `yaml:"go"`
	Custom            yaml.Node            `yaml:"custom"`
	Code              yaml.Node            `yaml:"code"`
	Script            yaml.Node            `yaml:"script"`
}

type rawCaps struct {
	Modes               rawModes             `yaml:"modes"`
	CategoryMappings    []rawCategoryMapping `yaml:"categorymappings"`
	CategoryMappingsAlt []rawCategoryMapping `yaml:"category_mappings"`
	Category            yaml.Node            `yaml:"category"`
	Categories          yaml.Node            `yaml:"categories"`
}

type rawCategoryMapping struct {
	ID       scalarString `yaml:"id"`
	CatID    scalarString `yaml:"cat-id"`
	Cat      stringList   `yaml:"cat"`
	Category stringList   `yaml:"category"`
	Desc     string       `yaml:"desc"`
	Standard scalarString `yaml:"standard"`
	Site     scalarString `yaml:"site"`
}

type rawMode struct {
	Name   string
	Params []string
}

type rawModes []rawMode

type rawSetting struct {
	Name     string       `yaml:"name"`
	Type     string       `yaml:"type"`
	Label    string       `yaml:"label"`
	Help     string       `yaml:"help"`
	Default  scalarString `yaml:"default"`
	Required bool         `yaml:"required"`
	Options  rawOptions   `yaml:"options"`
}

type rawSettings []rawSetting

type rawOptions []string

type rawLogin struct {
	Path         string        `yaml:"path"`
	Method       string        `yaml:"method"`
	Form         string        `yaml:"form"`
	Inputs       stringMap     `yaml:"inputs"`
	Headers      yaml.Node     `yaml:"headers"`
	Error        rawConditions `yaml:"error"`
	Test         *rawCondition `yaml:"test"`
	Cookies      yaml.Node     `yaml:"cookies"`
	Captcha      yaml.Node     `yaml:"captcha"`
	Recaptcha    yaml.Node     `yaml:"recaptcha"`
	TwoFactor    yaml.Node     `yaml:"twofactor"`
	TwoFactorAlt yaml.Node     `yaml:"two_factor"`
	RefreshAfter string        `yaml:"refresh_after"`
	Refresh      string        `yaml:"refresh"`
	Go           yaml.Node     `yaml:"go"`
	Custom       yaml.Node     `yaml:"custom"`
	Code         yaml.Node     `yaml:"code"`
	Script       yaml.Node     `yaml:"script"`
}

type rawCondition struct {
	Selector string     `yaml:"selector"`
	Message  rawMessage `yaml:"message"`
	Path     string     `yaml:"path"`
	Status   int        `yaml:"status"`
}

// rawMessage accepts both the plain message string and the mapping form with
// a text (and optional selector) used by many real definitions.
type rawMessage string

func (m *rawMessage) UnmarshalYAML(value *yaml.Node) error {
	if isNullNode(value) {
		*m = ""
		return nil
	}
	if value.Kind == yaml.AliasNode && value.Alias != nil {
		value = value.Alias
	}
	if value.Kind == yaml.ScalarNode {
		*m = rawMessage(scalarText(value))
		return nil
	}
	if value.Kind == yaml.MappingNode {
		text := ""
		selector := ""
		for i := 0; i+1 < len(value.Content); i += 2 {
			switch strings.ToLower(scalarText(value.Content[i])) {
			case "text":
				text = scalarText(value.Content[i+1])
			case "selector":
				selector = scalarText(value.Content[i+1])
			}
		}
		if text == "" {
			text = selector
		}
		*m = rawMessage(text)
		return nil
	}
	*m = rawMessage(scalarText(value))
	return nil
}

// UnmarshalYAML accepts both a selector string and the fuller condition
// mapping. login.test is sometimes written as a bare selector.
func (c *rawCondition) UnmarshalYAML(value *yaml.Node) error {
	if isNullNode(value) {
		*c = rawCondition{}
		return nil
	}
	if value.Kind == yaml.AliasNode && value.Alias != nil {
		value = value.Alias
	}
	if value.Kind == yaml.ScalarNode {
		c.Selector = scalarText(value)
		return nil
	}
	type plain rawCondition
	var out plain
	if err := value.Decode(&out); err != nil {
		return err
	}
	*c = rawCondition(out)
	return nil
}

type rawConditions []rawCondition

type rawSearch struct {
	Paths           rawPaths     `yaml:"paths"`
	Path            string       `yaml:"path"`
	Method          string       `yaml:"method"`
	Inputs          stringMap    `yaml:"inputs"`
	Headers         yaml.Node    `yaml:"headers"`
	Rows            rawRows      `yaml:"rows"`
	Fields          rawFields    `yaml:"fields"`
	Response        *rawResponse `yaml:"response"`
	Type            string       `yaml:"type"`
	KeywordsFilters yaml.Node    `yaml:"keywordsfilters"`
	TitleFilters    yaml.Node    `yaml:"titlefilters"`
	Prefilter       yaml.Node    `yaml:"prefilter"`
	Filter          yaml.Node    `yaml:"filter"`
	Sort            yaml.Node    `yaml:"sort"`
	Count           yaml.Node    `yaml:"count"`
	DateHeaders     yaml.Node    `yaml:"dateheaders"`
	RequestDelay    yaml.Node    `yaml:"requestDelay"`
	RequestDelayAlt yaml.Node    `yaml:"request_delay"`
	Delay           yaml.Node    `yaml:"delay"`
	Go              yaml.Node    `yaml:"go"`
	Custom          yaml.Node    `yaml:"custom"`
	Code            yaml.Node    `yaml:"code"`
	Script          yaml.Node    `yaml:"script"`
}

type rawSearchPath struct {
	Path           string       `yaml:"path"`
	Method         string       `yaml:"method"`
	Inputs         stringMap    `yaml:"inputs"`
	Headers        yaml.Node    `yaml:"headers"`
	Rows           rawRows      `yaml:"rows"`
	Fields         rawFields    `yaml:"fields"`
	Response       *rawResponse `yaml:"response"`
	FollowRedirect yaml.Node    `yaml:"followredirect"`
}

type rawPaths []rawSearchPath

type rawRows struct {
	Selector definition.Selector
	Path     string
	Set      bool
}

type rawResponse struct {
	Type        string    `yaml:"type"`
	Rows        rawRows   `yaml:"rows"`
	Fields      rawFields `yaml:"fields"`
	Count       yaml.Node `yaml:"count"`
	DateHeaders yaml.Node `yaml:"dateheaders"`
}

type rawField struct {
	Selector  definition.Selector `yaml:"selector"`
	Attribute string              `yaml:"attribute"`
	Attr      string              `yaml:"attr"`
	Filters   rawFilters          `yaml:"filters"`
	Default   scalarString        `yaml:"default"`
	Type      string              `yaml:"type"`
	Path      string              `yaml:"path"`
	Remove    string              `yaml:"remove"`
	Text      string              `yaml:"text"`
}

// UnmarshalYAML also accepts the shorthand "a.title" (or a fallback list)
// where a full field mapping would normally appear.
func (f *rawField) UnmarshalYAML(value *yaml.Node) error {
	if isNullNode(value) {
		*f = rawField{}
		return nil
	}
	if value.Kind == yaml.AliasNode && value.Alias != nil {
		value = value.Alias
	}
	if value.Kind == yaml.ScalarNode || value.Kind == yaml.SequenceNode {
		return f.Selector.UnmarshalYAML(value)
	}
	type plain rawField
	var out plain
	if err := value.Decode(&out); err != nil {
		return err
	}
	*f = rawField(out)
	return nil
}

type rawFields map[string]rawField

type rawFilter struct {
	Name    string
	Args    []string
	Invalid bool
}

type rawFilters []rawFilter

type rawDownload struct {
	Selectors rawDownloadSelectors `yaml:"selectors"`
	Selector  definition.Selector  `yaml:"selector"`
	Attribute string               `yaml:"attribute"`
	Attr      string               `yaml:"attr"`
	Filters   rawFilters           `yaml:"filters"`
	Method    string               `yaml:"method"`
	Path      string               `yaml:"path"`
	Inputs    stringMap            `yaml:"inputs"`
	Go        yaml.Node            `yaml:"go"`
	Custom    yaml.Node            `yaml:"custom"`
	Code      yaml.Node            `yaml:"code"`
	Script    yaml.Node            `yaml:"script"`
}

type rawDownloadSelector struct {
	Selector  definition.Selector `yaml:"selector"`
	Attribute string              `yaml:"attribute"`
	Attr      string              `yaml:"attr"`
	Filters   rawFilters          `yaml:"filters"`
	Method    string              `yaml:"method"`
	Path      string              `yaml:"path"`
}

type rawDownloadSelectors []rawDownloadSelector

// scalarString accepts any YAML scalar and keeps its textual value. Cardigann
// ids are sometimes quoted strings and sometimes integers.
type scalarString string

func (s *scalarString) UnmarshalYAML(value *yaml.Node) error {
	*s = scalarString(scalarText(value))
	return nil
}

// stringList accepts a scalar or a sequence of scalars.
type stringList []string

func (s *stringList) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		if isNullNode(value) {
			*s = nil
			return nil
		}
		*s = []string{scalarText(value)}
	case yaml.SequenceNode:
		out := make([]string, 0, len(value.Content))
		for _, item := range value.Content {
			out = append(out, listText(item)...)
		}
		*s = out
	case yaml.AliasNode:
		if value.Alias == nil {
			return nil
		}
		var inner stringList
		if err := inner.UnmarshalYAML(value.Alias); err != nil {
			return err
		}
		*s = inner
	default:
		// A mapping here is odd but harmless: keep anything that looks like a
		// URL or a value, otherwise ignore it.
		if text := scalarText(value); text != "" {
			*s = []string{text}
		}
	}
	return nil
}

func listText(value *yaml.Node) []string {
	if value == nil {
		return nil
	}
	switch value.Kind {
	case yaml.ScalarNode:
		if isNullNode(value) {
			return nil
		}
		return []string{scalarText(value)}
	case yaml.SequenceNode:
		var out []string
		for _, item := range value.Content {
			out = append(out, listText(item)...)
		}
		return out
	case yaml.MappingNode:
		if text := scalarText(value); text != "" {
			return []string{text}
		}
	case yaml.AliasNode:
		if value.Alias != nil {
			return listText(value.Alias)
		}
	}
	return nil
}

// stringMap accepts the scalar values Cardigann uses for inputs and settings.
type stringMap map[string]string

func (m *stringMap) UnmarshalYAML(value *yaml.Node) error {
	if isNullNode(value) {
		*m = nil
		return nil
	}
	if value.Kind == yaml.AliasNode && value.Alias != nil {
		value = value.Alias
	}
	if value.Kind != yaml.MappingNode {
		return &yaml.TypeError{Errors: []string{"expected a mapping"}}
	}
	out := make(map[string]string, len(value.Content)/2)
	for i := 0; i+1 < len(value.Content); i += 2 {
		key := scalarText(value.Content[i])
		if key == "" {
			continue
		}
		out[key] = nodeText(value.Content[i+1])
	}
	*m = out
	return nil
}

func (s *rawSettings) UnmarshalYAML(value *yaml.Node) error {
	if isNullNode(value) {
		*s = nil
		return nil
	}
	if value.Kind == yaml.AliasNode && value.Alias != nil {
		value = value.Alias
	}
	switch value.Kind {
	case yaml.SequenceNode:
		out := make(rawSettings, 0, len(value.Content))
		for _, item := range value.Content {
			var setting rawSetting
			if item.Kind == yaml.AliasNode && item.Alias != nil {
				item = item.Alias
			}
			if item.Kind == yaml.ScalarNode {
				setting.Name = scalarText(item)
			} else if err := item.Decode(&setting); err != nil {
				return err
			}
			out = append(out, setting)
		}
		*s = out
	case yaml.MappingNode:
		out := make(rawSettings, 0, len(value.Content)/2)
		for i := 0; i+1 < len(value.Content); i += 2 {
			name := scalarText(value.Content[i])
			item := value.Content[i+1]
			var setting rawSetting
			switch item.Kind {
			case yaml.MappingNode:
				if err := item.Decode(&setting); err != nil {
					return err
				}
			case yaml.ScalarNode:
				if !isNullNode(item) {
					setting.Type = scalarText(item)
				}
			}
			if setting.Name == "" {
				setting.Name = name
			}
			out = append(out, setting)
		}
		*s = out
	default:
		return &yaml.TypeError{Errors: []string{"settings: expected a list or a mapping"}}
	}
	return nil
}

func (o *rawOptions) UnmarshalYAML(value *yaml.Node) error {
	if isNullNode(value) {
		*o = nil
		return nil
	}
	if value.Kind == yaml.AliasNode && value.Alias != nil {
		value = value.Alias
	}
	var out []string
	switch value.Kind {
	case yaml.ScalarNode:
		out = append(out, scalarText(value))
	case yaml.SequenceNode:
		for _, item := range value.Content {
			out = append(out, optionText(item))
		}
	case yaml.MappingNode:
		keys := make([]string, 0, len(value.Content)/2)
		for i := 0; i+1 < len(value.Content); i += 2 {
			keys = append(keys, scalarText(value.Content[i]))
		}
		sortStrings(keys)
		out = append(out, keys...)
	default:
		return &yaml.TypeError{Errors: []string{"options: expected a scalar, list or mapping"}}
	}
	*o = dedupeStrings(out)
	return nil
}

func optionText(value *yaml.Node) string {
	if value == nil {
		return ""
	}
	if value.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(value.Content); i += 2 {
			switch strings.ToLower(scalarText(value.Content[i])) {
			case "value", "name", "id", "key":
				return scalarText(value.Content[i+1])
			}
		}
		if len(value.Content) > 1 {
			return scalarText(value.Content[1])
		}
		return ""
	}
	return scalarText(value)
}

func (m *rawModes) UnmarshalYAML(value *yaml.Node) error {
	if isNullNode(value) {
		*m = nil
		return nil
	}
	if value.Kind == yaml.AliasNode && value.Alias != nil {
		value = value.Alias
	}
	switch value.Kind {
	case yaml.MappingNode:
		out := make(rawModes, 0, len(value.Content)/2)
		for i := 0; i+1 < len(value.Content); i += 2 {
			out = append(out, rawMode{
				Name:   scalarText(value.Content[i]),
				Params: parseModeParams(value.Content[i+1]),
			})
		}
		*m = out
	case yaml.SequenceNode:
		out := make(rawModes, 0, len(value.Content))
		for _, item := range value.Content {
			if item.Kind == yaml.ScalarNode {
				out = append(out, rawMode{Name: scalarText(item)})
				continue
			}
			var mode struct {
				Name   string     `yaml:"name"`
				Params stringList `yaml:"params"`
			}
			if err := item.Decode(&mode); err != nil {
				return err
			}
			out = append(out, rawMode{Name: mode.Name, Params: mode.Params})
		}
		*m = out
	default:
		return &yaml.TypeError{Errors: []string{"caps.modes: expected a mapping"}}
	}
	return nil
}

func parseModeParams(value *yaml.Node) []string {
	if value == nil {
		return nil
	}
	if value.Kind == yaml.AliasNode && value.Alias != nil {
		value = value.Alias
	}
	switch value.Kind {
	case yaml.ScalarNode:
		if isNullNode(value) {
			return nil
		}
		return splitCommaList(scalarText(value))
	case yaml.SequenceNode:
		var out []string
		for _, item := range value.Content {
			out = append(out, parseModeParams(item)...)
		}
		return out
	case yaml.MappingNode:
		for i := 0; i+1 < len(value.Content); i += 2 {
			switch strings.ToLower(scalarText(value.Content[i])) {
			case "supportedparams", "supported_params", "params", "parameters":
				return parseModeParams(value.Content[i+1])
			}
		}
	}
	return nil
}

func (c *rawConditions) UnmarshalYAML(value *yaml.Node) error {
	if isNullNode(value) {
		*c = nil
		return nil
	}
	if value.Kind == yaml.AliasNode && value.Alias != nil {
		value = value.Alias
	}
	switch value.Kind {
	case yaml.ScalarNode:
		*c = rawConditions{{Selector: scalarText(value)}}
	case yaml.MappingNode:
		var condition rawCondition
		if err := value.Decode(&condition); err != nil {
			return err
		}
		*c = rawConditions{condition}
	case yaml.SequenceNode:
		out := make(rawConditions, 0, len(value.Content))
		for _, item := range value.Content {
			if item.Kind == yaml.ScalarNode {
				out = append(out, rawCondition{Selector: scalarText(item)})
				continue
			}
			var condition rawCondition
			if err := item.Decode(&condition); err != nil {
				return err
			}
			out = append(out, condition)
		}
		*c = out
	default:
		return &yaml.TypeError{Errors: []string{"condition: expected a scalar, mapping or list"}}
	}
	return nil
}

func (p *rawPaths) UnmarshalYAML(value *yaml.Node) error {
	if isNullNode(value) {
		*p = nil
		return nil
	}
	if value.Kind == yaml.AliasNode && value.Alias != nil {
		value = value.Alias
	}
	switch value.Kind {
	case yaml.ScalarNode:
		*p = rawPaths{{Path: scalarText(value)}}
	case yaml.MappingNode:
		var path rawSearchPath
		if err := value.Decode(&path); err != nil {
			return err
		}
		*p = rawPaths{path}
	case yaml.SequenceNode:
		out := make(rawPaths, 0, len(value.Content))
		for _, item := range value.Content {
			if item.Kind == yaml.ScalarNode {
				out = append(out, rawSearchPath{Path: scalarText(item)})
				continue
			}
			var path rawSearchPath
			if err := item.Decode(&path); err != nil {
				return err
			}
			out = append(out, path)
		}
		*p = out
	default:
		return &yaml.TypeError{Errors: []string{"search.paths: expected a scalar, mapping or list"}}
	}
	return nil
}

func (r *rawRows) UnmarshalYAML(value *yaml.Node) error {
	if isNullNode(value) {
		*r = rawRows{}
		return nil
	}
	if value.Kind == yaml.AliasNode && value.Alias != nil {
		value = value.Alias
	}
	r.Set = true
	switch value.Kind {
	case yaml.ScalarNode, yaml.SequenceNode:
		if err := r.Selector.UnmarshalYAML(value); err != nil {
			return err
		}
	case yaml.MappingNode:
		var aux struct {
			Selector definition.Selector `yaml:"selector"`
			Path     string              `yaml:"path"`
		}
		if err := value.Decode(&aux); err != nil {
			return err
		}
		r.Selector = aux.Selector
		r.Path = aux.Path
	default:
		return &yaml.TypeError{Errors: []string{"rows: expected a scalar, mapping or list"}}
	}
	return nil
}

// MarshalYAML is never used for raw values, but implementing it keeps the type
// usable in tests and avoids surprising yaml.v3 behaviour.
func (r rawRows) MarshalYAML() (any, error) {
	if r.Path != "" {
		return map[string]any{"path": r.Path, "selector": r.Selector}, nil
	}
	return r.Selector.MarshalYAML()
}

func (f *rawFields) UnmarshalYAML(value *yaml.Node) error {
	if isNullNode(value) {
		*f = nil
		return nil
	}
	if value.Kind == yaml.AliasNode && value.Alias != nil {
		value = value.Alias
	}
	if value.Kind != yaml.MappingNode {
		return &yaml.TypeError{Errors: []string{"fields: expected a mapping"}}
	}
	out := make(rawFields, len(value.Content)/2)
	for i := 0; i+1 < len(value.Content); i += 2 {
		name := scalarText(value.Content[i])
		if name == "" {
			continue
		}
		var field rawField
		if err := value.Content[i+1].Decode(&field); err != nil {
			return err
		}
		out[name] = field
	}
	*f = out
	return nil
}

func (f *rawFilters) UnmarshalYAML(value *yaml.Node) error {
	if isNullNode(value) {
		*f = nil
		return nil
	}
	if value.Kind == yaml.AliasNode && value.Alias != nil {
		value = value.Alias
	}
	switch value.Kind {
	case yaml.ScalarNode, yaml.MappingNode:
		var filter rawFilter
		if err := filter.UnmarshalYAML(value); err != nil {
			return err
		}
		*f = rawFilters{filter}
	case yaml.SequenceNode:
		out := make(rawFilters, 0, len(value.Content))
		for _, item := range value.Content {
			var filter rawFilter
			if err := filter.UnmarshalYAML(item); err != nil {
				return err
			}
			out = append(out, filter)
		}
		*f = out
	default:
		return &yaml.TypeError{Errors: []string{"filters: expected a scalar, mapping or list"}}
	}
	return nil
}

func (f *rawFilter) UnmarshalYAML(value *yaml.Node) error {
	if isNullNode(value) {
		return nil
	}
	if value.Kind == yaml.AliasNode && value.Alias != nil {
		value = value.Alias
	}
	switch value.Kind {
	case yaml.ScalarNode:
		f.Name = scalarText(value)
		return nil
	case yaml.MappingNode:
		if len(value.Content) == 0 {
			return nil
		}
		var nameNode, argsNode *yaml.Node
		for i := 0; i+1 < len(value.Content); i += 2 {
			key := strings.ToLower(scalarText(value.Content[i]))
			switch key {
			case "name":
				nameNode = value.Content[i+1]
			case "args", "arg", "arguments":
				argsNode = value.Content[i+1]
			}
		}
		if nameNode != nil {
			f.Name = scalarText(nameNode)
			f.Args = parseFilterArgs(argsNode)
			return nil
		}
		if len(value.Content) == 2 {
			f.Name = scalarText(value.Content[0])
			f.Args = parseFilterArgs(value.Content[1])
			return nil
		}
		f.Invalid = true
		return nil
	default:
		f.Invalid = true
		return nil
	}
}

func parseFilterArgs(value *yaml.Node) []string {
	if value == nil || isNullNode(value) {
		return nil
	}
	if value.Kind == yaml.AliasNode && value.Alias != nil {
		value = value.Alias
	}
	switch value.Kind {
	case yaml.ScalarNode:
		return []string{scalarRaw(value)}
	case yaml.SequenceNode:
		out := make([]string, 0, len(value.Content))
		for _, item := range value.Content {
			out = append(out, parseFilterArgs(item)...)
		}
		return out
	case yaml.MappingNode:
		var out []string
		for i := 0; i+1 < len(value.Content); i += 2 {
			out = append(out, scalarRaw(value.Content[i]))
			out = append(out, scalarRaw(value.Content[i+1]))
		}
		return out
	}
	return nil
}

// scalarRaw preserves leading and trailing whitespace. Filter arguments such
// as append [" "] are meaningful exactly as written.
func scalarRaw(value *yaml.Node) string {
	if value == nil {
		return ""
	}
	if value.Kind == yaml.AliasNode {
		if value.Alias == nil {
			return ""
		}
		return scalarRaw(value.Alias)
	}
	switch value.Kind {
	case yaml.ScalarNode:
		if isNullNode(value) {
			return ""
		}
		return value.Value
	case yaml.SequenceNode:
		parts := make([]string, 0, len(value.Content))
		for _, item := range value.Content {
			parts = append(parts, scalarRaw(item))
		}
		return strings.Join(parts, ",")
	case yaml.MappingNode:
		for i := 0; i+1 < len(value.Content); i += 2 {
			switch strings.ToLower(scalarText(value.Content[i])) {
			case "value", "name", "url", "link", "path", "id", "label", "key":
				return scalarRaw(value.Content[i+1])
			}
		}
	}
	return ""
}

func (s *rawDownloadSelectors) UnmarshalYAML(value *yaml.Node) error {
	if isNullNode(value) {
		*s = nil
		return nil
	}
	if value.Kind == yaml.AliasNode && value.Alias != nil {
		value = value.Alias
	}
	switch value.Kind {
	case yaml.ScalarNode:
		*s = rawDownloadSelectors{{Selector: definition.Selector{scalarText(value)}}}
	case yaml.MappingNode:
		var selector rawDownloadSelector
		if err := value.Decode(&selector); err != nil {
			return err
		}
		*s = rawDownloadSelectors{selector}
	case yaml.SequenceNode:
		out := make(rawDownloadSelectors, 0, len(value.Content))
		for _, item := range value.Content {
			if item.Kind == yaml.ScalarNode {
				out = append(out, rawDownloadSelector{Selector: definition.Selector{scalarText(item)}})
				continue
			}
			var selector rawDownloadSelector
			if err := item.Decode(&selector); err != nil {
				return err
			}
			out = append(out, selector)
		}
		*s = out
	default:
		return &yaml.TypeError{Errors: []string{"download.selectors: expected a scalar, mapping or list"}}
	}
	return nil
}

// scalarText returns the textual value of a scalar, or the most useful value
// inside a small mapping. Sequences are joined with commas.
func scalarText(value *yaml.Node) string {
	if value == nil {
		return ""
	}
	if value.Kind == yaml.AliasNode {
		if value.Alias == nil {
			return ""
		}
		return scalarText(value.Alias)
	}
	switch value.Kind {
	case yaml.ScalarNode:
		if isNullNode(value) {
			return ""
		}
		return strings.TrimSpace(value.Value)
	case yaml.SequenceNode:
		parts := make([]string, 0, len(value.Content))
		for _, item := range value.Content {
			if text := scalarText(item); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, ",")
	case yaml.MappingNode:
		for i := 0; i+1 < len(value.Content); i += 2 {
			switch strings.ToLower(scalarText(value.Content[i])) {
			case "value", "name", "url", "link", "path", "id", "label", "key":
				if text := scalarText(value.Content[i+1]); text != "" {
					return text
				}
			}
		}
	}
	return ""
}

func nodeText(value *yaml.Node) string {
	if value == nil {
		return ""
	}
	if value.Kind == yaml.AliasNode && value.Alias != nil {
		return nodeText(value.Alias)
	}
	if value.Kind == yaml.ScalarNode {
		if isNullNode(value) {
			return ""
		}
		return value.Value
	}
	// Inputs are scalar by convention; joining lists keeps odd definitions
	// usable without inventing a Go value.
	parts := listText(value)
	if len(parts) > 0 {
		return strings.Join(parts, ",")
	}
	return ""
}

func isNullNode(value *yaml.Node) bool {
	return value == nil || (value.Kind == yaml.ScalarNode && value.Tag == "!!null")
}

// nodeSet reports whether an optional YAML field is present and not explicitly
// disabled. It is used to detect captcha/custom-Go/advanced-flow markers.
func nodeSet(value yaml.Node) bool {
	if value.Kind == 0 {
		return false
	}
	if value.Kind == yaml.AliasNode && value.Alias != nil {
		return nodeSet(*value.Alias)
	}
	switch value.Kind {
	case yaml.SequenceNode, yaml.MappingNode:
		return len(value.Content) > 0
	case yaml.ScalarNode:
		if value.Tag == "!!null" {
			return false
		}
		switch strings.ToLower(strings.TrimSpace(value.Value)) {
		case "", "false", "no", "off", "0", "null", "~":
			return false
		}
	}
	return true
}

func splitCommaList(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}
