package engine

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"time"
)

// ErrSearchDeferred marks a search the connector did not send because the
// site's flood control (search.interval) had no free turn in time. The search
// is queued: a background worker sends it at the allowed pace and keeps the
// page in the search cache, so the next identical request reads it from there.
// It is not a failure of the site.
var ErrSearchDeferred = errors.New("search deferred by the flood interval")

// errSearchLater stops the pagination: a later page would need a turn of its
// own, and the first pages are worth more than waiting a whole interval.
var errSearchLater = errors.New("next search page needs another flood turn")

// errSearchCached says a queued page was filled by another request while the
// worker waited for its flood turn. It is not sent again and writes no history
// row of its own: the request that filled the cache already has one.
var errSearchCached = errors.New("search page already cached")

// searchQueueEntries bounds the deferred searches. A Gextto cycle asks about a
// hundred titles at once.
const searchQueueEntries = 256

// searchQueueAttempts is how many times a deferred search is sent before it
// is dropped: the site can still refuse it when the account searched from
// somewhere else, or while a burst block is on. Measured on DDUnlimited on
// 2026-10-28: three queued searches at 65 s were accepted, then the next two
// were refused. The retries are deliberately few: each one costs a turn.
const searchQueueAttempts = 3

// searchRefusalBackoff is how long, in intervals, no search is sent after a
// refusal. The forum asks to wait "a few minutes" and a refusal means the
// flood interval alone was not enough; retrying at the normal pace would just
// hammer the block. Four intervals are about 4m20s on DDUnlimited. Every
// further refusal in a row doubles the pause, up to searchRefusalBackoffMax:
// on 2026-10-29 the forum refused every search from 02:06 to 03:29, one every
// 5.5 minutes, so a fixed pause only kept knocking on the block.
const (
	searchRefusalBackoff    = 4
	searchRefusalBackoffMax = 32
)

// searchTurnMargin keeps a waiting search clear of the caller's deadline: the
// page still has to be read after the turn.
const searchTurnMargin = 15 * time.Second

// DeferredSearchEvent is one search page the background worker really sent to
// the site for a deferred search, or tried to send. Err is nil when the page
// was read and stored in the search cache. Attempt starts at 1.
type DeferredSearchEvent struct {
	Query   string
	Attempt int
	Started time.Time
	Elapsed time.Duration
	Bytes   int64
	Err     error
}

// DeferredSearchObserver receives the outcome of a background search send. It
// is called from the worker goroutine, never from the caller that queued the
// search.
type DeferredSearchObserver func(DeferredSearchEvent)

// PendingSearch is a deferred search page in a form that survives a restart:
// the request and the page counters, not the URL, which can carry a session
// token. The connector rebuilds the request when the page's turn comes.
type PendingSearch struct {
	Request  SearchRequest `json:"request"`
	Page     int           `json:"page"`
	Offset   int           `json:"offset"`
	Param    int           `json:"param"`
	Attempts int           `json:"attempts,omitempty"`
	QueuedAt time.Time     `json:"queued_at"`
}

// samePage reports whether two pending searches ask for the same page.
func (p PendingSearch) samePage(other PendingSearch) bool {
	return p.Page == other.Page && p.Offset == other.Offset && p.Param == other.Param &&
		reflect.DeepEqual(p.Request, other.Request)
}

// deferredSearch is one queued page. key is the search cache key; a page
// restored from a previous process has none until its request is built.
type deferredSearch struct {
	key     string
	pending PendingSearch
}

// searchInterval is the minimum time between two search requests sent to the
// site, zero when the definition has no flood interval.
func (c *DefinitionConnector) searchInterval() time.Duration {
	if c.def.Search.Interval <= 0 {
		return 0
	}
	return c.def.Search.Interval.Duration()
}

// searchLane returns the one-slot lane every search request to the site goes
// through when the definition has a flood interval.
func (c *DefinitionConnector) searchLane() chan struct{} {
	c.floodMu.Lock()
	defer c.floodMu.Unlock()
	if c.floodLane == nil {
		c.floodLane = make(chan struct{}, 1)
	}
	return c.floodLane
}

// takeSearchTurn waits for the lane and for the interval after the previous
// search answered, then holds the lane: the caller sends the request and calls
// the returned release. The site counts from the search it processed, so the
// interval runs from the end of the previous request, retries after a new
// login included.
//
// The first page of a search waits up to two intervals, never past the
// caller's deadline minus searchTurnMargin; a later page takes only a turn
// that is free now. Otherwise nothing is sent: ErrSearchDeferred or
// errSearchLater.
func (c *DefinitionConnector) takeSearchTurn(ctx context.Context, firstPage bool) (func(), error) {
	interval := c.searchInterval()
	if interval <= 0 {
		return func() {}, nil
	}
	if c.searchBlockedFor() > 0 {
		// The site refused the last search: a live request would wait for a
		// turn only to be refused again. It goes to the queue at once.
		if !firstPage {
			return nil, errSearchLater
		}
		return nil, ErrSearchDeferred
	}
	limit := time.Duration(0)
	if firstPage {
		limit = 2 * interval
		if deadline, ok := ctx.Deadline(); ok {
			if left := time.Until(deadline) - searchTurnMargin; left < limit {
				limit = left
			}
		}
	}
	c.floodMu.Lock()
	// Every search already waiting goes first: each takes a whole interval.
	// Before the first search searchLast is zero and the time left would be
	// hugely negative, letting every caller in: it never goes below zero.
	estimate := max(time.Until(c.searchLast.Add(interval)), 0) + time.Duration(c.floodWaiting)*interval
	if c.floodBusy {
		estimate += interval
	}
	if estimate > limit {
		c.floodMu.Unlock()
		if !firstPage {
			return nil, errSearchLater
		}
		return nil, ErrSearchDeferred
	}
	c.floodWaiting++
	c.floodMu.Unlock()
	return c.enterSearchLane(ctx, interval)
}

// enterSearchLane acquires the lane, waits out the interval and returns the
// release. The caller has already been counted in floodWaiting.
func (c *DefinitionConnector) enterSearchLane(ctx context.Context, interval time.Duration) (func(), error) {
	lane := c.searchLane()
	done := func() {
		c.floodMu.Lock()
		c.floodWaiting--
		c.floodMu.Unlock()
	}
	select {
	case lane <- struct{}{}:
	case <-ctx.Done():
		done()
		return nil, ctx.Err()
	}
	c.floodMu.Lock()
	c.floodWaiting--
	c.floodBusy = true
	wait := time.Until(c.searchLast.Add(interval))
	c.floodMu.Unlock()
	release := func() {
		c.floodMu.Lock()
		c.searchLast = time.Now()
		c.floodBusy = false
		c.floodMu.Unlock()
		<-lane
	}
	if wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			// Nothing was sent: the previous answer stays the reference.
			c.floodMu.Lock()
			c.floodBusy = false
			c.floodMu.Unlock()
			<-lane
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	return release, nil
}

// deferSearch queues a search page for the background worker. It reports
// false when the queue is full or the connector has been closed; a page
// already queued is not added twice.
func (c *DefinitionConnector) deferSearch(key string, pending PendingSearch) bool {
	if key == "" {
		return false
	}
	if pending.QueuedAt.IsZero() {
		pending.QueuedAt = c.now()
	}
	c.floodMu.Lock()
	if c.queueCtx == nil || c.queueCtx.Err() != nil {
		c.floodMu.Unlock()
		return false
	}
	if c.pageQueuedLocked(key, pending) {
		c.floodMu.Unlock()
		return true
	}
	if len(c.searchQueue) >= searchQueueEntries {
		c.floodMu.Unlock()
		return false
	}
	c.searchQueue = append(c.searchQueue, deferredSearch{key: key, pending: pending})
	c.startDrainLocked()
	c.floodMu.Unlock()
	c.savePendingSearches()
	return true
}

// pageQueuedLocked reports whether the page is already queued or being sent.
// The caller holds floodMu.
func (c *DefinitionConnector) pageQueuedLocked(key string, pending PendingSearch) bool {
	same := func(entry deferredSearch) bool {
		return (key != "" && entry.key == key) || entry.pending.samePage(pending)
	}
	if c.searchSending != nil && same(*c.searchSending) {
		return true
	}
	for _, entry := range c.searchQueue {
		if same(entry) {
			return true
		}
	}
	return false
}

// startDrainLocked starts the background worker if it is not running. The
// caller holds floodMu.
func (c *DefinitionConnector) startDrainLocked() {
	if !c.searchDraining && len(c.searchQueue) > 0 {
		c.searchDraining = true
		go c.drainSearchQueue(c.queueCtx)
	}
}

// ResumePendingSearches queues again the pages a previous process (or the
// connector this one replaced) saved, and starts the background worker. A
// page older than the definition's search.cache is dropped: its answer would
// already have expired from the cache it was meant to fill. The manager calls
// it once, on the connector it keeps.
func (c *DefinitionConnector) ResumePendingSearches(saved []PendingSearch) {
	if len(saved) == 0 {
		return
	}
	maxAge := c.def.Search.Cache.Duration()
	now := c.now()
	kept := 0
	c.floodMu.Lock()
	for _, pending := range saved {
		if c.searchInterval() <= 0 || maxAge <= 0 || pending.QueuedAt.IsZero() || now.Sub(pending.QueuedAt) > maxAge {
			continue
		}
		if len(c.searchQueue) >= searchQueueEntries || c.pageQueuedLocked("", pending) {
			continue
		}
		c.searchQueue = append(c.searchQueue, deferredSearch{pending: pending})
		kept++
	}
	c.startDrainLocked()
	c.floodMu.Unlock()
	if kept != len(saved) {
		c.savePendingSearches()
	}
}

// savePendingSearches hands the whole queue, the page being sent included,
// to the saver. A closed connector saves nothing: Close runs at shutdown too,
// and the saved queue is what the next process resumes.
func (c *DefinitionConnector) savePendingSearches() {
	if c.savePending == nil {
		return
	}
	c.saveMu.Lock()
	defer c.saveMu.Unlock()
	c.floodMu.Lock()
	if c.queueCtx == nil || c.queueCtx.Err() != nil {
		c.floodMu.Unlock()
		return
	}
	snapshot := make([]PendingSearch, 0, len(c.searchQueue)+1)
	if c.searchSending != nil {
		snapshot = append(snapshot, c.searchSending.pending)
	}
	for _, entry := range c.searchQueue {
		snapshot = append(snapshot, entry.pending)
	}
	c.floodMu.Unlock()
	c.savePending(snapshot)
}

// drainSearchQueue sends the deferred searches one turn at a time and keeps
// their pages in the search cache. It ends when the queue is empty or the
// connector is closed (indexer deleted, disabled or definition changed).
func (c *DefinitionConnector) drainSearchQueue(ctx context.Context) {
	defer func() {
		// A panic must not leave the queue marked as draining forever.
		_ = recover()
		c.floodMu.Lock()
		c.searchDraining = false
		c.floodMu.Unlock()
	}()
	for {
		if ctx.Err() != nil {
			return
		}
		c.floodMu.Lock()
		if len(c.searchQueue) == 0 {
			c.floodMu.Unlock()
			return
		}
		entry := c.searchQueue[0]
		c.searchQueue = c.searchQueue[1:]
		c.searchSending = &entry
		c.floodMu.Unlock()

		if _, cached := c.cachedSearchPage(entry.key); cached {
			c.finishSending(nil)
			continue
		}
		if c.deferredSearchAllowed != nil {
			if err := c.deferredSearchAllowed(ctx); err != nil {
				if ctx.Err() != nil {
					return
				}
				// The indexer's daily query budget is gone (or the indexer was
				// deleted): drop the queued pages. The request that queued them
				// already has its canceled history row.
				c.floodMu.Lock()
				c.searchQueue = nil
				c.floodMu.Unlock()
				c.finishSending(nil)
				return
			}
		}
		if !c.waitRefusalBackoff(ctx) {
			return
		}
		sendCtx, cancel := context.WithTimeout(ctx, 3*c.searchInterval()+2*time.Minute)
		err := c.sendDeferredSearch(sendCtx, entry)
		cancel()
		if ctx.Err() != nil {
			// Closed: the page stays in the saved queue for the next process.
			return
		}
		if errors.Is(err, errSearchRefused) && entry.pending.Attempts+1 < searchQueueAttempts {
			entry.pending.Attempts++
			// A refusal is the site asking for more than one interval: the
			// next send waits for the pause (see noteSearchRefused). New
			// entries wait with it, which is correct while the block is on.
			c.finishSending(&entry)
			continue
		}
		c.finishSending(nil)
	}
}

// finishSending ends the send of the current page, queues retry at the end
// when it is not nil, and saves the queue.
func (c *DefinitionConnector) finishSending(retry *deferredSearch) {
	c.floodMu.Lock()
	c.searchSending = nil
	if retry != nil {
		c.searchQueue = append(c.searchQueue, *retry)
	}
	c.floodMu.Unlock()
	c.savePendingSearches()
}

// waitRefusalBackoff pauses the background queue while the site's block is
// on. The flood interval paces normal sends; a refusal needs a longer pause so
// the worker does not hammer the block with one request every interval. It
// reports false when the connector is closed while waiting.
func (c *DefinitionConnector) waitRefusalBackoff(ctx context.Context) bool {
	for {
		wait := c.searchBlockedFor()
		if wait <= 0 {
			return true
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return false
		case <-timer.C:
		}
	}
}

// noteSearchRefused starts or lengthens the pause after the site refused a
// search: four intervals, doubled for every refusal in a row, at most
// searchRefusalBackoffMax intervals.
func (c *DefinitionConnector) noteSearchRefused() {
	interval := c.searchInterval()
	if interval <= 0 {
		return
	}
	c.floodMu.Lock()
	defer c.floodMu.Unlock()
	c.searchRefusals++
	steps := searchRefusalBackoff
	for i := 1; i < c.searchRefusals && steps < searchRefusalBackoffMax; i++ {
		steps *= 2
	}
	steps = min(steps, searchRefusalBackoffMax)
	c.searchBlockedUntil = c.now().Add(time.Duration(steps) * interval)
}

// noteSearchAccepted ends the pause: the site answered a search.
func (c *DefinitionConnector) noteSearchAccepted() {
	c.floodMu.Lock()
	defer c.floodMu.Unlock()
	c.searchRefusals = 0
	c.searchBlockedUntil = time.Time{}
}

// searchBlockedFor is how long the pause after a refusal still lasts.
func (c *DefinitionConnector) searchBlockedFor() time.Duration {
	c.floodMu.Lock()
	defer c.floodMu.Unlock()
	if c.searchBlockedUntil.IsZero() {
		return 0
	}
	return max(c.searchBlockedUntil.Sub(c.now()), 0)
}

// Close stops the background queue: queued pages are dropped and an in-flight
// send is canceled. The Manager calls it when it drops a connector, so a
// disabled or deleted indexer cannot keep spending flood turns for hours. The
// connector is not usable for new deferred searches after Close.
func (c *DefinitionConnector) Close() {
	if c == nil {
		return
	}
	if c.queueCancel != nil {
		c.queueCancel()
	}
	c.floodMu.Lock()
	c.searchQueue = nil
	c.searchSending = nil
	c.floodMu.Unlock()
}

func (c *DefinitionConnector) sendDeferredSearch(ctx context.Context, entry deferredSearch) error {
	started := time.Now()
	attempt := entry.pending.Attempts + 1
	written, sent, err := c.sendDeferredSearchOnce(ctx, entry)
	if errors.Is(err, errSearchCached) {
		// Another request filled the cache while this one waited for its turn:
		// no request left and nothing to report.
		return nil
	}
	if c.deferredSearch != nil {
		if sent.IsZero() {
			// No request left: the time belongs to the login or the lane.
			sent = started
		}
		c.deferredSearch(DeferredSearchEvent{
			Query:   entry.pending.Request.Query,
			Attempt: attempt,
			Started: sent,
			Elapsed: time.Since(sent),
			Bytes:   written,
			Err:     err,
		})
	}
	return err
}

// sendDeferredSearchOnce sends one queued page and stores it in the search
// cache. It returns how many bytes the page carried, when the request left
// (the login and the queue turn are not request latency), and the error. The
// send time lets the history row show the real pace the site saw.
func (c *DefinitionConnector) sendDeferredSearchOnce(ctx context.Context, entry deferredSearch) (int64, time.Time, error) {
	if err := c.ensureLogin(ctx); err != nil {
		return 0, time.Time{}, err
	}
	// The worker always waits for its turn, however long the lane is.
	c.floodMu.Lock()
	c.floodWaiting++
	c.floodMu.Unlock()
	release, err := c.enterSearchLane(ctx, c.searchInterval())
	if err != nil {
		return 0, time.Time{}, err
	}
	// The request is built after the turn: a token read before a long wait
	// could be stale by the time the request leaves.
	build := func() (*http.Request, error) {
		p := entry.pending
		return c.buildSearchRequest(ctx, p.Request, p.Page, p.Offset, p.Param)
	}
	request, err := build()
	if err != nil {
		release()
		return 0, time.Time{}, err
	}
	key := entry.key
	if key == "" {
		// A page restored from a previous process: its key is the URL.
		key = c.searchCacheKey(request)
	}
	// A concurrent foreground search may have filled this page while the
	// worker waited: sending it again would spend a turn for nothing.
	if _, cached := c.cachedSearchPage(key); cached {
		release()
		return 0, time.Time{}, errSearchCached
	}
	relogged := false
	sent := time.Now()
	response, err := c.fetchSearchPage(ctx, request, build, &relogged)
	release()
	if err != nil {
		return 0, sent, err
	}
	c.storeSearchPage(key, response)
	return int64(len(response.Body)), sent, nil
}

func deferredSearchError(id string, interval time.Duration, queued bool) error {
	if !queued {
		return fmt.Errorf("definition %q: %w: the site allows one search every %s and the queue is full", id, ErrSearchDeferred, interval)
	}
	return fmt.Errorf("definition %q: %w: the site allows one search every %s; queued, the next identical request reads it from the cache", id, ErrSearchDeferred, interval)
}

// blockedSearchError is deferredSearchError while the pause after a refusal
// is on: the message says why nothing was sent and when the next turn is.
func blockedSearchError(id string, wait time.Duration, queued bool) error {
	wait = wait.Round(time.Second)
	if !queued {
		return fmt.Errorf("definition %q: %w: the site refused the last search, next turn in %s, and the queue is full", id, ErrSearchDeferred, wait)
	}
	return fmt.Errorf("definition %q: %w: the site refused the last search, next turn in %s; queued, the next identical request reads it from the cache", id, ErrSearchDeferred, wait)
}
