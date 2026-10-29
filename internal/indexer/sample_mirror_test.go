package indexer

import (
	"testing"

	"github.com/buzzqw/indexarr/internal/engine"
)

// A run that met a dead address (Cloudflare 521) and then read the mirror
// keeps the mirror's answer as its sample; a later error never replaces a
// good answer.
func TestRunSampleKeepsTheMirrorThatAnswered(t *testing.T) {
	capture := newRunCapture(nil)
	capture.observe(engine.ResponseObservation{URL: "https://dead.example/search", Status: 521})
	capture.observe(engine.ResponseObservation{URL: "https://mirror.example/search", Status: 200, Body: []byte("ok")})
	capture.observe(engine.ResponseObservation{URL: "https://mirror.example/t/1", Status: 404})
	sample, ok := capture.sample(1, nil)
	if !ok {
		t.Fatal("no sample")
	}
	if sample.Status != 200 || sample.RequestURL != "https://mirror.example/search" {
		t.Errorf("sample = %d %q, want the mirror's 200", sample.Status, sample.RequestURL)
	}
}
