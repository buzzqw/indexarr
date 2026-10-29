package engine

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/buzzqw/indexarr/internal/definition"
)

func floodBackoffConnector(t *testing.T, handler http.Handler) *DefinitionConnector {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	def, err := definition.Parse([]byte(`schema: 1
meta:
  id: flood-backoff
  name: Flood backoff
  type: public
  protocol: torrent
  urls:
    - ` + server.URL + `/
capabilities:
  search: [q]
search:
  interval: 1m
  cache: 1h
  blocked: { selector: "div.refused", message: text }
  request:
    get: /search
    params: { q: "{{ query.keywords }}" }
  response:
    type: html
    rows: { selector: "tr" }
    fields:
      title: { selector: "a" }
`))
	if err != nil {
		t.Fatalf("parse definition: %v", err)
	}
	connector, err := NewDefinitionConnector(DefinitionConfig{
		Indexer:    IndexerRef{Slug: def.Meta.ID, Name: def.Meta.Name},
		Definition: def,
		Transport:  NewTransport(TransportConfig{AllowPrivate: true}),
	})
	if err != nil {
		t.Fatalf("NewDefinitionConnector: %v", err)
	}
	t.Cleanup(connector.Close)
	return connector
}

func TestSearchRefusalPauseDoublesUpToTheCap(t *testing.T) {
	connector := floodBackoffConnector(t, http.NotFoundHandler())
	var got []time.Duration
	for range 6 {
		connector.noteSearchRefused()
		got = append(got, connector.searchBlockedFor().Round(time.Minute))
	}
	want := []time.Duration{4 * time.Minute, 8 * time.Minute, 16 * time.Minute, 32 * time.Minute, 32 * time.Minute, 32 * time.Minute}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("pauses = %v, want %v", got, want)
		}
	}
	connector.noteSearchAccepted()
	if wait := connector.searchBlockedFor(); wait != 0 {
		t.Errorf("pause after an accepted search = %s, want 0", wait)
	}
}

// During the pause a live search no longer waits up to two intervals for a
// turn the site refuses anyway (167 s on DDUnlimited at 02:06 on 2026-10-29):
// it is queued at once and nothing reaches the site.
func TestLiveSearchDuringARefusalPauseIsQueuedWithoutTouchingTheSite(t *testing.T) {
	var requests atomic.Int32
	connector := floodBackoffConnector(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		writeBody(t, w, []byte(`<html><body><div class="refused">Al momento non ti è permesso fare ricerche.</div></body></html>`))
	}))
	ctx := context.Background()
	if _, err := connector.Search(ctx, SearchRequest{Mode: ModeSearch, Query: "first"}); !errors.Is(err, errSearchRefused) {
		t.Fatalf("first search error = %v, want the refusal", err)
	}
	_, err := connector.Search(ctx, SearchRequest{Mode: ModeSearch, Query: "second"})
	if !errors.Is(err, ErrSearchDeferred) || !strings.Contains(err.Error(), "refused the last search") {
		t.Fatalf("second search error = %v, want it queued during the pause", err)
	}
	if requests.Load() != 1 {
		t.Errorf("site requests = %d, want 1", requests.Load())
	}
}
