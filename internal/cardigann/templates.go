package cardigann

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var templatePattern = regexp.MustCompile(`\{\{\s*(.*?)\s*\}\}`)
var rangeDotPattern = regexp.MustCompile(`\{\{\s*\.\s*\}\}`)

// convertTemplate rewrites a Cardigann template into the deliberately small
// native template language. Only known variables are accepted: an unknown,
// dynamic or malformed expression is an error, never a silently empty value.
// The one intentional exception is control flow over categories: the branch
// or repeated parameter is approximated with a warning because the native
// language has no loops.
func (c *converter) convertTemplate(label, source string, critical bool) (string, error) {
	return c.convertTemplateMode(label, source, critical, false)
}

// convertTemplateMode is convertTemplate with a query-fragment mode used for
// Cardigann's special $raw input. In that mode a range over categories
// becomes a plain "key=value" query fragment.
func (c *converter) convertTemplateMode(label, source string, critical, queryMode bool) (string, error) {
	if !strings.Contains(source, "{{") {
		return source, nil
	}
	return c.rewriteTemplate(label, source, critical, queryMode)
}

type templateAction struct {
	start int
	end   int
	expr  string
}

func scanTemplateActions(source string) ([]templateAction, error) {
	if strings.Count(source, "{{") != strings.Count(source, "}}") {
		return nil, fmt.Errorf("malformed template expression")
	}
	matches := templatePattern.FindAllStringSubmatchIndex(source, -1)
	if len(matches) == 0 || strings.Count(source, "{{") != len(matches) {
		return nil, fmt.Errorf("malformed template expression")
	}
	out := make([]templateAction, 0, len(matches))
	for _, match := range matches {
		out = append(out, templateAction{
			start: match[0],
			end:   match[1],
			expr:  strings.TrimSpace(source[match[2]:match[3]]),
		})
	}
	return out, nil
}

// actionKind classifies one template action. The second return value is the
// condition expression for if/else-if actions.
func actionKind(expr string) (string, string) {
	trimmed := normaliseExpression(expr)
	lower := strings.ToLower(trimmed)
	switch {
	case lower == "end":
		return "end", ""
	case lower == "else":
		return "else", ""
	case strings.HasPrefix(lower, "else if "):
		return "else-if", strings.TrimSpace(trimmed[len("else if "):])
	case strings.HasPrefix(lower, "if "):
		return "if", strings.TrimSpace(trimmed[len("if "):])
	case strings.HasPrefix(lower, "range "):
		return "range", strings.TrimSpace(trimmed[len("range "):])
	case strings.HasPrefix(lower, "with "):
		return "with", strings.TrimSpace(trimmed[len("with "):])
	default:
		return "plain", trimmed
	}
}

func (c *converter) rewriteTemplate(label, source string, critical, queryMode bool) (string, error) {
	if !strings.Contains(source, "{{") {
		return source, nil
	}
	actions, err := scanTemplateActions(source)
	if err != nil {
		c.addUnsupported("malformed template")
		return "", fmt.Errorf("%s: malformed template expression", label)
	}

	var out strings.Builder
	last := 0
	for index := 0; index < len(actions); {
		action := actions[index]
		if action.start > last {
			out.WriteString(source[last:action.start])
		}
		kind, rest := actionKind(action.expr)
		switch kind {
		case "if":
			endIndex, elseIndex, elseKind, elseRest, findErr := findTemplateBlock(actions, index)
			if findErr != nil {
				return "", fmt.Errorf("%s: %v", label, findErr)
			}
			chooseThen, condErr := c.evaluateCondition(rest)
			if condErr != nil {
				c.addUnsupported("conditional " + rest)
				return "", fmt.Errorf("%s: request template: %w", label, condErr)
			}
			branch := ""
			if chooseThen {
				stop := len(source)
				if elseIndex >= 0 {
					stop = actions[elseIndex].start
				} else {
					stop = actions[endIndex].start
				}
				branch = source[action.end:stop]
			} else if elseIndex >= 0 {
				body := source[actions[elseIndex].end:actions[endIndex].start]
				if elseKind == "else-if" {
					branch = "{{ if " + elseRest + " }}" + body + "{{ end }}"
				} else {
					branch = body
				}
			}
			c.addWarning("conditional %q approximated to the %s branch", rest, branchName(chooseThen))
			rendered, renderErr := c.rewriteTemplate(label, branch, critical, queryMode)
			if renderErr != nil {
				return "", renderErr
			}
			out.WriteString(rendered)
			last = actions[endIndex].end
			index = endIndex + 1
		case "range":
			endIndex, _, _, _, findErr := findTemplateBlock(actions, index)
			if findErr != nil {
				return "", fmt.Errorf("%s: %v", label, findErr)
			}
			if !isCategoriesRange(rest) {
				c.addUnsupported("range " + rest)
				return "", fmt.Errorf("%s: request template: range over %q is not supported", label, rest)
			}
			body := source[action.end:actions[endIndex].start]
			approximated, approxErr := approximateCategoriesRange(body, queryMode)
			if approxErr != nil {
				c.addUnsupported("range .Categories")
				return "", fmt.Errorf("%s: request template: %w", label, approxErr)
			}
			c.addWarning("range over categories approximated; repeated parameters are collapsed")
			out.WriteString(approximated)
			last = actions[endIndex].end
			index = endIndex + 1
		case "else", "else-if", "end":
			return "", fmt.Errorf("%s: request template: unexpected %s", label, action.expr)
		case "with":
			return "", fmt.Errorf("%s: request template: with is not supported", label)
		default:
			converted, convertErr := c.convertExpression(action.expr)
			if convertErr != nil {
				kind := "request template"
				if !critical {
					kind = "optional template"
				}
				c.addUnsupported("template " + action.expr)
				return "", fmt.Errorf("%s: %s: %w", label, kind, convertErr)
			}
			out.WriteString(converted)
			last = action.end
			index++
		}
	}
	out.WriteString(source[last:])
	return out.String(), nil
}

func branchName(then bool) string {
	if then {
		return "then"
	}
	return "else"
}

func findTemplateBlock(actions []templateAction, start int) (end, elseIndex int, elseKind, elseRest string, err error) {
	elseIndex = -1
	depth := 1
	for index := start + 1; index < len(actions); index++ {
		kind, rest := actionKind(actions[index].expr)
		switch kind {
		case "if", "range", "with":
			depth++
		case "end":
			depth--
			if depth == 0 {
				return index, elseIndex, elseKind, elseRest, nil
			}
		case "else", "else-if":
			if depth == 1 && elseIndex < 0 {
				elseIndex = index
				elseKind = kind
				elseRest = rest
			}
		}
	}
	return 0, -1, "", "", fmt.Errorf("unclosed template block")
}

func isCategoriesRange(expr string) bool {
	trimmed := strings.TrimSpace(expr)
	return strings.EqualFold(trimmed, ".Categories") || strings.EqualFold(trimmed, ".Category")
}

// approximateCategoriesRange collapses a range body into one native
// expression. In query mode the body becomes a key=value fragment for $raw;
// otherwise it becomes a joined categories expression.
func approximateCategoriesRange(body string, queryMode bool) (string, error) {
	if strings.TrimSpace(body) == "" {
		return "", fmt.Errorf("empty categories range")
	}
	marker := "\x00"
	body = rangeDotPattern.ReplaceAllString(body, marker)
	if !strings.Contains(body, marker) {
		return "", fmt.Errorf("categories range does not use the current category")
	}
	if strings.Count(body, marker) > 1 {
		return "", fmt.Errorf("categories range uses the current category more than once")
	}
	if queryMode {
		return approximateQueryRange(body, marker)
	}
	return approximateTextRange(body, marker)
}

func approximateQueryRange(body, marker string) (string, error) {
	fragment := strings.Trim(strings.TrimSpace(body), "&")
	if fragment == marker {
		return "categories={{ categories }}", nil
	}
	index := strings.Index(fragment, marker)
	left := fragment[:index]
	right := fragment[index+len(marker):]
	if strings.HasSuffix(left, "[") {
		key := strings.TrimSuffix(left, "[")
		rest := strings.TrimPrefix(right, "]")
		value := strings.TrimPrefix(rest, "=")
		value = strings.Trim(value, "&")
		if value == "" {
			value = "1"
		}
		return strings.Trim(key, "&") + "=" + value, nil
	}
	if strings.HasSuffix(left, "=") {
		key := strings.Trim(strings.TrimSuffix(left, "="), "&")
		value := strings.Trim(right, "&")
		if value == "" {
			value = "{{ categories }}"
		}
		if key == "" {
			return "categories=" + value, nil
		}
		return key + "=" + value, nil
	}
	if strings.HasPrefix(right, "=") {
		key := strings.Trim(left, "&")
		value := strings.Trim(strings.TrimPrefix(right, "="), "&")
		if value == "" {
			value = "1"
		}
		if key == "" {
			return "categories=" + value, nil
		}
		// cat{{.}}=1 repeats the key once per category; keep one sample.
		return key + "=" + value, nil
	}
	key := strings.Trim(left, "&")
	if key != "" && strings.TrimSpace(right) == "" {
		// A repeated dynamic key such as c{{.}} has no single-value
		// equivalent; keep one key with the joined category ids.
		return key + "={{ categories }}", nil
	}
	rendered := strings.ReplaceAll(fragment, marker, "{{ categories }}")
	return rendered, nil
}

func approximateTextRange(body, marker string) (string, error) {
	trimmed := strings.TrimSpace(body)
	if trimmed == marker {
		return "{{ categories }}", nil
	}
	index := strings.Index(trimmed, marker)
	left := trimmed[:index]
	right := trimmed[index+len(marker):]
	if left == "" {
		separator := strings.TrimSpace(right)
		if separator == "" || separator == "," {
			return "{{ categories }}", nil
		}
		if len(separator) == 1 {
			return "{{ categories | join(" + strconv.Quote(separator) + ") }}", nil
		}
	}
	// Keep the literal prefix/suffix and join the ids with commas.
	return strings.ReplaceAll(body, marker, "{{ categories }}"), nil
}

func (c *converter) convertExpression(expr string) (string, error) {
	expr = normaliseExpression(expr)
	if expr == "" {
		return "", fmt.Errorf("empty expression")
	}

	if name, args, ok := splitTemplateCall(expr); ok {
		switch strings.ToLower(name) {
		case "join":
			parts := splitCallArguments(args)
			if len(parts) != 2 {
				return "", fmt.Errorf("join expects a variable and a separator")
			}
			_, isCategories, err := c.nativeVariable(parts[0])
			if err != nil {
				return "", err
			}
			if !isCategories {
				return "", fmt.Errorf("join is only supported on categories")
			}
			separator := unquoteTemplateString(parts[1])
			if separator == "" {
				separator = ","
			}
			return "{{ categories | join(" + strconv.Quote(separator) + ") }}", nil
		case "or":
			// The native language has no fallback expression. Use the first
			// operand the native language knows about.
			for _, part := range splitCallArguments(args) {
				nativeVariable := stripOuterParens(strings.TrimSpace(part))
				native, _, err := c.nativeVariable(nativeVariable)
				if err == nil {
					c.addWarning("or expression %q approximated to %q", expr, native)
					return "{{ " + native + " }}", nil
				}
			}
			return "", fmt.Errorf("or has no convertible operand")
		case "re_replace", "replace", "trim", "tolower", "toupper", "lower", "upper", "urldecode":
			parts := splitCallArguments(args)
			if len(parts) == 0 {
				return "", fmt.Errorf("function %q expects an operand", name)
			}
			native, _, err := c.nativeVariable(strings.TrimSpace(parts[0]))
			if err != nil {
				return "", err
			}
			c.addWarning("inline function %q approximated by its raw variable", name)
			return "{{ " + native + " }}", nil
		default:
			return "", fmt.Errorf("function %q is not supported", name)
		}
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
	case "imdb", "imdbid", "imdbidshort":
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

// splitTemplateCall recognises Go template function syntax such as
// `join .Categories ","`. The native language only supports join.
func splitTemplateCall(expr string) (name, args string, ok bool) {
	open := strings.IndexByte(expr, '(')
	if open >= 0 {
		// Function-call syntax has no space before the parenthesis. The
		// parenthesised form `or (a) (b)` must stay in the space-separated
		// branch below.
		close := strings.LastIndexByte(expr, ')')
		rawName := expr[:open]
		if close > open && strings.TrimSpace(rawName) == rawName {
			name = strings.TrimSpace(rawName)
			if name != "" && !strings.ContainsAny(name, " \t\"'") {
				return name, strings.TrimSpace(expr[open+1 : close]), true
			}
		}
	}
	fields := splitConditionFields(expr)
	if len(fields) < 2 || strings.HasPrefix(fields[0], ".") {
		return "", "", false
	}
	return fields[0], strings.Join(fields[1:], " "), true
}

func splitCallArguments(raw string) []string {
	var out []string
	var current strings.Builder
	depth := 0
	inSingle, inDouble := false, false
	for index := 0; index < len(raw); index++ {
		char := raw[index]
		switch char {
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
		case '(':
			if !inSingle && !inDouble {
				depth++
			}
		case ')':
			if !inSingle && !inDouble {
				depth--
			}
		case ',', ' ', '\t', '\n':
			if !inSingle && !inDouble && depth == 0 {
				if part := strings.TrimSpace(current.String()); part != "" {
					out = append(out, part)
				}
				current.Reset()
				continue
			}
		}
		current.WriteByte(char)
	}
	if part := strings.TrimSpace(current.String()); part != "" {
		out = append(out, part)
	}
	return out
}

func unquoteTemplateString(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if len(trimmed) >= 2 {
		if trimmed[0] == '"' && trimmed[len(trimmed)-1] == '"' {
			if value, err := strconv.Unquote(trimmed); err == nil {
				return value
			}
		}
		if trimmed[0] == '\'' && trimmed[len(trimmed)-1] == '\'' {
			return trimmed[1 : len(trimmed)-1]
		}
	}
	return trimmed
}

// evaluateCondition statically chooses one branch of a simple Go template
// condition. It recognises the patterns found in real definitions; anything
// else is an error so a request template never silently changes meaning.
func (c *converter) evaluateCondition(raw string) (bool, error) {
	expr := strings.TrimSpace(raw)
	if expr == "" {
		return false, fmt.Errorf("empty conditional")
	}
	expr = stripOuterParens(expr)
	fields := splitConditionFields(expr)
	if len(fields) == 0 {
		return false, fmt.Errorf("empty conditional")
	}
	switch strings.ToLower(fields[0]) {
	case "and":
		if len(fields) < 2 {
			return false, fmt.Errorf("and without operands")
		}
		for _, field := range fields[1:] {
			value, err := c.evaluateCondition(field)
			if err != nil {
				return false, err
			}
			if !value {
				return false, nil
			}
		}
		return true, nil
	case "or":
		if len(fields) < 2 {
			return false, fmt.Errorf("or without operands")
		}
		for _, field := range fields[1:] {
			value, err := c.evaluateCondition(field)
			if err != nil {
				return false, err
			}
			if value {
				return true, nil
			}
		}
		return false, nil
	case "not":
		if len(fields) != 2 {
			return false, fmt.Errorf("not expects one operand")
		}
		value, err := c.evaluateCondition(fields[1])
		return !value, err
	case "eq", "ne", "lt", "le", "gt", "ge":
		if len(fields) != 3 {
			return false, fmt.Errorf("%s expects two operands", fields[0])
		}
		left, err := c.conditionOperand(fields[1])
		if err != nil {
			return false, err
		}
		right, err := c.conditionOperand(fields[2])
		if err != nil {
			return false, err
		}
		switch strings.ToLower(fields[0]) {
		case "eq":
			return left == right, nil
		case "ne":
			return left != right, nil
		case "lt":
			return left < right, nil
		case "le":
			return left <= right, nil
		case "gt":
			return left > right, nil
		default:
			return left >= right, nil
		}
	default:
		value, err := c.conditionOperand(expr)
		if err != nil {
			return false, err
		}
		return truthy(value), nil
	}
}

func (c *converter) conditionOperand(raw string) (string, error) {
	value := stripOuterParens(strings.TrimSpace(raw))
	if value == "" {
		return "", fmt.Errorf("empty conditional operand")
	}
	if strings.HasPrefix(value, "\"") || strings.HasPrefix(value, "'") {
		return unquoteTemplateString(value), nil
	}
	if strings.HasPrefix(value, ".") {
		field := strings.TrimPrefix(value, ".")
		lower := strings.ToLower(field)
		switch lower {
		case "true":
			return "true", nil
		case "false":
			return "false", nil
		case "keywords", "categories", "category", "query", "page", "offset", "limit":
			return "true", nil
		}
		if strings.HasPrefix(lower, "query.") {
			key := strings.TrimSpace(field[len("query."):])
			if _, supported := queryVariable(key); supported {
				return "true", nil
			}
			// The native engine has no value for this query variable, so it
			// is always empty; choose the branch that does not use it.
			c.addWarning("conditional on unsupported query variable .Query.%s treated as false", key)
			return "false", nil
		}
		if strings.HasPrefix(lower, "result.") {
			return "true", nil
		}
		if strings.HasPrefix(lower, "config.") {
			key := strings.TrimSpace(field[len("config."):])
			if key == "" {
				return "", fmt.Errorf("empty .Config reference")
			}
			if native, ok := c.lookupSetting(key); ok {
				for _, setting := range c.settings {
					if setting.Name == native {
						return setting.Default, nil
					}
				}
			}
			// An undeclared setting still has a native counterpart; choose
			// the branch that keeps the useful part of the request.
			return "true", nil
		}
		return "", fmt.Errorf("unsupported conditional operand %q", value)
	}
	return value, nil
}

func truthy(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "0", "false", "no", "off", "null", "~":
		return false
	default:
		return true
	}
}

func stripOuterParens(value string) string {
	for len(value) >= 2 && value[0] == '(' && value[len(value)-1] == ')' {
		depth := 0
		balanced := true
		inSingle, inDouble := false, false
		for index := 0; index < len(value); index++ {
			switch value[index] {
			case '\'':
				if !inDouble {
					inSingle = !inSingle
				}
			case '"':
				if !inSingle {
					inDouble = !inDouble
				}
			case '(':
				if !inSingle && !inDouble {
					depth++
				}
			case ')':
				if !inSingle && !inDouble {
					depth--
					if depth == 0 && index != len(value)-1 {
						balanced = false
					}
				}
			}
		}
		if !balanced {
			break
		}
		value = strings.TrimSpace(value[1 : len(value)-1])
	}
	return value
}

func splitConditionFields(expr string) []string {
	var out []string
	var current strings.Builder
	depth := 0
	inSingle, inDouble := false, false
	flush := func() {
		if part := strings.TrimSpace(current.String()); part != "" {
			out = append(out, part)
		}
		current.Reset()
	}
	for index := 0; index < len(expr); index++ {
		char := expr[index]
		switch char {
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
		case '(':
			if !inSingle && !inDouble {
				depth++
			}
		case ')':
			if !inSingle && !inDouble {
				depth--
			}
		case ' ', '\t', '\n':
			if !inSingle && !inDouble && depth == 0 {
				flush()
				continue
			}
		}
		current.WriteByte(char)
	}
	flush()
	return out
}
