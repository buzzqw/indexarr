package web

import (
	"os"
	"strings"
	"testing"
)

// TestReleasesColumnsAreCustomizable pins the shared contract between the
// template, the CSS and app.js: every column is identified by name, the
// control is present in every language, and the fixed widths follow the
// column instead of its position, so reordering cannot break the layout.
func TestReleasesColumnsAreCustomizable(t *testing.T) {
	env := newTestEnv(t)
	insertSearchRelease(t, env.store, "Columns Test 1080p", "5040")

	for _, lang := range []string{"en", "it", "de", "fr", "es", "pl", "zh"} {
		body := get(t, env.handler, "/releases?lang="+lang).Body.String()
		for _, marker := range []string{
			`data-columns`, `data-columns-menu`, `data-columns-list`, `data-columns-reset`,
			`data-column="check"`, `data-column="title"`, `data-column="size"`,
			`data-column="seeders"`, `data-column="published"`, `data-column="actions"`,
			`data-column-toggle="title"`, `data-column-toggle="size"`,
			`data-column-toggle="seeders"`, `data-column-toggle="published"`,
			`data-column-move="up"`, `data-column-move="down"`,
		} {
			if !strings.Contains(body, marker) {
				t.Errorf("%s: missing %q", lang, marker)
			}
		}
		if strings.Contains(body, "cols.") {
			t.Errorf("%s: unresolved column key", lang)
		}
	}

	emptyEnv := newTestEnv(t)
	if body := get(t, emptyEnv.handler, "/releases").Body.String(); !strings.Contains(body, `data-columns-empty`) {
		t.Error("the empty row has no column marker for the colspan fix")
	}

	css, err := os.ReadFile("static/app.css")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(css), ".releases-table th:nth-child") {
		t.Error("column widths still depend on the position: reordering would break the layout")
	}
	for _, key := range []string{"check", "size", "seeders", "published", "actions"} {
		if !strings.Contains(string(css), `.releases-table th[data-column="`+key+`"]`) {
			t.Errorf("no width rule for column %q", key)
		}
	}

	script, err := os.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{"indexarr.columns.releases", "data-column-toggle", "data-column-move", "data-columns-reset"} {
		if !strings.Contains(string(script), marker) {
			t.Errorf("app.js is missing %q", marker)
		}
	}
}
