package torznabapi

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"strings"

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

// resolveIndexer finds a per-indexer route target. New-style slugs live in the
// indexers table; during the migration the legacy source name is accepted too.
func (s *Server) resolveIndexer(ctx context.Context, slug string) (store.Indexer, bool, error) {
	indexer, err := s.store.GetIndexerBySlug(ctx, slug)
	if err == nil {
		return indexer, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return store.Indexer{}, false, err
	}
	sources, err := s.store.ListSources(ctx)
	if err != nil {
		return store.Indexer{}, false, err
	}
	for _, source := range sources {
		if strings.EqualFold(strings.TrimSpace(source.Name), strings.TrimSpace(slug)) {
			return store.Indexer{
				Slug:           slug,
				Name:           source.Name,
				Enabled:        source.Enabled,
				Protocol:       "torrent",
				LegacySourceID: source.ID,
			}, true, nil
		}
	}
	return store.Indexer{}, false, nil
}

// releaseSelection is the resolved set of releases a request may see. It is
// built once per request so no per-release store lookups are needed.
type releaseSelection struct {
	all   bool
	ids   map[int64]struct{}
	names map[string]struct{}
}

func (sel releaseSelection) empty() bool {
	return !sel.all && len(sel.ids) == 0 && len(sel.names) == 0
}

func (sel releaseSelection) match(release store.Release) bool {
	if sel.all {
		return true
	}
	if _, ok := sel.ids[release.SourceID]; ok {
		return true
	}
	_, ok := sel.names[strings.ToLower(strings.TrimSpace(release.SourceName))]
	return ok
}

func selectionForIndexer(indexer store.Indexer) releaseSelection {
	if indexer.LegacySourceID > 0 {
		return releaseSelection{ids: map[int64]struct{}{indexer.LegacySourceID: {}}}
	}
	name := strings.ToLower(strings.TrimSpace(indexer.Name))
	if name == "" {
		return releaseSelection{}
	}
	return releaseSelection{names: map[string]struct{}{name: {}}}
}

// aggregateFilters are the Jackett-style indexer filters accepted by the
// aggregate endpoint.
type aggregateFilters struct {
	tags        []string
	indexerType string
	status      string
	lang        string
}

func parseAggregateFilters(query url.Values) aggregateFilters {
	return aggregateFilters{
		tags:        splitListValues(query["tag"]),
		indexerType: strings.TrimSpace(query.Get("indexer_type")),
		status:      strings.ToLower(strings.TrimSpace(query.Get("status"))),
		lang:        strings.TrimSpace(query.Get("lang")),
	}
}

func (f aggregateFilters) active() bool {
	return len(f.tags) > 0 || f.indexerType != "" || f.status != "" || f.lang != ""
}

// resolveAggregateSelection resolves tag, indexer_type, status and lang against
// the indexers table once per request. Indexers without a legacy source cannot
// own legacy releases yet, so they are skipped.
func (s *Server) resolveAggregateSelection(ctx context.Context, filters aggregateFilters) (releaseSelection, error) {
	if !filters.active() {
		return releaseSelection{all: true}, nil
	}
	indexers, err := s.store.ListIndexers(ctx)
	if err != nil {
		return releaseSelection{}, err
	}
	var definitions map[string]store.Definition
	if filters.indexerType != "" || filters.lang != "" {
		list, err := s.store.ListDefinitions(ctx)
		if err != nil {
			return releaseSelection{}, err
		}
		definitions = make(map[string]store.Definition, len(list))
		for _, definition := range list {
			definitions[definition.ID] = definition
		}
	}
	selected := releaseSelection{ids: map[int64]struct{}{}}
	for _, indexer := range indexers {
		if !filters.matchIndexer(indexer, definitions) {
			continue
		}
		if indexer.LegacySourceID <= 0 {
			continue
		}
		selected.ids[indexer.LegacySourceID] = struct{}{}
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
			return false
		}
	}
	if f.lang != "" && hasDefinition && !languageMatches(definitionLanguage(definition.Body), f.lang) {
		return false
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

// applySelection post-filters a page only when the store query could not
// express the selection. A name-only match (legacy sources resolved by name)
// cannot be pushed into SQL; id selections are pushed down through
// Filter.SourceIDs, so totals and pagination stay correct.
func applySelection(sel releaseSelection, sqlSourceID int64, releases []store.Release, total int64) ([]store.Release, int64) {
	if sel.all || sqlSourceID > 0 || len(sel.ids) > 0 {
		return releases, total
	}
	filtered := releases[:0]
	for _, release := range releases {
		if sel.match(release) {
			filtered = append(filtered, release)
		}
	}
	return filtered, int64(len(filtered))
}
