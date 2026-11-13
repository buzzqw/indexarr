package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestSavedSearchesWeb(t *testing.T) {
	env := newTestEnv(t)
	insertSearchRelease(t, env.store, "The Show 1080p", "5040")
	insertSearchRelease(t, env.store, "The Show 720p", "5030")
	token, cookie := csrfFrom(t, env.handler, "/releases")

	saved := postForm(t, env.handler, "/releases/saved", url.Values{
		"csrf":  {token},
		"name":  {"TV 1080p"},
		"query": {"q=show&quality=1080p"},
	}, cookie)
	if saved.Code != http.StatusSeeOther {
		t.Fatalf("save status = %d: %s", saved.Code, saved.Body.String())
	}
	list, err := env.store.ListSavedSearches(context.Background())
	if err != nil || len(list) != 1 {
		t.Fatalf("saved = %#v, %v", list, err)
	}
	page := get(t, env.handler, "/releases").Body.String()
	if !strings.Contains(page, "TV 1080p") || !strings.Contains(page, `id="saved-query"`) {
		t.Error("saved search controls missing")
	}

	filtered := get(t, env.handler, "/releases?q=show&quality=1080p").Body.String()
	if !strings.Contains(filtered, "The Show 1080p") || strings.Contains(filtered, "The Show 720p") {
		t.Error("quality filter not applied")
	}

	deleted := postForm(t, env.handler, "/releases/saved/"+strconvI(list[0].ID)+"/delete",
		url.Values{"csrf": {token}}, cookie)
	if deleted.Code != http.StatusSeeOther {
		t.Fatalf("delete status = %d", deleted.Code)
	}
	list, _ = env.store.ListSavedSearches(context.Background())
	if len(list) != 0 {
		t.Errorf("saved after delete = %#v", list)
	}
}

func TestAdvancedReleaseFilters(t *testing.T) {
	env := newTestEnv(t)
	insertSearchRelease(t, env.store, "The Show 1080p WEB-DL", "5040")
	insertSearchRelease(t, env.store, "The Show 2160p BluRay", "5045")
	insertSearchRelease(t, env.store, "A Movie 720p", "2030")
	insertSearchRelease(t, env.store, "An Old Show 480p", "2030")
	insertSearchRelease(t, env.store, "Four K Movie 4K HDR", "2040")

	if body := get(t, env.handler, "/releases?quality=1080p").Body.String(); !strings.Contains(body, "1080p WEB-DL") || strings.Contains(body, "2160p BluRay") {
		t.Error("resolution filter failed")
	}
	if body := get(t, env.handler, "/releases?source_quality=bluray").Body.String(); !strings.Contains(body, "2160p BluRay") || strings.Contains(body, "1080p WEB-DL") {
		t.Error("source quality filter failed")
	}
	if body := get(t, env.handler, "/releases?quality=2160p").Body.String(); !strings.Contains(body, "The Show 2160p BluRay") ||
		strings.Contains(body, "The Show 1080p WEB-DL") || strings.Contains(body, "A Movie 720p") {
		t.Error("2160p filter failed")
	}
	// An open value keeps that resolution and the higher ones: 1080p+ takes
	// 1080p, 2160p and the 4K alias, never 720p or 480p.
	body := get(t, env.handler, "/releases?quality=1080p%2B").Body.String()
	if !strings.Contains(body, "The Show 1080p WEB-DL") || !strings.Contains(body, "The Show 2160p BluRay") ||
		!strings.Contains(body, "Four K Movie 4K HDR") || strings.Contains(body, "A Movie 720p") ||
		strings.Contains(body, "An Old Show 480p") {
		t.Error("1080p+ filter failed")
	}
	// 720p+ also keeps 720p, still not 480p.
	body = get(t, env.handler, "/releases?quality=720p%2B").Body.String()
	if !strings.Contains(body, "A Movie 720p") || !strings.Contains(body, "The Show 1080p WEB-DL") ||
		!strings.Contains(body, "The Show 2160p BluRay") || strings.Contains(body, "An Old Show 480p") {
		t.Error("720p+ filter failed")
	}
	// A value that is not offered is ignored, like any other unknown choice.
	if body := get(t, env.handler, "/releases?quality=480p%2B").Body.String(); !strings.Contains(body, "An Old Show 480p") ||
		!strings.Contains(body, "The Show 2160p BluRay") {
		t.Error("an unknown quality must not filter")
	}
}
