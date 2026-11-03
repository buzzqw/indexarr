package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/buzzqw/indexarr/internal/indexer"
)

// The live search table shows one row per indexer. The count is an int in
// LiveIndexerResult while the numeric helpers of the templates take int64, so
// a "Results" value reaching the page as an int once made the whole page fail
// with "internal error" exactly when a search had run and returned rows.
func TestReleasesPageRendersLiveOutcomes(t *testing.T) {
	env := newTestEnv(t)
	req := httptest.NewRequest(http.MethodGet, "/releases?lang=en", nil)
	recorder := httptest.NewRecorder()
	data := releasesPage{
		base:          env.server.newBaseT(req, "releases.title", "releases"),
		LivePerformed: true,
		LiveStored:    3,
		LiveOutcomes: []indexer.LiveIndexerResult{{
			Name:     "Bench",
			Mode:     "search",
			Query:    "dark matter",
			Results:  3,
			Duration: time.Second,
		}},
	}
	env.server.render(recorder, req, "releases", data, http.StatusOK)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	for _, want := range []string{"Bench", "dark matter", ">3<"} {
		if !strings.Contains(body, want) {
			t.Errorf("the live outcome row is missing %q", want)
		}
	}
}
