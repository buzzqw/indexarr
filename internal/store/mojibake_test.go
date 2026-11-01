package store

import (
	"database/sql"
	"net/url"
	"strings"
	"testing"

	"github.com/buzzqw/indexarr/internal/textutil"
)

// TestRepairDoubleEncodedTitlesMigration: a title a site served with the wrong
// charset is repaired together with the search title and the magnet name, the
// rest of the magnet is left byte for byte, a correct title is untouched, and a
// row whose dedup key was built from the title is left alone (repairing it
// would change the key and the release would come back as a new one).
func TestRepairDoubleEncodedTitlesMigration(t *testing.T) {
	st := newTestStore(t)
	broken := "Ð‘Ð¾Ð»ÑŒÑˆÐ¾Ð¹ Ð‘Ð°Ðº Big Buck Bunny (2008) BDRip 1080p"
	magnet := "magnet:?xt=urn:btih:2F24D03EAB998CA672B8C1EF567A184609236C02&dn=" +
		url.QueryEscape(broken) + "&tr=udp%3A%2F%2Ftracker.example%3A1337%2Fannounce"

	insert := func(key, title, search, magnetValue string) {
		t.Helper()
		if _, err := st.db.Exec(`INSERT INTO releases(dedup_key, title, search_title, magnet,
			size, first_seen_at, last_seen_at) VALUES(?, ?, ?, ?, 1, 1, 1)`,
			key, title, search, magnetValue); err != nil {
			t.Fatalf("insert %s: %v", key, err)
		}
	}
	insert("guid:broken", broken, textutil.NormalizeTitle(broken), magnet)
	insert("guid:good", "Большой Бак Big Buck Bunny", textutil.NormalizeTitle("Большой Бак Big Buck Bunny"), "")
	insert("title:keyed", broken, textutil.NormalizeTitle(broken), "")

	tx, err := st.db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := repairDoubleEncodedTitles(tx); err != nil {
		tx.Rollback()
		t.Fatalf("repair: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	read := func(key string) (title, search, magnetValue string) {
		t.Helper()
		if err := st.db.QueryRow(`SELECT title, search_title, magnet FROM releases WHERE dedup_key = ?`,
			key).Scan(&title, &search, &magnetValue); err != nil {
			if err == sql.ErrNoRows {
				t.Fatalf("row %s disappeared", key)
			}
			t.Fatalf("read %s: %v", key, err)
		}
		return title, search, magnetValue
	}

	want := "Большой Бак Big Buck Bunny (2008) BDRip 1080p"
	title, search, magnetValue := read("guid:broken")
	if title != want {
		t.Errorf("title = %q, want %q", title, want)
	}
	if search != textutil.NormalizeTitle(want) {
		t.Errorf("search_title = %q, want %q", search, textutil.NormalizeTitle(want))
	}
	if !strings.HasPrefix(magnetValue, "magnet:?xt=urn:btih:2F24D03EAB998CA672B8C1EF567A184609236C02&dn=") {
		t.Errorf("the magnet lost its infohash: %q", magnetValue)
	}
	if !strings.HasSuffix(magnetValue, "&tr=udp%3A%2F%2Ftracker.example%3A1337%2Fannounce") {
		t.Errorf("the magnet lost its trackers: %q", magnetValue)
	}
	dn := magnetValue[strings.Index(magnetValue, "&dn=")+len("&dn="):]
	dn = dn[:strings.IndexByte(dn, '&')]
	if decoded, err := url.QueryUnescape(dn); err != nil || decoded != want {
		t.Errorf("magnet name = %q (%v), want %q", decoded, err, want)
	}

	if title, _, _ := read("guid:good"); title != "Большой Бак Big Buck Bunny" {
		t.Errorf("a correct title was changed: %q", title)
	}
	if title, _, _ := read("title:keyed"); title != broken {
		t.Errorf("a title-derived key was repaired anyway: %q", title)
	}
}
