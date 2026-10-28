package engine

import (
	"context"
	"errors"
	"fmt"
	"net/http"
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

// searchQueueEntries bounds the deferred searches. A Gextto cycle asks about a
// hundred titles at once.
const searchQueueEntries = 256

// searchQueueAttempts is how many times a deferred search is sent before it
// is dropped: the site can still refuse it when the account searched from
// somewhere else, or while a burst block is on. Measured on DDUnlimited on
// 2026-10-28: three queued searches at 65 s were accepted, then the next two
// were refused. The retries are deliberately few: each one costs a turn.
const searchQueueAttempts = 3

// searchRefusalBackoff is how long the worker waits after a refusal before
// spending another turn. The forum asks to wait "a few minutes" and a refusal
// means the flood interval alone was not enough; retrying at the normal pace
// would just hammer the block. Four intervals are about 4m20s on DDUnlimited.
const searchRefusalBackoff = 4

// searchTurnMargin keeps a waiting search clear of the caller's deadline: the
// page still has to be read after the turn.
const searchTurnMargin = 15 * time.Second

type searchBuild func(ctx context.Context) (*http.Request, error)

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

type deferredSearch struct {
	key      string
	query    string
	build    searchBuild
	attempts int
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
// false when the queue is full; a page already queued is not added twice.
func (c *DefinitionConnector) deferSearch(key, query string, build searchBuild) bool {
	if key == "" {
		return false
	}
	c.floodMu.Lock()
	defer c.floodMu.Unlock()
	for _, entry := range c.searchQueue {
		if entry.key == key {
			return true
		}
	}
	if len(c.searchQueue) >= searchQueueEntries {
		return false
	}
	c.searchQueue = append(c.searchQueue, deferredSearch{key: key, query: query, build: build})
	if !c.searchDraining {
		c.searchDraining = true
		go c.drainSearchQueue()
	}
	return true
}

// drainSearchQueue sends the deferred searches one turn at a time and keeps
// their pages in the search cache. It ends when the queue is empty.
func (c *DefinitionConnector) drainSearchQueue() {
	defer func() {
		// A panic must not leave the queue marked as draining forever.
		if recover() != nil {
			c.floodMu.Lock()
			c.searchDraining = false
			c.floodMu.Unlock()
		}
	}()
	for {
		c.floodMu.Lock()
		if len(c.searchQueue) == 0 {
			c.searchDraining = false
			c.floodMu.Unlock()
			return
		}
		entry := c.searchQueue[0]
		c.searchQueue = c.searchQueue[1:]
		c.floodMu.Unlock()

		if _, cached := c.cachedSearchPage(entry.key); cached {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*c.searchInterval()+2*time.Minute)
		err := c.sendDeferredSearch(ctx, entry)
		cancel()
		if errors.Is(err, errSearchRefused) && entry.attempts+1 < searchQueueAttempts {
			entry.attempts++
			// A refusal is the site asking for more than one interval: wait
			// before spending another turn. New entries wait with it, which is
			// correct while the block is on.
			c.waitRefusalBackoff()
			c.floodMu.Lock()
			c.searchQueue = append(c.searchQueue, entry)
			c.floodMu.Unlock()
		}
	}
}

// waitRefusalBackoff pauses the background queue after a refusal. The flood
// interval paces normal sends; a refusal needs a longer pause so the worker
// does not hammer the block with one request every interval.
func (c *DefinitionConnector) waitRefusalBackoff() {
	if interval := c.searchInterval(); interval > 0 {
		time.Sleep(time.Duration(searchRefusalBackoff) * interval)
	}
}

func (c *DefinitionConnector) sendDeferredSearch(ctx context.Context, entry deferredSearch) error {
	started := time.Now()
	attempt := entry.attempts + 1
	written, sent, err := c.sendDeferredSearchOnce(ctx, entry)
	if c.deferredSearch != nil {
		if sent.IsZero() {
			// No request left: the time belongs to the login or the lane.
			sent = started
		}
		c.deferredSearch(DeferredSearchEvent{
			Query:   entry.query,
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
	request, err := entry.build(ctx)
	if err != nil {
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
	relogged := false
	sent := time.Now()
	response, err := c.fetchSearchPage(ctx, request, func() (*http.Request, error) { return entry.build(ctx) }, &relogged)
	release()
	if err != nil {
		return 0, sent, err
	}
	c.storeSearchPage(entry.key, response)
	return int64(len(response.Body)), sent, nil
}

func deferredSearchError(id string, interval time.Duration, queued bool) error {
	if !queued {
		return fmt.Errorf("definition %q: %w: the site allows one search every %s and the queue is full", id, ErrSearchDeferred, interval)
	}
	return fmt.Errorf("definition %q: %w: the site allows one search every %s; queued, the next identical request reads it from the cache", id, ErrSearchDeferred, interval)
}
