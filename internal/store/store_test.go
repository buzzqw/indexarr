package store

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "indexarr-test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestSettingsAndAPIKey(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	settings, err := st.LoadSettings(ctx)
	if err != nil {
		t.Fatalf("LoadSettings: %v", err)
	}
	if settings.DefaultIntervalSeconds != 3600 || settings.Workers != 2 || !settings.FetchTorrents {
		t.Fatalf("unexpected defaults: %#v", settings)
	}

	settings.PublicURL = "http://idx.example/"
	settings.FlareSolverrURL = "http://fs.example:8191/"
	settings.RetentionDays = 14
	settings.Workers = 4
	settings.FetchTorrents = false
	settings.NotifyWebhookURL = "http://hook.example/indexarr"
	settings.NotifyNewReleases = false
	settings.AutoDisableAfter = 5
	if err := st.SaveSettings(ctx, settings); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}
	reloaded, err := st.LoadSettings(ctx)
	if err != nil {
		t.Fatalf("LoadSettings: %v", err)
	}
	if reloaded.PublicURL != "http://idx.example" {
		t.Errorf("public URL = %q", reloaded.PublicURL)
	}
	if reloaded.FlareSolverrURL != "http://fs.example:8191" {
		t.Errorf("flaresolverr URL = %q", reloaded.FlareSolverrURL)
	}
	if reloaded.RetentionDays != 14 || reloaded.Workers != 4 || reloaded.FetchTorrents {
		t.Errorf("reloaded = %#v", reloaded)
	}
	if reloaded.NotifyWebhookURL != "http://hook.example/indexarr" || reloaded.NotifyNewReleases ||
		reloaded.AutoDisableAfter != 5 {
		t.Errorf("notification settings = %#v", reloaded)
	}

	key, created, err := st.EnsureAPIKey(ctx)
	if err != nil {
		t.Fatalf("EnsureAPIKey: %v", err)
	}
	if !created || len(key) != 64 {
		t.Fatalf("key = %q, created = %v", key, created)
	}
	again, createdAgain, err := st.EnsureAPIKey(ctx)
	if err != nil {
		t.Fatalf("EnsureAPIKey: %v", err)
	}
	if createdAgain || again != key {
		t.Errorf("the API key must be stable: %q %v", again, createdAgain)
	}
}

func TestReleaseUpsertAndSearch(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	mustCreateDefinition(t, st, "def-a")
	mustCreateDefinition(t, st, "def-b")
	sourceID := mustCreateIndexer(t, st, "def-a", "source-a", "Source A")
	otherID := mustCreateIndexer(t, st, "def-b", "source-b", "Source B")

	hash := "0123456789abcdef0123456789abcdef01234567"
	now := time.Now().Add(-2 * time.Hour)
	show := Release{
		DedupKey:    hash,
		InfoHash:    hash,
		Title:       "The Show S01E02 1080p",
		SearchTitle: NormalizeSearch("The Show S01E02 1080p"),
		Size:        1000,
		Seeders:     5,
		Leechers:    1,
		Category:    "5040",
		PublishedAt: now,
		Magnet:      "magnet:?xt=urn:btih:" + hash,
		FirstSeenAt: now,
		LastSeenAt:  now,
	}
	isNew, err := st.UpsertRelease(ctx, &show)
	if err != nil {
		t.Fatalf("UpsertRelease: %v", err)
	}
	if !isNew || show.TimesSeen != 1 {
		t.Fatalf("new = %v, times seen = %d", isNew, show.TimesSeen)
	}
	recordProvenance(t, st, show.ID, sourceID)

	showAgain := show
	showAgain.Seeders = 9
	showAgain.Size = 2000
	isNew, err = st.UpsertRelease(ctx, &showAgain)
	if err != nil {
		t.Fatalf("UpsertRelease: %v", err)
	}
	if isNew || showAgain.TimesSeen != 2 || showAgain.ID != show.ID {
		t.Fatalf("second upsert: new=%v times=%d id=%d/%d", isNew, showAgain.TimesSeen, showAgain.ID, show.ID)
	}
	stored, _ := st.GetRelease(ctx, show.ID)
	if stored.Seeders != 9 || stored.Size != 2000 {
		t.Errorf("stored seeders/size = %d/%d", stored.Seeders, stored.Size)
	}

	movie := Release{
		DedupKey: "movie-key", Title: "A Movie 2024 1080p",
		SearchTitle: NormalizeSearch("A Movie 2024 1080p"),
		Size:        5000, Seeders: 20, Category: "2040",
		PublishedAt: now.Add(time.Hour),
		FirstSeenAt: now, LastSeenAt: now,
	}
	secondShow := Release{
		DedupKey: "show2-key", Title: "The Show S02E01 720p",
		SearchTitle: NormalizeSearch("The Show S02E01 720p"),
		Size:        3000, Seeders: 1, Category: "5030",
		PublishedAt: now.Add(2 * time.Hour),
		FirstSeenAt: now, LastSeenAt: now,
	}
	for _, rel := range []*Release{&movie, &secondShow} {
		if _, err := st.UpsertRelease(ctx, rel); err != nil {
			t.Fatalf("UpsertRelease: %v", err)
		}
	}
	recordProvenance(t, st, movie.ID, otherID)
	recordProvenance(t, st, secondShow.ID, sourceID)

	cases := []struct {
		name  string
		f     Filter
		total int64
	}{
		{"query", Filter{Query: "show"}, 2},
		{"query 1080p", Filter{Query: "1080p"}, 2},
		{"query no match", Filter{Query: "nonexistent"}, 0},
		{"category group", Filter{Categories: []string{"5000"}}, 2},
		{"category exact", Filter{Categories: []string{"5040"}}, 1},
		{"indexer", Filter{IndexerID: otherID}, 1},
		{"indexers", Filter{IndexerIDs: []int64{sourceID, otherID}}, 3},
		{"indexers paginated", Filter{IndexerIDs: []int64{sourceID, otherID}, Limit: 1, Offset: 1}, 3},
		{"season and episode", Filter{Query: "show", Season: 1, Episode: 2}, 1},
		{"another episode", Filter{Query: "show", Season: 2, Episode: 1}, 1},
		{"missing episode", Filter{Query: "show", Season: 2, Episode: 9}, 0},
		{"exclude normal", Filter{Query: "show", Exclude: []string{"1080p"}}, 1},
		{"only exclusion", Filter{Exclude: []string{"1080p"}}, 1},
		{"regex include and exclude", Filter{Regex: true, Include: []string{"(?i)^the show"}, Exclude: []string{"(?i)1080p"}}, 1},
		{"regex only exclusion", Filter{Regex: true, Exclude: []string{"(?i)1080p"}}, 1},
		{"regex char class", Filter{Regex: true, Include: []string{"(?i)s0[12]e0[12]"}}, 2},
		{"min seeders", Filter{MinSeeders: 5}, 2},
		{"min size", Filter{MinSize: 4000}, 1},
		{"max size", Filter{MaxSize: 2500}, 1},
		{"quality 1080p", Filter{Quality: "1080p"}, 2},
		{"quality 720p", Filter{Quality: "720p"}, 1},
		{"quality 2160p", Filter{Quality: "2160p"}, 0},
		{"pagination", Filter{Limit: 1, Offset: 1}, 3},
	}
	for _, tc := range cases {
		releases, total, err := st.SearchReleases(ctx, tc.f)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if total != tc.total {
			t.Errorf("%s: total = %d, want %d", tc.name, total, tc.total)
		}
		if tc.name == "pagination" && len(releases) != 1 {
			t.Errorf("pagination: %d rows", len(releases))
		}
	}

	byHash, found, err := st.ReleaseByInfoHash(ctx, hash)
	if err != nil || !found || byHash.ID != show.ID {
		t.Fatalf("ReleaseByInfoHash = %#v, %v, %v", byHash, found, err)
	}
	byKey, found, err := st.ReleaseByKey(ctx, "movie-key")
	if err != nil || !found || byKey.Title != movie.Title {
		t.Fatalf("ReleaseByKey = %#v, %v, %v", byKey, found, err)
	}

	stats, err := st.LoadStats(ctx)
	if err != nil {
		t.Fatalf("LoadStats: %v", err)
	}
	if stats.Releases != 3 || stats.Indexers != 2 || stats.EnabledIndexers != 2 {
		t.Errorf("stats = %#v", stats)
	}

	deleted, err := st.DeleteRelease(ctx, movie.ID)
	if err != nil || !deleted {
		t.Fatalf("DeleteRelease = %v, %v", deleted, err)
	}
	removed, err := st.PruneReleases(ctx, time.Now().Add(time.Hour))
	if err != nil || removed != 2 {
		t.Fatalf("PruneReleases = %d, %v", removed, err)
	}
	stats, _ = st.LoadStats(ctx)
	if stats.Releases != 0 {
		t.Errorf("releases after prune = %d", stats.Releases)
	}
}

func TestDedupeBackupAndClearTorrents(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	hash := "fedcba9876543210fedcba9876543210fedcba98"
	first := Release{
		DedupKey: "first", InfoHash: hash, Title: "First", SearchTitle: "first",
		Category: "5000",
	}
	if _, err := st.UpsertRelease(ctx, &first); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	if _, err := st.DB().ExecContext(ctx, `
		INSERT INTO releases(dedup_key, infohash, title, search_title, magnet, torrent, torrent_size,
			first_seen_at, last_seen_at, times_seen)
		VALUES('second', ?, 'Second', 'second', 'magnet:?xt=urn:btih:' || ?, x'6475', 2, ?, ?, 1)`,
		hash, hash, now, now); err != nil {
		t.Fatalf("insert duplicate: %v", err)
	}

	removed, err := st.DedupeByInfoHash(ctx)
	if err != nil || removed != 1 {
		t.Fatalf("DedupeByInfoHash = %d, %v", removed, err)
	}
	keeper, err := st.GetRelease(ctx, first.ID)
	if err != nil {
		t.Fatalf("GetRelease: %v", err)
	}
	if keeper.Magnet == "" || len(keeper.Torrent) == 0 || keeper.TorrentSize != 2 {
		t.Errorf("merged data missing: %#v", keeper)
	}

	backup := filepath.Join(t.TempDir(), "backup.db")
	if err := st.BackupTo(ctx, backup); err != nil {
		t.Fatalf("BackupTo: %v", err)
	}
	info, err := os.Stat(backup)
	if err != nil || info.Size() == 0 {
		t.Fatalf("backup file: %v, %v", info, err)
	}

	drained, err := st.ClearTorrents(ctx)
	if err != nil || drained != 1 {
		t.Fatalf("ClearTorrents = %d, %v", drained, err)
	}
	keeper, _ = st.GetRelease(ctx, first.ID)
	if len(keeper.Torrent) != 0 || keeper.TorrentSize != 0 {
		t.Errorf("torrent not cleared: %#v", keeper)
	}

	if err := st.Vacuum(ctx); err != nil {
		t.Fatalf("Vacuum: %v", err)
	}
	verdict, err := st.IntegrityCheck(ctx)
	if err != nil || !strings.EqualFold(verdict, "ok") {
		t.Fatalf("IntegrityCheck = %q, %v", verdict, err)
	}
	if n, err := st.DeleteAllReleases(ctx); err != nil || n != 1 {
		t.Fatalf("DeleteAllReleases = %d, %v", n, err)
	}
}

func TestReleaseFlagsAndBlocklist(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	rel := Release{
		DedupKey: "flags", Title: "Blocked CAM Release 1080p",
		SearchTitle: NormalizeSearch("Blocked CAM Release 1080p"),
		Magnet:      "magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567",
		Category:    "5040",
	}
	if _, err := st.UpsertRelease(ctx, &rel); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetReleaseFlags(ctx, rel.ID, true, false); err != nil {
		t.Fatal(err)
	}
	stored, _ := st.GetRelease(ctx, rel.ID)
	if !stored.Favorite || stored.Hidden {
		t.Errorf("flags after favorite = %#v", stored)
	}
	if _, err := st.SetReleaseFlags(ctx, rel.ID, true, true); err != nil {
		t.Fatal(err)
	}
	stored, _ = st.GetRelease(ctx, rel.ID)
	if !stored.Favorite || !stored.Hidden {
		t.Errorf("flags after hide = %#v", stored)
	}

	for _, rule := range []struct{ kind, value string }{
		{BlockKindTitle, "cam"},
		{BlockKindRegex, "blocked.*release"},
		{BlockKindInfohash, "0123456789abcdef0123456789abcdef01234567"},
		{BlockKindSource, "Source A"},
		{BlockKindCategory, "5000"},
	} {
		if _, err := st.AddBlocklistRule(ctx, rule.kind, rule.value, "test"); err != nil {
			t.Fatalf("add %s: %v", rule.kind, err)
		}
	}
	if _, err := st.AddBlocklistRule(ctx, BlockKindRegex, "[", ""); err == nil {
		t.Error("invalid regex accepted")
	}
	rules, err := st.ListBlocklist(ctx)
	if err != nil || len(rules) != 5 {
		t.Fatalf("rules = %d, %v", len(rules), err)
	}
	matcher := NewBlocklistMatcher(rules)
	if rule, ok := matcher.Match("", "Blocked CAM Release 1080p", "Source A", "5040"); !ok {
		t.Error("blocklist did not match")
	} else if rule.Kind == "" {
		t.Error("matched rule is empty")
	}
	if _, ok := matcher.Match("", "Allowed Release", "Source B", "2040"); ok {
		t.Error("blocklist matched an allowed release")
	}
	if _, err := st.DeleteBlocklistRule(ctx, rules[0].ID); err != nil {
		t.Fatal(err)
	}
	rules, _ = st.ListBlocklist(ctx)
	if len(rules) != 4 {
		t.Errorf("rules after delete = %d", len(rules))
	}
}

func TestSavedSearches(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	id, err := st.AddSavedSearch(ctx, "TV 1080p", "q=show&quality=1080p")
	if err != nil || id == 0 {
		t.Fatalf("AddSavedSearch = %d, %v", id, err)
	}
	list, err := st.ListSavedSearches(ctx)
	if err != nil || len(list) != 1 || list[0].Name != "TV 1080p" {
		t.Fatalf("list = %#v, %v", list, err)
	}
	if _, err := st.AddSavedSearch(ctx, "TV 1080p", "q=show&quality=720p"); err != nil {
		t.Fatal(err)
	}
	list, _ = st.ListSavedSearches(ctx)
	if len(list) != 1 || list[0].Query != "q=show&quality=720p" {
		t.Errorf("upsert = %#v", list)
	}
	if ok, err := st.DeleteSavedSearch(ctx, id); err != nil || !ok {
		t.Fatalf("DeleteSavedSearch = %v, %v", ok, err)
	}
	list, _ = st.ListSavedSearches(ctx)
	if len(list) != 0 {
		t.Errorf("list after delete = %#v", list)
	}
}

func TestNormalizeSearch(t *testing.T) {
	cases := map[string]string{
		"The.Show.S01E02-1080p":    "the show s01e02 1080p",
		"  Multiple   spaces  ":    "multiple spaces",
		"Movie (2024) [BluRay]":    "movie 2024 bluray",
		"Accenti àèìòù":            "accenti àèìòù",
		"Under_score and-dash.dot": "under score and dash dot",
	}
	for input, want := range cases {
		if got := NormalizeSearch(input); got != want {
			t.Errorf("NormalizeSearch(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestReleaseByExternalID(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	rel := Release{
		DedupKey: "external-1", GUID: "guid-1234",
		InfoHash: "0123456789abcdef0123456789abcdef01234567",
		Title:    "External", SearchTitle: "external",
	}
	if _, err := st.UpsertRelease(ctx, &rel); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{
		"guid-1234",
		"0123456789ABCDEF0123456789ABCDEF01234567",
		"external-1",
		"indexarr-external-1",
	} {
		found, ok, err := st.ReleaseByExternalID(ctx, id)
		if err != nil || !ok || found.ID != rel.ID {
			t.Errorf("ReleaseByExternalID(%q) = %#v, %v, %v", id, found, ok, err)
		}
	}
	if _, ok, err := st.ReleaseByExternalID(ctx, "missing"); err != nil || ok {
		t.Errorf("missing external id = %v, %v", ok, err)
	}
}

func recordProvenance(t *testing.T, st *Store, releaseID, indexerID int64) {
	t.Helper()
	if err := st.RecordReleaseSource(context.Background(), ReleaseSource{
		ReleaseID: releaseID, IndexerID: indexerID,
		FirstSeenAt: time.Now(), LastSeenAt: time.Now(),
	}); err != nil {
		t.Fatalf("RecordReleaseSource: %v", err)
	}
}
