package web

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/buzzqw/indexarr/internal/catalog"
	"github.com/buzzqw/indexarr/internal/i18n"
)

func syntheticCatalog(n int) []catalog.IndexEntry {
	out := make([]catalog.IndexEntry, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, catalog.IndexEntry{
			ID:          fmt.Sprintf("site-%04d", i),
			Name:        fmt.Sprintf("Site %04d", i+1),
			Description: "Synthetic site for the catalog performance test.",
			Language:    "en-US",
			Type:        "public",
			Protocol:    "torrent",
			Categories:  []string{"movies", "tv"},
			State:       "verificata",
			Hosts:       []string{fmt.Sprintf("site-%04d.example", i)},
		})
	}
	return out
}

// TestCatalogIndexPageHandlesThousandEntries replaces the index with 1000
// synthetic rows. The page must stay under 50 ms and must not fall back to
// catalog.Builtins(): seeing ExtTo in the body would prove it parsed the
// embedded definitions.
func TestCatalogIndexPageHandlesThousandEntries(t *testing.T) {
	env := newNativeEnv(t)
	entries := syntheticCatalog(1000)
	env.server.catalogIndex = func() ([]catalog.IndexEntry, error) { return entries, nil }

	// Warm the template cache so the measurement covers the handler, the
	// index lookup, the filters and the render, not one-off compilation.
	if recorder := get(t, env.handler, "/indexers/new?all=1"); recorder.Code != http.StatusOK {
		t.Fatalf("warm-up = %d: %s", recorder.Code, recorder.Body.String())
	}

	start := time.Now()
	recorder := get(t, env.handler, "/indexers/new?all=1")
	elapsed := time.Since(start)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	if elapsed > catalogIndexBudget {
		t.Errorf("GET /indexers/new with 1000 entries took %s, want < %s", elapsed, catalogIndexBudget)
	}
	t.Logf("GET /indexers/new with 1000 entries: %s", elapsed)
	body := recorder.Body.String()
	if strings.Contains(body, "ExtTo") {
		t.Error("the page parsed the real catalog instead of using the injected index")
	}
	if !strings.Contains(body, "Site 0001") {
		t.Error("the first synthetic site is missing")
	}
	if got := strings.Count(body, `class="site-row catalog-row"`); got != 50 {
		t.Errorf("rows on page 1 = %d, want 50", got)
	}
	if !strings.Contains(body, "1000 sites supported") {
		t.Error("the counter does not read the index")
	}
}

func TestCatalogVisibilitySplitsUsersAndQueue(t *testing.T) {
	env := newNativeEnv(t)
	env.server.catalogIndex = func() ([]catalog.IndexEntry, error) {
		return []catalog.IndexEntry{
			{ID: "ready", Name: "ReadyNative", Source: "native", Type: "public", Protocol: "torrent", State: "verificata", Hosts: []string{"ready.example"}},
			{ID: "draft", Name: "DraftNative", Source: "native", Type: "public", Protocol: "torrent", State: "bozza", Hosts: []string{"draft.example"}},
			{ID: "family", Name: "FamilyPending", Source: "jackett", Type: "private", Protocol: "torrent", State: "da-provvi", Family: "unit3d", Hosts: []string{"family.example"}},
			{ID: "blocked", Name: "BlockedSource", Source: "jackett", Type: "public", Protocol: "torrent", State: "bloccata", Hosts: []string{"blocked.example"}},
			{ID: "todo", Name: "WriteSource", Source: "jackett", Type: "private", Protocol: "torrent", State: "da-provvi", Hosts: []string{"todo.example"}},
		}, nil
	}

	body := get(t, env.handler, "/indexers/new?all=1").Body.String()
	if !strings.Contains(body, "ReadyNative") {
		t.Error("the verified source is missing from the user catalogue")
	}
	for _, hidden := range []string{"DraftNative", "FamilyPending", "BlockedSource", "WriteSource"} {
		if strings.Contains(body, hidden) {
			t.Errorf("unfinished source %q is visible in the user catalogue", hidden)
		}
	}
	if strings.Contains(body, "To write") {
		t.Error("the to-write badge leaked into the user catalogue")
	}

	body = get(t, env.handler, "/indexers/new?all=1&queue=1").Body.String()
	for _, want := range []string{"ReadyNative", "DraftNative", "FamilyPending", "BlockedSource", "WriteSource"} {
		if !strings.Contains(body, want) {
			t.Errorf("queue view is missing %q", want)
		}
	}
	if !strings.Contains(body, "To write") {
		t.Error("the queue view does not mark the source that still needs a definition")
	}

	body = get(t, env.handler, "/indexers/new?all=1&queue=1&state=blocked").Body.String()
	if !strings.Contains(body, "BlockedSource") {
		t.Error("the blocked filter is missing the blocked source")
	}
	for _, hidden := range []string{"ReadyNative", "DraftNative", "FamilyPending", "WriteSource"} {
		if strings.Contains(body, hidden) {
			t.Errorf("the blocked filter returned %q", hidden)
		}
	}
}

func TestCatalogFiltersCombine(t *testing.T) {
	env := newNativeEnv(t)

	cases := []struct {
		name string
		path string
		want []string
		not  []string
	}{
		{
			name: "public verified English movies",
			path: "/indexers/new?type=public&state=verified&proto=torrent&cat=movies&language=en-US",
			want: []string{"ExtTo", "The Pirate Bay"},
			not:  []string{"TorrentLeech", "Corsaro (ilCorSaRoBlu)"},
		},
		{
			name: "semi-private Italian",
			path: "/indexers/new?type=semi-private&cat=movies&language=it-IT",
			want: []string{"No site matches"},
			not:  []string{"Corsaro (account)", "ExtTo", "TorrentLeech"},
		},
		{
			name: "search by name",
			path: "/indexers/new?q=pirate",
			want: []string{"The Pirate Bay"},
			not:  []string{"ExtTo", "LimeTorrents"},
		},
		{
			name: "usenet in the site list is empty",
			path: "/indexers/new?proto=usenet",
			want: []string{"No site matches"},
			not:  []string{"ExtTo", "The Pirate Bay"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := get(t, env.handler, tc.path).Body.String()
			for _, want := range tc.want {
				if !strings.Contains(body, want) {
					t.Errorf("%s missing %q", tc.path, want)
				}
			}
			for _, unwanted := range tc.not {
				if strings.Contains(body, unwanted) {
					t.Errorf("%s still contains %q", tc.path, unwanted)
				}
			}
		})
	}
}

func TestCatalogPagination(t *testing.T) {
	env := newNativeEnv(t)
	entries := syntheticCatalog(1000)
	env.server.catalogIndex = func() ([]catalog.IndexEntry, error) { return entries, nil }

	body := get(t, env.handler, "/indexers/new?all=1&page=2").Body.String()
	if strings.Contains(body, "Site 0001") {
		t.Error("page 2 still shows the first-row site")
	}
	if !strings.Contains(body, "Site 0051") {
		t.Error("page 2 does not start at row 51")
	}
	if !strings.Contains(body, "Page 2 of 20") {
		t.Error("the pager does not report page 2 of 20")
	}
	if !strings.Contains(body, `page=3`) {
		t.Error("the next-page link is missing")
	}
}

func TestCatalogBulkAddIsIdempotentAndSkipsRequiredSettings(t *testing.T) {
	env := newNativeEnv(t)
	token, cookie := csrfFrom(t, env.handler, "/indexers/new")
	values := url.Values{
		"csrf":          {token},
		"definition_id": {"thepiratebay", "torrentscsv", "torrentgalaxy", "extto"},
	}

	first := postForm(t, env.handler, "/indexers/catalog/add", values, cookie)
	if first.Code != http.StatusSeeOther {
		t.Fatalf("bulk add = %d: %s", first.Code, first.Body.String())
	}
	afterFirst, err := env.store.ListIndexers(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(afterFirst) != 2 {
		t.Fatalf("indexers after first add = %d, want 2", len(afterFirst))
	}
	added := map[string]bool{}
	for _, in := range afterFirst {
		added[in.DefinitionID] = true
	}
	if !added["thepiratebay"] || !added["torrentscsv"] {
		t.Errorf("quick-add sites = %v", added)
	}
	if added["torrentgalaxy"] || added["extto"] {
		t.Errorf("a site that requires settings was added in bulk: %v", added)
	}

	second := postForm(t, env.handler, "/indexers/catalog/add", values, cookie)
	if second.Code != http.StatusSeeOther {
		t.Fatalf("second bulk add = %d: %s", second.Code, second.Body.String())
	}
	afterSecond, err := env.store.ListIndexers(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(afterSecond) != 2 {
		t.Errorf("indexers after second add = %d, want still 2", len(afterSecond))
	}

	for _, in := range afterSecond {
		// The catalog is paged, so query the row by name before checking its
		// "Already added" badge and detail link.
		body := get(t, env.handler, "/indexers/new?q="+url.QueryEscape(in.Name)).Body.String()
		if !strings.Contains(body, "Already added") {
			t.Errorf("the page does not mark %q as configured", in.Name)
		}
		if !strings.Contains(body, fmt.Sprintf(`href="/indexers/%d"`, in.ID)) {
			t.Errorf("no link to the detail of indexer %d", in.ID)
		}
	}
}

func TestCatalogSingleAddShowsNextStep(t *testing.T) {
	env := newNativeEnv(t)
	token, cookie := csrfFrom(t, env.handler, "/indexers/new")
	recorder := postForm(t, env.handler, "/indexers/catalog/add", url.Values{
		"csrf":          {token},
		"definition_id": {"thepiratebay"},
	}, cookie)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("add = %d: %s", recorder.Code, recorder.Body.String())
	}
	location := recorder.Header().Get("Location")
	if !strings.HasPrefix(location, "/indexers/") {
		t.Fatalf("Location = %q, want the detail page", location)
	}
	detail := get(t, env.handler, location).Body.String()
	if !strings.Contains(detail, "The Pirate Bay added.") {
		t.Error("the detail page lost the added message")
	}
	if !strings.Contains(detail, "Next step: your apps don") {
		t.Error("the detail page does not show the next step")
	}
}

func TestCatalogAlreadyAddedMatchesBridgeHost(t *testing.T) {
	env := newNativeEnv(t)
	seedDefinition(t, env.store, "torznab-generic", "torznab", phase2Definition("torznab-generic", "torznab", "private", []string{"https://bridge.example/"}, "", ""))
	id := seedPhase2Indexer(t, env.store, "torznab-generic", "extto-bridge", true, `{"url":"https://extto.org/api"}`)

	body := get(t, env.handler, "/indexers/new?q=ExtTo").Body.String()
	if !strings.Contains(body, "Already added") {
		t.Error("the bridge host did not mark the catalog site as added")
	}
	if !strings.Contains(body, fmt.Sprintf(`href="/indexers/%d"`, id)) {
		t.Errorf("no link to the bridge indexer %d", id)
	}
}

func TestCatalogIndexAndFiltersAreDeterministic(t *testing.T) {
	env := newNativeEnv(t)
	first := get(t, env.handler, "/indexers/new?type=public").Body.String()
	second := get(t, env.handler, "/indexers/new?type=public").Body.String()
	csrf := regexp.MustCompile(`name="csrf" value="[0-9a-f]{64}"`)
	if csrf.ReplaceAllString(first, "") != csrf.ReplaceAllString(second, "") {
		t.Error("the catalog page is not deterministic")
	}

	index, err := catalog.Index()
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(index))
	for _, entry := range index {
		ids = append(ids, entry.ID)
	}
	sorted := append([]string(nil), ids...)
	sort.Strings(sorted)
	if !reflect.DeepEqual(ids, sorted) {
		t.Errorf("catalog index is not sorted: %v", ids)
	}
}

// TestSitePickerTemplateUsesCatalog checks the new list markup: every {{T}}
// key must exist and no visible prose may stay outside the catalogs.
func TestSitePickerTemplateUsesCatalog(t *testing.T) {
	data, err := os.ReadFile("templates/components.html")
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	start := strings.Index(content, `{{define "site-picker"}}`)
	end := strings.Index(content, `{{define "pager"}}`)
	if start < 0 || end < start {
		t.Fatal("site-picker block not found")
	}
	block := content[start:end]

	known := make(map[string]bool, len(i18n.Keys()))
	for _, key := range i18n.Keys() {
		known[key] = true
	}
	for _, match := range templateKeyAction.FindAllStringSubmatch(block, -1) {
		if !known[match[1]] {
			t.Errorf("site-picker uses missing catalog key %q", match[1])
		}
	}

	static := templateTextAction.ReplaceAllString(block, " ")
	allowed := map[string]bool{"indexarr": true}
	var literal []string
	for _, match := range translatableAttribute.FindAllStringSubmatch(static, -1) {
		for _, token := range templateToken.FindAllString(match[1], -1) {
			if !allowed[token] {
				literal = append(literal, token)
			}
		}
	}
	text := strings.NewReplacer("<", "\n<", ">", ">\n").Replace(static)
	text = stripTags(text)
	for _, token := range templateToken.FindAllString(text, -1) {
		if !allowed[token] {
			literal = append(literal, token)
		}
	}
	if len(literal) > 0 {
		sort.Strings(literal)
		t.Errorf("site-picker has user-visible text outside the catalog: %v", literal)
	}
}

func stripTags(raw string) string {
	var out strings.Builder
	inside := false
	for _, r := range raw {
		switch r {
		case '<':
			inside = true
		case '>':
			inside = false
		default:
			if !inside {
				out.WriteRune(r)
			}
		}
	}
	return out.String()
}
