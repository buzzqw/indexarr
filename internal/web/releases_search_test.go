package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/buzzqw/indexarr/internal/store"
)

func insertSearchRelease(t *testing.T, st *store.Store, title, category string) {
	t.Helper()
	release := store.Release{
		DedupKey:    "key-" + title,
		Title:       title,
		SearchTitle: store.NormalizeSearch(title),
		Size:        1024,
		Category:    category,
		Magnet:      "magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567",
		FirstSeenAt: time.Now(),
		LastSeenAt:  time.Now(),
	}
	if _, err := st.UpsertRelease(context.Background(), &release); err != nil {
		t.Fatalf("UpsertRelease: %v", err)
	}
}

func TestReleasesExcludeAndRegex(t *testing.T) {
	env := newTestEnv(t)
	insertSearchRelease(t, env.store, "The Show 1080p", "5040")
	insertSearchRelease(t, env.store, "The Show 720p", "5030")
	insertSearchRelease(t, env.store, "A Movie 1080p", "2040")

	excluded := get(t, env.handler, "/releases?q=show+-1080p")
	if excluded.Code != http.StatusOK {
		t.Fatalf("status = %d", excluded.Code)
	}
	body := excluded.Body.String()
	if !strings.Contains(body, "The Show 720p") {
		t.Error("the 720p release is missing")
	}
	if strings.Contains(body, "The Show 1080p") || strings.Contains(body, "A Movie 1080p") {
		t.Error("an excluded release was returned")
	}

	regexBody := get(t, env.handler, "/releases?q=show&regex=1").Body.String()
	if !strings.Contains(regexBody, "The Show 720p") || !strings.Contains(regexBody, "The Show 1080p") {
		t.Error("regex include did not return both shows")
	}
	if strings.Contains(regexBody, "A Movie 1080p") {
		t.Error("regex include returned the movie")
	}

	quoted := url.Values{"q": {`"^The Show"`}, "regex": {"1"}}
	quotedBody := get(t, env.handler, "/releases?"+quoted.Encode()).Body.String()
	if !strings.Contains(quotedBody, "The Show 720p") || strings.Contains(quotedBody, "A Movie") {
		t.Error("quoted regex is not applied")
	}

	invalid := get(t, env.handler, "/releases?q=%5B&regex=1")
	if invalid.Code != http.StatusOK {
		t.Fatalf("invalid regex status = %d", invalid.Code)
	}
	if !strings.Contains(invalid.Body.String(), "Invalid regular expression") {
		t.Error("invalid regex error missing")
	}
}

func TestReleasesResultsPartial(t *testing.T) {
	env := newTestEnv(t)
	insertSearchRelease(t, env.store, "The Show 720p", "5030")

	recorder := get(t, env.handler, "/releases/results?q=show&regex=1")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	body := recorder.Body.String()
	if strings.Contains(body, "<!doctype") || strings.Contains(body, "<html") {
		t.Error("the partial contains the full layout")
	}
	if !strings.Contains(body, "The Show 720p") || !strings.Contains(body, "Results: ") {
		t.Errorf("partial body = %s", body)
	}
	if contentType := recorder.Header().Get("Content-Type"); !strings.Contains(contentType, "text/html") {
		t.Errorf("content type = %q", contentType)
	}

	full := get(t, env.handler, "/releases").Body.String()
	if !strings.Contains(full, `data-live-search`) || !strings.Contains(full, `name="regex"`) {
		t.Error("live search controls missing from the full page")
	}
}

func TestPagerUsesFullPagePath(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/releases/results?q=x", nil)
	pager := newPager(request, "/releases", 100, 50, 0)
	if !strings.HasPrefix(pager.Next, "/releases?") {
		t.Errorf("next = %q", pager.Next)
	}
	if strings.Contains(pager.Next, "/results") {
		t.Errorf("next points to the partial: %q", pager.Next)
	}
}

func TestReleasesFileNamesAndAdultFilter(t *testing.T) {
	env := newTestEnv(t)
	insertSearchRelease(t, env.store, "Family Show 1080p", "5040")
	insertSearchRelease(t, env.store, "Adult Show 1080p", "6000")

	// Find the family release in the DB to attach a file list.
	releases, _, err := env.store.SearchReleases(context.Background(), store.Filter{Query: "Family", Limit: 1})
	if err != nil || len(releases) != 1 {
		t.Fatalf("find family release: %+v, %v", releases, err)
	}
	if err := env.store.ReplaceReleaseFiles(context.Background(), releases[0].ID, []store.ReleaseFile{
		{Path: "Family.Show.S01E01.1080p.mkv", Size: 1000},
	}); err != nil {
		t.Fatalf("ReplaceReleaseFiles: %v", err)
	}

	fileBody := get(t, env.handler, "/releases?q=S01E01&files=1").Body.String()
	if !strings.Contains(fileBody, "Family Show 1080p") || strings.Contains(fileBody, "Adult Show") {
		t.Error("file-name search did not return only the family release")
	}
	if !strings.Contains(fileBody, `name="files"`) {
		t.Error("the file-name checkbox is missing")
	}
	titleBody := get(t, env.handler, "/releases?q=S01E01").Body.String()
	if strings.Contains(titleBody, "Family Show 1080p") {
		t.Error("a title search matched a file name")
	}

	adultBody := get(t, env.handler, "/releases?no_adult=1").Body.String()
	if strings.Contains(adultBody, "Adult Show") {
		t.Error("the adult filter did not hide the XXX category")
	}
	if !strings.Contains(adultBody, "Family Show") {
		t.Error("the adult filter hid a non-adult release")
	}
}

// The TMDB and TVDB buttons search the title a person would type, not the
// release name with its resolution, codec and group.
func TestReleaseMetadataSearchUsesTheDerivedTitle(t *testing.T) {
	env := newTestEnv(t)
	insertSearchRelease(t, env.store,
		"Mr-9.Do.or.Die.2023.1080p.ITA-ENG.BluRay.x265.AAC-V3SP4EV3R.mkv", "2040")
	insertSearchRelease(t, env.store,
		"Goodrich Il padre dell anno 2024 1080p H264 iTA EnG AC3 Sub E", "2040")
	body := get(t, env.handler, "/releases").Body.String()
	for _, want := range []string{
		"https://www.themoviedb.org/search?query=mr-9%20do%20or%20die",
		"https://thetvdb.com/search?query=mr-9%20do%20or%20die",
		"https://www.themoviedb.org/search?query=goodrich",
		"https://thetvdb.com/search?query=goodrich",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("release actions missing %q", want)
		}
	}
	if strings.Contains(body, "themoviedb.org/search?query=mr-9.do.or.die") {
		t.Error("the TMDB link still uses the raw release name")
	}
}

// Every row names the source that supplied the release and offers a direct
// send without opening the detail page.
func TestReleaseRowsShowSourceAndSendButton(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	seedHomeDefinition(t, env, "row-source-def")
	indexerID, err := env.store.CreateIndexer(ctx, store.Indexer{
		DefinitionID: "row-source-def", Protocol: "torrent", Slug: "row-source",
		Name: "Source One", Enabled: true, Priority: 25, ConfigJSON: "{}",
	})
	if err != nil {
		t.Fatal(err)
	}
	insertSearchRelease(t, env.store, "Source Release 1080p", "5040")
	releases, _, err := env.store.SearchReleases(ctx, store.Filter{})
	if err != nil || len(releases) != 1 {
		t.Fatalf("releases = %d, %v", len(releases), err)
	}
	now := time.Now()
	if err := env.store.RecordReleaseSource(ctx, store.ReleaseSource{
		ReleaseID: releases[0].ID, IndexerID: indexerID, FirstSeenAt: now, LastSeenAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	body := get(t, env.handler, "/releases?lang=en").Body.String()
	for _, want := range []string{
		"Source One",
		`formaction="/releases/` + strconv.FormatInt(releases[0].ID, 10) + `/send"`,
		`name="return" value="/releases?lang=en"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("release row missing %q", want)
		}
	}
}
