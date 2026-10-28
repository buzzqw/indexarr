package engine

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// ErrSearchDeferred marks a search the connector did not send because the
// site's flood control (search.interval) had no free slot in time. The search
// is queued: a background worker sends it at the allowed pace and keeps the
// page in the search cache, so the next identical request reads it from there.
// It is not a failure of the site.
var ErrSearchDeferred = errors.New("search deferred by the flood interval")

// errSearchLater stops the pagination: a later page would need a slot of its
// own, and the first pages are worth more than waiting a whole interval.
var errSearchLater = errors.New("next search page needs another flood slot")

// searchQueueEntries bounds the deferred searches. A Gextto cycle asks about a
// hundred titles at once.
const searchQueueEntries = 256

// searchQueueAttempts is how many times a deferred search is sent before it
// is dropped (the site can still refuse it when the account searched from
// somewhere else).
const searchQueueAttempts = 2

// searchSlotMargin keeps a waiting search clear of the caller's deadline: the
// page still has to be read after the slot.
const searchSlotMargin = 15 * time.Second

type searchBuild func(ctx context.Context) (*http.Request, error)

type deferredSearch struct {
	key      string
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

// takeSearchSlot waits for the next free search slot and reserves it. The
// first page of a search waits up to two intervals, never past the caller's
// deadline; a later page takes only a slot that is free now. It returns
// ErrSearchDeferred or errSearchLater without waiting when the slot is too far.
func (c *DefinitionConnector) takeSearchSlot(ctx context.Context, firstPage bool) error {
	interval := c.searchInterval()
	if interval <= 0 {
		return nil
	}
	c.floodMu.Lock()
	now := c.now()
	next := c.searchNext
	if next.Before(now) {
		next = now
	}
	wait := next.Sub(now)
	if wait > 0 {
		if !firstPage {
			c.floodMu.Unlock()
			return errSearchLater
		}
		limit := 2 * interval
		if deadline, ok := ctx.Deadline(); ok {
			if left := deadline.Sub(now) - searchSlotMargin; left < limit {
				limit = left
			}
		}
		if wait > limit {
			c.floodMu.Unlock()
			return ErrSearchDeferred
		}
	}
	c.searchNext = next.Add(interval)
	c.floodMu.Unlock()
	if wait <= 0 {
		return nil
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// searchRefused pushes the next slot a full interval away from a refusal: the
// site's clock counts from its last accepted search, which can be one sent by
// the same account from a browser.
func (c *DefinitionConnector) searchRefused() {
	interval := c.searchInterval()
	if interval <= 0 {
		return
	}
	c.floodMu.Lock()
	if next := c.now().Add(interval); c.searchNext.Before(next) {
		c.searchNext = next
	}
	c.floodMu.Unlock()
}

// deferSearch queues a search page for the background worker. It reports
// false when the queue is full; a page already queued is not added twice.
func (c *DefinitionConnector) deferSearch(key string, build searchBuild) bool {
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
	c.searchQueue = append(c.searchQueue, deferredSearch{key: key, build: build})
	if !c.searchDraining {
		c.searchDraining = true
		go c.drainSearchQueue()
	}
	return true
}

// deferredSearches is the number of searches waiting for a slot.
func (c *DefinitionConnector) deferredSearches() int {
	c.floodMu.Lock()
	defer c.floodMu.Unlock()
	return len(c.searchQueue)
}

// drainSearchQueue sends the deferred searches one slot at a time and keeps
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
			c.floodMu.Lock()
			c.searchQueue = append(c.searchQueue, entry)
			c.floodMu.Unlock()
		}
	}
}

func (c *DefinitionConnector) sendDeferredSearch(ctx context.Context, entry deferredSearch) error {
	if err := c.waitSearchSlot(ctx); err != nil {
		return err
	}
	if err := c.ensureLogin(ctx); err != nil {
		return err
	}
	request, err := entry.build(ctx)
	if err != nil {
		return err
	}
	relogged := false
	response, err := c.fetchSearchPage(ctx, request, func() (*http.Request, error) { return entry.build(ctx) }, &relogged)
	if err != nil {
		return err
	}
	c.storeSearchPage(entry.key, response)
	return nil
}

// waitSearchSlot is takeSearchSlot without the deadline rule: the background
// worker always waits for its turn.
func (c *DefinitionConnector) waitSearchSlot(ctx context.Context) error {
	interval := c.searchInterval()
	c.floodMu.Lock()
	now := c.now()
	next := c.searchNext
	if next.Before(now) {
		next = now
	}
	c.searchNext = next.Add(interval)
	c.floodMu.Unlock()
	wait := next.Sub(now)
	if wait <= 0 {
		return nil
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func deferredSearchError(id string, interval time.Duration, queued bool) error {
	if !queued {
		return fmt.Errorf("definition %q: %w: the site allows one search every %s and the queue is full", id, ErrSearchDeferred, interval)
	}
	return fmt.Errorf("definition %q: %w: the site allows one search every %s; queued, the next identical request reads it from the cache", id, ErrSearchDeferred, interval)
}
