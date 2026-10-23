package main

import (
	"testing"

	"github.com/buzzqw/indexarr/internal/catalog"
)

func TestDecideState(t *testing.T) {
	tests := []struct {
		name      string
		siteType  string
		family    bool
		live      catalogVerification
		hasLive   bool
		wantState string
	}{
		{"public live", "public", false, catalogVerification{HTTP: 200, Bytes: 5000, Title: "Tracker"}, true, catalog.SiteStateVerified},
		{"public challenge", "public", false, catalogVerification{HTTP: 200, Bytes: 5000, Title: "Just a moment..."}, true, catalog.SiteStateTry},
		{"public forbidden", "public", false, catalogVerification{HTTP: 403, Bytes: 5000}, true, catalog.SiteStateTry},
		{"public tiny page", "public", false, catalogVerification{HTTP: 200, Bytes: 120, Title: "x"}, true, catalog.SiteStateTry},
		{"public unreachable", "public", false, catalogVerification{Error: "no such host"}, true, catalog.SiteStateDead},
		{"private live", "private", true, catalogVerification{HTTP: 200, Bytes: 9000, Title: "Login"}, true, catalog.SiteStateTry},
		{"semi login", "semi-private", false, catalogVerification{HTTP: 200, Bytes: 9000, Title: "Login"}, true, catalog.SiteStateTry},
		{"no probe", "public", false, catalogVerification{}, false, catalog.SiteStateTry},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state, evidence := decideState(test.siteType, test.family, test.live, test.hasLive)
			if state != test.wantState {
				t.Errorf("state = %q, want %q (evidence %q)", state, test.wantState, evidence)
			}
			if evidence == "" {
				t.Error("evidence is empty")
			}
		})
	}
}

func TestRequiredForPreventsLightweightQuickAdd(t *testing.T) {
	for _, test := range []struct {
		siteType string
		family   string
		want     string
	}{
		{"public", "", "url"},
		{"public", catalog.FamilyUnit3D, "apikey"},
		{"private", "", "username"},
		{"private", catalog.FamilyNexusPHP, "username"},
		{"private", catalog.FamilyTorznab, "url"},
	} {
		required := requiredFor(test.siteType, test.family)
		if len(required) == 0 || required[0] != test.want {
			t.Errorf("requiredFor(%q, %q) = %v, want first %q", test.siteType, test.family, required, test.want)
		}
	}
}

func TestSlugify(t *testing.T) {
	for raw, want := range map[string]string{
		"aniRena":      "anirena",
		"hdtorrentsit": "hdtorrentsit",
		"a.b/c":        "a-b-c",
		"  x_y  ":      "x-y",
	} {
		if got := slugify(raw); got != want {
			t.Errorf("slugify(%q) = %q, want %q", raw, got, want)
		}
	}
}
