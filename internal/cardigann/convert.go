package cardigann

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/buzzqw/indexarr/internal/definition"
	"gopkg.in/yaml.v3"
)

const (
	defaultAuthor = "cardigann-import"
	defaultType   = "private"
)

type converter struct {
	raw  rawDefinition
	opts Options

	result Result

	settings     []definition.Setting
	settingNames map[string]string
	usedSettings map[string]bool

	secrets   []string
	extracted map[string]bool
}

// Convert parses one Cardigann definition from body and converts the supported
// subset to a native v1 definition. Unknown Cardigann fields are ignored;
// unsupported features are listed in the result. A request or search template
// that cannot be mapped safely makes Convert fail.
func Convert(body []byte) (Result, error) {
	return convertDefinition(body, Options{})
}

// ConvertFile reads and converts one Cardigann definition file.
func ConvertFile(path string) (Result, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return Result{}, fmt.Errorf("cardigann: read %s: %w", path, err)
	}
	result, err := convertDefinition(body, Options{})
	if err != nil {
		return result, fmt.Errorf("cardigann %s: %w", path, err)
	}
	return result, nil
}

// ConvertDirectory converts every *.yml/*.yaml file under path in sorted
// order. Hidden directories and directories named "testdata" are skipped. One
// invalid file never aborts the walk: successful conversions are returned in
// results and per-file failures in errs. A cancelled context stops the walk
// after the current file.
func ConvertDirectory(ctx context.Context, path string, opts Options) ([]Result, []error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, []error{err}
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, []error{fmt.Errorf("cardigann: stat %s: %w", path, err)}
	}
	if !info.IsDir() {
		return nil, []error{fmt.Errorf("cardigann: %s is not a directory", path)}
	}

	var files []string
	walkErr := filepath.WalkDir(path, func(filePath string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if filePath == path {
				return nil
			}
			name := entry.Name()
			if strings.HasPrefix(name, ".") || strings.EqualFold(name, "testdata") {
				return filepath.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		switch strings.ToLower(filepath.Ext(filePath)) {
		case ".yaml", ".yml":
			files = append(files, filePath)
		}
		return nil
	})
	if walkErr != nil {
		return nil, []error{fmt.Errorf("cardigann: walk %s: %w", path, walkErr)}
	}
	sort.Slice(files, func(i, j int) bool {
		return filepath.ToSlash(files[i]) < filepath.ToSlash(files[j])
	})

	results := make([]Result, 0, len(files))
	var errs []error
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			errs = append(errs, err)
			break
		}
		body, err := os.ReadFile(file)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", file, err))
			continue
		}
		result, err := convertDefinition(body, opts)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", file, err))
			continue
		}
		results = append(results, result)
	}
	return results, errs
}

func convertDefinition(body []byte, opts Options) (Result, error) {
	if len(bytes.TrimSpace(body)) == 0 {
		return Result{}, fmt.Errorf("cardigann: empty document")
	}
	var raw rawDefinition
	if err := yaml.Unmarshal(body, &raw); err != nil {
		return Result{}, fmt.Errorf("cardigann: YAML: %w", err)
	}

	c := &converter{
		raw:          raw,
		opts:         normaliseOptions(opts),
		settingNames: make(map[string]string),
		usedSettings: make(map[string]bool),
		extracted:    make(map[string]bool),
	}
	c.result.SourceID = strings.TrimSpace(raw.ID)
	c.result.Name = strings.TrimSpace(raw.Name)

	if nodeSet(raw.Go) || nodeSet(raw.Custom) || nodeSet(raw.Code) || nodeSet(raw.Script) {
		c.addUnsupported("custom Go")
		c.addWarning("definition contains custom Go, which is never translated")
		return c.fail(fmt.Errorf("custom Go is not supported"))
	}
	if nodeSet(raw.RequestDelay) || nodeSet(raw.RequestDelaySnake) || nodeSet(raw.Delay) {
		c.addUnsupported("request.delay")
		c.addWarning("request delay is not represented in a native definition")
	}

	meta, err := c.convertMeta()
	if err != nil {
		return c.fail(err)
	}
	c.result.Name = meta.Name
	if c.result.SourceID == "" {
		c.result.SourceID = meta.ID
	}

	c.convertSettings()
	categories := c.convertCategories()
	capabilities := c.convertCapabilities()
	auth := c.convertAuth()
	search, err := c.convertSearch()
	if err != nil {
		return c.fail(err)
	}

	def := &definition.Definition{
		Schema:       definition.CurrentSchema,
		Meta:         meta,
		Settings:     c.settings,
		Categories:   categories,
		Capabilities: capabilities,
		Auth:         auth,
		Search:       search,
		Health: &definition.Health{
			Query:   "test",
			MinRows: 1,
		},
		Tests: nil,
	}

	encoded, err := marshalDefinition(def)
	if err != nil {
		return c.fail(fmt.Errorf("marshal native definition: %w", err))
	}
	parsed, err := definition.Parse(encoded)
	if err != nil {
		return c.fail(fmt.Errorf("converted definition failed validation: %w", err))
	}

	c.result.Definition = parsed
	c.result.Body = encoded
	c.result.Hash = definition.Fingerprint(encoded)
	c.finalize()
	return c.result, nil
}

func normaliseOptions(opts Options) Options {
	opts.Author = strings.TrimSpace(opts.Author)
	if opts.Author == "" {
		opts.Author = defaultAuthor
	}
	opts.Type = strings.ToLower(strings.TrimSpace(opts.Type))
	return opts
}

func (c *converter) convertMeta() (definition.Meta, error) {
	id := slugify(c.raw.ID)
	if id == "" {
		id = slugify(c.raw.Name)
	}
	if id == "" {
		return definition.Meta{}, fmt.Errorf("definition has no usable id or name")
	}

	name := strings.TrimSpace(c.raw.Name)
	if name == "" {
		name = id
		c.addWarning("definition has no name; using id %q", id)
	}
	description := strings.TrimSpace(c.raw.Description)

	metaType, typeWarning := c.metaType()
	if typeWarning != "" {
		c.addWarning("%s", typeWarning)
	}
	protocol := c.metaProtocol()

	urls := c.metaURLs()
	if len(urls) == 0 {
		return definition.Meta{}, fmt.Errorf("definition has no valid http/https link")
	}

	return definition.Meta{
		ID:          id,
		Name:        name,
		Language:    strings.TrimSpace(c.raw.Language),
		Type:        metaType,
		Protocol:    protocol,
		Author:      c.opts.Author,
		Description: description,
		URLs:        urls,
	}, nil
}

func (c *converter) metaType() (string, string) {
	source := strings.ToLower(strings.TrimSpace(c.raw.Type))
	if source == "usenet" {
		source = ""
	}
	normalized := normalizeType(source)
	if c.opts.Type != "" {
		if optionType := normalizeType(c.opts.Type); optionType != "" {
			// An explicit option is an override for a migration that wants,
			// for example, to import all definitions as private.
			return optionType, ""
		}
		if normalized != "" {
			return normalized, fmt.Sprintf("unknown options type %q; using %q", c.opts.Type, normalized)
		}
		return defaultType, fmt.Sprintf("unknown options type %q; using %q", c.opts.Type, defaultType)
	}
	if normalized != "" {
		return normalized, ""
	}
	if source != "" {
		return defaultType, fmt.Sprintf("unknown site type %q; using %q", c.raw.Type, defaultType)
	}
	return defaultType, ""
}

func normalizeType(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "public":
		return "public"
	case "semi-private", "semiprivate", "semi private", "semi_private":
		return "semi-private"
	case "private":
		return "private"
	default:
		return ""
	}
}

func (c *converter) metaProtocol() string {
	if c.raw.Usenet {
		return "usenet"
	}
	if strings.EqualFold(strings.TrimSpace(c.raw.Type), "usenet") {
		return "usenet"
	}
	switch strings.ToLower(strings.TrimSpace(c.raw.Protocol)) {
	case "usenet", "newznab":
		return "usenet"
	case "", "torrent", "torznab":
		return "torrent"
	}
	if strings.TrimSpace(c.raw.Protocol) != "" {
		c.addWarning("unknown protocol %q; assuming torrent", c.raw.Protocol)
	}
	return "torrent"
}

func (c *converter) metaURLs() []string {
	var raw []string
	raw = append(raw, c.raw.Links...)
	raw = append(raw, c.raw.LegacyLinks...)
	raw = append(raw, c.raw.URLs...)
	if strings.TrimSpace(c.raw.Link) != "" {
		raw = append(raw, c.raw.Link)
	}
	if strings.TrimSpace(c.raw.URL) != "" {
		raw = append(raw, c.raw.URL)
	}

	var out []string
	seen := make(map[string]bool)
	for _, candidate := range raw {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		parsed, err := url.Parse(candidate)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			c.addWarning("link %q is not an absolute http/https URL; skipped", sanitizeURL(candidate))
			continue
		}
		if parsed.User != nil {
			c.addWarning("credentials in link %s were removed", sanitizeURL(candidate))
			parsed.User = nil
		}
		normalized := parsed.String()
		if seen[normalized] {
			continue
		}
		seen[normalized] = true
		out = append(out, normalized)
	}
	return out
}

func marshalDefinition(def *definition.Definition) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := yaml.NewEncoder(&buffer)
	encoder.SetIndent(2)
	if err := encoder.Encode(def); err != nil {
		return nil, err
	}
	if err := encoder.Close(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func (c *converter) addWarning(format string, args ...any) {
	message := redactSecrets(fmt.Sprintf(format, args...), c.secrets...)
	if message == "" || containsString(c.result.Warnings, message) {
		return
	}
	c.result.Warnings = append(c.result.Warnings, message)
}

func (c *converter) addUnsupported(feature string) {
	feature = strings.TrimSpace(feature)
	if feature == "" || containsString(c.result.Unsupported, feature) {
		return
	}
	c.result.Unsupported = append(c.result.Unsupported, feature)
}

func (c *converter) finalize() {
	for i, warning := range c.result.Warnings {
		c.result.Warnings[i] = redactSecrets(warning, c.secrets...)
	}
	for i, feature := range c.result.Unsupported {
		c.result.Unsupported[i] = redactSecrets(feature, c.secrets...)
	}
}

func (c *converter) fail(err error) (Result, error) {
	c.finalize()
	c.result.Definition = nil
	c.result.Body = nil
	c.result.Hash = ""
	return c.result, err
}
