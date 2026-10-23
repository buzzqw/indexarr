// Command indexarr-import reads an existing Jackett or Prowlarr installation
// and prints a redacted import plan. With -apply it creates native indexers in
// a disposable indexarr database: a verified catalog definition when one
// matches the site, otherwise a native torznab indexer pointed at the
// per-indexer endpoint of Jackett or Prowlarr. The original Jackett/Prowlarr
// instances are only read, never modified.
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

	"github.com/buzzqw/indexarr/internal/config"
	"github.com/buzzqw/indexarr/internal/importers"
	"github.com/buzzqw/indexarr/internal/store"
	"github.com/buzzqw/indexarr/internal/vault"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	flags := flag.NewFlagSet("indexarr-import", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	jackettDir := flags.String("jackett", "", "Jackett data directory or ServerConfig.json")
	jackettURL := flags.String("jackett-url", "", "live Jackett base URL, overrides the copied ServerConfig.json")
	jackettKey := flags.String("jackett-key", os.Getenv("INDEXARR_JACKETT_KEY"), "Jackett API key (default: INDEXARR_JACKETT_KEY)")
	prowlarrURL := flags.String("prowlarr", "", "Prowlarr base URL, e.g. http://127.0.0.1:9696")
	prowlarrKey := flags.String("prowlarr-key", os.Getenv("INDEXARR_PROWLARR_KEY"), "Prowlarr API key (default: INDEXARR_PROWLARR_KEY)")
	dbPath := flags.String("db", "", "target indexarr SQLite database for -apply")
	configPath := flags.String("config", "", "indexarr JSON configuration (optional, used for vault_key_path)")
	apply := flags.Bool("apply", false, "write native indexers into -db (default: print the plan)")
	jsonOut := flags.Bool("json", false, "print machine-readable output")
	timeout := flags.Duration("timeout", 20*time.Second, "network timeout for Prowlarr")
	flags.Usage = func() {
		fmt.Fprint(os.Stderr, `usage: indexarr-import (-jackett DIR | -prowlarr URL -prowlarr-key KEY) [flags]

Reads an existing Jackett or Prowlarr configuration without modifying it.
With -apply and -db it creates native indexers in a disposable indexarr
database: verified catalog definitions where available, otherwise native
torznab indexers pointed at the manager's per-indexer endpoint.

The vault key is resolved with the same rules as the server: INDEXARR_MASTER_KEY,
INDEXARR_MASTER_KEY_FILE, -config's vault_key_path, <db>.key, then the user
configuration directory. Secrets are sealed before they touch the database.

flags:
  -jackett DIR        Jackett data directory or ServerConfig.json
  -jackett-url URL    live Jackett base URL (default: from the copied config)
  -jackett-key KEY    Jackett API key (default: INDEXARR_JACKETT_KEY)
  -prowlarr URL       Prowlarr base URL
  -prowlarr-key KEY   Prowlarr API key (default: INDEXARR_PROWLARR_KEY)
  -db PATH            target indexarr SQLite database
  -config PATH        indexarr JSON configuration (optional)
  -apply              write native indexers (otherwise only print the plan)
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
		if err == nil && (strings.TrimSpace(*jackettURL) != "" || strings.TrimSpace(*jackettKey) != "") {
			plan, err = importers.OverrideManagerEndpoint(plan, *jackettURL, *jackettKey)
		}
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

	targets, err := importers.Targets(plan)
	if err != nil {
		fmt.Fprintf(os.Stderr, "indexarr-import: %v\n", err)
		return 1
	}

	if *apply {
		if *dbPath == "" {
			fmt.Fprintln(os.Stderr, "indexarr-import: -apply requires -db")
			return 2
		}
		secretVault, keySource, vaultErr := resolveVault(*dbPath, *configPath)
		if vaultErr != nil && targetsNeedVault(targets) {
			fmt.Fprintf(os.Stderr, "indexarr-import: vault key required to seal indexer secrets: %v\n", vaultErr)
			return 1
		}
		if vaultErr != nil {
			fmt.Fprintf(os.Stderr, "indexarr-import: warning: secrets vault unavailable (%v); nothing to seal\n", vaultErr)
		}
		result, applyErr := applyTargets(ctx, *dbPath, secretVault, targets)
		if applyErr != nil {
			fmt.Fprintf(os.Stderr, "indexarr-import: %v\n", applyErr)
			return 1
		}
		printApplySummary(result, *dbPath, keySource.Kind)
	}

	printPlan(redactPlan(plan), targets, *jsonOut)
	return 0
}

// redactPlan removes the secrets that are only needed to talk to the original
// service. The plan summary never prints them.
func redactPlan(plan importers.Plan) importers.Plan {
	plan.ManagerAPIKey = ""
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

func printPlan(plan importers.Plan, targets []importers.NativeTarget, asJSON bool) {
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
	for i, indexer := range plan.Indexers {
		fmt.Printf("  %-40s protocol=%-7s enabled=%t categories=%s hint=%s\n",
			indexer.Name, indexer.Protocol, indexer.Enabled,
			joinInts(indexer.Categories), indexer.DefinitionHint)
		if i < len(targets) {
			fmt.Printf("    target: %s\n", targetLabel(targets[i]))
		}
	}
	for _, app := range plan.Applications {
		fmt.Printf("  app %-30s kind=%s sync=%s\n", app.Name, app.Kind, app.SyncLevel)
	}
	for _, warning := range plan.Warnings {
		fmt.Printf("  warning: %s\n", warning)
	}
}

// targetLabel describes a native target without its secrets.
func targetLabel(target importers.NativeTarget) string {
	kind := "native"
	if target.ViaManager != "" {
		kind = "torznab bridge"
	}
	label := fmt.Sprintf("%-14s %s -> %s", kind, target.Name, target.DefinitionID)
	if target.ViaManager != "" {
		label += " (" + target.ViaManager + ")"
	}
	if !target.Enabled {
		label += " [disabled]"
	}
	return label
}

func printApplySummary(result importers.ApplyResult, dbPath, keySource string) {
	native, bridges := 0, 0
	for _, item := range result.Applied {
		if item.ViaManager == "" {
			native++
		} else {
			bridges++
		}
	}
	fmt.Printf("applied %d indexers to %s: %d created, %d updated (%d native, %d torznab bridge)\n",
		len(result.Applied), dbPath, result.Created, result.Updated, native, bridges)
	for _, item := range result.Applied {
		action := "created"
		if item.Updated {
			action = "updated"
		}
		kind := "native"
		if item.ViaManager != "" {
			kind = "torznab bridge"
		}
		fmt.Printf("  %-14s %-40s %s (%s)\n", kind, item.Name, action, item.DefinitionID)
	}
	if keySource != "" {
		fmt.Printf("vault key source: %s\n", keySource)
	}
}

// resolveVault applies the documented key resolution order. The optional
// indexarr.json is read only for vault_key_path; an unreadable or invalid file
// is a hard error so the import never silently seals with another key.
func resolveVault(dbPath, configPath string) (*vault.Vault, vault.KeySource, error) {
	keyPath := ""
	path := strings.TrimSpace(configPath)
	if path == "" {
		if info, err := os.Stat("indexarr.json"); err == nil && !info.IsDir() {
			path = "indexarr.json"
		}
	}
	if path != "" {
		if _, err := os.Stat(path); err != nil {
			return nil, vault.KeySource{}, fmt.Errorf("configuration %s: %w", path, err)
		}
		cfg, err := config.Load(path)
		if err != nil {
			return nil, vault.KeySource{}, err
		}
		keyPath = cfg.VaultKeyPath
	}
	return vault.Resolve(vault.ResolveOptions{
		EnvKey:        os.Getenv("INDEXARR_MASTER_KEY"),
		EnvKeyFile:    os.Getenv("INDEXARR_MASTER_KEY_FILE"),
		ConfigPath:    keyPath,
		DBPath:        dbPath,
		UserConfigDir: os.UserConfigDir,
	})
}

func targetsNeedVault(targets []importers.NativeTarget) bool {
	for _, target := range targets {
		if len(target.Secrets) > 0 {
			return true
		}
	}
	return false
}

// applyTargets opens the disposable database and delegates to the shared
// importers.ApplyTargets service, so the CLI and the web UI cannot drift.
func applyTargets(ctx context.Context, dbPath string, secretVault *vault.Vault, targets []importers.NativeTarget) (importers.ApplyResult, error) {
	st, err := store.Open(dbPath)
	if err != nil {
		return importers.ApplyResult{}, err
	}
	defer st.Close()
	return importers.ApplyTargets(ctx, st, secretVault, targets)
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
