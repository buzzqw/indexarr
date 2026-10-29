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
// Before a sync changes anything it stores a snapshot of the application's
// indexer list (app_snapshots). The App page lists those snapshots and can
// restore one; a restore only touches the indexers whose name carries the
// ManagedPrefix, creates a before-restore snapshot of its own, and still
// re-applies the current desired state on the next scheduled sync.
//
// Secrets: the application API key is decrypted with the vault and passed only
// to the appsync client. Logs contain application names and counters, never
// API keys or URLs with credentials. The appsync client redacts the key from
// every error it returns, and snapshots never include it.
package syncjob

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
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
	client, appKey, indexarrKey, err := s.connect(ctx, app)
	if err != nil {
		return err
	}
	// Defence in depth: even if an application echoes the submitted indexer
	// settings in an error body, neither key may reach last_error, a log line
	// or the admin API. appsync already redacts the application key.
	defer func() { err = scrubError(err, appKey, indexarrKey) }()

	remote, err := client.ListIndexers(ctx)
	if err != nil {
		return fmt.Errorf("syncjob: %s: list indexers: %w", app.Name, err)
	}
	desired, indexerIDs, err := s.desiredIndexers(ctx, indexarrKey, app, client)
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

	// Snapshot before the first change. A snapshot failure aborts the sync
	// instead of modifying the application without a way back.
	if len(actions) > 0 {
		if _, err := s.recordSnapshot(ctx, app.ID, store.SnapshotReasonSync, client); err != nil {
			return fmt.Errorf("syncjob: %s: snapshot before sync: %w", app.Name, err)
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
	byName, byBaseURL, err := s.mappingFor(ctx, desired)
	if err != nil {
		return fmt.Errorf("syncjob: %s: map indexers: %w", app.Name, err)
	}
	desiredIDs := make(map[int64]bool, len(indexerIDs))
	for _, id := range indexerIDs {
		desiredIDs[id] = true
	}

	return s.applyActions(ctx, app, client, actions, links, byName, byBaseURL,
		byRemote, desiredIDs, result)
}

// connect resolves the application API key, loads the indexarr Torznab key and
// builds the appsync client. The caller must scrub errors with both keys.
func (s *Service) connect(ctx context.Context, app store.Application) (appsync.Client, string, string, error) {
	if s.vault == nil {
		return nil, "", "", errors.New("syncjob: vault is not configured; cannot decrypt the application API key")
	}
	if len(app.APIKeyEnc) == 0 {
		return nil, "", "", fmt.Errorf("syncjob: application %q has no API key", app.Name)
	}
	apiKey, err := s.vault.OpenString(string(app.APIKeyEnc))
	if err != nil {
		return nil, "", "", fmt.Errorf("syncjob: decrypt API key for %q: %w", app.Name, err)
	}
	if strings.TrimSpace(apiKey) == "" {
		return nil, "", "", fmt.Errorf("syncjob: application %q has an empty API key", app.Name)
	}

	if s.baseURL == "" {
		return nil, "", "", errors.New("syncjob: public base URL is not configured")
	}
	settings, err := s.store.LoadSettings(ctx)
	if err != nil {
		return nil, "", "", fmt.Errorf("syncjob: load settings: %w", err)
	}
	indexarrKey := strings.TrimSpace(settings.APIKey)
	if indexarrKey == "" {
		return nil, "", "", errors.New("syncjob: indexarr API key is not configured")
	}

	client, err := s.clientFor(app, apiKey)
	if err != nil {
		return nil, "", "", fmt.Errorf("syncjob: create %s client for %q: %w", app.Kind, app.Name, err)
	}
	if client == nil {
		return nil, "", "", fmt.Errorf("syncjob: create %s client for %q: no client returned", app.Kind, app.Name)
	}
	return client, apiKey, indexarrKey, nil
}

// recordSnapshot stores the current indexer list of an application. Clients
// without indexer management return ErrUnsupported: they have nothing to
// snapshot and nothing to restore.
func (s *Service) recordSnapshot(ctx context.Context, appID int64, reason string, client appsync.Client) (int64, error) {
	snapshotter, ok := client.(appsync.Snapshotter)
	if !ok {
		return 0, nil
	}
	payload, err := snapshotter.IndexerSnapshot(ctx)
	if err != nil {
		if errors.Is(err, appsync.ErrUnsupported) {
			return 0, nil
		}
		return 0, err
	}
	return s.store.CreateAppSnapshot(ctx, store.AppSnapshot{
		AppID:        appID,
		Reason:       reason,
		IndexerCount: appsync.CountSnapshotIndexers(payload),
		Payload:      string(payload),
	})
}

// SnapshotApp stores a manual snapshot of the application's current indexer
// list. It is the "save a checkpoint now" action of the App page.
func (s *Service) SnapshotApp(ctx context.Context, applicationID int64) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	app, err := s.store.GetApplication(ctx, applicationID)
	if err != nil {
		return 0, err
	}
	client, appKey, indexarrKey, err := s.connect(ctx, app)
	if err != nil {
		return 0, scrubError(err, appKey, indexarrKey)
	}
	id, err := s.recordSnapshot(ctx, app.ID, store.SnapshotReasonManual, client)
	if err != nil {
		return 0, scrubError(fmt.Errorf("syncjob: %s: manual snapshot: %w", app.Name, err),
			appKey, indexarrKey)
	}
	if id == 0 {
		return 0, appsync.ErrUnsupported
	}
	return id, nil
}

// Restore returns the indexers managed by indexarr to the state stored in one
// snapshot. Operator-created indexers are never touched. A before-restore
// snapshot is taken first, so a restore is itself reversible.
func (s *Service) Restore(ctx context.Context, applicationID, snapshotID int64) (result SyncResult, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	result = SyncResult{ApplicationID: applicationID}
	app, err := s.store.GetApplication(ctx, applicationID)
	if err != nil {
		result.Err = err
		return result, err
	}
	result.Name = app.Name

	client, appKey, indexarrKey, err := s.connect(ctx, app)
	if err != nil {
		result.Err = err
		return result, err
	}
	defer func() {
		if err != nil {
			err = scrubError(err, appKey, indexarrKey)
			result.Err = err
		}
	}()

	target, err := s.store.GetAppSnapshot(ctx, app.ID, snapshotID)
	if err != nil {
		return result, fmt.Errorf("syncjob: %s: load snapshot: %w", app.Name, err)
	}
	remote, err := client.ListIndexers(ctx)
	if err != nil {
		return result, fmt.Errorf("syncjob: %s: list indexers: %w", app.Name, err)
	}
	if _, err := s.recordSnapshot(ctx, app.ID, store.SnapshotReasonBeforeRestore, client); err != nil {
		return result, fmt.Errorf("syncjob: %s: snapshot before restore: %w", app.Name, err)
	}

	desired, err := appsync.SnapshotSpecs([]byte(target.Payload))
	if err != nil {
		return result, fmt.Errorf("syncjob: %s: %w", app.Name, err)
	}
	// The API key of a managed indexer is indexarr's own Torznab key, not the
	// application key. Sending the current one keeps a restored indexer
	// working after a key rotation instead of reviving a stale credential.
	for i := range desired {
		desired[i].APIKey = indexarrKey
	}

	// A restore is always complete: add, update and delete. Operator-created
	// indexers are not in desired and Reconcile never deletes them.
	actions := appsync.Reconcile(desired, remote, appsync.ReconcileOptions{SyncLevel: "full"})
	links, err := s.store.ListAppIndexers(ctx, app.ID)
	if err != nil {
		return result, fmt.Errorf("syncjob: %s: list existing app indexers: %w", app.Name, err)
	}
	byRemote := make(map[string]int64, len(links))
	for _, link := range links {
		byRemote[link.RemoteID] = link.IndexerID
	}
	byName, byBaseURL, err := s.mappingFor(ctx, desired)
	if err != nil {
		return result, fmt.Errorf("syncjob: %s: map indexers: %w", app.Name, err)
	}
	desiredIDs := make(map[int64]bool, len(desired))
	for _, spec := range desired {
		if id, ok := localIndexerID(spec, byName, byBaseURL); ok {
			desiredIDs[id] = true
		}
	}

	if err := s.applyActions(ctx, app, client, actions, links, byName, byBaseURL,
		byRemote, desiredIDs, &result); err != nil {
		result.Err = err
		s.record(ctx, app, err)
		return result, err
	}
	s.record(ctx, app, nil)
	s.logger.Info("syncjob: application restored",
		"application", app.Name, "snapshot", snapshotID,
		"added", result.Added, "updated", result.Updated, "deleted", result.Deleted,
		"skipped", result.Skipped)
	return result, nil
}

// mappingFor maps the desired specs to local indexer ids by managed name and,
// as a fallback, by Torznab base URL.
func (s *Service) mappingFor(ctx context.Context, desired []appsync.IndexerSpec) (map[string]int64, map[string]int64, error) {
	indexers, err := s.store.ListIndexers(ctx)
	if err != nil {
		return nil, nil, err
	}
	byName := make(map[string]int64, len(indexers))
	byBaseURL := make(map[string]int64, len(indexers))
	for _, indexer := range indexers {
		byName[appsync.ManagedPrefix+indexer.Name] = indexer.ID
		byBaseURL[s.baseURL+"/torznab/"+indexer.Slug] = indexer.ID
	}
	return byName, byBaseURL, nil
}

// localIndexerID finds the local indexer a desired spec belongs to. The slug
// (in the Torznab base URL) is unique, so it wins over the display name, which
// indexarr does not constrain.
func localIndexerID(spec appsync.IndexerSpec, byName, byBaseURL map[string]int64) (int64, bool) {
	if id, ok := byBaseURL[normalizeSpecURL(spec.BaseURL)]; ok {
		return id, true
	}
	if id, ok := byName[spec.Name]; ok {
		return id, true
	}
	return 0, false
}

func normalizeSpecURL(raw string) string {
	return strings.TrimRight(strings.TrimSpace(raw), "/")
}

// applyActions performs add, update and delete in a deterministic order and
// records the outcome. A failure on one indexer never aborts the others.
func (s *Service) applyActions(ctx context.Context, app store.Application, client appsync.Client,
	actions []appsync.Action, links []store.AppIndexer,
	byName, byBaseURL, byRemote map[string]int64, desiredIDs map[int64]bool,
	result *SyncResult) error {

	var actionErr error
	remember := func(err error) {
		if actionErr == nil {
			actionErr = err
		}
	}
	failedDeletes := make(map[string]bool)

	for _, action := range actions {
		if action.Kind != appsync.ActionAdd {
			continue
		}
		created, err := client.AddIndexer(ctx, action.Spec)
		if err != nil {
			remember(fmt.Errorf("syncjob: %s: add %q: %w", app.Name, action.Spec.Name, err))
			result.Skipped++
			continue
		}
		indexerID, ok := localIndexerID(action.Spec, byName, byBaseURL)
		if !ok {
			remember(fmt.Errorf("syncjob: %s: no local indexer for %q", app.Name, action.Spec.Name))
			continue
		}
		if err := s.store.UpsertAppIndexer(ctx, store.AppIndexer{
			ApplicationID: app.ID,
			IndexerID:     indexerID,
			RemoteID:      strconv.Itoa(created.ID),
			SyncedHash:    appsync.SpecHash(action.Spec),
		}); err != nil {
			remember(fmt.Errorf("syncjob: %s: record added indexer: %w", app.Name, err))
			continue
		}
		result.Added++
	}

	for _, action := range actions {
		if action.Kind != appsync.ActionUpdate {
			continue
		}
		updated, err := client.UpdateIndexer(ctx, action.RemoteID, action.Spec)
		if err != nil {
			remember(fmt.Errorf("syncjob: %s: update %q: %w", app.Name, action.Spec.Name, err))
			result.Skipped++
			continue
		}
		indexerID, ok := localIndexerID(action.Spec, byName, byBaseURL)
		if !ok {
			remember(fmt.Errorf("syncjob: %s: no local indexer for %q", app.Name, action.Spec.Name))
			continue
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
			remember(fmt.Errorf("syncjob: %s: record updated indexer: %w", app.Name, err))
			continue
		}
		result.Updated++
	}

	for _, action := range actions {
		if action.Kind != appsync.ActionDelete {
			continue
		}
		if err := client.DeleteIndexer(ctx, action.RemoteID); err != nil {
			remember(fmt.Errorf("syncjob: %s: delete indexer %d: %w", app.Name, action.RemoteID, err))
			failedDeletes[strconv.Itoa(action.RemoteID)] = true
			result.Skipped++
			continue
		}
		if indexerID, ok := byRemote[strconv.Itoa(action.RemoteID)]; ok {
			if err := s.store.DeleteAppIndexer(ctx, app.ID, indexerID); err != nil {
				remember(fmt.Errorf("syncjob: %s: forget deleted indexer: %w", app.Name, err))
				continue
			}
		}
		result.Deleted++
	}

	// Forget links to indexers that are no longer desired (disabled, deleted
	// or excluded by the tag filter). This also cleans rows left by an earlier
	// interrupted sync, but never drops a link whose remote deletion just
	// failed: the next sync will retry it.
	for _, link := range links {
		if desiredIDs[link.IndexerID] || failedDeletes[link.RemoteID] {
			continue
		}
		if err := s.store.DeleteAppIndexer(ctx, app.ID, link.IndexerID); err != nil {
			remember(fmt.Errorf("syncjob: %s: forget stale indexer: %w", app.Name, err))
		}
	}
	return actionErr
}

// desiredIndexers builds the desired specs from every enabled indexer that
// matches the application's tag filter. The returned IDs are parallel to the
// specs. Categories are narrowed to what the target application consumes and
// to the application's own selection, tags are translated to the application's
// tag IDs, and the application's search flags and seeder floor are applied, so
// one per-site indexer carries the same priority and labels as the indexer in
// indexarr.
//
// The application tag filter is the parsed applications.tag_filter. An empty
// filter means "all"; otherwise an indexer belongs to the app when it has at
// least one of the labels. Matching is case-insensitive, like the tag labels.
func (s *Service) desiredIndexers(ctx context.Context, indexarrKey string, app store.Application, client appsync.Client) ([]appsync.IndexerSpec, []int64, error) {
	kind := appsync.Kind(strings.ToLower(strings.TrimSpace(app.Kind)))
	filter := splitTags(app.TagFilter)
	selectedCategories := splitCategoryIDs(app.Categories)
	indexers, err := s.store.ListIndexers(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("syncjob: list indexers: %w", err)
	}

	labels := make([]string, 0)
	selected := make([]store.Indexer, 0, len(indexers))
	for _, indexer := range indexers {
		if !indexer.Enabled {
			continue
		}
		if !matchesTagFilter(indexer.Tags, filter) {
			continue
		}
		selected = append(selected, indexer)
		for _, label := range splitTags(indexer.Tags) {
			labels = append(labels, label)
		}
	}
	tagIDs, err := resolveTagIDs(ctx, client, labels)
	if err != nil {
		return nil, nil, fmt.Errorf("syncjob: resolve tags: %w", err)
	}

	categories := make(map[string][]int)
	specs := make([]appsync.IndexerSpec, 0, len(selected))
	ids := make([]int64, 0, len(selected))
	for _, indexer := range selected {
		// Sonarr, Radarr and the other Servarr apps speak only torrent and
		// usenet. An eD2k indexer is still available to indexarr's own clients
		// and to gextto through the Torznab endpoint, but it is never pushed
		// into an application that would send its link to a torrent client.
		if strings.EqualFold(strings.TrimSpace(indexer.Protocol), "ed2k") {
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
		cats, keep := categoriesForApplication(kind, cats, selectedCategories)
		if !keep {
			continue
		}

		implementation, contract := "Torznab", "TorznabSettings"
		protocol := strings.ToLower(strings.TrimSpace(indexer.Protocol))
		if protocol == "usenet" {
			implementation, contract = "Newznab", "NewznabSettings"
		} else {
			protocol = "torrent"
		}

		spec := appsync.IndexerSpec{
			Name:           appsync.ManagedPrefix + indexer.Name,
			Implementation: implementation,
			ConfigContract: contract,
			// Servarr appends its own apiPath (/api) to this base URL, so the
			// value is the per-site Torznab root, exactly like Prowlarr.
			BaseURL:                   s.baseURL + "/torznab/" + slug,
			APIKey:                    indexarrKey,
			Categories:                cats,
			Priority:                  clampPriority(indexer.Priority),
			Tags:                      tagsForLabels(indexer.Tags, tagIDs),
			EnableRSS:                 app.EnableRSS,
			EnableAutomaticSearch:     app.EnableAutomaticSearch,
			EnableInteractiveSearch:   app.EnableInteractiveSearch,
			Protocol:                  protocol,
			MinSeeders:                indexer.MinSeeders,
			SeedRatio:                 indexer.SeedRatio,
			SeedTimeMinutes:           indexer.SeedTimeMinutes,
			SeasonPackSeedTimeMinutes: indexer.SeasonPackSeedTimeMinutes,
		}
		if app.MinSeeders > 0 {
			spec.MinSeeders = app.MinSeeders
		}
		// The two simple applications do not store every field. Normalize the
		// spec to what they can represent, otherwise every sync would compute
		// a different hash and rewrite the same provider forever.
		if kind == appsync.KindMylar3 {
			spec.Tags = nil
			spec.Priority = 25
			spec.MinSeeders = 0
			spec.SeedRatio = 0
			spec.SeedTimeMinutes = 0
			spec.SeasonPackSeedTimeMinutes = 0
		}
		if kind == appsync.KindLazyLibrarian {
			spec.Tags = nil
			spec.SeasonPackSeedTimeMinutes = 0
		}
		specs = append(specs, spec)
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

// splitCategoryIDs parses applications.categories into sorted, unique ids.
// Invalid or negative values are ignored: the web form and the admin API
// validate them before saving, and a malformed legacy value must not make the
// whole sync fail.
func splitCategoryIDs(raw string) []int {
	seen := make(map[int]bool)
	out := make([]int, 0, 8)
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, err := strconv.Atoi(part)
		if err != nil || id < 0 || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	sort.Ints(out)
	return out
}

// categoriesForApplication narrows the indexer's standard categories to the
// application's selected categories and to the categories its kind supports.
// An application with no selection keeps the previous behaviour: every
// category the kind supports. When the indexer exposes no categories the
// application selection is used as-is. The second result is false when the
// indexer must be skipped because a non-empty selection has no overlap with
// it.
func categoriesForApplication(kind appsync.Kind, indexer, selected []int) ([]int, bool) {
	cats := indexer
	if len(selected) > 0 {
		if len(indexer) == 0 {
			cats = selected
		} else {
			cats = intersectCategories(indexer, selected)
			if len(cats) == 0 {
				return nil, false
			}
		}
	}
	cats = appsync.CategoriesForKind(kind, cats)
	if len(selected) > 0 && len(cats) == 0 {
		return nil, false
	}
	return cats, true
}

// intersectCategories returns the ids present in both lists, sorted and
// deduplicated. Both inputs are usually already sorted.
func intersectCategories(a, b []int) []int {
	wanted := make(map[int]bool, len(b))
	for _, id := range b {
		wanted[id] = true
	}
	seen := make(map[int]bool, len(a))
	out := make([]int, 0, len(a))
	for _, id := range a {
		if wanted[id] && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	sort.Ints(out)
	return out
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
	// Sonarr redacts the key itself, but Radarr can echo only a prefix of it
	// inside the indexer URL. Treat any credential query value as secret so a
	// partial key never reaches last_error, a log line or the redirect.
	scrubbed = credentialQuery.ReplaceAllString(scrubbed, "$1=[REDACTED]")
	if scrubbed == text {
		return err
	}
	return &scrubbedError{message: scrubbed, err: err}
}

var credentialQuery = regexp.MustCompile(`(?i)\b(api_?key|apikey|key|token|password|pass)=([^\s&"'<>\\]+)`)

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

// matchesTagFilter reports whether an indexer belongs to an application. An
// empty filter means every indexer; otherwise one shared label is enough.
// Labels are compared case-insensitively, as Servarr does.
func matchesTagFilter(raw string, filter []string) bool {
	if len(filter) == 0 {
		return true
	}
	wanted := make(map[string]bool, len(filter))
	for _, label := range filter {
		key := strings.ToLower(strings.TrimSpace(label))
		if key != "" {
			wanted[key] = true
		}
	}
	if len(wanted) == 0 {
		return true
	}
	for _, label := range splitTags(raw) {
		if wanted[strings.ToLower(label)] {
			return true
		}
	}
	return false
}

// splitTags turns the comma-separated label list stored with an indexer into
// trimmed, unique labels.
func splitTags(raw string) []string {
	var out []string
	seen := make(map[string]bool)
	for _, part := range strings.Split(raw, ",") {
		label := strings.TrimSpace(part)
		if label == "" {
			continue
		}
		key := strings.ToLower(label)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, label)
	}
	return out
}

// resolveTagIDs maps indexer tag labels to the application's tag IDs. Clients
// without tag support (Mylar3, LazyLibrarian, tests) return no mapping and the
// tags are simply omitted from the indexer payload.
func resolveTagIDs(ctx context.Context, client appsync.Client, labels []string) (map[string]int, error) {
	out := make(map[string]int)
	resolver, ok := client.(appsync.TagResolver)
	if !ok {
		return out, nil
	}
	unique := make([]string, 0, len(labels))
	seen := make(map[string]bool, len(labels))
	for _, label := range labels {
		key := strings.ToLower(strings.TrimSpace(label))
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		unique = append(unique, strings.TrimSpace(label))
	}
	if len(unique) == 0 {
		return out, nil
	}
	sort.Strings(unique)
	ids, err := resolver.ResolveTags(ctx, unique)
	if err != nil {
		return nil, err
	}
	for i, label := range unique {
		if i >= len(ids) || ids[i] <= 0 {
			continue
		}
		out[strings.ToLower(label)] = ids[i]
	}
	return out, nil
}

// tagsForLabels returns the application tag IDs for an indexer's labels, in
// the order the labels were written.
func tagsForLabels(raw string, byLabel map[string]int) []int {
	if len(byLabel) == 0 {
		return nil
	}
	seen := make(map[int]bool)
	var out []int
	for _, label := range splitTags(raw) {
		id, ok := byLabel[strings.ToLower(label)]
		if !ok || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
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
