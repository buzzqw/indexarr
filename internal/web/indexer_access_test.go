package web

import (
	"strings"
	"testing"

	"github.com/buzzqw/indexarr/internal/catalog"
	"github.com/buzzqw/indexarr/internal/definition"
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
		"torrentleech":      "login",
		"nexusphp-generic":  "login+passkey",
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
