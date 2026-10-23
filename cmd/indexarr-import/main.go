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
	jackettDir := flags.String("jackett", "", "Jackett data directory or ServerConfig.json (offline fallback)")
	jackettURL := flags.String("jackett-url", "", "live Jackett base URL; alone it reads the t=indexers list, with -jackett it overrides the copy")
	jackettKey := flags.String("jackett-key", os.Getenv("INDEXARR_JACKETT_KEY"), "Jackett API key (default: INDEXARR_JACKETT_KEY)")
	prowlarrURL := flags.String("prowlarr", "", "Prowlarr base URL, e.g. http://127.0.0.1:9696")
	prowlarrKey := flags.String("prowlarr-key", os.Getenv("INDEXARR_PROWLARR_KEY"), "Prowlarr API key (default: INDEXARR_PROWLARR_KEY)")
	dbPath := flags.String("db", "", "target indexarr SQLite database for -apply")
	configPath := flags.String("config", "", "indexarr JSON configuration (optional, used for vault_key_path)")
	apply := flags.Bool("apply", false, "write native indexers into -db (default: print the plan)")
	jsonOut := flags.Bool("json", false, "print machine-readable output")
	timeout := flags.Duration("timeout", 20*time.Second, "network timeout for Prowlarr")
	flags.Usage = func() {
		fmt.Fprint(os.Stderr, `usage: indexarr-import (-jackett-url URL | -jackett DIR | -prowlarr URL) [flags]

Reads an existing Jackett or Prowlarr installation without modifying it. A
live Jackett is read with a GET on
/api/v2.0/indexers/all/results/torznab/api?t=indexers&configured=true and every
indexer is probed; a copy of the Jackett data directory is the offline
fallback. Prowlarr is read with the official v1 API.

With -apply and -db it creates native indexers in a disposable indexarr
database: verified catalog definitions where available, otherwise a native
torznab indexer pointed at the manager's per-indexer endpoint. The same site
arriving from several sources is imported once, preferring a verified native
definition, then Prowlarr, then Jackett.

The optional INDEXARR_TORRENTLEECH_RSS_URL environment variable adds the
native torrentleech-rss indexer. The value contains a personal rsskey: it is
sealed in the vault and never printed or written to a log.

The vault key is resolved with the same rules as the server: INDEXARR_MASTER_KEY,
INDEXARR_MASTER_KEY_FILE, -config's vault_key_path, <db>.key, then the user
configuration directory. Secrets are sealed before they touch the database.

flags:
  -jackett DIR        Jackett data directory or ServerConfig.json (fallback)
  -jackett-url URL    live Jackett base URL (with -jackett it overrides the copy)
  -jackett-key KEY    Jackett API key (default: INDEXARR_JACKETT_KEY)
  -prowlarr URL       Prowlarr base URL
  -prowlarr-key KEY   Prowlarr API key (default: INDEXARR_PROWLARR_KEY)
  -db PATH            target indexarr SQLite database
  -config PATH        indexarr JSON configuration (optional)
  -apply              write native indexers (otherwise only print the plan)
  -json               machine-readable output
  -timeout 20s        network timeout
`)
	}
	if err := flags.Parse(args); err != nil {
		return 2
	}
	feedURL := strings.TrimSpace(os.Getenv("INDEXARR_TORRENTLEECH_RSS_URL"))
	if *jackettDir == "" && *jackettURL == "" && *prowlarrURL == "" && feedURL == "" {
		flags.Usage()
		return 2
	}
	if strings.TrimSpace(*jackettKey) == "" && (*jackettDir != "" || *jackettURL != "") {
		fmt.Fprintln(os.Stderr, "indexarr-import: -jackett-key is required with -jackett or -jackett-url")
		return 2
	}
	if strings.TrimSpace(*prowlarrKey) == "" && *prowlarrURL != "" {
		fmt.Fprintln(os.Stderr, "indexarr-import: -prowlarr-key is required with -prowlarr")
		return 2
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	var plans []importers.Plan
	// The live Jackett API is the primary source. The copied data directory is
	// used only when the API does not answer, so a stale copy can never hide a
	// running instance's real indexer list.
	if *jackettURL != "" {
		plan, err := importers.FromJackettAPI(ctx, *jackettURL, *jackettKey, nil)
		if err == nil {
			plans = append(plans, plan)
		} else if *jackettDir == "" {
			fmt.Fprintf(os.Stderr, "indexarr-import: %v\n", err)
			return 1
		} else {
			fmt.Fprintf(os.Stderr, "indexarr-import: warning: live Jackett failed (%v); using the copy\n", err)
			plan, err = importers.FromJackettConfig(ctx, *jackettDir)
			if err != nil {
				fmt.Fprintf(os.Stderr, "indexarr-import: %v\n", err)
				return 1
			}
			plan, err = importers.OverrideManagerEndpoint(plan, "", *jackettKey)
			if err != nil {
				fmt.Fprintf(os.Stderr, "indexarr-import: %v\n", err)
				return 1
			}
			plans = append(plans, plan)
		}
	} else if *jackettDir != "" {
		plan, err := importers.FromJackettConfig(ctx, *jackettDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "indexarr-import: %v\n", err)
			return 1
		}
		if strings.TrimSpace(*jackettURL) != "" || strings.TrimSpace(*jackettKey) != "" {
			plan, err = importers.OverrideManagerEndpoint(plan, *jackettURL, *jackettKey)
			if err != nil {
				fmt.Fprintf(os.Stderr, "indexarr-import: %v\n", err)
				return 1
			}
		}
		plans = append(plans, plan)
	}
	if *prowlarrURL != "" {
		plan, err := importers.FromProwlarr(ctx, *prowlarrURL, *prowlarrKey, nil)
		if err != nil {
			fmt.Fprintf(os.Stderr, "indexarr-import: %v\n", err)
			return 1
		}
		plans = append(plans, plan)
	}

	var targets []importers.NativeTarget
	for _, plan := range plans {
		planTargets, err := importers.Targets(plan)
		if err != nil {
			fmt.Fprintf(os.Stderr, "indexarr-import: %v\n", err)
			return 1
		}
		targets = append(targets, planTargets...)
	}
	if feedURL != "" {
		targets = append(targets, importers.TorrentLeechRSSTarget(feedURL))
	}
	targets, dropped := importers.Deduplicate(targets)

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

	printPlans(plans, targets, dropped, *jsonOut)
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

// jsonOutput is the machine-readable import plan. Target secrets are removed
// before encoding, exactly like the human-readable report.
type jsonOutput struct {
	Plans   []importers.Plan         `json:"plans"`
	Targets []importers.NativeTarget `json:"targets"`
	Dropped []droppedTargetJSON      `json:"dropped,omitempty"`
}

type droppedTargetJSON struct {
	Target    importers.NativeTarget `json:"target"`
	CoveredBy importers.NativeTarget `json:"covered_by"`
}

func printPlans(plans []importers.Plan, targets []importers.NativeTarget, dropped []importers.DroppedTarget, asJSON bool) {
	redactedPlans := make([]importers.Plan, 0, len(plans))
	for _, plan := range plans {
		redactedPlans = append(redactedPlans, redactPlan(plan))
	}
	redactedTargets := redactTargets(targets)
	redactedDropped := make([]droppedTargetJSON, 0, len(dropped))
	for _, item := range dropped {
		redactedDropped = append(redactedDropped, droppedTargetJSON{
			Target:    redactTarget(item.Target),
			CoveredBy: redactTarget(item.CoveredBy),
		})
	}

	if asJSON {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(jsonOutput{
			Plans: redactedPlans, Targets: redactedTargets, Dropped: redactedDropped,
		}); err != nil {
			fmt.Fprintf(os.Stderr, "indexarr-import: encode plan: %v\n", err)
		}
		return
	}
	for _, plan := range redactedPlans {
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
	printImportTable(redactedTargets, redactedDropped)
}

// printImportTable is the per-site result of the import: native, via Jackett,
// via Prowlarr, disabled, or not imported because another source already
// covers the site.
func printImportTable(targets []importers.NativeTarget, dropped []droppedTargetJSON) {
	fmt.Printf("import table (one indexer per site):\n")
	for _, target := range targets {
		fmt.Printf("  %-28s %-34s %s\n", target.SiteKey, target.Name, targetKind(target))
	}
	for _, item := range dropped {
		fmt.Printf("  %-28s %-34s non importato: già coperto da %s\n",
			item.Target.SiteKey, item.Target.Name, item.CoveredBy.Name)
	}
}

func targetKind(target importers.NativeTarget) string {
	if !target.Enabled {
		reason := strings.TrimSpace(target.DisabledReason)
		if reason == "" {
			reason = "disattivato"
		}
		return "disattivato (" + reason + ")"
	}
	if !strings.EqualFold(target.DefinitionID, "torznab-generic") {
		return "nativo (" + target.DefinitionID + ")"
	}
	switch strings.ToLower(strings.TrimSpace(target.ViaManager)) {
	case "prowlarr":
		return "via Prowlarr"
	case "jackett":
		return "via Jackett"
	default:
		return "torznab"
	}
}

// redactTarget removes the vaulted values from a target before any output.
func redactTarget(target importers.NativeTarget) importers.NativeTarget {
	target.Secrets = nil
	return target
}

func redactTargets(targets []importers.NativeTarget) []importers.NativeTarget {
	out := make([]importers.NativeTarget, 0, len(targets))
	for _, target := range targets {
		out = append(out, redactTarget(target))
	}
	return out
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
	fmt.Printf("applied %d indexers to %s: %d created, %d updated, %d duplicates skipped (%d native, %d torznab bridge)\n",
		len(result.Applied), dbPath, result.Created, result.Updated, result.Skipped, native, bridges)
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
	for _, item := range result.Dropped {
		fmt.Printf("  %-14s %-40s non importato: già coperto da %s\n",
			"duplicate", item.Target.Name, item.CoveredBy.Name)
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
