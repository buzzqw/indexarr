package web

import (
	"bytes"
	"fmt"
	htmlparser "golang.org/x/net/html"
	"net/http"
	"regexp"
	"strings"

	"github.com/andybalholm/cascadia"
	"github.com/buzzqw/indexarr/internal/engine"
)

const (
	// A pasted page is bounded: the tool is a diagnostic aid, not a way to
	// make the daemon parse an unbounded document from a form post.
	definitionDebugMaxHTML = 1 << 20
	definitionDebugMaxRows = 10
)

type definitionDebugPage struct {
	base
	HTML          string
	RowSelector   string
	FieldSelector string
	Attribute     string
	Pattern       string
	Result        *definitionDebugResult
}

type definitionDebugResult struct {
	RowCount  int
	Truncated bool
	NoRows    bool
	ErrorKey  string
	ErrorArg  string
	Error     string // translated in the handler, ready to render
	Rows      []definitionDebugRow
}

type definitionDebugRow struct {
	Number int
	Value  string
	Match  bool
	Groups []string
}

func (s *Server) definitionDebug(w http.ResponseWriter, r *http.Request) {
	data := definitionDebugPage{base: s.newBaseT(r, "defDebug.title", "definitions")}
	if r.Method == http.MethodPost {
		data.HTML = r.FormValue("html")
		data.RowSelector = r.FormValue("row_selector")
		data.FieldSelector = r.FormValue("field_selector")
		data.Attribute = r.FormValue("attribute")
		data.Pattern = r.FormValue("pattern")
		result := runDefinitionDebug(data.HTML, data.RowSelector, data.FieldSelector, data.Attribute, data.Pattern)
		if result.ErrorKey != "" {
			if result.ErrorArg != "" {
				result.Error = s.translate(r, result.ErrorKey, result.ErrorArg)
			} else {
				result.Error = s.translate(r, result.ErrorKey)
			}
		}
		data.Result = &result
	}
	s.render(w, r, "definition_debug", data, http.StatusOK)
}

// runDefinitionDebug parses the pasted page with the same selector and regex
// implementations the engine uses. It returns translation keys instead of
// English prose so the result reads in the operator's language.
func runDefinitionDebug(body, rowSelector, fieldSelector, attribute, pattern string) definitionDebugResult {
	result := definitionDebugResult{}
	if strings.TrimSpace(body) == "" || strings.TrimSpace(rowSelector) == "" {
		result.ErrorKey = "defDebug.errorEmpty"
		return result
	}
	if len(body) > definitionDebugMaxHTML {
		result.ErrorKey = "defDebug.errorTooLarge"
		result.ErrorArg = fmt.Sprint(definitionDebugMaxHTML)
		return result
	}

	rowsSelector, err := engine.CompileSelector(strings.TrimSpace(rowSelector))
	if err != nil {
		result.ErrorKey = "defDebug.errorRowSelector"
		result.ErrorArg = err.Error()
		return result
	}
	var fieldSelectorCompiled cascadia.Selector
	if trimmed := strings.TrimSpace(fieldSelector); trimmed != "" {
		fieldSelectorCompiled, err = engine.CompileSelector(trimmed)
		if err != nil {
			result.ErrorKey = "defDebug.errorFieldSelector"
			result.ErrorArg = err.Error()
			return result
		}
	}
	var re *regexp.Regexp
	if trimmed := strings.TrimSpace(pattern); trimmed != "" {
		re, err = engine.CompileRegex(trimmed)
		if err != nil {
			result.ErrorKey = "defDebug.errorRegex"
			result.ErrorArg = err.Error()
			return result
		}
	}

	document, err := htmlparser.Parse(bytes.NewReader([]byte(body)))
	if err != nil {
		result.ErrorKey = "defDebug.errorHTML"
		result.ErrorArg = err.Error()
		return result
	}
	nodes := cascadia.QueryAll(document, rowsSelector)
	result.RowCount = len(nodes)
	result.NoRows = len(nodes) == 0
	result.Truncated = len(nodes) > definitionDebugMaxRows
	if result.Truncated {
		nodes = nodes[:definitionDebugMaxRows]
	}

	attr := strings.TrimSpace(attribute)
	result.Rows = make([]definitionDebugRow, 0, len(nodes))
	for index, node := range nodes {
		target := node
		if fieldSelectorCompiled != nil {
			target = cascadia.Query(node, fieldSelectorCompiled)
		}
		value := ""
		if target != nil {
			if attr == "" || strings.EqualFold(attr, "text") {
				value = definitionDebugNodeText(target)
			} else {
				value = definitionDebugAttr(target, attr)
			}
		}
		row := definitionDebugRow{Number: index + 1, Value: value}
		if re != nil {
			matches := re.FindStringSubmatch(value)
			row.Match = matches != nil
			if row.Match && len(matches) > 1 {
				row.Groups = matches[1:]
			}
		}
		result.Rows = append(result.Rows, row)
	}
	return result
}

func definitionDebugNodeText(node *htmlparser.Node) string {
	if node == nil {
		return ""
	}
	var out strings.Builder
	var walk func(*htmlparser.Node)
	walk = func(current *htmlparser.Node) {
		if current.Type == htmlparser.TextNode {
			out.WriteString(current.Data)
		}
		for child := current.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(node)
	return strings.TrimSpace(out.String())
}

func definitionDebugAttr(node *htmlparser.Node, name string) string {
	if node == nil {
		return ""
	}
	for _, attribute := range node.Attr {
		if strings.EqualFold(attribute.Key, name) {
			return strings.TrimSpace(attribute.Val)
		}
	}
	return ""
}
