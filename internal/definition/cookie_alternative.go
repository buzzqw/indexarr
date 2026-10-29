package definition

import "strings"

// CookieSetting is the setting that carries a session cookie copied from the
// browser.
const CookieSetting = "cookie"

// loginSettingNames are the credentials a form login posts. A session cookie
// pasted from the browser replaces all of them.
var loginSettingNames = map[string]bool{
	"username": true, "user": true, "email": true, "login": true,
	"password": true, "pass": true,
}

// CookieAlternative reports whether the definition signs in with a login
// form run by the definition engine and can therefore take the session
// cookie of a browser instead: the way out when the site asks for a captcha
// or a second factor. Family connectors (connector:) do their own login and
// do not read the cookie.
func (d *Definition) CookieAlternative() bool {
	if d == nil || strings.TrimSpace(d.Connector) != "" || d.Auth == nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(d.Auth.Strategy), "form") && len(d.Auth.Steps) > 0
}

// CookieCovers reports whether a required setting may stay empty because the
// session cookie replaces the login.
func (d *Definition) CookieCovers(name string) bool {
	return d.CookieAlternative() && loginSettingNames[strings.ToLower(strings.TrimSpace(name))]
}

// LoginSatisfied reports whether the required settings are present, counting
// a session cookie as a replacement for the login credentials. lookup returns
// whether a setting has a value.
func (d *Definition) LoginSatisfied(name string, lookup func(string) bool) bool {
	if lookup(name) {
		return true
	}
	return d.CookieCovers(name) && lookup(CookieSetting)
}

// addCookieAlternative appends the optional session cookie to a form-login
// definition that does not declare one, so the form, the vault and the
// browser import treat it as any other setting.
func (d *Definition) addCookieAlternative() {
	if !d.CookieAlternative() {
		return
	}
	for _, setting := range d.Settings {
		if strings.EqualFold(strings.TrimSpace(setting.Name), CookieSetting) {
			return
		}
	}
	d.Settings = append(d.Settings, Setting{
		Name:        CookieSetting,
		Type:        "password",
		Label:       "Session cookie",
		Help:        "Alternative to the login: the Cookie header of a browser already signed in to the site.",
		Alternative: true,
	})
}
