package indexer

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/buzzqw/indexarr/internal/engine"
	"github.com/buzzqw/indexarr/internal/feed"
	"github.com/buzzqw/indexarr/internal/store"
)

// EnrichReport is the outcome of one infohash backfill pass.
type EnrichReport struct {
	Candidates int
	Attempted  int
	Recovered  int
	Merged     int
	Failed     int
	// Skipped counts the sources left alone because opening them is an
	// engagement action (a thank-you) or belongs to another indexer.
	Skipped int
}

// EnrichMissingInfohashes recovers the infohash where the listing did not
// carry one. It opens the sealed download reference of hashless releases and
// reads the magnet found on the detail page or the .torrent the tracker sends
// back. The work is capped per indexer so a single site is never hammered.
//
// The rule is the same for public and private trackers: the hash of a private
// .torrent includes the private flag and the passkey, so an equal hash really
// means the same torrent.
func (m *Manager) EnrichMissingInfohashes(ctx context.Context, perIndexer int) (EnrichReport, error) {
	var report EnrichReport
	if m == nil || m.store == nil || m.vault == nil {
		return report, nil
	}
	if perIndexer <= 0 {
		perIndexer = 10
	}
	candidates, err := m.store.HashlessSources(ctx, 5000)
	if err != nil {
		return report, err
	}
	report.Candidates = len(candidates)
	used := make(map[int64]int)
	engaging := make(map[int64]bool)
	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		if strings.TrimSpace(candidate.DownloadRef) == "" {
			continue
		}
		if used[candidate.IndexerID] >= perIndexer {
			continue
		}
		parsed, openErr := OpenDownloadRef(m.vault, candidate.DownloadRef)
		if openErr != nil {
			report.Failed++
			continue
		}
		// A Smartfeed topic is sealed for the native MirCrew indexer: only a
		// client's grab may open it. Read with the feed's own connector it
		// failed every pass ("pending topic"), ten times per run.
		if parsed.IndexerID > 0 && parsed.IndexerID != candidate.IndexerID {
			report.Skipped++
			continue
		}
		if m.engagesOnGrab(ctx, candidate.IndexerID, engaging) {
			report.Skipped++
			continue
		}
		used[candidate.IndexerID]++
		report.Attempted++
		payload, downloadErr := m.Download(ctx, candidate.IndexerID, parsed.Item)
		if downloadErr != nil {
			report.Failed++
			m.recordEnrichFailure(ctx, candidate, downloadErr)
			continue
		}
		hash := feed.MagnetHash(payload.Magnet)
		if hash == "" && len(payload.Body) > 0 {
			if computed, hashErr := feed.InfoHash(payload.Body); hashErr == nil {
				hash = computed
			}
		}
		if hash == "" {
			report.Failed++
			continue
		}
		if len(payload.Body) > 0 {
			contentType := strings.TrimSpace(payload.ContentType)
			if contentType == "" {
				contentType = "application/x-bittorrent"
			}
			_ = m.store.SavePayload(ctx, candidate.ReleaseID, payload.Body, contentType, "torrent")
		}
		keeperID, merged, mergeErr := m.store.SetReleaseInfoHash(ctx, candidate.ReleaseID, hash)
		if mergeErr != nil {
			report.Failed++
			continue
		}
		report.Recovered++
		if merged && keeperID != candidate.ReleaseID {
			report.Merged++
		}
		// The source answers again: a later grab must not inherit the failures
		// of a page that was dead yesterday.
		_ = m.store.ClearSourceDownloadFailure(ctx, candidate.ReleaseID, candidate.IndexerID)
	}
	if report.Attempted > 0 {
		m.logger.Info("indexer: infohash enrichment",
			"candidates", report.Candidates,
			"attempted", report.Attempted,
			"recovered", report.Recovered,
			"merged", report.Merged,
			"failed", report.Failed,
			"skipped", report.Skipped)
	}
	return report, nil
}

// recordEnrichFailure keeps a dead release page from being opened at every
// pass. The candidates are read oldest first, so without this the same few
// hundred pages the site has removed are retried forever (about a hundred 404s
// a day on limetorrents). A 404/410 counts on the source and quarantines it;
// anything else is only noted for the release card, because a login or a
// timeout says nothing about the link.
func (m *Manager) recordEnrichFailure(ctx context.Context, candidate store.HashlessSource, downloadErr error) {
	if errors.Is(downloadErr, engine.ErrLinkGone) {
		failures, until, err := m.store.RecordSourceDownloadFailure(ctx, candidate.ReleaseID, candidate.IndexerID, downloadErr.Error())
		if err != nil {
			m.logger.Debug("indexer: cannot record the dead release link", "indexer", candidate.IndexerSlug, "error", err)
			return
		}
		if failures == 2 {
			m.logger.Info("indexer: release page gone",
				"indexer", candidate.IndexerSlug,
				"release", candidate.Title,
				"retry_after", until.Format(time.RFC3339))
		}
		return
	}
	_ = m.store.RecordSourceDownloadNotice(ctx, candidate.ReleaseID, candidate.IndexerID, downloadErr.Error())
}

// engagesOnGrab reports an indexer whose download runs an engagement action
// first (download.before: MirCrew thanks the topic to unlock the magnet). A
// background pass must never perform it: it thanked about ten MirCrew topics
// per run on the user's account for releases nobody had asked for. The answer
// is cached per pass; a definition that cannot be read keeps the old
// behaviour, since its download would fail anyway.
func (m *Manager) engagesOnGrab(ctx context.Context, indexerID int64, cache map[int64]bool) bool {
	if engages, ok := cache[indexerID]; ok {
		return engages
	}
	engages := false
	if indexer, err := m.store.GetIndexer(ctx, indexerID); err == nil {
		if def, defErr := m.definitionFor(ctx, indexer); defErr == nil && def != nil {
			engages = def.Download != nil && def.Download.Before != nil
		}
	}
	cache[indexerID] = engages
	return engages
}

// EnrichTargets is the default per-site budget of the enrichment job. It is
// exported so the command can report the configured value without duplicating
// the number.
func EnrichTargets() int { return 10 }
