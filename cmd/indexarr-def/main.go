// Command indexarr-def validates, formats and inspects native indexarr
// definitions. The real work lives in internal/deftool so it can be tested
// without invoking the binary.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/buzzqw/indexarr/internal/definition"
	"github.com/buzzqw/indexarr/internal/deftool"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return 2
	}
	switch args[0] {
	case "-version", "--version", "version":
		fmt.Fprintf(stdout, "indexarr-def %s\n", version)
		return 0
	case "-h", "--help", "help":
		usage(stdout)
		return 0
	case "validate":
		return runValidate(args[1:], stdout, stderr)
	case "schema":
		return runSchema(args[1:], stdout, stderr)
	case "fmt":
		return runFormat(args[1:], stdout, stderr)
	case "fingerprint":
		return runFingerprint(args[1:], stdout, stderr)
	case "check-fixtures":
		return runCheckFixtures(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "indexarr-def: unknown command %q\n", args[0])
		usage(stderr)
		return 2
	}
}

func usage(out io.Writer) {
	fmt.Fprint(out, `usage: indexarr-def <command> [flags]

commands:
  validate [flags] <file-or-dir>...  parse and validate definitions
  schema                            print the v1 JSON Schema
  fmt [-w] <file>                   print or rewrite canonical YAML
  fingerprint <file>                print the SHA-256 of the raw file
  check-fixtures <file>             verify tests[].fixture files exist

flags:
  -json      validate: emit a machine-readable array
  -w         fmt: rewrite the file in place
  -version   print the version and exit
`)
}

func runValidate(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("validate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	asJSON := flags.Bool("json", false, "emit a machine-readable JSON array")
	flags.Usage = func() {
		fmt.Fprintln(stderr, "usage: indexarr-def validate [-json] <file-or-dir>...")
	}
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() == 0 {
		flags.Usage()
		return 2
	}

	results := deftool.ValidatePaths(flags.Args())
	failed := false
	for _, result := range results {
		if !result.OK {
			failed = true
		}
	}

	if *asJSON {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(results); err != nil {
			fmt.Fprintf(stderr, "indexarr-def: encode results: %v\n", err)
			return 2
		}
	} else {
		for _, result := range results {
			if !result.OK {
				fmt.Fprintf(stdout, "FAIL %s: %v\n", result.Path, result.Err)
				continue
			}
			def, err := definition.Load(result.Path)
			if err != nil {
				fmt.Fprintf(stdout, "FAIL %s: %v\n", result.Path, err)
				failed = true
				continue
			}
			fmt.Fprintf(stdout, "ok %s (id=%s, schema=%d, %d settings, %d categories)\n",
				result.Path, def.Meta.ID, def.Schema, len(def.Settings), len(def.Categories))
		}
	}
	if failed {
		return 1
	}
	return 0
}

func runSchema(args []string, stdout, stderr io.Writer) int {
	if len(args) != 0 {
		fmt.Fprintln(stderr, "usage: indexarr-def schema")
		return 2
	}
	body := definition.JSONSchema()
	if _, err := stdout.Write(body); err != nil {
		fmt.Fprintf(stderr, "indexarr-def: write schema: %v\n", err)
		return 2
	}
	if len(body) == 0 || body[len(body)-1] != '\n' {
		fmt.Fprintln(stdout)
	}
	return 0
}

func runFormat(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("fmt", flag.ContinueOnError)
	flags.SetOutput(stderr)
	write := flags.Bool("w", false, "rewrite the file in place")
	flags.Usage = func() {
		fmt.Fprintln(stderr, "usage: indexarr-def fmt [-w] <file>")
	}
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 1 {
		flags.Usage()
		return 2
	}

	filePath := flags.Arg(0)
	body, err := os.ReadFile(filePath)
	if err != nil {
		fmt.Fprintf(stderr, "indexarr-def: read %s: %v\n", filePath, err)
		return 1
	}
	formatted, err := deftool.Format(body)
	if err != nil {
		fmt.Fprintf(stderr, "FAIL %s: %v\n", filePath, err)
		return 1
	}
	if !*write {
		if _, err := stdout.Write(formatted); err != nil {
			fmt.Fprintf(stderr, "indexarr-def: write: %v\n", err)
			return 2
		}
		return 0
	}

	mode := os.FileMode(0o644)
	if info, err := os.Stat(filePath); err == nil {
		mode = info.Mode().Perm()
	}
	if err := os.WriteFile(filePath, formatted, mode); err != nil {
		fmt.Fprintf(stderr, "indexarr-def: write %s: %v\n", filePath, err)
		return 1
	}
	return 0
}

func runFingerprint(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "usage: indexarr-def fingerprint <file>")
		return 2
	}
	body, err := os.ReadFile(args[0])
	if err != nil {
		fmt.Fprintf(stderr, "indexarr-def: read %s: %v\n", args[0], err)
		return 1
	}
	fmt.Fprintln(stdout, definition.Fingerprint(body))
	return 0
}

func runCheckFixtures(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "usage: indexarr-def check-fixtures <file>")
		return 2
	}
	results, err := deftool.CheckFixtures(args[0])
	if err != nil {
		fmt.Fprintf(stdout, "FAIL %s: %v\n", args[0], err)
		return 1
	}
	if len(results) == 0 {
		fmt.Fprintf(stdout, "ok %s (no fixtures declared)\n", args[0])
		return 0
	}

	failed := false
	for _, result := range results {
		if result.OK {
			fmt.Fprintf(stdout, "ok %s (%d bytes)\n", result.Path, result.Size)
			continue
		}
		failed = true
		fmt.Fprintf(stdout, "FAIL %s: %v\n", result.Path, result.Err)
	}
	if failed {
		return 1
	}
	return 0
}
