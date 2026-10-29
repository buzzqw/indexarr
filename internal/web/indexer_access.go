package web

import (
	"net/http"
	"sort"
	"strings"

	"github.com/buzzqw/indexarr/internal/definition"
)

// accessMethodView is one way a source lets indexarr in, shown above the
// access fields so the user sees why exactly those fields are asked.
type accessMethodView struct {
	Label string
	Help  string
}

// accessFieldRank keeps the access fields in the same order on every source:
// username, password, cookie, API key, passkey, then anything else.
func accessFieldRank(name string) int {
	lower := strings.ToLower(strings.TrimSpace(name))
	switch lower {
	case "username", "user", "email", "login":
		return 0
	case "password", "pass":
		return 1
	case "cookie", "cookievalue", "session", "session_cookie":
		return 2
	case "apikey", "api_key", "api-key", "token", "auth", "secret":
		return 3
	case "passkey", "rsskey":
		return 4
	}
	switch {
	case strings.HasSuffix(lower, "_password"):
		return 1
	case strings.HasSuffix(lower, "_cookie"):
		return 2
	case strings.HasSuffix(lower, "_key"), strings.HasSuffix(lower, "_token"), strings.HasSuffix(lower, "_secret"):
		return 3
	}
	return 5
}

func sortAccessFields(fields []indexerSettingField) {
	sort.SliceStable(fields, func(i, j int) bool {
		return accessFieldRank(fields[i].Name) < accessFieldRank(fields[j].Name)
	})
}

func sortAccessRows(rows []indexerAccessRow) {
	sort.SliceStable(rows, func(i, j int) bool {
		return accessFieldRank(rows[i].Name) < accessFieldRank(rows[j].Name)
	})
}

// accessMethodKeys names the access methods a definition uses, in the same
// order as the fields. A restricted source without credential fields keeps
// its key in the personal address (an RSS rsskey, a Smartfeed token); a
// public one needs no account at all.
func accessMethodKeys(def *definition.Definition) []string {
	if def == nil {
		return nil
	}
	kind := strings.ToLower(strings.TrimSpace(def.Meta.Type))
	restricted := kind == "private" || kind == "semi-private"
	found := map[string]bool{}
	hasURL := false
	for _, setting := range def.Settings {
		if strings.EqualFold(strings.TrimSpace(setting.Name), "url") {
			hasURL = true
		}
		if !isAccessSetting(setting.Name) || (!setting.Required && !restricted) {
			continue
		}
		switch accessFieldRank(setting.Name) {
		case 0, 1:
			found["login"] = true
		case 2:
			found["cookie"] = true
		case 3:
			found["apikey"] = true
		case 4:
			found["passkey"] = true
		}
	}
	var keys []string
	for _, key := range []string{"login", "cookie", "apikey", "passkey"} {
		if found[key] {
			keys = append(keys, key)
		}
	}
	if len(keys) > 0 {
		return keys
	}
	if restricted && hasURL {
		return []string{"urlkey"}
	}
	return []string{"none"}
}

var accessMethodText = map[string][2]string{
	"login":   {"idx.accessMethod.login.label", "idx.accessMethod.login.help"},
	"cookie":  {"idx.accessMethod.cookie.label", "idx.accessMethod.cookie.help"},
	"apikey":  {"idx.accessMethod.apikey.label", "idx.accessMethod.apikey.help"},
	"passkey": {"idx.accessMethod.passkey.label", "idx.accessMethod.passkey.help"},
	"urlkey":  {"idx.accessMethod.urlkey.label", "idx.accessMethod.urlkey.help"},
	"none":    {"idx.accessMethod.none.label", "idx.accessMethod.none.help"},
}

func (s *Server) accessMethods(r *http.Request, def *definition.Definition) []accessMethodView {
	var views []accessMethodView
	for _, key := range accessMethodKeys(def) {
		text := accessMethodText[key]
		views = append(views, accessMethodView{
			Label: s.translate(r, text[0]),
			Help:  s.translate(r, text[1]),
		})
	}
	return views
}
