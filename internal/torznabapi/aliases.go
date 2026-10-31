package torznabapi

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"strings"

	"github.com/buzzqw/indexarr/internal/definition"
	"github.com/buzzqw/indexarr/internal/health"
	"github.com/buzzqw/indexarr/internal/store"
	"gopkg.in/yaml.v3"
)

const (
	pathJackettPrefix = "/api/v2.0/indexers/"
	pathJackettSuffix = "/results/torznab/api"
	pathIndexerPrefix = "/torznab/"
	pathIndexerSuffix = "/api"
	slugAll           = "all"
)

// torznabPath returns the indexer slug addressed by path and whether the path
// is a Torznab endpoint. Aggregate endpoints use the pseudo-slug "all".
func torznabPath(path string) (string, bool) {
	switch path {
	case pathAPI, pathNewznab, pathTorznab:
		return slugAll, true
	}
	if slug, ok := pathBetween(path, pathJackettPrefix, pathJackettSuffix); ok {
		return slug, true
	}
	if slug, ok := pathBetween(path, pathIndexerPrefix, pathIndexerSuffix); ok {
		return slug, true
	}
	return "", false
}

func pathBetween(path, prefix, suffix string) (string, bool) {
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, suffix) {
		return "", false
	}
	slug := strings.Trim(path[len(prefix):len(path)-len(suffix)], "/")
	if slug == "" || strings.Contains(slug, "/") {
		return "", false
	}
	return slug, true
}

func isAggregateSlug(slug string) bool {
	return slug == "" || strings.EqualFold(slug, slugAll)
}

// resolveIndexer finds a per-indexer route target. The slug is the native
// indexer slug; a legacy client that still uses the old source name is matched
// against the indexer name as a compatibility fallback.
func (s *Server) resolveIndexer(ctx context.Context, slug string) (store.Indexer, bool, error) {
	indexer, err := s.store.GetIndexerBySlug(ctx, slug)
	if err == nil {
		return indexer, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return store.Indexer{}, false, err
	}
	indexers, err := s.store.ListIndexers(ctx)
	if err != nil {
		return store.Indexer{}, false, err
	}
	for _, candidate := range indexers {
		if strings.EqualFold(strings.TrimSpace(candidate.Name), strings.TrimSpace(slug)) {
			return candidate, true, nil
		}
	}
	return store.Indexer{}, false, nil
}

// releaseSelection is the resolved set of indexers a request may see. Release
// provenance lives in release_sources, so the selection is pushed into SQL.
type releaseSelection struct {
	all   bool
	ids   map[int64]struct{}
	names map[int64]string
}

func (sel releaseSelection) empty() bool {
	return !sel.all && len(sel.ids) == 0
}

func selectionForIndexer(indexer store.Indexer) releaseSelection {
	if indexer.ID <= 0 {
		return releaseSelection{}
	}
	return releaseSelection{
		ids:   map[int64]struct{}{indexer.ID: {}},
		names: map[int64]string{indexer.ID: indexer.Name},
	}
}

// aggregateFilters are the Jackett-style indexer filters accepted by the
// aggregate endpoint.
type aggregateFilters struct {
	tags        []string
	indexerType string
	status      string
	lang        string
	// test matches the last health check of a source: passed or failed.
	// It is separate from status because Jackett keeps "did the last test
	// work" apart from "is the source usable now".
	test string
}

func parseAggregateFilters(query url.Values) aggregateFilters {
	return aggregateFilters{
		tags:        splitListValues(query["tag"]),
		indexerType: strings.TrimSpace(query.Get("indexer_type")),
		status:      strings.ToLower(strings.TrimSpace(query.Get("status"))),
		lang:        strings.TrimSpace(query.Get("lang")),
		test:        strings.ToLower(strings.TrimSpace(query.Get("test"))),
	}
}

func (f aggregateFilters) active() bool {
	return len(f.tags) > 0 || f.indexerType != "" || f.status != "" || f.lang != "" || f.test != ""
}

// healthStatus reports whether status names a health state rather than the
// enabled/disabled column. The values mirror the UI health model and Jackett's
// filter vocabulary.
func (f aggregateFilters) healthStatus() bool {
	switch f.status {
	case "healthy", "ok", "failing", "failed", "degraded", "problem", "unknown", "testing":
		return true
	default:
		return false
	}
}

func (f aggregateFilters) wantsHealth() bool {
	return f.healthStatus() || f.test != ""
}

// resolveAggregateSelection resolves tag, indexer_type, status, lang and test
// against the indexers table once per request.
func (s *Server) resolveAggregateSelection(ctx context.Context, filters aggregateFilters) (releaseSelection, error) {
	if !filters.active() {
		return releaseSelection{all: true}, nil
	}
	indexers, err := s.store.ListIndexers(ctx)
	if err != nil {
		return releaseSelection{}, err
	}
	var definitions map[string]store.Definition
	if filters.indexerType != "" || filters.lang != "" || filters.wantsHealth() {
		list, err := s.store.ListDefinitions(ctx)
		if err != nil {
			return releaseSelection{}, err
		}
		definitions = make(map[string]store.Definition, len(list))
		for _, definition := range list {
			definitions[definition.ID] = definition
		}
	}
	var healthInfo map[int64]indexerHealthInfo
	if filters.wantsHealth() {
		healthInfo, err = s.loadIndexerHealth(ctx, indexers, definitions)
		if err != nil {
			return releaseSelection{}, err
		}
	}
	selected := releaseSelection{
		ids:   map[int64]struct{}{},
		names: map[int64]string{},
	}
	for _, indexer := range indexers {
		if !filters.matchIndexer(indexer, definitions) {
			continue
		}
		if filters.wantsHealth() && !filters.matchHealth(healthInfo[indexer.ID]) {
			continue
		}
		selected.ids[indexer.ID] = struct{}{}
		selected.names[indexer.ID] = indexer.Name
	}
	return selected, nil
}

func (f aggregateFilters) matchIndexer(indexer store.Indexer, definitions map[string]store.Definition) bool {
	if len(f.tags) > 0 && !tagsMatch(indexer.Tags, f.tags) {
		return false
	}
	definition, hasDefinition := definitions[indexer.DefinitionID]
	if f.indexerType != "" {
		want := strings.ToLower(f.indexerType)
		matches := strings.EqualFold(indexer.Protocol, want) ||
			(hasDefinition && strings.EqualFold(definition.Family, want))
		if !matches {
			return false
		}
	}
	if f.status != "" {
		switch f.status {
		case "enabled", "active", "on":
			if !indexer.Enabled {
				return false
			}
		case "disabled", "inactive", "off":
			if indexer.Enabled {
				return false
			}
		default:
			// Health states are resolved after the state and history are
			// loaded for the whole selection; anything else is unknown.
			if !f.healthStatus() {
				return false
			}
		}
	}
	if f.lang != "" && hasDefinition && !languageMatches(definitionLanguage(definition.Body), f.lang) {
		return false
	}
	return true
}

// indexerHealthInfo is the part of the health model the aggregate filter
// needs: the assessment and the last manual/periodic test.
type indexerHealthInfo struct {
	assessment health.Assessment
	testStatus string
	hasTest    bool
}

// loadIndexerHealth reads the state, the health-relevant history and the
// definition connector of the selected indexers in batches. It shares the
// health model with the web UI, so the API cannot disagree with the page.
func (s *Server) loadIndexerHealth(ctx context.Context, indexers []store.Indexer, definitions map[string]store.Definition) (map[int64]indexerHealthInfo, error) {
	ids := make([]int64, 0, len(indexers))
	for _, indexer := range indexers {
		if indexer.ID > 0 {
			ids = append(ids, indexer.ID)
		}
	}
	states, err := s.store.IndexerStates(ctx)
	if err != nil {
		return nil, err
	}
	history, err := s.store.ListIndexerHealthHistory(ctx, ids, 50)
	if err != nil {
		return nil, err
	}
	feedOnly := make(map[string]bool, len(definitions))
	for id, def := range definitions {
		parsed, err := definition.ParseShared([]byte(def.Body))
		if err == nil && parsed != nil {
			feedOnly[id] = strings.EqualFold(strings.TrimSpace(parsed.Connector), "rss")
		}
	}
	out := make(map[int64]indexerHealthInfo, len(indexers))
	for _, indexer := range indexers {
		state := states[indexer.ID]
		info := indexerHealthInfo{}
		for _, row := range history[indexer.ID] {
			if strings.EqualFold(strings.TrimSpace(row.Kind), "health") {
				info.testStatus = strings.ToLower(strings.TrimSpace(row.Status))
				info.hasTest = true
				break
			}
		}
		info.assessment = health.Assess(health.Input{
			Enabled:   indexer.Enabled,
			Circuit:   state.Circuit,
			LastError: state.LastError,
			History:   history[indexer.ID],
			FeedOnly:  feedOnly[indexer.DefinitionID],
		}, health.DefaultThresholds())
		out[indexer.ID] = info
	}
	return out, nil
}

// matchHealth applies status=healthy|failing|unknown and test=passed|failed.
// A disabled indexer is not healthy, and a canceled or paused check is not a
// failed one: it did not test the source.
func (f aggregateFilters) matchHealth(info indexerHealthInfo) bool {
	if f.test != "" {
		passed := info.hasTest && info.testStatus == "ok"
		failed := info.hasTest && info.testStatus != "" && info.testStatus != "ok" &&
			info.testStatus != "canceled" && info.testStatus != "paused"
		switch f.test {
		case "passed", "pass", "ok":
			if !passed {
				return false
			}
		case "failed", "fail", "error":
			if !failed {
				return false
			}
		default:
			return false
		}
	}
	switch f.status {
	case "healthy", "ok":
		return info.assessment.State == health.StateOK
	case "failing", "failed", "degraded", "problem":
		return info.assessment.State == health.StateDegraded || info.assessment.State == health.StateProblem
	case "unknown", "testing":
		return info.assessment.State == health.StateTesting
	}
	return true
}

func tagsMatch(have string, want []string) bool {
	available := map[string]struct{}{}
	for _, tag := range splitList(have) {
		available[strings.ToLower(tag)] = struct{}{}
	}
	for _, tag := range want {
		if _, ok := available[strings.ToLower(tag)]; ok {
			return true
		}
	}
	return false
}

func languageMatches(have, want string) bool {
	have = strings.ToLower(strings.TrimSpace(have))
	want = strings.ToLower(strings.TrimSpace(want))
	if have == "" || want == "" {
		// Missing meta.language means "no language filter": ignore it.
		return true
	}
	if have == want {
		return true
	}
	return strings.HasPrefix(have, want+"-") || strings.HasPrefix(want, have+"-")
}

func definitionLanguage(body string) string {
	if strings.TrimSpace(body) == "" {
		return ""
	}
	var doc struct {
		Meta struct {
			Language string `yaml:"language"`
		} `yaml:"meta"`
	}
	if err := yaml.Unmarshal([]byte(body), &doc); err != nil {
		return ""
	}
	return strings.TrimSpace(doc.Meta.Language)
}

func splitList(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func splitListValues(values []string) []string {
	var out []string
	for _, value := range values {
		out = append(out, splitList(value)...)
	}
	return out
}
