package store

import (
	"strings"
	"testing"
)

// TestRepairEscapedMagnetsMigration: a magnet stored with the ampersand escaped
// gets its separators back, the rest of the link stays byte for byte, a literal
// `&amp;` inside a title is not mistaken for a separator, and a row without a
// magnet is untouched.
func TestRepairEscapedMagnetsMigration(t *testing.T) {
	st := newTestStore(t)
	const hash = "0123456789abcdef0123456789abcdef01234567"

	insert := func(key, magnet string) {
		t.Helper()
		if _, err := st.db.Exec(`INSERT INTO releases(dedup_key, title, search_title, magnet,
			size, first_seen_at, last_seen_at) VALUES(?, ?, ?, ?, 1, 1, 1)`,
			key, key, strings.ToLower(key), magnet); err != nil {
			t.Fatalf("insert %s: %v", key, err)
		}
	}
	escaped := "magnet:?xt=urn:btih:" + hash +
		"&amp;dn=The.Apothecary.Diaries.S03E01" +
		"&amp;tr=udp%3A%2F%2Ftracker.example%3A1337%2Fannounce"
	literal := "magnet:?xt=urn:btih:" + hash + "&dn=AC&amp;DC"
	insert("escaped", escaped)
	insert("literal", literal)
	insert("empty", "")

	tx, err := st.db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := repairEscapedMagnets(tx); err != nil {
		tx.Rollback()
		t.Fatalf("repair: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	read := func(key string) string {
		t.Helper()
		var magnet string
		if err := st.db.QueryRow(`SELECT magnet FROM releases WHERE dedup_key = ?`,
			key).Scan(&magnet); err != nil {
			t.Fatalf("read %s: %v", key, err)
		}
		return magnet
	}

	want := "magnet:?xt=urn:btih:" + hash +
		"&dn=The.Apothecary.Diaries.S03E01" +
		"&tr=udp%3A%2F%2Ftracker.example%3A1337%2Fannounce"
	if got := read("escaped"); got != want {
		t.Errorf("repaired magnet = %q, want %q", got, want)
	}
	if got := read("literal"); got != literal {
		t.Errorf("a literal &amp; inside the name was rewritten: %q", got)
	}
	if got := read("empty"); got != "" {
		t.Errorf("an empty magnet was written: %q", got)
	}
}

func TestUnescapeMagnetParams(t *testing.T) {
	cases := []struct{ in, want string }{
		{"magnet:?xt=urn:btih:aa&amp;dn=Name", "magnet:?xt=urn:btih:aa&dn=Name"},
		{"magnet:?xt=urn:btih:aa&amp;tr=http%3A%2F%2Ft", "magnet:?xt=urn:btih:aa&tr=http%3A%2F%2Ft"},
		{"magnet:?xt=urn:btih:aa&dn=AC&amp;DC", "magnet:?xt=urn:btih:aa&dn=AC&amp;DC"},
		{"magnet:?xt=urn:btih:aa&amp;x1=y", "magnet:?xt=urn:btih:aa&amp;x1=y"},
		{"http://example.test/?a=1&amp;b=2", "http://example.test/?a=1&amp;b=2"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := unescapeMagnetParams(tc.in); got != tc.want {
			t.Errorf("unescapeMagnetParams(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
