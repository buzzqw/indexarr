package web

import (
	"encoding/csv"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestReleasesExportCSV(t *testing.T) {
	env := newTestEnv(t)
	insertSearchRelease(t, env.store, "Export Show 1080p", "5040")
	insertSearchRelease(t, env.store, "Other Show 1080p", "5040")

	recorder := get(t, env.handler, "/releases/export?format=csv&q=Export")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Type"); !strings.Contains(got, "text/csv") {
		t.Errorf("content type = %q", got)
	}
	if got := recorder.Header().Get("Content-Disposition"); !strings.HasPrefix(got, `attachment; filename="indexarr-releases-`) || !strings.Contains(got, ".csv") {
		t.Errorf("content disposition = %q", got)
	}
	rows, err := csv.NewReader(strings.NewReader(recorder.Body.String())).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("csv rows = %d, want header + 1 release: %v", len(rows), rows)
	}
	if rows[0][1] != "title" || rows[0][9] != "url" {
		t.Errorf("csv header = %v", rows[0])
	}
	if !strings.Contains(rows[1][1], "Export Show") {
		t.Errorf("csv row = %v", rows[1])
	}
	if !strings.Contains(rows[1][9], "/releases/") {
		t.Errorf("csv link = %q", rows[1][9])
	}
}

func TestReleasesExportJSON(t *testing.T) {
	env := newTestEnv(t)
	insertSearchRelease(t, env.store, "JSON Show 1080p", "5040")

	recorder := get(t, env.handler, "/releases/export?format=json&q=JSON")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	if got := recorder.Header().Get("Content-Type"); !strings.Contains(got, "application/json") {
		t.Errorf("content type = %q", got)
	}
	var payload struct {
		Count     int `json:"count"`
		Truncated bool
		Releases  []struct {
			Title  string `json:"title"`
			Magnet string `json:"magnet"`
			URL    string `json:"url"`
		} `json:"releases"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Count != 1 || payload.Truncated || len(payload.Releases) != 1 {
		t.Fatalf("payload = %+v", payload)
	}
	if !strings.Contains(payload.Releases[0].Title, "JSON Show") ||
		!strings.HasPrefix(payload.Releases[0].Magnet, "magnet:") ||
		!strings.Contains(payload.Releases[0].URL, "/releases/") {
		t.Errorf("release = %+v", payload.Releases[0])
	}
}

func TestReleasesExportRejectsUnknownFormat(t *testing.T) {
	env := newTestEnv(t)
	recorder := get(t, env.handler, "/releases/export?format=xml")
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("status = %d", recorder.Code)
	}
	if got := recorder.Header().Get("Location"); !strings.Contains(got, "err=") {
		t.Errorf("location = %q", got)
	}
}

func TestCSVSafeNeutralizesFormulaMarkers(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"=cmd|'/c calc'!A1", "'=cmd|'/c calc'!A1"},
		{"+SUM(A1)", "'+SUM(A1)"},
		{"-1", "'-1"},
		{"@SUM(A1)", "'@SUM(A1)"},
		{"A normal title", "A normal title"},
		{"", ""},
	} {
		if got := csvSafe(tc.in); got != tc.want {
			t.Errorf("csvSafe(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestExportButtonsAreAvailableInEveryLanguage(t *testing.T) {
	env := newTestEnv(t)
	for _, lang := range []string{"en", "it", "de", "fr", "es", "pl", "zh"} {
		body := get(t, env.handler, "/releases?lang="+lang).Body.String()
		if !strings.Contains(body, `/releases/export?format=csv`) || !strings.Contains(body, `/releases/export?format=json`) {
			t.Errorf("%s: export links are missing", lang)
		}
		if !strings.Contains(body, "class=\"export-actions\"") {
			t.Errorf("%s: the export block is missing", lang)
		}
		if strings.Contains(body, "export.csv") || strings.Contains(body, "export.help") {
			t.Errorf("%s: unresolved export key", lang)
		}
	}
}
