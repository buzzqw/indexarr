package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

const debugFixtureHTML = `<html><body><table class="search-table"><tbody>
<tr><td><a class="title" href="/dl/123">First release</a></td><td><span class="date" title="2026-11-23">today</span></td></tr>
<tr><td><a class="title" href="/dl/456">Second release</a></td><td><span class="date" title="2026-11-22">yesterday</span></td></tr>
</tbody></table></body></html>`

func postDebug(t *testing.T, env *testEnv, token string, cookie *http.Cookie, values url.Values) string {
	t.Helper()
	values.Set("csrf", token)
	recorder := postForm(t, env.handler, "/definitions/debug", values, cookie)
	if recorder.Code != http.StatusOK {
		t.Fatalf("debug status = %d: %s", recorder.Code, recorder.Body.String())
	}
	return recorder.Body.String()
}

func TestDefinitionDebugReadsRowsFieldsAndRegex(t *testing.T) {
	env := newTestEnv(t)
	token, cookie := csrfFrom(t, env.handler, "/definitions/debug")
	body := postDebug(t, env, token, cookie, url.Values{
		"html":           {debugFixtureHTML},
		"row_selector":   {"table.search-table tbody tr"},
		"field_selector": {"a.title"},
		"attribute":      {"href"},
		"pattern":        {`^/dl/(\d+)`},
	})
	for _, want := range []string{"2 rows matched", "/dl/123", "/dl/456", "123", "456", "The expression matches this value."} {
		if !strings.Contains(body, want) {
			t.Errorf("result missing %q", want)
		}
	}
	if strings.Contains(body, "Only the first 10 rows") || strings.Contains(body, "No row matched") {
		t.Error("unexpected warning in the result")
	}
}

func TestDefinitionDebugReadsTextAndGroupsOfManyRows(t *testing.T) {
	env := newTestEnv(t)
	token, cookie := csrfFrom(t, env.handler, "/definitions/debug")
	rows := strings.Repeat(`<tr><td><a class="title" href="/dl/1">A title</a></td></tr>`, 12)
	body := postDebug(t, env, token, cookie, url.Values{
		"html":         {"<table><tbody>" + rows + "</tbody></table>"},
		"row_selector": {"tr"},
		"attribute":    {"text"},
		"pattern":      {"(A) (title)"},
	})
	if !strings.Contains(body, "12 rows matched") {
		t.Error("the full row count is missing")
	}
	if !strings.Contains(body, "Only the first 10 rows") {
		t.Error("the truncation note is missing")
	}
	if !strings.Contains(body, "A title") || !strings.Contains(body, ">A<") || !strings.Contains(body, ">title<") {
		t.Error("text extraction or capture groups are missing")
	}
}

func TestDefinitionDebugReportsErrorsInTheOperatorLanguage(t *testing.T) {
	env := newTestEnv(t)
	token, cookie := csrfFrom(t, env.handler, "/definitions/debug")

	empty := postDebug(t, env, token, cookie, url.Values{})
	if !strings.Contains(empty, "Paste the page HTML and write a row selector.") {
		t.Error("empty form has no explanation")
	}

	badSelector := postDebug(t, env, token, cookie, url.Values{
		"html": {debugFixtureHTML}, "row_selector": {"tr["},
	})
	if !strings.Contains(badSelector, "The row selector is not valid:") {
		t.Error("invalid row selector has no explanation")
	}

	badRegex := postDebug(t, env, token, cookie, url.Values{
		"html": {debugFixtureHTML}, "row_selector": {"tr"}, "pattern": {"["},
	})
	if !strings.Contains(badRegex, "The regular expression is not valid:") {
		t.Error("invalid regex has no explanation")
	}

	noRows := postDebug(t, env, token, cookie, url.Values{
		"html": {debugFixtureHTML}, "row_selector": {".not-there"},
	})
	if !strings.Contains(noRows, "No row matched this selector.") {
		t.Error("an empty result has no explanation")
	}

	italianValues := url.Values{"csrf": {token}}
	italianRecorder := postForm(t, env.handler, "/definitions/debug?lang=it", italianValues, cookie)
	if italianRecorder.Code != http.StatusOK {
		t.Fatalf("italian debug status = %d", italianRecorder.Code)
	}
	italian := italianRecorder.Body.String()
	if !strings.Contains(italian, "della pagina e scrivi un selettore") {
		t.Error("the error is not translated")
	}
}

func TestDefinitionDebugPageIsAvailableInEveryLanguage(t *testing.T) {
	env := newTestEnv(t)
	titles := map[string]string{
		"en": "Definition debugger", "it": "Debugger delle definizioni",
		"de": "Definitionen-Debugger", "fr": "Débogueur de définitions",
		"es": "Depurador de definiciones", "pl": "Debugger definicji",
		"zh": "定义调试器",
	}
	for lang, title := range titles {
		body := get(t, env.handler, "/definitions/debug?lang="+lang).Body.String()
		if !strings.Contains(body, title) {
			t.Errorf("%s: title %q is missing", lang, title)
		}
		if !strings.Contains(body, `action="/definitions/debug"`) {
			t.Errorf("%s: the form is missing", lang)
		}
		if strings.Contains(body, "defDebug.") || strings.Contains(body, "pageHelp.definition_debug") {
			t.Errorf("%s: unresolved catalog key", lang)
		}
		if !strings.Contains(body, "page-help") {
			t.Errorf("%s: the explanatory paragraph is missing", lang)
		}
	}
}
