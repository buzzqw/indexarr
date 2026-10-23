package cardigann

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/buzzqw/indexarr/internal/definition"
)

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return body
}

func convertFixture(t *testing.T, name string) Result {
	t.Helper()
	result, err := Convert(readFixture(t, name))
	if err != nil {
		t.Fatalf("Convert(%s): %v", name, err)
	}
	return result
}

func findSetting(t *testing.T, def *definition.Definition, name string) definition.Setting {
	t.Helper()
	for _, setting := range def.Settings {
		if setting.Name == name {
			return setting
		}
	}
	t.Fatalf("setting %q not found in %v", name, def.Settings)
	return definition.Setting{}
}

func hasString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func containsSubstring(values []string, want string) bool {
	for _, value := range values {
		if strings.Contains(value, want) {
			return true
		}
	}
	return false
}

func TestConvertPrivateHTML(t *testing.T) {
	result := convertFixture(t, "private-html.yml")
	if result.SourceID != "synthetic-private" {
		t.Errorf("SourceID = %q", result.SourceID)
	}
	if result.Name != "Synthetic Private Tracker" {
		t.Errorf("Name = %q", result.Name)
	}
	if result.Hash != definition.Fingerprint(result.Body) {
		t.Errorf("Hash does not match Body fingerprint")
	}

	def := result.Definition
	if def == nil {
		t.Fatal("nil definition")
	}
	if def.Meta.ID != "synthetic-private" {
		t.Errorf("meta.id = %q", def.Meta.ID)
	}
	if def.Meta.Type != "private" {
		t.Errorf("meta.type = %q", def.Meta.Type)
	}
	if def.Meta.Protocol != "torrent" {
		t.Errorf("meta.protocol = %q", def.Meta.Protocol)
	}
	if def.Meta.Author != "cardigann-import" {
		t.Errorf("meta.author = %q", def.Meta.Author)
	}
	if len(def.Meta.URLs) != 2 {
		t.Fatalf("meta.urls = %v, want 2 deduped URLs", def.Meta.URLs)
	}
	if def.Meta.URLs[0] != "https://private.example/" || def.Meta.URLs[1] != "http://private-mirror.example/" {
		t.Errorf("meta.urls = %v", def.Meta.URLs)
	}

	username := findSetting(t, def, "username")
	if username.Type != "text" || !username.Required {
		t.Errorf("username setting = %+v", username)
	}
	password := findSetting(t, def, "password")
	if password.Type != "password" || !password.Required {
		t.Errorf("password setting = %+v", password)
	}
	sortSetting := findSetting(t, def, "sort")
	if sortSetting.Type != "select" || sortSetting.Default != "seeders" {
		t.Errorf("sort setting = %+v", sortSetting)
	}
	if !hasString(sortSetting.Options, "date") || !hasString(sortSetting.Options, "seeders") {
		t.Errorf("sort options = %v", sortSetting.Options)
	}
	cookieSetting := findSetting(t, def, "cookie-session")
	if cookieSetting.Type != "password" {
		t.Errorf("cookie_session setting = %+v", cookieSetting)
	}

	if len(def.Categories) != 2 {
		t.Fatalf("categories = %+v", def.Categories)
	}
	category := def.Categories[0]
	if category.Site != "1" || category.Standard != 5040 || category.Desc != "TV HD" {
		t.Errorf("category[0] = %+v", category)
	}
	if def.Categories[1].Site != "2" || def.Categories[1].Standard != 2040 {
		t.Errorf("category[1] = %+v", def.Categories[1])
	}
	if !hasString(def.Capabilities.Search, "q") {
		t.Errorf("capabilities.search = %v", def.Capabilities.Search)
	}
	if !hasString(def.Capabilities.TVSearch, "imdbid") {
		t.Errorf("capabilities.tv-search = %v", def.Capabilities.TVSearch)
	}

	if def.Auth == nil || def.Auth.Strategy != "form" {
		t.Fatalf("auth = %+v", def.Auth)
	}
	if len(def.Auth.Steps) != 3 {
		t.Fatalf("auth steps = %d, want 3", len(def.Auth.Steps))
	}
	getStep := def.Auth.Steps[0]
	if getStep.Get != "login.php" {
		t.Errorf("step[0].get = %q", getStep.Get)
	}
	csrf, ok := getStep.Extract["csrf_token"]
	if !ok || csrf.Selector != `input[name="csrf_token"]` || csrf.Attr != "value" {
		t.Errorf("csrf extractor = %+v", csrf)
	}
	post := def.Auth.Steps[1]
	if post.Post != "login.php" {
		t.Errorf("step[1].post = %q", post.Post)
	}
	if post.Form["username"] != "{{ settings.username }}" {
		t.Errorf("form username = %q", post.Form["username"])
	}
	if post.Form["password"] != "{{ settings.password }}" {
		t.Errorf("form password = %q", post.Form["password"])
	}
	if post.Form["csrf_token"] != "{{ csrf_token }}" {
		t.Errorf("form csrf_token = %q", post.Form["csrf_token"])
	}
	if post.Form["action"] != "login" {
		t.Errorf("form action = %q", post.Form["action"])
	}
	if !strings.Contains(post.Headers["Cookie"], "session={{ settings.cookie-session }}") {
		t.Errorf("cookie header = %q", post.Headers["Cookie"])
	}
	if def.Auth.Steps[2].Get != "index.php" {
		t.Errorf("step[2].get = %q", def.Auth.Steps[2].Get)
	}
	if def.Auth.Success == nil || def.Auth.Success.Selector != "a.logout" {
		t.Errorf("auth success = %+v", def.Auth.Success)
	}
	if len(def.Auth.Failure) != 1 || def.Auth.Failure[0].Selector != "div.error" {
		t.Errorf("auth failure = %+v", def.Auth.Failure)
	}

	if def.Search.Request.Get != "torrents.php" {
		t.Errorf("search.request.get = %q", def.Search.Request.Get)
	}
	wantParams := map[string]string{
		"q":     "{{ query.keywords }}",
		"cat":   "{{ categories }}",
		"page":  "{{ page }}",
		"order": "{{ settings.sort }}",
	}
	for key, want := range wantParams {
		if got := def.Search.Request.Params[key]; got != want {
			t.Errorf("param %s = %q, want %q", key, got, want)
		}
	}
	response := def.Search.Response
	if response.Type != "html" {
		t.Errorf("response.type = %q", response.Type)
	}
	if len(response.Rows.Selector) != 1 || response.Rows.Selector[0] != "table.torrents > tbody > tr" {
		t.Errorf("response.rows = %+v", response.Rows)
	}
	title := response.Fields["title"]
	if len(title.Selector) != 2 || len(title.Filters) != 1 || title.Filters[0].Name != "trim" {
		t.Errorf("title field = %+v", title)
	}
	categoryField := response.Fields["category"]
	if categoryField.Type != "category" || len(categoryField.Filters) != 1 ||
		categoryField.Filters[0].Name != "query_param" || categoryField.Filters[0].Arg != "cat" {
		t.Errorf("category field = %+v", categoryField)
	}
	dateField := response.Fields["date"]
	if dateField.Type != "date" || len(dateField.Layouts) != 1 || dateField.Layouts[0] != "02-01-2006 15:04" {
		t.Errorf("date field = %+v", dateField)
	}
	if response.Fields["download"].Type != "url" || response.Fields["download"].Attr != "href" {
		t.Errorf("download field = %+v", response.Fields["download"])
	}
	if len(response.Required) != 2 || response.Required[0] != "title" || response.Required[1] != "download" {
		t.Errorf("required = %v", response.Required)
	}
	if def.Health == nil || def.Health.Query != "test" || def.Health.MinRows != 1 {
		t.Errorf("health = %+v", def.Health)
	}
	if len(def.Tests) != 0 {
		t.Errorf("tests = %+v, want empty for a bozza definition", def.Tests)
	}
	if len(result.Unsupported) != 0 {
		t.Errorf("unsupported = %v, want none", result.Unsupported)
	}
}

func TestConvertPublicHTML(t *testing.T) {
	result := convertFixture(t, "public-html.yml")
	def := result.Definition
	if def.Meta.Type != "public" {
		t.Errorf("meta.type = %q", def.Meta.Type)
	}
	if def.Meta.Protocol != "torrent" {
		t.Errorf("meta.protocol = %q", def.Meta.Protocol)
	}
	if def.Auth != nil {
		t.Errorf("public tracker has auth: %+v", def.Auth)
	}
	if len(def.Meta.URLs) != 1 || def.Meta.URLs[0] != "https://public.example/" {
		t.Errorf("meta.urls = %v", def.Meta.URLs)
	}
	if len(def.Categories) != 2 {
		t.Fatalf("categories = %+v", def.Categories)
	}
	if def.Categories[0].Site != "Movies/HD" || def.Categories[0].Standard != 2040 ||
		def.Categories[0].Desc != "Movies/HD" {
		t.Errorf("category[0] = %+v", def.Categories[0])
	}
	if def.Categories[1].Site != "TV/HD" || def.Categories[1].Standard != 5040 {
		t.Errorf("category[1] = %+v", def.Categories[1])
	}
	if !hasString(def.Capabilities.Search, "q") {
		t.Errorf("capabilities.search = %v", def.Capabilities.Search)
	}
	if len(def.Capabilities.TVSearch) != 0 {
		t.Errorf("capabilities.tv-search = %v, want empty", def.Capabilities.TVSearch)
	}
	if _, ok := def.Search.Response.Fields["download"]; !ok {
		t.Errorf("link field was not mapped to download: %+v", def.Search.Response.Fields)
	}
	if _, ok := def.Search.Response.Fields["link"]; ok {
		t.Errorf("link field still present")
	}
	if def.Search.Response.Fields["download"].Attr != "href" {
		t.Errorf("download attr = %q", def.Search.Response.Fields["download"].Attr)
	}
	if def.Search.Response.Fields["download"].Type != "url" {
		t.Errorf("download type = %q, want url", def.Search.Response.Fields["download"].Type)
	}
	if strings.Contains(string(result.Body), "\nauth:") {
		t.Errorf("public body contains an auth block")
	}
}

func TestConvertJSONAPI(t *testing.T) {
	result := convertFixture(t, "json-api.yml")
	def := result.Definition
	if def.Meta.Protocol != "usenet" {
		t.Errorf("meta.protocol = %q, want usenet", def.Meta.Protocol)
	}
	if def.Search.Response.Type != "json" {
		t.Fatalf("response.type = %q", def.Search.Response.Type)
	}
	if def.Search.Response.Rows.Path != "data.results" {
		t.Errorf("rows path = %q", def.Search.Response.Rows.Path)
	}
	response := def.Search.Response
	if response.Fields["title"].Path != "name" {
		t.Errorf("title path = %q", response.Fields["title"].Path)
	}
	if response.Fields["download"].Path != "download.url" {
		t.Errorf("download path = %q", response.Fields["download"].Path)
	}
	if response.Fields["date"].Type != "date" || len(response.Fields["date"].Layouts) != 1 {
		t.Errorf("date field = %+v", response.Fields["date"])
	}
	if _, ok := response.Fields["imdbid"]; !ok {
		t.Errorf("imdb field was not mapped to imdbid: %+v", response.Fields)
	}
	if !containsSubstring(result.Warnings, "JSON rows selector") {
		t.Errorf("warnings = %v", result.Warnings)
	}
	if !containsSubstring(result.Warnings, "selector interpreted as a JSON path") {
		t.Errorf("warnings = %v", result.Warnings)
	}
	if !containsSubstring(result.Warnings, "attribute") {
		t.Errorf("warnings = %v", result.Warnings)
	}
	if !hasString(result.Unsupported, "search.count") {
		t.Errorf("unsupported = %v, want search.count", result.Unsupported)
	}
	if !containsSubstring(result.Warnings, "caps.modes is missing") {
		t.Errorf("warnings = %v", result.Warnings)
	}
}

func TestFiltersAndTemplates(t *testing.T) {
	result := convertFixture(t, "filters.yml")
	def := result.Definition

	if def.Search.Request.Post != "search" {
		t.Errorf("request.post = %q", def.Search.Request.Post)
	}
	if def.Search.Request.Get != "" {
		t.Errorf("request.get = %q", def.Search.Request.Get)
	}
	if len(def.Search.Request.Params) != 0 {
		t.Errorf("params = %+v, want POST form instead", def.Search.Request.Params)
	}
	form := def.Search.Request.Form
	if form["q"] != "{{ query.keywords }}" || form["cat"] != "{{ categories }}" ||
		form["page"] != "{{ page }}" || form["offset"] != "{{ offset }}" {
		t.Errorf("form = %+v", form)
	}
	if form["apikey"] != "{{ settings.apikey }}" {
		t.Errorf("apikey form value = %q", form["apikey"])
	}
	apikey := findSetting(t, def, "apikey")
	if apikey.Type != "password" {
		t.Errorf("apikey setting = %+v", apikey)
	}

	titleFilters := def.Search.Response.Fields["title"].Filters
	want := []struct {
		name string
		arg  string
	}{
		{"trim", ""},
		{"lower", ""},
		{"replace", "foo:bar"},
		{"suffix", " (x)"},
		{"prefix", "[x] "},
		{"regex", `^\[(.*)\]`},
	}
	if len(titleFilters) != len(want) {
		t.Fatalf("title filters = %+v", titleFilters)
	}
	for i, expected := range want {
		if titleFilters[i].Name != expected.name || titleFilters[i].Arg != expected.arg {
			t.Errorf("filter[%d] = %+v, want %+v", i, titleFilters[i], expected)
		}
	}
	date := def.Search.Response.Fields["date"]
	if date.Type != "date" || len(date.Layouts) != 1 || date.Layouts[0] != "02-01-2006" {
		t.Errorf("date field = %+v", date)
	}
	download := def.Search.Response.Fields["download"]
	if download.Selector[0] != "a.direct" || download.Attr != "href" || download.Type != "url" {
		t.Errorf("download field = %+v", download)
	}
	category := def.Search.Response.Fields["category"]
	if category.Type != "category" || category.Filters[0].Name != "query_param" || category.Filters[0].Arg != "cat" {
		t.Errorf("category field = %+v", category)
	}
	if !containsSubstring(result.Warnings, "download section replaced field") {
		t.Errorf("warnings = %v", result.Warnings)
	}
	if len(result.Unsupported) != 0 {
		t.Errorf("unsupported = %v, want none", result.Unsupported)
	}
}

func TestUnsupportedFeatures(t *testing.T) {
	result := convertFixture(t, "unsupported.yml")
	def := result.Definition
	if def.Auth != nil {
		t.Errorf("captcha login should not produce auth: %+v", def.Auth)
	}
	for _, feature := range []string{
		"captcha",
		"keywordsfilters",
		"prefilter",
		`filter "customfilter"`,
		`filter "split"`,
		"download multi-step",
		"response.count",
	} {
		if !hasString(result.Unsupported, feature) {
			t.Errorf("unsupported %q missing from %v", feature, result.Unsupported)
		}
	}
	if !containsSubstring(result.Warnings, "authentication not converted") {
		t.Errorf("warnings = %v", result.Warnings)
	}
	if _, err := definition.Parse(result.Body); err != nil {
		t.Errorf("converted body does not parse: %v", err)
	}
}

func TestDynamicTemplateFailsClosed(t *testing.T) {
	result, err := Convert(readFixture(t, "dynamic-template.yml"))
	if err == nil {
		t.Fatal("Convert succeeded for a dynamic .Today search template")
	}
	if result.Body != nil {
		t.Errorf("failed conversion returned a body")
	}
	if result.SourceID != "synthetic-dynamic-template" {
		t.Errorf("SourceID = %q", result.SourceID)
	}
	if !hasString(result.Unsupported, "template .Today") {
		t.Errorf("unsupported = %v, want template .Today", result.Unsupported)
	}
	if !strings.Contains(err.Error(), ".Today") {
		t.Errorf("error = %v", err)
	}
}

func TestSecretsAreNotCopied(t *testing.T) {
	result := convertFixture(t, "secrets.yml")
	body := string(result.Body)
	for _, secret := range []string{
		"synthetic-literal-password",
		"synthetic-cookie-value",
		"synthetic-default-token",
	} {
		if strings.Contains(body, secret) {
			t.Errorf("body contains literal secret %q", secret)
		}
		if containsSubstring(result.Warnings, secret) {
			t.Errorf("warnings contain literal secret %q: %v", secret, result.Warnings)
		}
		if containsSubstring(result.Unsupported, secret) {
			t.Errorf("unsupported contains literal secret %q: %v", secret, result.Unsupported)
		}
	}
	def := result.Definition
	apikey := findSetting(t, def, "apikey")
	if apikey.Default != "" {
		t.Errorf("secret default was copied: %+v", apikey)
	}
	password := findSetting(t, def, "password")
	if password.Type != "password" || !password.Required {
		t.Errorf("password setting = %+v", password)
	}
	cookie := findSetting(t, def, "cookie-session")
	if cookie.Type != "password" {
		t.Errorf("cookie setting = %+v", cookie)
	}
	if def.Auth == nil || len(def.Auth.Steps) != 1 {
		t.Fatalf("auth = %+v", def.Auth)
	}
	form := def.Auth.Steps[0].Form
	if form["password"] != "{{ settings.password }}" {
		t.Errorf("password form value = %q", form["password"])
	}
	if !strings.Contains(def.Auth.Steps[0].Headers["Cookie"], "session={{ settings.cookie-session }}") {
		t.Errorf("cookie header = %q", def.Auth.Steps[0].Headers["Cookie"])
	}
}

func TestConvertDirectory(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, body []byte) {
		t.Helper()
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, body, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write("z-private.yml", readFixture(t, "private-html.yml"))
	write("a-public.yml", readFixture(t, "public-html.yml"))
	write("m-filters.yaml", readFixture(t, "filters.yml"))
	write("bad.yml", readFixture(t, "invalid.yml"))
	write("notes.txt", []byte("not a definition"))
	write(".hidden/ignored.yml", readFixture(t, "private-html.yml"))
	write("nested/testdata/ignored.yml", readFixture(t, "private-html.yml"))

	results, errs := ConvertDirectory(context.Background(), dir, Options{Author: "synthetic-author"})
	if len(errs) != 1 {
		t.Fatalf("errs = %v, want one per-file error", errs)
	}
	if !strings.Contains(errs[0].Error(), "bad.yml") || !strings.Contains(errs[0].Error(), "YAML") {
		t.Errorf("error = %v", errs[0])
	}
	if len(results) != 3 {
		t.Fatalf("results = %d, want 3", len(results))
	}
	var order []string
	for _, result := range results {
		order = append(order, result.SourceID)
		if result.Definition.Meta.Author != "synthetic-author" {
			t.Errorf("author = %q", result.Definition.Meta.Author)
		}
		if result.Hash != definition.Fingerprint(result.Body) {
			t.Errorf("hash mismatch for %s", result.SourceID)
		}
	}
	wantOrder := []string{"synthetic-public", "synthetic-filters", "synthetic-private"}
	if strings.Join(order, ",") != strings.Join(wantOrder, ",") {
		t.Errorf("order = %v, want %v", order, wantOrder)
	}

	override, overrideErrs := ConvertDirectory(context.Background(), dir, Options{Type: "semi-private"})
	if len(overrideErrs) != 1 {
		t.Fatalf("override errs = %v", overrideErrs)
	}
	for _, result := range override {
		if result.Definition.Meta.Type != "semi-private" {
			t.Errorf("%s type = %q, want semi-private", result.SourceID, result.Definition.Meta.Type)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cancelled, cancelErrs := ConvertDirectory(ctx, dir, Options{})
	if len(cancelled) != 0 {
		t.Errorf("cancelled results = %d", len(cancelled))
	}
	if len(cancelErrs) != 1 || !errors.Is(cancelErrs[0], context.Canceled) {
		t.Errorf("cancel errs = %v", cancelErrs)
	}
}

func TestConvertFileAndDefaults(t *testing.T) {
	result, err := ConvertFile(filepath.Join("testdata", "private-html.yml"))
	if err != nil {
		t.Fatalf("ConvertFile: %v", err)
	}
	if result.Definition.Meta.Author != "cardigann-import" {
		t.Errorf("author = %q", result.Definition.Meta.Author)
	}
	if _, err := ConvertFile(filepath.Join("testdata", "does-not-exist.yml")); err == nil {
		t.Error("ConvertFile accepted a missing file")
	}
	if _, err := Convert(nil); err == nil {
		t.Error("Convert accepted an empty document")
	}
	if _, err := Convert([]byte("---\n")); err == nil {
		t.Error("Convert accepted a document without metadata")
	}
	if _, err := Convert(readFixture(t, "invalid.yml")); err == nil {
		t.Error("Convert accepted invalid YAML")
	}
}

func TestConvertedBodiesAreDeterministic(t *testing.T) {
	body := readFixture(t, "private-html.yml")
	first, err := Convert(body)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Convert(body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.Body, second.Body) {
		t.Error("body is not deterministic")
	}
	if first.Hash != second.Hash {
		t.Error("hash is not deterministic")
	}
}

func TestAllFixturesParseAndValidate(t *testing.T) {
	valid := []string{
		"private-html.yml",
		"public-html.yml",
		"json-api.yml",
		"filters.yml",
		"unsupported.yml",
		"secrets.yml",
	}
	for _, name := range valid {
		t.Run(name, func(t *testing.T) {
			result, err := Convert(readFixture(t, name))
			if err != nil {
				t.Fatalf("Convert: %v", err)
			}
			def, err := definition.Parse(result.Body)
			if err != nil {
				t.Fatalf("definition.Parse: %v", err)
			}
			if err := def.Validate(); err != nil {
				t.Fatalf("Validate: %v", err)
			}
			if result.Hash != definition.Fingerprint(result.Body) {
				t.Error("hash mismatch")
			}
			if result.Definition == nil || result.Definition.Meta.ID != def.Meta.ID {
				t.Error("Result.Definition does not match Body")
			}
			if result.Definition.Health == nil || result.Definition.Health.Query != "test" {
				t.Errorf("health = %+v", result.Definition.Health)
			}
			if len(result.Definition.Tests) != 0 {
				t.Errorf("tests = %+v", result.Definition.Tests)
			}
		})
	}

	// Expected failures are intentional and documented here.
	failures := []string{"dynamic-template.yml", "unsupported-response.yml", "invalid.yml"}
	for _, name := range failures {
		t.Run("expected-failure-"+name, func(t *testing.T) {
			if _, err := Convert(readFixture(t, name)); err == nil {
				t.Errorf("Convert(%s) unexpectedly succeeded", name)
			}
		})
	}

	responseResult, err := Convert(readFixture(t, "unsupported-response.yml"))
	if err == nil {
		t.Fatal("unsupported response type should fail")
	}
	if !hasString(responseResult.Unsupported, "response.type rss") {
		t.Errorf("unsupported = %v", responseResult.Unsupported)
	}
	if !containsSubstring(responseResult.Unsupported, "Unknown Category") {
		t.Errorf("unsupported = %v", responseResult.Unsupported)
	}
	if !containsSubstring(responseResult.Warnings, "parameter \"limit\"") {
		t.Errorf("warnings = %v", responseResult.Warnings)
	}
}

func TestTestdataIsSynthetic(t *testing.T) {
	deny := []string{
		"iptorrents", "torrentleech", "rutracker", "yggtorrent", "passthepopcorn",
		"broadcasthe", "redacted", "hdbits", "alpharatio", "pretome", "revolutiontt",
		"morethantv", "ncore", "torrentday", "scenetime", "1337x", "rarbg",
		"kickass", "demonoid", "animebytes",
	}
	hostPattern := regexp.MustCompile(`https?://([^/\s"']+)`)
	err := filepath.WalkDir("testdata", func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		switch strings.ToLower(filepath.Ext(path)) {
		case ".yml", ".yaml":
		default:
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		text := strings.ToLower(string(body))
		if !strings.Contains(text, "# synthetic fixture written for indexarr tests") {
			t.Errorf("%s is missing the synthetic-fixture marker", path)
		}
		if !strings.Contains(text, "synthetic") {
			t.Errorf("%s does not use synthetic names", path)
		}
		for _, name := range deny {
			if strings.Contains(text, name) {
				t.Errorf("%s looks copied from a real definition (%s)", path, name)
			}
		}
		for _, match := range hostPattern.FindAllStringSubmatch(string(body), -1) {
			host := strings.ToLower(match[1])
			if !strings.HasSuffix(host, ".example") && !strings.HasSuffix(host, ".example:443") {
				t.Errorf("%s uses non-synthetic host %q", path, host)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestLenientShapes(t *testing.T) {
	body := []byte(`
# Synthetic fixture written for indexarr tests. Not derived from a real tracker.
id: Synthetic Lenient
name: Synthetic Lenient Tracker
type: public
links: https://lenient.example/
unexpectedTopLevel: { nested: true }
caps:
  modes:
    search: q,limit
  categorymappings:
    - { cat-id: 10, category: TV/HD, desc: TV }
settings:
  username: text
login:
  path: login.php
  method: post
  inputs:
    username: "{{ .Config.username }}"
    csrf_token: ""
    _token: "{{ .Config._token }}"
  test: a.logout
search:
  path: search.php
  rows: table.results tr
  fields:
    title: a.title
    link:
      selector: a.dl
      attribute: href
    category:
      selector: a.cat
      attribute: href
      filters: { querystring: cat }
`)
	result, err := Convert(body)
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	def := result.Definition
	if def.Meta.ID != "synthetic-lenient" {
		t.Errorf("meta.id = %q", def.Meta.ID)
	}
	if len(def.Categories) != 1 || def.Categories[0].Site != "10" || def.Categories[0].Standard != 5040 {
		t.Errorf("categories = %+v", def.Categories)
	}
	username := findSetting(t, def, "username")
	if username.Type != "text" || !username.Required {
		t.Errorf("username setting = %+v", username)
	}
	if def.Auth == nil || def.Auth.Success == nil || def.Auth.Success.Selector != "a.logout" {
		t.Errorf("auth = %+v", def.Auth)
	}
	if len(def.Auth.Steps) != 2 || len(def.Auth.Steps[0].Extract) != 2 {
		t.Errorf("auth steps = %+v", def.Auth.Steps)
	}
	if def.Auth.Steps[0].Extract["csrf_token"].Selector != `input[name="csrf_token"]` {
		t.Errorf("csrf extract = %+v", def.Auth.Steps[0].Extract)
	}
	if def.Auth.Steps[0].Extract["_token"].Selector != `input[name="_token"]` {
		t.Errorf("_token extract = %+v", def.Auth.Steps[0].Extract)
	}
	if def.Search.Request.Get != "search.php" {
		t.Errorf("request.get = %q", def.Search.Request.Get)
	}
	if def.Search.Response.Fields["title"].Selector[0] != "a.title" {
		t.Errorf("title field = %+v", def.Search.Response.Fields["title"])
	}
	download := def.Search.Response.Fields["download"]
	if download.Selector[0] != "a.dl" || download.Attr != "href" || download.Type != "url" {
		t.Errorf("download field = %+v", download)
	}
	category := def.Search.Response.Fields["category"]
	if category.Type != "category" || len(category.Filters) != 1 || category.Filters[0].Arg != "cat" {
		t.Errorf("category field = %+v", category)
	}
	if !containsSubstring(result.Warnings, `parameter "limit"`) {
		t.Errorf("warnings = %v", result.Warnings)
	}
}

func TestWeakSecretDoesNotBreakConversion(t *testing.T) {
	body := []byte(`
# Synthetic fixture written for indexarr tests. Not derived from a real tracker.
id: synthetic-weak-secret
name: Synthetic Weak Secret
type: private
links: https://weak.example/
login:
  path: login.php
  inputs:
    password: test
  test: a.logout
search:
  paths:
    - path: search.php
  inputs:
    q: "{{ .Keywords }}"
  rows: { selector: table tr }
  fields:
    title: { selector: a.title }
    download: { selector: a.dl, attribute: href }
`)
	result, err := Convert(body)
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	password := findSetting(t, result.Definition, "password")
	if password.Type != "password" {
		t.Errorf("password setting = %+v", password)
	}
	if result.Definition.Auth == nil {
		t.Fatal("auth was not converted")
	}
	if got := result.Definition.Auth.Steps[0].Form["password"]; got != "{{ settings.password }}" {
		t.Errorf("password form value = %q", got)
	}
}

func TestJSONRootArrayWithoutRows(t *testing.T) {
	body := []byte(`
# Synthetic fixture written for indexarr tests. Not derived from a real tracker.
id: synthetic-json-root
name: Synthetic JSON Root
type: public
links: https://root.example/
caps:
  modes:
    search: [q]
search:
  paths:
    - path: /api/search
  inputs:
    q: "{{ .Keywords }}"
  response:
    type: json
    fields:
      title: { path: name }
      download: { path: url }
`)
	result, err := Convert(body)
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	if result.Definition.Search.Response.Rows.Path != "" {
		t.Errorf("rows path = %q, want empty root", result.Definition.Search.Response.Rows.Path)
	}
	if !containsSubstring(result.Warnings, "response root must be an array") {
		t.Errorf("warnings = %v", result.Warnings)
	}
	if _, err := definition.Parse(result.Body); err != nil {
		t.Errorf("body does not parse: %v", err)
	}
}

func TestTemplateConversionIsSafe(t *testing.T) {
	newConverter := func() *converter {
		return &converter{
			settingNames: make(map[string]string),
			usedSettings: make(map[string]bool),
			extracted:    make(map[string]bool),
		}
	}

	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{name: "keywords", input: "{{ .Keywords }}", want: "{{ query.keywords }}"},
		{name: "query", input: "{{ .Query }}", want: "{{ query.keywords }}"},
		{name: "season", input: "{{ .Query.Season }}", want: "{{ query.season }}"},
		{name: "episode", input: "{{ .Query.Episode }}", want: "{{ query.ep }}"},
		{name: "categories", input: "{{ .Categories }}", want: "{{ categories }}"},
		{name: "category singular", input: "{{ .Category }}", want: "{{ categories }}"},
		{name: "join", input: `{{ .Categories | join(",") }}`, want: `{{ categories | join(",") }}`},
		{name: "config", input: "{{ .Config.sort }}", want: "{{ settings.sort }}"},
		{name: "native settings", input: "{{ settings.sort }}", want: "{{ settings.sort }}"},
		{name: "page", input: "{{ .Page }}", want: "{{ page }}"},
		{name: "offset", input: "{{ .Offset }}", want: "{{ offset }}"},
		{name: "dynamic today", input: "{{ .Today }}", wantErr: true},
		{name: "unknown", input: "{{ .Bogus }}", wantErr: true},
		{name: "function", input: "{{ index .Keywords 0 }}", wantErr: true},
		{name: "malformed", input: "{{ .Keywords", wantErr: true},
		{name: "empty config", input: "{{ .Config }}", wantErr: true},
		{name: "unsupported filter", input: "{{ .Keywords | urlquery }}", wantErr: true},
		{name: "join non-categories", input: `{{ .Keywords | join(",") }}`, wantErr: true},
		{name: "multiple filters", input: `{{ .Categories | join(",") | trim }}`, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			c := newConverter()
			var got string
			var err error
			func() {
				defer func() {
					if recovered := recover(); recovered != nil {
						err = fmt.Errorf("panic: %v", recovered)
					}
				}()
				got, err = c.convertTemplate("test", test.input, true)
			}()
			if test.wantErr {
				if err == nil {
					t.Fatalf("convertTemplate(%q) = %q, want error", test.input, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("convertTemplate(%q): %v", test.input, err)
			}
			if got != test.want {
				t.Errorf("convertTemplate(%q) = %q, want %q", test.input, got, test.want)
			}
		})
	}
}

func TestFilterMappingEdgeCases(t *testing.T) {
	newConverter := func() *converter {
		return &converter{
			settingNames: make(map[string]string),
			usedSettings: make(map[string]bool),
			extracted:    make(map[string]bool),
		}
	}

	c := newConverter()
	translated := c.convertFilters("f", rawFilters{
		{Name: "htmldecode"},
		{Name: "timeparse", Args: []string{"02-01-2006 15:04", "Europe/Rome"}},
	})
	if len(translated.layouts) != 1 || translated.layouts[0] != "02-01-2006 15:04" {
		t.Errorf("layouts = %v", translated.layouts)
	}
	if !translated.inferDate {
		t.Error("timeparse did not mark the field as a date")
	}
	if len(translated.filters) != 0 {
		t.Errorf("filters = %+v", translated.filters)
	}
	if !containsSubstring(c.result.Warnings, "htmldecode") {
		t.Errorf("warnings = %v", c.result.Warnings)
	}
	if !containsSubstring(c.result.Warnings, "timezone") {
		t.Errorf("warnings = %v", c.result.Warnings)
	}

	c = newConverter()
	translated = c.convertFilters("f", rawFilters{
		{Name: "replace", Args: []string{"a:b", "c"}},
		{Name: "regexp", Args: []string{"["}},
		{Name: "split", Args: []string{" ", "0"}},
		{Name: "join", Args: []string{","}},
		{Name: "urldecode"},
		{Name: "unknown-filter"},
		{Name: "", Invalid: true},
	})
	if len(translated.filters) != 0 {
		t.Errorf("unsupported filters leaked into output: %+v", translated.filters)
	}
	if !hasString(c.result.Unsupported, `filter "replace"`) {
		t.Errorf("unsupported = %v", c.result.Unsupported)
	}
	if !hasString(c.result.Unsupported, `filter "regexp"`) {
		t.Errorf("unsupported = %v", c.result.Unsupported)
	}
	if !hasString(c.result.Unsupported, `filter "unknown-filter"`) {
		t.Errorf("unsupported = %v", c.result.Unsupported)
	}
	if !containsSubstring(c.result.Unsupported, "filter in field") {
		t.Errorf("unsupported = %v", c.result.Unsupported)
	}
}
