package web

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/buzzqw/indexarr/internal/catalog"
	"github.com/buzzqw/indexarr/internal/definition"
	"github.com/buzzqw/indexarr/internal/store"
)

// Every built-in source must tell the user how it is accessed: a restricted
// one shows its credential fields or explains that the key is in the
// personal address, a public one says that no account is needed.
func TestEveryCatalogSourceHasAnAccessMethod(t *testing.T) {
	entries, err := catalog.Builtins()
	if err != nil {
		t.Fatalf("Builtins: %v", err)
	}
	for _, entry := range entries {
		def, err := definition.Parse(entry.Body)
		if err != nil {
			t.Fatalf("%s: %v", entry.ID, err)
		}
		keys := accessMethodKeys(def)
		kind := strings.ToLower(def.Meta.Type)
		restricted := kind == "private" || kind == "semi-private"
		switch {
		case len(keys) == 0:
			t.Errorf("%s: no access method", entry.ID)
		case restricted && keys[0] == "none":
			t.Errorf("%s: %s source reported as needing no account", entry.ID, kind)
		case !restricted && keys[0] == "urlkey":
			t.Errorf("%s: public source reported with a key in the address", entry.ID)
		}
		if keys[0] == "urlkey" {
			for _, setting := range def.Settings {
				if setting.Name == "url" && !strings.EqualFold(setting.Type, "password") {
					t.Errorf("%s: the personal address carries a key but is a %q setting", entry.ID, setting.Type)
				}
			}
		}
	}
}

func TestAccessMethodKeys(t *testing.T) {
	for id, want := range map[string]string{
		"torrentleech":      "login+cookie",
		"ddunlimited":       "login+cookie",
		"nexusphp-generic":  "login+passkey", // a family connector: no cookie
		"iptorrents":        "cookie",
		"corsaro-account":   "cookie",
		"torznab-generic":   "apikey",
		"torrentleech-rss":  "urlkey",
		"mircrew-smartfeed": "urlkey",
		"1337x":             "none",
	} {
		entry, ok := catalog.Get(id)
		if !ok {
			t.Fatalf("%s is not in the catalog", id)
		}
		def, err := definition.Parse(entry.Body)
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		if got := strings.Join(accessMethodKeys(def), "+"); got != want {
			t.Errorf("%s: access method = %q, want %q", id, got, want)
		}
	}
}

func TestAccessFieldsKeepAFixedOrder(t *testing.T) {
	fields := []indexerSettingField{{Name: "passkey"}, {Name: "cookie"}, {Name: "password"}, {Name: "apikey"}, {Name: "username"}}
	sortAccessFields(fields)
	var names []string
	for _, field := range fields {
		names = append(names, field.Name)
	}
	if got := strings.Join(names, ","); got != "username,password,cookie,apikey,passkey" {
		t.Fatalf("order = %s", got)
	}
}

func TestAccessSectionIsAlwaysOnTheForm(t *testing.T) {
	env := newNativeEnv(t)
	for id, want := range map[string]string{
		"1337x":            "This source does not need an account",
		"torrentleech-rss": "access is in the personal address",
		"torrentleech":     "Username and password",
	} {
		body := get(t, env.handler, "/indexers/new?definition="+id+"&lang=en").Body.String()
		for _, text := range []string{`id="access"`, "Site access", "Access method", want} {
			if !strings.Contains(body, text) {
				t.Errorf("%s: form missing %q", id, text)
			}
		}
	}
}

func TestAccessSectionIsAlwaysOnTheOverview(t *testing.T) {
	env := newNativeEnv(t)
	entry, ok := catalog.Get("1337x")
	if !ok {
		t.Fatal("1337x is not in the catalog")
	}
	seedDefinition(t, env.store, "1337x", "", string(entry.Body))
	id := seedPhase2Indexer(t, env.store, "1337x", "public-test", true, `{}`)
	body := get(t, env.handler, "/indexers/"+strconvI(id)+"?lang=en").Body.String()
	for _, text := range []string{`id="access"`, "Access method", "No account", "This source does not need an account"} {
		if !strings.Contains(body, text) {
			t.Errorf("overview missing %q", text)
		}
	}
	if strings.Contains(body, "Edit credentials") {
		t.Error("a public source offers to edit credentials it does not have")
	}
}

func TestFormLoginOffersTheCookieAsAlternative(t *testing.T) {
	env := newNativeEnv(t)
	request := httptest.NewRequest("GET", "/indexers/new?definition=torrentleech&lang=en", nil)
	form, err := env.server.buildCompactIndexerForm(request, "torrentleech", nil, nil, nil, "")
	if err != nil {
		t.Fatalf("buildCompactIndexerForm: %v", err)
	}
	var names []string
	for _, field := range form.AccessFields {
		names = append(names, field.Name)
		if field.Required {
			t.Errorf("%s is required, but the login and the cookie are alternatives", field.Name)
		}
	}
	if got := strings.Join(names, ","); got != "username,password,cookie" {
		t.Fatalf("access fields = %s", got)
	}
	if !form.CookieAlternative || !form.CanImportCookie {
		t.Errorf("cookie alternative = %v, import = %v", form.CookieAlternative, form.CanImportCookie)
	}

	body := get(t, env.handler, "/indexers/new?definition=torrentleech&lang=en").Body.String()
	for _, text := range []string{"One of the two is enough", "Username and password</span> or <span", "instead of the login", `name="setting_cookie"`} {
		if !strings.Contains(body, text) {
			t.Errorf("form missing %q", text)
		}
	}
}

func TestPastedCookieIsEnoughOnTheOverview(t *testing.T) {
	env := newNativeEnv(t)
	entry, ok := catalog.Get("torrentleech")
	if !ok {
		t.Fatal("torrentleech is not in the catalog")
	}
	seedDefinition(t, env.store, "torrentleech", "", string(entry.Body))
	id := seedPhase2Indexer(t, env.store, "torrentleech", "tl-cookie", true, `{}`)

	visible := stripDetails(get(t, env.handler, "/indexers/"+strconvI(id)+"?lang=en").Body.String())
	if !strings.Contains(visible, "Access is missing:") {
		t.Fatal("a login source with neither login nor cookie is not reported as missing access")
	}
	if strings.Contains(visible, "No session cookie yet") {
		t.Error("a login source asks for the cookie as if it were required")
	}

	if err := env.store.PutSecret(context.Background(), store.Secret{IndexerID: id, Key: "cookie", Nonce: []byte("n"), Ciphertext: []byte("c")}); err != nil {
		t.Fatalf("PutSecret: %v", err)
	}
	visible = stripDetails(get(t, env.handler, "/indexers/"+strconvI(id)+"?lang=en").Body.String())
	if strings.Contains(visible, "Access is missing:") {
		t.Error("the pasted cookie does not stand for the username and password")
	}
}
