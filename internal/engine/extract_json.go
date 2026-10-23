package engine

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/buzzqw/indexarr/internal/definition"
)

// decodeJSON decodes body preserving numbers as json.Number so integer fields
// are not mangled through float64.
func decodeJSON(body []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var root any
	if err := decoder.Decode(&root); err != nil {
		return nil, fmt.Errorf("json: decode response: %w", err)
	}
	return root, nil
}

// extractJSONRows follows response.rows.path (empty means the document root)
// and returns each array element as a raw field map.
func extractJSONRows(body []byte, schema definition.Response, base *url.URL) ([]map[string]string, error) {
	root, err := decodeJSON(body)
	if err != nil {
		return nil, err
	}

	rowsValue := walkJSONPath(root, schema.Rows.Path)
	array, ok := rowsValue.([]any)
	if !ok {
		if rowsValue == nil {
			return nil, nil
		}
		return nil, fmt.Errorf("json: rows path %q is not an array", schema.Rows.Path)
	}

	out := make([]map[string]string, 0, len(array))
	for _, element := range array {
		values := make(map[string]string, len(schema.Fields))
		for name, field := range schema.Fields {
			path := field.Path
			if path == "" {
				path = name
			}
			raw := jsonValueString(walkJSONPath(element, path))
			raw, err = applyFilters(raw, field.Filters)
			if err != nil {
				return nil, fmt.Errorf("json: field %q: %w", name, err)
			}
			values[name] = raw
		}
		out = append(out, values)
	}
	return out, nil
}

// walkJSONPath resolves a dot path such as "data.items.0.attributes.name".
// Numeric segments index arrays. Missing paths return nil.
func walkJSONPath(root any, path string) any {
	path = strings.TrimSpace(path)
	path = strings.TrimPrefix(path, "$")
	path = strings.TrimPrefix(path, ".")
	if path == "" || path == "$" {
		return root
	}

	current := root
	for _, token := range strings.Split(path, ".") {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}
		switch node := current.(type) {
		case map[string]any:
			current = node[token]
		case []any:
			index, err := strconv.Atoi(token)
			if err != nil || index < 0 || index >= len(node) {
				return nil
			}
			current = node[index]
		default:
			return nil
		}
		if current == nil {
			return nil
		}
	}
	return current
}

func jsonValueString(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case json.Number:
		return typed.String()
	case bool:
		return strconv.FormatBool(typed)
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case []any, map[string]any:
		encoded, err := json.Marshal(typed)
		if err != nil {
			return ""
		}
		return string(encoded)
	default:
		return fmt.Sprintf("%v", typed)
	}
}
