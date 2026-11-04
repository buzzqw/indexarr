package indexer

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/buzzqw/indexarr/internal/definition"
	"github.com/buzzqw/indexarr/internal/engine"
	"github.com/buzzqw/indexarr/internal/store"
)

// fakeCatalogConnector answers the catalogue requests of the tests and lets
// the site search fail, so the local history is the only answer.
type fakeCatalogConnector struct {
	pages  map[string]engine.CatalogResult
	forums []definition.CatalogForum
	search error
	delay  time.Duration
	active atomic.Int32
	max    atomic.Int32
}

func (f *fakeCatalogConnector) peak() int32 { return f.max.Load() }

func (f *fakeCatalogConnector) Capabilities() engine.Capabilities {
	return engine.Capabilities{
		Protocol: engine.ProtocolTorrent,
		Modes:    []engine.Mode{engine.ModeSearch},
		Params:   map[engine.Mode][]string{engine.ModeSearch: {"q"}},
	}
}

func (f *fakeCatalogConnector) Search(context.Context, engine.SearchRequest) ([]engine.Item, error) {
	if f.search != nil {
		return nil, f.search
	}
	return nil, nil
}

func (f *fakeCatalogConnector) Latest(context.Context, engine.LatestRequest) ([]engine.Item, error) {
	return nil, nil
}

func (f *fakeCatalogConnector) Download(context.Context, engine.Item) (engine.Payload, error) {
	return engine.Payload{}, errors.New("not used")
}

func (f *fakeCatalogConnector) Test(context.Context) (engine.TestResult, error) {
	return engine.TestResult{}, nil
}

func (f *fakeCatalogConnector) Catalog(_ context.Context, req engine.CatalogRequest) (engine.CatalogResult, error) {
	current := f.active.Add(1)
	for {
		seen := f.max.Load()
		if current <= seen || f.max.CompareAndSwap(seen, current) {
			break
		}
	}
	defer f.active.Add(-1)
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	key := req.Forum + ":" + strconv.Itoa(req.Start)
	if page, ok := f.pages[key]; ok {
		return page, nil
	}
	return engine.CatalogResult{}, nil
}

func (f *fakeCatalogConnector) CatalogForums() []definition.CatalogForum { return f.forums }

const catalogTestDefinition = `schema: 1
meta: { id: forum, name: Forum, type: private, protocol: torrent, urls: ["https://forum.example/"] }
capabilities: { search: [q] }
search:
  request: { get: /search.php }
  response:
    type: html
    rows: { selector: "li" }
    fields:
      title: { selector: "a" }
    required: [title]
catalog:
  forums:
    - { id: "52", standard: 5040 }
    - { id: "26", standard: 2040 }
  request: { get: /viewforum.php }
  forum_param: f
  start_param: start
  page_size: 2
  response:
    type: html
    rows: { selector: "li" }
    fields:
      title: { selector: "a" }
      details: { selector: "a", attr: href, type: url }
    required: [title, details]
`

func newCatalogTestManager(t *testing.T, connector *fakeCatalogConnector) (*Manager, *store.Store, int64) {
	t.Helper()
	st := newTestStore(t)
	seedDefinition(t, st, "forum", "native", catalogTestDefinition)
	def, err := st.GetDefinition(context.Background(), "forum")
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	id, err := st.CreateIndexer(context.Background(), store.Indexer{
		DefinitionID: def.ID,
		Protocol:     "torrent",
		Slug:         "forum",
		Name:         "Forum",
		Enabled:      true,
		ConfigJSON:   `{"url":"https://forum.example/"}`,
	})
	if err != nil {
		t.Fatalf("CreateIndexer: %v", err)
	}
	row, err := st.GetIndexer(context.Background(), id)
	if err != nil {
		t.Fatalf("GetIndexer: %v", err)
	}
	manager := &Manager{
		store: st,
		cache: map[int64]cachedConnector{
			id: {key: connectorKey(def, row), connector: connector},
		},
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	return manager, st, id
}

// The walk reads the forum that ran longest ago, stores the page and moves the
// cursor forward; a short page sends the cursor back to the top so the next
// pass starts again.
func TestRunCatalogPageWalksEveryForum(t *testing.T) {
	ctx := context.Background()
	connector := &fakeCatalogConnector{
		forums: []definition.CatalogForum{{ID: "52", Standard: 5040}, {ID: "26", Standard: 2040}},
		pages: map[string]engine.CatalogResult{
			"52:0": {Entries: []engine.CatalogEntry{
				{Title: "Serie A", URL: "https://forum.example/viewtopic.php?t=1", Forum: "52", Standard: 5040},
				{Title: "Serie B", URL: "https://forum.example/viewtopic.php?t=2", Forum: "52", Standard: 5040},
			}, More: true},
			"52:2": {Entries: []engine.CatalogEntry{
				{Title: "Serie C", URL: "https://forum.example/viewtopic.php?t=3", Forum: "52", Standard: 5040},
			}, More: false},
			"26:0": {Entries: []engine.CatalogEntry{
				{Title: "Film", URL: "https://forum.example/viewtopic.php?t=4", Forum: "26", Standard: 2040},
			}, More: false},
		},
	}
	manager, st, indexerID := newCatalogTestManager(t, connector)

	first, err := manager.RunCatalogPage(ctx, indexerID)
	if err != nil {
		t.Fatalf("RunCatalogPage: %v", err)
	}
	if first.Forum != "52" || first.Start != 0 || first.Topics != 2 || first.Added != 2 || first.NextStart != 2 || first.Finished {
		t.Fatalf("first page = %+v", first)
	}
	if count, _ := st.TopicCount(ctx, indexerID); count != 2 {
		t.Fatalf("topics = %d, want 2", count)
	}

	// The other forum was never read, so it is the next one.
	second, err := manager.RunCatalogPage(ctx, indexerID)
	if err != nil {
		t.Fatalf("RunCatalogPage: %v", err)
	}
	if second.Forum != "26" || second.Start != 0 || !second.Finished || second.NextStart != 0 {
		t.Fatalf("second page = %+v", second)
	}

	// Back to the first forum, continuing from the saved cursor.
	third, err := manager.RunCatalogPage(ctx, indexerID)
	if err != nil {
		t.Fatalf("RunCatalogPage: %v", err)
	}
	if third.Forum != "52" || third.Start != 2 || !third.Finished || third.NextStart != 0 {
		t.Fatalf("third page = %+v", third)
	}
	if count, _ := st.TopicCount(ctx, indexerID); count != 4 {
		t.Fatalf("topics = %d, want 4 (2 + 1 + 1)", count)
	}
}

// A search that the site refuses is answered from the history: the threads are
// found locally and the page is not left empty.
func TestSearchFallsBackToTheTopicHistory(t *testing.T) {
	ctx := context.Background()
	connector := &fakeCatalogConnector{
		forums: []definition.CatalogForum{{ID: "52", Standard: 5040}},
		pages: map[string]engine.CatalogResult{
			"52:0": {Entries: []engine.CatalogEntry{
				{Title: "Dark Matter S01 1080p", URL: "https://forum.example/viewtopic.php?t=1", Forum: "52", Standard: 5040},
				{Title: "Dark Matter S02 1080p", URL: "https://forum.example/viewtopic.php?t=2", Forum: "52", Standard: 5040},
			}},
		},
		search: errors.New("search refused by the site"),
	}
	manager, _, indexerID := newCatalogTestManager(t, connector)
	if _, err := manager.RunCatalogPage(ctx, indexerID); err != nil {
		t.Fatalf("RunCatalogPage: %v", err)
	}

	items, err := manager.Search(ctx, indexerID, engine.SearchRequest{Mode: engine.ModeSearch, Query: "Dark Matter"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("items = %d, want the 2 stored topics: %+v", len(items), items)
	}
	for _, item := range items {
		if item.DetailsURL == "" {
			t.Errorf("item %q has no topic address", item.Title)
		}
		if len(item.Categories) != 1 || item.Categories[0] != 5040 {
			t.Errorf("item %q categories = %v, want 5040", item.Title, item.Categories)
		}
	}

	// The live search still answers with the site's row when there is one:
	// the local topic is only added when the site did not return it.
	connector.search = nil
	items, err = manager.Search(ctx, indexerID, engine.SearchRequest{Mode: engine.ModeSearch, Query: "Dark Matter"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("items = %d, want the 2 stored topics", len(items))
	}
}

func TestOlderRunOrdering(t *testing.T) {
	now := time.Now()
	if olderRun(time.Time{}, time.Time{}) {
		t.Error("two never-read forums keep their order")
	}
	if !olderRun(time.Time{}, now) {
		t.Error("a never-read forum is older than a read one")
	}
	if olderRun(now, time.Time{}) {
		t.Error("a read forum is not older than a never-read one")
	}
	if !olderRun(now.Add(-time.Hour), now) {
		t.Error("the earlier run goes first")
	}
}

// A local topic never removes a row the site returned: the merge appends what
// is missing, so a live search can only gain history, never lose a fresh row.
func TestMergeCatalogItemsKeepsTheSiteRows(t *testing.T) {
	site := []engine.Item{
		{Title: "Dark Matter S01", DetailsURL: "https://forum.example/viewtopic.php?t=1", Magnet: "magnet:?xt=urn:btih:aa"},
		{Title: "Dark Matter S02", DetailsURL: "https://forum.example/viewtopic.php?t=9"},
	}
	local := []engine.Item{
		{Title: "Dark Matter S01 (thread)", DetailsURL: "https://forum.example/viewtopic.php?t=1"},
		{Title: "Dark Matter S00", DetailsURL: "https://forum.example/viewtopic.php?t=3"},
	}
	merged := mergeCatalogItems(site, local, 10)
	if len(merged) != 3 {
		t.Fatalf("merged = %d rows, want the 2 site rows plus 1 new topic", len(merged))
	}
	for i, want := range site {
		if merged[i].DetailsURL != want.DetailsURL {
			t.Errorf("row %d = %q, want the site row %q", i, merged[i].DetailsURL, want.DetailsURL)
		}
	}
	if merged[2].DetailsURL != "https://forum.example/viewtopic.php?t=3" {
		t.Errorf("the appended row = %q", merged[2].DetailsURL)
	}

	// The cap counts the site rows too: the history cannot push them out.
	capped := mergeCatalogItems(site, local, 2)
	if len(capped) != 2 || capped[1].DetailsURL != site[1].DetailsURL {
		t.Fatalf("capped = %+v, want the site rows only", capped)
	}
}

// The page holds a few rows more than the topics (announcements and stickies
// are skipped), so the cursor must advance by the site's page size and not by
// the number of stored entries: otherwise every page would overlap the next.
func TestRunCatalogPageAdvancesByTheSitePageSize(t *testing.T) {
	ctx := context.Background()
	var start int
	connector := &fakeCatalogConnector{
		forums: []definition.CatalogForum{{ID: "52", Standard: 5040}},
		pages: map[string]engine.CatalogResult{
			"52:0": {PageSize: 25, More: true, Entries: []engine.CatalogEntry{
				{Title: "Serie A", URL: "https://forum.example/viewtopic.php?t=1", Forum: "52", Standard: 5040},
				{Title: "Serie B", URL: "https://forum.example/viewtopic.php?t=2", Forum: "52", Standard: 5040},
			}},
			"52:25": {PageSize: 25, More: false, Entries: []engine.CatalogEntry{
				{Title: "Serie C", URL: "https://forum.example/viewtopic.php?t=3", Forum: "52", Standard: 5040},
			}},
		},
	}
	manager, _, indexerID := newCatalogTestManager(t, connector)
	first, err := manager.RunCatalogPage(ctx, indexerID)
	if err != nil {
		t.Fatalf("RunCatalogPage: %v", err)
	}
	if first.NextStart != 25 || first.Finished {
		t.Fatalf("first page = %+v, want the next start at 25", first)
	}
	second, err := manager.RunCatalogPage(ctx, indexerID)
	if err != nil {
		t.Fatalf("RunCatalogPage: %v", err)
	}
	start = second.Start
	if start != 25 || !second.Finished || second.NextStart != 0 {
		t.Fatalf("second page = %+v, want start 25 and a finished forum", second)
	}
}

// A manual page read and the scheduled pass must not log in at the same time:
// the forum invalidates the first form token and both fail.
func TestRunCatalogPageIsSerializedPerIndexer(t *testing.T) {
	ctx := context.Background()
	connector := &fakeCatalogConnector{
		forums: []definition.CatalogForum{{ID: "52", Standard: 5040}},
		pages: map[string]engine.CatalogResult{
			"52:0": {PageSize: 25, More: true, Entries: []engine.CatalogEntry{
				{Title: "Serie A", URL: "https://forum.example/viewtopic.php?t=1", Forum: "52", Standard: 5040},
			}},
			"52:25": {PageSize: 25, More: true, Entries: []engine.CatalogEntry{
				{Title: "Serie B", URL: "https://forum.example/viewtopic.php?t=2", Forum: "52", Standard: 5040},
			}},
		},
	}
	connector.delay = 40 * time.Millisecond
	manager, _, indexerID := newCatalogTestManager(t, connector)

	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := manager.RunCatalogPage(ctx, indexerID); err != nil {
				t.Errorf("RunCatalogPage: %v", err)
			}
		}()
	}
	wg.Wait()
	if got := connector.peak(); got != 1 {
		t.Fatalf("concurrent catalogue requests = %d, want 1 (serialized per indexer)", got)
	}
}

// The series and the movies get five pages for every page of the rest of the
// forum, and the rest is never starved: over a full cycle the ratio is 5:1.
func TestCatalogWeightFavoursSeriesAndMovies(t *testing.T) {
	forums := []definition.CatalogForum{
		{ID: "51", Standard: 5040, Weight: 5},
		{ID: "39", Standard: 7020},
	}
	cursors := map[string]store.TopicCursor{}
	picks := map[string]int{}
	for i := 0; i < 60; i++ {
		forum := nextCatalogForum(forums, cursors)
		picks[forum.ID]++
		cursor := cursors[forum.ID]
		cursor.Runs++
		cursor.LastRunAt = time.Unix(int64(1_700_000_000+i*60), 0)
		cursors[forum.ID] = cursor
	}
	if picks["51"] != 50 || picks["39"] != 10 {
		t.Fatalf("picks = %v, want 50 pages of the series and 10 of the rest", picks)
	}
}
