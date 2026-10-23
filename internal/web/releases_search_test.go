package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
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
	if !strings.Contains(body, "The Show 720p") || !strings.Contains(body, "release(s) found") {
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
