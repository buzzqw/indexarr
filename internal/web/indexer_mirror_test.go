package web

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/buzzqw/indexarr/internal/catalog"
	"github.com/buzzqw/indexarr/internal/store"
)

func TestUnofficialMirrorBadgeAndFormWarning(t *testing.T) {
	env := newTestEnv(t)
	entry, ok := catalog.Get("1337x")
	if !ok {
		t.Fatal("1337x is not in the catalog")
	}
	request := httptest.NewRequest("GET", "/indexers?lang=it", nil)
	badges := env.server.indexerBadges(request, store.Indexer{
		DefinitionID: "1337x", Name: "1337x", Enabled: true,
	}, entry.Definition)
	found := false
	for _, badge := range badges {
		if badge.Label == "Mirror non ufficiale" {
			found = true
		}
	}
	if !found {
		t.Fatalf("badges = %+v, want the unofficial mirror badge", badges)
	}

	form, err := env.server.buildCompactIndexerForm(request, "1337x", nil, nil, nil, "")
	if err != nil {
		t.Fatalf("buildCompactIndexerForm: %v", err)
	}
	if !strings.Contains(form.MirrorWarning, "Mirror non ufficiale") {
		t.Fatalf("mirror warning = %q", form.MirrorWarning)
	}
	if form.StateWarning != "" {
		t.Fatalf("a verified definition must not show the draft warning: %q", form.StateWarning)
	}
}

func TestCookieFieldUsesTranslatedLabelAndHelp(t *testing.T) {
	env := newTestEnv(t)
	request := httptest.NewRequest("GET", "/indexers/new?lang=it", nil)
	form, err := env.server.buildCompactIndexerForm(request, "corsaro-account", nil, nil, nil, "")
	if err != nil {
		t.Fatalf("buildCompactIndexerForm: %v", err)
	}
	if len(form.RequiredFields) != 1 {
		t.Fatalf("required fields = %+v", form.RequiredFields)
	}
	field := form.RequiredFields[0]
	if field.Name != "cookie" {
		t.Fatalf("field = %+v, want cookie", field)
	}
	if field.Label != "Cookie di sessione" {
		t.Fatalf("label = %q", field.Label)
	}
	if !strings.Contains(field.Help, "Strumenti sviluppatore") {
		t.Fatalf("help = %q", field.Help)
	}
}
