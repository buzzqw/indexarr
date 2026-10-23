package cardigann

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var templatePattern = regexp.MustCompile(`\{\{\s*(.*?)\s*\}\}`)

// convertTemplate rewrites a Cardigann template into the deliberately small
// native template language. Only known variables are accepted: an unknown,
// dynamic or malformed expression is an error, never a silently empty value.
func (c *converter) convertTemplate(label, source string, critical bool) (string, error) {
	if !strings.Contains(source, "{{") {
		return source, nil
	}
	if strings.Count(source, "{{") != strings.Count(source, "}}") {
		c.addUnsupported("malformed template")
		return "", fmt.Errorf("%s: malformed template expression", label)
	}
	matches := templatePattern.FindAllStringSubmatchIndex(source, -1)
	if len(matches) == 0 || strings.Count(source, "{{") != len(matches) {
		c.addUnsupported("malformed template")
		return "", fmt.Errorf("%s: malformed template expression", label)
	}

	var out strings.Builder
	last := 0
	for _, match := range matches {
		if match[0] > last {
			out.WriteString(source[last:match[0]])
		}
		expr := strings.TrimSpace(source[match[2]:match[3]])
		converted, err := c.convertExpression(expr)
		if err != nil {
			kind := "request template"
			if !critical {
				kind = "optional template"
			}
			c.addUnsupported("template " + expr)
			return "", fmt.Errorf("%s: %s: %w", label, kind, err)
		}
		out.WriteString(converted)
		last = match[1]
	}
	out.WriteString(source[last:])
	return out.String(), nil
}

func (c *converter) convertExpression(expr string) (string, error) {
	expr = normaliseExpression(expr)
	if expr == "" {
		return "", fmt.Errorf("empty expression")
	}

	base, filter, arg, hasFilter, err := splitTemplatePipeline(expr)
	if err != nil {
		return "", err
	}

	native, isCategories, err := c.nativeVariable(base)
	if err != nil {
		return "", err
	}

	if hasFilter {
		if !strings.EqualFold(filter, "join") {
			return "", fmt.Errorf("filter %q is not supported", filter)
		}
		if !isCategories {
			return "", fmt.Errorf("join is only supported on categories")
		}
		if arg == "" {
			arg = ","
		}
		return "{{ categories | join(" + strconv.Quote(arg) + ") }}", nil
	}
	return "{{ " + native + " }}", nil
}

func (c *converter) nativeVariable(base string) (string, bool, error) {
	base = strings.TrimSpace(base)
	if base == "" {
		return "", false, fmt.Errorf("empty expression")
	}

	if strings.HasPrefix(base, ".") {
		field := strings.TrimPrefix(base, ".")
		switch strings.ToLower(field) {
		case "keywords", "query":
			return "query.keywords", false, nil
		case "categories", "category":
			return "categories", true, nil
		case "page":
			return "page", false, nil
		case "offset":
			return "offset", false, nil
		case "limit":
			return "limit", false, nil
		case "today", "yesterday", "timestamp", "now", "utctime":
			return "", false, fmt.Errorf("dynamic variable .%s is not supported", field)
		}

		lower := strings.ToLower(field)
		if strings.HasPrefix(lower, "config.") {
			key := strings.TrimSpace(field[len("config."):])
			if key == "" {
				return "", false, fmt.Errorf("empty .Config reference")
			}
			native := c.ensureSetting(key, false)
			if native == "" {
				return "", false, fmt.Errorf("invalid .Config reference")
			}
			return "settings." + native, false, nil
		}
		if strings.HasPrefix(lower, "query.") {
			key := strings.TrimSpace(field[len("query."):])
			if variable, ok := queryVariable(key); ok {
				return "query." + variable, false, nil
			}
			return "", false, fmt.Errorf("unknown query variable .Query.%s", key)
		}
		return "", false, fmt.Errorf("unknown variable .%s", field)
	}

	lower := strings.ToLower(base)
	switch lower {
	case "categories":
		return "categories", true, nil
	case "page":
		return "page", false, nil
	case "offset":
		return "offset", false, nil
	case "limit":
		return "limit", false, nil
	}
	if strings.HasPrefix(lower, "settings.") {
		key := strings.TrimSpace(base[len("settings."):])
		native := c.ensureSetting(key, false)
		if native == "" {
			return "", false, fmt.Errorf("invalid settings reference")
		}
		return "settings." + native, false, nil
	}
	if strings.HasPrefix(lower, "query.") {
		key := strings.TrimSpace(base[len("query."):])
		if variable, ok := queryVariable(key); ok {
			return "query." + variable, false, nil
		}
		return "", false, fmt.Errorf("unknown query variable %q", base)
	}
	if c.extracted[base] {
		return base, false, nil
	}
	return "", false, fmt.Errorf("unknown variable %q", base)
}

func queryVariable(raw string) (string, bool) {
	normalized := strings.ToLower(strings.TrimSpace(raw))
	normalized = strings.NewReplacer("_", "", "-", "", ".", "").Replace(normalized)
	switch normalized {
	case "keywords", "keyword", "query", "q":
		return "keywords", true
	case "season":
		return "season", true
	case "ep", "episode":
		return "ep", true
	case "imdb", "imdbid":
		return "imdbid", true
	case "tvdb", "tvdbid":
		return "tvdbid", true
	case "tmdb", "tmdbid":
		return "tmdbid", true
	case "tvmaze", "tvmazeid":
		return "tvmazeid", true
	case "rid", "rageid":
		return "rid", true
	case "artist":
		return "artist", true
	case "album":
		return "album", true
	case "author":
		return "author", true
	case "title":
		return "title", true
	case "year":
		return "year", true
	case "genre":
		return "genre", true
	default:
		return "", false
	}
}

// normaliseExpression removes Go template whitespace-trim markers, which are
// irrelevant for the native language.
func normaliseExpression(expr string) string {
	expr = strings.TrimSpace(expr)
	if strings.HasPrefix(expr, "-") {
		expr = strings.TrimSpace(strings.TrimPrefix(expr, "-"))
	}
	if strings.HasSuffix(expr, "-") {
		expr = strings.TrimSpace(strings.TrimSuffix(expr, "-"))
	}
	return expr
}

// splitTemplatePipeline splits on an unquoted "|". Only one filter is
// accepted, matching the native template language.
func splitTemplatePipeline(expr string) (base, filter, arg string, hasFilter bool, err error) {
	inSingle, inDouble := false, false
	index := -1
	for i := 0; i < len(expr); i++ {
		switch expr[i] {
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
		case '|':
			if !inSingle && !inDouble {
				if index >= 0 {
					return "", "", "", false, fmt.Errorf("more than one filter is not supported")
				}
				index = i
			}
		}
	}
	if inSingle || inDouble {
		return "", "", "", false, fmt.Errorf("unterminated quoted argument")
	}
	if index < 0 {
		return strings.TrimSpace(expr), "", "", false, nil
	}
	base = strings.TrimSpace(expr[:index])
	filterExpr := strings.TrimSpace(expr[index+1:])
	filter, arg = splitFunction(filterExpr)
	if filter == "" {
		return "", "", "", false, fmt.Errorf("empty filter")
	}
	return base, filter, arg, true, nil
}

func splitFunction(expr string) (string, string) {
	open := strings.IndexByte(expr, '(')
	if open < 0 {
		return strings.TrimSpace(expr), ""
	}
	close := strings.LastIndexByte(expr, ')')
	if close < open {
		return "", ""
	}
	name := strings.TrimSpace(expr[:open])
	raw := strings.TrimSpace(expr[open+1 : close])
	if len(raw) >= 2 {
		if raw[0] == '"' && raw[len(raw)-1] == '"' {
			if value, err := strconv.Unquote(raw); err == nil {
				return name, value
			}
		}
		if raw[0] == '\'' && raw[len(raw)-1] == '\'' {
			return name, raw[1 : len(raw)-1]
		}
	}
	return name, raw
}
