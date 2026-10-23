package engine

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"

	"github.com/buzzqw/indexarr/internal/definition"
)

// xmlNode is a deliberately small XML tree: enough for RSS, Atom and the
// Newznab/Torznab responses without pulling in a third-party XML library.
type xmlNode struct {
	Name     string
	Attr     []xml.Attr
	Children []*xmlNode
	Text     string
}

func (n *xmlNode) attribute(name string) string {
	if n == nil {
		return ""
	}
	for _, attr := range n.Attr {
		if strings.EqualFold(attr.Name.Local, name) {
			return attr.Value
		}
	}
	return ""
}

// textContent concatenates the character data of n and all descendants.
func (n *xmlNode) textContent() string {
	if n == nil {
		return ""
	}
	var out strings.Builder
	var walk func(*xmlNode)
	walk = func(current *xmlNode) {
		out.WriteString(current.Text)
		for _, child := range current.Children {
			walk(child)
		}
	}
	walk(n)
	return strings.TrimSpace(out.String())
}

func parseXML(body []byte) (*xmlNode, error) {
	decoder := xml.NewDecoder(bytes.NewReader(body))
	root := &xmlNode{Name: "#document"}
	stack := []*xmlNode{root}
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("xml: decode response: %w", err)
		}
		switch typed := token.(type) {
		case xml.StartElement:
			node := &xmlNode{Name: typed.Name.Local, Attr: append([]xml.Attr(nil), typed.Attr...)}
			parent := stack[len(stack)-1]
			parent.Children = append(parent.Children, node)
			stack = append(stack, node)
		case xml.EndElement:
			if len(stack) > 1 {
				stack = stack[:len(stack)-1]
			}
		case xml.CharData:
			stack[len(stack)-1].Text += string(typed)
		}
	}
	return root, nil
}

func extractXMLRows(body []byte, schema definition.Response, base *url.URL) ([]map[string]string, error) {
	root, err := parseXML(body)
	if err != nil {
		return nil, err
	}
	path := strings.TrimSpace(schema.Rows.Path)
	if path == "" {
		return nil, fmt.Errorf("xml: response rows path is required")
	}
	rows := lookupXMLNodes(root, path)
	out := make([]map[string]string, 0, len(rows))
	for _, row := range rows {
		values := make(map[string]string, len(schema.Fields))
		for name, field := range schema.Fields {
			fieldPath := field.Path
			if fieldPath == "" {
				fieldPath = name
			}
			raw := lookupXMLValue(row, fieldPath)
			raw, err = applyFilters(raw, field.Filters)
			if err != nil {
				return nil, fmt.Errorf("xml: field %q: %w", name, err)
			}
			values[name] = raw
		}
		out = append(out, values)
	}
	return out, nil
}

// lookupXMLNodes walks a dot path and returns every matching node. A numeric
// segment selects a child by index.
func lookupXMLNodes(start *xmlNode, path string) []*xmlNode {
	current := []*xmlNode{start}
	for _, rawToken := range strings.Split(path, ".") {
		token := strings.TrimSpace(rawToken)
		if token == "" || token == "#text" {
			continue
		}
		name, _ := splitXMLToken(token)
		var next []*xmlNode
		for _, node := range current {
			if index, err := strconv.Atoi(name); err == nil {
				if index >= 0 && index < len(node.Children) {
					next = append(next, node.Children[index])
				}
				continue
			}
			for _, child := range node.Children {
				if xmlNameMatches(child.Name, name) {
					next = append(next, child)
				}
			}
		}
		current = next
		if len(current) == 0 {
			return nil
		}
	}
	return current
}

// lookupXMLValue resolves a field path relative to a row. It supports
// "title", "title.#text", "enclosure@url" and "@attr" (attribute of the row).
func lookupXMLValue(row *xmlNode, path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if strings.HasPrefix(path, "@") {
		return row.attribute(strings.TrimPrefix(path, "@"))
	}

	tokens := strings.Split(path, ".")
	last := strings.TrimSpace(tokens[len(tokens)-1])
	if last == "#text" {
		nodes := lookupXMLNodes(row, strings.Join(tokens[:len(tokens)-1], "."))
		if len(nodes) == 0 {
			return ""
		}
		return nodes[0].textContent()
	}
	name, attr := splitXMLToken(last)
	parentPath := strings.Join(tokens[:len(tokens)-1], ".")

	nodes := lookupXMLNodes(row, parentPath)
	if len(nodes) == 0 {
		nodes = []*xmlNode{row}
	}
	var node *xmlNode
	for _, candidate := range nodes {
		if xmlNameMatches(candidate.Name, name) {
			node = candidate
			break
		}
		for _, child := range candidate.Children {
			if xmlNameMatches(child.Name, name) {
				node = child
				break
			}
		}
		if node != nil {
			break
		}
	}
	if node == nil {
		return ""
	}
	if attr != "" {
		return node.attribute(attr)
	}
	return node.textContent()
}

// splitXMLToken splits "enclosure@url" into ("enclosure", "url"). A bare
// "@url" yields ("", "url").
func splitXMLToken(token string) (string, string) {
	at := strings.LastIndexByte(token, '@')
	if at < 0 {
		return token, ""
	}
	return strings.TrimSpace(token[:at]), strings.TrimSpace(token[at+1:])
}

// xmlNameMatches compares element names ignoring an optional namespace
// prefix, so "torznab:attr" also matches a node whose local name is "attr".
func xmlNameMatches(nodeName, tokenName string) bool {
	local := func(name string) string {
		if colon := strings.LastIndexByte(name, ':'); colon >= 0 {
			return name[colon+1:]
		}
		return name
	}
	return strings.EqualFold(local(nodeName), local(tokenName))
}
