package cardigann

import (
	"github.com/buzzqw/indexarr/internal/definition"
)

// Options controls the metadata written by ConvertDirectory. Convert and
// ConvertFile use the defaults documented on each field.
type Options struct {
	// Author is recorded in meta.author. Empty means "cardigann-import".
	Author string
	// Type overrides the Cardigann site type when it is missing or invalid.
	// Empty means: use the source type, falling back to "private".
	Type string
}

// Result is the outcome of converting one Cardigann definition.
//
// Definition and Body describe the same document: Definition is the parsed
// native definition and Body is the canonical YAML serialization used for
// Fingerprint. Hash is definition.Fingerprint(Body).
type Result struct {
	SourceID    string
	Name        string
	Definition  *definition.Definition
	Body        []byte
	Warnings    []string
	Unsupported []string
	Hash        string
}
