package indexer

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Sonarr asks for one episode several times, once per localized or alternate
// title. The requests arrive one after the other, so a full live window each
// costs 15-20 seconds for one episode. These constants give the burst a single
// budget: the first variant waits normally, the following ones share what is
// left. Every variant still runs its own indexer queries in the background, so
// the rows found late are stored.
const (
	// liveEpisodeBudget is the total wait allowed for the variants of one
	// episode, measured from the first request.
	liveEpisodeBudget = 9 * time.Second
	// liveEpisodeMinWait guarantees a minimal window to the last variants so
	// the fast indexers can still answer them.
	liveEpisodeMinWait = 600 * time.Millisecond
	// liveEpisodeWindow bounds how long a title burst is considered the same
	// episode. Sonarr sends the variants within a few seconds.
	liveEpisodeWindow = 90 * time.Second
)

// liveVariantApps lists the clients that send several title variants for the
// same episode. A user agent only ever makes the wait shorter, never longer.
var liveVariantApps = map[string]bool{
	"sonarr":   true,
	"radarr":   true,
	"lidarr":   true,
	"readarr":  true,
	"whisparr": true,
}

// episodeGroupKey identifies one episode search by the only fields the client
// sends for every variant: the mode, the season/episode pair and the category
// set. The query is deliberately excluded, because it is what changes between
// variants. Movies are not grouped: their title variants do not share a stable
// season key.
func episodeGroupKey(req LiveQuery) string {
	mode := strings.ToLower(strings.TrimSpace(req.Mode))
	if mode != "tvsearch" || req.Season <= 0 {
		return ""
	}
	categories := append([]int(nil), req.Categories...)
	sort.Ints(categories)
	return strings.Join([]string{
		mode,
		strings.ToLower(strings.TrimSpace(req.App)),
		fmt.Sprint(req.Season),
		fmt.Sprint(req.Episode),
		fmt.Sprint(req.Year),
		strings.ToLower(strings.TrimSpace(req.TVDbID)),
		fmt.Sprint(categories),
	}, "|")
}

// beginEpisodeVariant returns the shared budget for one variant. reused is
// false for the first request of an episode (which keeps the full window) and
// true for the following ones. remaining is the time left for the burst.
func (m *Manager) beginEpisodeVariant(req LiveQuery) (reused bool, remaining time.Duration) {
	key := episodeGroupKey(req)
	if key == "" || !liveVariantApps[strings.ToLower(strings.TrimSpace(req.App))] {
		return false, 0
	}
	now := time.Now()
	m.liveEpisodesMu.Lock()
	defer m.liveEpisodesMu.Unlock()
	for stored, started := range m.liveEpisodes {
		if now.Sub(started) > liveEpisodeWindow {
			delete(m.liveEpisodes, stored)
		}
	}
	started, ok := m.liveEpisodes[key]
	if !ok {
		m.liveEpisodes[key] = now
		return false, 0
	}
	remaining = liveEpisodeBudget - now.Sub(started)
	if remaining < liveEpisodeMinWait {
		remaining = liveEpisodeMinWait
	}
	return true, remaining
}

// withEpisodeBudget bounds the wait of a variant without cancelling the
// background work: the workers run on a context detached from ctx.
func withEpisodeBudget(ctx context.Context, remaining time.Duration) (context.Context, context.CancelFunc) {
	if remaining <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, remaining)
}
