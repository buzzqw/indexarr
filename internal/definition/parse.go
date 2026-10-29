package definition

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"

	"gopkg.in/yaml.v3"
)

// Parse decodes and validates one YAML definition document.
//
// Decoding is strict: unknown keys are rejected so that typos in a definition
// are reported instead of silently ignored. Errors returned by the YAML
// decoder carry line information from yaml.v3.
func Parse(body []byte) (*Definition, error) {
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, errors.New("definition: empty document")
	}

	dec := yaml.NewDecoder(bytes.NewReader(body))
	dec.KnownFields(true)

	var def Definition
	if err := dec.Decode(&def); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errors.New("definition: empty document")
		}
		return nil, fmt.Errorf("definition: YAML: %w", err)
	}

	var extra yaml.Node
	err := dec.Decode(&extra)
	switch {
	case err == nil:
		return nil, fmt.Errorf("definition: multiple YAML documents are not supported (second document at line %d)", extra.Line)
	case !errors.Is(err, io.EOF):
		return nil, fmt.Errorf("definition: YAML: %w", err)
	}

	if err := def.Validate(); err != nil {
		return nil, err
	}
	def.addCookieAlternative()
	return &def, nil
}

// Load reads and parses a definition from path.
func Load(path string) (*Definition, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("definition: read %s: %w", path, err)
	}
	def, err := Parse(body)
	if err != nil {
		return nil, fmt.Errorf("definition %s: %w", path, err)
	}
	return def, nil
}

// Fingerprint returns the lowercase hexadecimal SHA-256 of the raw definition
// bytes, used to detect changes in stored definitions.
func Fingerprint(body []byte) string {
	return fmt.Sprintf("%x", sha256.Sum256(body))
}
