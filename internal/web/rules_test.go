package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/buzzqw/indexarr/internal/store"
)

func TestRulesPageAddAndDelete(t *testing.T) {
	env := newTestEnv(t)
	token, cookie := csrfFrom(t, env.handler, "/settings?section=rules")

	recorder := postForm(t, env.handler, "/rules", url.Values{
		"csrf":  {token},
		"kind":  {"title"},
		"value": {"cam"},
		"note":  {"bad quality"},
	}, cookie)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("add status = %d: %s", recorder.Code, recorder.Body.String())
	}
	body := get(t, env.handler, "/settings?section=rules").Body.String()
	if !strings.Contains(body, "cam") || !strings.Contains(body, "bad quality") {
		t.Error("rule not listed")
	}

	invalid := postForm(t, env.handler, "/rules", url.Values{
		"csrf":  {token},
		"kind":  {"regex"},
		"value": {"["},
	}, cookie)
	if invalid.Code != http.StatusSeeOther {
		t.Fatalf("invalid status = %d", invalid.Code)
	}
	if location := invalid.Header().Get("Location"); !strings.Contains(location, "err=") {
		t.Errorf("invalid regex accepted: %q", location)
	}

	rules, _ := env.store.ListBlocklist(context.Background())
	if len(rules) != 1 {
		t.Fatalf("rules = %d", len(rules))
	}
	deleted := postForm(t, env.handler, "/rules/"+strconvI(rules[0].ID)+"/delete",
		url.Values{"csrf": {token}}, cookie)
	if deleted.Code != http.StatusSeeOther {
		t.Fatalf("delete status = %d", deleted.Code)
	}
	rules, _ = env.store.ListBlocklist(context.Background())
	if len(rules) != 0 {
		t.Errorf("rules after delete = %d", len(rules))
	}
}

func TestReleaseCuration(t *testing.T) {
	env := newTestEnv(t)
	insertSearchRelease(t, env.store, "Curation Test 1080p", "5040")
	releases, _, err := env.store.SearchReleases(context.Background(), store.Filter{})
	if err != nil || len(releases) != 1 {
		t.Fatalf("releases = %d, %v", len(releases), err)
	}
	release := releases[0]
	token, cookie := csrfFrom(t, env.handler, "/releases/"+strconvI(release.ID))

	for _, path := range []string{"favorite", "hidden"} {
		recorder := postForm(t, env.handler, "/releases/"+strconvI(release.ID)+"/"+path,
			url.Values{"csrf": {token}}, cookie)
		if recorder.Code != http.StatusSeeOther {
			t.Fatalf("%s status = %d", path, recorder.Code)
		}
	}
	stored, _ := env.store.GetRelease(context.Background(), release.ID)
	if !stored.Favorite || !stored.Hidden {
		t.Errorf("flags = %#v", stored)
	}
	visible := get(t, env.handler, "/releases").Body.String()
	if strings.Contains(visible, "Curation Test 1080p") {
		t.Error("a hidden release is still listed")
	}
	hiddenPage := get(t, env.handler, "/releases?hidden=1").Body.String()
	if !strings.Contains(hiddenPage, "Curation Test 1080p") {
		t.Error("show hidden does not list the release")
	}
	favorites := get(t, env.handler, "/releases?favorites=1").Body.String()
	if !strings.Contains(favorites, "Curation Test 1080p") {
		t.Error("favorites filter does not list the release")
	}

	blocked := postForm(t, env.handler, "/releases/"+strconvI(release.ID)+"/block",
		url.Values{"csrf": {token}}, cookie)
	if blocked.Code != http.StatusSeeOther {
		t.Fatalf("block status = %d", blocked.Code)
	}
	rules, _ := env.store.ListBlocklist(context.Background())
	if len(rules) != 1 || rules[0].Kind != "infohash" {
		t.Errorf("block rules = %#v", rules)
	}
	stored, _ = env.store.GetRelease(context.Background(), release.ID)
	if !stored.Hidden {
		t.Error("block did not hide the release")
	}
}
