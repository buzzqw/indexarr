package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/buzzqw/indexarr/internal/config"
	"github.com/buzzqw/indexarr/internal/definition"
	"github.com/buzzqw/indexarr/internal/store"
	"github.com/buzzqw/indexarr/internal/textutil"
)

// topicsUsage documents the command that imports and inspects the local topic
// history of a forum indexer.
func topicsUsage() {
	fmt.Fprint(os.Stderr, `usage: indexarr topics <command>

Commands:
  import   read the topics of a mircrew-indexer database into the history
  count    print how many topics the history holds for each indexer

Flags of "import":
  --from PATH       mircrew-indexer SQLite database (required)
  --config PATH     indexarr configuration file (default indexarr.json)
  --db PATH         indexarr SQLite database, overrides the configuration
  --indexer SLUG    indexer that receives the topics (default: the MirCrew one)
  --dry-run         read the source and print the count without storing
`)
}

func runTopicsCommand(args []string) int {
	if len(args) == 0 {
		topicsUsage()
		return 2
	}
	switch args[0] {
	case "import":
		return runTopicsImport(args[1:])
	case "count":
		return runTopicsCount(args[1:])
	default:
		topicsUsage()
		return 2
	}
}

func runTopicsCount(args []string) int {
	fs := flag.NewFlagSet("topics count", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	configPath := fs.String("config", "indexarr.json", "JSON configuration file")
	dbPath := fs.String("db", "", "SQLite database path, overrides the configuration")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if *dbPath != "" {
		cfg.DBPath = *dbPath
	}
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer st.Close()
	ctx := context.Background()
	indexers, err := st.ListIndexers(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	for _, item := range indexers {
		count, err := st.TopicCount(ctx, item.ID)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if count == 0 {
			continue
		}
		cursors, _ := st.TopicCursors(ctx, item.ID)
		progress := make([]string, 0, len(cursors))
		for forum, cursor := range cursors {
			progress = append(progress, fmt.Sprintf("f%s start=%d", forum, cursor.NextStart))
		}
		fmt.Printf("%-24s %6d topics  %s\n", item.Slug, count, strings.Join(progress, ", "))
	}
	return 0
}

func runTopicsImport(args []string) int {
	fs := flag.NewFlagSet("topics import", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	from := fs.String("from", "", "mircrew-indexer SQLite database")
	configPath := fs.String("config", "indexarr.json", "JSON configuration file")
	dbPath := fs.String("db", "", "SQLite database path, overrides the configuration")
	slug := fs.String("indexer", "", "indexer that receives the topics")
	dryRun := fs.Bool("dry-run", false, "print the count without storing")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if strings.TrimSpace(*from) == "" {
		fmt.Fprintln(os.Stderr, "topics import: --from is required")
		return 2
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if *dbPath != "" {
		cfg.DBPath = *dbPath
	}
	source, err := sql.Open("sqlite", "file:"+*from+"?mode=ro")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer source.Close()
	rows, err := source.Query(`SELECT topic_id, forum_id, title, url FROM topics ORDER BY topic_id`)
	if err != nil {
		fmt.Fprintln(os.Stderr, "read topics:", err)
		return 1
	}
	type sourceTopic struct {
		TopicID int64
		ForumID string
		Title   string
		URL     string
	}
	var topics []sourceTopic
	for rows.Next() {
		var item sourceTopic
		if err := rows.Scan(&item.TopicID, &item.ForumID, &item.Title, &item.URL); err != nil {
			rows.Close()
			fmt.Fprintln(os.Stderr, "read topics:", err)
			return 1
		}
		if strings.TrimSpace(item.Title) == "" || strings.TrimSpace(item.URL) == "" {
			continue
		}
		item.ForumID = strings.TrimSpace(item.ForumID)
		topics = append(topics, item)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		fmt.Fprintln(os.Stderr, "read topics:", err)
		return 1
	}
	if *dryRun {
		fmt.Printf("topics import: %d topics in %s (dry run)\n", len(topics), *from)
		return 0
	}
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer st.Close()
	ctx := context.Background()
	target, err := findTopicIndexer(ctx, st, *slug)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	standards := topicForumStandards(ctx, st, target.DefinitionID)
	converted := make([]store.Topic, 0, len(topics))
	now := time.Now()
	for _, item := range topics {
		converted = append(converted, store.Topic{
			IndexerID:   target.ID,
			ForumID:     item.ForumID,
			Title:       item.Title,
			TitleKey:    textutil.NormalizeTitle(item.Title),
			URL:         strings.TrimSpace(item.URL),
			Category:    standards[item.ForumID],
			FirstSeenAt: now,
			LastSeenAt:  now,
		})
	}
	added, err := st.UpsertTopics(ctx, target.ID, converted)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("topics import: %d read, %d new, %d already known for %s\n",
		len(converted), added, len(converted)-added, target.Slug)
	return 0
}

// findTopicIndexer picks the indexer that receives the imported topics: the
// one named on the command line, or the only MirCrew indexer of the database.
func findTopicIndexer(ctx context.Context, st *store.Store, slug string) (store.Indexer, error) {
	indexers, err := st.ListIndexers(ctx)
	if err != nil {
		return store.Indexer{}, err
	}
	slug = strings.TrimSpace(slug)
	for _, item := range indexers {
		if slug != "" {
			if strings.EqualFold(item.Slug, slug) {
				return item, nil
			}
			continue
		}
		if strings.EqualFold(item.DefinitionID, "mircrew") {
			return item, nil
		}
	}
	if slug != "" {
		return store.Indexer{}, fmt.Errorf("topics import: no indexer with slug %q", slug)
	}
	return store.Indexer{}, fmt.Errorf("topics import: no MirCrew indexer; pass --indexer")
}

// topicForumStandards maps a forum id to the Torznab category its topics
// belong to, using the catalog block of the indexer's definition.
func topicForumStandards(ctx context.Context, st *store.Store, definitionID string) map[string]int {
	out := map[string]int{}
	row, err := st.GetDefinition(ctx, definitionID)
	if err != nil {
		return out
	}
	parsed, err := definition.Parse([]byte(row.Body))
	if err != nil || !parsed.HasCatalog() {
		return out
	}
	for _, forum := range parsed.Catalog.Forums {
		if forum.Standard > 0 {
			out[strings.TrimSpace(forum.ID)] = forum.Standard
		}
	}
	return out
}
