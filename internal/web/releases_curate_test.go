package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/buzzqw/indexarr/internal/store"
)

func curateIDs(ids ...int64) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, strconv.FormatInt(id, 10))
	}
	return out
}

func TestBulkCurationSetsFavoriteAndVisibility(t *testing.T) {
	env := newTestEnv(t)
	insertSearchRelease(t, env.store, "Curate One 1080p", "5040")
	insertSearchRelease(t, env.store, "Curate Two 1080p", "5040")
	insertSearchRelease(t, env.store, "Curate Three 1080p", "5040")
	releases, _, err := env.store.SearchReleases(context.Background(), store.Filter{Limit: 10})
	if err != nil || len(releases) != 3 {
		t.Fatalf("releases = %d, %v", len(releases), err)
	}
	one, two, three := releases[0].ID, releases[1].ID, releases[2].ID
	oneTitle, threeTitle := releases[0].Title, releases[2].Title
	token, cookie := csrfFrom(t, env.handler, "/releases")

	post := func(op string, ids ...int64) *httptest.ResponseRecorder {
		t.Helper()
		values := url.Values{"csrf": {token}, "op": {op}}
		for _, id := range curateIDs(ids...) {
			values.Add("ids", id)
		}
		recorder := postForm(t, env.handler, "/releases/bulk", values, cookie)
		if recorder.Code != http.StatusSeeOther {
			t.Fatalf("%s status = %d: %s", op, recorder.Code, recorder.Body.String())
		}
		return recorder
	}
	flags := func(id int64) store.Release {
		t.Helper()
		release, err := env.store.GetRelease(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		return release
	}

	if got := post("favorite", one, two).Header().Get("Location"); !strings.Contains(got, "msg=2+updated%2C+0+failed") {
		t.Errorf("favorite message = %q", got)
	}
	if !flags(one).Favorite || !flags(two).Favorite || flags(three).Favorite {
		t.Error("favorite did not mark only the selected releases")
	}

	post("unfavorite", one)
	if flags(one).Favorite || !flags(two).Favorite {
		t.Error("unfavorite did not clear only its release")
	}

	post("hide", one, two)
	body := get(t, env.handler, "/releases").Body.String()
	if strings.Contains(body, oneTitle) || strings.Contains(body, releases[1].Title) {
		t.Error("hidden releases are still listed")
	}
	if !strings.Contains(body, threeTitle) {
		t.Error("the unselected release disappeared")
	}

	post("unhide", one, two)
	body = get(t, env.handler, "/releases").Body.String()
	if !strings.Contains(body, oneTitle) || !strings.Contains(body, releases[1].Title) {
		t.Error("unhide did not bring the releases back")
	}
}

func TestBulkCurationBlockAddsRulesAndHides(t *testing.T) {
	env := newTestEnv(t)
	insertSearchRelease(t, env.store, "Block One 1080p", "5040")
	insertSearchRelease(t, env.store, "Block Two 1080p", "5040")
	releases, _, err := env.store.SearchReleases(context.Background(), store.Filter{Limit: 10})
	if err != nil || len(releases) != 2 {
		t.Fatalf("releases = %d, %v", len(releases), err)
	}
	token, cookie := csrfFrom(t, env.handler, "/releases")
	values := url.Values{"csrf": {token}, "op": {"block"}}
	for _, release := range releases {
		values.Add("ids", strconv.FormatInt(release.ID, 10))
	}
	recorder := postForm(t, env.handler, "/releases/bulk", values, cookie)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Location"); !strings.Contains(got, "msg=2+updated%2C+0+failed") {
		t.Errorf("block message = %q", got)
	}

	rules, err := env.store.ListBlocklist(context.Background())
	if err != nil || len(rules) != 1 {
		t.Fatalf("rules = %d, %v", len(rules), err)
	}
	if rules[0].Kind != store.BlockKindInfohash {
		t.Errorf("rule kind = %q, want infohash", rules[0].Kind)
	}
	for _, release := range releases {
		stored, err := env.store.GetRelease(context.Background(), release.ID)
		if err != nil || !stored.Hidden {
			t.Errorf("release %d not hidden: %#v %v", release.ID, stored, err)
		}
	}
}

func TestBulkCurationRequiresSelectionAndCountsFailures(t *testing.T) {
	env := newTestEnv(t)
	insertSearchRelease(t, env.store, "Partial One 1080p", "5040")
	releases, _, err := env.store.SearchReleases(context.Background(), store.Filter{Limit: 10})
	if err != nil || len(releases) != 1 {
		t.Fatalf("releases = %d, %v", len(releases), err)
	}
	token, cookie := csrfFrom(t, env.handler, "/releases")

	none := postForm(t, env.handler, "/releases/bulk",
		url.Values{"csrf": {token}, "op": {"favorite"}}, cookie)
	if got := none.Header().Get("Location"); !strings.Contains(got, "err=") {
		t.Errorf("no selection location = %q", got)
	}

	partial := postForm(t, env.handler, "/releases/bulk", url.Values{
		"csrf": {token}, "op": {"favorite"},
		"ids": {strconv.FormatInt(releases[0].ID, 10), "999999"},
	}, cookie)
	got := partial.Header().Get("Location")
	if !strings.Contains(got, "err=1+updated%2C+1+failed") {
		t.Errorf("partial location = %q", got)
	}

	invalid := postForm(t, env.handler, "/releases/bulk", url.Values{
		"csrf": {token}, "op": {"unknown"},
		"ids": {strconv.FormatInt(releases[0].ID, 10)},
	}, cookie)
	if got := invalid.Header().Get("Location"); !strings.Contains(got, "err=") {
		t.Errorf("invalid op location = %q", got)
	}
}

func TestBulkCurationKeepsTheFilteredResultsPage(t *testing.T) {
	env := newTestEnv(t)
	insertSearchRelease(t, env.store, "Return One 1080p", "5040")
	releases, _, err := env.store.SearchReleases(context.Background(), store.Filter{Limit: 10})
	if err != nil || len(releases) != 1 {
		t.Fatalf("releases = %d, %v", len(releases), err)
	}
	token, cookie := csrfFrom(t, env.handler, "/releases")
	id := strconv.FormatInt(releases[0].ID, 10)

	for _, tc := range []struct{ returnTo, want string }{
		{"/releases?favorites=1", "/releases?favorites=1&msg="},
		{"https://evil.invalid/", "/releases?msg="},
		{"//evil.invalid/", "/releases?msg="},
	} {
		recorder := postForm(t, env.handler, "/releases/bulk", url.Values{
			"csrf": {token}, "op": {"favorite"}, "ids": {id}, "return": {tc.returnTo},
		}, cookie)
		if got := recorder.Header().Get("Location"); !strings.HasPrefix(got, tc.want) {
			t.Errorf("return %q -> %q, want prefix %q", tc.returnTo, got, tc.want)
		}
	}
}

func TestBulkCurationButtonsAreTranslated(t *testing.T) {
	env := newTestEnv(t)
	insertSearchRelease(t, env.store, "Translate One 1080p", "5040")
	labels := map[string]string{
		"en": "Add to favorites",
		"it": "Aggiungi ai preferiti",
		"de": "Zu Favoriten hinzufügen",
		"fr": "Ajouter aux favoris",
		"es": "Añadir a favoritos",
		"pl": "Dodaj do ulubionych",
		"zh": "加入收藏",
	}
	for lang, label := range labels {
		body := get(t, env.handler, "/releases?lang="+lang).Body.String()
		for _, op := range []string{"favorite", "unfavorite", "hide", "unhide", "block"} {
			if !strings.Contains(body, `name="op" value="`+op+`"`) {
				t.Errorf("%s: bulk action %s has no button", lang, op)
			}
		}
		if !strings.Contains(body, label) {
			t.Errorf("%s: translated label %q is missing", lang, label)
		}
		if strings.Contains(body, "releases.bulkCurateHelp") || strings.Contains(body, "releases.bulkFavoriteTip") {
			t.Errorf("%s: unresolved curation key", lang)
		}
		if !strings.Contains(body, `<p class="help">`) {
			t.Errorf("%s: curation help paragraph is missing", lang)
		}
	}
}
