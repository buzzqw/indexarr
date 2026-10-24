package web

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/buzzqw/indexarr/internal/store"
)

// networkForm returns the Settings → Network form markup.
func networkForm(t *testing.T, body string) string {
	t.Helper()
	start := strings.Index(body, `name="section" value="network"`)
	if start < 0 {
		t.Fatal("network form not found")
	}
	formStart := strings.LastIndex(body[:start], "<form")
	formEnd := strings.Index(body[start:], "</form>")
	if formStart < 0 || formEnd < 0 {
		t.Fatal("network form boundaries not found")
	}
	return body[formStart : start+formEnd+len("</form>")]
}

var submitButton = regexp.MustCompile(`(?s)<button\b[^>]*type="submit"[^>]*>(.*?)</button>`)

// TestNetworkFormSavesBeforeItTests guards the coordinator's report: pressing
// Enter in the FlareSolverr URL must run the normal Save, never the test.
func TestNetworkFormSavesBeforeItTests(t *testing.T) {
	env := newTestEnv(t)
	body := get(t, env.handler, "/settings?section=network").Body.String()
	form := networkForm(t, body)

	buttons := submitButton.FindAllStringSubmatch(form, -1)
	if len(buttons) < 2 {
		t.Fatalf("network form has %d submit buttons, want Save and Test", len(buttons))
	}
	if !strings.Contains(buttons[0][0], "Save settings") {
		t.Errorf("first submit button is not Save: %s", buttons[0][0])
	}
	if strings.Contains(buttons[0][0], "formaction=") {
		t.Errorf("first submit button redirects elsewhere: %s", buttons[0][0])
	}
	if !strings.Contains(buttons[1][0], "/settings/flaresolverr/test") {
		t.Errorf("test button is not after Save: %s", buttons[1][0])
	}

	if !strings.Contains(form, `name="flaresolverr_url" type="text"`) {
		t.Error("the FlareSolverr URL field must accept host:port (type=text)")
	}
}

func TestNetworkSaveAcceptsHostPortAndKeepsFieldError(t *testing.T) {
	env := newTestEnv(t)
	token, cookie := csrfFrom(t, env.handler, "/settings?section=network")
	values := url.Values{
		"csrf":                         {token},
		"section":                      {"network"},
		"flaresolverr_url":             {"127.0.0.1:8191"},
		"flaresolverr_timeout_seconds": {"60"},
		"host_rate":                    {"1"},
		"host_burst":                   {"4"},
	}
	recorder := postForm(t, env.handler, "/settings", values, cookie)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
	if location := recorder.Header().Get("Location"); !strings.Contains(location, "section=network") {
		t.Errorf("location = %q, want the network section", location)
	}
	settings, err := env.store.LoadSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if settings.FlareSolverrURL != "http://127.0.0.1:8191" {
		t.Errorf("FlareSolverr URL = %q, want the http:// prefix added", settings.FlareSolverrURL)
	}

	// An invalid address keeps the typed value and puts a localized error next
	// to the field, instead of a bare 400 page.
	values.Set("flaresolverr_url", "not a url")
	recorder = postForm(t, env.handler, "/settings", values, cookie)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("invalid URL status = %d: %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if !strings.Contains(body, `class="field-error"`) ||
		!strings.Contains(body, "must be an absolute http:// or https:// address") {
		t.Errorf("invalid URL did not render an English field error: %s", body)
	}
	if strings.Contains(body, "deve essere") {
		t.Error("the English page contains an Italian validation message")
	}
	if !strings.Contains(body, `name="flaresolverr_url"`) || !strings.Contains(body, `value="not a url"`) {
		t.Error("the invalid value was not kept in the field")
	}
}

func TestNetworkTestSavesHostPortEndpoint(t *testing.T) {
	env := newTestEnv(t)
	fs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1" {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, `{"status":"ok","solution":{"status":200,"response":"<html>solved</html>"}}`)
	}))
	defer fs.Close()

	hostPort := strings.TrimPrefix(fs.URL, "http://")
	token, cookie := csrfFrom(t, env.handler, "/settings?section=network")
	recorder := postForm(t, env.handler, "/settings/flaresolverr/test", url.Values{
		"csrf":                         {token},
		"flaresolverr_url":             {hostPort},
		"flaresolverr_timeout_seconds": {"30"},
	}, cookie)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
	location := recorder.Header().Get("Location")
	if !strings.Contains(location, "section=network") || !strings.Contains(location, "FlareSolverr+OK") {
		t.Errorf("location = %q", location)
	}
	settings, err := env.store.LoadSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if settings.FlareSolverrURL != fs.URL {
		t.Errorf("test saved %q, want %q", settings.FlareSolverrURL, fs.URL)
	}
	if settings.FlareSolverrTimeoutSeconds != 30 {
		t.Errorf("test saved timeout %d, want 30", settings.FlareSolverrTimeoutSeconds)
	}
}

func createTestNativeIndexer(t *testing.T, env *nativeEnv, name, slug string) int64 {
	t.Helper()
	token, cookie := csrfFrom(t, env.handler, "/indexers/new?definition=test-native")
	recorder := postForm(t, env.handler, "/indexers", url.Values{
		"csrf":            {token},
		"definition_id":   {"test-native"},
		"name":            {name},
		"slug":            {slug},
		"protocol":        {"usenet"},
		"priority":        {"25"},
		"enabled":         {"on"},
		"url":             {env.apiURL},
		"categories":      {"5000"},
		"setting_apikey":  {"synthetic-secret"},
		"setting_flavour": {"a"},
	}, cookie)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("create status = %d: %s", recorder.Code, recorder.Body.String())
	}
	indexers, err := env.store.ListIndexers(context.Background())
	if err != nil || len(indexers) == 0 {
		t.Fatalf("indexers = %#v, %v", indexers, err)
	}
	return indexers[len(indexers)-1].ID
}

func TestIndexerEditValidationSaysEditAndIsLocalized(t *testing.T) {
	env := newNativeEnv(t)
	id := createTestNativeIndexer(t, env, "Edit Target", "edit-target")
	editPath := "/indexers/" + strconvI(id) + "/edit"
	token, cookie := csrfFrom(t, env.handler, editPath)

	values := url.Values{
		"csrf":             {token},
		"definition_id":    {"test-native"},
		"name":             {"Edit Target"},
		"slug":             {"edit-target"},
		"protocol":         {"usenet"},
		"priority":         {"25"},
		"enabled":          {"on"},
		"url":              {env.apiURL},
		"categories":       {"5000"},
		"interval_seconds": {"1"},
	}
	recorder := postForm(t, env.handler, "/indexers/"+strconvI(id), values, cookie)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "<h1>Edit Edit Target</h1>") {
		t.Errorf("edit validation title is not an edit title: %s", firstHeading(body))
	}
	if strings.Contains(body, "Add test-native") || strings.Contains(body, "<h1>Add ") {
		t.Errorf("edit validation looks like an add form: %s", firstHeading(body))
	}
	if !strings.Contains(body, "The interval must be 0 or at least 60 seconds") {
		t.Error("the English page does not contain the English interval error")
	}
	if strings.Contains(body, "L'intervallo") {
		t.Error("the English page contains the Italian interval error")
	}

	// The same failure in Italian must stay Italian.
	recorder = postForm(t, env.handler, "/indexers/"+strconvI(id)+"?lang=it", values, cookie)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("Italian status = %d", recorder.Code)
	}
	italian := recorder.Body.String()
	if !strings.Contains(italian, "intervallo deve essere 0 o almeno 60 secondi") {
		t.Error("the Italian page does not localize the interval error")
	}
	if !strings.Contains(italian, "<h1>Modifica Edit Target</h1>") {
		t.Errorf("Italian edit title: %s", firstHeading(italian))
	}
}

func firstHeading(body string) string {
	start := strings.Index(body, "<h1>")
	if start < 0 {
		return "(no h1)"
	}
	end := strings.Index(body[start:], "</h1>")
	if end < 0 {
		return body[start:]
	}
	return body[start : start+end+len("</h1>")]
}

func TestStoredIndexerSecretIsNotMarkedRequired(t *testing.T) {
	env := newNativeEnv(t)
	id := createTestNativeIndexer(t, env, "Secret Target", "secret-target")
	body := get(t, env.handler, "/indexers/"+strconvI(id)+"/edit").Body.String()
	field := regexp.MustCompile(`(?s)<input id="setting_apikey".*?>`).FindString(body)
	if field == "" {
		t.Fatal("the stored API key field is missing from the edit form")
	}
	if strings.Contains(field, " required") {
		t.Errorf("a stored secret is still required by the browser: %s", field)
	}
	if !strings.Contains(field, "configured") {
		t.Errorf("a stored secret does not say it is configured: %s", field)
	}
}

func TestDownloadClientClearFlags(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	id, err := env.store.CreateDownloadClient(ctx, store.DownloadClient{
		Name: "Clear Test", Kind: "qbittorrent", Protocol: "torrent",
		URL: "http://127.0.0.1:8080", PasswordEnc: "sealed-password",
		APIKeyEnc: "sealed-key", Enabled: true,
	})
	if err != nil {
		t.Fatalf("CreateDownloadClient: %v", err)
	}
	client, err := env.store.GetDownloadClient(ctx, id)
	if err != nil {
		t.Fatal(err)
	}

	client.PasswordEnc = ""
	client.APIKeyEnc = ""
	if err := env.store.UpdateDownloadClient(ctx, client); err != nil {
		t.Fatalf("UpdateDownloadClient keep: %v", err)
	}
	kept, err := env.store.GetDownloadClient(ctx, client.ID)
	if err != nil {
		t.Fatal(err)
	}
	if kept.PasswordEnc == "" || kept.APIKeyEnc == "" {
		t.Errorf("blank fields cleared the stored secrets: %#v", kept)
	}

	kept.ClearPassword = true
	kept.ClearAPIKey = true
	if err := env.store.UpdateDownloadClient(ctx, kept); err != nil {
		t.Fatalf("UpdateDownloadClient clear: %v", err)
	}
	cleared, err := env.store.GetDownloadClient(ctx, client.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cleared.PasswordEnc != "" || cleared.APIKeyEnc != "" {
		t.Errorf("remove flags did not clear the secrets: %#v", cleared)
	}
}

func TestIndexerCreateSavesBeforeTesting(t *testing.T) {
	env := newNativeEnv(t)
	body := get(t, env.handler, "/indexers/new?definition=test-native").Body.String()
	start := strings.Index(body, `action="/indexers"`)
	end := strings.Index(body[start:], "</form>")
	if start < 0 || end < 0 {
		t.Fatal("create form not found")
	}
	form := body[start : start+end+len("</form>")]
	buttons := submitButton.FindAllStringSubmatch(form, -1)
	if len(buttons) < 2 {
		t.Fatalf("create form has %d submit buttons, want save and add-and-test", len(buttons))
	}
	if !strings.Contains(buttons[0][0], "Add without testing") {
		t.Errorf("the first submit button is not the plain save: %s", buttons[0][0])
	}
	if strings.Contains(buttons[0][0], `name="test"`) {
		t.Errorf("the first submit button runs the test: %s", buttons[0][0])
	}
	if !strings.Contains(buttons[1][0], `name="test"`) {
		t.Errorf("the add-and-test button is not after the save: %s", buttons[1][0])
	}
}
