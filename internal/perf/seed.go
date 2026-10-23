// Package perf contains the dataset generator used by the benchmarks, the
// leak test and the benchseed command. Nothing in production imports it: the
// server binary never links this code.
package perf

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/buzzqw/indexarr/internal/store"
	"github.com/buzzqw/indexarr/internal/textutil"
)

// DefaultAPIKey is the API key written into benchmark databases so the shell
// harness and the in-process benchmarks can call the Torznab endpoint without
// scraping the startup log.
const DefaultAPIKey = "bench0000000000000000000000000000000000000000000000000000000000"

// trackers are the synthetic legacy sources every seeded release belongs to.
// Ten sources make the source filter selective enough to be realistic.
var trackers = []string{
	"alpha-tracker", "beta-tracker", "gamma-tracker", "delta-tracker",
	"epsilon-tracker", "zeta-tracker", "eta-tracker", "theta-tracker",
	"iota-tracker", "kappa-tracker",
}

var categories = []string{"2040", "2045", "5040", "5045", "5030", "2000", "5000", "3000", "7000"}

var qualityTags = []string{
	"1080p WEB-DL DDP5.1 x264", "2160p WEB-DL DDP5.1 HDR x265",
	"1080p BluRay x264", "720p WEB-DL AAC x264", "1080p HDTV x264",
	"2160p BluRay DTS-HD x265", "480p WEB-DL x264",
}

var languages = []string{"ITA", "ENG", "ITA ENG", "MULTI"}

// Options controls the generated dataset.
type Options struct {
	// Releases is the number of release rows to create.
	Releases int
	// APIKey, when not empty, is stored in the settings table.
	APIKey string
	// Hidden is the fraction of releases marked hidden (0 disables it).
	Hidden float64
}

// Seed fills st with a deterministic, Torznab-shaped dataset. It inserts rows
// directly with one prepared statement inside a single transaction: running
// UpsertRelease 50.000 times would measure the generator, not the schema.
// Triggers still fire, so the FTS index and every constraint are exercised.
func Seed(ctx context.Context, st *store.Store, opts Options) error {
	if opts.Releases <= 0 {
		return fmt.Errorf("perf: releases must be positive")
	}
	if opts.APIKey != "" {
		settings, err := st.LoadSettings(ctx)
		if err != nil {
			return fmt.Errorf("load settings: %w", err)
		}
		settings.APIKey = opts.APIKey
		if err := st.SaveSettings(ctx, settings); err != nil {
			return fmt.Errorf("save settings: %w", err)
		}
	}

	sourceIDs, err := insertSources(ctx, st)
	if err != nil {
		return err
	}
	if err := insertReleases(ctx, st, opts, sourceIDs); err != nil {
		return err
	}
	if err := insertProvenance(ctx, st); err != nil {
		return err
	}
	_, _ = st.DB().ExecContext(ctx, "ANALYZE")
	return nil
}

// SeedFile opens (creating it when needed) the database at path and fills it.
// It is the entry point of the benchseed command.
func SeedFile(ctx context.Context, path string, opts Options) error {
	st, err := store.Open(path)
	if err != nil {
		return err
	}
	defer st.Close()
	return Seed(ctx, st, opts)
}

func insertSources(ctx context.Context, st *store.Store) ([]int64, error) {
	ids := make([]int64, 0, len(trackers))
	for _, name := range trackers {
		res, err := st.DB().ExecContext(ctx,
			`INSERT INTO sources(name, kind, manager, url, enabled)
			 VALUES(?, 'torznab', 'auto', ?, 1)`,
			name, fmt.Sprintf("http://%s.test/api", name))
		if err != nil {
			// A re-run on an existing database keeps the source already there.
			var id int64
			if scanErr := st.DB().QueryRowContext(ctx,
				`SELECT id FROM sources WHERE name = ?`, name).Scan(&id); scanErr != nil {
				return nil, fmt.Errorf("insert source %s: %w", name, err)
			}
			ids = append(ids, id)
			continue
		}
		id, err := res.LastInsertId()
		if err != nil {
			return nil, fmt.Errorf("source id %s: %w", name, err)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func insertReleases(ctx context.Context, st *store.Store, opts Options, sourceIDs []int64) error {
	tx, err := st.DB().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO releases(
			dedup_key, infohash, imdb_id, tmdb_id, tvdb_id, title, search_title,
			size, seeders, leechers, category, published_at, source_id, source_name,
			guid, magnet, torrent, torrent_size, torrent_fetched_at, first_seen_at,
			last_seen_at, times_seen, hidden, protocol)
		VALUES(?, ?, '', '', '', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, 'torrent')`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	now := time.Now()
	for i := 0; i < opts.Releases; i++ {
		title := releaseTitle(i)
		hash := fmt.Sprintf("%040x", i+1)
		sourceIdx := i % len(sourceIDs)
		sourceID := sourceIDs[sourceIdx]
		sourceName := trackers[sourceIdx]
		published := now.Add(-time.Duration(i) * 90 * time.Second)
		size := int64(300_000_000 + (i%997)*7_000_000)
		seeders := int64(i % 500)
		leechers := int64(i % 40)
		category := categories[i%len(categories)]
		magnet := fmt.Sprintf("magnet:?xt=urn:btih:%s&dn=%s", hash, strings.ReplaceAll(title, " ", "."))
		var torrent []byte
		var torrentSize int64
		if i%20 == 0 {
			// Five percent of the rows carries a small placeholder payload: the
			// Torznab link then points at /dl/<id>.torrent instead of a magnet.
			torrent = []byte(fmt.Sprintf("d8:announce30:http://tracker.test/announce4:infod4:name%d:%se", len(title), title))
			torrentSize = int64(len(torrent))
		}
		hidden := 0
		if opts.Hidden > 0 && float64(i)/float64(opts.Releases) < opts.Hidden {
			hidden = 1
		}
		if _, err := stmt.ExecContext(ctx,
			fmt.Sprintf("bench-%d", i), hash, title, textutil.NormalizeTitle(title),
			size, seeders, leechers, category, published.Unix(), sourceID, sourceName,
			hash, magnet, torrent, torrentSize, published.Unix(),
			published.Unix(), published.Unix(), hidden); err != nil {
			return fmt.Errorf("seed release %d: %w", i, err)
		}
	}
	if err := stmt.Close(); err != nil {
		return err
	}
	return tx.Commit()
}

// insertProvenance gives the first releases one to three indexer rows, which is
// what a multi-indexer installation looks like.
func insertProvenance(ctx context.Context, st *store.Store) error {
	if _, err := st.DB().ExecContext(ctx, `
		INSERT INTO definitions(id, schema_version, family, state, origin, author, body, hash, updated_at)
		VALUES('bench-def', 1, 'torznab', 'stabile', 'local', 'bench', 'id: bench-def
name: bench', 'bench', 0)
		ON CONFLICT(id) DO NOTHING`); err != nil {
		return fmt.Errorf("seed definition: %w", err)
	}
	indexerIDs := make([]int64, 0, 3)
	for i, name := range []string{"Alpha Tracker", "Beta Tracker", "Gamma Tracker"} {
		slug := fmt.Sprintf("bench-indexer-%d", i)
		if _, err := st.DB().ExecContext(ctx, `
			INSERT INTO indexers(definition_id, protocol, slug, name, enabled, created_at, updated_at)
			VALUES('bench-def', 'torrent', ?, ?, 1, 0, 0)
			ON CONFLICT(slug) DO NOTHING`, slug, name); err != nil {
			return fmt.Errorf("seed indexer %s: %w", slug, err)
		}
		var id int64
		if err := st.DB().QueryRowContext(ctx,
			`SELECT id FROM indexers WHERE slug = ?`, slug).Scan(&id); err != nil {
			return fmt.Errorf("indexer id %s: %w", slug, err)
		}
		indexerIDs = append(indexerIDs, id)
	}
	for wanted, indexerID := range indexerIDs {
		if _, err := st.DB().ExecContext(ctx, `
			INSERT OR IGNORE INTO release_sources(
				release_id, indexer_id, first_seen_at, last_seen_at, seeders, leechers, download_ref)
			SELECT id, ?, first_seen_at, last_seen_at, seeders, leechers, ''
			FROM releases WHERE (id % ?) <= ?`,
			indexerID, len(indexerIDs), wanted); err != nil {
			return fmt.Errorf("seed provenance %d: %w", indexerID, err)
		}
	}
	return nil
}

func releaseTitle(i int) string {
	year := 1990 + i%36
	season := 1 + i%8
	episode := 1 + i%24
	quality := qualityTags[i%len(qualityTags)]
	language := languages[i%len(languages)]
	switch i % 3 {
	case 0:
		return fmt.Sprintf("The Benchmark Movie %d (%d) %s %s-GRP%02d", i, year, quality, language, i%41)
	case 1:
		return fmt.Sprintf("Benchmark Show S%02dE%02d %s %s-GRP%02d", season, episode, quality, language, i%41)
	default:
		return fmt.Sprintf("Benchmark.Documentary.%d.%d.%s.%s-GRP%02d", year, i, strings.ReplaceAll(quality, " ", "."), language, i%41)
	}
}
