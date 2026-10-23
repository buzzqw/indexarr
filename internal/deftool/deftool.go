// Package deftool implements the operations behind the indexarr-def command.
//
// It validates native indexer definitions, formats them canonically, checks
// their offline fixtures and exposes the resulting values in a form suitable
// for both humans and scripts. It never executes a definition and never
// performs network I/O.
package deftool

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/buzzqw/indexarr/internal/definition"
	"gopkg.in/yaml.v3"
)

// Result is the outcome of validating one definition file.
type Result struct {
	Path string
	ID   string
	OK   bool
	Err  error
}

// MarshalJSON encodes the result as {path, ok, id, error}. The error is
// rendered as a string; a nil error becomes the empty string.
func (r Result) MarshalJSON() ([]byte, error) {
	message := ""
	if r.Err != nil {
		message = r.Err.Error()
	}
	return json.Marshal(struct {
		Path  string `json:"path"`
		OK    bool   `json:"ok"`
		ID    string `json:"id"`
		Error string `json:"error"`
	}{Path: r.Path, OK: r.OK, ID: r.ID, Error: message})
}

// FixtureResult is the outcome of checking one tests[].fixture path.
type FixtureResult struct {
	Path string
	OK   bool
	Size int64
	Err  error
}

// MarshalJSON encodes the fixture result as {path, ok, size, error}.
func (r FixtureResult) MarshalJSON() ([]byte, error) {
	message := ""
	if r.Err != nil {
		message = r.Err.Error()
	}
	return json.Marshal(struct {
		Path  string `json:"path"`
		OK    bool   `json:"ok"`
		Size  int64  `json:"size"`
		Error string `json:"error"`
	}{Path: r.Path, OK: r.OK, Size: r.Size, Error: message})
}

// ValidateOptions controls the extra checks applied while validating.
type ValidateOptions struct {
	// RejectDerived fails drafts produced by a conversion tool such as the
	// Cardigann converter. The built-in catalog uses it for
	// internal/catalog/definitions: derived drafts are writing aids and never
	// ship with the binary.
	RejectDerived bool
}

// ValidatePaths validates every YAML definition under the given files and
// directories. Directories are walked recursively in sorted order; hidden
// directories are skipped. Invalid definitions are reported as results, not
// as an error: callers decide how to present them.
func ValidatePaths(paths []string) []Result {
	return ValidatePathsWithOptions(paths, ValidateOptions{})
}

// ValidatePathsWithOptions is ValidatePaths with additional policy checks.
func ValidatePathsWithOptions(paths []string, opts ValidateOptions) []Result {
	var out []Result
	for _, target := range paths {
		info, err := os.Stat(target)
		if err != nil {
			out = append(out, Result{Path: target, Err: fmt.Errorf("stat: %w", err)})
			continue
		}
		if !info.IsDir() {
			out = append(out, ValidateFileWithOptions(target, opts))
			continue
		}
		files, err := definitionFiles(target)
		if err != nil {
			out = append(out, Result{Path: target, Err: err})
			continue
		}
		if len(files) == 0 {
			out = append(out, Result{Path: target, Err: errors.New("no .yaml or .yml files found")})
			continue
		}
		for _, file := range files {
			out = append(out, ValidateFileWithOptions(file, opts))
		}
	}
	return out
}

// ValidateFile parses and validates one definition file.
func ValidateFile(path string) Result {
	return ValidateFileWithOptions(path, ValidateOptions{})
}

// ValidateFileWithOptions is ValidateFile with additional policy checks.
func ValidateFileWithOptions(path string, opts ValidateOptions) Result {
	result := Result{Path: path}
	body, err := os.ReadFile(path)
	if err != nil {
		result.Err = fmt.Errorf("read: %w", err)
		return result
	}
	def, err := definition.Parse(body)
	if err != nil {
		result.Err = err
		return result
	}
	result.ID = def.Meta.ID
	if opts.RejectDerived && def.IsDerived() {
		result.Err = fmt.Errorf("derived draft (derived_from=%q, author=%q) cannot be in the catalog; rewrite it from observed fixtures",
			def.Meta.DerivedFrom, def.Meta.Author)
		return result
	}
	result.OK = true
	return result
}

// Format parses a definition and returns its canonical YAML rendering.
// Comments and the original key order are not preserved: the output is the
// serialization of the parsed definition.
func Format(body []byte) ([]byte, error) {
	def, err := definition.Parse(body)
	if err != nil {
		return nil, err
	}

	var buffer bytes.Buffer
	encoder := yaml.NewEncoder(&buffer)
	encoder.SetIndent(2)
	if err := encoder.Encode(def); err != nil {
		return nil, fmt.Errorf("definition: marshal YAML: %w", err)
	}
	if err := encoder.Close(); err != nil {
		return nil, fmt.Errorf("definition: marshal YAML: %w", err)
	}
	return buffer.Bytes(), nil
}

// CheckFixtures verifies that every tests[].fixture declared by the definition
// exists, is a regular file and is not empty. Fixtures are resolved relative
// to the definition file. It does not extract or execute anything.
func CheckFixtures(definitionPath string) ([]FixtureResult, error) {
	def, err := definition.Load(definitionPath)
	if err != nil {
		return nil, err
	}

	dir := filepath.Dir(definitionPath)
	out := make([]FixtureResult, 0, len(def.Tests))
	for _, test := range def.Tests {
		raw := strings.TrimSpace(test.Fixture)
		result := FixtureResult{Path: filepath.Join(dir, filepath.FromSlash(raw))}
		if raw == "" {
			result.Err = errors.New("fixture path is empty")
			out = append(out, result)
			continue
		}
		if !safeFixturePath(raw) {
			result.Err = errors.New("fixture must be a relative path without \"..\"")
			out = append(out, result)
			continue
		}

		info, err := os.Stat(result.Path)
		if err != nil {
			result.Err = fmt.Errorf("fixture: %w", err)
			out = append(out, result)
			continue
		}
		result.Size = info.Size()
		switch {
		case !info.Mode().IsRegular():
			result.Err = errors.New("fixture is not a regular file")
		case info.Size() == 0:
			result.Err = errors.New("fixture is empty")
		default:
			result.OK = true
		}
		out = append(out, result)
	}
	return out, nil
}

func definitionFiles(root string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(root, func(filePath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if filePath != root && strings.HasPrefix(entry.Name(), ".") {
				return fs.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		switch strings.ToLower(filepath.Ext(filePath)) {
		case ".yaml", ".yml":
			files = append(files, filePath)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk %s: %w", root, err)
	}
	sort.Strings(files)
	return files, nil
}

func safeFixturePath(raw string) bool {
	if raw == "" || strings.HasPrefix(raw, "~") || strings.HasPrefix(raw, `\`) {
		return false
	}
	if path.IsAbs(raw) || filepath.IsAbs(raw) || filepath.VolumeName(raw) != "" {
		return false
	}
	for _, part := range strings.FieldsFunc(raw, func(r rune) bool {
		return r == '/' || r == '\\'
	}) {
		if part == ".." {
			return false
		}
	}
	return true
}
