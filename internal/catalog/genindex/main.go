// Command genindex writes the compact site catalog embedded in the binary.
//
// It is run by "go generate ./internal/catalog" from the catalog directory and
// uses the same parsing and validation path as the application, so the index
// can never describe a definition that the catalog itself would reject.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/buzzqw/indexarr/internal/catalog"
)

func main() {
	out := flag.String("out", "index.json", "output file")
	flag.Parse()

	entries, err := catalog.Builtins()
	if err != nil {
		fmt.Fprintf(os.Stderr, "genindex: load catalog: %v\n", err)
		os.Exit(1)
	}
	body, err := json.MarshalIndent(catalog.BuildIndex(entries), "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "genindex: encode index: %v\n", err)
		os.Exit(1)
	}
	body = append(body, '\n')
	if err := os.WriteFile(*out, body, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "genindex: write %s: %v\n", *out, err)
		os.Exit(1)
	}
}
