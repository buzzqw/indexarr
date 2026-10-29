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
	fields := append(append([]indexerSettingField{}, form.RequiredFields...), form.AccessFields...)
	var field indexerSettingField
	found := false
	for _, candidate := range fields {
		if candidate.Name == "cookie" {
			field = candidate
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("required fields = %+v, access fields = %+v", form.RequiredFields, form.AccessFields)
	}
	if field.Label != "Cookie di sessione" {
		t.Fatalf("label = %q", field.Label)
	}
	if !strings.Contains(field.Help, "Strumenti sviluppatore") {
		t.Fatalf("help = %q", field.Help)
	}
}

func TestOptionalCookieIsShownInTheAccessSection(t *testing.T) {
	env := newNativeEnv(t)
	request := httptest.NewRequest("GET", "/indexers/new?definition=iptorrents&lang=it", nil)
	form, err := env.server.buildCompactIndexerForm(request, "iptorrents", nil, nil, nil, "")
	if err != nil {
		t.Fatalf("buildCompactIndexerForm: %v", err)
	}
	var cookie *indexerSettingField
	for i := range form.AccessFields {
		if form.AccessFields[i].Name == "cookie" {
			cookie = &form.AccessFields[i]
		}
	}
	if cookie == nil {
		t.Fatalf("access fields = %+v, want the optional cookie", form.AccessFields)
	}
	if cookie.Required {
		t.Fatal("the IPTorrents cookie must stay optional: it can be imported")
	}
	if form.NoAccessFields {
		t.Fatal("the form still says the source has no access fields")
	}

	body := stripDetails(get(t, env.handler, "/indexers/new?definition=iptorrents&lang=it").Body.String())
	start := strings.Index(body, `<section class="form-access"`)
	if start < 0 {
		t.Fatal("the form has no Access section")
	}
	section := body[start:]
	if end := strings.Index(section, `</section>`); end >= 0 {
		section = section[:end]
	}
	if !strings.Contains(section, `name="setting_cookie"`) {
		t.Fatalf("the cookie is not in the Access section: %s", section)
	}
}
