package indexer

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/buzzqw/indexarr/internal/engine"
	"github.com/buzzqw/indexarr/internal/notify"
	"github.com/buzzqw/indexarr/internal/store"
)

// credentialPattern masks the query parameters that regularly carry tracker
// credentials in transport errors.
var credentialPattern = regexp.MustCompile(`(?i)(apikey|api_key|api_token|passkey|token|password)=([^&\s"']+)`)

func redactQuerySecrets(text string) string {
	return credentialPattern.ReplaceAllString(text, "$1=***")
}

// redactValues masks every known secret value longer than a couple of
// characters, so a connector error cannot echo credentials.
func redactValues(text string, values []string) string {
	for _, value := range values {
		if len(value) >= 3 {
			text = strings.ReplaceAll(text, value, "[redacted]")
		}
	}
	return redactQuerySecrets(text)
}

// redactedError keeps the original error for errors.Is/As while exposing a
// redacted message.
type redactedError struct {
	message string
	cause   error
}

func (e *redactedError) Error() string { return e.message }
func (e *redactedError) Unwrap() error { return e.cause }

func redactError(err error, values []string) error {
	if err == nil {
		return nil
	}
	message := redactValues(err.Error(), values)
	if message == err.Error() {
		return err
	}
	return &redactedError{message: message, cause: err}
}

// statusFor classifies an operation. A caller cancellation wins over every
// other classification because it says why the run actually stopped. A
// Cloudflare challenge is "blocked": it is not a generic failure and it has
// its own cause and remedy in the interface.
func statusFor(ctx context.Context, err error) string {
	if ctx != nil && ctx.Err() != nil {
		return "canceled"
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "canceled"
	}
	if errors.Is(err, engine.ErrChallenge) {
		return "blocked"
	}
	if errors.Is(err, engine.ErrSearchDeferred) {
		// Not sent at all: the site's flood interval had no free slot and the
		// search waits in the background queue. The site did not fail.
		return "canceled"
	}
	if err != nil {
		return "error"
	}
	return "ok"
}

// recordHistory appends one indexer_history row and, when stats is not nil,
// the matching metrics row in the same transaction. It never fails the caller:
// a history write error is logged and ignored.
func (m *Manager) recordHistory(ctx context.Context, indexerID int64, kind, query, status string, started time.Time, bytes int64, opErr error, stats *RunStats) int64 {
	base := context.WithoutCancel(ctx)
	errorValue := ""
	if opErr != nil {
		errorValue = m.redact(indexerID, errorText(opErr))
	}
	var metrics *store.RunMetrics
	if stats != nil {
		metrics = stats.Metrics()
	}
	id, err := m.store.RecordIndexerHistory(base, store.IndexerHistory{
		IndexerID: indexerID,
		Kind:      kind,
		Query:     truncate(query, 300),
		Status:    status,
		LatencyMS: time.Since(started).Milliseconds(),
		Bytes:     bytes,
		Error:     errorValue,
		StartedAt: started,
		Metrics:   metrics,
	})
	if err != nil {
		m.logger.Warn("indexer: record history",
			"indexer_id", indexerID, "kind", kind, "error", err)
	}
	return id
}

// queryHistoryKinds are the indexer operations counted against the daily query
// limit. A rejected attempt is not written for Search/Latest, and canceled
// rows are excluded by CountIndexerQueries: the count reflects requests that
// really left for the site.
var queryHistoryKinds = []string{"search", "latest", "run"}

// deferredSearchObserver returns the callback the definition connector calls
// for every search page the background worker sends. Without these rows a
// deferred search is visible only as a canceled row with the flood code: the
// real send, its latency and the site's answer (accepted, refused, dropped
// after the retries) never reach the history.
func (m *Manager) deferredSearchObserver(indexer store.Indexer) engine.DeferredSearchObserver {
	return func(event engine.DeferredSearchEvent) {
		ctx := context.Background()
		status := statusFor(ctx, event.Err)
		err := event.Err
		if err != nil {
			err = fmt.Errorf("indexer %s: background search: %w", indexer.Slug, err)
		}
		m.recordHistory(ctx, indexer.ID, "search", event.Query, status,
			event.Started, event.Bytes, err, nil)
	}
}

// checkQueryLimit enforces indexer.QueryLimit over a rolling 24-hour window
// before the connector is touched. A zero limit disables the check.
func (m *Manager) checkQueryLimit(ctx context.Context, indexerID int64) error {
	indexer, err := m.store.GetIndexer(ctx, indexerID)
	if err != nil {
		return fmt.Errorf("indexer %d: %w", indexerID, err)
	}
	if indexer.QueryLimit <= 0 {
		return nil
	}
	count, err := m.store.CountIndexerQueries(ctx, indexerID, queryHistoryKinds,
		time.Now().Add(-24*time.Hour))
	if err != nil {
		return fmt.Errorf("indexer %s: count queries: %w", indexer.Slug, err)
	}
	if count >= indexer.QueryLimit {
		return fmt.Errorf("indexer %s: %w (%d/%d in 24h)",
			indexer.Slug, ErrQueryLimit, count, indexer.QueryLimit)
	}
	return nil
}

// CheckGrab enforces indexer.GrabLimit over a rolling 24-hour window. It is
// called before a release payload is served; a zero limit disables the check.
func (m *Manager) CheckGrab(ctx context.Context, indexerID int64) error {
	indexer, err := m.store.GetIndexer(ctx, indexerID)
	if err != nil {
		return fmt.Errorf("indexer %d: %w", indexerID, err)
	}
	if indexer.GrabLimit <= 0 {
		return nil
	}
	count, err := m.store.CountIndexerHistory(ctx, indexerID, []string{"grab"},
		time.Now().Add(-24*time.Hour))
	if err != nil {
		return fmt.Errorf("indexer %s: count grabs: %w", indexer.Slug, err)
	}
	if count >= indexer.GrabLimit {
		return fmt.Errorf("indexer %s: %w (%d/%d in 24h)",
			indexer.Slug, ErrGrabLimit, count, indexer.GrabLimit)
	}
	return nil
}

// Grab delivery kinds, stored in the status column of a grab row.
const (
	GrabViaTorrent = "ok"
	GrabViaMagnet  = "magnet"
	GrabViaNZB     = "nzb"
	// GrabViaEd2k marks a release delivered as an ed2k:// link.
	GrabViaEd2k = "ed2k"
)

type grabViaKey struct{}

// WithGrabVia tells RecordGrab how the grab was delivered.
func WithGrabVia(ctx context.Context, via string) context.Context {
	return context.WithValue(ctx, grabViaKey{}, via)
}

// GrabViaFromContext returns the delivery kind set by WithGrabVia.
func GrabViaFromContext(ctx context.Context) (string, bool) {
	via, ok := ctx.Value(grabViaKey{}).(string)
	return via, ok && via != ""
}

// RecordGrab appends a grab history row. Callers invoke it only after a
// download was served successfully. The app comes from notify.WithApp and the
// delivery kind from WithGrabVia; a plain torrent is the default.
func (m *Manager) RecordGrab(ctx context.Context, indexerID int64, releaseID int64, title string) error {
	status := GrabViaTorrent
	if via, ok := GrabViaFromContext(ctx); ok {
		status = via
	}
	_, err := m.store.RecordIndexerHistory(ctx, store.IndexerHistory{
		IndexerID: indexerID,
		Kind:      "grab",
		Query:     truncate(title, 300),
		Status:    status,
		App:       notify.AppFrom(ctx),
		StartedAt: time.Now(),
	})
	if err != nil {
		return fmt.Errorf("indexer %d: record grab for release %d: %w", indexerID, releaseID, err)
	}
	return nil
}

// Search runs a search through the cached connector and records history,
// metrics and the last successful sample.
func (m *Manager) Search(ctx context.Context, indexerID int64, req engine.SearchRequest) ([]engine.Item, error) {
	started := time.Now()
	stats := &RunStats{}
	if err := m.checkQueryLimit(ctx, indexerID); err != nil {
		return nil, err
	}
	connector, indexer, err := m.Connector(ctx, indexerID)
	if err != nil {
		m.recordHistory(ctx, indexerID, "search", req.Query, statusFor(ctx, err), started, 0, err, stats)
		return nil, err
	}
	capture := newRunCapture(m.sampleSecrets(ctx, indexer.ID))
	ctx = engine.WithResponseObserver(ctx, capture.observe)
	ctx = engine.WithNormalizeObserver(ctx, func(observation engine.NormalizeObservation) {
		if !observation.OK {
			stats.discardParse()
		}
	})
	items, err := connector.Search(ctx, req)
	if err != nil {
		err = fmt.Errorf("indexer %s: search: %w", indexer.Slug, err)
	} else {
		stats.Seen = len(items)
		for _, item := range items {
			stats.observeItem(item, capture.secrets)
		}
	}
	m.recordHistory(ctx, indexer.ID, "search", req.Query, statusFor(ctx, err), started, 0, err, stats)
	if err == nil {
		m.recordSample(context.WithoutCancel(ctx), indexer.ID, capture, stats)
	}
	return items, err
}

// Latest runs a latest feed request through the cached connector and records
// history, metrics and the last successful sample.
func (m *Manager) Latest(ctx context.Context, indexerID int64, req engine.LatestRequest) ([]engine.Item, error) {
	started := time.Now()
	stats := &RunStats{}
	if err := m.checkQueryLimit(ctx, indexerID); err != nil {
		return nil, err
	}
	connector, indexer, err := m.Connector(ctx, indexerID)
	if err != nil {
		m.recordHistory(ctx, indexerID, "latest", "", statusFor(ctx, err), started, 0, err, stats)
		return nil, err
	}
	capture := newRunCapture(m.sampleSecrets(ctx, indexer.ID))
	ctx = engine.WithResponseObserver(ctx, capture.observe)
	ctx = engine.WithNormalizeObserver(ctx, func(observation engine.NormalizeObservation) {
		if !observation.OK {
			stats.discardParse()
		}
	})
	items, err := connector.Latest(ctx, req)
	if err != nil {
		err = fmt.Errorf("indexer %s: latest: %w", indexer.Slug, err)
	} else {
		stats.Seen = len(items)
		for _, item := range items {
			stats.observeItem(item, capture.secrets)
		}
	}
	m.recordHistory(ctx, indexer.ID, "latest", "", statusFor(ctx, err), started, 0, err, stats)
	if err == nil {
		m.recordSample(context.WithoutCancel(ctx), indexer.ID, capture, stats)
	}
	return items, err
}

// Download fetches the payload for one item through the cached connector and
// records history. Errors never contain the tracker download URL.
func (m *Manager) Download(ctx context.Context, indexerID int64, item engine.Item) (engine.Payload, error) {
	started := time.Now()
	connector, indexer, err := m.Connector(ctx, indexerID)
	if err != nil {
		m.recordHistory(ctx, indexerID, "download", "", statusFor(ctx, err), started, 0, err, nil)
		return engine.Payload{}, err
	}
	payload, err := connector.Download(ctx, item)
	if err != nil {
		err = fmt.Errorf("indexer %s: download: %w", indexer.Slug, err)
	}
	m.recordHistory(ctx, indexer.ID, "download", "", statusFor(ctx, err), started, int64(len(payload.Body)), err, nil)
	return payload, err
}

// Test runs the connector's phased self-test and records history. The error
// column carries a compact phase summary only when the test fails.
func (m *Manager) Test(ctx context.Context, indexerID int64) (engine.TestResult, error) {
	started := time.Now()
	stats := &RunStats{}
	connector, indexer, err := m.Connector(ctx, indexerID)
	if err != nil {
		m.recordHistory(ctx, indexerID, "test", "", statusFor(ctx, err), started, 0, err, stats)
		return engine.TestResult{}, err
	}
	capture := newRunCapture(m.sampleSecrets(ctx, indexer.ID))
	ctx = engine.WithResponseObserver(ctx, capture.observe)
	ctx = engine.WithNormalizeObserver(ctx, func(observation engine.NormalizeObservation) {
		if !observation.OK {
			stats.discardParse()
		}
	})
	result, err := connector.Test(ctx)
	if err != nil {
		err = fmt.Errorf("indexer %s: test: %w", indexer.Slug, err)
	}
	historyErr := err
	if historyErr == nil && !result.OK {
		historyErr = errors.New(phasesSummary(result))
	}
	m.recordHistory(ctx, indexer.ID, "test", "", statusFor(ctx, historyErr), started, 0, historyErr, stats)
	if historyErr == nil {
		// A successful manual test is a health check too: record it so the
		// old failure disappears from the status without waiting for the next
		// scheduled check.
		m.recordHistory(ctx, indexer.ID, "health", "", "ok", started, 0, nil, stats)
		m.clearStateError(ctx, indexer.ID)
		m.recordSample(context.WithoutCancel(ctx), indexer.ID, capture, stats)
	}
	return result, err
}

// clearStateError removes a stale last error after a successful check.
func (m *Manager) clearStateError(ctx context.Context, indexerID int64) {
	state, err := m.store.GetIndexerState(ctx, indexerID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		m.logger.Warn("indexer: load state", "indexer_id", indexerID, "error", err)
		return
	}
	state.IndexerID = indexerID
	if strings.TrimSpace(state.Circuit) == "" {
		state.Circuit = "closed"
	}
	state.LastError = ""
	state.UpdatedAt = time.Now()
	if err := m.store.UpsertIndexerState(ctx, state); err != nil {
		m.logger.Warn("indexer: update state", "indexer_id", indexerID, "error", err)
	}
}

// phasesSummary keeps only the failed phases, truncated to a compact row.
func phasesSummary(result engine.TestResult) string {
	var parts []string
	for _, phase := range result.Phases {
		if phase.OK {
			continue
		}
		message := strings.TrimSpace(phase.Message)
		if message == "" {
			message = "failed"
		}
		parts = append(parts, phase.Name+": "+message)
	}
	if len(parts) == 0 {
		return "test failed"
	}
	return truncate(strings.Join(parts, "; "), 500)
}

func truncate(text string, limit int) string {
	text = strings.TrimSpace(text)
	if limit <= 0 || len(text) <= limit {
		return text
	}
	return text[:limit] + "…"
}
