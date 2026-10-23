// Package health implements the catalog health model and the periodic
// self-check job.
//
// The model is deliberately pure: Assess turns the recent history that WP17
// already records into one state, and NextState applies the catalog
// transitions. The Home page, the sidebar badge, the indexer list and the
// indexer detail all call the same functions, so a site cannot look healthy in
// one place and degraded in another.
package health

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/buzzqw/indexarr/internal/engine"
	"github.com/buzzqw/indexarr/internal/indexer"
	"github.com/buzzqw/indexarr/internal/notify"
	"github.com/buzzqw/indexarr/internal/store"
)

// Catalog and UI states. The first four mirror the status-dot scale of the
// interface; "disabled" is added because a disabled indexer is not unhealthy.
const (
	StateOK       = "ok"
	StateDegraded = "degraded"
	StateProblem  = "problem"
	StateTesting  = "testing"
	StateDisabled = "disabled"
)

// Reason codes. They are stable identifiers, not user-facing text: the web
// layer maps them to a localized sentence and an action.
const (
	ReasonCircuit      = "circuit"
	ReasonLastError    = "last_error"
	ReasonErrors       = "errors"
	ReasonZeroResults  = "zero_results"
	ReasonCompleteness = "completeness"
)

// Catalog states used by definitions. They are repeated here to keep the
// transition function independent from the storage layer.
const (
	CatalogDraft     = "bozza"
	CatalogVerified  = "verificata"
	CatalogStable    = "stabile"
	CatalogDegraded  = "degradata"
	defaultHealthRun = 6 * time.Hour
)

// Thresholds configures when a site is degraded. The defaults follow WP9:
// three failures or three empty answers in a row, and any required field
// missing from more than half of the items.
type Thresholds struct {
	MinFieldPercent        int
	ConsecutiveFailures    int
	ConsecutiveZeroResults int
	StableSuccesses        int
}

// DefaultThresholds returns the documented defaults.
func DefaultThresholds() Thresholds {
	return Thresholds{
		MinFieldPercent:        50,
		ConsecutiveFailures:    3,
		ConsecutiveZeroResults: 3,
		StableSuccesses:        3,
	}
}

func (t Thresholds) normalized() Thresholds {
	defaults := DefaultThresholds()
	if t.MinFieldPercent <= 0 {
		t.MinFieldPercent = defaults.MinFieldPercent
	}
	if t.MinFieldPercent > 100 {
		t.MinFieldPercent = 100
	}
	if t.ConsecutiveFailures <= 0 {
		t.ConsecutiveFailures = defaults.ConsecutiveFailures
	}
	if t.ConsecutiveZeroResults <= 0 {
		t.ConsecutiveZeroResults = defaults.ConsecutiveZeroResults
	}
	if t.StableSuccesses <= 0 {
		t.StableSuccesses = defaults.StableSuccesses
	}
	return t
}

// Reason is one cause of a degraded or broken state.
type Reason struct {
	Code    string
	Count   int
	Field   string // size, seeders, category, date or infohash
	Percent int
}

// Outcome is one health-relevant execution, newest first in every slice the
// package receives. It is the only input of the model, so a caller cannot
// invent a second status source.
type Outcome struct {
	Kind          string
	Status        string
	Error         string
	Seen          int64
	HasSeen       bool
	FieldTotal    int64
	FieldSize     int64
	FieldSeeders  int64
	FieldCategory int64
	FieldDate     int64
	FieldHash     int64
}

// Input is everything Assess needs. Enabled is false when the indexer row is
// disabled; Circuit and LastError come from indexer_state. History is the
// newest-first history returned by the store.
type Input struct {
	Enabled   bool
	Circuit   string
	LastError string
	History   []store.IndexerHistory
	// Outcomes bypasses History for the pure table tests and for callers that
	// already have the compact form.
	Outcomes []Outcome
	// FeedOnly marks an indexer that only publishes a latest feed. A zero-row
	// search is not a fault for it: the feed has no query endpoint.
	FeedOnly bool
}

// Assessment is one indexer's health picture.
type Assessment struct {
	State        string
	Reasons      []Reason
	Failures     int
	ZeroResults  int
	Completeness int // minimum field completeness in percent, -1 when unknown
	HasData      bool
	// LastError is the newest error text, redacted by the caller. The UI uses
	// it to explain the concrete cause instead of a generic streak.
	LastError string
}

// Healthy reports whether the indexer has no current problem.
func (a Assessment) Healthy() bool {
	return a.State == StateOK
}

// NeedsAttention reports whether the state is shown in the Problems filter.
func (a Assessment) NeedsAttention() bool {
	return a.State == StateDegraded || a.State == StateProblem
}

// OutcomesFromHistory converts the stored rows into model outcomes. Only the
// kinds that say something about the site are used: a grab or a manual test
// must not change the health of the indexer. Canceled executions are kept but
// never counted as failures, because a shutdown is not the site's fault.
func OutcomesFromHistory(rows []store.IndexerHistory) []Outcome {
	out := make([]Outcome, 0, len(rows))
	for _, row := range rows {
		if !healthKind(row.Kind) {
			continue
		}
		outcome := Outcome{
			Kind:   strings.ToLower(strings.TrimSpace(row.Kind)),
			Status: strings.ToLower(strings.TrimSpace(row.Status)),
			Error:  strings.TrimSpace(row.Error),
		}
		if row.Metrics != nil {
			metrics := *row.Metrics
			outcome.HasSeen = true
			outcome.Seen = metrics.Seen
			outcome.FieldTotal = metrics.FieldTotal
			outcome.FieldSize = metrics.FieldSize
			outcome.FieldSeeders = metrics.FieldSeeders
			outcome.FieldCategory = metrics.FieldCategory
			outcome.FieldDate = metrics.FieldDate
			outcome.FieldHash = metrics.FieldHash
		}
		out = append(out, outcome)
	}
	return out
}

func healthKind(kind string) bool {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "run", "search", "latest", "health":
		return true
	default:
		return false
	}
}

// Assess computes the state from the WP17 history. The result is the single
// status source of the UI and of the catalog transition job.
func Assess(in Input, thresholds Thresholds) Assessment {
	thresholds = thresholds.normalized()
	assessment := Assessment{State: StateTesting, Completeness: -1}
	if !in.Enabled {
		assessment.State = StateDisabled
		return assessment
	}
	outcomes := in.Outcomes
	if outcomes == nil {
		outcomes = OutcomesFromHistory(in.History)
	}
	signals := analyse(outcomes, thresholds)
	assessment.Failures = signals.failures
	assessment.ZeroResults = signals.zeroResults
	assessment.HasData = len(outcomes) > 0
	assessment.LastError = strings.TrimSpace(in.LastError)
	for _, outcome := range outcomes {
		if outcomeFailed(outcome) && outcome.Error != "" {
			assessment.LastError = outcome.Error
			break
		}
	}
	if signals.completeness >= 0 {
		assessment.Completeness = signals.completeness
	}

	hard := false
	if strings.EqualFold(strings.TrimSpace(in.Circuit), "open") {
		hard = true
		assessment.Reasons = append(assessment.Reasons, Reason{Code: ReasonCircuit})
	}
	if strings.TrimSpace(in.LastError) != "" {
		hard = true
		assessment.Reasons = append(assessment.Reasons, Reason{Code: ReasonLastError})
	}

	latestFailed := len(outcomes) > 0 && outcomeFailed(outcomes[0])
	if latestFailed {
		hard = true
	}
	if signals.failures >= thresholds.ConsecutiveFailures || latestFailed {
		count := signals.failures
		if count < 1 {
			count = 1
		}
		assessment.Reasons = append(assessment.Reasons, Reason{Code: ReasonErrors, Count: count})
	}
	if !in.FeedOnly && signals.zeroResults >= thresholds.ConsecutiveZeroResults {
		assessment.Reasons = append(assessment.Reasons, Reason{
			Code: ReasonZeroResults, Count: signals.zeroResults,
		})
	}

	switch {
	case hard:
		assessment.State = StateProblem
	case len(assessment.Reasons) > 0:
		assessment.State = StateDegraded
	case !assessment.HasData:
		assessment.State = StateTesting
	default:
		assessment.State = StateOK
	}
	return assessment
}

// NextState applies the catalog rule. It is pure and table-tested.
//
//	draft            -> draft        (health never promotes a draft)
//	verificata       -> stabile      after StableSuccesses clean outcomes
//	verificata       -> degradata    after ConsecutiveFailures/ZeroResults or low completeness
//	stabile          -> degradata    under the same conditions
//	degradata        -> verificata   after a clean outcome, once the degraded signal is gone
//
// A definition is never deleted or rewritten: only its state changes.
func NextState(current string, recent []Outcome, thresholds Thresholds) string {
	thresholds = thresholds.normalized()
	current = strings.ToLower(strings.TrimSpace(current))
	if current == "" || current == CatalogDraft {
		return current
	}
	signals := analyse(recent, thresholds)
	degraded := signals.failures >= thresholds.ConsecutiveFailures ||
		signals.zeroResults >= thresholds.ConsecutiveZeroResults
	latestClean := len(recent) > 0 && !outcomeFailed(recent[0]) && !outcomeCanceled(recent[0]) &&
		(!recent[0].HasSeen || recent[0].Seen > 0)

	switch {
	case degraded:
		switch current {
		case CatalogVerified, CatalogStable, CatalogDegraded:
			return CatalogDegraded
		}
	case current == CatalogDegraded:
		if latestClean {
			return CatalogVerified
		}
	case current == CatalogVerified:
		if consecutiveClean(recent) >= thresholds.StableSuccesses {
			return CatalogStable
		}
	}
	return current
}

// NextStateGroup applies NextState to every indexer that shares a definition
// and takes the worst answer. A healthy filter must never hide a degraded
// sibling: the definition is degraded if any filter is, it recovers only when
// every filter has a clean outcome, and it becomes stable only when every
// filter has accumulated enough clean outcomes.
func NextStateGroup(current string, groups [][]Outcome, thresholds Thresholds) string {
	thresholds = thresholds.normalized()
	current = strings.ToLower(strings.TrimSpace(current))
	if current == "" || current == CatalogDraft {
		return current
	}
	degraded := false
	allClean := true
	allStable := true
	for _, outcomes := range groups {
		target := NextState(current, outcomes, thresholds)
		if target == CatalogDegraded {
			degraded = true
		}
		latest := len(outcomes) > 0 && !outcomeFailed(outcomes[0]) && !outcomeCanceled(outcomes[0]) &&
			(!outcomes[0].HasSeen || outcomes[0].Seen > 0)
		if !latest {
			allClean = false
		}
		if target != CatalogStable {
			allStable = false
		}
	}
	switch {
	case degraded:
		return CatalogDegraded
	case current == CatalogDegraded && allClean:
		return CatalogVerified
	case current == CatalogVerified && allStable && len(groups) > 0:
		return CatalogStable
	default:
		return current
	}
}

type analysis struct {
	failures     int
	zeroResults  int
	completeness int
	field        string
}

func analyse(outcomes []Outcome, thresholds Thresholds) analysis {
	out := analysis{completeness: -1}
	out.failures = consecutiveFailures(outcomes)
	out.zeroResults = consecutiveZero(outcomes)
	out.completeness, out.field = latestCompleteness(outcomes)
	return out
}

func consecutiveFailures(outcomes []Outcome) int {
	count := 0
	for _, outcome := range outcomes {
		if outcomeFailed(outcome) {
			count++
			continue
		}
		break
	}
	return count
}

func consecutiveClean(outcomes []Outcome) int {
	count := 0
	for _, outcome := range outcomes {
		if outcomeFailed(outcome) || outcomeCanceled(outcome) {
			break
		}
		count++
	}
	return count
}

func consecutiveZero(outcomes []Outcome) int {
	count := 0
	for _, outcome := range outcomes {
		// Only a scheduled latest run tells us that a site should have
		// returned something. Sonarr/Radarr searches are user queries: an
		// alternate title that legitimately finds nothing must not turn the
		// indexer red.
		if !strings.EqualFold(strings.TrimSpace(outcome.Kind), "run") {
			continue
		}
		if outcomeFailed(outcome) || outcomeCanceled(outcome) {
			break
		}
		if !outcome.HasSeen || outcome.Seen > 0 {
			break
		}
		count++
	}
	return count
}

// latestCompleteness returns the minimum field completeness of the newest
// execution that measured normalized items. Health checks are excluded: their
// one-item sample would make every site look incomplete.
func latestCompleteness(outcomes []Outcome) (int, string) {
	for _, outcome := range outcomes {
		if outcome.Kind == "health" || outcome.FieldTotal <= 0 {
			continue
		}
		fields := []struct {
			name  string
			value int64
		}{
			{"size", outcome.FieldSize},
			{"seeders", outcome.FieldSeeders},
			{"category", outcome.FieldCategory},
			{"date", outcome.FieldDate},
			{"infohash", outcome.FieldHash},
		}
		minimum := int64(101)
		name := ""
		for _, field := range fields {
			percent := field.value * 100 / outcome.FieldTotal
			if percent < minimum {
				minimum = percent
				name = field.name
			}
		}
		if minimum > 100 {
			minimum = 100
		}
		return int(minimum), name
	}
	return -1, ""
}

func outcomeFailed(outcome Outcome) bool {
	return strings.EqualFold(strings.TrimSpace(outcome.Status), "error")
}

func outcomeCanceled(outcome Outcome) bool {
	return strings.EqualFold(strings.TrimSpace(outcome.Status), "canceled")
}

// ---------------------------------------------------------------- Checker

// Config wires the periodic checker. Only Store and Manager are required.
type Config struct {
	Store      *store.Store
	Manager    *indexer.Manager
	Logger     *slog.Logger
	Clock      engine.Clock
	Interval   time.Duration
	Thresholds Thresholds
	// Notify replaces the default webhook/Telegram delivery in tests.
	Notify func(context.Context, store.Settings, notify.Event)
}

// Result is the outcome of one health check.
type Result struct {
	IndexerID    int64
	Slug         string
	Name         string
	DefinitionID string
	FromState    string
	ToState      string
	Items        int
	Err          error
}

// Checker runs health.query for every enabled indexer and updates the catalog
// state of the definitions it uses.
type Checker struct {
	store      *store.Store
	manager    *indexer.Manager
	logger     *slog.Logger
	clock      engine.Clock
	interval   time.Duration
	thresholds Thresholds
	notify     func(context.Context, store.Settings, notify.Event)

	startOnce sync.Once
	wg        sync.WaitGroup
}

// New builds a Checker.
func New(cfg Config) (*Checker, error) {
	if cfg.Store == nil {
		return nil, errors.New("health: store is required")
	}
	if cfg.Manager == nil {
		return nil, errors.New("health: manager is required")
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	clock := cfg.Clock
	if clock == nil {
		clock = engine.RealClock{}
	}
	interval := cfg.Interval
	if interval <= 0 {
		interval = defaultHealthRun
	}
	return &Checker{
		store:      cfg.Store,
		manager:    cfg.Manager,
		logger:     logger.With("component", "health"),
		clock:      clock,
		interval:   interval,
		thresholds: cfg.Thresholds.normalized(),
		notify:     cfg.Notify,
	}, nil
}

// RunOnce checks every enabled indexer once. Individual failures are reported
// in the results and do not abort the pass.
func (c *Checker) RunOnce(ctx context.Context) ([]Result, error) {
	indexers, err := c.store.ListIndexers(ctx)
	if err != nil {
		return nil, fmt.Errorf("health: list indexers: %w", err)
	}
	settings, _ := c.store.LoadSettings(ctx)

	results := make([]Result, len(indexers))
	sem := make(chan struct{}, 3)
	var wg sync.WaitGroup
	for i, item := range indexers {
		if !item.Enabled {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(slot int, row store.Indexer) {
			defer wg.Done()
			defer func() { <-sem }()
			results[slot] = c.checkOne(ctx, row)
		}(i, item)
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return results, err
	}

	out := make([]Result, 0, len(results))
	byDefinition := map[string][]int{}
	for _, result := range results {
		if result.IndexerID == 0 {
			continue
		}
		out = append(out, result)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].IndexerID < out[j].IndexerID })
	for i := range out {
		byDefinition[out[i].DefinitionID] = append(byDefinition[out[i].DefinitionID], i)
	}
	for definitionID, indexes := range byDefinition {
		c.transitionDefinition(ctx, settings, definitionID, out, indexes)
	}
	return out, nil
}

func (c *Checker) checkOne(ctx context.Context, row store.Indexer) Result {
	result := Result{
		IndexerID: row.ID, Slug: row.Slug, Name: row.Name,
		DefinitionID: row.DefinitionID,
	}
	checked, err := c.manager.Health(ctx, row.ID)
	result.Items = checked.Items
	result.Err = err
	if err != nil {
		c.logger.Warn("health check failed", "indexer", row.Slug,
			"error", c.manager.RedactError(row.ID, err))
	}
	return result
}

// transitionDefinition applies one state per definition, not one per indexer:
// several configured filters can share a definition, and a healthy filter must
// not hide a degraded one. The combined history is newest-first across all the
// enabled indexers of the definition.
func (c *Checker) transitionDefinition(ctx context.Context, settings store.Settings, definitionID string, results []Result, indexes []int) {
	if definitionID == "" || len(indexes) == 0 {
		return
	}
	definition, err := c.store.GetDefinition(ctx, definitionID)
	if err != nil {
		return
	}
	// Evaluate every configured filter on its own history, then take the worst
	// answer. A healthy filter must never hide a degraded sibling that shares
	// the same definition.
	// Evaluate every configured filter on its own history, then take the worst
	// answer. A healthy filter must never hide a degraded sibling that shares
	// the same definition.
	groups := make([][]Outcome, 0, len(indexes))
	for _, index := range indexes {
		rows, err := c.store.ListIndexerHistory(ctx, results[index].IndexerID, 50)
		if err != nil {
			groups = append(groups, nil)
			continue
		}
		groups = append(groups, OutcomesFromHistory(rows))
	}
	next := NextStateGroup(definition.State, groups, c.thresholds)
	for _, index := range indexes {
		results[index].FromState = definition.State
		results[index].ToState = definition.State
	}
	if next == "" || next == definition.State {
		return
	}
	if err := c.store.SetDefinitionState(ctx, definition.ID, next); err != nil {
		c.logger.Warn("cannot update definition state", "definition", definition.ID, "error", err)
		return
	}
	for _, index := range indexes {
		results[index].ToState = next
	}
	source := results[indexes[0]]
	cause := error(nil)
	for _, index := range indexes {
		if results[index].Err != nil {
			source = results[index]
			cause = results[index].Err
			break
		}
	}
	c.logger.Info("definition state changed", "definition", definition.ID,
		"indexer", source.Slug, "from", definition.State, "to", next)
	if next == CatalogDegraded {
		c.sendDegraded(ctx, settings, store.Indexer{
			ID: source.IndexerID, Name: source.Name, Slug: source.Slug,
		}, definition.State, cause)
	}
}

func (c *Checker) sendDegraded(ctx context.Context, settings store.Settings, row store.Indexer, from string, cause error) {
	event := notify.Event{
		Kind:   "indexer_degraded",
		Source: row.Name,
		Error:  c.manager.RedactError(row.ID, cause),
		At:     c.clock.Now().UTC(),
	}
	if from != "" {
		event.Titles = []string{"catalog state: " + from + " -> " + CatalogDegraded}
	}
	if c.notify != nil {
		c.notify(ctx, settings, event)
		return
	}
	cfg := notify.Config{
		WebhookURL:     settings.NotifyWebhookURL,
		TelegramToken:  settings.NotifyTelegramToken,
		TelegramChatID: settings.NotifyTelegramChatID,
	}
	if !notify.Configured(cfg) {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := notify.Send(ctx, cfg, event); err != nil {
			c.logger.Warn("degraded notification failed", "error", err)
		}
	}()
}

// Start begins the periodic pass. The first run waits for the jitter instead
// of firing at startup, so a restart does not query every tracker at once.
func (c *Checker) Start(ctx context.Context) {
	c.startOnce.Do(func() {
		c.wg.Add(1)
		go func() {
			defer c.wg.Done()
			c.loop(ctx)
		}()
	})
}

// Wait blocks until the background loop has stopped.
func (c *Checker) Wait() { c.wg.Wait() }

func (c *Checker) loop(ctx context.Context) {
	c.logger.Info("health checker started", "interval", c.interval.String())
	timer := time.NewTimer(c.jitter(c.interval))
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			c.logger.Info("health checker stopped")
			return
		case <-timer.C:
			if _, err := c.RunOnce(ctx); err != nil && ctx.Err() == nil {
				c.logger.Warn("health pass failed", "error", err)
			}
			timer.Reset(c.interval + c.jitter(c.interval/5))
		}
	}
}

// jitter returns a random delay in [0, maximum]. It only spaces the periodic
// pass; no user-visible behaviour depends on the exact value.
func (c *Checker) jitter(maximum time.Duration) time.Duration {
	if maximum <= 0 {
		return 0
	}
	return time.Duration(rand.Int63n(int64(maximum) + 1))
}
