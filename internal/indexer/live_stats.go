package indexer

import (
	"context"
	"sort"
	"time"

	"github.com/buzzqw/indexarr/internal/store"
)

// The early-finish rule exists because one or two sites answer after several
// seconds while every other site has already replied. Waiting for them costs
// the whole configured window on every title variant Sonarr sends for the same
// episode. These constants describe when a site is considered "usually only a
// timeout": it must answer late (p90) and it must rarely bring a result.
const (
	// liveStatWindow is how many recent searches of one indexer are kept.
	liveStatWindow = 24
	// liveStatMinSamples is the evidence needed before a site can be
	// classified. Until then the fan-out waits normally.
	liveStatMinSamples = 4
	// liveSlowP90 is the p90 duration above which a site is late.
	liveSlowP90 = 2500 * time.Millisecond
	// liveSlackSuccessPct is the percentage of searches that returned at
	// least one row below which a late site is considered unproductive.
	liveSlackSuccessPct = 15
	// liveEarlyGrace is the extra window granted after every productive site
	// answered, so a late site that was only slightly slow still lands in the
	// response. It is not the full wait: the point is to skip the long tail.
	liveEarlyGrace = 1500 * time.Millisecond
)

// liveStatSeries is a small ring of the newest outcomes of one indexer. The
// ring keeps memory bounded and follows changes in the site's behaviour.
type liveStatSeries struct {
	durations [liveStatWindow]int64 // milliseconds
	results   [liveStatWindow]int64
	n         int
	next      int
}

func (s *liveStatSeries) add(duration time.Duration, results int64) {
	s.durations[s.next] = duration.Milliseconds()
	s.results[s.next] = results
	s.next = (s.next + 1) % liveStatWindow
	if s.n < liveStatWindow {
		s.n++
	}
}

// profile returns the p90 duration and the percentage of searches that
// returned at least one row.
func (s *liveStatSeries) profile() (p90 time.Duration, successPct int, samples int) {
	if s.n == 0 {
		return 0, 0, 0
	}
	values := make([]int64, s.n)
	copy(values, s.durations[:s.n])
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	rank := (90*s.n + 99) / 100 // ceil(0.90 * n)
	if rank < 1 {
		rank = 1
	}
	if rank > s.n {
		rank = s.n
	}
	withResults := 0
	for i := 0; i < s.n; i++ {
		if s.results[i] > 0 {
			withResults++
		}
	}
	return time.Duration(values[rank-1]) * time.Millisecond, withResults * 100 / s.n, s.n
}

// observeLiveStat records one outcome. Skipped indexers never ran, so they
// carry no timing evidence.
func (m *Manager) observeLiveStat(outcome LiveIndexerResult) {
	if outcome.Skipped || outcome.IndexerID <= 0 {
		return
	}
	m.liveStatsMu.Lock()
	defer m.liveStatsMu.Unlock()
	series := m.liveStats[outcome.IndexerID]
	if series == nil {
		series = &liveStatSeries{}
		m.liveStats[outcome.IndexerID] = series
	}
	series.add(outcome.Duration, int64(outcome.Results))
}

// liveIndexerIsSlack reports a site that usually answers late and rarely
// brings a result. With too little evidence it is not slack, so the fan-out
// keeps waiting.
func (m *Manager) liveIndexerIsSlack(indexerID int64) bool {
	m.liveStatsMu.Lock()
	series := m.liveStats[indexerID]
	var p90 time.Duration
	var success, samples int
	if series != nil {
		p90, success, samples = series.profile()
	}
	m.liveStatsMu.Unlock()
	return samples >= liveStatMinSamples && p90 >= liveSlowP90 && success <= liveSlackSuccessPct
}

// allPendingSlack reports whether every indexer still running is a site that
// usually only produces a timeout. At least one other indexer must already
// have answered, otherwise there is nothing to finish early.
func (m *Manager) allPendingSlack(pending map[int64]store.Indexer, answered int) bool {
	if answered == 0 || len(pending) == 0 {
		return false
	}
	for id := range pending {
		if !m.liveIndexerIsSlack(id) {
			return false
		}
	}
	return true
}

// seedLiveStats warms the per-indexer picture from the searches already stored
// so the first request after a restart benefits from the early finish. It is
// best effort: a missing table or a slow database only means a normal wait.
func (m *Manager) seedLiveStats(ctx context.Context) {
	samples, err := m.store.RecentLiveIndexerSamples(ctx, 1000)
	if err != nil {
		m.logger.Debug("live stats: cannot read recent searches", "error", err)
		return
	}
	m.liveStatsMu.Lock()
	defer m.liveStatsMu.Unlock()
	for _, sample := range samples {
		if sample.Skipped || sample.IndexerID <= 0 {
			continue
		}
		series := m.liveStats[sample.IndexerID]
		if series == nil {
			series = &liveStatSeries{}
			m.liveStats[sample.IndexerID] = series
		}
		series.add(time.Duration(sample.Duration)*time.Millisecond, sample.Results)
	}
}
