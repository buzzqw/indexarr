package indexer

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/buzzqw/indexarr/internal/definition"
	"github.com/buzzqw/indexarr/internal/engine"
	"github.com/buzzqw/indexarr/internal/store"
	"github.com/buzzqw/indexarr/internal/textutil"
)

// ErrNoCatalog means the indexer's definition does not keep a local topic
// history.
var ErrNoCatalog = errors.New("indexer: no topic catalog")

// catalogConnector is the part of a definition connector the topic history
// needs. It is a separate interface so every other connector stays unchanged.
type catalogConnector interface {
	Catalog(ctx context.Context, req engine.CatalogRequest) (engine.CatalogResult, error)
	CatalogForums() []definition.CatalogForum
}

// CatalogOutcome is what one catalogue page did.
type CatalogOutcome struct {
	IndexerID int64
	Forum     string
	Start     int
	Topics    int
	Added     int
	NextStart int
	Finished  bool
	Duration  time.Duration
}

// CatalogSupported reports whether the indexer's definition keeps a topic
// history. The runner uses it to schedule the slow walk.
func (m *Manager) CatalogSupported(ctx context.Context, indexer store.Indexer) bool {
	return m.catalogDefinition(ctx, indexer) != nil
}

// catalogDefinition returns the parsed definition when it keeps a topic
// history. The body is parsed only when it mentions a catalog, so the periodic
// pass does not pay for 50 YAML documents at every tick.
func (m *Manager) catalogDefinition(ctx context.Context, indexer store.Indexer) *definition.Definition {
	def, err := m.store.GetDefinition(ctx, indexer.DefinitionID)
	if err != nil || !strings.Contains(def.Body, "catalog:") {
		return nil
	}
	parsed, err := definition.Parse([]byte(def.Body))
	if err != nil || !parsed.HasCatalog() {
		return nil
	}
	return parsed
}

// RunCatalogPage reads one page of one forum and stores its topics. The forum
// with the oldest run goes first, so every forum advances without a separate
// schedule; a page that ends the listing sends the cursor back to the first
// row, ready for the next pass. The page is counted against the indexer's
// daily query limit, because it is a request to the site.
func (m *Manager) RunCatalogPage(ctx context.Context, indexerID int64) (outcome CatalogOutcome, err error) {
	defer m.lockRun(indexerID)()
	started := time.Now()
	outcome = CatalogOutcome{IndexerID: indexerID}
	stats := &RunStats{}
	if err := m.checkQueryLimit(ctx, indexerID); err != nil {
		return outcome, err
	}
	connector, indexer, err := m.Connector(ctx, indexerID)
	defer func() {
		outcome.Duration = time.Since(started)
		kind := "catalog"
		status := statusFor(ctx, err)
		query := outcome.Forum
		if query != "" {
			query = fmt.Sprintf("%s start=%d", outcome.Forum, outcome.Start)
		}
		m.recordHistory(ctx, indexerID, kind, query, status, started, 0, err, stats)
	}()
	if err != nil {
		return outcome, err
	}
	catalog, ok := connector.(catalogConnector)
	if !ok {
		return outcome, ErrNoCatalog
	}
	forums := catalog.CatalogForums()
	if len(forums) == 0 {
		return outcome, ErrNoCatalog
	}
	cursors, cursorErr := m.store.TopicCursors(ctx, indexerID)
	if cursorErr != nil {
		return outcome, cursorErr
	}
	forum := nextCatalogForum(forums, cursors)
	cursor := cursors[forum.ID]
	outcome.Forum = forum.ID
	outcome.Start = cursor.NextStart

	result, err := catalog.Catalog(ctx, engine.CatalogRequest{Forum: forum.ID, Start: cursor.NextStart})
	if err != nil {
		return outcome, fmt.Errorf("indexer %s: catalog: %w", indexer.Slug, err)
	}
	stats.Seen = len(result.Entries)
	topics := make([]store.Topic, 0, len(result.Entries))
	now := time.Now()
	for _, entry := range result.Entries {
		topics = append(topics, store.Topic{
			IndexerID:   indexerID,
			ForumID:     forum.ID,
			Title:       entry.Title,
			TitleKey:    textutil.NormalizeTitle(entry.Title),
			URL:         entry.URL,
			Category:    forum.Standard,
			FirstSeenAt: now,
			LastSeenAt:  now,
		})
	}
	added, err := m.store.UpsertTopics(ctx, indexerID, topics)
	if err != nil {
		return outcome, err
	}
	outcome.Topics = len(topics)
	outcome.Added = added
	pageSize := result.PageSize
	if pageSize <= 0 {
		pageSize = len(result.Entries)
	}
	next := cursor.NextStart + pageSize
	if !result.More || len(result.Entries) == 0 {
		// The listing is over: the next pass starts again from the top and
		// finds the threads that were already known.
		next = 0
		outcome.Finished = true
	}
	outcome.NextStart = next
	saveErr := m.store.SaveTopicCursor(ctx, store.TopicCursor{
		IndexerID: indexerID,
		ForumID:   forum.ID,
		NextStart: next,
		LastRunAt: now,
	})
	if saveErr != nil {
		return outcome, saveErr
	}
	m.store.InvalidateCounts()
	return outcome, nil
}

// nextCatalogForum picks the forum to read: the one that ran longest ago, so
// the walk spreads evenly over the forums of the definition. A forum that was
// never read is older than every forum that was.
func nextCatalogForum(forums []definition.CatalogForum, cursors map[string]store.TopicCursor) definition.CatalogForum {
	chosen := forums[0]
	for _, forum := range forums[1:] {
		if olderRun(cursors[forum.ID].LastRunAt, cursors[chosen.ID].LastRunAt) {
			chosen = forum
		}
	}
	return chosen
}

func olderRun(candidate, current time.Time) bool {
	if candidate.IsZero() {
		return !current.IsZero()
	}
	if current.IsZero() {
		return false
	}
	return candidate.Before(current)
}

// catalogItems answers a search from the local history. The entries have no
// magnet: the grab opens the topic, exactly as the same row would do from the
// site's own search. Only the definition that declares a catalog pays this
// lookup, and the limit keeps old threads from flooding fresh results.
func (m *Manager) catalogItems(ctx context.Context, indexer store.Indexer, query string, limit int) ([]engine.Item, error) {
	if strings.TrimSpace(query) == "" {
		return nil, nil
	}
	if m.catalogDefinition(ctx, indexer) == nil {
		return nil, nil
	}
	terms := strings.Fields(textutil.NormalizeTitle(query))
	if len(terms) == 0 {
		return nil, nil
	}
	topics, err := m.store.SearchTopics(ctx, indexer.ID, terms, limit)
	if err != nil {
		return nil, err
	}
	items := make([]engine.Item, 0, len(topics))
	for _, topic := range topics {
		categories := []int{}
		if topic.Category > 0 {
			categories = append(categories, topic.Category)
		}
		items = append(items, engine.Item{
			Protocol:   engine.Protocol(strings.ToLower(strings.TrimSpace(indexer.Protocol))),
			Title:      topic.Title,
			DetailsURL: topic.URL,
			Published:  topic.FirstSeenAt,
			Categories: categories,
		})
	}
	return items, nil
}

// mergeCatalogItems appends the local topics the site search did not return.
// A topic already in the site answer keeps the site row: it carries the
// magnets the site published.
func mergeCatalogItems(site, local []engine.Item, limit int) []engine.Item {
	if len(local) == 0 {
		return site
	}
	seen := make(map[string]struct{}, len(site)+len(local))
	for _, item := range site {
		if key := strings.TrimSpace(item.DetailsURL); key != "" {
			seen[key] = struct{}{}
			continue
		}
		seen[strings.ToLower(strings.TrimSpace(item.Title))] = struct{}{}
	}
	out := site
	for _, item := range local {
		if limit > 0 && len(out) >= limit {
			return out
		}
		key := strings.TrimSpace(item.DetailsURL)
		if key == "" {
			key = strings.ToLower(strings.TrimSpace(item.Title))
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, item)
	}
	return out
}
