package definition

import _ "embed"

//go:embed schema.json
var schemaJSON []byte

// JSONSchema returns the draft 2020-12 JSON Schema for definition schema v1.
// The returned slice is a copy and may be modified by the caller.
func JSONSchema() []byte {
	out := make([]byte, len(schemaJSON))
	copy(out, schemaJSON)
	return out
}
