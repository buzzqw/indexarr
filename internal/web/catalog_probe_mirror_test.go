package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/buzzqw/indexarr/internal/catalog"
)

// A catalog site whose first domain is gone but whose mirror answers is
// reachable: «Prova» must not report it as failed.
func TestCatalogProbeTriesTheMirrors(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := dead.URL
	dead.Close()
	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer mirror.Close()

	site := &catalog.Site{URL: deadURL, URLs: []string{deadURL, mirror.URL}}
	if !probeAnyAddress(context.Background(), site) {
		t.Fatal("probe failed although the mirror answers")
	}
	if probeAnyAddress(context.Background(), &catalog.Site{URL: deadURL}) {
		t.Fatal("probe passed with every address down")
	}
}
