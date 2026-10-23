package engine

import (
	"bytes"
	"fmt"
	"net/url"
	"strings"

	"github.com/andybalholm/cascadia"
	"golang.org/x/net/html"

	"github.com/buzzqw/indexarr/internal/definition"
)

// extractHTMLRows parses an HTML response and returns one raw value map per
// row. Selector compile errors are returned (never panicked) so a malformed
// definition cannot crash the process.
func extractHTMLRows(body []byte, schema definition.Response, base *url.URL) ([]map[string]string, error) {
	document, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("html: parse response: %w", err)
	}

	var rows []*html.Node
	for _, raw := range schema.Rows.Selector {
		selector, err := cascadia.Compile(raw)
		if err != nil {
			return nil, fmt.Errorf("html: invalid row selector %q: %w", raw, err)
		}
		nodes := cascadia.QueryAll(document, selector)
		if len(nodes) > 0 {
			rows = nodes
			break
		}
	}
	if len(schema.Rows.Selector) == 0 {
		return nil, fmt.Errorf("html: response rows selector is required")
	}

	out := make([]map[string]string, 0, len(rows))
	for _, row := range rows {
		values := make(map[string]string, len(schema.Fields))
		for name, field := range schema.Fields {
			raw, err := extractHTMLField(row, field)
			if err != nil {
				return nil, fmt.Errorf("html: field %q: %w", name, err)
			}
			raw, err = applyFilters(raw, field.Filters)
			if err != nil {
				return nil, fmt.Errorf("html: field %q: %w", name, err)
			}
			values[name] = raw
		}
		out = append(out, values)
	}
	return out, nil
}

func extractHTMLField(row *html.Node, field definition.Field) (string, error) {
	for _, raw := range field.Selector {
		selector, err := cascadia.Compile(raw)
		if err != nil {
			return "", fmt.Errorf("invalid selector %q: %w", raw, err)
		}
		node := cascadia.Query(row, selector)
		if node == nil {
			continue
		}
		if field.Attr == "" || strings.EqualFold(field.Attr, "text") {
			return nodeText(node), nil
		}
		return htmlAttr(node, field.Attr), nil
	}
	return "", nil
}

func htmlAttr(node *html.Node, name string) string {
	for _, attr := range node.Attr {
		if strings.EqualFold(attr.Key, name) {
			return attr.Val
		}
	}
	return ""
}

// nodeText returns the trimmed text content of node, including descendants.
func nodeText(node *html.Node) string {
	if node == nil {
		return ""
	}
	var out strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			out.WriteString(n.Data)
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(node)
	return strings.TrimSpace(out.String())
}

// extractHTMLValue extracts a single value from a whole HTML document. It is
// used by login extractors and the details-page download heuristic.
func extractHTMLValue(body []byte, selector, attr string) (string, error) {
	document, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("html: parse response: %w", err)
	}
	compiled, err := cascadia.Compile(selector)
	if err != nil {
		return "", fmt.Errorf("html: invalid selector %q: %w", selector, err)
	}
	node := cascadia.Query(document, compiled)
	if node == nil {
		return "", nil
	}
	if attr == "" || strings.EqualFold(attr, "text") {
		return nodeText(node), nil
	}
	return htmlAttr(node, attr), nil
}

// extractHTMLValues returns every value matched by selector, in document
// order. It is used by a download target with all: true.
func extractHTMLValues(body []byte, selector, attr string) ([]string, error) {
	document, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("html: parse response: %w", err)
	}
	compiled, err := cascadia.Compile(selector)
	if err != nil {
		return nil, fmt.Errorf("html: invalid selector %q: %w", selector, err)
	}
	nodes := cascadia.QueryAll(document, compiled)
	out := make([]string, 0, len(nodes))
	for _, node := range nodes {
		value := ""
		if attr == "" || strings.EqualFold(attr, "text") {
			value = nodeText(node)
		} else {
			value = htmlAttr(node, attr)
		}
		if strings.TrimSpace(value) != "" {
			out = append(out, value)
		}
	}
	return out, nil
}

// htmlSelectorPresent reports whether selector matches at least one node.
func htmlSelectorPresent(body []byte, selector string) (bool, error) {
	document, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return false, fmt.Errorf("html: parse response: %w", err)
	}
	compiled, err := cascadia.Compile(selector)
	if err != nil {
		return false, fmt.Errorf("html: invalid selector %q: %w", selector, err)
	}
	return cascadia.Query(document, compiled) != nil, nil
}

// firstHTMLLink returns the first anchor href satisfying match.
func firstHTMLLink(body []byte, match func(href string) bool) (string, error) {
	document, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("html: parse response: %w", err)
	}
	selector, err := cascadia.Compile("a[href]")
	if err != nil {
		return "", err
	}
	for _, node := range cascadia.QueryAll(document, selector) {
		href := strings.TrimSpace(htmlAttr(node, "href"))
		if href != "" && match(href) {
			return href, nil
		}
	}
	return "", nil
}
