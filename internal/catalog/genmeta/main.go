// Command genmeta refreshes the compact manager metadata snapshots used by the
// site catalog generator.
//
// It is a networked command and is never run by "go generate". Run it by hand
// after a manager inventory changes:
//
//	JACKETT_URL=... JACKETT_API_KEY=... \
//	PROWLARR_URL=... PROWLARR_API_KEY=... \
//	go run ./internal/catalog/genmeta
//
// Only GET requests are made. Keys are read from the environment, never from
// flags, so they do not appear in the process list. The snapshots contain no
// credentials and no Cardigann definition.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/buzzqw/indexarr/internal/importers"
)

type snapshot struct {
	Schema   int                        `json:"schema"`
	Source   string                     `json:"source"`
	Fetched  string                     `json:"fetched"`
	Endpoint string                     `json:"endpoint"`
	Sites    []importers.CatalogIndexer `json:"sites"`
}

func main() {
	out := flag.String("out", "internal/catalog/metadata", "output directory")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	client := &http.Client{Timeout: 45 * time.Second}

	now := time.Now().UTC().Format("2006-01-02")
	written := 0

	if base, key := os.Getenv("JACKETT_URL"), os.Getenv("JACKETT_API_KEY"); base != "" {
		sites, err := importers.FetchJackettCatalog(ctx, base, key, client)
		if err != nil {
			fmt.Fprintf(os.Stderr, "genmeta: jackett: %v\n", err)
			os.Exit(1)
		}
		if err := write(filepath.Join(*out, "jackett.json"), snapshot{
			Schema:   1,
			Source:   "jackett",
			Fetched:  now,
			Endpoint: "GET /api/v2.0/indexers/all/results/torznab/api?t=indexers&configured=false",
			Sites:    sites,
		}); err != nil {
			fmt.Fprintf(os.Stderr, "genmeta: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("jackett: %d sites\n", len(sites))
		written++
	}

	if base, key := os.Getenv("PROWLARR_URL"), os.Getenv("PROWLARR_API_KEY"); base != "" {
		sites, err := importers.FetchProwlarrCatalog(ctx, base, key, client)
		if err != nil {
			fmt.Fprintf(os.Stderr, "genmeta: prowlarr: %v\n", err)
			os.Exit(1)
		}
		if err := write(filepath.Join(*out, "prowlarr.json"), snapshot{
			Schema:   1,
			Source:   "prowlarr",
			Fetched:  now,
			Endpoint: "GET /api/v1/indexer",
			Sites:    sites,
		}); err != nil {
			fmt.Fprintf(os.Stderr, "genmeta: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("prowlarr: %d sites\n", len(sites))
		written++
	}

	if written == 0 {
		fmt.Fprintln(os.Stderr, "genmeta: set JACKETT_URL/JACKETT_API_KEY or PROWLARR_URL/PROWLARR_API_KEY")
		os.Exit(2)
	}
}

func write(path string, value snapshot) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	body, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	body = append(body, '\n')
	return os.WriteFile(path, body, 0o644)
}
