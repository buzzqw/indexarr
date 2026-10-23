package catalog

import (
	"strings"
	"testing"

	"github.com/buzzqw/indexarr/internal/definition"
)

func TestSitesAreValidAndLargeEnough(t *testing.T) {
	sites, err := Sites()
	if err != nil {
		t.Fatalf("Sites: %v", err)
	}
	if len(sites) < 150 {
		t.Fatalf("lightweight sites = %d, want at least 150", len(sites))
	}
	native, err := Builtins()
	if err != nil {
		t.Fatalf("Builtins: %v", err)
	}
	nativeIDs := make(map[string]bool, len(native))
	for _, entry := range native {
		nativeIDs[entry.ID] = true
	}
	seen := make(map[string]bool, len(sites))
	for _, site := range sites {
		if seen[site.ID] {
			t.Errorf("duplicate site id %q", site.ID)
		}
		seen[site.ID] = true
		if nativeIDs[site.ID] {
			t.Errorf("site %q collides with a native definition id", site.ID)
		}
		if site.Name == "" || site.URL == "" || site.Type == "" || site.State == "" {
			t.Errorf("%s: incomplete row: %+v", site.ID, site)
		}
		if site.State == SiteStateVerified && !strings.HasPrefix(site.Evidence, "GET ") {
			t.Errorf("%s: verified without GET evidence: %q", site.ID, site.Evidence)
		}
		if site.State == SiteStateDead && site.Evidence == "" {
			t.Errorf("%s: dead without evidence", site.ID)
		}
		if site.Usage < 0 {
			t.Errorf("%s: negative usage %d", site.ID, site.Usage)
		}
	}
}

func TestSitesCoverManagerFamilies(t *testing.T) {
	sites, err := Sites()
	if err != nil {
		t.Fatalf("Sites: %v", err)
	}
	counts := map[string]int{}
	for _, site := range sites {
		counts[site.Family]++
	}
	for _, family := range []string{FamilyUnit3D, FamilyGazelle, FamilyNexusPHP, FamilyTorrentTrader, FamilyXbtit} {
		if counts[family] == 0 {
			t.Errorf("family %q has no lightweight site", family)
		}
	}
	if counts[FamilyNexusPHP] < 10 {
		t.Errorf("NexusPHP coverage = %d, want the curated set", counts[FamilyNexusPHP])
	}
}

func TestItalianPriorityRows(t *testing.T) {
	sites, err := Sites()
	if err != nil {
		t.Fatalf("Sites: %v", err)
	}
	byID := map[string]Site{}
	for _, site := range sites {
		byID[site.ID] = site
	}
	want := map[string]struct {
		family string
		state  string
	}{
		"jk-arabafenice":     {FamilyXbtit, SiteStateTry},
		"jk-girotorrent":     {FamilyXbtit, SiteStateTry},
		"jk-hdtorrentsit":    {FamilyUnit3D, SiteStateTry},
		"jk-itatorrents":     {FamilyUnit3D, SiteStateTry},
		"jk-shareisland-api": {FamilyUnit3D, SiteStateTry},
		"jk-futuretorrent":   {"", SiteStateTry},
		"jk-trackerzero":     {"", SiteStateTry},
		"mircrew":            {FamilyTorznab, SiteStateTry},
	}
	for id, expected := range want {
		site, ok := byID[id]
		if !ok {
			t.Errorf("Italian priority row %q is missing", id)
			continue
		}
		if !site.Italian {
			t.Errorf("%s: Italian = false", id)
		}
		if site.Family != expected.family {
			t.Errorf("%s: family = %q, want %q", id, site.Family, expected.family)
		}
		if site.State != expected.state {
			t.Errorf("%s: state = %q, want %q", id, site.State, expected.state)
		}
		if site.Type == "private" && site.State == SiteStateVerified {
			t.Errorf("%s: private site marked verified without an account", id)
		}
	}

	entries, err := Builtins()
	if err != nil {
		t.Fatalf("Builtins: %v", err)
	}
	native := map[string]Entry{}
	for _, entry := range entries {
		native[entry.ID] = entry
	}
	if entry := native["corsaro"]; !entry.Italian || entry.Family != FamilyXbtit || entry.State != "verificata" {
		t.Errorf("corsaro metadata = family %q italian %v state %q", entry.Family, entry.Italian, entry.State)
	}
	if entry := native["extto"]; !entry.Italian || entry.Family != FamilyCustom || entry.State != "verificata" {
		t.Errorf("extto metadata = family %q italian %v state %q", entry.Family, entry.Italian, entry.State)
	}
	if entry := native["ildragonegro"]; !entry.Italian || entry.Family != FamilyCustom || entry.State != "bozza" {
		t.Errorf("ildragonegro metadata = family %q italian %v state %q", entry.Family, entry.Italian, entry.State)
	}
}

func TestDeadSitesAreNotProposable(t *testing.T) {
	sites, err := Sites()
	if err != nil {
		t.Fatalf("Sites: %v", err)
	}
	entries, err := Builtins()
	if err != nil {
		t.Fatalf("Builtins: %v", err)
	}
	index := BuildFullIndex(entries, sites)
	dead := 0
	for _, site := range sites {
		if site.State == SiteStateDead {
			dead++
		}
	}
	if dead == 0 {
		t.Fatal("the catalog has no dead rows; the state must be exercised")
	}
	byID := map[string]IndexEntry{}
	for _, entry := range index {
		byID[entry.ID] = entry
	}
	for _, site := range sites {
		if site.State != SiteStateDead {
			continue
		}
		entry, ok := byID[site.ID]
		if !ok {
			t.Errorf("dead site %q missing from the index", site.ID)
			continue
		}
		if entry.Proposable {
			t.Errorf("dead site %q is proposable", site.ID)
		}
		if entry.State != SiteStateDead || entry.Evidence == "" {
			t.Errorf("dead site %q row = %+v", site.ID, entry)
		}
	}
}

func TestIndexExposesItalianAndUsageMetadata(t *testing.T) {
	entries, err := Builtins()
	if err != nil {
		t.Fatalf("Builtins: %v", err)
	}
	sites, err := Sites()
	if err != nil {
		t.Fatalf("Sites: %v", err)
	}
	index := BuildFullIndex(entries, sites)
	italian := 0
	withURL := 0
	for _, entry := range index {
		if entry.Italian {
			italian++
		}
		if entry.URL != "" {
			withURL++
		}
	}
	if italian != 11 {
		t.Errorf("Italian rows = %d, want 11", italian)
	}
	if withURL == 0 {
		t.Error("the index has no site URLs")
	}
	for _, entry := range index {
		if entry.ID == "corsaro" && entry.Usage != 0 {
			t.Errorf("corsaro usage = %d, want 0", entry.Usage)
		}
		if entry.ID == "1337x" && entry.Usage != 1 {
			t.Errorf("1337x usage = %d, want the production signal 1", entry.Usage)
		}
	}
}

func TestDefinitionForSiteBuildsFamilyDefinition(t *testing.T) {
	sites, err := Sites()
	if err != nil {
		t.Fatalf("Sites: %v", err)
	}
	var unit3d *Site
	for i := range sites {
		if sites[i].ID == "jk-hdtorrentsit" {
			unit3d = &sites[i]
			break
		}
	}
	if unit3d == nil {
		t.Fatal("jk-hdtorrentsit is missing")
	}
	body, err := DefinitionForSite(*unit3d)
	if err != nil {
		t.Fatalf("DefinitionForSite: %v", err)
	}
	def, err := definition.Parse(body)
	if err != nil {
		t.Fatalf("synthesized definition does not parse: %v", err)
	}
	if def.Connector != FamilyUnit3D || def.Meta.Type != "private" {
		t.Errorf("definition = connector %q type %q", def.Connector, def.Meta.Type)
	}
	names := map[string]bool{}
	for _, setting := range def.Settings {
		names[setting.Name] = true
	}
	if !names["url"] || !names["apikey"] {
		t.Errorf("unit3d settings = %v", names)
	}

	unknown := Site{ID: "jk-unknown", Name: "Unknown", URL: "https://example.test/", Type: "private", State: SiteStateTry}
	if _, err := DefinitionForSite(unknown); err == nil {
		t.Fatal("DefinitionForSite accepted an unknown family")
	}
	dead := *unit3d
	dead.State = SiteStateDead
	if _, err := DefinitionForSite(dead); err == nil {
		t.Fatal("DefinitionForSite accepted a dead site")
	}
}

func TestSiteStatsAddsNativeAndLightweightRows(t *testing.T) {
	entries, err := Builtins()
	if err != nil {
		t.Fatalf("Builtins: %v", err)
	}
	sites, err := Sites()
	if err != nil {
		t.Fatalf("Sites: %v", err)
	}
	stats := Stats(entries, sites)
	if stats.Total != len(entries)+len(sites) {
		t.Errorf("total = %d", stats.Total)
	}
	if stats.Verified+stats.ToTry+stats.Dead != stats.Total {
		t.Errorf("states do not add up: %+v", stats)
	}
	if stats.PublicVerified < 70 {
		t.Errorf("public verified = %d, want at least 70 including native definitions", stats.PublicVerified)
	}
	if stats.Italian == 0 || stats.Families[FamilyNexusPHP] == 0 {
		t.Errorf("stats = %+v", stats)
	}
}

func TestParseSitesRejectsUnknownFieldsAndStates(t *testing.T) {
	base := `schema: 1
sites:
  - id: ok
    name: Ok
    url: https://example.test/
    type: public
    state: da-provvi
`
	if _, err := parseSites([]byte(base)); err != nil {
		t.Fatalf("valid manifest: %v", err)
	}
	if _, err := parseSites([]byte(strings.Replace(base, "state: da-provvi", "state: verified", 1))); err == nil {
		t.Fatal("accepted an unknown state")
	}
	if _, err := parseSites([]byte(strings.Replace(base, "state: da-provvi", "state: verificata", 1))); err == nil {
		t.Fatal("accepted a verified row without evidence")
	}
	if _, err := parseSites([]byte(strings.Replace(base, "type: public", "type: secret", 1))); err == nil {
		t.Fatal("accepted an unknown type")
	}
	if _, err := parseSites([]byte(strings.Replace(base, "id: ok", "id: ok\n    extra: nope", 1))); err == nil {
		t.Fatal("accepted an unknown field")
	}
}
