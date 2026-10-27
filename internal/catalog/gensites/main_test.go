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
		{"public timeout is inconclusive", "public", false, catalogVerification{Error: "context deadline exceeded"}, true, catalog.SiteStateTry},
		{"public refused connection is dead", "public", false, catalogVerification{Error: "connect: connection refused"}, true, catalog.SiteStateDead},
		{"public ipv6 route is inconclusive", "public", false, catalogVerification{Error: "network is unreachable"}, true, catalog.SiteStateTry},
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

func TestApplyBlockedOverridesProbe(t *testing.T) {
	site := catalog.Site{
		ID:       "jk-example",
		SourceID: "example",
		State:    catalog.SiteStateVerified,
		Evidence: "GET -> 200",
	}
	applyBlocked(&site, map[string]blockedSource{
		"example": {ID: "example", State: catalog.SiteStateBlocked, Reason: "captcha", Evidence: "GET -> 403", Checked: "2026-10-27"},
	})
	if site.State != catalog.SiteStateBlocked || site.Evidence != "GET -> 403" || site.Note != "captcha" || site.Checked != "2026-10-27" {
		t.Fatalf("site = %+v", site)
	}

	applyBlocked(&site, map[string]blockedSource{
		"jk-example": {ID: "jk-example", State: catalog.SiteStateDead, Reason: "gone", Evidence: "GET -> 410", Checked: "2026-10-27"},
	})
	if site.State != catalog.SiteStateDead || site.Note != "gone" {
		t.Fatalf("manual id fallback failed: %+v", site)
	}

	applyBlocked(&site, map[string]blockedSource{
		"other": {ID: "other", State: catalog.SiteStateBlocked, Reason: "x", Evidence: "y", Checked: "z"},
	})
	if site.State != catalog.SiteStateDead {
		t.Fatalf("an unrelated entry changed the site: %+v", site)
	}
}

func TestBlockedManifestIsValid(t *testing.T) {
	blocked := loadBlocked("../metadata/blocked.yaml")
	if len(blocked) == 0 {
		t.Fatal("blocked manifest is empty")
	}
	for id, entry := range blocked {
		if entry.Reason == "" || entry.Evidence == "" || entry.Checked == "" {
			t.Errorf("%s: incomplete entry %+v", id, entry)
		}
		if entry.State != catalog.SiteStateBlocked && entry.State != catalog.SiteStateDead {
			t.Errorf("%s: state = %q", id, entry.State)
		}
	}
}

func TestSingleFamilyRejectsAmbiguousPages(t *testing.T) {
	for _, test := range []struct {
		name string
		in   []string
		want string
	}{
		{"one", []string{catalog.FamilyNexusPHP}, catalog.FamilyNexusPHP},
		{"duplicate marker", []string{"unit3d", "unit3d"}, catalog.FamilyUnit3D},
		{"two families", []string{"unit3d", "nexusphp"}, ""},
		{"empty", nil, ""},
		{"blank", []string{" ", ""}, ""},
	} {
		if got := singleFamily(test.in); got != test.want {
			t.Errorf("%s: singleFamily(%v) = %q, want %q", test.name, test.in, got, test.want)
		}
	}
}

func TestDeadProbeErrorIsNarrow(t *testing.T) {
	for text, want := range map[string]bool{
		"dial tcp: lookup x on 1.1.1.1:53: no such host": true,
		"connect: connection refused":                    true,
		"context deadline exceeded":                      false,
		"network is unreachable":                         false,
		"tls: handshake failure":                         false,
	} {
		if got := deadProbeError(text); got != want {
			t.Errorf("deadProbeError(%q) = %v, want %v", text, got, want)
		}
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
