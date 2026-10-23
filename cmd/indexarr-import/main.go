// Command indexarr-import reads an existing Jackett or Prowlarr installation
// and prints a redacted import plan. With -apply it creates Torznab bridge
// sources in a disposable indexarr database, leaving the original
// Jackett/Prowlarr untouched.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/buzzqw/indexarr/internal/importers"
	"github.com/buzzqw/indexarr/internal/store"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	flags := flag.NewFlagSet("indexarr-import", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	jackettDir := flags.String("jackett", "", "Jackett data directory or ServerConfig.json")
	prowlarrURL := flags.String("prowlarr", "", "Prowlarr base URL, e.g. http://127.0.0.1:9696")
	prowlarrKey := flags.String("prowlarr-key", "", "Prowlarr API key")
	dbPath := flags.String("db", "", "target indexarr SQLite database for -apply")
	apply := flags.Bool("apply", false, "write bridge sources into -db (default: print the plan)")
	jsonOut := flags.Bool("json", false, "print machine-readable output")
	timeout := flags.Duration("timeout", 20*time.Second, "network timeout for Prowlarr")
	flags.Usage = func() {
		fmt.Fprint(os.Stderr, `usage: indexarr-import (-jackett DIR | -prowlarr URL -prowlarr-key KEY) [flags]

Reads an existing Jackett or Prowlarr configuration without modifying it.
With -apply and -db it creates Torznab bridge sources in a disposable
indexarr database.

flags:
  -jackett DIR        Jackett data directory or ServerConfig.json
  -prowlarr URL       Prowlarr base URL
  -prowlarr-key KEY   Prowlarr API key
  -db PATH            target indexarr SQLite database
  -apply              write sources (otherwise only print the plan)
  -json               machine-readable output
  -timeout 20s        Prowlarr HTTP timeout
`)
	}
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if (*jackettDir == "") == (*prowlarrURL == "") {
		flags.Usage()
		return 2
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	var plan importers.Plan
	var err error
	switch {
	case *jackettDir != "":
		plan, err = importers.FromJackettConfig(ctx, *jackettDir)
	case *prowlarrURL != "":
		if strings.TrimSpace(*prowlarrKey) == "" {
			fmt.Fprintln(os.Stderr, "indexarr-import: -prowlarr-key is required with -prowlarr")
			return 2
		}
		plan, err = importers.FromProwlarr(ctx, *prowlarrURL, *prowlarrKey, nil)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "indexarr-import: %v\n", err)
		return 1
	}

	// Apply before redaction: the importer needs the real API key to create
	// working bridge sources. The printed plan never contains it.
	if *apply {
		if *dbPath == "" {
			fmt.Fprintln(os.Stderr, "indexarr-import: -apply requires -db")
			return 2
		}
		ids, applyErr := applyPlan(ctx, plan, *dbPath, *prowlarrURL, *prowlarrKey)
		if applyErr != nil {
			fmt.Fprintf(os.Stderr, "indexarr-import: %v\n", applyErr)
			return 1
		}
		fmt.Printf("applied %d sources to %s\n", len(ids), *dbPath)
	}

	printPlan(redactPlan(plan), *jsonOut)
	return 0
}

// redactPlan removes the secrets that are only needed to talk to the original
// service. The plan summary never prints them.
func redactPlan(plan importers.Plan) importers.Plan {
	for i := range plan.Indexers {
		plan.Indexers[i].APIKey = ""
		plan.Indexers[i].Password = ""
		if plan.Indexers[i].Username != "" {
			plan.Indexers[i].Username = "***"
		}
	}
	for i := range plan.Applications {
		plan.Applications[i].APIKey = ""
	}
	for i := range plan.Proxies {
		plan.Proxies[i].Password = ""
		if plan.Proxies[i].Username != "" {
			plan.Proxies[i].Username = "***"
		}
	}
	return plan
}

func printPlan(plan importers.Plan, asJSON bool) {
	if asJSON {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(plan); err != nil {
			fmt.Fprintf(os.Stderr, "indexarr-import: encode plan: %v\n", err)
		}
		return
	}
	fmt.Printf("source: %s\n", plan.Source)
	fmt.Printf("indexers: %d, applications: %d, proxies: %d, warnings: %d\n",
		len(plan.Indexers), len(plan.Applications), len(plan.Proxies), len(plan.Warnings))
	for _, indexer := range plan.Indexers {
		fmt.Printf("  %-40s protocol=%-7s enabled=%t categories=%s hint=%s\n",
			indexer.Name, indexer.Protocol, indexer.Enabled,
			joinInts(indexer.Categories), indexer.DefinitionHint)
	}
	for _, app := range plan.Applications {
		fmt.Printf("  app %-30s kind=%s sync=%s\n", app.Name, app.Kind, app.SyncLevel)
	}
	for _, warning := range plan.Warnings {
		fmt.Printf("  warning: %s\n", warning)
	}
}

func applyPlan(ctx context.Context, plan importers.Plan, dbPath, prowlarrBase, prowlarrKey string) ([]int64, error) {
	st, err := store.Open(dbPath)
	if err != nil {
		return nil, err
	}
	defer st.Close()
	existing, err := st.ListSources(ctx)
	if err != nil {
		return nil, err
	}
	names := make(map[string]bool, len(existing))
	for _, source := range existing {
		names[strings.ToLower(source.Name)] = true
	}
	unique := func(name string) string {
		base := strings.TrimSpace(name)
		if base == "" {
			base = "imported"
		}
		candidate := base
		for i := 2; names[strings.ToLower(candidate)]; i++ {
			candidate = base + " (" + strconv.Itoa(i) + ")"
		}
		names[strings.ToLower(candidate)] = true
		return candidate
	}

	var ids []int64
	if plan.Source == "prowlarr" {
		// One aggregate Prowlarr source is the bridge that Prowlarr itself
		// documents: indexarr asks Prowlarr for every configured indexer.
		if strings.TrimSpace(prowlarrBase) == "" || strings.TrimSpace(prowlarrKey) == "" {
			return nil, fmt.Errorf("prowlarr base URL and API key are required")
		}
		id, err := st.CreateSource(ctx, store.Source{
			Name: unique("Prowlarr"), Kind: "torznab", Manager: "prowlarr",
			URL: strings.TrimRight(prowlarrBase, "/"), APIKey: prowlarrKey, Enabled: true,
		})
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
		return ids, nil
	}

	for _, indexer := range plan.Indexers {
		if !indexer.Enabled {
			continue
		}
		id, err := st.CreateSource(ctx, store.Source{
			Name:       unique(indexer.Name),
			Kind:       "torznab",
			Manager:    "jackett",
			URL:        indexer.BaseURL,
			APIKey:     indexer.APIKey,
			Categories: joinInts(indexer.Categories),
			Enabled:    true,
		})
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func joinInts(values []int) string {
	if len(values) == 0 {
		return ""
	}
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, strconv.Itoa(value))
	}
	return strings.Join(parts, ",")
}
