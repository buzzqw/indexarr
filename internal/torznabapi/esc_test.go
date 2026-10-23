package torznabapi

import (
	"bytes"
	"encoding/xml"
	"testing"
)

// TestWriteEscMatchesStandardLibrary pins the hand-written escaper to the
// encoding/xml behaviour it replaces: Torznab output must stay byte-identical
// for titles, magnets and provenance names.
func TestWriteEscMatchesStandardLibrary(t *testing.T) {
	cases := []string{
		"",
		"plain title",
		`A & B < C > D "quoted" 'single'`,
		"tabs\tnewlines\nand\rcarriage returns",
		"caffè — 日本語 — 🎬",
		"magnet:?xt=urn:btih:abc&dn=Movie%20(2024)",
		"invalid \xff utf8",
		"\xff\xfe broken",
	}
	for _, text := range cases {
		var want bytes.Buffer
		if err := xml.EscapeText(&want, []byte(text)); err != nil {
			t.Fatalf("EscapeText(%q): %v", text, err)
		}
		var got bytes.Buffer
		writeEsc(&got, text)
		if got.String() != want.String() {
			t.Errorf("writeEsc(%q) = %q, want %q", text, got.String(), want.String())
		}
		if escaped := esc(text); escaped != want.String() {
			t.Errorf("esc(%q) = %q, want %q", text, escaped, want.String())
		}
	}
}

func TestWriteHelpers(t *testing.T) {
	var b bytes.Buffer
	writeInt(&b, 0)
	writeInt(&b, -7)
	writeIntTag(&b, "size", 1234)
	writeTag(&b, "title", "A & B")
	writeAttrInt(&b, "seeders", 9)
	writeAttr(&b, "category", "2040")
	want := `0-7<size>1234</size><title>A &amp; B</title>` +
		`<torznab:attr name="seeders" value="9"/><torznab:attr name="category" value="2040"/>`
	if b.String() != want {
		t.Fatalf("helpers = %q, want %q", b.String(), want)
	}
}
