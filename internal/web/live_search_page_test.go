package web

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestLiveLimitBoundsThePageSize(t *testing.T) {
	for _, tc := range []struct{ in, want int }{
		{0, 50}, {-1, 50}, {25, 50}, {100, 100}, {200, 200}, {900, 500},
	} {
		if got := liveLimit(tc.in); got != tc.want {
			t.Errorf("liveLimit(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestLivePageValuesKeepTheSearch(t *testing.T) {
	form := liveSearchForm{
		Mode: "tvsearch", Query: "Show", Season: 3, Episode: 2, Year: 2024,
		IMDbID: "tt123", IndexerIDs: []int64{7, 9}, Category: "5040",
	}
	values := livePageValues(form, 100, 1700000000)
	for key, want := range map[string]string{
		"source": "live", "live_since": "1700000000", "live_limit": "100",
		"mode": "tvsearch", "q": "Show", "live_season": "3", "episode": "2",
		"year": "2024", "imdbid": "tt123", "cat": "5040",
	} {
		if got := values.Get(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	if got := values["indexers"]; len(got) != 2 || got[0] != "7" || got[1] != "9" {
		t.Errorf("indexers = %v", got)
	}
	if _, present := values["tmdbid"]; present {
		t.Error("an empty field must not be carried into the page link")
	}
}

func TestLiveSearchPageReadsTheStoredResultsWithoutASecondFanOut(t *testing.T) {
	env := newTestEnv(t)
	insertSearchRelease(t, env.store, "Live Page Show 1080p", "5040")
	since := time.Now().Add(-time.Minute).Unix()

	target := "/releases/live?" + livePageValues(liveSearchForm{
		Mode: "search", Query: "Live Page", IndexerIDs: nil,
	}, 50, since).Encode()
	recorder := get(t, env.handler, target+"&page=1")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "Live Page Show 1080p") {
		t.Error("the stored row after live_since is missing")
	}
	if !strings.Contains(body, `class="pagination"`) {
		t.Error("live results have no pagination")
	}
	if !strings.Contains(body, `action="/releases/search"`) {
		t.Error("the live page lost its search form")
	}

	expired := get(t, env.handler, "/releases/live?source=live")
	if !strings.Contains(expired.Body.String(), "no longer available") {
		t.Error("a page without live_since has no explanation")
	}
}

func TestLiveLimitSelectorIsTranslated(t *testing.T) {
	env := newTestEnv(t)
	for _, lang := range []string{"en", "it", "de", "fr", "es", "pl", "zh"} {
		body := get(t, env.handler, "/releases?lang="+lang).Body.String()
		if !strings.Contains(body, `name="live_limit"`) {
			t.Errorf("%s: the limit selector is missing", lang)
		}
		for _, value := range []string{"50", "100", "200"} {
			if !strings.Contains(body, `<option value="`+value+`"`) {
				t.Errorf("%s: option %s is missing", lang, value)
			}
		}
		if strings.Contains(body, "releases.liveLimit") || strings.Contains(body, "releases.livePageExpired") {
			t.Errorf("%s: unresolved live page key", lang)
		}
	}
}
