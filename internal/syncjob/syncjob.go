// Package syncjob keeps the indexers of indexarr in sync with the applications
// that consume Torznab/Newznab: Sonarr, Radarr, Lidarr, Readarr, Whisparr,
// Mylar3 and LazyLibrarian. It is the Prowlarr replacement of indexarr 2.
//
// The desired state is computed from every enabled indexer in the store and is
// the single source of truth: reconciliation adds missing managed indexers,
// updates changed ones and removes managed indexers that are no longer
// desired. Manually created indexers in the application (those without the
// appsync.ManagedPrefix) are never touched.
//
// Rollback is deliberately not implemented in this wave. Because the desired
// state is authoritative, "undoing" a sync means restoring the previous
// indexer configuration and running the job again; a future snapshot table of
// app_indexers will provide real one-click rollback. No fake rollback is
// stored here.
//
// Secrets: the application API key is decrypted with the vault and passed only
// to the appsync client. Logs contain application names and counters, never
// API keys or URLs with credentials. The appsync client redacts the key from
// every error it returns.
package syncjob

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/buzzqw/indexarr/internal/appsync"
	"github.com/buzzqw/indexarr/internal/definition"
	"github.com/buzzqw/indexarr/internal/store"
	"github.com/buzzqw/indexarr/internal/vault"
)

const (
	// DefaultInterval is used when Config.Interval is zero.
	DefaultInterval = 15 * time.Minute
	// MinimumInterval is the shortest ticker interval accepted.
	MinimumInterval = time.Minute
	// initialSyncDelay lets the rest of the process finish starting before the
	// first automatic sync runs.
	initialSyncDelay = 2 * time.Second
	// maxErrorRunes bounds applications.last_error.
	maxErrorRunes = 500
)

// Config configures a sync Service.
type Config struct {
	Store  *store.Store
	Vault  *vault.Vault
	Logger *slog.Logger
	// BaseURL is the public base URL of indexarr, for example
	// https://indexarr.example. It is used to build the Torznab endpoints
	// written into the applications.
	BaseURL string
	// Interval is the automatic sync period. Zero means DefaultInterval.
	Interval time.Duration
	// ClientFor builds the appsync client for an application. When nil,
	// appsync.New is used. The apiKey argument is already decrypted.
	ClientFor func(app store.Application, apiKey string) (appsync.Client, error)
}

// SyncResult reports what one application sync did.
type SyncResult struct {
	ApplicationID int64
	Name          string
	Added         int
	Updated       int
	Deleted       int
	Skipped       int
	Err           error
}

// Service reconciles indexarr indexers with downstream applications.
type Service struct {
	store     *store.Store
	vault     *vault.Vault
	logger    *slog.Logger
	baseURL   string
	interval  time.Duration
	clientFor func(store.Application, string) (appsync.Client, error)

	// initialDelay is a field so tests can shorten the startup delay.
	initialDelay time.Duration

	// mu serialises syncs. Without it a manual sync and the periodic ticker
	// could both decide that an indexer is missing and add it twice.
	mu sync.Mutex
	wg sync.WaitGroup
}

// New validates cfg and returns a Service.
func New(cfg Config) (*Service, error) {
	if cfg.Store == nil {
		return nil, errors.New("syncjob: store is required")
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	interval := cfg.Interval
	switch {
	case interval <= 0:
		interval = DefaultInterval
	case interval < MinimumInterval:
		interval = MinimumInterval
	}
	clientFor := cfg.ClientFor
	if clientFor == nil {
		clientFor = defaultClient
	}
	return &Service{
		store:        cfg.Store,
		vault:        cfg.Vault,
		logger:       logger,
		baseURL:      strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/"),
		interval:     interval,
		clientFor:    clientFor,
		initialDelay: initialSyncDelay,
	}, nil
}

// defaultClient builds the appsync client from the stored application. The
// store level "add" is mapped to appsync's "add-remove" because both only add
// and remove; appsync.New rejects "add" otherwise.
func defaultClient(app store.Application, apiKey string) (appsync.Client, error) {
	return appsync.New(appsync.Config{
		Kind:      appsync.Kind(strings.ToLower(strings.TrimSpace(app.Kind))),
		Name:      app.Name,
		URL:       app.URL,
		APIKey:    apiKey,
		SyncLevel: syncLevel(app.SyncLevel),
	})
}

// SyncApp synchronises one application. The returned SyncResult always
// describes the application, including when an error occurred; result.Err and
// the returned error are the same value on failure. A disabled application is
// left completely untouched.
func (s *Service) SyncApp(ctx context.Context, applicationID int64) (SyncResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	result := SyncResult{ApplicationID: applicationID}
	app, err := s.store.GetApplication(ctx, applicationID)
	if err != nil {
		result.Err = err
		return result, err
	}
	result.Name = app.Name

	// Disabled means "do not sync", so neither the application nor the
	// app_indexers rows are modified.
	if syncLevel(app.SyncLevel) == "disabled" {
		return result, nil
	}

	if err := s.sync(ctx, app, &result); err != nil {
		result.Err = err
		s.record(ctx, app, err)
		s.logger.Warn("syncjob: application sync failed",
			"application", app.Name, "added", result.Added, "updated", result.Updated,
			"deleted", result.Deleted, "skipped", result.Skipped, "error", err)
		return result, err
	}

	s.record(ctx, app, nil)
	s.logger.Info("syncjob: application synced",
		"application", app.Name, "added", result.Added, "updated", result.Updated,
		"deleted", result.Deleted, "skipped", result.Skipped)
	return result, nil
}

// SyncAll syncs every configured application in name order. One failure does
// not stop the remaining applications; it is recorded in that application's
// last_error and in its result.
func (s *Service) SyncAll(ctx context.Context) []SyncResult {
	apps, err := s.store.ListApplications(ctx)
	if err != nil {
		s.logger.Error("syncjob: list applications", "error", err)
		return nil
	}
	results := make([]SyncResult, 0, len(apps))
	for _, app := range apps {
		result, _ := s.SyncApp(ctx, app.ID)
		results = append(results, result)
	}
	return results
}

// Start launches the periodic sync in a goroutine. It returns immediately.
// The first sync runs after a short delay and then once per interval until ctx
// is cancelled. Call Wait to block until the goroutine has stopped.
func (s *Service) Start(ctx context.Context) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.run(ctx)
	}()
}

// Wait blocks until the goroutine started by Start has returned.
func (s *Service) Wait() {
	s.wg.Wait()
}

func (s *Service) run(ctx context.Context) {
	delay := s.initialDelay
	if delay <= 0 {
		delay = initialSyncDelay
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return
	case <-timer.C:
	}

	s.SyncAll(ctx)

	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.SyncAll(ctx)
		}
	}
}

// sync performs the reconciliation for one enabled application.
func (s *Service) sync(ctx context.Context, app store.Application, result *SyncResult) (err error) {
	if s.vault == nil {
		return errors.New("syncjob: vault is not configured; cannot decrypt the application API key")
	}
	if len(app.APIKeyEnc) == 0 {
		return fmt.Errorf("syncjob: application %q has no API key", app.Name)
	}
	apiKey, err := s.vault.OpenString(string(app.APIKeyEnc))
	if err != nil {
		return fmt.Errorf("syncjob: decrypt API key for %q: %w", app.Name, err)
	}
	if strings.TrimSpace(apiKey) == "" {
		return fmt.Errorf("syncjob: application %q has an empty API key", app.Name)
	}

	if s.baseURL == "" {
		return errors.New("syncjob: public base URL is not configured")
	}
	settings, err := s.store.LoadSettings(ctx)
	if err != nil {
		return fmt.Errorf("syncjob: load settings: %w", err)
	}
	indexarrKey := strings.TrimSpace(settings.APIKey)
	if indexarrKey == "" {
		return errors.New("syncjob: indexarr API key is not configured")
	}
	// Defence in depth: even if an application echoes the submitted indexer
	// settings in an error body, neither key may reach last_error, a log line
	// or the admin API. appsync already redacts the application key.
	defer func() { err = scrubError(err, apiKey, indexarrKey) }()

	client, err := s.clientFor(app, apiKey)
	if err != nil {
		return fmt.Errorf("syncjob: create %s client for %q: %w", app.Kind, app.Name, err)
	}
	if client == nil {
		return fmt.Errorf("syncjob: create %s client for %q: no client returned", app.Kind, app.Name)
	}

	remote, err := client.ListIndexers(ctx)
	if err != nil {
		return fmt.Errorf("syncjob: %s: list indexers: %w", app.Name, err)
	}
	desired, indexerIDs, err := s.desiredIndexers(ctx, indexarrKey)
	if err != nil {
		return err
	}

	level := syncLevel(app.SyncLevel)
	actions := appsync.Reconcile(desired, remote, appsync.ReconcileOptions{SyncLevel: level})
	if level == "add-remove" {
		// Updates are intentionally not applied at this level: report how many
		// indexers full mode would have updated.
		for _, action := range appsync.Reconcile(desired, remote,
			appsync.ReconcileOptions{SyncLevel: "full"}) {
			if action.Kind == appsync.ActionUpdate {
				result.Skipped++
			}
		}
	}

	links, err := s.store.ListAppIndexers(ctx, app.ID)
	if err != nil {
		return fmt.Errorf("syncjob: %s: list existing app indexers: %w", app.Name, err)
	}
	byRemote := make(map[string]int64, len(links))
	for _, link := range links {
		byRemote[link.RemoteID] = link.IndexerID
	}
	indexerByBaseURL := make(map[string]int64, len(desired))
	desiredIDs := make(map[int64]bool, len(indexerIDs))
	for i, spec := range desired {
		indexerByBaseURL[spec.BaseURL] = indexerIDs[i]
		desiredIDs[indexerIDs[i]] = true
	}

	// Apply in a deterministic order: additions, then updates, then removals.
	for _, action := range actions {
		if action.Kind != appsync.ActionAdd {
			continue
		}
		created, err := client.AddIndexer(ctx, action.Spec)
		if err != nil {
			return fmt.Errorf("syncjob: %s: add %q: %w", app.Name, action.Spec.Name, err)
		}
		indexerID, ok := indexerByBaseURL[action.Spec.BaseURL]
		if !ok {
			return fmt.Errorf("syncjob: internal: no local indexer for %q", action.Spec.Name)
		}
		if err := s.store.UpsertAppIndexer(ctx, store.AppIndexer{
			ApplicationID: app.ID,
			IndexerID:     indexerID,
			RemoteID:      strconv.Itoa(created.ID),
			SyncedHash:    appsync.SpecHash(action.Spec),
		}); err != nil {
			return fmt.Errorf("syncjob: %s: record added indexer: %w", app.Name, err)
		}
		result.Added++
	}

	for _, action := range actions {
		if action.Kind != appsync.ActionUpdate {
			continue
		}
		updated, err := client.UpdateIndexer(ctx, action.RemoteID, action.Spec)
		if err != nil {
			return fmt.Errorf("syncjob: %s: update %q: %w", app.Name, action.Spec.Name, err)
		}
		indexerID, ok := indexerByBaseURL[action.Spec.BaseURL]
		if !ok {
			return fmt.Errorf("syncjob: internal: no local indexer for %q", action.Spec.Name)
		}
		remoteID := action.RemoteID
		if updated.ID > 0 {
			remoteID = updated.ID
		}
		if err := s.store.UpsertAppIndexer(ctx, store.AppIndexer{
			ApplicationID: app.ID,
			IndexerID:     indexerID,
			RemoteID:      strconv.Itoa(remoteID),
			SyncedHash:    appsync.SpecHash(action.Spec),
		}); err != nil {
			return fmt.Errorf("syncjob: %s: record updated indexer: %w", app.Name, err)
		}
		result.Updated++
	}

	for _, action := range actions {
		if action.Kind != appsync.ActionDelete {
			continue
		}
		if err := client.DeleteIndexer(ctx, action.RemoteID); err != nil {
			return fmt.Errorf("syncjob: %s: delete indexer %d: %w", app.Name, action.RemoteID, err)
		}
		if indexerID, ok := byRemote[strconv.Itoa(action.RemoteID)]; ok {
			if err := s.store.DeleteAppIndexer(ctx, app.ID, indexerID); err != nil {
				return fmt.Errorf("syncjob: %s: forget deleted indexer: %w", app.Name, err)
			}
		}
		result.Deleted++
	}

	// Forget links to indexers that are no longer desired (disabled, deleted
	// or removed from the app's managed set). This also cleans rows left by an
	// earlier interrupted sync.
	for _, link := range links {
		if desiredIDs[link.IndexerID] {
			continue
		}
		if err := s.store.DeleteAppIndexer(ctx, app.ID, link.IndexerID); err != nil {
			return fmt.Errorf("syncjob: %s: forget stale indexer: %w", app.Name, err)
		}
	}
	return nil
}

// desiredIndexers builds the desired specs from every enabled indexer. The
// returned IDs are parallel to the specs.
func (s *Service) desiredIndexers(ctx context.Context, indexarrKey string) ([]appsync.IndexerSpec, []int64, error) {
	indexers, err := s.store.ListIndexers(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("syncjob: list indexers: %w", err)
	}

	categories := make(map[string][]int)
	specs := make([]appsync.IndexerSpec, 0, len(indexers))
	ids := make([]int64, 0, len(indexers))
	for _, indexer := range indexers {
		if !indexer.Enabled {
			continue
		}
		slug := strings.TrimSpace(indexer.Slug)
		if slug == "" {
			return nil, nil, fmt.Errorf("syncjob: indexer %d has no slug", indexer.ID)
		}
		cats, ok := categories[indexer.DefinitionID]
		if !ok {
			cats, err = s.definitionCategories(ctx, indexer.DefinitionID)
			if err != nil {
				return nil, nil, err
			}
			categories[indexer.DefinitionID] = cats
		}

		implementation, contract := "Torznab", "TorznabSettings"
		protocol := strings.ToLower(strings.TrimSpace(indexer.Protocol))
		if protocol == "usenet" {
			implementation, contract = "Newznab", "NewznabSettings"
		} else {
			protocol = "torrent"
		}

		specs = append(specs, appsync.IndexerSpec{
			Name:                    appsync.ManagedPrefix + indexer.Name,
			Implementation:          implementation,
			ConfigContract:          contract,
			BaseURL:                 s.baseURL + "/torznab/" + slug,
			APIKey:                  indexarrKey,
			Categories:              cats,
			Priority:                clampPriority(indexer.Priority),
			EnableRSS:               true,
			EnableAutomaticSearch:   true,
			EnableInteractiveSearch: true,
			Protocol:                protocol,
		})
		ids = append(ids, indexer.ID)
	}
	return specs, ids, nil
}

// definitionCategories parses the stored definition and returns its standard
// Torznab categories, deduplicated and sorted. A missing or invalid definition
// is an error: silently skipping it would make the indexer look undesired and
// the reconciler would delete it from the application.
func (s *Service) definitionCategories(ctx context.Context, definitionID string) ([]int, error) {
	def, err := s.store.GetDefinition(ctx, definitionID)
	if err != nil {
		return nil, fmt.Errorf("syncjob: load definition %q: %w", definitionID, err)
	}
	parsed, err := definition.Parse([]byte(def.Body))
	if err != nil {
		return nil, fmt.Errorf("syncjob: parse definition %q: %w", definitionID, err)
	}
	seen := make(map[int]bool, len(parsed.Categories))
	for _, category := range parsed.Categories {
		seen[category.Standard] = true
	}
	out := make([]int, 0, len(seen))
	for standard := range seen {
		out = append(out, standard)
	}
	sort.Ints(out)
	return out, nil
}

// record stores the outcome in applications.last_sync_at/last_error.
func (s *Service) record(ctx context.Context, app store.Application, syncErr error) {
	app.LastSyncAt = time.Now().UTC()
	app.LastError = compactError(syncErr)
	if err := s.store.UpdateApplication(ctx, app); err != nil {
		s.logger.Warn("syncjob: record application result", "application", app.Name, "error", err)
	}
}

// compactError turns an error into the single-line, bounded text stored in
// applications.last_error.
func compactError(err error) string {
	if err == nil {
		return ""
	}
	text := strings.Join(strings.Fields(err.Error()), " ")
	if utf8.RuneCountInString(text) > maxErrorRunes {
		text = string([]rune(text)[:maxErrorRunes])
	}
	return text
}

// scrubbedError keeps the original error reachable with errors.Is/As while
// presenting redacted text.
type scrubbedError struct {
	message string
	err     error
}

func (e *scrubbedError) Error() string { return e.message }
func (e *scrubbedError) Unwrap() error { return e.err }

// scrubError replaces every occurrence of the given secrets in err's text.
// The original error is returned unchanged when nothing matched.
func scrubError(err error, secrets ...string) error {
	if err == nil {
		return nil
	}
	text := err.Error()
	scrubbed := text
	for _, secret := range secrets {
		secret = strings.TrimSpace(secret)
		if secret == "" {
			continue
		}
		for _, form := range []string{
			secret,
			url.QueryEscape(secret),
			url.PathEscape(secret),
		} {
			if form != "" {
				scrubbed = strings.ReplaceAll(scrubbed, form, "[REDACTED]")
			}
		}
	}
	if scrubbed == text {
		return err
	}
	return &scrubbedError{message: scrubbed, err: err}
}

// syncLevel maps a store level to the levels appsync understands. "add" and
// "add-remove" both mean add/remove only.
func syncLevel(level string) string {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "", "disabled":
		return "disabled"
	case "add", "add-remove":
		return "add-remove"
	case "full":
		return "full"
	default:
		return "disabled"
	}
}

// clampPriority keeps the value in Servarr's 1..50 range; zero defaults to 25.
func clampPriority(priority int) int {
	switch {
	case priority == 0:
		return 25
	case priority < 1:
		return 1
	case priority > 50:
		return 50
	default:
		return priority
	}
}
