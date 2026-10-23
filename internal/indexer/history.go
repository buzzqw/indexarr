package indexer

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/buzzqw/indexarr/internal/engine"
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

// statusFor classifies an operation. Cancellation wins over a generic error so
// the history shows why the run stopped.
func statusFor(ctx context.Context, err error) string {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "canceled"
	}
	if ctx != nil && ctx.Err() != nil {
		return "canceled"
	}
	if err != nil {
		return "error"
	}
	return "ok"
}

// recordHistory appends one indexer_history row. It never fails the caller: a
// history write error is logged and ignored.
func (m *Manager) recordHistory(ctx context.Context, indexerID int64, kind, query, status string, started time.Time, bytes int64, opErr error) {
	base := context.WithoutCancel(ctx)
	errorValue := ""
	if opErr != nil {
		errorValue = m.redact(indexerID, errorText(opErr))
	}
	_, err := m.store.RecordIndexerHistory(base, store.IndexerHistory{
		IndexerID: indexerID,
		Kind:      kind,
		Query:     truncate(query, 300),
		Status:    status,
		LatencyMS: time.Since(started).Milliseconds(),
		Bytes:     bytes,
		Error:     errorValue,
		StartedAt: started,
	})
	if err != nil {
		m.logger.Warn("indexer: record history",
			"indexer_id", indexerID, "kind", kind, "error", err)
	}
}

// Search runs a search through the cached connector and records history.
func (m *Manager) Search(ctx context.Context, indexerID int64, req engine.SearchRequest) ([]engine.Item, error) {
	started := time.Now()
	connector, indexer, err := m.Connector(ctx, indexerID)
	if err != nil {
		m.recordHistory(ctx, indexerID, "search", req.Query, statusFor(ctx, err), started, 0, err)
		return nil, err
	}
	items, err := connector.Search(ctx, req)
	if err != nil {
		err = fmt.Errorf("indexer %s: search: %w", indexer.Slug, err)
	}
	m.recordHistory(ctx, indexer.ID, "search", req.Query, statusFor(ctx, err), started, 0, err)
	return items, err
}

// Latest runs a latest feed request through the cached connector and records
// history.
func (m *Manager) Latest(ctx context.Context, indexerID int64, req engine.LatestRequest) ([]engine.Item, error) {
	started := time.Now()
	connector, indexer, err := m.Connector(ctx, indexerID)
	if err != nil {
		m.recordHistory(ctx, indexerID, "latest", "", statusFor(ctx, err), started, 0, err)
		return nil, err
	}
	items, err := connector.Latest(ctx, req)
	if err != nil {
		err = fmt.Errorf("indexer %s: latest: %w", indexer.Slug, err)
	}
	m.recordHistory(ctx, indexer.ID, "latest", "", statusFor(ctx, err), started, 0, err)
	return items, err
}

// Download fetches the payload for one item through the cached connector and
// records history. Errors never contain the tracker download URL.
func (m *Manager) Download(ctx context.Context, indexerID int64, item engine.Item) (engine.Payload, error) {
	started := time.Now()
	connector, indexer, err := m.Connector(ctx, indexerID)
	if err != nil {
		m.recordHistory(ctx, indexerID, "download", "", statusFor(ctx, err), started, 0, err)
		return engine.Payload{}, err
	}
	payload, err := connector.Download(ctx, item)
	if err != nil {
		err = fmt.Errorf("indexer %s: download: %w", indexer.Slug, err)
	}
	m.recordHistory(ctx, indexer.ID, "download", "", statusFor(ctx, err), started, int64(len(payload.Body)), err)
	return payload, err
}

// Test runs the connector's phased self-test and records history. The error
// column carries a compact phase summary only when the test fails.
func (m *Manager) Test(ctx context.Context, indexerID int64) (engine.TestResult, error) {
	started := time.Now()
	connector, indexer, err := m.Connector(ctx, indexerID)
	if err != nil {
		m.recordHistory(ctx, indexerID, "test", "", statusFor(ctx, err), started, 0, err)
		return engine.TestResult{}, err
	}
	result, err := connector.Test(ctx)
	if err != nil {
		err = fmt.Errorf("indexer %s: test: %w", indexer.Slug, err)
	}
	historyErr := err
	if historyErr == nil && !result.OK {
		historyErr = errors.New(phasesSummary(result))
	}
	m.recordHistory(ctx, indexer.ID, "test", "", statusFor(ctx, historyErr), started, 0, historyErr)
	return result, err
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
