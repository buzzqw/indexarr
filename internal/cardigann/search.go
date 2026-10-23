package cardigann

import (
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/buzzqw/indexarr/internal/definition"
	"gopkg.in/yaml.v3"
)

// convertSearch maps search.paths, search.inputs, rows and fields. The first
// usable path is used; multi-path and multi-step searches are reported.
func (c *converter) convertSearch() (definition.Search, error) {
	search := c.raw.Search
	if search == nil {
		c.addWarning("no search section found; the definition cannot be converted")
		return definition.Search{}, fmt.Errorf("no search section")
	}

	c.reportSearchExtras(search)
	if nodeSet(search.Go) || nodeSet(search.Custom) || nodeSet(search.Code) || nodeSet(search.Script) {
		c.addUnsupported("custom Go")
		c.addWarning("search uses custom Go; the definition cannot be converted")
		return definition.Search{}, fmt.Errorf("search uses custom Go, which is not translated")
	}

	path, pathCount, err := c.selectSearchPath(search)
	if err != nil {
		return definition.Search{}, err
	}
	if pathCount > 1 {
		c.addWarning("%d search paths found; only the first usable path was converted", pathCount)
	}

	method := strings.ToLower(strings.TrimSpace(firstNonEmpty(path.Method, search.Method)))
	if strings.Contains(method, "{{") {
		convertedMethod, methodErr := c.convertTemplate("search.method", method, false)
		if methodErr != nil {
			return definition.Search{}, methodErr
		}
		c.addWarning("search method template %q approximated to %q", method, strings.TrimSpace(convertedMethod))
		method = strings.ToLower(strings.TrimSpace(convertedMethod))
	}
	if method == "" {
		method = "get"
	}
	if method != "get" && method != "post" {
		c.addUnsupported("search method " + method)
		return definition.Search{}, fmt.Errorf("search method %q is not supported", method)
	}

	requestPath, err := c.convertTemplate("search.path", path.Path, true)
	if err != nil {
		return definition.Search{}, err
	}

	inputs := make(map[string]string, len(search.Inputs)+len(path.Inputs))
	for key, value := range search.Inputs {
		inputs[key] = value
	}
	for key, value := range path.Inputs {
		inputs[key] = value
	}
	renderedInputs := make(map[string]string, len(inputs))
	for _, key := range sortedKeys(inputs) {
		if strings.TrimSpace(key) == "" {
			c.addWarning("search input with an empty name ignored")
			continue
		}
		if key == "$raw" {
			rawParams, rawErr := c.convertRawInput("search.inputs.$raw", inputs[key])
			if rawErr != nil {
				return definition.Search{}, rawErr
			}
			for name, value := range rawParams {
				renderedInputs[name] = value
			}
			continue
		}
		rendered, err := c.convertTemplate("search.inputs."+key, inputs[key], true)
		if err != nil {
			return definition.Search{}, err
		}
		renderedInputs[key] = rendered
	}

	headers, err := c.convertHeaders("search.headers", search.Headers, path.Headers)
	if err != nil {
		return definition.Search{}, err
	}

	request := definition.Request{}
	if len(headers) > 0 {
		request.Headers = headers
	}
	if method == "post" {
		request.Post = requestPath
		request.Form = renderedInputs
	} else {
		request.Get = requestPath
		request.Params = renderedInputs
	}

	response, err := c.convertResponse(search, path)
	if err != nil {
		return definition.Search{}, err
	}

	return definition.Search{
		Request:  request,
		Response: response,
	}, nil
}

func (c *converter) selectSearchPath(search *rawSearch) (rawSearchPath, int, error) {
	paths := append(rawPaths(nil), search.Paths...)
	if len(paths) == 0 && strings.TrimSpace(search.Path) != "" {
		paths = append(paths, rawSearchPath{Path: search.Path, Method: search.Method})
	}
	if len(paths) == 0 {
		return rawSearchPath{}, 0, fmt.Errorf("search has no path")
	}

	usable := 0
	for _, path := range paths {
		if strings.TrimSpace(path.Path) != "" {
			usable++
		}
	}
	if usable == 0 {
		return rawSearchPath{}, 0, fmt.Errorf("search paths are all empty")
	}
	seenUsable := 0
	for _, path := range paths {
		if strings.TrimSpace(path.Path) == "" {
			continue
		}
		seenUsable++
		if seenUsable == 1 {
			return path, usable, nil
		}
	}
	return rawSearchPath{}, 0, fmt.Errorf("search has no usable path")
}

func (c *converter) reportSearchExtras(search *rawSearch) {
	if nodeSet(search.RequestDelay) || nodeSet(search.RequestDelayAlt) || nodeSet(search.Delay) {
		c.addUnsupported("request.delay")
		c.addWarning("search request delay is not represented in a native definition")
	}
	if nodeSet(search.KeywordsFilters) || nodeSet(c.raw.KeywordsFilters) {
		c.addUnsupported("keywordsfilters")
		c.addWarning("keywordsfilters are not mapped; keyword rewriting is lost")
	}
	if nodeSet(search.TitleFilters) || nodeSet(c.raw.TitleFilters) {
		c.addUnsupported("titlefilters")
		c.addWarning("titlefilters are not mapped; title rewriting is lost")
	}
	if nodeSet(search.Prefilter) {
		c.addUnsupported("prefilter")
		c.addWarning("search.prefilter is not mapped")
	}
	if nodeSet(search.Filter) {
		c.addUnsupported("search.filter")
		c.addWarning("search.filter is not mapped")
	}
	if nodeSet(search.Sort) {
		c.addWarning("search.sort is not mapped; the site's default sort is used")
	}
	if nodeSet(search.Count) {
		c.addUnsupported("search.count")
		c.addWarning("search.count is not mapped")
	}
	if nodeSet(search.DateHeaders) {
		c.addUnsupported("search.dateheaders")
		c.addWarning("search.dateheaders is not mapped")
	}
	for _, path := range search.Paths {
		if nodeSet(path.FollowRedirect) {
			c.addWarning("search path followredirect is implicit in the native transport")
		}
	}
}

func (c *converter) convertResponse(search *rawSearch, path rawSearchPath) (definition.Response, error) {
	responseType := c.resolveResponseType(search, path)
	if responseType == "" {
		responseType = "html"
	}
	switch responseType {
	case "html", "json", "xml":
	default:
		c.addUnsupported("response.type " + responseType)
		c.addWarning("response type %q is not supported by native v1", responseType)
		return definition.Response{}, fmt.Errorf("response type %q is not supported", responseType)
	}

	rows := rawRows{}
	switch {
	case path.Rows.Set:
		rows = path.Rows
	case path.Response != nil && path.Response.Rows.Set:
		rows = path.Response.Rows
	case search.Response != nil && search.Response.Rows.Set:
		rows = search.Response.Rows
	case search.Rows.Set:
		rows = search.Rows
	case c.raw.Response != nil && c.raw.Response.Rows.Set:
		rows = c.raw.Response.Rows
	}

	fields := rawFields{}
	switch {
	case len(path.Fields) > 0:
		fields = path.Fields
	case path.Response != nil && len(path.Response.Fields) > 0:
		fields = path.Response.Fields
	case search.Response != nil && len(search.Response.Fields) > 0:
		fields = search.Response.Fields
	case len(search.Fields) > 0:
		fields = search.Fields
	case c.raw.Response != nil && len(c.raw.Response.Fields) > 0:
		fields = c.raw.Response.Fields
	}
	if len(fields) == 0 {
		return definition.Response{}, fmt.Errorf("search has no fields")
	}
	fields = c.resolveTextFields(fields)

	c.reportResponseExtras(search, path)

	response := definition.Response{Type: responseType}
	switch responseType {
	case "html":
		selector := rows.Selector
		if len(selector) == 0 && rows.Path != "" {
			c.addWarning("HTML rows.path %q used as a CSS selector", rows.Path)
			selector = definition.Selector{rows.Path}
		}
		if len(selector) == 0 {
			return definition.Response{}, fmt.Errorf("HTML search has no rows selector")
		}
		response.Rows = definition.RowSelector{Selector: selector}
	case "json":
		rowPath := strings.TrimSpace(rows.Path)
		if rowPath == "" && len(rows.Selector) > 0 {
			rowPath = cleanJSONPath(rows.Selector[0])
			c.addWarning("JSON rows selector %q interpreted as a path", rows.Selector[0])
			if len(rows.Selector) > 1 {
				c.addWarning("JSON rows has multiple selectors; only the first was used")
			}
		}
		if rowPath == "" {
			c.addWarning("JSON rows path is missing; the response root must be an array")
		}
		response.Rows = definition.RowSelector{Path: cleanJSONPath(rowPath)}
	case "xml":
		rowPath := strings.TrimSpace(rows.Path)
		if rowPath == "" && len(rows.Selector) > 0 {
			rowPath = cleanXMLPath(rows.Selector[0])
			c.addWarning("XML rows selector %q interpreted as a path", rows.Selector[0])
			if len(rows.Selector) > 1 {
				c.addWarning("XML rows has multiple selectors; only the first was used")
			}
		}
		if rowPath == "" {
			return definition.Response{}, fmt.Errorf("XML search has no rows path")
		}
		response.Rows = definition.RowSelector{Path: rowPath}
	}

	fieldNames := make([]string, 0, len(fields))
	for name := range fields {
		fieldNames = append(fieldNames, name)
	}
	sort.Strings(fieldNames)

	origin := make(map[string]string, len(fields))
	for _, name := range fieldNames {
		field, nativeName, ok := c.convertField(name, fields[name], responseType)
		if !ok {
			continue
		}
		if previous, exists := origin[nativeName]; exists {
			if nativeName == "download" && previous == "link" {
				response.Fields[nativeName] = field
				origin[nativeName] = name
				c.addWarning("field %q replaced link as the download field", name)
			} else {
				c.addWarning("field %q duplicates native field %q; ignored", name, nativeName)
			}
			continue
		}
		if response.Fields == nil {
			response.Fields = make(map[string]definition.Field, len(fields))
		}
		response.Fields[nativeName] = field
		origin[nativeName] = name
	}

	c.applyDownload(&response, responseType, origin)

	if _, ok := response.Fields["title"]; !ok {
		return definition.Response{}, fmt.Errorf("search fields have no title")
	}
	response.Required = append(response.Required, "title")
	for _, link := range []string{"download", "magnet", "details"} {
		if _, ok := response.Fields[link]; ok {
			response.Required = append(response.Required, link)
			break
		}
	}
	if _, ok := response.Fields["download"]; !ok {
		if _, magnet := response.Fields["magnet"]; !magnet {
			if _, details := response.Fields["details"]; !details {
				c.addWarning("no download, magnet or details field; native rows may be discarded")
			}
		}
	}
	return response, nil
}

func (c *converter) resolveResponseType(search *rawSearch, path rawSearchPath) string {
	candidates := []string{}
	if path.Response != nil {
		candidates = append(candidates, path.Response.Type)
	}
	if search.Response != nil {
		candidates = append(candidates, search.Response.Type)
	}
	candidates = append(candidates, search.Type)
	if c.raw.Response != nil {
		candidates = append(candidates, c.raw.Response.Type)
	}
	for _, candidate := range candidates {
		name := strings.ToLower(strings.TrimSpace(candidate))
		if name == "" {
			continue
		}
		switch name {
		case "html", "json", "xml":
			return name
		case "application/json", "text/json":
			return "json"
		case "application/xml", "text/xml":
			return "xml"
		case "text/html":
			return "html"
		default:
			c.addUnsupported("response.type " + name)
			return name
		}
	}
	return "html"
}

func (c *converter) reportResponseExtras(search *rawSearch, path rawSearchPath) {
	responses := []*rawResponse{search.Response, path.Response, c.raw.Response}
	for _, response := range responses {
		if response == nil {
			continue
		}
		if nodeSet(response.Count) {
			c.addUnsupported("response.count")
			c.addWarning("response.count is not mapped")
		}
		if nodeSet(response.DateHeaders) {
			c.addUnsupported("response.dateheaders")
			c.addWarning("response.dateheaders is not mapped")
		}
	}
}

func (c *converter) convertField(name string, raw rawField, responseType string) (definition.Field, string, bool) {
	original := strings.TrimSpace(name)
	if original == "" {
		c.addWarning("field with an empty name ignored")
		return definition.Field{}, "", false
	}
	nativeName := nativeFieldName(original)
	if nativeName == "" {
		c.addWarning("field %q has no native name; dropped", original)
		return definition.Field{}, "", false
	}

	if strings.TrimSpace(raw.Remove) != "" {
		c.addWarning("field %q remove selector is not mapped", original)
	}
	if strings.TrimSpace(string(raw.Default)) != "" {
		c.addUnsupported("field default")
		c.addWarning("field %q default value is not supported by native v1", original)
	}

	translation := c.convertFilters(original, raw.Filters)
	attr := strings.TrimSpace(firstNonEmpty(raw.Attribute, raw.Attr))
	if strings.EqualFold(attr, "text") {
		attr = ""
	}

	field := definition.Field{
		Filters: translation.filters,
		Layouts: translation.layouts,
	}

	switch responseType {
	case "html":
		selector := raw.Selector
		if len(selector) == 0 && strings.TrimSpace(raw.Path) != "" {
			c.addWarning("field %q path used as an HTML selector", original)
			selector = definition.Selector{raw.Path}
		}
		if len(selector) == 0 {
			c.addWarning("field %q has no selector; dropped", original)
			return definition.Field{}, "", false
		}
		field.Selector = selector
		field.Attr = attr
	case "json":
		path := strings.TrimSpace(raw.Path)
		if path == "" && len(raw.Selector) > 0 {
			path = cleanJSONPath(raw.Selector[0])
			c.addWarning("field %q selector interpreted as a JSON path", original)
			if len(raw.Selector) > 1 {
				c.addWarning("field %q has multiple selectors; only the first was used", original)
			}
		}
		if path == "" {
			c.addWarning("field %q has no JSON path; dropped", original)
			return definition.Field{}, "", false
		}
		if attr != "" {
			c.addWarning("field %q attribute %q is not supported for JSON; ignored", original, attr)
		}
		field.Path = cleanJSONPath(path)
		if !looksLikeJSONPath(field.Path) {
			c.addWarning("field %q JSON path %q may not be a valid dotted path", original, field.Path)
		}
	case "xml":
		path := strings.TrimSpace(raw.Path)
		if path == "" && len(raw.Selector) > 0 {
			path = cleanXMLPath(raw.Selector[0])
			c.addWarning("field %q selector interpreted as an XML path", original)
			if len(raw.Selector) > 1 {
				c.addWarning("field %q has multiple selectors; only the first was used", original)
			}
		}
		if path == "" {
			c.addWarning("field %q has no XML path; dropped", original)
			return definition.Field{}, "", false
		}
		if attr != "" {
			path = strings.TrimSuffix(path, "@") + "@" + attr
		}
		field.Path = path
	}

	field.Type = c.fieldType(nativeName, raw.Type, translation)
	if field.Type == "date" && len(field.Layouts) == 0 {
		// Native date parsing also understands common layouts and Unix
		// timestamps, so this is not an error.
		c.addWarning("field %q is a date without an explicit layout", original)
	}
	if !knownField(nativeName) {
		c.addWarning("field %q has no native semantic and will be ignored by normalization", original)
	}
	return field, nativeName, true
}

func (c *converter) fieldType(name, rawType string, translation filterTranslation) string {
	kind := strings.ToLower(strings.TrimSpace(rawType))
	switch kind {
	case "text", "int", "size", "date", "url", "category":
		return kind
	case "integer", "number":
		return "int"
	case "":
		// infer below
	default:
		c.addWarning("field %q has unknown type %q; inferred", name, rawType)
	}
	if translation.inferDate {
		return "date"
	}
	switch strings.ToLower(name) {
	case "size":
		return "size"
	case "seeders", "leechers", "peers", "files", "grabs", "comments":
		return "int"
	case "date", "published":
		return "date"
	case "category", "categories":
		return "category"
	case "download", "details", "magnet":
		return "url"
	default:
		return "text"
	}
}

func nativeFieldName(name string) string {
	switch strings.ToLower(name) {
	case "link":
		return "download"
	case "imdb":
		return "imdbid"
	case "tmdb":
		return "tmdbid"
	case "tvdb":
		return "tvdbid"
	case "publishdate", "pubdate", "published":
		return "date"
	case "categories":
		return "category"
	default:
		return name
	}
}

func knownField(name string) bool {
	switch name {
	case "title", "guid", "details", "download", "magnet", "infohash",
		"size", "seeders", "leechers", "date", "category", "imdbid",
		"tmdbid", "tvdbid":
		return true
	default:
		return false
	}
}

func (c *converter) applyDownload(response *definition.Response, responseType string, origin map[string]string) {
	download := c.raw.Download
	if download == nil {
		return
	}
	if nodeSet(download.Go) || nodeSet(download.Custom) || nodeSet(download.Code) || nodeSet(download.Script) {
		c.addUnsupported("custom Go")
		c.addWarning("download uses custom code; only its selector is mapped")
	}
	if nodeSetString(download.Method) && !strings.EqualFold(strings.TrimSpace(download.Method), "get") {
		c.addUnsupported("download multi-step")
		c.addWarning("download method %q is not a simple GET; mapping kept only for the link", download.Method)
	}
	if strings.TrimSpace(download.Path) != "" || len(download.Inputs) > 0 {
		c.addUnsupported("download multi-step")
		c.addWarning("download path/inputs are not mapped; a multi-step download needs a native connector")
	}
	if len(download.Selector) > 0 && len(download.Selectors) > 0 {
		c.addWarning("download selector and download.selectors are both present; using selectors")
	}

	field := rawField{}
	switch {
	case len(download.Selectors) > 0:
		if len(download.Selectors) > 1 {
			c.addUnsupported("download multi-step")
			c.addWarning("download has %d selectors; only the first was converted", len(download.Selectors))
		}
		first := download.Selectors[0]
		if strings.TrimSpace(first.Path) != "" ||
			(nodeSetString(first.Method) && !strings.EqualFold(strings.TrimSpace(first.Method), "get")) {
			c.addUnsupported("download multi-step")
		}
		field = rawField{
			Selector:  first.Selector,
			Attribute: first.Attribute,
			Attr:      first.Attr,
			Filters:   first.Filters,
		}
	case len(download.Selector) > 0:
		field = rawField{
			Selector:  download.Selector,
			Attribute: download.Attribute,
			Attr:      download.Attr,
			Filters:   download.Filters,
		}
	}
	if len(field.Selector) == 0 {
		c.addWarning("download section has no selector; falling back to search fields")
		return
	}

	converted, _, ok := c.convertField("download", field, responseType)
	if !ok {
		return
	}
	if response.Fields == nil {
		response.Fields = make(map[string]definition.Field)
	}
	if previous, exists := origin["download"]; exists && previous != "download" {
		c.addWarning("download section replaced field %q", previous)
	}
	response.Fields["download"] = converted
	origin["download"] = "download"
}

func nodeSetString(value string) bool {
	return strings.TrimSpace(value) != ""
}

func cleanJSONPath(raw string) string {
	path := strings.TrimSpace(raw)
	path = strings.TrimPrefix(path, "$.")
	path = strings.TrimPrefix(path, "$")
	path = strings.TrimPrefix(path, ".")
	return path
}

func cleanXMLPath(raw string) string {
	return cleanJSONPath(raw)
}

func looksLikeJSONPath(path string) bool {
	if path == "" {
		return true
	}
	for _, r := range path {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.', r == '_', r == '-', r == '[', r == ']', r == '*':
		default:
			return false
		}
	}
	return true
}

// convertHeaders maps Cardigann search headers to native request headers.
// A list value is joined with ", " because HTTP allows equivalent repeated
// values in a single comma-separated field, with a warning that they are not
// byte-for-byte equivalent.
func (c *converter) convertHeaders(label string, nodes ...yaml.Node) (map[string]string, error) {
	var out map[string]string
	for _, node := range nodes {
		if nodeSet(node) {
			if node.Kind == yaml.AliasNode && node.Alias != nil {
				node = *node.Alias
			}
			if node.Kind != yaml.MappingNode {
				c.addWarning("%s is not a mapping; ignored", label)
				continue
			}
			for index := 0; index+1 < len(node.Content); index += 2 {
				name := strings.TrimSpace(scalarText(node.Content[index]))
				if name == "" {
					c.addWarning("%s contains a header without a name; ignored", label)
					continue
				}
				value, multiple := headerValue(node.Content[index+1])
				if multiple {
					c.addWarning("header %q has multiple values; joined with \", \"", name)
				}
				if value == "" {
					c.addWarning("header %q has no usable value; ignored", name)
					continue
				}
				converted, err := c.convertTemplate(label+"."+name, value, true)
				if err != nil {
					return nil, err
				}
				if out == nil {
					out = make(map[string]string, len(nodes)*2)
				}
				out[name] = converted
			}
		}
	}
	return out, nil
}

func headerValue(node *yaml.Node) (string, bool) {
	if node == nil {
		return "", false
	}
	if node.Kind == yaml.AliasNode && node.Alias != nil {
		return headerValue(node.Alias)
	}
	switch node.Kind {
	case yaml.ScalarNode:
		return scalarText(node), false
	case yaml.SequenceNode:
		parts := make([]string, 0, len(node.Content))
		for _, item := range node.Content {
			if text := scalarText(item); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, ", "), len(parts) > 1
	case yaml.MappingNode:
		return scalarText(node), false
	default:
		return "", false
	}
}

// convertRawInput expands Cardigann's special $raw input. Cardigann appends
// the rendered string to the query; native definitions need individual
// parameters, so the fragment is parsed after template conversion.
func (c *converter) convertRawInput(label, source string) (map[string]string, error) {
	fragment, err := c.convertTemplateMode(label, source, true, true)
	if err != nil {
		return nil, err
	}
	fragment = strings.TrimSpace(fragment)
	fragment = strings.TrimPrefix(fragment, "?")
	fragment = strings.Trim(fragment, "&")
	if fragment == "" {
		return nil, nil
	}
	out, err := parseRawQuery(fragment)
	if err != nil {
		return nil, fmt.Errorf("%s: raw query fragment cannot be parsed: %w", label, err)
	}
	if len(out) == 0 {
		return nil, nil
	}
	c.addWarning("raw search input $raw expanded into native query parameters; repeated parameters are approximated")
	return out, nil
}

var resultFieldPattern = regexp.MustCompile(`\.Result\.([A-Za-z0-9_]+)`)

// resolveTextFields replaces a text-only field whose template only selects
// among other result fields (for example a computed title) with a field that
// has a real selector. The native format has no computed fields, so this is
// the only representation that keeps the required title or download link.
func (c *converter) resolveTextFields(fields rawFields) rawFields {
	out := make(rawFields, len(fields))
	for name, field := range fields {
		out[name] = field
	}
	for name := range fields {
		field := fields[name]
		if !textOnlyField(field) {
			continue
		}
		seen := map[string]bool{name: true}
		resolved, ok := c.resolveTextField(name, fields, seen, 0)
		if !ok {
			continue
		}
		resolved.Text = ""
		out[name] = resolved
		c.addWarning("field %q text template resolved from another result field", name)
	}
	return out
}

func (c *converter) resolveTextField(name string, fields rawFields, seen map[string]bool, depth int) (rawField, bool) {
	if depth > 8 {
		return rawField{}, false
	}
	field, ok := fields[name]
	if !ok {
		return rawField{}, false
	}
	if !textOnlyField(field) {
		return field, true
	}
	for _, match := range resultFieldPattern.FindAllStringSubmatch(field.Text, -1) {
		target := match[1]
		if seen[target] {
			continue
		}
		seen[target] = true
		child, found := c.resolveTextField(target, fields, seen, depth+1)
		if !found || textOnlyField(child) {
			continue
		}
		child.Filters = append(append([]rawFilter(nil), child.Filters...), field.Filters...)
		child.Text = ""
		return child, true
	}
	return rawField{}, false
}

func textOnlyField(field rawField) bool {
	return len(field.Selector) == 0 && strings.TrimSpace(field.Path) == ""
}

// parseRawQuery parses a raw Cardigann query fragment. It deliberately avoids
// net/url.ParseQuery because definitions sometimes use semicolons inside a
// value and Go rejects those as separators.
func parseRawQuery(fragment string) (map[string]string, error) {
	out := make(map[string]string)
	for _, pair := range strings.Split(fragment, "&") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		key, value := pair, ""
		if index := strings.IndexByte(pair, '='); index >= 0 {
			key, value = pair[:index], pair[index+1:]
		}
		decodedKey, err := url.QueryUnescape(key)
		if err != nil {
			return nil, fmt.Errorf("invalid parameter name %q", key)
		}
		decodedValue, err := url.QueryUnescape(value)
		if err != nil {
			return nil, fmt.Errorf("invalid value for %q", decodedKey)
		}
		decodedKey = strings.TrimSpace(decodedKey)
		if decodedKey == "" || strings.Contains(decodedKey, "{{") {
			return nil, fmt.Errorf("parameter name %q cannot be represented", decodedKey)
		}
		out[decodedKey] = decodedValue
	}
	return out, nil
}
