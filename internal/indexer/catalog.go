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

// catalogForumFailuresBeforeSkip is how many failed pages a forum may have
// before the walk stops asking it.
const catalogForumFailuresBeforeSkip = 3

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
		// A page that fails (a restricted or removed forum, a block) must not
		// hold the walk hostage: the forum still counts as read, so the next
		// page goes to another one, and three failures in a row skip it.
		saveErr := m.store.SaveTopicCursor(ctx, store.TopicCursor{
			IndexerID: indexerID,
			ForumID:   forum.ID,
			NextStart: cursor.NextStart,
			Runs:      cursor.Runs + 1,
			Failures:  cursor.Failures + 1,
			LastRunAt: time.Now(),
		})
		if saveErr != nil {
			return outcome, saveErr
		}
		return outcome, fmt.Errorf("indexer %s: catalog: %w", indexer.Slug, err)
	}
	entries := result.Entries
	// The listing is sorted by last post, so a topic that receives a new
	// episode moves to the first page. That page is re-read at every pass, in
	// addition to the page the cursor is on: otherwise a thread updated today
	// would show its old title until the walk comes back to the top, which on
	// a big forum takes weeks.
	if catalogHasFrontPage(forum, cursor.NextStart) {
		front, frontErr := catalog.Catalog(ctx, engine.CatalogRequest{Forum: forum.ID, Start: 0})
		if frontErr != nil {
			m.logger.Debug("indexer: catalogue front page",
				"indexer", indexer.Slug, "forum", forum.ID, "error", frontErr)
		} else {
			entries = append(front.Entries, entries...)
		}
	}
	stats.Seen = len(entries)
	topics := make([]store.Topic, 0, len(entries))
	now := time.Now()
	for _, entry := range entries {
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
		Runs:      cursor.Runs + 1,
		Failures:  0,
		LastRunAt: now,
	})
	if saveErr != nil {
		return outcome, saveErr
	}
	m.store.InvalidateCounts()
	return outcome, nil
}

// nextCatalogForum picks the forum to read. The series and the movies carry a
// higher weight: a forum is chosen by (pages read + 1) / weight, so a forum of
// weight 5 gets five pages for every page of a forum of weight 1, and no forum
// is ever starved. Equal scores go to the forum that ran longest ago.
func nextCatalogForum(forums []definition.CatalogForum, cursors map[string]store.TopicCursor) definition.CatalogForum {
	// A forum that failed three times in a row is skipped, unless every forum
	// is skipped: then the walk starts again from the one that waited longest
	// (with the next attempt its failure counter is cleared by a success).
	eligible := make([]definition.CatalogForum, 0, len(forums))
	for _, forum := range forums {
		if cursors[forum.ID].Failures < catalogForumFailuresBeforeSkip {
			eligible = append(eligible, forum)
		}
	}
	if len(eligible) == 0 {
		eligible = forums
	}
	chosen := eligible[0]
	for _, forum := range eligible[1:] {
		left := catalogScore(cursors[forum.ID], forum.CatalogWeight())
		right := catalogScore(cursors[chosen.ID], chosen.CatalogWeight())
		switch {
		case left < right:
			chosen = forum
		case left == right && olderRun(cursors[forum.ID].LastRunAt, cursors[chosen.ID].LastRunAt):
			chosen = forum
		}
	}
	return chosen
}

// catalogHasFrontPage reports whether the walk of this forum can re-read its
// first page: a forum selected by "start" (not a fixed index topic) whose
// cursor already moved past the top.
func catalogHasFrontPage(forum definition.CatalogForum, nextStart int) bool {
	return forum.RefreshesFront() && strings.TrimSpace(forum.URL) == "" && nextStart > 0
}

func catalogScore(cursor store.TopicCursor, weight int) float64 {
	if weight <= 0 {
		weight = 1
	}
	return float64(cursor.Runs+1) / float64(weight)
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

// itemResolver is the connector method that opens the details of items whose
// definition resolves them at search time (DDUnlimited expands a topic into
// its files). A catalogue answer then looks like the site's own.
type itemResolver interface {
	ResolveItems(ctx context.Context, items []engine.Item) ([]engine.Item, error)
	ResolvesOnSearch() bool
}

// resolveCatalogItems lets a definition that resolves at search time turn the
// stored topics into the rows a client expects; the others keep the topic row
// and open it at grab time.
func resolveCatalogItems(ctx context.Context, connector engine.Connector, items []engine.Item) ([]engine.Item, error) {
	resolver, ok := connector.(itemResolver)
	if !ok || !resolver.ResolvesOnSearch() {
		return items, nil
	}
	return resolver.ResolveItems(ctx, items)
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
