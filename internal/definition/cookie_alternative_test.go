package definition

import "testing"

func TestFormLoginGetsTheCookieAlternative(t *testing.T) {
	def, err := Load("testdata/example.yaml")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	last := def.Settings[len(def.Settings)-1]
	if last.Name != CookieSetting || !last.Alternative || last.Required || last.Type != "password" {
		t.Fatalf("cookie alternative = %+v", last)
	}
	only := func(names ...string) func(string) bool {
		return func(name string) bool {
			for _, n := range names {
				if n == name {
					return true
				}
			}
			return false
		}
	}
	if !def.LoginSatisfied("password", only("cookie")) {
		t.Error("the cookie does not replace the password")
	}
	if def.LoginSatisfied("passkey", only("cookie")) {
		t.Error("the cookie replaces a setting that is not a login credential")
	}
	def.Connector = "nexusphp"
	if def.CookieAlternative() {
		t.Error("a family connector, which does its own login, is offered the cookie")
	}
	if def.LoginSatisfied("username", only()) {
		t.Error("an empty login is accepted without a cookie")
	}
}
