package indexer

import (
	"context"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/buzzqw/indexarr/internal/engine"
	"github.com/buzzqw/indexarr/internal/feed"
	"github.com/buzzqw/indexarr/internal/store"
)

// runCapture keeps the first HTTP response of one execution. The response is
// truncated and redacted before it can reach the store: a sample is a
// diagnostic, not a copy of the tracker page.
type runCapture struct {
	mu          sync.Mutex
	observation engine.ResponseObservation
	has         bool
	secrets     []string
}

func newRunCapture(secrets []string) *runCapture {
	return &runCapture{secrets: append([]string(nil), secrets...)}
}

func (c *runCapture) observe(observation engine.ResponseObservation) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.has {
		return
	}
	c.observation = observation
	c.has = true
}

// sample builds the row for indexer_samples. It returns false when the run
// did not perform any HTTP request.
func (c *runCapture) sample(indexerID int64, stats *RunStats) (store.IndexerSample, bool) {
	if c == nil {
		return store.IndexerSample{}, false
	}
	c.mu.Lock()
	observation := c.observation
	has := c.has
	c.mu.Unlock()
	if !has {
		return store.IndexerSample{}, false
	}

	body := feed.RedactText(string(observation.Body), c.secrets)
	bodyBytes := int64(len(observation.Body))
	truncated := bodyBytes >= int64(engine.ObservationBodyLimit)
	if len(body) > engine.ObservationBodyLimit {
		body = truncateUTF8(body, engine.ObservationBodyLimit)
		truncated = true
	}
	items := []store.SampleItem{}
	if stats != nil {
		items = stats.SampleItems
	}
	return store.IndexerSample{
		IndexerID:  indexerID,
		RequestURL: feed.RedactURL(observation.URL),
		Method:     observation.Method,
		Status:     observation.Status,
		LatencyMS:  observation.Duration.Milliseconds(),
		Body:       body,
		BodyBytes:  bodyBytes,
		Truncated:  truncated,
		Items:      items,
		ItemsCount: len(items),
		FetchedAt:  time.Now(),
	}, true
}

// recordSample writes the last successful request of an execution. It never
// fails the caller: a sample is diagnostics and must not turn a good run into
// an error.
func (m *Manager) recordSample(ctx context.Context, indexerID int64, capture *runCapture, stats *RunStats) {
	if capture == nil || indexerID <= 0 {
		return
	}
	sample, ok := capture.sample(indexerID, stats)
	if !ok {
		return
	}
	if err := m.store.PutIndexerSample(ctx, sample); err != nil {
		m.logger.Warn("indexer: record sample", "indexer_id", indexerID, "error", err)
	}
}

// sampleSecrets returns the plaintext values that must never reach a sample.
// It covers the vault-sealed indexer secrets plus any sensitive setting kept
// in config_json, so an installation that stores an API key or a feed URL
// there is protected too.
func (m *Manager) sampleSecrets(ctx context.Context, indexerID int64) []string {
	var out []string
	if indexerID > 0 {
		if indexer, err := m.store.GetIndexer(ctx, indexerID); err == nil {
			if config, err := decodeConfig(indexer.ConfigJSON); err == nil {
				for key, value := range config {
					if !sensitiveSettingName(key) {
						continue
					}
					if text := strings.TrimSpace(settingString(value)); len(text) >= 3 {
						out = append(out, text)
					}
				}
			}
		}
	}
	m.mu.Lock()
	if entry, ok := m.cache[indexerID]; ok {
		out = append(out, entry.secrets...)
	}
	m.mu.Unlock()
	return out
}

// truncateUTF8 cuts text at limit bytes without leaving an invalid tail.
func truncateUTF8(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(text) <= limit {
		return text
	}
	for limit > 0 && !utf8.RuneStart(text[limit]) {
		limit--
	}
	return text[:limit]
}

func sensitiveSettingName(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "api_key", "apikey", "api_token", "passkey", "rsskey", "authkey",
		"token", "secret", "password", "key":
		return true
	}
	return false
}
