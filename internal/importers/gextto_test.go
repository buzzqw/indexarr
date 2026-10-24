package importers

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func TestFromGexttoMapsEverySourceWithoutLeakingRSSKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gextto.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT NOT NULL DEFAULT '')`); err != nil {
		t.Fatal(err)
	}
	urls := []string{
		"https://extto.org/browse/?filter=u%3DEZTV",
		"https://extto.org/browse/?filter=c%3DBooks&filter=tg%3DItalian",
		"https://extto.org/browse/?q=MIRCrew",
		"https://torrentgalaxy.one/get-posts/user:MIRCrewRS/",
		"https://rss.knaben.org/ita///1337x%7Ceztv",
		"https://rss.torrentleech.org/fake-rsskey",
	}
	raw, _ := json.Marshal(urls)
	if _, err := db.Exec(`INSERT INTO settings(key,value) VALUES('url',?)`, string(raw)); err != nil {
		t.Fatal(err)
	}

	targets, warnings, err := FromGextto(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 || len(targets) != len(urls) {
		t.Fatalf("targets=%d warnings=%v", len(targets), warnings)
	}
	byDefinition := map[string]NativeTarget{}
	for _, target := range targets {
		byDefinition[target.Name] = target
	}
	if got := byDefinition["ExtTo – EZTV"].Config["filter"]; got != "u=EZTV" {
		t.Errorf("uploader filter = %q", got)
	}
	if got := byDefinition["ExtTo – Italian Books"].Config; got["filter"] != "c=Books" || got["filter-extra"] != "tg=Italian" {
		t.Errorf("multi filter config = %v", got)
	}
	if got := byDefinition["ExtTo – MIRCrew"].Config; got["query"] != "MIRCrew" || got["filter"] != "c=TV" {
		t.Errorf("fixed query config = %v", got)
	}
	if got := byDefinition["TorrentGalaxy – MIRCrewRS"].Config["uploader"]; got != "MIRCrewRS" {
		t.Errorf("uploader = %q", got)
	}
	tl := byDefinition["TorrentLeech (RSS)"]
	if len(tl.Config) != 0 || !strings.Contains(tl.Secrets["url"], "fake-rsskey") {
		t.Errorf("the RSS key must be a vault secret, got config=%v", tl.Config)
	}
	first := make([]string, len(targets))
	for i, target := range targets {
		first[i] = target.Key()
	}
	again, _, err := FromGextto(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	second := make([]string, len(again))
	for i, target := range again {
		second[i] = target.Key()
	}
	if strings.Join(first, "\n") != strings.Join(second, "\n") {
		t.Error("the import is not idempotent: target keys changed")
	}
}
