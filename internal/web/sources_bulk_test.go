package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/buzzqw/indexarr/internal/store"
)

func strconvI(value int64) string { return strconv.FormatInt(value, 10) }

func createBulkTestSources(t *testing.T, env *testEnv) []int64 {
	t.Helper()
	ids := make([]int64, 0, 2)
	for _, name := range []string{"Bulk A", "Bulk B"} {
		id, err := env.store.CreateSource(context.Background(), store.Source{
			Name: name, Kind: "torznab", Manager: "auto",
			URL:    "http://" + strings.ToLower(strings.ReplaceAll(name, " ", "-")) + ".example/api",
			APIKey: "key", Enabled: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	return ids
}

func TestSourcesBulkActions(t *testing.T) {
	env := newTestEnv(t)
	ids := createBulkTestSources(t, env)
	token, cookie := csrfFrom(t, env.handler, "/sources")

	post := func(values url.Values) *httptest.ResponseRecorder {
		values.Set("csrf", token)
		return postForm(t, env.handler, "/sources/bulk-update", values, cookie)
	}
	idsValues := []string{strconvI(ids[0]), strconvI(ids[1])}

	if recorder := post(url.Values{"ids": idsValues, "action": {"disable"}}); recorder.Code != http.StatusSeeOther {
		t.Fatalf("disable status = %d: %s", recorder.Code, recorder.Body.String())
	}
	for _, id := range ids {
		source, _ := env.store.GetSource(context.Background(), id)
		if source.Enabled {
			t.Errorf("source %d still enabled", id)
		}
	}

	post(url.Values{"ids": idsValues, "action": {"interval"}, "interval_seconds": {"900"}})
	for _, id := range ids {
		source, _ := env.store.GetSource(context.Background(), id)
		if source.IntervalSeconds != 900 {
			t.Errorf("source %d interval = %d", id, source.IntervalSeconds)
		}
	}

	post(url.Values{"ids": idsValues, "action": {"categories"}, "categories": {"5000, 2000"}})
	for _, id := range ids {
		source, _ := env.store.GetSource(context.Background(), id)
		if source.Categories != "5000,2000" {
			t.Errorf("source %d categories = %q", id, source.Categories)
		}
	}

	post(url.Values{"ids": idsValues, "action": {"enable"}})
	for _, id := range ids {
		source, _ := env.store.GetSource(context.Background(), id)
		if !source.Enabled {
			t.Errorf("source %d still disabled", id)
		}
	}
}

func TestSourcesExportImportAndClone(t *testing.T) {
	env := newTestEnv(t)
	ids := createBulkTestSources(t, env)
	token, cookie := csrfFrom(t, env.handler, "/sources")

	export := get(t, env.handler, "/sources/export")
	if export.Code != http.StatusOK {
		t.Fatalf("export status = %d", export.Code)
	}
	var payload sourcesJSON
	if err := json.Unmarshal(export.Body.Bytes(), &payload); err != nil {
		t.Fatalf("export JSON: %v", err)
	}
	if payload.Version != 1 || len(payload.Sources) != 2 {
		t.Fatalf("export payload = %#v", payload)
	}

	imported := sourcesJSON{Version: 1, Sources: []sourceJSON{
		{Name: "Imported", Kind: "rss", URL: "https://imported.example/feed.xml", Enabled: true},
		{Name: "Bulk A", Kind: "rss", URL: "https://duplicate.example/feed.xml"},
	}}
	body, _ := json.Marshal(imported)
	recorder := postForm(t, env.handler, "/sources/import", url.Values{
		"csrf": {token},
		"json": {string(body)},
	}, cookie)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("import status = %d: %s", recorder.Code, recorder.Body.String())
	}
	sources, _ := env.store.ListSources(context.Background())
	names := map[string]bool{}
	for _, source := range sources {
		names[source.Name] = true
	}
	if !names["Imported"] || len(sources) != 3 {
		t.Errorf("sources after import = %d, names = %#v", len(sources), names)
	}

	clone := postForm(t, env.handler, "/sources/"+strconvI(ids[0])+"/clone", url.Values{"csrf": {token}}, cookie)
	if clone.Code != http.StatusSeeOther {
		t.Fatalf("clone status = %d: %s", clone.Code, clone.Body.String())
	}
	sources, _ = env.store.ListSources(context.Background())
	found := false
	for _, source := range sources {
		if source.Name == "Bulk A copy" {
			found = true
			if source.Enabled {
				t.Error("a cloned source must start disabled")
			}
		}
	}
	if !found {
		t.Errorf("clone not created: %#v", names)
	}

	delete := postForm(t, env.handler, "/sources/bulk-delete", url.Values{
		"csrf": {token},
		"ids":  {strconvI(ids[0]), strconvI(ids[1])},
	}, cookie)
	if delete.Code != http.StatusSeeOther {
		t.Fatalf("bulk delete status = %d", delete.Code)
	}
	sources, _ = env.store.ListSources(context.Background())
	if len(sources) != 2 { // imported + cloned
		t.Errorf("sources after delete = %d", len(sources))
	}
}
