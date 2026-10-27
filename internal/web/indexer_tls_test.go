package web

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/buzzqw/indexarr/internal/catalog"
	"github.com/buzzqw/indexarr/internal/store"
)

// TestInsecureTLSBadgeAndFormWarning pins the visible side of the TLS
// exception: a definition that opts into skipping certificate verification
// shows a badge on the indexer and a warning in the form, in Italian too.
func TestInsecureTLSBadgeAndFormWarning(t *testing.T) {
	env := newTestEnv(t)
	entry, ok := catalog.Get("torrentby")
	if !ok {
		t.Fatal("torrentby is not in the catalog")
	}
	if !entry.Definition.Meta.InsecureTLS {
		t.Fatal("the torrentby definition lost meta.insecure_tls")
	}

	request := httptest.NewRequest("GET", "/indexers?lang=it", nil)
	badges := env.server.indexerBadges(request, store.Indexer{
		DefinitionID: "torrentby", Name: "torrent.by", Enabled: true,
	}, entry.Definition)
	found := false
	for _, badge := range badges {
		if badge.Kind == "tls" && badge.Label == "TLS non verificato" {
			found = true
		}
	}
	if !found {
		t.Fatalf("badges = %+v, want the TLS badge", badges)
	}

	form, err := env.server.buildCompactIndexerForm(request, "torrentby", nil, nil, nil, "")
	if err != nil {
		t.Fatalf("buildCompactIndexerForm: %v", err)
	}
	if !strings.Contains(form.TLSWarning, "catena incompleta") {
		t.Fatalf("TLS warning = %q", form.TLSWarning)
	}
	if form.MirrorWarning != "" {
		t.Fatalf("torrent.by is not a mirror: %q", form.MirrorWarning)
	}
}
