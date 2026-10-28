package web

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/buzzqw/indexarr/internal/health"
	"github.com/buzzqw/indexarr/internal/i18n"
	"github.com/buzzqw/indexarr/internal/store"
)

// A flood refusal and a deferred search are known conditions: Home and the
// indexer page must say what the site is doing, not "unexpected error".
func TestFloodErrorsHaveTheirOwnProblem(t *testing.T) {
	messages := []string{
		`indexer ddunlimited: search: definition "ddunlimited": search deferred by the flood interval: the site allows one search every 1m5s`,
		`indexer ddunlimited: search: definition "ddunlimited": search refused by the site: Al momento non ti è permesso fare ricerche`,
		`indexer ddunlimited: background search: definition "ddunlimited": search refused by the site: Al momento non ti è permesso fare ricerche`,
	}
	for _, message := range messages {
		if got := explainIndexerError(message, ""); got != "home.problem.flood" {
			t.Errorf("explainIndexerError(%q) = %q, want home.problem.flood", message, got)
		}
	}
	// The concrete rate-limit cause keeps priority over the generic flood
	// wording, so a 429 still points at the frequency control.
	if got := explainIndexerError("HTTP 429 too many requests", ""); got != "home.problem.rateLimit" {
		t.Errorf("429 = %q, want home.problem.rateLimit", got)
	}
}

// A concrete cause without a %d must not carry the failure count: the Home
// template passes it to Sprintf only when Count is non-zero.
func TestConcreteProblemCausesDoNotCarryACount(t *testing.T) {
	view := indexerHealthView{Assessment: health.Assessment{
		Reasons:   []health.Reason{{Code: health.ReasonErrors, Count: 3}},
		LastError: `definition "ddunlimited": search refused by the site: Al momento non ti è permesso fare ricerche`,
	}}
	if code, count := homeProblemCode(view); code != "home.problem.flood" || count != 0 {
		t.Fatalf("homeProblemCode = %q/%d, want home.problem.flood/0", code, count)
	}
}

func TestFloodProblemTextIsLocalized(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/indexers/51", nil)
	problem := (&Server{}).problemForMessage(request,
		store.Indexer{ID: 51, Slug: "ddunlimited"},
		`definition "ddunlimited": search refused by the site: Al momento non ti è permesso fare ricerche`)
	if problem.Text != i18n.T(i18n.EN, "idx.problem.flood") {
		t.Errorf("flood problem text = %q, want %q", problem.Text, i18n.T(i18n.EN, "idx.problem.flood"))
	}
	if problem.ActionURL != "/indexers/51" {
		t.Errorf("flood action = %q, want the indexer page", problem.ActionURL)
	}
}
