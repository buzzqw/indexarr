// Command gensupported writes docs/SUPPORTED.md, the generated list of every
// source the catalog ships.
//
// It is run by "go generate ./internal/catalog" from the catalog directory and
// uses the same parsing and validation path as the application, so the list
// can never describe a definition the catalog itself would reject.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/buzzqw/indexarr/internal/catalog"
)

func main() {
	out := flag.String("out", "../../docs/SUPPORTED.md", "output file")
	flag.Parse()

	entries, err := catalog.Builtins()
	if err != nil {
		fmt.Fprintf(os.Stderr, "gensupported: load catalog: %v\n", err)
		os.Exit(1)
	}
	body := catalog.RenderSupportedDocs(entries)
	if err := os.WriteFile(*out, body, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "gensupported: write %s: %v\n", *out, err)
		os.Exit(1)
	}
}
