// Command benchseed creates a disposable indexarr database pre-filled with N
// synthetic releases. It is used by scripts/bench.sh and never shipped in the
// server binary. The API key is written to stdout, one line, so the shell
// harness can capture it; progress and timing go to stderr.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/buzzqw/indexarr/internal/perf"
)

func main() {
	dbPath := flag.String("db", "", "database path to create")
	count := flag.Int("n", 10000, "number of releases to generate")
	key := flag.String("key", perf.DefaultAPIKey, "Torznab API key to store")
	flag.Parse()
	if *dbPath == "" {
		fmt.Fprintln(os.Stderr, "benchseed: -db is required")
		os.Exit(2)
	}
	started := time.Now()
	if err := perf.SeedFile(context.Background(), *dbPath, perf.Options{
		Releases: *count,
		APIKey:   *key,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "benchseed: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "benchseed: %d releases in %s\n", *count, time.Since(started).Round(time.Millisecond))
	fmt.Println(*key)
}
