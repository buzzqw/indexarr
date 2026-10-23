package web

import (
	"context"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/buzzqw/indexarr/internal/store"
)

// TestSourcePagesRedactSecrets covers WP-R point 4: the legacy list and edit
// pages must never print the raw source URL, its query string or the stored
// credentials. Editing with empty fields keeps the current values because the
// form does not render them.
func TestSourcePagesRedactSecrets(t *testing.T) {
	env := newTestEnv(t)
	const (
		rssKey   = "rsskey0123456789abcdef0123456789abcdef"
		apiKey   = "SYNTHETIC-API-KEY-123"
		password = "synthetic-password"
	)
	rawURL := "https://user:pass@tracker.example/rss/" + rssKey + "/feed?rsskey=" + rssKey

	ctx := context.Background()
	id, err := env.store.CreateSource(ctx, store.Source{
		Name:     "Secret feed",
		Kind:     "rss",
		Manager:  "auto",
		URL:      rawURL,
		APIKey:   apiKey,
		Username: "feeduser",
		Password: password,
		Enabled:  true,
	})
	if err != nil {
		t.Fatalf("CreateSource: %v", err)
	}

	list := get(t, env.handler, "/indexers?tab=legacy")
	if list.Code != 200 {
		t.Fatalf("GET legacy list = %d: %s", list.Code, list.Body.String())
	}
	listBody := list.Body.String()
	for _, secret := range []string{rssKey, "rsskey=", "user:pass", apiKey, password} {
		if strings.Contains(listBody, secret) {
			t.Errorf("source list leaks %q", secret)
		}
	}
	if !strings.Contains(listBody, "tracker.example") || !strings.Contains(listBody, "***") {
		t.Errorf("source list does not show the redacted URL: %s", listBody)
	}

	editPath := "/sources/" + strconv.FormatInt(id, 10) + "/edit"
	edit := get(t, env.handler, editPath)
	if edit.Code != 200 {
		t.Fatalf("GET edit = %d: %s", edit.Code, edit.Body.String())
	}
	editBody := edit.Body.String()
	for _, secret := range []string{rssKey, "rsskey=", "user:pass", apiKey, password} {
		if strings.Contains(editBody, secret) {
			t.Errorf("source form leaks %q", secret)
		}
	}
	if strings.Contains(editBody, `name="url" type="url" required`) {
		t.Error("edit form marks URL as required although an empty value keeps the current one")
	}

	token, cookie := csrfFrom(t, env.handler, editPath)
	updated := postForm(t, env.handler, "/sources/"+strconv.FormatInt(id, 10), url.Values{
		"csrf":             {token},
		"name":             {"Renamed feed"},
		"kind":             {"rss"},
		"manager":          {"auto"},
		"url":              {""},
		"api_key":          {""},
		"username":         {"feeduser"},
		"password":         {""},
		"categories":       {""},
		"default_category": {""},
		"query":            {""},
		"interval_seconds": {"0"},
		"enabled":          {"on"},
	}, cookie)
	if updated.Code != 303 {
		t.Fatalf("POST edit = %d: %s", updated.Code, updated.Body.String())
	}

	got, err := env.store.GetSource(ctx, id)
	if err != nil {
		t.Fatalf("GetSource: %v", err)
	}
	if got.Name != "Renamed feed" {
		t.Errorf("name = %q", got.Name)
	}
	if got.URL != rawURL || got.APIKey != apiKey || got.Password != password {
		t.Errorf("empty fields did not keep the stored secrets: %#v", got)
	}
}
