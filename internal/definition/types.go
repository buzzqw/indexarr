// Package definition implements the native indexarr definition format (v1).
//
// A definition is a YAML document describing one indexer: metadata, user
// settings, category mapping, capabilities, authentication, search and
// offline tests. The package parses and validates documents, fingerprints
// them for storage, upgrades older schema versions and publishes the JSON
// Schema used by editors.
package definition

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Definition is one indexer definition.
type Definition struct {
	Schema       int          `yaml:"schema" json:"schema"`
	Connector    string       `yaml:"connector,omitempty" json:"connector,omitempty"`
	Meta         Meta         `yaml:"meta" json:"meta"`
	Settings     []Setting    `yaml:"settings,omitempty" json:"settings,omitempty"`
	Categories   []Category   `yaml:"categories,omitempty" json:"categories,omitempty"`
	Capabilities Capabilities `yaml:"capabilities,omitempty" json:"capabilities,omitempty"`
	Auth         *Auth        `yaml:"auth,omitempty" json:"auth,omitempty"`
	Search       Search       `yaml:"search,omitempty" json:"search,omitempty"`
	Download     *Download    `yaml:"download,omitempty" json:"download,omitempty"`
	Health       *Health      `yaml:"health,omitempty" json:"health,omitempty"`
	Tests        []Test       `yaml:"tests,omitempty" json:"tests,omitempty"`
}

// Meta identifies the indexer and its mirrors.
type Meta struct {
	ID          string   `yaml:"id" json:"id"`
	Name        string   `yaml:"name" json:"name"`
	Language    string   `yaml:"language,omitempty" json:"language,omitempty"`
	Type        string   `yaml:"type" json:"type"`
	Protocol    string   `yaml:"protocol" json:"protocol"`
	Author      string   `yaml:"author,omitempty" json:"author,omitempty"`
	Description string   `yaml:"description,omitempty" json:"description,omitempty"`
	URLs        []string `yaml:"urls" json:"urls"`
	Updated     string   `yaml:"updated,omitempty" json:"updated,omitempty"`
	// Interval is an optional suggested refresh interval (a Go duration such
	// as "15m"). An empty value means the definition has no recommendation and
	// the global default interval applies.
	Interval Duration `yaml:"interval,omitempty" json:"interval,omitempty"`
	// DerivedFrom marks a draft produced by a conversion tool. Derived drafts
	// are writing aids: they stay local and the built-in catalog rejects them.
	DerivedFrom string `yaml:"derived_from,omitempty" json:"derived_from,omitempty"`
}

// cardigannImportAuthor is the default author written by the Cardigann
// converter. A definition carrying it is a derived draft even when the
// derived_from marker is missing.
const cardigannImportAuthor = "cardigann-import"

// IsDerived reports whether the definition is a draft produced by a
// conversion tool such as the Cardigann converter. Derived drafts are writing
// aids: they stay local, are never imported by the API and never enter the
// built-in catalog.
func (d *Definition) IsDerived() bool {
	if d == nil {
		return false
	}
	if strings.TrimSpace(d.Meta.DerivedFrom) != "" {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(d.Meta.Author), cardigannImportAuthor)
}

// Setting is a user-configurable value rendered as a form field.
type Setting struct {
	Name     string   `yaml:"name" json:"name"`
	Type     string   `yaml:"type,omitempty" json:"type,omitempty"`
	Label    string   `yaml:"label,omitempty" json:"label,omitempty"`
	Help     string   `yaml:"help,omitempty" json:"help,omitempty"`
	Default  string   `yaml:"default,omitempty" json:"default,omitempty"`
	Required bool     `yaml:"required,omitempty" json:"required,omitempty"`
	Options  []string `yaml:"options,omitempty" json:"options,omitempty"`
}

// Category maps a site category id to a standard Torznab/Newznab category.
type Category struct {
	Site     string `yaml:"site" json:"site"`
	Standard int    `yaml:"standard" json:"standard"`
	Desc     string `yaml:"desc,omitempty" json:"desc,omitempty"`
}

// Capabilities lists the query parameters supported per search mode.
type Capabilities struct {
	Search      []string `yaml:"search,omitempty" json:"search,omitempty"`
	TVSearch    []string `yaml:"tv-search,omitempty" json:"tv-search,omitempty"`
	MovieSearch []string `yaml:"movie-search,omitempty" json:"movie-search,omitempty"`
	MusicSearch []string `yaml:"music-search,omitempty" json:"music-search,omitempty"`
	BookSearch  []string `yaml:"book-search,omitempty" json:"book-search,omitempty"`
}

// Auth describes how a definition authenticates against the site.
type Auth struct {
	Strategy     string      `yaml:"strategy,omitempty" json:"strategy,omitempty"`
	Steps        []AuthStep  `yaml:"steps,omitempty" json:"steps,omitempty"`
	Success      *Condition  `yaml:"success,omitempty" json:"success,omitempty"`
	Failure      []Condition `yaml:"failure,omitempty" json:"failure,omitempty"`
	RefreshAfter Duration    `yaml:"refresh_after,omitempty" json:"refresh_after,omitempty"`
}

// AuthStep is one explicit step of a login flow.
type AuthStep struct {
	Get     string               `yaml:"get,omitempty" json:"get,omitempty"`
	Post    string               `yaml:"post,omitempty" json:"post,omitempty"`
	Form    map[string]string    `yaml:"form,omitempty" json:"form,omitempty"`
	Headers map[string]string    `yaml:"headers,omitempty" json:"headers,omitempty"`
	Extract map[string]Extractor `yaml:"extract,omitempty" json:"extract,omitempty"`
}

// Extractor pulls a value out of an authentication response.
type Extractor struct {
	Selector string `yaml:"selector,omitempty" json:"selector,omitempty"`
	Attr     string `yaml:"attr,omitempty" json:"attr,omitempty"`
	JSON     string `yaml:"json,omitempty" json:"json,omitempty"`
	Regex    string `yaml:"regex,omitempty" json:"regex,omitempty"`
}

// Condition recognises a successful or failed authentication page.
type Condition struct {
	Selector string `yaml:"selector,omitempty" json:"selector,omitempty"`
	Message  string `yaml:"message,omitempty" json:"message,omitempty"`
	Status   int    `yaml:"status,omitempty" json:"status,omitempty"`
}

// Search describes how to issue and interpret a search.
type Search struct {
	Request    Request    `yaml:"request,omitempty" json:"request,omitempty"`
	Pagination Pagination `yaml:"pagination,omitempty" json:"pagination,omitempty"`
	Response   Response   `yaml:"response,omitempty" json:"response,omitempty"`
}

// Request is the HTTP request template for a search.
type Request struct {
	Get     string            `yaml:"get,omitempty" json:"get,omitempty"`
	Post    string            `yaml:"post,omitempty" json:"post,omitempty"`
	Params  map[string]string `yaml:"params,omitempty" json:"params,omitempty"`
	Form    map[string]string `yaml:"form,omitempty" json:"form,omitempty"`
	Headers map[string]string `yaml:"headers,omitempty" json:"headers,omitempty"`
}

// Pagination controls how additional result pages are requested.
type Pagination struct {
	Type     string `yaml:"type,omitempty" json:"type,omitempty"`
	Start    int    `yaml:"start,omitempty" json:"start,omitempty"`
	Step     int    `yaml:"step,omitempty" json:"step,omitempty"`
	MaxPages int    `yaml:"max_pages,omitempty" json:"max_pages,omitempty"`
	Param    string `yaml:"param,omitempty" json:"param,omitempty"`
	StopWhen string `yaml:"stop_when,omitempty" json:"stop_when,omitempty"`
}

// Response describes how to locate rows and fields in a response body.
type Response struct {
	Type     string           `yaml:"type,omitempty" json:"type,omitempty"`
	Rows     RowSelector      `yaml:"rows,omitempty" json:"rows,omitempty"`
	Fields   map[string]Field `yaml:"fields,omitempty" json:"fields,omitempty"`
	Required []string         `yaml:"required,omitempty" json:"required,omitempty"`
}

// Download describes how to obtain a magnet link or torrent URL from a
// details page. It is optional: definitions that already expose magnets or
// direct download links in their listing keep working unchanged.
type Download struct {
	// ResolveOn selects when details pages are opened: "search" resolves them
	// while searching so the infohash is available; "grab" (the default when
	// the field is empty) leaves the details page untouched until Download is
	// called.
	ResolveOn string `yaml:"resolve_on,omitempty" json:"resolve_on,omitempty"`
	// From is the search field that holds the details URL. Empty means
	// "details".
	From string `yaml:"from,omitempty" json:"from,omitempty"`
	// MaxConcurrent caps the details requests in flight. Zero means the
	// default of 2; the engine clamps the effective value to 1..8.
	MaxConcurrent int `yaml:"max_concurrent,omitempty" json:"max_concurrent,omitempty"`
	// Extract lists the targets to try, in order. The first target that
	// yields a value wins.
	Extract []DownloadTarget `yaml:"extract" json:"extract"`
}

// DownloadTarget is one extractor on a details page. Exactly one of Magnet
// and Torrent must be set: Magnet yields a magnet: URI, Torrent yields a URL
// to a .torrent file.
type DownloadTarget struct {
	Magnet  *Field `yaml:"magnet,omitempty" json:"magnet,omitempty"`
	Torrent *Field `yaml:"torrent,omitempty" json:"torrent,omitempty"`
}

// Field describes how to extract one value from a row.
type Field struct {
	Selector Selector `yaml:"selector,omitempty" json:"selector,omitempty"`
	Attr     string   `yaml:"attr,omitempty" json:"attr,omitempty"`
	Type     string   `yaml:"type,omitempty" json:"type,omitempty"`
	Path     string   `yaml:"path,omitempty" json:"path,omitempty"`
	Filters  []Filter `yaml:"filters,omitempty" json:"filters,omitempty"`
	Layouts  []string `yaml:"layouts,omitempty" json:"layouts,omitempty"`
}

// Health is the definition's own self-check query.
type Health struct {
	Query   string `yaml:"query,omitempty" json:"query,omitempty"`
	MinRows int    `yaml:"min_rows,omitempty" json:"min_rows,omitempty"`
}

// Test is an offline fixture test for the definition.
type Test struct {
	Name    string      `yaml:"name,omitempty" json:"name,omitempty"`
	Fixture string      `yaml:"fixture" json:"fixture"`
	Expect  Expectation `yaml:"expect,omitempty" json:"expect,omitempty"`
}

// Expectation contains the invariants a fixture test must satisfy.
type Expectation struct {
	MinRows       int      `yaml:"min_rows,omitempty" json:"min_rows,omitempty"`
	Required      []string `yaml:"required,omitempty" json:"required,omitempty"`
	Authenticated bool     `yaml:"authenticated,omitempty" json:"authenticated,omitempty"`
}

// Selector is one selector or an ordered list of fallback selectors.
type Selector []string

// UnmarshalYAML accepts either a scalar selector or a sequence of fallbacks.
func (s *Selector) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		if value.Tag == "!!null" {
			*s = nil
			return nil
		}
		var one string
		if err := value.Decode(&one); err != nil {
			return fmt.Errorf("selector: %w (line %d)", err, value.Line)
		}
		*s = Selector{one}
		return nil
	case yaml.SequenceNode:
		var many []string
		if err := value.Decode(&many); err != nil {
			return fmt.Errorf("selector: %w (line %d)", err, value.Line)
		}
		*s = Selector(many)
		return nil
	default:
		return fmt.Errorf("selector: must be a string or a list of strings (line %d)", value.Line)
	}
}

// MarshalYAML writes a single selector as a scalar and fallbacks as a list.
func (s Selector) MarshalYAML() (any, error) {
	switch len(s) {
	case 0:
		return nil, nil
	case 1:
		return s[0], nil
	default:
		return []string(s), nil
	}
}

// UnmarshalJSON accepts either a scalar selector or a list of fallbacks.
func (s *Selector) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		*s = nil
		return nil
	}
	var one string
	if err := json.Unmarshal(trimmed, &one); err == nil {
		*s = Selector{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(trimmed, &many); err != nil {
		return fmt.Errorf("selector: must be a string or a list of strings")
	}
	*s = Selector(many)
	return nil
}

// MarshalJSON writes a single selector as a scalar and fallbacks as a list.
func (s Selector) MarshalJSON() ([]byte, error) {
	switch len(s) {
	case 0:
		return []byte("[]"), nil
	case 1:
		return json.Marshal(s[0])
	default:
		return json.Marshal([]string(s))
	}
}

// Filter is a named post-processing step with an optional argument.
type Filter struct {
	Name string `yaml:"name" json:"name"`
	Arg  string `yaml:"arg,omitempty" json:"arg,omitempty"`
}

// UnmarshalYAML accepts "trim" or a single-key mapping such as
// {query_param: cat}.
func (f *Filter) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		if value.Tag == "!!null" {
			*f = Filter{}
			return nil
		}
		var name string
		if err := value.Decode(&name); err != nil {
			return fmt.Errorf("filter: %w (line %d)", err, value.Line)
		}
		*f = Filter{Name: name}
		return nil
	case yaml.MappingNode:
		if len(value.Content) != 2 {
			return fmt.Errorf("filter: mapping must have exactly one key (line %d)", value.Line)
		}
		key, val := value.Content[0], value.Content[1]
		if key.Kind != yaml.ScalarNode {
			return fmt.Errorf("filter: key must be a string (line %d)", key.Line)
		}
		if val.Kind != yaml.ScalarNode {
			return fmt.Errorf("filter %q: argument must be a scalar (line %d)", key.Value, val.Line)
		}
		arg := val.Value
		if val.Tag == "!!null" {
			arg = ""
		}
		*f = Filter{Name: key.Value, Arg: arg}
		return nil
	default:
		return fmt.Errorf("filter: must be a string or a single-key mapping (line %d)", value.Line)
	}
}

// MarshalYAML writes filters without an argument as a scalar.
func (f Filter) MarshalYAML() (any, error) {
	if f.Arg == "" {
		return f.Name, nil
	}
	return map[string]string{f.Name: f.Arg}, nil
}

// UnmarshalJSON accepts either a scalar filter or a single-key mapping.
func (f *Filter) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		*f = Filter{}
		return nil
	}
	var name string
	if err := json.Unmarshal(trimmed, &name); err == nil {
		*f = Filter{Name: name}
		return nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &obj); err != nil {
		return fmt.Errorf("filter: must be a string or a single-key mapping")
	}
	if len(obj) != 1 {
		return fmt.Errorf("filter: mapping must have exactly one key")
	}
	for name, raw := range obj {
		var arg string
		if err := json.Unmarshal(raw, &arg); err != nil {
			return fmt.Errorf("filter %q: argument must be a string", name)
		}
		*f = Filter{Name: name, Arg: arg}
	}
	return nil
}

// MarshalJSON writes filters without an argument as a scalar.
func (f Filter) MarshalJSON() ([]byte, error) {
	if f.Arg == "" {
		return json.Marshal(f.Name)
	}
	return json.Marshal(map[string]string{f.Name: f.Arg})
}

// RowSelector locates the result rows of a response. HTML and XML definitions
// use Selector, JSON definitions use Path, and both may be present.
type RowSelector struct {
	Selector Selector `yaml:"selector,omitempty" json:"selector,omitempty"`
	Path     string   `yaml:"path,omitempty" json:"path,omitempty"`
}

// UnmarshalYAML accepts a plain selector shorthand or a mapping with
// "selector" and/or "path".
func (r *RowSelector) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode, yaml.SequenceNode:
		var s Selector
		if err := s.UnmarshalYAML(value); err != nil {
			return err
		}
		*r = RowSelector{Selector: s}
		return nil
	case yaml.MappingNode:
		var out RowSelector
		for i := 0; i < len(value.Content); i += 2 {
			key, val := value.Content[i], value.Content[i+1]
			switch key.Value {
			case "selector":
				if err := out.Selector.UnmarshalYAML(val); err != nil {
					return fmt.Errorf("rows.selector: %w", err)
				}
			case "path":
				if val.Kind != yaml.ScalarNode {
					return fmt.Errorf("rows.path: must be a string (line %d)", val.Line)
				}
				if err := val.Decode(&out.Path); err != nil {
					return fmt.Errorf("rows.path: %w (line %d)", err, val.Line)
				}
			default:
				return fmt.Errorf("rows: unknown key %q (line %d)", key.Value, key.Line)
			}
		}
		*r = out
		return nil
	default:
		return fmt.Errorf("rows: must be a selector, a list of selectors, or a mapping with selector/path (line %d)", value.Line)
	}
}

// MarshalYAML writes the compact selector shorthand when no path is used.
func (r RowSelector) MarshalYAML() (any, error) {
	if r.Path != "" {
		out := map[string]any{"path": r.Path}
		if len(r.Selector) > 0 {
			out["selector"] = r.Selector
		}
		return out, nil
	}
	return r.Selector.MarshalYAML()
}

// UnmarshalJSON accepts a plain selector shorthand or a mapping with
// "selector" and/or "path".
func (r *RowSelector) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		*r = RowSelector{}
		return nil
	}
	if trimmed[0] == '{' {
		var aux struct {
			Selector Selector `json:"selector"`
			Path     string   `json:"path"`
		}
		dec := json.NewDecoder(bytes.NewReader(trimmed))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&aux); err != nil {
			return fmt.Errorf("rows: %w", err)
		}
		*r = RowSelector{Selector: aux.Selector, Path: aux.Path}
		return nil
	}
	var s Selector
	if err := s.UnmarshalJSON(trimmed); err != nil {
		return err
	}
	*r = RowSelector{Selector: s}
	return nil
}

// MarshalJSON writes the compact selector shorthand when no path is used.
func (r RowSelector) MarshalJSON() ([]byte, error) {
	if r.Path != "" {
		out := map[string]any{"path": r.Path}
		if len(r.Selector) > 0 {
			out["selector"] = r.Selector
		}
		return json.Marshal(out)
	}
	if len(r.Selector) == 0 {
		return []byte("null"), nil
	}
	return json.Marshal(r.Selector)
}

// Duration is a time.Duration that decodes from Go duration strings in YAML
// and JSON, for example "6h" or "1h30m". An empty string means zero.
type Duration time.Duration

// Duration returns the value as a standard time.Duration.
func (d Duration) Duration() time.Duration {
	return time.Duration(d)
}

// UnmarshalYAML parses a Go duration string.
func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind != yaml.ScalarNode {
		return fmt.Errorf("duration: must be a string (line %d)", value.Line)
	}
	if value.Tag == "!!null" {
		*d = 0
		return nil
	}
	var raw string
	if err := value.Decode(&raw); err != nil {
		return fmt.Errorf("duration: %w (line %d)", err, value.Line)
	}
	if raw == "" {
		*d = 0
		return nil
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil {
		return fmt.Errorf("duration %q: %w (line %d)", raw, err, value.Line)
	}
	*d = Duration(parsed)
	return nil
}

// MarshalYAML writes a Go duration string.
func (d Duration) MarshalYAML() (any, error) {
	return time.Duration(d).String(), nil
}

// UnmarshalJSON parses a Go duration string.
func (d *Duration) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		*d = 0
		return nil
	}
	var raw string
	if err := json.Unmarshal(trimmed, &raw); err != nil {
		return fmt.Errorf("duration: must be a Go duration string")
	}
	if raw == "" {
		*d = 0
		return nil
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil {
		return fmt.Errorf("duration %q: %w", raw, err)
	}
	*d = Duration(parsed)
	return nil
}

// MarshalJSON writes a Go duration string.
func (d Duration) MarshalJSON() ([]byte, error) {
	if time.Duration(d) == 0 {
		return json.Marshal("")
	}
	return json.Marshal(time.Duration(d).String())
}
